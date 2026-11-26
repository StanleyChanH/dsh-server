# dsh-server 运维手册

dsh（DeepSeek Harness）agent 常驻服务：docker compose 三服务栈。成员在私有网络
（局域网 / VPN）内用浏览器访问 `https://<DSH_TRUSTED_HOST>`；也可以选配第二个、面向
公网的入口 Cloudflare Tunnel（见下文「Cloudflare Tunnel」）。私有地址只在 `.env` 的
`DSH_TRUSTED_HOST` 配一处；下文命令与示例一律用文档保留地址 `192.0.2.10`。

项目概览与快速开始见 [README](../README.zh-CN.md)，设计取舍见
[design.zh-CN.md](design.zh-CN.md)。

## 架构

```
成员浏览器 ──https──> caddy（发布 80/443，TLS internal）
                        ├─ /auth/* ────────────> auth-gate:8080（登录页/登录/检查/探活）
                        └─ 其余路径 forward_auth ┘   │ cookie 无效 → 302 跳登录页
                                                     │ cookie 有效 ↓ 放行
                                                 dsh:3080（agent Web UI）
```

- **caddy**：唯一发布端口的服务（80 仅跳转 + 443 本体）。证书由内置 CA 签发（IP SAN），
  全站不启用压缩（保 SSE/WebSocket 实时流）。
- **auth-gate**：Go 登录门。内嵌登录页（`auth-gate/login.html`）、bcrypt 用户表
  （`auth-gate/data/users.json`）、HMAC 签名 cookie `gate_session`。Caddy `forward_auth`
  先打 `/auth/check` 门禁，未登录的 GET 由它 302 跳登录页（非 GET 401——浏览器只对
  3xx 自动导航，401 只会渲染空白页）。
- **dsh**：agent 本体。所有状态在 `dsh-home/`（挂 `/data/dsh`），共享工作区 `workspace/`
  （挂 `/workspace`），容器 home 运行期状态在 `dsh-root/`（挂 `/root`，2026-11-23 起——
  npm 缓存、工作区 symlink、gh 认证等跨重建存活）。
- dsh 与 auth-gate **不发布任何端口**，仅 compose 内网互通。
- **cloudflared**（可选，`compose.tunnel.yml`）：Cloudflare Tunnel，一条到 Cloudflare
  的出站连接。它独占一个 compose 网络，只到得了 caddy 的第二个监听器（`:8080`，
  不发布），别的都到不了。见[「Cloudflare Tunnel」](#cloudflare-tunnel)。

## 构建前必读：镜像来源

- 如果构建机 **gcr.io 直连不可达**（如中国大陆网络）：auth-gate 的运行基底
  `gcr.io/distroless/static:latest` 先经你信任的镜像代理拉取，再 `docker tag` 成原名。
  经代理拉取等于信任该代理的内容，只用自己认可的来源。
- **切勿 `docker build --pull` / `docker compose build --pull`**——会试图直连 gcr.io
  拉基底镜像而失败。本仓库构建一律不带 `--pull`。
- `LAN_PROXY`（见变量表）只加速 **RUN 步骤与容器内出网**，docker daemon 拉镜像
  不经它（daemon 的代理与镜像加速要单独配置）——gcr.io 不可达时，上面的预处理
  流程不变。
- 换机器重建前先处理镜像源：

  ```sh
  docker pull <镜像代理>/gcr.io/distroless/static:latest
  docker tag  <镜像代理>/gcr.io/distroless/static:latest gcr.io/distroless/static:latest
  ```

  其余基底（`golang:1.24-alpine`、`node:24-slim`、`caddy:2.10-alpine`）来自 Docker Hub。
  auth-gate 构建阶段的 Go 模块代理默认 `https://goproxy.cn,direct`，可用 `.env` 的
  `GOPROXY` 覆盖（Dockerfile 已接）。

## 首次部署

前提：**amd64（x86-64）** 的 Linux 宿主机（dsh 镜像安装的是 amd64 版 GitHub CLI），
装有 Docker Engine 与 Compose v2、`curl`、`openssl`、`jq`；成员能经私有网络访问
本机的 80/443。宿主机不需要 Go 或 Node。

token 同步任务还依赖两点：宿主机上有 cron 守护进程在运行；root 能运行
`docker compose`（Compose 插件装在系统级，而不是只装给某一个用户）。

脚本默认管理员账号的 uid 是 1000（见 compose.yml 的 `user:`）。如果不一样，
要把 compose 和那里点名的两个脚本一起改，否则 auth-gate 读不到 token 文件。

1. 配置变量：`cp .env.example .env`，至少填两项——`DSH_TRUSTED_HOST`（成员浏览器
   里输入的 IP 或域名，不带协议和端口）与 `AUTH_GATE_SECRET`（`openssl rand -hex 32`
   的输出）。`DSH_VERSION` 已预填为当前验证过的版本。
2. 构建镜像：`docker compose build`（**不带 `--pull`**，理由见上）。dsh 镜像约
   3.4GB（含 chromium 与中日韩字体），首次构建要下载较多内容。
3. 建第一个账号（此时栈还没起也没关系）：

   ```sh
   scripts/add-user.sh <名字>
   # 提示处输入密码（不回显）；同名重跑即改密码
   ```

   auth-gate 是 fail-fast 的：`users.json` 还不存在时它会 crash-loop。`scripts/add-user.sh`
   在服务容器不在时自动改用一次性容器跑 `-adduser`，写的仍是同一个
   `auth-gate/data` 挂载——所以先建账号再启动，不管顺序怎么排都能解开。
4. 启动：`docker compose up -d`，用 `docker compose ps` 确认 dsh 与 auth-gate
   为 healthy。
5. 取出根证书并发给自己：`caddy/data/caddy/pki/authorities/local/root.crt`
   （导入方法见「加成员」；文件是 root:root 0600，宿主机读取要 sudo）。
6. 装 token 自动同步的 cron：把下面内容写进 `/etc/cron.d/dsh-server-token`
   （root 权限；`$(pwd)` 在当前 shell 里展开成仓库的绝对路径）——每分钟把最新
   dsh token 原子写进 `auth-gate/data/dsh-token`。缺了这步，401 自动换乘在 dsh
   首次重启后就会静默失效（成员一直停在「凭证同步中」页）；装完约 1 分钟内应看到
   `auth-gate/data/dsh-token` 文件出现。

   ```sh
   sudo tee /etc/cron.d/dsh-server-token >/dev/null <<EOF
   SHELL=/bin/sh
   PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
   * * * * * root $(pwd)/scripts/sync-token.sh
   EOF
   ```

   之后它每分钟都会更新一次时间戳。如果没有，手动跑一次 `sudo scripts/sync-token.sh`
   看报错：cron 得在运行，root 也得能运行 `docker compose`。

7. 跑一遍 `scripts/smoke.sh` 做整栈自检（详见「冒烟自检」；§0 会检查 cron 文件在位、
   token 文件正在被刷新）。

## 冒烟自检

`scripts/smoke.sh`（可重复跑）对运行中的栈走一遍关键路径：未登录 302 跳登录页、
登录发 cookie、dsh token URL 换取、401 自动换乘（含 token 同步一致性）、
WebSocket 升级、删用户立即撤销 cookie、重启 dsh 换 token 且旧 URL 失效。
任何一步不符预期即非零退出。默认目标是由 `docker compose config` 从 `.env` 解析出的
对外地址；也可以 `BASE_URL=https://host scripts/smoke.sh` 指定。它会：

- 临时建用户 `smoketest` 并在结束时删除（users.json 热重载，无需重启）；
- **重启一次 dsh**（token 轮换验证；已登录成员的 dsh cookie 实测不受影响，
  但正开着的页面会闪断重连）；
- 输出里 token 一律打码（完整 token 只在容器日志里）；
- token 没人在同步时直接拒绝：cron 文件缺失，或 token 文件好几分钟没刷新，§0 就
  失败——否则下一次 dsh 重启后自动换乘就会失效。

## 启停

```sh
docker compose up -d      # 启动（已配置 restart: unless-stopped，宿主机重启自动拉起）
docker compose ps         # 看三服务状态/健康检查
docker compose logs -f    # 跟日志（dsh 日志里有敏感 token URL，注意别外贴）
docker compose restart dsh  # 只重启某个服务（dsh 重启后 token 轮换，见「取 token URL」）
docker compose stop       # 停栈（数据都在宿主机目录，不丢）
docker compose down       # 停栈并删容器/内网（不动 ./dsh-home 等挂载目录）
```

注意：

- auth-gate 的用户表按 mtime 热重载，加人/改密不需要重启任何服务。
- 单纯 `restart` 不会让改过的 `.env` 生效：重启出来的容器沿用创建时的环境变量。
  要重建才行，见[「更新 dsh-server」](#更新-dsh-server)。

## 更新 dsh-server

容器沿用创建时的环境变量，`docker compose restart` 改不了它。所以变更一律靠
**重建**容器来生效：

| 之后要做的操作 | 命令 |
| --- | --- |
| `git pull` 之后 | `docker compose build && docker compose up -d`，然后 `scripts/smoke.sh` |
| 改了 `.env` | `docker compose up -d`（地址或代理变了会重建 caddy 和 dsh；dsh 重启会打断正在运行的任务） |
| 只改了 `caddy/Caddyfile` | `docker compose up -d --no-deps --force-recreate caddy` |
| 开启或关闭 Cloudflare Tunnel | 和改 `.env` 一样：`docker compose up -d`，关闭时加 `--remove-orphans`。见[「Cloudflare Tunnel」](#cloudflare-tunnel) |

`docker compose up -d` 只重建配置变了的容器，其余照常运行；
`docker compose up -d --dry-run` 可以先看它会重建哪些。

拉取更新后不要指望 `restart`：重启出来的容器仍是旧的环境变量，改过的 `.env` 不起
作用；而且一个在 `DSH_TRUSTED_HOST` 出现之前创建的 caddy 容器，一旦重启就加载不了
现在的 Caddyfile，陷入 crash-loop。

如果这次拉取改了 `compose.yml`、某个 Dockerfile 或 `.env.example`，先把自己的
`.env` 和 `.env.example` 对一遍：新变成必填的变量会让每一条 `docker compose` 命令
都报错退出，报错里会点名那个变量。

## 升级 dsh

dsh 是 developer preview，破坏性变更预期——**手动逐版升级，不自动跟随**。
版本号记录在 `.env` 的 `DSH_VERSION`（当前 `0.2.2-rc.5`；作为 build arg 传入
`dsh/Dockerfile` 的 `npm install -g @deepseek-ai/dsh@${DSH_VERSION}` 钉版，
升级只改这一处）：

1. 改 `.env` 里的 `DSH_VERSION`。
2. `docker compose build dsh`（勿 `--pull`）。
3. `docker compose up -d dsh`（重建容器；compose 会因镜像变更自动换容器）。
4. **重建自愈已内建**（2026-11-23 起，原 restore-container.sh 退役）：`/root`
   整体挂出（`dsh-root/`），工作区 symlink、npm 缓存、gh 认证等运行期状态跨
   重建存活；apt 四件套（openssh-client/sshpass/xauth/python3）、chromium 与
   中日韩/emoji 字体、gh、pnpm 均已烘进镜像，重建零联网补装（mnemon 的 opt/
   补链分支仍在 entrypoint 里，`opt/` 不存在时跳过）。核验：
   `docker compose exec dsh test -f /root/.gitkeep && echo 挂载OK`。
   换机部署漏拷 `dsh-root/` 的症状：会话状态写进 rootfs、重建即丢（smoke §0 会拦）。
5. `docker compose ps` 确认 healthy。实测语义：重启/重建后**旧 token URL 立即失效**，
   但已经用 token 换到 `dsh-auth-*` cookie 的浏览器会话**继续有效**（会话状态在
   `dsh-home/` 里持久化）——已登录的成员无感；新 token 由 cron 每分钟自动同步
   （见「自动换乘与降级模式」），**不再需要取 URL 手动分发**。
6. **跑 `scripts/smoke.sh`，全绿才算升级完成**：§0 含挂载探针与镜像依赖在位
   检查，§10 端到端验证 401 → 自动换乘 → dsh 会话的
   整条链；dsh 换版若改了日志格式/状态码，断点会当场暴露（此时按「降级模式」
   处理）。
7. 出问题回滚：版本改回旧值，重复 2–6。

升级 caddy / auth-gate：改 `compose.yml` 里的镜像 tag 或 `auth-gate/` 源码后
`docker compose build && docker compose up -d`，同样勿 `--pull`。单独编辑
`caddy/Caddyfile` 时注意它是**单文件 bind mount**：编辑器保存通常是换新 inode，
容器里挂的还是旧 inode，改完必须重建 caddy 才能吃到新配置：`docker compose up -d --no-deps --force-recreate caddy`。

升级 gh：改 `dsh/Dockerfile` 里的 `GH_VERSION` 与其下的 sha256（取自该版本 release
的 `gh_<版本>_checksums.txt` 中 `linux_amd64.deb` 一行），再走上面第 2–6 步。下载
走 GitHub，构建时 `LAN_PROXY` 须可用。chromium 等 apt 包只在所在层重建时才刷新。

## 加成员

新成员两样：**gate 账号 + 根证书**。启动 URL 不再手动发——登录后由「401 自动
换乘」自动完成 dsh token 换取（见「自动换乘与降级模式」）。

1. 建账号（auth-gate 容器在不在运行都行；不在时脚本会自动改用一次性容器）：

   ```sh
   scripts/add-user.sh <名字>
   # 提示处输入密码（不回显）；同名重跑即改密码，服务热重载，无需重启
   ```

2. 分发根证书 `caddy/data/caddy/pki/authorities/local/root.crt`，成员导入为受信 CA：
   - macOS：钥匙串访问 → 系统钥匙串 → 导入 → 双击该证书 → 始终信任。
   - Windows：双击 → 安装证书 → 本地计算机 → 受信任的根证书颁发机构。
   - Firefox 有独立证书库：设置 → 隐私与安全 → 证书 → 导入。
   - 只发证书文件本身；CA 私钥不出宿主机。
   - 注意该文件是 root:root 0600（caddy 容器写的），宿主机取它要 sudo。

成员首次访问流程：打开 `https://192.0.2.10` → 登录页输账号密码 → 自动换乘
发 dsh 会话 cookie，30 天免登。若赶上 dsh 刚重启的同步窗口，会先看到
「凭证同步中」页，约 1 分钟内自动恢复，无需操作。

删人（立即踢下线，其 gate cookie 马上失效）：auth-gate 容器以宿主机管理员账号
（uid 1000，见 compose.yml `user:`）运行，users.json 属主就是管理员，直接编辑即可：

```sh
tmp=$(mktemp) && jq --arg u '要删的名字' 'del(.[$u])' auth-gate/data/users.json > "$tmp" && cat "$tmp" > auth-gate/data/users.json && rm "$tmp"
```

`--arg` 把用户名当**字面量键**传入：含 `.`/引号/`$` 的名字也安全。千万别写成
`del(.john.doe)`——jq 会把它解析成 `.john` 下的 `doe` 字段，键不存在时 del 是
**无操作且退出码 0**，文件被原样重写，管理员以为踢了人，实际其 cookie 与登录
在会话期内一直有效。

服务按 mtime 热重载，无需重启任何容器。

## Cloudflare Tunnel

这套栈是为私有网络设计的。给既不在局域网也不在 VPN 里的成员，有一个可选的第二
入口：**Cloudflare Tunnel**，跑在独立的容器里（`compose.tunnel.yml`）。不启用就不
存在。它不需要入站端口、公网 IP 或端口转发：`cloudflared` 主动连上 Cloudflare 并保
持这条连接，Cloudflare 把请求顺着它送进来。Cloudflare Access 挡在公网主机名前面，
决定谁能进；登录门在它后面照常生效。隧道容器独占一个 compose 网络，只到得了
caddy，所以 Cloudflare 面板里地址配错了也绕不过登录门。

需要一个 Cloudflare 账号，以及一个 DNS 托管在那里的域名。下面的菜单名取自 Cloudflare
2026 年 11 月的文档；它会不定期改菜单名。

### 开启

1. **建隧道。** Cloudflare 面板：*Networking* → *Tunnels* → *Create a tunnel*；如果让
   选连接器类型，选 *Cloudflared*；起个名字 → *Create Tunnel*。复制 token：安装命令
   里那一长串（在 `--token` 之后，或在 `service install` 之后）。接着页面会等连接器
   连上来。不用等：隧道已经建好，连接器到第 5 步随栈一起启动。
2. **先用 Access 保护这个主机名**，要在路由存在之前：路由一建出来就对互联网开放。
   *Zero Trust* → *Access controls* → *Applications* → *Create new application* →
   *Self-hosted and private* → *Add public hostname*。域名填成员要输入的主机名，路
   径留空（整个域名）。加一条动作为 *Allow* 的策略，写明谁可以用：邮箱地址，或身份
   提供方里的组。账号里还没有登录方式的话，先加 *One-time PIN*（*Zero Trust* →
   *Integrations* → *Identity providers*）：邮件验证码。
3. **加路由。** *Networking* → *Tunnels* → 这条隧道 → *Routes* 标签页 → *Add route*
   → *Published application*。子域和域名同第 2 步：域名下只有一级（`dsh.example.org`；
   更深的名字需要 Cloudflare 默认不签发的证书）。Service URL 填 `http://caddy:8080`。
   `caddy` 是 compose 服务名，连接始终在 compose 网络内，所以是 HTTP 而不是 HTTPS。
4. **在 `.env` 里启用。** 把 `.env.example` 中 *Cloudflare Tunnel* 那一段的三行抄进
   `.env`，去掉注释并填好（那一段还有第四行 `TUNNEL_PROTOCOL`，是可选的）：
   `COMPOSE_FILE`、`TUNNEL_TOKEN`（第 1 步的 token）、`TUNNEL_HOSTNAME`（第 3 步的
   公网主机名：只填主机名，不带协议和路径）。
5. **生效**，在部署目录里：

   ```sh
   docker compose up -d
   ```

   镜像是用更早的版本构建的部署，先跑 `docker compose build`：dsh 的 entrypoint 在
   镜像里。这会启动 `cloudflared`，并重建 caddy（约 10 秒不可用）和 dsh（正在运行
   的 agent 任务会被打断）。连上之后 `docker compose ps` 里 `cloudflared` 是
   `healthy`，通常一分钟内；连不上的话约两分钟后是 `unhealthy`。
6. **验证：**

   ```sh
   scripts/smoke.sh
   ```

   §11 检查隧道：dsh 是否认这个公网主机名、两个入口上登录限流各按哪个地址计数、
   公网主机名是否在 Cloudflare Access 后面应答。

这套栈只认一个公网主机名，就是 `TUNNEL_HOSTNAME` 里的那个。caddy 的隧道监听器会
应答 Cloudflare 路由过来的任何主机名，所以你指向这条隧道的每一个额外主机名都需要
自己的 Access 应用。没有的话，那个主机名从互联网上直达登录页（dsh 仍然拒绝它的
API 调用）。

### 关闭

删掉 `.env` 里的 `COMPOSE_FILE` 那一行（隧道的那几个变量想删也可以），然后：

```sh
docker compose up -d --remove-orphans
```

它会删掉 `cloudflared` 容器，并重建不带隧道的 caddy 和 dsh。compose 网络
`<project>_tunnel` 会留下，`docker network rm <project>_tunnel` 可以删掉它。之后在
Cloudflare 面板里删掉那条路由（隧道的 *Routes* 标签页），或者整条隧道。

### 需要知道的

- **在部署目录里运行 `docker compose`，不要带 `--env-file`。** `COMPOSE_FILE` 里是
  相对路径，compose 按命令所在的目录解析；而且带了 `--env-file` 时它根本不会被读。
  这样的命令只作用于基础栈。对 `config`、`ps`、`logs` 无害，但这样跑 `up -d` 会把
  caddy 和 dsh 重建成不带隧道（dsh 会重启），而 `cloudflared` 还在运行：在部署目录
  里重新 `docker compose up -d` 之前，公网入口是断的。冒烟测试会在 §11 指出来。
- `docker compose config` 会打印 `TUNNEL_TOKEN`，别把它的输出贴到任何地方。
- `TUNNEL_HOSTNAME` 只填主机名，可以带端口。别的写法 entrypoint 不会传给 dsh：dsh
  遇到这种值会拒绝启动，而 caddy 要等 dsh，一个笔误会让私有入口也跟着挂。冒烟
  测试会点名这样的值。
- 改了 `TUNNEL_HOSTNAME` 之后要 `docker compose up -d`。容器沿用创建时的环境变量，
  没重建的话冒烟测试会指出来。
- 升级 cloudflared 版本后：`docker compose up -d cloudflared`，然后跑冒烟测试。
- cloudflared 先用 QUIC（UDP）连 Cloudflare，连不上再回退到 HTTP/2（TCP），这就是它
  的默认值 `auto`。到 Cloudflare 的 UDP 被限速的网络里，一次启动可能在回退之前就以
  `Register tunnel error ... context deadline exceeded` 和退出码 1 结束，Docker 会重启
  容器（在实测的那个网络上是第三次启动才连上）。这种网络下在 `.env` 里设
  `TUNNEL_PROTOCOL=http2`，然后 `docker compose up -d cloudflared`：连接器一开始就走 TCP。
- 经隧道进来的访客地址取自 `Cf-Connecting-Ip`。Cloudflare 侧如果有移除或改写这个
  头的功能开着（"Remove visitor IP headers"、Pseudo IPv4 的 "Overwrite Headers"
  模式），所有隧道访客会共用一个登录限流桶：每分钟一共 5 次错误密码。
- Cloudflare 会关闭 100 秒内源站没有任何数据的代理请求（它的 524 错误），所以 dsh
  的流如果静默那么久，可能需要重连。
- dsh 对外宣称的仍是 `DSH_PUBLIC_URL`，也就是私有地址。
- dsh 的会话属于签发它的那个地址，所以私有主机名和公网主机名是两个会话。第二个
  由自动换乘处理。

## 取 token URL

dsh 每次进程重启都换新 token，旧 URL 失效。重启/升级/重建容器后：

```sh
scripts/dsh-url.sh
# 输出形如 https://192.0.2.10/?token=...
```

**该 URL 是敏感串**（= 持有者的 dsh 登录凭证），只发给该发的人；勿群发、勿贴聊天群。
它也一直留在 `docker compose logs dsh` 里，宿主机能读 docker 的账号本来就可见——
但日志导出/外发前先想着这事。

## 自动换乘与降级模式

gate 账号是成员的唯一凭证：token 的"手动分发"已改为"自动换乘"，成员登录后
无需任何 URL 即进入 dsh。链路：

```
成员访问 /（无 dsh cookie）→ dsh 401
  → Caddy 拦下 401 → 302 /auth/dsh-entry
  → auth-gate：gate cookie ✓ + token 就绪 → 302 /?token=<当前>（同时种 30 秒防环 cookie）
  → dsh 下发 dsh-auth-* 会话 cookie → 30 天无感
```

token 同步链：dsh 每次重启换 token；宿主 cron（`/etc/cron.d/dsh-server-token`）
每分钟跑 `scripts/sync-token.sh`，从 dsh 日志提取最新 token 原子写入
`auth-gate/data/dsh-token`（auth-gate 按 mtime 热加载；解析失败不动旧文件）。

- **同步窗口约 1 分钟**：dsh 重启/升级后到下一轮 cron 之间，未登录 dsh 的成员
  访问站点会看到「凭证同步中」页（20 秒自动刷新），最坏约 100 秒自愈（重启时机
  不巧时：等容器起来 + 下一轮 cron），全程无需人工操作；
- 30 秒内二次换乘会被防环 cookie 拦下出「凭证同步中」页——不会 401 死循环，
  已登录的会话（dsh cookie）始终不受影响；
- **降级模式**（任一环失效，典型：dsh 升级改了日志格式 → `sync-token.sh` 解析
  失败、token 停在旧值）：自动换乘失效但**不锁人**。处置先分清两种情况：
  - **同步窗口内**（dsh 重启后 ≤1 分钟，cron 下一轮就补上）：新成员/新设备
    停在同步页是预期行为，等自愈即可，无需任何操作；
  - **解析持续失败**（`sync-token.sh` 连续报错、token 文件不再更新）：**不会
    自愈**，立即手动回退——`scripts/dsh-url.sh` 取当前 token URL 发给该成员
    打开一次（即自动换乘之前的手动模式；若 `dsh-url.sh` 本身也报错，直接
    `docker compose logs dsh | grep token=` 取 URL 发之）。同时修 `dsh-url.sh`
    的解析——这是恢复自动换乘的必要修复，不是可选项；修好后下一轮 cron 自动
    恢复换乘，无需重启任何服务。

## 备份

全部状态都在宿主机目录，备份就是 rsync。停栈后做最稳妥（冷备）；不停栈做热备，
至少避开会话写入的高峰时段。
**必须 sudo**：dsh-home 里有 root 属主文件（`.credentials.yaml` 0600、`storages/`
drwx------，dsh 容器以 root 写），caddy/data 下 pki 树也是 root:root 0600——
普通用户的 `rsync -a` 逐文件报错、exit 23，备出来的档案**缺凭证/会话/CA**：

```sh
sudo rsync -aH dsh-home/       <备份目标>/dsh-home/        # 会话/插件/凭证/配置（含模型 API key）
sudo rsync -aH dsh-root/       <备份目标>/dsh-root/        # 容器 /root 状态（npm 缓存、工作区 symlink、gh 认证）
sudo rsync -aH workspace/      <备份目标>/workspace/       # agent 共享工作区产物
sudo rsync -aH auth-gate/data/ <备份目标>/auth-gate-data/  # users.json（丢了=全员重设密码）
sudo rsync -aH caddy/data/     <备份目标>/caddy-data/      # 内置 CA（丢了=要重发根证书）
```

`.env` 单独妥善保管（含 AUTH_GATE_SECRET）；它不入 git，备份介质要加密。

还原同样用 `sudo rsync -aH`（root 权限保属主）。若属主信息已丢（备份介质不带
属主），按写入方补齐，否则容器写不进对应目录：

```sh
sudo chown -R root:root dsh-home/ workspace/ caddy/data/   # dsh/caddy 容器以 root 写
sudo chown -R 1000:1000 auth-gate/data/                    # auth-gate 以管理员账号运行（compose user:）
```

## 排障

| 症状 | 排查 |
| --- | --- |
| 整站打不开 | `docker compose ps`：caddy 起了吗？`docker compose logs caddy`；宿主机防火墙放行 80/443（注意 Docker 发布的端口走 FORWARD/DOCKER 链、绕过 ufw；80/443 平时由 docker-proxy 直接监听）；成员是否在私有网络内 |
| `docker compose` 任一命令报 `DSH_TRUSTED_HOST is not set` / `DSH_VERSION is not set` | `.env` 缺必填项或拼错键名（没有 `.env` 就 `cp .env.example .env`）；这两个变量没有默认值，是故意的 |
| caddy 重启后 crash-loop，日志里是 Caddyfile 解析错误（`default_sni` 参数个数不对） | 容器是在 `DSH_TRUSTED_HOST` 出现之前创建的（`git pull` 更新了代码，却没跟着 `docker compose up -d`），里面没有这个变量。重建它：`docker compose up -d --no-deps --force-recreate caddy`。同时确认 `.env` 里定义了 `DSH_TRUSTED_HOST` |
| curl 打 https://192.0.2.10 报 `000`/exit 35，openssl 看到 `tlsv1 alert internal error` | 站点是裸 IP，客户端不发 SNI（RFC 6066 禁止 IP 字面量），Caddy 挑不到证书。Caddyfile 全局块的 `default_sni {$DSH_TRUSTED_HOST}` 就是治这个的——它和站点地址都来自 `.env`，别手改 Caddyfile 写死 |
| caddy 迟迟不起 | caddy `depends_on` 要求 dsh 与 auth-gate 都 healthy：`docker compose ps` 看谁 unhealthy，`docker compose logs dsh` / `logs auth-gate` 查原因 |
| auth-gate 起不来（crash-loop：`stat /data/users.json: no such file`） | 首次部署 users.json 还不存在，先建第一个用户（`add-user.sh` 会自动走一次性容器），再 `docker compose up -d` |
| dsh 不 healthy | 手跑健康检查同款命令：`docker compose exec dsh sh -c 'ip=$(hostname -i \| awk "{print \$1}"); curl -s --noproxy "*" -o /dev/null --max-time 5 "http://$ip:3080/"; echo "rc=$? ip=$ip"'`。注意**不能加 `-f`**：dsh 的 `/` 对未认证请求就是 401（无 token/cookie），`curl -f` 把 401 当失败 → 永远 unhealthy |
| 浏览器证书告警 | 根证书未导入或已换 CA：重新分发 `caddy/data/caddy/pki/authorities/local/root.crt`（换 CA 后旧的要删；文件是 root:root 0600，宿主机取要 sudo） |
| 一直跳登录页 / 登录后又弹回 | `docker compose logs auth-gate`；cookie 是 Secure 的，必须 `https://` 访问；密码错 5 次触发限流，等 1 分钟 |
| 登录过了但停在「凭证同步中」页 | 自动换乘的 token 同步链断了或正在窗口内：先等 1 分钟（cron 自动补）；仍不行 `docker compose logs auth-gate`，手动回退 `scripts/dsh-url.sh` 取 URL 发该成员（见「自动换乘与降级模式」） |
| 手工 curl 探 WebSocket 一直 404 | 探测必须 `--http1.1`：默认 ALPN 协商到 h2 后 `Connection`/`Upgrade` 头会被丢弃，dsh 收到的是普通 GET（404）。带 cookie + `--http1.1` + 三个 `Sec-WebSocket-*` 头才是 101 |
| `add-user.sh` 报错 | 密码不能为空；容器没在运行时会自动退回一次性容器模式（需要本地 docker 权限） |
| `dsh-url.sh` 取不到 URL | 容器没起或日志轮转：`docker compose logs dsh` 直接看 |
| `smoke.sh` 一上来就退出，说无法确定测试目标 | 当前目录没有有效的 `.env`（脚本从部署目录运行，目标由 `docker compose config` 解析）；或显式给 `BASE_URL=` |
| `smoke.sh` 在 §0 停下：`token file does not exist` 或 `token file is … old` | cron 没在同步 token：cron 没运行，或者 root 跑不了 `docker compose`。手动跑 `sudo scripts/sync-token.sh` 看报错。`git pull` 之后还要确认 `.env` 里该有的必填变量都还在 |
| `smoke.sh` 在 §0 的 `cloudflared healthy` 失败 | 隧道启用了但没连上：`docker compose logs cloudflared` 会说明原因（token 解析不了会直接退出再被拉起；token 不被 Cloudflare 接受、或到不了 Cloudflare，则进程在跑但一直不就绪）。`TUNNEL_PROTOCOL` 没设或是 `auto` 时，每次退出前都有 `Register tunnel error`、容器反复重启，说明 QUIC 过不去：在 `.env` 里设 `TUNNEL_PROTOCOL=http2`，再 `docker compose up -d cloudflared`。每次退出前是 `unknown protocol`：值不是 `auto`、`quic`、`http2` 之一。私有入口不受影响。见「Cloudflare Tunnel」 |
| 公网主机名返回 `502 Bad Gateway` 或 1033 错误 | 隧道启用时：看 `docker compose ps cloudflared`。不是 `healthy` 说明隧道断了（`docker compose logs cloudflared`）。是 `healthy` 则说明 cloudflared 找不到 caddy——开启隧道时 caddy 没被重建，不在隧道网络里：`docker compose up -d`。再确认公网主机名的服务是 `http://caddy:8080` |
| 公网主机名直接显示 dsh-server 的登录页，之前没有 Cloudflare Access 的登录 | 公网主机名前面只有登录门，等于登录页挂在了互联网上。立刻补上 Access 应用（「Cloudflare Tunnel」第 2 步）；补上之前 `scripts/smoke.sh` 的 §11 会失败 |
| 走隧道的所有人被一起限流（`尝试过多`） | Cloudflare 没有发 `Cf-Connecting-Ip`，或者改写了它，于是所有隧道访客共用一个桶：每分钟一共 5 次错误密码。检查该域名的 Managed Transforms 和 Pseudo IPv4 设置（「Cloudflare Tunnel」之「需要知道的」） |
| `smoke.sh` 的 §11 失败：`dsh was created with TUNNEL_HOSTNAME "..."` | `.env` 在 dsh 创建之后改过。容器沿用创建时的环境变量：`docker compose up -d` 会重建 dsh |
| `smoke.sh` 的 §11 失败：`dsh was not started with TUNNEL_HOSTNAME "..."` | 两种情况之一：`.env` 的 `TUNNEL_HOSTNAME` 里不止主机名（带了协议或路径），entrypoint 没把它传给 dsh，`docker compose logs dsh` 里有说明——改成只填主机名，然后 `docker compose up -d`；或者 dsh 镜像比隧道这个选项还旧、它的 entrypoint 不认识这个变量——`docker compose build dsh && docker compose up -d` |
| 忘了某成员密码 | `scripts/add-user.sh <名字>` 重设即 |
| 构建失败提到 gcr.io | 误用了 `--pull`，或新机器没做镜像源预处理，见「构建前必读：镜像来源」 |

## 已知风险

- dsh 是 developer preview：破坏性变更预期，升级手动、逐版、可回滚。
- agent 会执行模型生成的命令——容器即隔离边界，agent 只能碰挂载进容器的东西
  （`dsh-home/`、`workspace/`、`dsh-root/`）。dsh 容器以 root 运行且未挂
  docker socket：不要给它加 `privileged`、宿主机目录或 docker socket。
- **全员共用同一个 dsh 实例**：会话、凭据、工作区在成员之间共享。登录门回答的
  是「谁能进」，不是「数据是谁的」。
- token URL 长期存在于 `docker compose logs dsh`（敏感串，勿外发日志）。
- 访问面 = 私有网络 + 登录门。不开 Cloudflare Tunnel 时它不暴露在公网，也别把 443
  转发到公网。开了隧道后，公网主机名属于访问面：前面是 Cloudflare Access 和登录
  门，而且 Access 必须一直在（缺了它 `scripts/smoke.sh` 会失败）。
- 开了 Cloudflare Tunnel，成员收发的一切都经过 Cloudflare。公网主机名的 TLS 由它
  终结，所以自动换乘的 dsh token URL、两枚会话 cookie、提示词和输出在它那一侧是
  明文，可能进入它的日志。这是公网入口的代价；不开隧道时没有任何东西离开私有
  网络。
- 开了隧道，caddy 的隧道监听器会应答 Cloudflare 路由过来的每一个主机名。
  `TUNNEL_HOSTNAME` 是这套栈认识并检查的那一个；其他任何主机名，除非有自己的
  Access 应用，否则从互联网上直达登录页（dsh 仍然拒绝它的 API 调用）。不要在没有
  Access 的情况下把主机名指向这条隧道。
- **Docker 发布端口走 FORWARD/DOCKER 链，绕过 ufw 规则**：若启用 ufw 想收紧，
  必须显式写 `DOCKER-USER` 链规则，光 `ufw deny 443` 拦不住这套栈。

## 变量速查（.env）

| 变量 | 用途 | 默认/示例 |
| --- | --- | --- |
| `DSH_TRUSTED_HOST` | **必填**。成员浏览器里输入的 IP 或域名（不带协议和端口）。一处驱动：Caddy 站点地址与证书、dsh 的 `--trusted-host`、`DSH_PUBLIC_URL` 的默认值。改后运行 `docker compose up -d` | `192.0.2.10` |
| `DSH_VERSION` | **必填**。部署版本（build arg，Dockerfile npm install 消费；升级单改 .env） | `0.2.2-rc.5` |
| `AUTH_GATE_SECRET` | **必填**。会话签名密钥（`openssl rand -hex 32`）；更换后全员需重新登录登录门 | — |
| `DSH_PUBLIC_URL` | dsh 对外公告地址；仅需要与默认值不同时才设 | `https://<DSH_TRUSTED_HOST>/` |
| `LAN_PROXY` | 局域网代理：注入 dsh 构建（npm/apt/gh 下载）与容器出网（git clone 等），置空全关；不覆盖 daemon 拉镜像 | 空 |
| `NO_PROXY` | 代理豁免地址（内网/本地必须在内；healthcheck 另有 `--noproxy` 铁闸） | `localhost,127.0.0.1,…` |
| `AUTH_SESSION_HOURS` | 登录会话时长（小时） | `720`（30 天） |
| `GOPROXY` | 构建 auth-gate 时的 Go 模块代理 | `https://goproxy.cn,direct` |
| `COMPOSE_FILE` | 可选。`compose.yml:compose.tunnel.yml` 即启用 Cloudflare Tunnel（[「Cloudflare Tunnel」](#cloudflare-tunnel)） | 不设置 |
| `TUNNEL_TOKEN` | 启用隧道时**必填**。隧道的 token（Cloudflare 面板的 Networking → Tunnels）。它是机密：拿到的人可以把自己的服务器接到这条隧道上 | - |
| `TUNNEL_HOSTNAME` | 启用隧道时**必填**。公网主机名，例如 `dsh.example.org`（不带协议和路径）；dsh 接受的第二个受信主机。改了之后要 `docker compose up -d` | - |
| `TUNNEL_PROTOCOL` | 启用隧道时可选。cloudflared 连 Cloudflare 的方式：`auto`（先 QUIC，回退到 HTTP/2）、`quic` 或 `http2`。到 Cloudflare 的 UDP 被限速时设为 `http2`。改了之后要 `docker compose up -d cloudflared` | `auto` |
