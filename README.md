<div align="center">

<img src="./web/public/favicon.svg" alt="GPT-Load" width="96">

# GPT-Load

**A self-hosted AI gateway for multi-channel, multi-credential setups**

API keys, subscription accounts, traffic scheduling, failure handling, request logs, and usage accounting — behind a single entry point.

English · [中文](README_CN.md) · [日本語](README_JP.md) | [Official Website](https://www.gpt-load.com)

[![Release](https://img.shields.io/github/v/tag/tbphp/gpt-load?filter=v2.*)](https://github.com/tbphp/gpt-load/releases)
[![Docker](https://img.shields.io/badge/Docker-ghcr.io%2Ftbphp%2Fgpt--load%3A2-2496ED?logo=docker&logoColor=white)](https://github.com/tbphp/gpt-load/pkgs/container/gpt-load)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

<a href="https://trendshift.io/repositories/14880" target="_blank"><img src="https://trendshift.io/api/badge/repositories/14880" alt="tbphp/gpt-load | Trendshift" width="220" height="48"/></a>
<a href="https://hellogithub.com/repository/tbphp/gpt-load" target="_blank"><img src="https://api.hellogithub.com/v1/widgets/recommend.svg?rid=554dc4c46eb14092b9b0c56f1eb9021c&claim_uid=Qlh8vzrWJ0HCneG" alt="Featured｜HelloGitHub" width="220" height="47"/></a>

</div>

---

## 本 Fork 的定位与开发约定（维护者及 AI 接手必读）

这是 `wonderyuan/gpt-load`，上游为 [`tbphp/gpt-load`](https://github.com/tbphp/gpt-load)。本 Fork 服务于维护者及少数同事、朋友的私人 AI 网关，通常 1～3 人并发，统一管理模型订阅、API 凭据和独立 AccessKey；不是面向公众的商业充值平台。生产入口为 `https://ai.yuanundo.top`，实际部署与验收记录见 [`deploy/aliyun/README.md`](deploy/aliyun/README.md)。以下约定是本 Fork 的维护约束，后续 AI 在实施前也应阅读。

### 薄定制、持续跟随上游

- `main` 保持干净的上游基线；`custom` 是长期定制集成与生产发布分支。功能使用短期分支开发，验收后合入 `custom`。升级先同步并检查上游，再将基线 merge 到 `custom`；不对共享/已发布分支反复 rebase 或强推。未配置 upstream remote 时先核实地址，不把 Fork 的 origin 当上游。
- 优先原生配置，其次独立新增模块，最后才修改核心逻辑。部署文件集中在 `deploy/aliyun/`；额度总览是独立展示层，GLM 额度查询是独立渠道适配，两者不要捆绑成全面改版。
- 每项定制应能独立替换或撤掉；按功能拆分小提交，不夹带全仓格式化、依赖升级、改名或无关重构。默认只增强实际使用的界面，不复制整套上游首页，不维护第二套网关核心。
- 复用认证、代理、凭据加密、额度观测、统计、组件和查询机制。不要另写一套代理路由、账号系统或计费系统。上游有同等能力后优先回归上游，移除重复定制。
- 合并冲突保留上游既有行为与本 Fork 必需改动，不整文件选一边；着重复查接口与数据结构语义。没有用户明确要求，不擅自 commit、push、改写历史或触发自动升级。

### 运行、安全与数据口径

- 阿里云 2 核主机内核可见内存约 1.57GiB，同时保留 OpenViking 和通知网关。单 GPT-Load 容器、SQLite、现有 Caddy/HTTPS，不引入 Redis、PostgreSQL、监控平台或额外常驻服务。Go/前端在本机或可信 CI 编译，生产只打包/运行；根据实测保留内存余量，不靠 Swap 或过低 GC 目标强撑。
- 国内 GLM 显式 Direct；ChatGPT/Grok 使用独立认证代理，固定新加坡区域、仅区内故障切换，不添加跨区或 DIRECT 兜底。不改其他服务路由，不暴露应用内部端口和 OAuth 回调端口，不误将容器 localhost 当宿主机。
- 账号官方订阅额度、AccessKey 分配的估算费用预算、本地 Token/费用统计必须分开。按上游真实窗口显示，未知/查询失败/过期数据不能当 0%。用户明确要求的周期费用估算须与同周期观测对齐并标「≈」，不能冒充官方金额；GLM 已知套餐优先固定积分容量，不按本地费用反推，Coding Plan 与普通按量接口不能混淆。
- 复用已保存的额度观测，限制并发和刷新频率；普通打开页面不应对所有账号持续高频请求。上游重置检测为独立后台任务：平时5分钟、已知重置附近/候选确认1分钟串行查询，失败退避；新分组联动默认关闭，仅确认的唯一账号/周期可影响专属Key。通知topic `model.subscription.reset` 使用持久去重，不借通知测试清零真实用量。新增查询不得记录或返回密钥。共享 OAuth refresh token 可能在两个网关间产生轮换冲突，不把一次快照导入当持续同步，不为了验收强制刷新授权。
- 生产 `.env`、登录密钥、OAuth Token、API Key、订阅 URL/节点认证、SQLite 与备份不得提交 Git、写入 README/代码或明文输出。只记录私有文件位置，日志和截图要脱敏。
- 不停止、重启或删除无关服务，不全局 prune，不改他人数据库/卷/密钥。破坏性或扩大范围的操作必须另行取得授权。
- 维护任务依赖 `yuanundo` 的 Codex Key：腾讯云源 Key 21、阿里云目标 Key 10 不参加批量停用或迁移导入。2026-10-28 用户明确撤销“自动额度重置联动排除 yuanundo”的例外：本次新增分组联动开启时，目标 Key 10 与同组专用 Key 一样按确认的上游重置同步；关闭不联动。此授权不包含停用、迁移、重放历史用量或无条件立即重置。操作前按当前身份与分组核对，不仅凭 ID 猜测。

### 验证、发布与交接

- 按维护者 2026-10-28 最新要求，修改和部署后仅对实际改动做最小验证，不默认跑全量测试/回归、make check、全仓 lint/type-check 或与改动无关的真实模型请求。部署产物仍须正常构建；发布时保留一致性备份及必要的健康、认证、权限/状态核对，不用压测耗尽配额或内存。
- 发布固定提交与镜像 digest，不用 latest 作为唯一标识。没有提交授权但需要部署本次代码时，必须明确记录工作区源码指纹和可恢复源码快照，不把旧 HEAD 冒充本次完整代码；默认发布路径仍要求干净提交。
- 激活前制作经校验的 SQLite 一致性备份，并连同 `auth.key`、`encryption.key`、私有配置和旧镜像标识保留。禁止热复制单 DB 忽略 WAL；数据库迁移后回滚必须恢复兼容的数据库与密钥，不能仅切镜像。
- 交付报告区分实测成功、仅配置、尚未验证和失败项，记录运行版本、资源变化、备份位置、回滚方法及遗留风险。文档写当前事实；实现和部署状态变更时更新本节链接的运维记录，不制造大量重复文档。

上游原始介绍与使用文档保留在下面，以便正常跟随上游；Fork 定制规则集中维护本节。

## Sponsors

<sub>[Become a sponsor](mailto:tangb7420@gmail.com)</sub>

<details open>
<summary>Sponsor details (collapsible)</summary>

<a href="https://modelflare.dev/sign-up?utm_source=github&amp;utm_campaign=gptload" target="_blank" rel="sponsored noopener noreferrer"><img src="./screenshot/modelflare-banner-en.png" alt="Modelflare"></a>

Thanks to Modelflare for sponsoring this project! Modelflare: Full strength, stable, nothing watered down. Global SOTA models at a lower cost. Operated by US-based Havenbyte LLC, Modelflare supports GPT, Claude, Gemini, Grok, and other leading models through common compatible protocols, plus 40+ image and video models.

**Cache compensation** is a highlight: selected OpenAI groups (price-first, stable, and premium) guarantee daily cache hit rates of 65%, 75%, or 85%, and if eligible requests fall short, Modelflare refunds the difference, visible on the usage dashboard after settlement.

**Special offers**: a single top-up of US&#36;20 unlocks the GPT discount group at a 0.015x price multiplier, and a single top-up of US&#36;50 unlocks the Claude discount group at a 0.03x multiplier. Register via <a href="https://modelflare.dev/sign-up?utm_source=github&amp;utm_campaign=gptload" target="_blank" rel="sponsored noopener noreferrer">this exclusive link</a> to get started.

---

<table>
<tbody>
<tr>
<td width="180"><a href="https://ofox.ai/?utm_source=github&amp;utm_medium=sponsorship&amp;utm_content=gpt_load" target="_blank" rel="noopener noreferrer"><img src="./screenshot/ofoxai.svg" alt="OfoxAI" width="150"></a></td>
<td><strong>OfoxAI: Text, image, and video AI in one platform</strong><br>OfoxAI is a unified AI API platform bringing together text, image, and video models from multiple providers. With OpenAI-compatible endpoints and native Anthropic and Gemini interfaces, developers can access models for AI applications, agents, and content creation through one platform. <a href="https://ofox.ai/?utm_source=github&amp;utm_medium=sponsorship&amp;utm_content=gpt_load" target="_blank" rel="noopener noreferrer">Explore OfoxAI models and APIs →</a></td>
</tr>
<tr>
<td width="180"><a href="https://www.packyapi.ai/register?aff=ahiS" target="_blank" rel="sponsored noopener noreferrer"><picture><source media="(prefers-color-scheme: dark)" srcset="./screenshot/packycode-dark.png"><source media="(prefers-color-scheme: light)" srcset="./screenshot/packycode-light.png"><img src="./screenshot/packycode-light.png" alt="PackyCode" width="150"></picture></a></td>
<td><strong>PackyCode</strong><br>Access leading AI models through PackyCode with one API endpoint and one API key. Enjoy fast, reliable access with automatic failover and dedicated high-speed routes for Codex and Claude Code. Get started with $1 in free credits, a discount on your first top-up, and savings of up to 80% on eligible routes. Pay in RMB with no currency conversion markups or extra top-up fees. <a href="https://www.packyapi.ai/register?aff=ahiS" target="_blank" rel="sponsored noopener noreferrer">Sign up through the link and start building today.</a></td>
</tr>
<tr>
<td width="180"><a href="https://www.axisnow.io/zh"><img src="./screenshot/axisnow.jpg" alt="AxisNow" width="150"></a></td>
<td>Protect and accelerate websites and APIs, <strong>serving users in mainland China</strong> and around the world, and extend acceleration and security capabilities to native/mobile apps through a client SDK — <strong>self-built private-deployment CDN | subscription-based high-protection CDN | an independently controllable, flexibly composable CDN network.</strong></td>
</tr>
<tr>
<td width="180"><a href="https://go.apimart.ai/gh-gpt-load"><img src="./screenshot/apimart.png" alt="APIMart" width="150"></a></td>
<td>Thanks to APIMart for sponsoring this project! APIMart is a low-cost API platform for AI image &amp; video generation — GPT-Image-2 from $0.006/image, 160+ images per dollar. One async API covers both image and video: submit a task, get an ID, fetch results via polling or callback. Batch tens of thousands of images without timeouts, switch models without changing code. Pay-as-you-go with no monthly fee — <a href="https://go.apimart.ai/gh-gpt-load">sign up here</a> to get started.</td>
</tr>
</tbody>
</table>

</details>

## Why GPT-Load

Your application only needs one base URL and one AccessKey. Providers, accounts, credentials, models, and routing policy are all configured in the management UI.

<img src="./screenshot/architecture-overview.svg" alt="GPT-Load unified access and upstream routing architecture" width="860">

- **One gateway, native protocols** — Manage official APIs, cloud platforms, model services, and compatible relays together while clients keep their OpenAI, Anthropic, or Gemini native interfaces.
- **One mechanism for API keys and subscriptions** — Codex, Claude, Antigravity, Grok, and API-key channels share credential management, scheduling, and health handling.
- **Scheduling and failure isolation built in** — Multi-credential scheduling, configurable weights, retries, cooldown, blacklisting, and session affinity reduce the impact of overloaded or failing credentials.
- **Observable, self-hosted, and simple to deploy** — Inspect health, routes, logs, usage, and cost estimates in an embedded UI backed by SQLite, MySQL, or PostgreSQL with local credential encryption.

## Quick start

> [!WARNING]
> If you are using 1.x, read [Moving from 1.x](#moving-from-1x) first. 2.0 cannot open, import, or migrate 1.x data in place.

### 1. Start the service

Requires Docker and Docker Compose.

```bash
git clone --depth 1 https://github.com/tbphp/gpt-load.git
cd gpt-load

cp .env.example .env
docker compose up -d
```

Confirm the service is up:

```bash
curl --fail http://127.0.0.1:3001/health
```

The first start generates a management key. Read it and store it safely:

```bash
docker compose exec gpt-load sh -c 'cat /app/data/auth.key'
```

Open <http://127.0.0.1:3001> and sign in to the console with that key.

> You can also set `AUTH_KEY` explicitly in `.env` before starting. By default the service listens on the loopback address only and is not exposed to the internet.

### 2. Initial configuration

Initial setup takes three steps:

1. **Add a channel** — Choose an upstream service and add one or more API keys. For subscription channels, complete the OAuth flow or import credentials as prompted.
2. **Create a group** — Pick a channel, then configure available models and runtime policy.
3. **Create an AccessKey** — Set the groups and client protocols it may use, then give the generated AccessKey to your application.

<details>
<summary>OAuth callback ports for subscription channels</summary>

The Codex, Claude, and Antigravity OAuth clients use fixed callback ports. Compose publishes them on the address configured by `HOST`, which defaults to `127.0.0.1`; setting `HOST=0.0.0.0` also publishes these callback ports on all host interfaces. Because the ports are fixed by the upstream clients, only one default Compose instance can run on a host at a time.

When working over SSH or from a remote browser, the browser's `localhost` may not reach GPT-Load — paste the full callback URL into the authorization dialog to finish the flow.

</details>

## Screenshots

**Groups** — View channels, models, credential counts, traffic, and health in one place

<img src="./screenshot/groups-en-US.png" alt="GPT-Load modern groups overview" width="860">

**Usage statistics** — Review request trends, cache hit rate, token categories, and cost estimates

<img src="./screenshot/usage-en-US.png" alt="GPT-Load modern usage statistics" width="860">

## Scope

### Client protocols

| Protocol                | Main entry                                                         |
| ----------------------- | ------------------------------------------------------------------ |
| OpenAI Chat Completions | `POST /v1/chat/completions`                                        |
| OpenAI Responses        | `/v1/responses` and its resource paths                             |
| OpenAI Images           | `POST /v1/images/...`                                              |
| OpenAI Embeddings       | `POST /v1/embeddings`                                              |
| Rerank                  | `POST /v1/rerank`                                                  |
| Mistral native          | `/v1/ocr`, `/v1/audio/...` |
| Anthropic Messages      | `POST /v1/messages`                                                |
| Gemini                  | `/v1beta/models/...`                                               |
| Gemini Embeddings       | `POST /v1beta/models/{model}:embedContent` / `:batchEmbedContents` |

### Built-in channels

- **Official and cloud** — OpenAI, Anthropic, Gemini, xAI, Azure OpenAI, AWS Bedrock, Google Vertex AI
- **Model services** — DeepSeek, Moonshot AI, SiliconFlow, Zhipu AI, Alibaba, Volcengine, OpenRouter, Cline, Groq, Cerebras, Mistral, Nebius, Parasail, Wafer, Hugging Face (Chat), Cohere (text reranking), OpenCode Go, OpenCode Zen
- **Subscription** — Codex, Claude, Antigravity, Grok
- **Custom** — OpenAI Compatible (any compatible relay)

## Deployment and data

Docker Compose uses application-managed SQLite by default. Data lives in the `gpt-load-data` named volume and includes the database, `auth.key`, and `encryption.key`.

> [!IMPORTANT]
> `encryption.key` decrypts channel credentials. When backing up or migrating, the database and the key **must be kept together**. Once the key is lost or replaced, existing encrypted credentials cannot be recovered, and this version does not support master key rotation.

<details>
<summary>Using an external database</summary>

Use the unified `DATABASE_DSN` to connect SQLite, MySQL, or PostgreSQL:

```text
mysql://user:password@db.example:3306/gpt_load?charset=utf8mb4&collation=utf8mb4_bin
postgres://user:password@db.example:5432/gpt_load?sslmode=require
```

</details>

Common operations:

```bash
docker compose logs -f      # view logs
docker compose pull && docker compose up -d   # update to the latest 2.x image
docker compose stop         # stop the service
```

The official Compose file uses `ghcr.io/tbphp/gpt-load:2`. Before GA, `2` tracks verified 2.0 Beta and RC releases; after GA, it tracks stable 2.x releases only. Exact image tags omit the Git tag's `v` prefix (for example, `2.0.0-beta.25`), while `2.0-beta` remains the 2.0 Beta channel. `latest` remains on 1.x.

<details>
<summary>Using a native binary</summary>

Download the build for your platform from [GitHub Releases](https://github.com/tbphp/gpt-load/releases), and verify it against the bundled `SHA256SUMS` first:

```bash
chmod +x ./gpt-load-linux-amd64

HOST=127.0.0.1 DATA_DIR=./data ./gpt-load-linux-amd64
```

Then open <http://127.0.0.1:3001>. Portable builds are provided for five targets across Linux, macOS (amd64 / arm64), and Windows; `gpt-load-windows-amd64.exe` keeps running in the foreground as before.

Windows desktop users can instead download `gpt-load-windows-setup.exe`. After one administrator approval, Setup installs and starts a low-privilege Windows service, enables automatic startup, and creates desktop and Start Menu shortcuts to the GPT-Load management page. Setup displays the generated management key before it finishes; save it before closing the page. The protected copy remains at `%ProgramData%\GPT-Load\data\auth.key`. Service configuration and its `.env` live in `%ProgramData%\GPT-Load`, with persistent data in `%ProgramData%\GPT-Load\data`.

Installing a newer Setup stops the service gracefully before updating it. Windows uninstall removes the program and service but preserves data. Advanced users can still manage an installed service with `gpt-load-windows-amd64.exe service start|stop|restart|status`.

</details>

### Environment configuration

At startup, the application reads `.env` in the current directory; existing process environment variables take precedence. Unless noted otherwise, changes require restarting the process or container; see [`.env.example`](.env.example) for the common configuration template.

<details>
<summary>Show all environment variables</summary>

| Variable | Default | Description |
| --- | --- | --- |
| `HOST` | `127.0.0.1` | Native listening address, and the default host address for Compose's main port and OAuth callback ports; Compose always listens on `0.0.0.0` inside the container. |
| `PORT` | `3001` | HTTP service port, must be `1–65535`; Compose also uses it for the container port, host publishing, and health check. |
| `BIND_ADDRESS` | Empty, inherits `HOST` | Compose only; overrides the host publishing address for the main service port without changing OAuth callback ports. |
| `OAUTH_CALLBACK_BIND_ADDRESS` | Empty, inherits `HOST` | Compose only; overrides the host publishing address for the fixed OAuth callback ports `1455`, `54545`, and `51121`. |
| `GRACEFUL_SHUTDOWN_TIMEOUT` | `10` | Maximum time to wait for requests after a stop signal, positive integer in seconds. |
| `CONTAINER_STOP_GRACE_PERIOD` | `15s` | Docker duration to wait before Compose force-stops the container; should be longer than `GRACEFUL_SHUTDOWN_TIMEOUT`. |
| `READ_TIMEOUT` | `60` | HTTP request read timeout, positive integer in seconds. |
| `IDLE_TIMEOUT` | `120` | HTTP keep-alive idle connection timeout, positive integer in seconds. |
| `DATA_DIR` | `./data` | Directory for the managed database, `auth.key`, `encryption.key`, and runtime state; official Compose uses `/app/data`, while the Windows Setup service uses `%ProgramData%\GPT-Load\data`. |
| `DATABASE_DSN` | Empty, uses `${DATA_DIR}/gpt-load.db` | Empty uses application-managed SQLite; non-empty values support SQLite paths or URLs, MySQL URLs, and PostgreSQL URLs, and are treated as operator-managed external databases. Container file paths must be inside a mounted directory. |
| `DATABASE_MAX_OPEN_CONNECTIONS` | `10` | Maximum open connections for MySQL and PostgreSQL, positive integer. SQLite always uses one connection. |
| `DATABASE_MAX_IDLE_CONNECTIONS` | `5` | Maximum idle connections for MySQL and PostgreSQL, positive integer and no greater than `DATABASE_MAX_OPEN_CONNECTIONS`. SQLite always uses one connection. |
| `AUTH_KEY` | Empty, reads or generates `${DATA_DIR}/auth.key` | Bearer key for the management UI and `/api` management API, not a data-plane AccessKey. |
| `ENCRYPTION_KEY` | Empty, reads or generates `${DATA_DIR}/encryption.key` | Encrypts channel credentials; changing or losing it makes existing credentials undecryptable, so back it up with the database. |
| `CLIENT_IP_HEADER` | Empty, uses the connection IP | Client IP header, such as `X-Forwarded-For` or `CF-Connecting-IP`; missing or invalid values fall back to the connection IP. Shared by logs, AccessKey IP restrictions, and other IP consumers; supports IPv4/IPv6. Restart after changes. |
| `TRUSTED_PROXIES` | Empty | Optional comma-separated proxy IPs or CIDRs; applies only with `CLIENT_IP_HEADER`. Empty means trusting the selected header directly, with trust enforced by your deployment. When set, only matching connection peers may supply the header; others use their connection IP. For `X-Forwarded-For`, scan right to left for the first untrusted IP (leftmost if all are trusted); without a list, use the leftmost IP. Other headers accept one IP only. Restart after changes. |
| `HTTP_PROXY` | Empty | Environment proxy for HTTP upstream requests. |
| `HTTPS_PROXY` | Empty | Environment proxy for HTTPS upstream requests. |
| `NO_PROXY` | Empty | Comma-separated hosts, domains, or IPs that bypass the environment proxy. |
| `LOG_LEVEL` | `info` | Supports `panic`, `fatal`, `error`, `warn`, `warning`, `info`, `debug`, and `trace`; invalid values warn and fall back to `info`. |
| `LOG_FORMAT` | `text` | Supports `text` and `json`; any other value fails startup. |
| `MODELS_DEV_AUTO_SYNC_ENABLED` | Unset, initial default `true` | When unset, uses the persisted management UI setting; when set, forces Models.dev auto-sync on or off and makes the same UI option read-only. |

Environment proxies apply only when no proxy is specified on the credential, group, or global settings.

</details>

## Moving from 1.x

> [!WARNING]
> GPT-Load 2.0 is a complete rewrite. It **cannot** open, import, or migrate 1.x data in place.

Deploy 2.0 with its own database, `DATA_DIR`, port, and Docker volume. Cut traffic over only after verification, and keep the original 1.x deployment until the rollback window closes. Documentation for the 1.4.x maintenance line is at the [official docs](https://www.gpt-load.com/docs).

## Open-source dependencies

Some of GPT-Load's capabilities build on these projects, with thanks:

| Project                                                     | Role                                                                                 | License    |
| ----------------------------------------------------------- | ------------------------------------------------------------------------------------ | ---------- |
| [Bifrost Core](https://github.com/maximhq/bifrost)          | Provider authentication, request/response conversion, streaming, usage normalization | Apache-2.0 |
| [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) | OAuth and execution adapter for subscription channels                                | MIT        |
| [Lobe Icons](https://github.com/lobehub/lobe-icons)         | Channel brand icons in the management UI                                             | MIT        |

GPT-Load owns credential storage, account selection, scheduling, retry, health, affinity, logging, and usage policy. Third-party notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md), full license texts in [`LICENSES/`](LICENSES/), and each release ships a CycloneDX SBOM covering the Go dependency graph.

Channel icons identify their respective upstream providers. All trademarks belong to their owners; this project is not affiliated with or endorsed by them.

## Project support

<table>
<tbody>
<tr>
<td align="center" width="33%">
<a href="https://openai.com/">
<picture>
<source media="(prefers-color-scheme: dark)" srcset="./screenshot/sponsor-openai-lockup-white.svg">
<source media="(prefers-color-scheme: light)" srcset="./screenshot/sponsor-openai-lockup-black.svg">
<img src="./screenshot/sponsor-openai-lockup-black.svg" alt="OpenAI" width="120">
</picture>
</a>
<br><sub>Platform support</sub>
</td>
<td align="center" width="33%">
<a href="https://linux.do"><img src="./screenshot/l.png" alt="LINUX DO" width="120"></a>
<br><sub>Community support</sub>
</td>
<td align="center" width="33%">
<a href="https://www.digitalocean.com/?refcode=3d52cff21342&utm_campaign=Referral_Invite&utm_medium=Referral_Program&utm_source=badge"><img src="https://web-platforms.sfo2.cdn.digitaloceanspaces.com/WWW/Badge%202.svg" alt="DigitalOcean" width="120"></a>
<br><sub>Infrastructure support</sub>
</td>
</tr>
</tbody>
</table>

---

[MIT License](LICENSE) · [Third-party notices](THIRD_PARTY_NOTICES.md) · [Security policy](SECURITY.md)
