# 阿里云 GPT-Load 私人网关

## 实际状态（2026-10-28）

**已部署并开通公网 HTTPS，已从腾讯云 Sub2API 同步三个正常账号，分别建组并通过真实最小请求；ChatGPT/Grok 固定新加坡区内故障切换，智谱直连。**

公网后台：`https://ai.yuanundo.top/`；未登录自动进入 `https://ai.yuanundo.top/login?redirect=/`，登录后首页路径就是 `/`。无需本地端口或 SSH 隧道。

- 服务器：`ssh aliyun`，39.108.215.240，阿里云深圳，Ubuntu 24.04.5，x86_64，2 vCPU，内核可见内存 1612MiB、Swap 2048MiB。
- 用户明确授权后，已停止 travel-radar、wedding-journal、homepage；没有删除容器、镜像、数据库或配置。travel-radar 在 30 秒停止超时后退出 137，OOMKilled=false；另外两项退出 0/143。OpenViking、notification-gateway、Caddy、Mihomo 未停止或重启。
- 运行目录 `/opt/gpt-load`；单个非 root 容器，SQLite，数据 `/opt/gpt-load/data`。仅发布 `127.0.0.1:3100`，不公开 OAuth 回调或语音端口。
- Fork：`github.com/wonderyuan/gpt-load`；上游 `github.com/tbphp/gpt-load`。本地无 upstream remote，未擅自添加。
- 应用 SHA：`6548ee0ed4cbfccc2147a48f18a7c802505ac1ba`，对应上游 `v2.0.0-rc.56`，属于 2.x **预发布**版。部署前应用源码未修改。
- 当前固定镜像：`wonderyuan/gpt-load@sha256:884a419ea01e28dc5b33f4c791aaf9900fad3e7466449d7cc3f0f0fa42811103`，版本 `2.0.0-fork-6548ee0e-snapshot-b5db6886c3bd`，源码指纹 `b5db6886c3bd8617f50c8257516205a7ae94889bff07b96db54ce2982a4ff9c7`。未授权 Git 提交，base HEAD 仍 6548ee0e；普通密钥完整只读总览于 2026-10-28 19:37 CST 激活，基于上一模型资讯版本叠加本轮权限/展示白名单增量，重置卡、密钥周期重置和模型资讯保留。发布前 `7da106c7…` 保留作兼容回滚点。
- 上一额度总览版本 `wonderyuan/gpt-load@sha256:70d698f3cb56ffda361107dcb2f0f7f2179444b899d968bc83aadd2b32031f1f` 保留；退回旧版前必须先关闭新增credit模式，不能把启用该模式的数据直接给旧二进制启动。
- 上一稳定镜像 `wonderyuan/gpt-load@sha256:f57b213545b9c02a09b32dfab1bb40becdc2a7eec44526151fc7dfc81c4cd481` 保留，可配套本次发布前备份回滚。
- 已通过更新/回滚演练的另一镜像：`wonderyuan/gpt-load@sha256:43ce468637eed921580e394986eb6fe2c28fcf751b09d76175023d7bf366183c`，独立 tag `sha-6548ee0ed4cb-prebuilt-20261027T080625Z`。两者应用 SHA 相同，当前已切回原始程序基线。
- 首次程序 SHA256：`865f459c5c826b35cbb8ffe2af6e37d42bf485d230f1934e6e51b0619e67d0a3`。含本机成功构建的前端，linux/amd64 静态 Go 二进制；不是直接使用上游镜像。
- 维护者于 2026-10-28 明确授权将全部定制代码提交整合到 `custom`，仅保留 `main` 与 `custom`；`main` 保持上游基线，临时功能/部署分支完成整合后清理。本轮授权不包含推送远端。根 README 是项目背景和 AI 接手开发规则的唯一主入口，后续仍遵循薄定制边界。

### 资源与容量

| 时点 | MemAvailable | Swap 已用 | 说明 |
| --- | --- | --- | --- |
| 停三个服务之前 | 604MiB | 967MiB | 全部五个旧业务仍运行 |
| 停止后 | 811MiB | 711MiB | 随后短时波动约 750–795MiB |
| GPT-Load 验收后、无打包任务时 | 约 763–808MiB | 约 710–711MiB | 只有保留的两项业务及新网关 |

当前应用限制：**256MiB RAM、不额外使用 Swap、1 CPU、128 PID**，`GOMEMLIMIT=192MiB`、`GOMAXPROCS=1`，日志 5MiB × 3，30 秒健康检查，unless-stopped。空载 Docker 工作集约 **20–26MiB**；模拟三路流及 65 秒长流的 cgroup 内存峰值约 **55.6MiB**，应用无 OOM、重启计数 0、cpu.stat 无限流。重启会重置部分 cgroup 峰值，因此这是本次有记录的峰值，不是长期保证。

测试期间 CPU 以 90–98% idle 为主，没有持续双向换页，Swap 不因测试持续增长。不能据此保证真实 Codex 大上下文、附件及 OAuth 后台任务只占几十 MiB；遇到接近 256MiB、OOM 或 GC/流式延迟问题，先停新增负载评估 384MiB 或扩容，不能牺牲现有业务。

最终 16:19 CST 采样：MemAvailable **738MiB**、Swap 已用 **710MiB**、负载 **0.15/0.15/0.10**；应用 cgroup 当前约 **26MiB**，本轮重建后峰值约 31MiB，无 OOM、换出或 CPU 限流。此前 55.6MiB 流式验收峰值仍作为本次容量依据，不用重建后的较低数值替代。

根盘约 40GiB，最终 df 已用约 **20GiB**、可用 **18GiB**（54%）。运行镜像 Docker 报告约 **171MB**（包含存储口径开销），最终数据约 **460KiB**、备份约 **3.5MiB**、交付目录约 **286MiB**；发布演练额外保留了一份交付包和镜像，不作全局清理。原先 Docker 缓存、其他镜像均未清理。

## 账号额度薄定制发布（2026-10-27）

根 README 的 Fork 规则是后续开发/AI 接手入口：保留 `main` 上游基线，长期集成为 `custom`，原部署准备分支保留；源码未 commit/push。本次无需数据库迁移、无新增运行依赖，经典界面未改。

- 新版首页状态条后新增独立“账号额度概览”，显示全部凭据（分页，每页100条），不受上游原活跃Top4/最近24小时过滤。原首页订阅卡片仍保留；列表行代表分组凭据成员，不跨账号加总百分比。
- 每个账号可直接看遮罩、分组、可用状态、上游各窗口剩余/已用、重置倒计时和最近同步；分组列表新增5h/7d摘要。只有手动单账号/本页同步访问上游，本页串行；普通页面刷新只GET存储快照，未知、过期、失败不当0。
- GLM 使用独立 native API-key observer：只识别官方CN Coding Plan目标，固定官方monitor quota接口、raw Authorization、既有effective proxy policy、10秒超时/1MiB上限/不跟随重定向；不伪装成OAuth，不改变计费和API-key认证。接入既有观察持久化，失败保留历史并标错误，target变更清旧数据。
- 线上GLM实测5h已用17%/剩余83%、7d已用64%/剩余36%，重置时间正确。ChatGPT只返回7d窗口时按实展示，不伪造5h；Grok含weekly与credits窗口，和AccessKey费用额度分开。
- 新API `GET /api/fork/account-quotas` 仅管理员、no-store、mask-only，不返回原文email/Token。单元测试涵盖分页/权限/缺值/过期/真实DTO形状。第一次公网验收发现完整凭据parser错误依赖总览未返回的proxy字段，已改为独立最小投影并补真实结构回归；错误快照未作为最终交付。
- 测试：Go `./...` 全通过（本机缺Compose导致首轮容器契约测试失败，经官方校验和验证的隔离Compose补齐后重跑全过）；前端完整lint/style、类型检查、构建通过；Node额度回归14/14。浏览器实测桌面/390px首页及分组摘要，手动同步本页成功，GLM/ChatGPT/Grok新鲜数据均可读。
- 部署后各做一次最小模型请求均返回OK，ChatGPT Responses SSE完成；Grok上游reasoning输出未严格遵守请求的max_tokens，实际completion289，因此没有继续生成压测。
- 源码包保存在服务器 `/opt/gpt-load/releases/6548ee0ed4cbfccc2147a48f18a7c802505ac1ba-snapshot-104036a9aee6-20261027T105158Z/`（source.tar.gz、source.sha256、release.json），本机 `build/aliyun-release.PNmpJE/` 有相同快照及二进制。后续只更新文档不会改变已编译应用内容；下一次发布重新计算指纹。
- 原稳定版本配套发布前备份 `/opt/gpt-load/backups/20261027T104402Z-5gz0pr_n`；最终修复激活前备份 `/opt/gpt-load/backups/20261027T105307Z-qwvtxhcu`。回滚使用前述旧稳定digest及经校验配套备份，保留管理员AUTH_KEY override，禁止直接覆盖/删除现有数据。
- 更新后一次采样应用cgroup约45MiB、峰值84MiB/256MiB、无OOM/换出/CPU限流；宿主MemAvailable约750–863MiB，Swap约704–724MiB。短时vmstat无持续换出，保留服务HTTPS状态不变。SQLite日志/凭据/代理及key均持久保存。

## 已验证与未验证

**实际通过：**

- 容器启动、health、管理员 Bearer 认证；未认证管理 API 和 `/v1/models` 返回 401。
- 全局并发 4，每 Key 默认 2、每分组默认 3，日志保留 7 天，全局 Direct；关闭语音与 Models.dev 自动同步，保留 Responses WebSocket 能力。
- 临时本地模拟上游的 Group、三个独立且限分组的 AccessKey 创建与删除。
- 普通 Chat Completions，三路并发 SSE，经隔离的本地 Caddy 实例首段约 8–12ms、持续 6 秒；Responses SSE 转换首段约 149ms；65 秒 SSE 正常结束。每 Key 第三个同时请求返回 429。
- 测试 Key 未出现在容器日志中；管理员密钥也未出现在检查的日志中。请求正文记录关闭。
- 只重启 GPT-Load 后，数据库、管理员 key、并发设置保留。
- Python SQLite Backup API 一致性快照、校验和；隔离恢复容器成功使用备份的 auth/key、设置及 owner 元数据。测试容器已停止删除，测试数据留在私有 releases 目录。
- 本机配置、内存、代理、SQLite、客户端 IP 单测及 gateway WebSocket/Stream 相关回归通过。**不是公开域名 WebSocket 端到端验收。**
- 保留的 OpenViking/通知网关仍运行，原 HTTPS 入口返回预期 401/200、TLS 验证成功。

**未完成：** 当前备案/阿里云接入权威确认、独立 OAuth 授权及自然到期后的 Token 刷新长期验证、真实大上下文负载。HTTPS 成功不代表合规核验。测试 Group/Keys 全部已删除，保留的三个分组都使用用户明确授权同步的真实账号。

项目自动创建的 Default Key 仍禁用。`owner` Key 已启用，仅授权分组 **3/4/5**，并发2、请求正文不记录；凭据私有保存于 `/opt/gpt-load/owner-access-key.json`，不写入仓库或报告。源码要求创建 Group 时提供有效 API 凭据或已完成 OAuth 的 staged credential，不能创建无凭据假分组。

## 当前访问方式

直接打开 `https://ai.yuanundo.top/`，未登录时由应用转到 `/login?redirect=/`。也可直接打开 `/login`、`/groups`、`/access-keys`、`/settings`、`/monitor/usage` 等项目已有页面路径；需要登录的页面由原生认证保护，无额外网页认证阻挡模型 API。

公网域名已在浏览器实际显示登录表单，管理员鉴权通过同一 HTTPS 域名的 API 验证。旧的本地 SSH 隧道仅可用于诊断，不再是用户日常访问方式。

管理员登录密钥已按用户要求自定义，通过服务器权限0600的 `/opt/gpt-load/.env` 中 `AUTH_KEY` 注入，优先于原 `data/auth.key`。原文件按项目要求保留64位hex，不改为短密码；原管理员密钥经HTTPS验证已失效。恢复/回滚必须保留配套 `.env`，不能误删覆盖使旧密钥重新生效。`encryption.key` 与数据库配套保管，未随管理员密钥修改，不放进 Git、聊天或日志。AccessKey 不是管理员 key，不要将管理员 key 配置给模型客户端。

## DNS、Caddy 与 HTTPS

`ai.yuanundo.top` 已解析至 `39.108.215.240`，使用 AliDNS / 万网，无 AAAA；本次没有修改 DNS。2026-10-27 16:24 CST，用户再次明确要求公网 HTTPS 访问后，备份并只追加本站独立块，校验成功后 graceful reload Caddy，原有站点内容完整保留。

- 变更前备份：`/opt/gpt-load/releases/caddy-backups/Caddyfile-before-ai-20261027T082412Z`（目录0700、文件0600）。
- 原 Caddyfile SHA256：`aa9617ffa0d4c2cbfc18818b8e1710d5f4116b804979d07f1548fcec271da432`。
- 现 SHA256：`920cfb174fcb5cbd036e0477e9e4b093fb50e88dfaa37c7ae7e999251a8d3532`，变更记录 `/opt/gpt-load/releases/https-site.json`。
- Let's Encrypt 正式证书已签发，CN=ai.yuanundo.top，issuer=YE2，至 **2027-01-25 07:25:52 UTC**，由现有 Caddy 自动续期。TLSv1.3 和系统证书链校验通过；HTTP 返回 308 至同路径 HTTPS。
- 公网 `/`、`/login`、`/groups`、`/monitor/usage` 返回前端页面，浏览器确认 `/login?redirect=/` 正常显示；`/health` 返回200，无认证 `/api/auth/session` 和 `/v1/models` 返回401，正确管理员 key 经 HTTPS 返回200。
- OpenViking 和通知网关既有域名 TLS 与401/200行为不变，内部应用端口仍仅绑定 loopback。

历史上下文有用户曾确认 yuanundo.top 已备案，但本次工信部页面访问失败，**当前备案和阿里云接入状态仍未完成权威核验，不能以 HTTPS 成功代替合规证明**。这是遗留事项，不宣称已核验。

已应用的 `Caddyfile` 保留 `/v1` 和 Authorization，禁用代理响应缓冲，复用 Caddy 自动 HTTPS/HTTP 跳转及 WebSocket Upgrade，不设置截断长响应的全局超时。HTTP 请求体上限 32MB，超过会413；并非 WebSocket 消息大小限制。当前 compose 只信任专用网桥网关 `172.30.87.1/32` 的 X-Forwarded-For；公网鉴权日志观察到真实公网客户端 IP，伪造 XFF 不作为已信任客户端地址。

可用后台 `https://ai.yuanundo.top/`；模型 Base URL `https://ai.yuanundo.top/v1`；`POST /v1/responses`、`POST /v1/chat/completions`，Responses WebSocket 为 `GET /v1/responses` Upgrade。`/api` 是管理接口，不是模型 Base URL；仍需真实上游授权和可用 Key 才能调用模型。

## 国内直连与海外代理

全局显式 Direct，标准代理环境变量清空。原生优先级仍为 **Credential > Group > Global > Environment > Direct**；没有改宿主全局 VPN/路由。

### 2026-10-27 腾讯云账号同步

源为已有 SSH 别名 `tcc`（43.160.210.22）的 Sub2API，镜像 `716b68ea2dcd3e9fcd73afed57c940808fd787c9`。只读取得账号，经双 SSH 管道直接送到目标，未在本机落盘或打印 Token。没有修改源账号、数据库或服务；源容器和 health 保持正常。

| 源账号 | 目标组 / ID | 凭据与网络 | 验证 |
| --- | --- | --- | --- |
| ChatGPT plus / 2 | ChatGPT plus / 3 | Codex OAuth，Custom 新加坡代理 | 实时发现15模型，gpt-6.1-sol Responses SSE返回OK，约3.6秒 |
| Grok / 5 | Grok / 4 | Grok OAuth，同一海外代理 | 实时发现21模型，grok-4.7返回OK，约6.1秒 |
| 智谱glm / 6 | 智谱glm / 5 | zhipuai API Key，coding URL，显式Direct | 实时发现11模型，glm-5.3-flash返回OK，约6.6秒 |
| codex12 / 3 | 未导入/未启用 | 源账号已报Token revoked 401且不可调度 | 不能通过复制凭据修复，需要重新授权 |

智谱保留源 coding 地址 `https://open.bigmodel.cn/api/coding/paas/v4`，不误用普通按量端点。实际模型按管理界面当前选择启用，不强行覆盖用户选择；首次测试旧模型名返回本地503，使用已启用模型后通过。初次智谱输出上限16全用于reasoning、可见正文为空，提高到64后以27总token返回OK。Grok测试报告含上游系统提示消耗约1302总token，未继续大量测试。

OAuth导入使用官方credential stage与建组API，导入/模型发现/真实请求均未观察到主动refresh（last_refresh_at为空）。**这是一轮快照同步，不是两个网关之间的持续Token同步。** 源继续运行时，如果任一端轮换refresh_token，另一端可能持旧值而后续刷新失败；禁用目标组也不能被认为绝对阻止后台刷新。没有为了测试强制刷新或修改源调度，长期并行应分别授权独立会话，或明确单端持有刷新职责。不要把本次可用等同于自然过期后的续期已验证。

### 新加坡固定区域故障切换

复用原 Mihomo，增加 **GPT-Load Singapore** fallback组和带随机认证的私有HTTP listener **172.17.0.1:7892**。GPT-Load管理代理ID=1，ChatGPT/Grok组均Custom引用它；GLM强制Direct，凭据级均继承组。没有使用原7890的全局规则出口，也没有改动原Travel Radar/节点选择等选择结果。

候选严格限定 `🇸🇬 新加坡|01`、`🇸🇬 新加坡|02`、`🇸🇬 新加坡|04`，不含DIRECT或其他地区。每300秒使用ChatGPT trace做健康检查，fallback仅失败时切到区内下一个，减少url-test反复选最低延迟造成的IP切换；它固定区域，不固定单一IP。全故障会请求失败，不跨区兜底。订阅更新后若标签改变，应重新验收该正则而不是添加全球兜底。

香港2个样本6次请求中有1次超时；新加坡4个普通节点12次HTTPS探测全成功，01/02稳定约0.3–0.4秒，04约0.2–0.7秒，03抖动1秒以上所以未入选。四个新加坡出口实测 `loc=SG/colo=SIN`；当前选择01。香港不在OpenAI官方支持区域列表，不能仅因低延迟就用作ChatGPT出口。

监听器只绑Docker宿主桥地址，未认证CONNECT实测 **407**；随机认证凭据位于服务器root-only `/opt/gpt-load/proxy-access.json`，完整订阅仍在 `/etc/mihomo/config.yaml`，没有进入仓库。`mihomo.example.yaml` 仅是无密钥的增量示例，不可替换完整配置。临时测试listener/候选全节点组已删除。

修改前配置备份 `/opt/gpt-load/releases/proxy/mihomo-before-20261027T085547Z.yaml`。配置先 `mihomo -t` 校验，再通过认证管理API graceful reload；进程PID仍104151，无宿主重启。回滚时先禁用海外组，恢复已知私有配置再校验reload，不能让海外组误用DIRECT。现成proxy-watch未改，长期应观察订阅更新和进程重启后的同一区域约束。

有效Custom连接失败不自动直连，但删除/禁用GPT-Load集中代理条目可能退回继承最终Direct；**不要删除/停用该条目来做故障演练**。配置条目本身存在时才保证所述区域约束。OAuth浏览器登录走用户电脑，服务端exchange/refresh/model继承分组代理；使用官方手动回调，不公开云端回调端口。

同步后一次资源采样：应用当前约73–83MiB，cgroup峰值约164MiB/256MiB，无OOM、无换出；Mihomo约51MiB、历史峰值71MiB；宿主MemAvailable约741–753MiB，Swap约701MiB。真实账号带来的内存明显高于空载，当前仍有余量。

## Sub2API 原 Key 预迁移（2026-10-28，历史准备阶段）

5 把有效原 Key 已通过官方管理 API 复制到目标并保持 **disabled**：源19/21 → 目标9/10（仅ChatGPT组3）；源25 → 目标11（仅Grok组4）；源28/29 → 目标12/13（仅GLM组5）。原Key值未改，未打印到日志；源服务和源Key均未修改。过期时间按数据库实际字段保留，名字中提及的日期不冒充真实expires_at。

原5h/7d限额仅作为待确认草稿保留：ChatGPT各200/200，Grok100/100，GLM400/400与300/299.99；不是用户当前积分套餐已确认的分摊方案。总额度草稿使用源导出时的剩余值；因源仍在消费，正式切换必须重新读取最终状态，不能把快照当最终余额，也不能启用零用量的新周期并宣称额度守恒。当前owner综合Key未修改，Default仍禁用。

用户澄清当前GLM为积分套餐，不应将历史价卡直接视为当前规则。官方quota实测Pro为5h12000积分/7d60000积分，存在currentValue/remaining；官方overview页面当日仍描述工作日14–18基准积分、平峰0.5倍。历史渠道旧USD价不自动迁入；GPT-Load原生Key预算为估算USD而非官方积分，并且没有同等钟点分时价或旧周期状态导入API。因此没有改价卡、没有直接写生产SQLite额度表、没有添加重置联动代码。

正式启用的阻碍：GLM两人分摊目标与内部核算口径需确认；旧Key当前用量/周期边界无法由现有API等价导入；旧入口切流需授权后避免双端消费。私有映射 `/opt/gpt-load/migrations/key-policy-20261028/staged-map.json` 不含Key原文，临时原文导出已删除；后置一致性备份 `/opt/gpt-load/backups/20261028T021729Z-k46et9ze` 已校验。**这是预建成功，不是限额迁移完成或已启用。**

## 四把 Key 正式切流与积分均分（2026-10-28）

**用户最后明确排除 `yuanundo` Codex，保护任务连续性：源Key21与目标Key10必须保持active，不停用、不清用量、不重新导入。** 曾在之前五把Key的停流步骤中源21被禁用，收到排除指令后已只恢复其active状态并按源源码清理该Key认证缓存。目标10当前有真实流量和历史，完全保留。后续操作不得重用包含21的旧批量脚本，也不得声称已经全部离开腾讯云。

其余4把正式迁移：

| 来源Key | 目标Key | 分组 | 配额策略 |
| --- | --- | --- | --- |
| 19，同事ChatGPT | 9 | ChatGPT plus / 3 | 原USD估算200/5h、200/7d，承接源已用与原周期、总量、有效期 |
| 25，个人Grok | 11 | Grok / 4 | 原USD估算100/5h、100/7d，承接源已用，模型限grok-4.7 |
| 28，同事GLM | 12 | 智谱glm / 5 | 官方积分目标6000/5h、30000/周，历史周用量折算约42201积分，当前周耗尽，登录可用但模型429 |
| 29，个人GLM | 13 | 智谱glm / 5 | 同样6000/5h、30000/周，承接历史周用量约2368积分；模型调用已实测200/OK |

原密钥值不变，客户端Base URL须切到 `https://ai.yuanundo.top/v1`。源19/25/28/29只修改status=disabled，并精准发布按sha256(key)的认证缓存失效，未停止Sub2API/Pg/Redis。源Key21已恢复active；源数据库、Key原值、费用历史保留。源原生管理员登录未成功（环境bootstrap密码过期），未伪造JWT或重置密码；窄范围状态变更使用经源码验证的事务/认证缓存协议，不清其他缓存。

新增admin-only一次性 `POST /api/fork/access-keys/:id/quota-import` 接受旧used/limit/固定周期锚点，只允许disabled、drained、无规则的新目标；持久指纹保证重试不重放用量。仅清理了本任务新建且从未使用的9/11/12/13零用量草稿规则，再经该接口导入；没有直接改GPT-Load生产SQLite表。已使用的10不适用该接口，保持不动。源先暂停并排空、冻结账目后启目标，防止双端重复放额。

GLM官方Pro返回5h总额12000积分、周60000积分，用户授权两人均分。credit模式为group5专属opt-in `overrides.glm_coding_credit_enabled=true`，仅CN官方Coding目标、仅官方确认的glm-5.3与glm-5.3-flash、group/key倍率1。独立定价表按请求开始时刻冻结：官方input/cache/output系数6.9/1.7/24与2.3/0.56/8；积分=(未缓存输入×系数+缓存×系数+输出×系数)/10000，工作日14–18 UTC+8按1、其他时段0.5。原“平峰价×2”与当前“基准×平峰折扣”在已确认模型数值等价，但积分不是现金美元。FlashX当前文档无确定系数，已从积分组移除，不能零价放行。

复用native账本 `1积分=0.01内部单位`，Key限额内部60/300即6000/30000积分。GLM Key登录首页通过安全key_accounting元数据直接显示积分及说明，不将60/300误称真实美元；通用管理员/混合Key和历史统计仍是native单位，不混算成美元采购成本。请求计费receipt v8带coding_credit元数据，普通模型仍v7。官方工具/MCP独立积分未接入，不承诺与上游每一分完全一致。

当期GLM周消费不是daily-reset的Key7d列，按官方reset时间范围对usage_logs actual_cost求和并用原内部积分转换承接；这能避免把源上已日清零的周用量误当剩余额度，仍属于本地估算，不是官方逐人归因。空的5h官方窗不返回nextResetTime且currentValue=0，保留未启动而不伪造时间。未来仍用原生首次请求起窗；没有自动“每次追随上游重置”的后台联动，这不属于已完成范围。

目标迁移状态 `/opt/gpt-load/migrations/key-policy-20261028/four-complete.json`，源只含4把的工具 `/opt/sub2api/migrations/gptload-key-cutover-20261028/source-four-cutover.py`，被保护21不在IDS。临时final-four-private原文导出已在日志核验与原Key认证等值确认后删除；durable状态只含映射和额度，不含Key原文。最终一致性备份 `/opt/gpt-load/backups/20261028T044209Z-5g9xwhoe` 已逐文件SHA256校验，并包含四Key迁移记录。

回滚必须先禁用目标9/11/12/13并排空，再恢复源4把原active状态并失效对应缓存，不能双开；已发生目标消费时不能直接回旧源余额而丢弃新消费，需按记录补账或仅恢复客户端入口并保守扣留余额。21/10不参与回滚。原程序、源码快照与预发布备份均保留；数据库无schema迁移，但新增设置需兼容处理。

验收：4把原Key在目标按access_key身份登录、仅能看本Key/home/models，管理路由403；跨组模型请求本地拒绝，GLM同事超周限额返回429且提供原reset，个人GLM真实19 tokens返回OK。源4把disabled、源21active，目标10active，所有服务未被关闭。目标credit个人首页单位与实际receipt下一轮以实际运行数据复查。

## 分组额度总览与重置卡入口（2026-10-28）

首页现按配置分组同时展示账号卡与专用 AccessKey 预算。账号卡展示脱敏身份、套餐、状态、真实额度窗口与重置时间；Key 卡直接展示 5h/7d 的限额、已用、剩余、进度与周期时间。跨组 Key 独立展示一次，GLM 专用 Key 显示积分。页面沿用 modern 公共组件、视觉变量和断点；不再在卡片重复展示长段口径说明。

新增管理员只读 `GET /api/fork/key-quotas`，按 ID 在 SQL 层分页，仅查询脱敏 metadata、配置分组及原生 quota Snapshot，不读取 Key 原文、不扫描请求日志、不改变额度。`group_ids` 保留配置范围（空数组表示全部），潜在匹配与当前有效组分别返回；普通 Key 返回 403、匿名返回 401。账号概览同步去除了 observation 内嵌的明文邮箱。

支持重置的账号卡列出实际返回的重置卡明细和到期时间，提供“使用一张”确认操作。复用原生 `reset-credits/consume` 与幂等键，由上游自动选择卡片，不伪造按卡 ID 选卡。结果未知时复用同次请求，成功后等待新观测再允许下一次使用。真实 Codex 现存两张卡及期限已只读核对，本次没有消费生产重置卡。

验证记录：全量 Go 测试、control 只读投影 race 测试、原生 reset-credit 回归、前端 type-check/lint/stylechecker/build、24 项 Node 数据测试均通过。浏览器已验证桌面、390px 窄屏及明暗主题；隔离 mock 中取消操作 0 次 POST，确认操作仅 1 次 POST/扣卡，卡数 2→1 且额度刷新。该模拟不等同于消耗真实上游卡的验收。

本次仅替换 GPT-Load 镜像，无 schema、额度、Key 状态、代理或 Caddy 配置变更。激活前一致性备份 `/opt/gpt-load/backups/20261028T074646Z-l97tkyf8` 已校验所有 SHA256。交付目录 `/opt/gpt-load/releases/6548ee0ed4cbfccc2147a48f18a7c802505ac1ba-snapshot-b217188e71d1-20261028T073620Z` 保存源码包、源码指纹及 `activation.json`；本地交付目录为 `build/aliyun-release.PNVT0r`。

公网首页、登录页、两个 fork 额度接口均 200，三个账号与七把 Key 已正确投影；普通 Key 仍只读自己的首页，管理额度接口 403。源 Codex Key21 和目标 Key10 仍 active，目标 Key 配置逐项比对不变。保留 OpenViking、通知网关，未重启腾讯云服务。发布后 cgroup 当前约 95.5MiB、本轮峰值 105.6MiB，无 max/oom/oom_kill，宿主 MemAvailable 约 699MiB；此为发布后采样。

本次回滚可在维护锁内将 `GPT_LOAD_IMAGE` 切回发布前 `wonderyuan/gpt-load@sha256:7097bc57e166599edd00e361fddba13670ab4acbda45aeaaa4e0fdec51a400f8`，只重建 GPT-Load，不恢复旧数据库以免丢弃发布后的请求消费；不要执行任何 Key 迁移回滚或停用 21/10。

### 紧凑分区与周期金额估算（2026-10-28 16:38 CST）

根据用户追加要求，额度总览改为上方账号区、下方密钥用量区。账号响应式三列，密钥按订阅分组轻分隔、组内两列，复用渠道目录的厂商图标；窄屏单列。每张卡的 5h/7d 共用一个纵向双进度条区域，突出本周期已用金额或积分，附紧凑的剩余、限额和重置时间。总预算仅从本页展示隐藏，未删除任何实际额度规则，整体受限状态仍显示。重置卡列表、期限与原生确认入口保留。

账号响应新增独立 `cycle_estimates` 只读字段，不进入定价或配额账本。GLM 官方 CN Coding、credit 模式、已识别 Pro/glm-pro 按 12000/5h 与 60000/7d 固定积分乘当前窗口已用比例；其他 5h/7d 账号窗按本地同周期参考费用 / 上游已用比例估算总量，截止时间与观测时间对齐，界面用「≈」。缺失、零比例、过期、读取失败不编造金额。费用不完整但有正费用时仍提供用户要求的近似估算，机器元数据 `complete=false`；已有官方 USD 窗按原周期返回，不借月度总额冒充周额度。请求量、Key 任意总预算不参与容量推算。

验收：全量 Go、control race、前端类型/lint/style/format/build 与 29 项 Node 测试通过。离线浏览器已核对桌面/390px：独立区域、厂商图标、紧凑双条、GLM 12000/60000 积分及重置卡保留；线上新前端 HTML SHA256 与交付产物一致。公网首页与两额度接口 200、匿名 401；普通 Key 权限隔离由 control 回归覆盖。本轮旧 `owner-access-key.json` 指向已不在当前列表的 Key2，试探返回 401，未恢复、重建或修改该 Key。

部署前当前列表已为五把 Key（9/10/11/12/13），不是上轮记录的七把；本次激活前后所有现有 Key 的名称、状态、范围、额度规则、期限逐项一致。源21/目标10保持 active，真实 Codex 重置卡仍为两张。本次未改变 schema、实际额度、账号凭据、代理、Caddy 或其他服务。

备份 `/opt/gpt-load/backups/20261028T083633Z-i6_st9lg` 已校验；交付 `/opt/gpt-load/releases/6548ee0ed4cbfccc2147a48f18a7c802505ac1ba-snapshot-3195f32c8c4c-20261028T082959Z`（含 `activation.json`），本地 `build/aliyun-release.mEcjIa`。兼容回滚只切回发布前 `wonderyuan/gpt-load@sha256:d9e81272da418e9fe0138bf546887f906d73680dba3df5f476749a608012e7f5`，不回灌旧 DB 丢弃新消费，不执行 Key 迁移回滚。发布后采样 memory.current 约 130.7MiB、peak 141.3MiB，无 max/oom/oom_kill、重启0，宿主 MemAvailable 约733MiB。

### 智谱重置卡与密钥周期重置（2026-10-28 18:20 CST）

账号重置卡默认折叠，仅显示数量，展开后展示明细与期限。智谱按 5 小时卡/周卡提供使用入口，周卡确认框说明会同时重置 5 小时与周额度；Codex 原生行为不变。只在明确为 credits/USD 且 used/limit/remaining 均为零时隐藏空窗口，未知、真耗尽和有额度窗口保留。

官方控制台公开静态模块核实：`GET https://www.bigmodel.cn/api/biz/customer-package-reset/list?targetType=PERSONAL`，以及 `POST https://www.bigmodel.cn/api/biz/customer-package-reset/use`；裸 API Key 可读列表已在现账号验证。使用参数由官方前端确认，为 `targetType/resetType/recordId/grantType/requestId`。API-key action 独立于 OAuth/subscription driver，复用原生 control durable 操作表、选卡持久化、同请求重试与 observation-refresh；不创建第二套 reset 引擎。浏览器只拿卡类型、available 与期限，不返回兑换记录 ID 或凭据。超时/不确定结果保持原卡、原 UUID；partial 卡查询不会伪造零张，也不会提前释放消费锁。

原生 consume 路由继续 `POST /api/groups/:group/credentials/:id/reset-credits/consume`，Codex/Claude `{}` 兼容；智谱新增可选 `reset_type: five_hour|week`。展示按实际分组 target 的 `reset_supported/reset_types`，不扩大 PAYG、国际站、自定义代理或团队目标。成功后只更新账号运行态与观测，不自动清任何 Key 用量。

密钥卡新增默认空的选择框，点击“重置所选用量”在确认框选择 5h/7d，逐 Key 调用原生 `/api/access-keys/:id/cost-limits/reset`；仅发送已持久化的选定周期 rule IDs，不包含 total 或其他周期、不改 limit/status/期限、不删日志。Key reset 原生接口无幂等，网络结果不明不自动重试；翻页或身份切换清理选择，逐 Key 显示结果并刷新原生/概览缓存。

本次从上一线上源码包加本轮 27 文件白名单 overlay 构建，隔离掉并行 model-info 未完成功能。隔离源码 `build/reset-isolated-source`，产物 `build/reset-isolated-source/build/aliyun-release.v9PIeN`；服务器交付 `/opt/gpt-load/releases/6548ee0ed4cbfccc2147a48f18a7c802505ac1ba-snapshot-ab01d532a6eb-20261028T095152Z`，有源码包、指纹与 activation.json。全量隔离 Go、adapter/control reset race、前端 type/lint/style/format、41 项 Node 与隔离构建通过。浏览器核对默认折叠、智谱按类型确认并取消；仅测试 DB 的合成 Key2 执行 5h reset，已用 12.34→0，7d 已用88与两限额不变。没有消费真实卡或重置真实 Key。

线上只读 observation-refresh 已读取 11 张智谱卡（4 张5h、7张周），卡期限为 2026-10-28 22:25:56 和 2026-12-22 23:59:59 CST；未请求真实 consume，消费线请求合同以 mock 覆盖。Codex 原两张卡未减少。部署前保护 Key10 已由其他操作改名为 `ChatGPT · yuanundo`，先核对其 active、group3，再保留当前名称；源 Key21/目标10均 active。

发布前一致性备份 `/opt/gpt-load/backups/20261028T101714Z-ggczhcix` 校验通过。激活前后五把 Key 的名称、状态、范围、规则、期限一致；只替换 GPT-Load 镜像，不改 Caddy/Mihomo、其他服务或腾讯云。线上首页/health/额度接口正常，匿名管理接口401。发布后采样 cgroup当前约178.5MiB、峰值209.5MiB、无 max/oom/oom_kill、重启0，宿主 MemAvailable约637MiB；峰值包含实际流量，不是单功能开销。兼容回滚切回 `wonderyuan/gpt-load@sha256:da7c5ca08424593e6cb2e13c574e3491720bfaf429127eb3fa317dd445328af7`，不回灌旧数据库丢弃新消费，不执行 Key 迁移回滚。

### 模型资讯发布（2026-10-28 18:50 CST）

新增 modern 原生 `/model-info` 导航，管理员与有效 AccessKey 登录后均可查看。总分矩阵每格使用左分数、右上费用/右下分钟耗时的三分格；帕累托使用模型颜色与形状、默认 low→ultra 轨迹、阶梯前沿和就近标签，费用/综合默认对数、耗时默认线性。另接入公开站点近 5 分钟均速与套餐参考额度；未嵌入源站页面、广告、外链跳转、全部档位明细表或口径公式。套餐参考值与本网关用量/账号余额分开。

后端固定源只读投影，首页资料缓存 10 分钟、均速 30 秒，失败保留有限历史并标 stale，缺值不补零。模型/档位/套餐动态扩展；测试覆盖 schema 版本、可选字段、样式类和单项坏数据等小变化，不能保证任意重大重构自动兼容。阿里云直连两个雷达域名 TCP 超时，既有新加坡代理实测首页 200/约1.9秒、均速200/约0.6秒；因此只增加私有 `MODEL_INFO_PROXY_URL` 环境变量并复用该 listener。代理原文仍只在服务器私有配置中，未改全局 Direct、GLM直连、ChatGPT/Grok分流或 Caddy/Mihomo。

发布基线为已运行的重置卡 snapshot `ab01d532a6eb…`，复制 36 个白名单功能/Compose文件，额外仅在隔离副本规范 uuid 直接依赖分类及两处既有格式，不升级任何依赖。隔离源码 `build/model-info-release-source`，本机产物 `build/model-info-release-source/build/aliyun-release.qyyVxr`；服务器交付 `/opt/gpt-load/releases/6548ee0ed4cbfccc2147a48f18a7c802505ac1ba-snapshot-cb5376f0017f-20261028T104823Z` 保存源码包/指纹、overlay清单、`activation.json` 和 root-only 激活前环境/Compose。源码仍未 commit/push；工作分支 `feat/model-info`。

实际隔离源码 `make check` 全通过（Go全量、vet、模块校验、前端type/lint/style/format/build及diff-check），两组 Node 回归57/57。上线前一致性备份 `/opt/gpt-load/backups/20261028T104737Z-o5dyzwtx` 的15文件SHA256校验全部通过。持维护锁只替换GPT-Load镜像，增加仅资讯可见的代理变量；激活前后五把Key、分组、额度规则、集中代理、系统设置和数据库schema逐项摘要一致，保护Key10（ChatGPT · yuanundo、active、group3）未改，腾讯云源21未操作。auth/encryption文件、Caddy/Mihomo文件和OpenViking/通知网关启动时间不变，未消费真实重置卡或重置真实Key。

公网 `/model-info`、`/health`、管理员会话、模型资讯和两个额度接口均200；资讯三块ready，实测8模型/46帕累托点/4套餐。匿名资讯接口401，浏览器确认公网页面由原生登录保护；认证后UI已在同构本地构建验收，本轮未为截图导出生产密钥，也未额外消耗真实模型Token。发布后首轮采样cgroup当前约118MiB、峰值186MiB/256MiB；18:54 CST最终采样当前约183.5MiB、峰值208.6MiB，无max/oom/oom_kill、重启0，资讯三块仍ready（均速14模型）。首轮宿主MemAvailable约699MiB；峰值包含真实网关流量，不将它归因于资讯功能，也不视为长期上限保证。最终记录保存为 `verification.json`。

兼容回滚：在维护锁内恢复该交付目录的 `pre-activation.env` 和 `pre-activation.compose.yaml`（包含原AUTH_KEY配置），仅重建GPT-Load回到 `wonderyuan/gpt-load@sha256:59957175a4472a06904af601ef56be5df85e134363ec3c8429b07a186886d6f5`；不回灌旧DB丢弃发布后消费，不执行Key迁移回滚、不停用21/10。`activation.json` 记录了配套备份与前后镜像。

### 普通密钥完整只读总览（2026-10-28 19:37 CST）

维护者明确要求所有有效 AccessKey 可查看与管理员一致的总览信息，但操作权限仅留管理员。新增 `GET /api/fork/home-overview` 单独返回全站库存/并发、固定24小时趋势、脱敏活跃账号和健康信息；开放既有脱敏账号/Key额度GET。原 `/api/home`、`/api/home/statistics`、模型/日志/用量的密钥作用域不改，未开放通用分组、健康、设置等管理API。所有写操作继续由原生GET精确白名单与principal校验限制。

普通密钥总览展示全局状态、全部账号/密钥额度、趋势、活跃账号和待关注事项。同步、重置、选择框、密钥复制/导入和管理跳转隐藏；查看用的分页、重试、展开及客户端切换保留。可继续访问模型资讯tab。全局只读投影不返回密钥明文、上游proxy/参数、原始邮箱；普通密钥的账号额度响应额外移除可能包含邮箱的account_summary并遮罩邮箱别名，管理员原合同不变。

按用户本轮最新要求，**仅验证修改部分，不跑全量回归**：4个精确后端权限/脱敏测试、只读首页解析/模板2项以及quota只读组合11项最小检查；本机Vite/Go仅用于构建发布产物，没有运行make check、全仓lint/type-check或真实模型生成请求。隔离实例用普通Key确认可见完整状态与全部Key卡，管理操作不显示；发布后用现有非保护Key9在服务器内存中验证总览库存/活跃Key集合与管理员一致，账号/Key额度及模型资讯200，通用管理GET及reveal/reset403，匿名401。未创建生产测试Key、未真实同步/消费重置卡或重置额度。

发布基线为 `cb5376f0017f…` 源码包，本轮隔离源码 `build/shared-home-release-source`；最终产物 `build/shared-home-release-source/build/aliyun-release.0S3T0K`，服务器交付 `/opt/gpt-load/releases/6548ee0ed4cbfccc2147a48f18a7c802505ac1ba-snapshot-b5db6886c3bd-20261028T113517Z` 保存源码包、指纹、overlay清单、`activation.json`/`verification.json` 和私有激活前配置。仅本机隔离发布脚本改用Vite产物编译跳过全仓类型检查，未修改上游依赖或网关数据面。

激活前一致性备份 `/opt/gpt-load/backups/20261028T112820Z-xxoysqi2` 逐文件校验通过。只替换GPT-Load镜像，环境/Compose、五把Key、分组、额度定义、代理、系统设置与schema摘要不变，Key10仍为active的yuanundo Codex，腾讯云源21未操作。容器healthy、无OOM、重启0。兼容回滚在维护锁内恢复该交付目录的 `pre-activation.env` 后仅重建GPT-Load回到 `wonderyuan/gpt-load@sha256:7da106c74b2738e65cbade68de4e90d08dfb3b6ce3fbedfe11586ce6d4e01d7f`，不回灌旧DB丢弃新消费。

### 定制分支整合与同事登录密钥（2026-10-28）

维护者授权将全部定制源码（额度/积分、重置卡、模型资讯、共享只读总览及部署文档）整合提交到 `custom`，只保留 `main`、`custom` 两个本地分支；`main` 不变。原有临时分支没有独有提交，清理前保存 `build/pre-custom-consolidation.bundle`。代理、登录密钥、数据库、备份、构建产物和 `.zcode/` 本地计划不纳入源码。`DESIGN_SYSTEM.md` 作为设计交付文档保留。

上线的完整只读总览已是最新应用行为，本轮源码收拢不额外扩展业务逻辑。原生管理API创建同事登录Key14，名称 `liangxiankai`，随机Key原文仅存服务器 `/opt/gpt-load/access-keys/liangxiankai.json`（0600），不写Git或文档。它是普通 `access_key`，可读完整总览与模型资讯，管理设置/重置403；因为本轮只授权登录，不分配模型额度，原生模型白名单设为不匹配任何已配置模型的专用占位项，未给同事无限模型调用权。原五把Key和保护10保持原状态/配置。创建前一致性备份 `/opt/gpt-load/backups/20261028T114429Z-stsa9uvg`。

首次创建连接中断时，进程发生一次 `modernc/sqlite _walFindFrame` SIGBUS并由容器自动恢复（restart1、OOMKilled=false）。当时请求日志写入栈触发，创建未持久化；只读核对后沿用同一个幂等请求，通过HTTP管理接口创建成功。此后不再为本次验证直接打开运行中SQLite；未把该故障未经证实地归因于模型资讯或OOM。保留日志，未做数据库重置/清理，继续按本轮要求只做必要最小验证。

## 状态、日志、重启和备份

```sh
ssh aliyun
cd /opt/gpt-load
docker compose ps
docker compose logs --tail 100 gpt-load
curl -fsS http://127.0.0.1:3100/health
docker compose restart gpt-load
python3 /opt/gpt-load/bin/backup.py /opt/gpt-load
systemctl list-timers gpt-load-backup.timer
```

`/health` 只是 HTTP 进程检查，不代表 DB/上游/OAuth 完好。分享日志前脱敏。不要运行 Docker 全局 prune。

已启用宿主 timer 每天 **04:30 CST** 在线备份，无停服，Nice=15、idle I/O、CPUQuota=10%、MemoryMax=96MiB，已手动执行验证 Result=success。备份程序使用 SQLite Backup API，关闭目标连接并 checkpoint 成独立数据库，连同两份 key、compose、私有 .env、owner 文件、镜像 revision/digest 和 SHA256SUMS 保存于 root-only `/opt/gpt-load/backups`；同步账号后额外纳入 proxy-access、account-sync-state、Mihomo配置和provider文件。仅成功快照保留最近14份，无新容器。源账号临时明文导出在完成导入、日志检查和一致性备份后已删除，没有复制到本机或Git。未备份到外部存储。

最初快照发现 WAL 临时文件关闭后使校验清单失效，已修复重测；旧目录保留并标记 `INVALID-CHECKSUMS`，不能恢复它。只有带 `COMPLETE` 且校验通过的快照可用。

备份/发布共用 `.maintenance.lock`。手工更新必须持有该锁，避免备份期间变更程序版本或两份密钥。应用不会正常运行时自动轮换两份 key；不要手工并发轮换。

## 后续 Fork 构建、更新与回滚

维护一个本机交付入口，不建设 CI 平台：

```sh
# 在本机 Fork 内，先提交真正的应用源码变更（本次没有代提交）。
bash deploy/aliyun/release.sh aliyun
```

依赖已有 Go/Node/npx/crane/SSH。默认脚本拒绝未提交的应用源码；用户只授权开发部署而未授权提交时，可显式运行 `bash deploy/aliyun/release.sh aliyun --snapshot`。该模式从 Git 管理范围内的源码建立隔离副本，拒绝密钥/数据库/运行目录，以内容指纹和 base HEAD 共同标记版本，保留 source.tar.gz/source.sha256/release.json，不以旧提交冒充新代码；默认干净提交路径保持不变。锁定 pnpm11.17，从隔离源码副本构建前端再交叉编译 Go，crane 下载 Dockerfile 指定的 Alpine digest，经 SSH 交付。服务器**仅使用现有 Dockerfile 的 runtime/prebuilt 段打包，不执行 Go/前端编译、不启动构建虚拟机**。镜像 tag 含完整 SHA 和每次构建时间标识，拒绝覆盖已有发布 tag，记录 binary SHA256/OCI revision，输出不可变 RepoDigest；产物位于 gitignored build 与服务器 releases。无需镜像仓库凭据，生产不依赖拉取 GHCR。该命令不自动激活、覆盖生产 .env 或清理旧镜像。

更新激活步骤：

1. 在当前版本运行备份；核对新镜像 revision/digest、构建结果，保留旧 digest 与备份。
2. 进入维护窗口，以 `flock /opt/gpt-load/.maintenance.lock` 互斥，只修改私有 `.env` 的 GPT_LOAD_IMAGE；`docker compose config --quiet && docker compose up -d --pull never --no-build`。
3. 验证 health、认证、设置、流式、资源与保留服务。若有真实凭据，最小 Token 请求验证，不进行大规模压测。
4. **无迁移、且旧程序兼容当前库**才允许仅切回旧 digest。涉及迁移必须配套恢复旧数据库、auth.key、encryption.key 及私有配置。
5. 恢复时只停止 GPT-Load；先将失败后的 data 留存到新的 root-only 目录，不覆盖删除。在新暂存目录校验 SHA256SUMS、quick_check 和版本，再恢复配套文件，保证 UID/GID=10001:10001、目录0700、key/db0600；使用旧镜像启动并重新验收。备份后的新增写入会丢失，需明确告知。

本次已演练独立数据恢复、重启持久化，以及“原始程序基线 → 脚本生成的 prebuilt 镜像 → 原始程序基线”的实际切换，管理员 key、加密 key 和 SQLite 设置保持不变，记录在服务器 `releases/rollback-demo.json`。首次同 SHA 重复构建复用 tag 导致旧 digest 失去可寻址引用，运行容器未中断；已重新打包保留原基线，脚本修正为每次唯一发布 tag，备份元数据改从实际容器取得，不依赖可能已被替换的 tag。旧失败备份目录未删除、没有 COMPLETE。

不同应用版本的 schema 迁移回滚**没有验证**，不能据同一 SHA 的镜像切换演练推断跨版本迁移安全。

**判断：三个正常账号的真实模型调用已验证，可用 `https://ai.yuanundo.top/v1` 进行小范围试用；ChatGPT/Grok走新加坡区内自动故障切换、GLM直连。仍需关注双网关共用OAuth刷新令牌的长期冲突、codex12重新授权、真实大上下文峰值及当前备案接入核验；不能将一轮最小调用宣称为长期稳定性保证。**
