# dsh 团队常驻服务器 — 部署设计

日期：2026-11-21（增量：2026-11-22 自动换乘、2026-11-23 /root 持久化、2026-11-24 依赖烘进镜像）
状态：已实施

> 英文版：[design.md](design.md)。运维手册：[operations.zh-CN.md](operations.zh-CN.md)。

## 目标与动因

把 DeepSeek Harness（dsh）部署成本团队的常驻 agent 服务器：docker compose 常驻运行，
团队成员通过浏览器随时随地使用同一个 agent 实例（会话历史、插件、配置全部持久化）。

## 目标

- dsh 以 `restart: unless-stopped` 常驻，升级可控（锁定版本）
- 团队成员通过私有网络（VPN 子网路由 / 局域网）访问 `https://192.0.2.10`
  （示例地址；实际由 `.env` 的 `DSH_TRUSTED_HOST` 决定，全栈只在这一处配置）
- 所有可维护资产集中在部署目录 `/path/to/dsh-server/` 下
- agent 有持久共享工作区，产物落在宿主机

## 非目标

- 不做公网暴露（只走 tailnet/局域网）
- 不做多租户/按用户权限（dsh 当前是单 token + cookie 模型，登录门 auth-gate 按人发密码弥补）
- 不从源码构建 dsh（用官方 npm 包）

## 约束（侦察结论）

- dsh 无官方镜像；npm 包 `@deepseek-ai/dsh`，当前部署 `0.2.2-rc.5`（npm `latest`；0.2.3 起 Node 要求提升：24.x 系列 ≥ 24.22.3，或 ≥ 26.13.1）
- dsh 是 developer preview，破坏性变更预期 → 版本必须锁定，升级手动
- `dsh web` 默认绑 127.0.0.1:3080；`--host` 只接受具体 IP（拒绝通配），
  官方 JSDoc 明确容器场景绑"容器自身 IP"（`hostname -i`）
- 反代需要：保留 Host 头、透传 WebSocket、`--trusted-host` 点名浏览器可见 authority、
  `--public-url` 公告外部地址（无路径前缀，cookie 无需改写）
- 认证：启动 URL 内置 token → 30 天签名 cookie；进程重启换新 token（需从 logs 取新 URL 分发）
- 所有状态在 `$DSH_HOME`（profiles/会话/凭证/配置）
- 安全：dsh 未审计，会执行模型生成的命令——容器即隔离边界，agent 只能碰挂载进容器的东西

## 方案选择

- **容器化方式**：`node:24-slim` + 全局安装锁版本的 npm 包。
  备选"源码构建"被否：重、慢、升级维护成本高，npm 包即官方发布物。
- **入口**：沿用现有的私有网络通路（VPN 子网路由），成员直敲 `https://<DSH_TRUSTED_HOST>`。
  备选"服务器也入 tailnet"（ts.net 域名 + 免费证书）被否，保持现网架构。
- **TLS**：Caddy `tls internal`（内置 CA，签 IP SAN 证书），成员首次导入根证书一次
  （根证书导出放在宿主机，分发即可）。
- **认证**：auth-gate（Go 登录门，源码 `auth-gate/`）已替换原定的 Caddy `basic_auth`：
  Caddy `forward_auth` 打 `/auth/check` 门禁，未登录的 GET 302 跳转内嵌登录页
  （`auth-gate/login.html`；非 GET 401——浏览器只对 3xx 自动导航），
  bcrypt 用户表按人发号、可单独撤销，登录后发 HMAC 签名 cookie `gate_session`（默认 30 天），
  外层再叠 dsh 启动 token 双层。
- **绑定**：entrypoint 脚本取 `hostname -i` 传给 `--host`（compose 内网中 Caddy 独享，
  不发布 3080 端口，符合官方"端口限制在可信代理内"）。

## 部署结构

```
/path/to/dsh-server/
├── docs/design*.md         # 本文档（design.md 为英文版）
├── docs/operations*.md     # 运维手册（启停/升级/加人/取 URL/备份/排障）
├── README*.md              # 项目概览与快速开始
├── compose.yml             # dsh + auth-gate + caddy 三服务
├── .env / .env.example     # DSH_VERSION 等变量（.env 不入库）
├── dsh/
│   ├── Dockerfile          # node:24-slim + git/curl/ca-certificates + npm 包
│   └── entrypoint.sh       # 取容器 IP → exec dsh web --no-open --host <ip> \
│                           #   --public-url https://<DSH_TRUSTED_HOST>/ --trusted-host <DSH_TRUSTED_HOST>
├── auth-gate/              # Go 登录门（内嵌 login.html，bcrypt 用户表，HMAC cookie）
│   └── data/               # users.json 所在（挂为 /data）
├── caddy/
│   ├── Caddyfile           # https://{$DSH_TRUSTED_HOST} { tls internal; forward_auth auth-gate; reverse_proxy dsh:3080 }
│   ├── data/               # Caddy 持久化（含内部 CA，root.crt 从这里分发）
│   └── config/
├── scripts/                # add-user.sh（加人/改密）、dsh-url.sh（取启动 URL）、
│                           #   sync-token.sh（cron 同步 token）、smoke.sh（端到端自检）、
│                           #   check.sh（CI 跑的那组检查）
├── dsh-home/               # 挂为 $DSH_HOME —— 会话/插件/凭证/配置全在此
├── dsh-root/               # 挂为 /root —— 容器 home 目录，跨重建保留
└── workspace/              # 挂为 /workspace —— agent 共享工作区（容器 WORKDIR）
```

compose 要点：caddy 发布 80(仅跳转)/443，dsh 与 auth-gate 不发布端口；
`DSH_HOME=/data/dsh`；json-file 日志上限防膨胀；模型 provider 的 API key 首次在
Web UI 里配置，持久化在 dsh-home。

## 运维手册（已交付为 `docs/operations.zh-CN.md`）

- 启动/升级/停止命令
- 重启后取新启动 URL：`scripts/dsh-url.sh` 从 `docker compose logs dsh` 提取
- 新成员接入：`scripts/add-user.sh` 加账号 + 分发根证书（启动 URL 此后由自动换乘接管，见下方增量）
- 备份：rsync `dsh-home/` + `workspace/`
- 已知风险提示（developer preview、token 重启轮换、agent 可写 workspace）

## 部署时验证结论（2026-11-21 实测，Task 7 冒烟）

- `hostname -i` 在 compose 网络里稳定返回单 IPv4（172.18.0.2）；healthcheck 用同
  IP 探测 3080 可行，但 `/` 未认证就是 401，探测**不能带 `curl -f`**（否则永远
  unhealthy）——liveness 语义放宽为"端口上完成一次 HTTP 应答"。
- Caddy internal CA 对 IP SAN 签发正常（叶子+中间证书共 2 张下发）。两个实测坑：
  1. 裸 IP 站点没有 SNI（RFC 6066 禁止 IP 字面量），Caddy 无 SNI 挑不到证书 →
     握手 `alert internal error`。需全局 `default_sni`（现为 `{$DSH_TRUSTED_HOST}`）。
  2. forward_auth 沿用原始方法与 URI，登录门按方法分派拒绝码：GET 302（浏览器
     只对 3xx 自动导航）、非 GET 401（API 语义），均带 Location 回跳。
- WebSocket 透传验证通过：`/api/remote.mux` 带双 cookie + HTTP/1.1 升级头 → 101。
  注意探测必须 `--http1.1`：ALPN 协商到 h2 后 `Connection`/`Upgrade` 头被丢弃，
  dsh 收到的是普通 GET（404），曾据此误判过"没有 WS 端点"。
- dsh 会话两层实证：gate cookie 只开门；token URL 一次性换取 `dsh-auth-*` cookie
  （状态在 dsh-home 持久化）。重启 dsh：旧 token URL 立即 401，已登录的 dsh
  cookie 仍有效，gate cookie 不受影响。
- 宿主机防火墙：这套栈自己不配置防火墙；Docker 发布的端口由 docker-proxy 直接
  监听。提醒：这些端口走 FORWARD/DOCKER 链、绕过 ufw，宿主机若开着 ufw，要收紧
  访问必须显式写 DOCKER-USER 链规则。
- auth-gate 以宿主机管理员 uid（compose `user: "1000:1000"`）运行，users.json
  属主与宿主机账号对齐，直接编辑删人无需 sudo（此前容器以 root 写就 0600，
  宿主机编辑须 sudo）。
- 首次部署顺序坑：auth-gate fail-fast，users.json 不存在即 crash-loop；
  `add-user.sh` 兜底改为服务容器不可用时走一次性容器，先建用户再 up。

## 增量设计：401 自动换乘（2026-11-22，已确认）

**动机**：两层门（gate 账号 + dsh token URL）对新成员太繁琐。把 dsh token 的
"手动分发"改为"自动换乘"——gate 账号成为唯一凭证。

**已确认的代价**：dsh token 不再是面向成员的第二道秘密（门禁实质 = admin 发号
的 gate 账号）；token 会在成员浏览器历史出现一次。

**流程**：

```
成员访问 /（无 dsh cookie）→ dsh 401
  → Caddy handle_response 拦 401 → 302 /auth/dsh-entry
  → auth-gate：gate cookie ✓ + token 文件就绪 → 302 /?token=<当前>
  → dsh 发会话 cookie → 30 天无感
```

**组件与细节裁定**：

1. `scripts/sync-token.sh`（host）：从 `docker compose logs dsh --no-color`
   提取最新 token（与 dsh-url.sh 同源逻辑），校验形态；**解析失败不写文件**
   （fail-safe：宁缺勿旧）；成功则原子写 `auth-gate/data/dsh-token`
   （0600，内容仅 token 串）。cron：`/etc/cron.d/dsh-server-token` 每分钟。
2. auth-gate `GET /auth/dsh-entry`（不限方法）：
   - 无有效 gate cookie → 302 `/auth/login?next=/`
   - token 文件就绪 → 302 `/?token=<tok>`，同时种 `gate_entry_retry` cookie
     （值=unix 秒，MaxAge 35s，HttpOnly）
   - retry cookie 存在且 <30s（刚试过仍在循环）或 token 未就绪（缺失/为空）→
     200 极简"凭证同步中，约 1 分钟后自动恢复"页（防重启同步窗口死循环）。
     「缺失→同步页」仅在启动态成立：启动时文件不存在则 token 留空；运行中
     文件被移走时 stat 失败，reload 沿用 mtime 缓存的旧值继续下发（fail-open，
     循环由 guard 页限频兜底）。为空则另一回事：TrimSpace 后是空串，照常同步页
   - token 文件用与 users.json 相同的 mtime 缓存模式读取
3. Caddyfile：`reverse_proxy dsh:3080` 块内
   `@dsh_unauth status 401` + `handle_response @dsh_unauth { redir * /auth/dsh-entry 302 }`。
   （`redir` 首个 `/` 开头 token 会被当成路径 matcher 永不命中，必须显式 `*`。）
   该拦截对 dsh 的一切 401 生效（浏览器流语义，可接受）。
4. smoke.sh 新增段落：带 gate cookie 无 dsh cookie → `/` 得 302→`/auth/dsh-entry`→
   302→`/?token=…`，跟踪后非 401；sync-token.sh 产出与 logs 一致。

**降级模式**（任一环失效，如 dsh 升级改了日志格式/状态码）：自动换乘失效但不锁人，
退回手动模式（`dsh-url.sh` 取 URL 发成员）；smoke.sh 升级后必跑可立即发现断点。
升级运维流程更新：改 `DSH_VERSION` → 重建 → `scripts/smoke.sh` 全绿才算完成。

**新成员接入简化为两样**：gate 账号 + 根证书（token URL 不再需要手动发）。


## 增量设计：/root 持久化、深度重置与恢复脚本退役（2026-11-23，已实施）

**动因**：11-22 升级事故的结构性修补最初落在 restore-container.sh（entrypoint
自动重放），但它是「事后补」——每次重建仍要联网装 apt/pnpm、重放 symlink。
根治 = 让状态根本不落 rootfs 蒸发面。

**裁定**：

1. **`./dsh-root:/root` 整体 bind mount**（从运行中容器 `docker cp` 播种迁移，
   含 200M npm 缓存）。团队未来在 `/root` 新增的任何状态自动跨重建存活。
   副作用（接受）：镜像更新里 `/root` 的新文件被挂载遮蔽，不会自动出现。
2. **确定性依赖烘进 Dockerfile**：apt 四件套（openssh-client/sshpass/xauth/
   python3）与 pnpm——原本是团队运行期装的。升级重建零联网补装。
3. **entrypoint 接管 `/usr/local` 补链**：gh/mnemon 二进制本体在持久卷
   `/data/dsh/opt/`，entrypoint 幂等 `ln -sf`（瞬时、无网络依赖）。
   `/usr/local` **故意不挂**：挂了会遮蔽镜像内钉版的 dsh 本体，升级静默失效。
   （第 3 条里 gh 的部分已于 2026-11-24 被取代：gh 已烘进镜像。）
4. **restore-container.sh 退役**（连同 CONTAINER-UPDATE-GUIDE.md，二者的备份 tarball 留在宿主机）。职责三分：
   **镜像管确定性依赖，挂载管运行期状态，entrypoint 只做挂载后补链**。
5. smoke §0 检查对象同步换轨：`/root/normal` 在位（挂载生效探针；同日稍后改为
   结构性标记 `/root/.gitkeep`），不再检查恢复脚本。
6. **深度重置**（同日）：清 sessions/attachments/profiles/使用统计（已留备份，178M）；保留 workspaces、
   凭据、gh 认证、relay 配对。团队成员需重装皮肤插件（已知代价）。

**验证**：重建两轮 + smoke §0–§10 全绿；gh/mnemon/pnpm/apt 四件套在位；
`/root` symlink 时间戳跨重建不变。


## 增量设计：chromium/字体与 gh 烘进镜像（2026-11-24，已实施）

**动因**：团队在运行期容器里手动 apt 装 chromium 与中日韩/emoji 字体（未带
`--no-install-recommends`，拉进 327 个包），装在可写层，重建即蒸发——与 11-22
事故同类。gh 自 11-23 清空后未安装。设计文档：
`docs/superpowers/specs/2026-11-24-bake-chromium-gh-design.md`。

**裁定**：

1. **chromium + 字体进镜像**：`chromium fonts-noto-cjk fonts-noto-color-emoji
   fonts-liberation`，`--no-install-recommends`（镜像内实增 158 个包）。
   fonts-liberation 是 chromium 的推荐包，显式列出以顶替网页常见的 Arial/Times。
2. **gh 取 GitHub Releases 钉版**：2.105.0 + sha256 校验。Debian 12 源只有 2.23.0。
3. **两层置于 npm 层之前**：升级 dsh 只重建 npm 层及其后的层；放在其后则每次
   升级都要重新下载浏览器与字体。
4. **entrypoint 退役 `opt/gh` 补链**：gh 已在镜像，补出来的 `/usr/local/bin/gh`
   会遮蔽钉版。
5. **smoke §0 增加镜像依赖在位检查**：重建后丢东西当场暴露；并断言 gh 解析到
   镜像内的 `/usr/bin/gh`、中日韩/emoji 字体可解析——这两样丢了不报错，只表现
   为被遮蔽或截图出豆腐块。

**验证**：一次性容器内 Chromium 156.0.8078.108、gh 2.105.0 在位；`fc-match` 命中
Noto Sans CJK SC / Noto Sans Mono CJK SC / Liberation Sans / Noto Color Emoji；
无头渲染「中文 + emoji + Latin」截图目检无缺字，不带 `--no-sandbox` 以 root 启动
明确报错。镜像 2.19GB → 3.4GB（chromium/字体层 804MB，gh 层 42MB）。2026-11-25
换容器后 smoke §0–§10 全绿（含新增的镜像依赖在位检查）。

## 增量设计：经 Cloudflare Tunnel 的公网入口（2026-11-26，已实施）

**动因**：既不在局域网也不在 VPN 里的成员完全够不着这套栈，而端口转发被安全模型
排除在外——dsh 会执行用户让它做的任何事。Cloudflare Tunnel 不开入站端口，公网主机名
前面可以放 Cloudflare Access。登录门保留：Access 回答「谁可以到达这台服务器」，登录
门回答「这是哪位成员」，dsh 在最里面还有自己的会话。计划：
`docs/superpowers/plans/2026-11-26-cloudflare-tunnel.md`。

**裁定**：

1. **用覆盖文件而不是 compose profile**：`compose.tunnel.yml`，经 `.env` 的
   `COMPOSE_FILE` 加载。profile 做不到「缺变量就失败」：未激活的 profile 里有
   `${TUNNEL_TOKEN:?}` 会让所有 compose 命令都报错；不加它，缺 token 又只表现为容器
   永远连不上。用覆盖文件，不设 `COMPOSE_FILE` 的部署渲染结果与从前完全一样。
2. **cloudflared 独占一个网络，caddy 接入两个**：cloudflared 只到得了 caddy。
   Cloudflare 面板里地址配错了（比如 `dsh:3080`）从它那里不可达，所以面板配错也绕
   不过登录门。
3. **caddy 的第二个监听器 `http://:8080`，不发布，路由链相同**：路由抽成一个片段，
   两个监听器都导入，片段的参数指定各代理向上游追加什么。无论隧道是否启用，这个
   监听器都存在——地址是空环境占位符的站点块无法通过适配。
4. **只在隧道监听器上从 `Cf-Connecting-Ip` 取客户端地址**：那条 `header_up` 放在
   隧道监听器导入的片段里；私有监听器导入一个空片段，保持 Caddy 的默认行为（对端
   地址；客户端自带的 `X-Forwarded-For` 被丢弃）。把默认行为写成
   `header_up X-Forwarded-For {remote_host}` 是同一回事，Caddy 还会警告它多余。三个
   跳（`/auth/*`、`forward_auth`、dsh）上的地址都必须正确，因为 auth-gate 按
   `X-Forwarded-For` 的首段统计登录失败。经 Cloudflare 时不能直接用这个头：请求本来
   就带着它的话，Cloudflare 是把访客地址**追加**在后面，首段是访客自己填的。
5. **公网主机名是 dsh 的第二个受信主机**：变量有值时，entrypoint 追加
   `--trusted-host "$TUNNEL_HOSTNAME"`。dsh 的来源校验会拒绝 `Host` 没被告知过的 API
   请求，而经隧道进来的 `Host` 就是公网主机名。
6. **Cloudflare Access 在面板里配置；栈本身不校验 Access 的 JWT。** 登录门和 dsh
   自己的会话在它后面照常生效。冒烟测试从宿主机访问公网主机名，应答不是 Access 的
   跳转就失败。
7. **smoke §11** 检查启用之后可能出错的各个环节：dsh 是带着当前的 `TUNNEL_HOSTNAME`
   创建的并且认它（经隧道监听器取得会话、WebSocket 升级被接受）、限流在两个监听器
   上各按正确的地址计数、Access 挡在首页和登录端点两个路径前面。

**实测且被依赖的事实**（Caddy 2.10.2、cloudflared 2026.11.2、Compose 5.5.0）：

- `header_up` 不接受请求匹配器：`header_up @name X-Foo value` 适配不报错，实际变成
  对名为 `@name` 的头做正则替换。
- 没有地址的站点块无法适配：「server block without any key is global configuration,
  and if used, it must be first」。
- `COMPOSE_FILE` 按命令所在目录解析，带 `--env-file` 时不会被读。
  `docker compose up -d --remove-orphans` 会删掉 cloudflared 容器，但留下
  `<project>_tunnel` 网络。
- token 解析不了时 cloudflared 立即退出（状态 255），完全没有网络时也是（状态 1）。
  token 格式合法但 Cloudflare 不接受时它在运行，`/ready` 答 503，并会被 Docker 不时
  重启。`cloudflared tunnel ready` 读 `TUNNEL_METRICS`：对答 200 的端点退出 0，否则
  退出 1。这个版本没有 `--protocol` 参数。
- dsh 的会话绑定在签发它的主机上：cookie 载荷里写着它，同一个 cookie 换个 `Host`
  出示，dsh 答 401。
- dsh 与 auth-gate 都不读 `X-Forwarded-Proto`，两者的 cookie 无论如何都是 `Secure`，
  所以隧道监听器不转发它。

**验证**：`scripts/check.sh` 用 auth-gate 与 dsh 的替身运行 Caddyfile，断言两个监听
器各自交给后端什么（两边都带伪造的头）；Caddyfile 的六种改坏方式都被报出。compose
检查断言拓扑和两个必填变量；覆盖文件的六种改坏方式都被报出。entrypoint 的参数列表
在 dsh 镜像的基础镜像里检查，包括 `TUNNEL_HOSTNAME` 为空的情况。单元测试钉住「空的
`X-Forwarded-For` 回退到对端地址」。在一个沙箱栈上（dsh 由当前代码树构建，不发布端口，
独立的项目名和镜像 tag）：隧道关闭时，未改动的冒烟测试通过；换上 cloudflared 与
Access 的替身后 §0–§11 通过，并且每项新检查都见过因各自的原因失败，其中包括 dsh
运行旧的 entrypoint、以及 Caddyfile 计数了伪造的地址。没有涉及任何运行中的部署。
**写作时尚未了结**：Cloudflare Access 对冒烟测试请求的应答是用替身比对的。第一次
对着真实隧道运行，才能确定 `302` 这个模式对不对，以及运维手册里引用的面板文案是否
仍然适用。
