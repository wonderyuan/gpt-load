#!/usr/bin/env python3
"""Create a private, consistent SQLite snapshot with matching gateway keys/config."""
import datetime
from contextlib import closing
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tempfile

root = Path(sys.argv[1] if len(sys.argv) > 1 else "/opt/gpt-load").resolve()
os.umask(0o077)
with (root / ".maintenance.lock").open("a") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    backups = root / "backups"
    backups.mkdir(mode=0o700, exist_ok=True)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    target = Path(tempfile.mkdtemp(prefix=stamp + "-", dir=backups))
    try:
        data = target / "data"
        data.mkdir(mode=0o700)
        with closing(sqlite3.connect((root / "data/gpt-load.db").as_uri() + "?mode=ro", uri=True, timeout=10)) as source:
            with closing(sqlite3.connect(data / "gpt-load.db")) as dest:
                source.backup(dest, pages=128, sleep=0.05)
                dest.commit()
                dest.execute("PRAGMA wal_checkpoint(TRUNCATE)")
                dest.execute("PRAGMA journal_mode=DELETE")
                if dest.execute("PRAGMA quick_check").fetchone() != ("ok",):
                    raise RuntimeError("SQLite backup quick_check failed")
        for name in ("auth.key", "encryption.key"):
            source = root / "data" / name
            if not source.is_file() or not source.stat().st_size:
                raise RuntimeError("Missing required key file: " + name)
            shutil.copy2(source, data / name)
        for name in ("compose.yaml", ".env"):
            shutil.copy2(root / name, target / name)
        for name in ("owner-access-key.json", "proxy-access.json", "account-sync-state.json"):
            private_config = root / name
            if private_config.exists():
                shutil.copy2(private_config, target / name)
        migration = root / "migrations" / "key-policy-20261028"
        if migration.is_dir():
            saved_migration = target / "key-migration"
            saved_migration.mkdir(mode=0o700)
            for name in ("staged-map.json", "four-complete.json", "release.json", "target-policies-before.json"):
                source = migration / name
                if source.is_file():
                    shutil.copy2(source, saved_migration / name)
        proxy_config = Path("/etc/mihomo/config.yaml")
        if (root / "proxy-access.json").exists() and proxy_config.exists():
            proxy_backup = target / "mihomo"
            proxy_backup.mkdir(mode=0o700)
            shutil.copy2(proxy_config, proxy_backup / "config.yaml")
            providers = proxy_config.parent / "providers"
            if providers.is_dir():
                shutil.copytree(providers, proxy_backup / "providers")
        container = subprocess.check_output(
            ["docker", "compose", "--project-directory", str(root), "ps", "-q", "gpt-load"], text=True
        ).strip()
        if not container:
            raise RuntimeError("Gateway container is not running")
        state = json.loads(subprocess.check_output(["docker", "inspect", container], text=True))[0]
        metadata = {
            "created_at_utc": stamp,
            "configured_image": state["Config"]["Image"],
            "image_id": state["Image"],
            "revision": state["Config"]["Labels"].get("org.opencontainers.image.revision"),
            "source_sha256": state["Config"]["Labels"].get("io.gpt-load.source.sha256"),
            "source_mode": state["Config"]["Labels"].get("io.gpt-load.source.mode"),
            "sqlite": "consistent backup API snapshot; not a live WAL file copy",
        }
        (target / "release.json").write_text(json.dumps(metadata, indent=2) + "\n")
        hashes = []
        for path in sorted(p for p in target.rglob("*") if p.is_file()):
            path.chmod(0o600)
            hashes.append(hashlib.sha256(path.read_bytes()).hexdigest() + "  " + str(path.relative_to(target)))
        (target / "SHA256SUMS").write_text("\n".join(hashes) + "\n")
        (target / "COMPLETE").write_text("ok\n")
        complete = sorted(p for p in backups.iterdir() if p.is_dir() and (p / "COMPLETE").is_file())
        # Retention applies only to this script's successfully completed snapshots.
        for old in complete[:-14]:
            shutil.rmtree(old)
        print(target)
    except BaseException:
        print("Incomplete backup preserved for diagnosis: " + str(target), file=sys.stderr)
        raise
