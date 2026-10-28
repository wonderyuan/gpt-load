#!/usr/bin/env bash
set -euo pipefail

host=${1:-aliyun}
mode=${2:-}
[[ -z "$mode" || "$mode" == --snapshot ]] || { printf 'Usage: %s [host] [--snapshot]\n' "$0" >&2; exit 1; }
root=$(git rev-parse --show-toplevel)
cd "$root"
for tool in go node npx crane python3 ssh scp; do
  command -v "$tool" >/dev/null || { printf 'Missing tool: %s\n' "$tool" >&2; exit 1; }
done
if [[ "$mode" != --snapshot ]]; then
  git diff --quiet HEAD -- '*.go' go.mod go.sum third_party web internal Dockerfile LICENSE LICENSES THIRD_PARTY_NOTICES.md || {
    printf 'Commit application changes or explicitly use --snapshot for a recorded working-tree release.\n' >&2
    exit 1
  }
  if [[ -n $(git ls-files --others --exclude-standard -- '*.go' third_party web internal) ]]; then
    printf 'Untracked application source requires a commit or explicit --snapshot.\n' >&2
    exit 1
  fi
fi
sha=$(git rev-parse HEAD)
mkdir -p build
out=$(mktemp -d "$root/build/aliyun-release.XXXXXX")
mkdir -p "$out/context/release" "$out/context/LICENSES" "$out/docker-config" "$out/source"
python3 - "$out" <<'PY'
from pathlib import Path
import hashlib,io,json,os,subprocess,sys,tarfile
out=Path(sys.argv[1]);digest=hashlib.sha256()
names=subprocess.check_output(['git','ls-files','-z','--cached','--others','--exclude-standard']).split(b'\0')
with tarfile.open(out/'source.tar.gz','w:gz',format=tarfile.PAX_FORMAT) as archive:
    for name in sorted(set(n for n in names if n)):
        path=Path(os.fsdecode(name))
        if not path.exists() and not path.is_symlink():
            continue
        if path.is_symlink():
            raise SystemExit('Source snapshot refuses symlinks: '+str(path))
        if not path.is_file():
            continue
        parts=path.parts
        if any(p in ('data','backups','secrets','node_modules','.git','build','__pycache__') for p in parts) or path.suffix in ('.key','.pem','.db','.sqlite') or path.name.startswith('.env') and path.name!='.env.example':
            raise SystemExit('Private/runtime file is not allowed in a source snapshot: '+str(path))
        data=path.read_bytes();mode=0o755 if os.access(path,os.X_OK) else 0o644
        digest.update(name+b'\0'+str(mode).encode()+b'\0'+hashlib.sha256(data).digest())
        info=tarfile.TarInfo(str(path));info.size=len(data);info.mode=mode;info.mtime=0
        archive.addfile(info,io.BytesIO(data))
        target=out/'source'/path;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(data);target.chmod(mode)
(out/'source.sha256').write_text(digest.hexdigest()+'\n')
PY
source_sha=$(<"$out/source.sha256")
release_id=$sha
version="2.0.0-fork-${sha:0:12}"
if [[ "$mode" == --snapshot ]]; then
  release_id="${sha}-snapshot-${source_sha:0:12}"
  version="2.0.0-fork-${sha:0:8}-snapshot-${source_sha:0:12}"
fi
cd "$out/source"
npx --yes pnpm@11.17.0 --dir web install --frozen-lockfile
npx --yes pnpm@11.17.0 --dir web exec vite build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOMAXPROCS=4 go build -p 4 -trimpath \
  -ldflags "-s -w -X gpt-load/internal/platform/version.Version=$version" \
  -o "$out/context/release/gpt-load-linux-amd64" .
base=$(python3 - <<'PY'
from pathlib import Path
import re
text=Path('Dockerfile').read_text()
print(re.search(r'^FROM (\S+) AS runtime$',text,re.M).group(1))
PY
)
DOCKER_CONFIG="$out/docker-config" crane pull --platform linux/amd64 "$base" "$out/runtime-base.tar"
cp LICENSE THIRD_PARTY_NOTICES.md "$out/context/"
cp LICENSES/*.txt "$out/context/LICENSES/"
python3 - "$out" "$sha" "$base" "$source_sha" "$mode" <<'PY'
from pathlib import Path
import hashlib,json,re,sys
out=Path(sys.argv[1]);sha=sys.argv[2];base=sys.argv[3];source_sha=sys.argv[4];mode=sys.argv[5]
text=Path('Dockerfile').read_text()
runtime=text[text.index('FROM '+base+' AS runtime'):text.index('FROM runtime AS source-build')]
base_tag='gpt-load-runtime-base:'+base.split('@sha256:')[1][:16]
runtime=runtime.replace('FROM '+base+' AS runtime','FROM '+base_tag+' AS runtime',1)
binary=out/'context/release/gpt-load-linux-amd64'
digest=hashlib.sha256(binary.read_bytes()).hexdigest()
runtime+='\nLABEL org.opencontainers.image.source="https://github.com/wonderyuan/gpt-load" \\\n      org.opencontainers.image.revision="'+sha+'" \\\n      io.gpt-load.binary.sha256="'+digest+'"\n'
runtime+='\nLABEL io.gpt-load.source.sha256="'+source_sha+'" io.gpt-load.source.mode="'+('snapshot' if mode else 'commit')+'"\n'
(out/'context/Dockerfile').write_text(runtime)
(out/'context/SHA256SUMS').write_text(digest+'  release/gpt-load-linux-amd64\n')
(out/'release.json').write_text(json.dumps({'revision':sha,'source_sha256':source_sha,'source_mode':'snapshot' if mode else 'commit','binary_sha256':digest,'base':base,'base_tag':base_tag},indent=2)+'\n')
PY
COPYFILE_DISABLE=1 tar -czf "$out/context.tar.gz" -C "$out/context" .
build_id=$(date -u +%Y%m%dT%H%M%SZ)
remote="/opt/gpt-load/releases/${release_id}-${build_id}"
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$host" "install -d -m 0700 '$remote'"
scp -o BatchMode=yes -o StrictHostKeyChecking=yes "$out/runtime-base.tar" "$out/context.tar.gz" "$out/release.json" "$out/source.tar.gz" "$out/source.sha256" "$host:$remote/"
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$host" bash -s -- "$remote" "$release_id" "$build_id" <<'REMOTE'
set -euo pipefail
remote=$1
sha=$2
build_id=$3
image="wonderyuan/gpt-load:sha-${sha}-${build_id}"
if docker image inspect "$image" >/dev/null 2>&1; then
  printf 'Refusing to overwrite an existing release tag: %s\n' "$image" >&2
  exit 1
fi
cd "$remote"
docker load --input runtime-base.tar
python3 - <<'PY'
import json,subprocess,tarfile
with tarfile.open('runtime-base.tar') as archive:
    tag=json.load(archive.extractfile('manifest.json'))[0]['RepoTags'][0]
base_tag=json.load(open('release.json'))['base_tag']
subprocess.run(['docker','tag',tag,base_tag],check=True)
PY
mkdir context
tar -xzf context.tar.gz -C context
cd context
sha256sum -c SHA256SUMS
docker build --pull=false --network host --build-arg TARGETARCH=amd64 \
  --target prebuilt -t "$image" .
docker image inspect "$image" \
  --format '{{json .RepoDigests}} revision={{index .Config.Labels "org.opencontainers.image.revision"}} source={{index .Config.Labels "io.gpt-load.source.sha256"}}'
REMOTE
printf '\nBuilt but NOT activated. Artifacts: %s\n' "$out"
printf 'Remote release: %s\nBack up first, then change /opt/gpt-load/.env to the printed immutable digest.\n' "$remote"
