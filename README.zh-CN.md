<h1 align="center">dsh-server</h1>

<p align="center">
  <a href="https://github.com/StanleyChanH/dsh-server/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/StanleyChanH/dsh-server/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/License-MIT-blue.svg"></a>
  <a href="https://github.com/deepseek-ai/deepseek-harness"><img alt="dsh: DeepSeek Harness" src="https://img.shields.io/badge/dsh-DeepSeek%20Harness-4d6bfe"></a>
</p>

<p align="center">
  <b>团队常驻、自托管的 <a href="https://github.com/deepseek-ai/deepseek-harness">DeepSeek Harness</a></b><br>
  一套 Docker Compose 栈：每人一个账号、裸 IP 上的 HTTPS、再也不用分发 token URL。
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

## 为什么要有它

[DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness)（dsh）的 Web UI `dsh web` 只有一种凭证：一条内含 token 的启动 URL。谁拿到这条 URL 谁就有全部权限，而且 dsh 每次重启 token 都会变。一个人用没问题；团队用的话，每次重启或升级之后都得在聊天工具里转发一条机密 URL。

dsh-server 在 dsh 外面套了三个小服务：

- **auth-gate** 给每位成员一个独立账号：bcrypt 密码、签名会话 cookie、按 IP 的登录限流。
- **caddy** 用内置 CA 终结 HTTPS，所以没有域名、只有裸 IP 也能用；并把 dsh 的 `401` 变成自动登录。
- **dsh** 运行 agent 本体，全部状态落在宿主机，重启、重建、升级都不丢。

成员打开一个地址、输入密码，就进了 dsh。

## 特性

- **账号取代共享密钥。** 每人一个登录，可单独撤销，无需重启。
- **自动登录。** 登录之后成员再也看不到 token URL：dsh 的 token 每分钟同步一次，由登录门自动签发。
- **无域名的 HTTPS。** 证书来自 Caddy 内置 CA，以 IP 地址作为 SAN；成员导入一次根证书即可。
- **钉版、可控的升级。** dsh 版本是 `.env` 里的一个变量，什么都不会浮动。
- **重建不丢状态。** 会话、凭据、插件，甚至容器的 home 目录，都在宿主机目录里。
- **开箱即用的 agent 镜像。** git、带中日韩与 emoji 字体的 chromium、GitHub CLI、pnpm、mnemon 都已烘进镜像。
- **覆盖整条链路的冒烟测试。** `scripts/smoke.sh` 对运行中的栈验证登录门、token 换取、自动登录、WebSocket 透传、撤销与 token 轮换。
- **可选的公网入口。** 独立容器里的 Cloudflare Tunnel 不在宿主机开任何端口。由 Cloudflare Access 决定谁能进，登录门照样生效。
- **一个地址，只配一次。** `DSH_TRUSTED_HOST` 同时驱动 Caddy 站点、证书和 dsh。

## 架构

```
            成员浏览器（私有网络：局域网、VPN、Tailscale 子网路由……）
                             | https://<DSH_TRUSTED_HOST>
                             v
+---------------------------- 宿主机：docker compose ----------------------------+
|  caddy  :80 -> :443  （TLS 来自内置 CA）                                        |
|    |- /auth/* ------------------> auth-gate :8080   登录页 / 登录 / 门禁检查       |
|    '- 其余路径                                                                   |
|         forward_auth /auth/check --> auth-gate   无会话？302 跳登录页              |
|         reverse_proxy -------------> dsh :3080      agent Web UI（SSE、WebSocket） |
|                                           | 401                                  |
|                                           +-> 302 /auth/dsh-entry -> /?token=...  |
|                                                                                 |
|  cron，每分钟：scripts/sync-token.sh --> auth-gate/data/dsh-token                  |
+---------------------------------------------------------------------------------+
  宿主机上的状态：dsh-home/  dsh-root/  workspace/  auth-gate/data/  caddy/data/
```

没有会话的请求会被重定向到登录页。登录门的 cookie 有效之后，第一个到达 dsh 的请求会返回 `401`；Caddy 把它改写成跳转 `/auth/dsh-entry`，auth-gate 签发当前 token，dsh 下发自己的会话 cookie。此后成员不再需要参与。

这套栈可以有第二个入口，给既不在局域网也不在 VPN 里的成员用：Cloudflare Tunnel（[`compose.tunnel.yml`](compose.tunnel.yml)，不启用就不存在）。

```
        成员浏览器（互联网上的任何地方）
                             | https://<TUNNEL_HOSTNAME>
                             v
                     Cloudflare（Access 决定谁能进）
                             | 隧道：由 cloudflared 保持的连接，只有出站
                             v
+---------------------------- 宿主机：docker compose ----------------------------+
|  cloudflared   （compose.tunnel.yml，可选）                                    |
|    | http://caddy:8080    在自己的网络里：到得了 caddy，别的都到不了           |
|    v                                                                           |
|  caddy，第二个监听器  -->  与上图相同的登录门和路由                            |
+--------------------------------------------------------------------------------+
```

## 环境要求

- 一台 **amd64（x86-64）** 的 Linux 宿主机，装有 Docker Engine 与 Compose v2（实测 Docker 29.7、Compose v5.5），以及 `curl`、`openssl`、`jq`。宿主机不需要 Go 或 Node。
- 80 与 443 端口空闲，且成员能经**私有**网络访问（局域网、VPN、Tailscale 子网路由……）。
- dsh 镜像 3.4GB，另需构建空间；构建期间需要联网（Docker Hub、Debian 源、npm、GitHub Releases、gcr.io）。
- 宿主机上有 **cron** 守护进程在运行：token 同步任务以 root 身份执行，root 必须能运行 `docker compose`。
- 两个一次性步骤需要 `sudo`（读取根证书、安装 cron）。

> [!IMPORTANT]
> 这套栈是为私有网络设计的，不要靠端口转发把它暴露到公网：见[安全模型](#安全模型)。要加公网入口，受支持的方式是 [Cloudflare Tunnel](docs/operations.zh-CN.md#cloudflare-tunnel)。

## 快速开始

```sh
git clone https://github.com/StanleyChanH/dsh-server.git
cd dsh-server
```

1. **配置。** 复制模板并填两项：

   ```sh
   cp .env.example .env
   ${EDITOR:-vi} .env
   ```

   - `DSH_TRUSTED_HOST`：成员在浏览器里输入的 IP 或域名（不带协议和端口）。
   - `AUTH_GATE_SECRET`：`openssl rand -hex 32` 的输出。

2. **构建镜像。** 不要加 `--pull`；如果构建机访问不了 gcr.io，先读[镜像来源](docs/operations.zh-CN.md#构建前必读镜像来源)。

   ```sh
   docker compose build
   ```

3. **建第一个账号。** 栈还没启动时也可以执行。

   ```sh
   scripts/add-user.sh alice
   ```

4. **启动。**

   ```sh
   docker compose up -d
   docker compose ps    # dsh 与 auth-gate 应为 healthy
   ```

5. **信任根证书。** 把它从宿主机取出来，在你的设备上导入为受信 CA（各系统做法见[加成员](docs/operations.zh-CN.md#加成员)）。

   ```sh
   sudo cat caddy/data/caddy/pki/authorities/local/root.crt > dsh-server-root.crt
   ```

6. **安装 token 同步任务。** 每分钟运行一次；dsh 重启后自动登录能恢复，靠的就是它。

   ```sh
   sudo tee /etc/cron.d/dsh-server-token >/dev/null <<EOF
   SHELL=/bin/sh
   PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
   * * * * * root $(pwd)/scripts/sync-token.sh
   EOF
   ```

   约一分钟后，`auth-gate/data/dsh-token` 应当出现，并且每分钟更新一次时间戳。如果没有，手动跑一次 `sudo scripts/sync-token.sh` 看原因：cron 得在运行，而且 root 得能运行 `docker compose`。

7. **验证。** 冒烟测试走一遍整条链路，其中会重启一次 dsh：

   ```sh
   scripts/smoke.sh
   ```

   如果它在 §0 停下并提示 token 文件的问题，说明第 6 步的 cron 还没正常工作。

现在打开 `https://<DSH_TRUSTED_HOST>`，用 `alice` 登录。如果浏览器提示证书问题，说明这台设备还没导入第 5 步的根证书。

## 配置

所有变量都在 `.env` 里（模板：[`.env.example`](.env.example)）。改完之后运行 `docker compose up -d`：compose 会重建环境变量变了的容器（改 `DSH_TRUSTED_HOST` 时是 caddy 和 dsh；dsh 重启会打断正在运行的任务）。单纯 `restart` 沿用旧的环境变量。

| 变量 | 用途 | 默认 / 示例 |
| --- | --- | --- |
| `DSH_TRUSTED_HOST` | **必填。** 成员访问用的地址，不带协议和端口，例如 `192.0.2.10` 或 `dsh.example.org`。驱动 Caddy 站点与证书、dsh 的可信主机，以及默认的对外 URL。 | 无 |
| `DSH_VERSION` | **必填。** 要安装的 dsh 版本。 | `0.2.2-rc.5` |
| `AUTH_GATE_SECRET` | **必填。** 登录会话的签名密钥（`openssl rand -hex 32`）。 | — |
| `DSH_PUBLIC_URL` | dsh 对外公告的地址。仅当它必须与 `https://<DSH_TRUSTED_HOST>/` 不同时才设置。 | 自动推导 |
| `AUTH_SESSION_HOURS` | 登录会话时长（小时）。 | `720`（30 天） |
| `LAN_PROXY` | 镜像构建与 dsh 会话内进程使用的出网代理。留空即关闭。 | 空 |
| `NO_PROXY` | 不走代理的地址。 | 私有网段 |
| `GOPROXY` | 构建 auth-gate 用的 Go 模块代理。 | `https://goproxy.cn,direct` |
| `COMPOSE_FILE`、`TUNNEL_TOKEN`、`TUNNEL_HOSTNAME` | 可选：启用 Cloudflare Tunnel（[做法](docs/operations.zh-CN.md#cloudflare-tunnel)）。 | 不设置 |

## 日常命令

| 事项 | 命令 |
| --- | --- |
| 加成员 / 重置密码 | `scripts/add-user.sh <名字>` |
| 跟日志 | `docker compose logs -f` |
| 重启 dsh（token 轮换；自动登录约 1 分钟内恢复） | `docker compose restart dsh` |
| 停 / 起 | `docker compose stop` / `docker compose up -d` |
| 端到端自检 | `scripts/smoke.sh` |
| 升级 dsh | 改 `.env` 的 `DSH_VERSION`，然后 `docker compose build dsh && docker compose up -d dsh && scripts/smoke.sh` |
| `git pull` 之后更新 | `docker compose build && docker compose up -d`，然后 `scripts/smoke.sh` |
| 换地址 | 改 `.env` 的 `DSH_TRUSTED_HOST`，然后 `docker compose up -d`（会重建 caddy 和 dsh） |
| 开启 / 关闭公网入口 | 按运维手册改 `.env`，然后 `docker compose up -d`（[Cloudflare Tunnel](docs/operations.zh-CN.md#cloudflare-tunnel)） |
| 删成员、备份、排障 | [docs/operations.zh-CN.md](docs/operations.zh-CN.md) |

## 安全模型

- **默认只在私有网络。** dsh 会执行模型生成的命令，是 developer preview，且未经安全审计。限流按可信网络设计，TLS 来自私有 CA，dsh 的 token 会在 URL 里出现一次。不要把 80/443 开放到公网；成员异地访问就在前面加 VPN（Tailscale、WireGuard 之类），或者用不开端口的 [Cloudflare Tunnel](docs/operations.zh-CN.md#cloudflare-tunnel)。启用隧道后，公网主机名前面必须保留 Cloudflare Access：登录限流挡不住整个互联网。没有 Access 时冒烟测试会失败。
- **登录门只决定谁能进，不区分数据是谁的。** 全员共用同一个 dsh 实例：同一批会话、凭据和工作区。dsh 内部没有按成员的隔离。
- **容器即边界。** agent 只能碰挂载进容器的东西：`dsh-home/`、`workspace/`、`dsh-root/`。dsh 容器以 root 运行，没有挂 docker socket——不要给它加 `privileged`、宿主机目录或 docker socket。
- **落盘的机密。** `.env` 里有会话密钥；`dsh-home/` 里有模型 API key；`auth-gate/data/` 里有密码哈希和当前的 dsh token。它们都不进 git，备份也要保护好。
- **Docker 发布的端口绕过 ufw。** 启用宿主机防火墙时要显式写 `DOCKER-USER` 规则。

## 升级 dsh

dsh 是 developer preview，版本之间会有破坏性变更，所以升级要手动、一次一版：

1. 改 `.env` 的 `DSH_VERSION`。
2. `docker compose build dsh`（不加 `--pull`），然后 `docker compose up -d dsh`。
3. 跑 `scripts/smoke.sh`。全绿才算升级完成；出现失败就把版本改回去重来。

会话和设置都保留，已登录的成员感知不到切换。细节（如何回滚、日志格式变了怎么办）见[升级 dsh](docs/operations.zh-CN.md#升级-dsh)。

## 更新 dsh-server

容器沿用创建时的环境变量，`docker compose restart` 改不了它。所以变更一律靠**重建**容器来生效：

| 之后要做的操作 | 命令 |
| --- | --- |
| `git pull` 之后 | `docker compose build && docker compose up -d`，然后 `scripts/smoke.sh` |
| 改了 `.env` | `docker compose up -d`（地址或代理变了会重建 caddy 和 dsh；dsh 会重启） |
| 只改了 `caddy/Caddyfile` | `docker compose up -d --no-deps --force-recreate caddy` |

`docker compose up -d` 只重建配置变了的容器，其余照常运行；加 `--dry-run` 可以先看它会重建哪些。

拉取更新后不要指望 `restart`：重启出来的容器仍是旧的环境变量；而且一个在 `DSH_TRUSTED_HOST` 出现之前创建的 caddy 容器，重启后加载不了现在的 Caddyfile。细节见[更新 dsh-server](docs/operations.zh-CN.md#更新-dsh-server)。

## 常见问题

**可以用域名吗？**
可以：`DSH_TRUSTED_HOST` 接受域名。TLS 仍然来自内置 CA（`tls internal`），所以无论用 IP 还是域名，成员都要导入根证书。

**成员不在局域网或 VPN 里，能访问吗？**
可以，走 [Cloudflare Tunnel](docs/operations.zh-CN.md#cloudflare-tunnel)：宿主机不开端口，由 Cloudflare Access 决定谁能进，登录门照样生效。需要一个 Cloudflare 账号和托管在那里的域名。不要改成把 443 端口转发到宿主机。

**该钉哪个 dsh 版本？**
`.env.example` 里的是本栈验证过的版本。升级前先在 npm 上查一下 `@deepseek-ai/dsh`，升级后务必跑冒烟测试。

## 文档

| 文档 | 内容 |
| --- | --- |
| [运维手册](docs/operations.zh-CN.md)（[English](docs/operations.md)） | 首次部署、冒烟自检、启停、更新整套栈、升级、加成员、可选的 Cloudflare Tunnel、token URL、自动换乘与降级模式、备份还原、排障、已知风险 |
| [设计](docs/design.zh-CN.md)（[English](docs/design.md)） | 目标与非目标、约束、三个服务背后的取舍、历次设计增量 |

## 参与贡献

欢迎贡献：问题报告、文档修正、想法都有帮助。开发环境、CI 检查项与提交约定见 [CONTRIBUTING.md](CONTRIBUTING.md)。漏洞请不要开 issue，见 [SECURITY.md](SECURITY.md)。

## 许可证

[MIT](LICENSE)

## 致谢

- DeepSeek 的 [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness)，本栈部署的 agent。dsh-server 是独立项目，与 DeepSeek 无隶属关系，也未获其背书。
- [Caddy](https://caddyserver.com/)，反向代理；dsh 镜像还打包了 [Chromium](https://www.chromium.org/)、[GitHub CLI](https://cli.github.com/)、[pnpm](https://pnpm.io/) 与 [mnemon](https://github.com/mnemon-dev/mnemon)，各自遵循其自身许可证。
