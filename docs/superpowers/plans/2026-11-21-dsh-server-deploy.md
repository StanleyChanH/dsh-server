# dsh 团队服务器部署栈 Implementation Plan

> 历史文档：按当时的仓库布局写成。文中提到的 `README.md` 现为 `docs/operations.zh-CN.md`，`DESIGN.md` 现为 `docs/design.zh-CN.md`；地址与路径以文档占位符表示。


> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用 docker compose 把 DeepSeek Harness 部署为团队常驻 agent 服务器：auth-gate 登录门 + Caddy 反代（tls internal）+ dsh 容器，团队经 `https://192.0.2.10` 访问。

**Architecture:** 三容器一 compose：`caddy`（公网入口，443/80，forward_auth 委托鉴权）→ `auth-gate`（Go 单二进制：登录页 + bcrypt + HMAC cookie）与 `dsh`（npm 官方包，DSH_HOME 与 workspace 挂载宿主机）。dsh 不发布端口，仅 compose 内网可达。

**Tech Stack:** Go 1.24（标准库 + golang.org/x/crypto/bcrypt）、node:24-slim、caddy:2.10-alpine、docker compose v5。

**Spec:** `/path/to/dsh-server/DESIGN.md`（部署设计）+ `/path/to/dsh-server/auth-gate/login.html`（已批准的登录页，界面文案与交互以此文件为准，不得改动 UI 文案）。

## Global Constraints

- dsh 版本锁定 `0.2.2-rc.5`（npm `@deepseek-ai/dsh`），基础镜像 `node:24-slim`（Node >= 24.9）
- 一切经 docker compose 管理，宿主机零安装；所有状态路径在 `/path/to/dsh-server/` 下
- dsh 容器：`DSH_HOME=/data/dsh`（←`./dsh-home`）、`/workspace`（←`./workspace`），3080 端口不发布到宿主机
- dsh 启动参数：`dsh web --no-open --host <容器自身IP(hostname -i)> --port 3080 --public-url $DSH_PUBLIC_URL --trusted-host 192.0.2.10`
- 认证 cookie：名 `gate_session`，HttpOnly + Secure + SameSite=Lax，寿命 `AUTH_SESSION_HOURS`（默认 720h）；HMAC-SHA256，密钥 `AUTH_GATE_SECRET`（32 字节 hex）
- bcrypt cost 12；users.json 为扁平 map `{"用户名":"$2a$12$..."}`，存放 `auth-gate/data/users.json`（挂载 `/data`）
- Caddy 站点 `https://192.0.2.10`，`tls internal`，**禁用 `encode`**（SSE/WebSocket 流不能被压缩缓存）
- 所有服务 `restart: unless-stopped`，日志 json-file 上限 10m×3
- `.env` 含密钥，进 `.gitignore`；其余文件 git 跟踪（Task 1 `git init`）

## Review Focus

1. **`/auth/check` 未授权时的重定向闭环**：浏览器访问任意路径被 401 后必须带 `Location: /auth/login?next=<原路径>`（取 `X-Forwarded-Uri`）→ 登录成功回到原路径。→ Task 3 测试 `TestCheckUnauthorizedRedirectsToLogin`。
2. **cookie 篡改/过期必须失败关闭**：改一位签名、过期值、已删除用户都要 401。→ Task 1 测试 `TestVerifyRejectsTampered/Expired/UnknownUser`。
3. **限流只数失败且成功后清零**：同 IP 5 次错误密码后第 6 次（即使密码正确）应 429；成功登录重置计数。→ Task 2 测试 `TestRateLimit`。
4. **流式透传**：Caddy 配置无压缩；WebSocket 升级透传——集成冒烟验证 101。→ Task 7 步骤 5。
5. **dsh 重启换 token 的运维闭环**：`scripts/dsh-url.sh` 必须能从 logs 提取最新启动 URL。→ Task 7 步骤 6。

---

### Task 1: git 仓库与 auth-gate HMAC cookie

**Files:**
- Create: `.gitignore`
- Create: `auth-gate/go.mod`、`auth-gate/cookie.go`
- Test: `auth-gate/cookie_test.go`

**Interfaces:**
- Produces: `Sign(user string, expires time.Time) string` 与 `Verify(value string, userExists func(string) bool) (string, error)`（包名 `main`，签名密钥来自包级变量 `SessionSecret []byte`，Task 3 的 main 里赋值）
- Produces: `.gitignore` 含 `.env`、`dsh-home/`、`workspace/`、`caddy/data/`、`caddy/config/`、`auth-gate/data/`

- [ ] **Step 1: `git init` + 写 `.gitignore`（内容如上）+ 首次提交**

- [ ] **Step 2: `go mod init`（module 名 `dsh-server/auth-gate`）并 `go get golang.org/x/crypto@latest`，提交 go.mod/go.sum**

- [ ] **Step 3: 写失败测试 `cookie_test.go`**

测试用例：`TestSignVerifyRoundTrip`（签名→验签返回用户名）；`TestVerifyRejectsTampered`（改签名末位字符 → error）；`TestVerifyRejectsExpired`（expires 为过去 → error）；`TestVerifyRejectsUnknownUser`（userExists 返回 false → error）；`TestVerifyMalformed`（乱串 → error）。

- [ ] **Step 4: `go test ./...` 确认 FAIL（未定义符号）**

- [ ] **Step 5: 实现 `cookie.go`**

cookie 值格式 `base64url(user|unix秒) + "." + base64url(hmac_sha256(secret, user|unix秒))`；Verify 用 `hmac.Equal` 常数时间比较。SessionSecret 为空时 Verify 一律失败。

- [ ] **Step 6: `go test ./...` PASS，提交**

---

### Task 2: 用户存储、bcrypt 校验与限流

**Files:**
- Create: `auth-gate/auth.go`
- Test: `auth-gate/auth_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `type Users struct`，方法 `LoadUsers(path string) error`（启动加载 + `loginAt` mtime 变化时自动重载）、`Check(username, password string) bool`（bcrypt.CompareHashAndPassword）；`type Limiter struct`，方法 `NewLimiter(max int, window time.Duration) *Limiter`、`Fail(ip string)`、`Allowed(ip string) bool`、`Reset(ip string)`

- [ ] **Step 1: 写失败测试 `auth_test.go`**

`TestCheckValidPassword`（bcrypt hash 对应明文 → true）；`TestCheckWrongPassword/UnknownUser`（false）；`TestReloadOnFileChange`（写新 users.json 后无需重启，新用户可过）；`TestRateLimit`（max=5, window=1min：5 次 Fail 后 `Allowed` false，`Reset` 后 true；window 过期后 Allowed 恢复 true——用可注入的 now 函数或短 window 实现）。

- [ ] **Step 2: `go test ./...` 确认 FAIL**

- [ ] **Step 3: 实现 `auth.go`**

bcrypt cost 12；Limiter 固定窗口 map[ip]{count, windowStart}，惰性清理过期项；Limiter 的 now 用包级变量 `nowFunc = time.Now` 便于测试注入。

- [ ] **Step 4: `go test ./...` PASS，提交**

---

### Task 3: HTTP handlers 与登录页服务

**Files:**
- Create: `auth-gate/handlers.go`、`auth-gate/main.go`
- Modify: `auth-gate/login.html`（无改动，仅 `go:embed` 引用）
- Test: `auth-gate/handlers_test.go`

**Interfaces:**
- Consumes: Task 1 `Sign/Verify`、Task 2 `Users/Limiter`
- Produces: 路由（http.ServeMux 标准模式）：
  - `GET /auth/login` → 返回嵌入的 login.html（`Content-Type: text/html; charset=utf-8`）
  - `POST /auth/login` → JSON `{username,password}`：成功 200 + Set-Cookie（302 不需要，前端跳转）；密码错 401 文本 `凭证不匹配`；限流 429 文本 `尝试过多`
  - `GET /auth/check` → cookie 有效 200 `ok`；否则 401 + `Location: /auth/login?next=<X-Forwarded-Uri 的 path+query，缺省="/">`
  - `GET /auth/ping` → 200 `pong`（healthcheck 用）
- main 读 env：`AUTH_GATE_SECRET`（hex 解码入 SessionSecret，为空则启动失败）、`AUTH_USERS`（默认 `/data/users.json`）、`AUTH_SESSION_HOURS`（默认 720）、`AUTH_ADDR`（默认 `:8080`）；容器入口 `exec /gate`；客户端 IP 取 `X-Forwarded-For` 第一段

- [ ] **Step 1: 写失败测试 `handlers_test.go`（httptest + 手工塞 RemoteAddr/XFF）**

`TestLoginPageServed`（GET /auth/login → 200 且含 `连接到节点`）；`TestLoginSuccessSetsCookie`（POST 正确凭证 → 200，Set-Cookie 含 `gate_session`、`HttpOnly`、`Secure`、`SameSite=Lax`）；`TestLoginWrongPassword`（401）；`TestRateLimitReturns429`（5 错后 POST 正确密码 → 429）；`TestCheckOkWithCookie`（带 Task 1 签的 cookie → 200）；`TestCheckUnauthorizedRedirectsToLogin`（无 cookie + `X-Forwarded-Uri: /x/y?z=1` → 401，`Location: /auth/login?next=%2Fx%2Fy%3Fz%3D1`）。

- [ ] **Step 2: `go test ./...` 确认 FAIL**

- [ ] **Step 3: 实现 handlers.go + main.go（login.html 用 `//go:embed login.html`，users/limiter 单例挂 handler struct）**

- [ ] **Step 4: `go test ./...` PASS，提交**

---

### Task 4: adduser 子命令与 auth-gate 镜像

**Files:**
- Modify: `auth-gate/main.go`（加 `-adduser`、`-ping` 子命令分支）
- Create: `auth-gate/Dockerfile`
- Test: `auth-gate/main_test.go`

**Interfaces:**
- Consumes: Task 3 main 结构
- Produces: `gate -adduser <name>`（stdin 读一行明文 → bcrypt → 写入/更新 users.json，输出 `user <name> updated`）；`gate -ping`（GET localhost:$AUTH_ADDR/auth/ping 期待 pong，退出码 0/1）；镜像 `dsh-server-auth-gate:latest`（多阶段：`golang:1.24-alpine` 构建 → `gcr.io/distroless/static` 运行，二进制名 `/gate`，`AUTH_USERS=/data/users.json`，VOLUME 无所谓，挂载由 compose 管）

- [ ] **Step 1: 失败测试 `TestAddUserWritesFile`（临时目录 users.json：adduser alice/pw → LoadUsers 后 Check(alice,pw)==true，原用户仍在）**

- [ ] **Step 2: FAIL 确认 → 实现 → PASS，提交**

- [ ] **Step 3: 写 Dockerfile 并 `docker build -t dsh-server-auth-gate ./auth-gate` 成功（含 CGO_ENABLED=0），提交**

- [ ] **Step 4: 冒烟：`docker run --rm -e AUTH_GATE_SECRET=<64hex> dsh-server-auth-gate -ping` 容器另起后再验证——简化为：起一个临时容器 serve，host 上 `curl -s localhost:18080/auth/login | grep 连接到节点` 成功后清理，提交（如有修复）**

---

### Task 5: dsh 镜像

**Files:**
- Create: `dsh/Dockerfile`、`dsh/entrypoint.sh`

**Interfaces:**
- Produces: 镜像 `dsh-server-dsh:latest`：`FROM node:24-slim`，apt 装 `git curl ca-certificates`（no-install-recommends，清 apt lists），`npm i -g @deepseek-ai/dsh@0.2.2-rc.5`；`ENV DSH_HOME=/data/dsh`；`WORKDIR /workspace`；entrypoint.sh 逻辑：`IP=$(hostname -i | awk '{print $1}')`，`exec dsh web --no-open --host "$IP" --port 3080 --public-url "${DSH_PUBLIC_URL:-https://192.0.2.10/}" --trusted-host "${DSH_TRUSTED_HOST:-192.0.2.10}"`

- [ ] **Step 1: 写 Dockerfile + entrypoint.sh（chmod +x，shellcheck 若有则过一遍），提交**

- [ ] **Step 2: `docker build -t dsh-server-dsh ./dsh` 成功；`docker run --rm dsh-server-dsh node --version` 输出 v24.x 且 >= 24.9（`docker run --rm dsh-server-dsh dsh --version` 输出 0.2.2-rc.5），提交**

---

### Task 6: Caddyfile、compose、.env 与运维件

**Files:**
- Create: `caddy/Caddyfile`、`compose.yml`、`.env.example`、`scripts/dsh-url.sh`、`scripts/add-user.sh`、`README.md`
- Modify: `DESIGN.md`（认证段：basic_auth → auth-gate + 登录页，引用 login.html）
- Create dirs: `caddy/data/`、`caddy/config/`、`dsh-home/`、`workspace/`、`auth-gate/data/`（各放 `.gitkeep`）

**Interfaces:**
- Consumes: Task 4/5 镜像名
- Produces: `compose up -d` 可用的完整栈；`.env` 变量：`DSH_VERSION=0.2.2-rc.5`、`DSH_PUBLIC_URL=https://192.0.2.10/`、`DSH_TRUSTED_HOST=192.0.2.10`、`AUTH_GATE_SECRET=<openssl rand -hex 32>`、`AUTH_SESSION_HOURS=720`

Caddyfile 要点（精确）：站点块 `https://192.0.2.10`；全局 `admin off`；`tls internal`；无 `encode`；`handle /auth/* { reverse_proxy auth-gate:8080 }`；`handle { forward_auth auth-gate:8080 { uri /auth/check } ; reverse_proxy dsh:3080 }`；dsh healthcheck：`curl -fs "http://$(hostname -i | awk '{print $1}'):3080/" || exit 1`（30s 间隔，启动等待 60s）；auth-gate healthcheck `CMD /gate -ping`；caddy 端口 `80:80`、`443:443`。

- [ ] **Step 1: 写全部文件（README 覆盖 DESIGN"运维手册"节：启停/升级/加人/根证书分发/取 token URL/备份/排障），提交**

- [ ] **Step 2: `openssl rand -hex 32` 生成 `.env`（不提交）；`docker compose config` 校验通过，提交（若 Caddyfile/compose 有修）**

---

### Task 7: 集成冒烟与收尾

**Files:**
- Create: `scripts/smoke.sh`（保留为运维自检工具）
- Modify: 可能微调 Task 5/6 产物

**Interfaces:**
- Consumes: 完整 compose 栈

- [ ] **Step 1: `docker compose up -d --build`；`docker compose ps` 三服务 healthy/running；`docker compose logs dsh` 出现 `dsh web:` 启动 URL 行**

- [ ] **Step 2: 宿主机 `curl -sk -o /dev/null -w '%{http_code} %{redirect_url}' https://192.0.2.10/some/path` → `401 https://192.0.2.10/auth/login?next=%2Fsome%2Fpath` 类输出**

- [ ] **Step 3: 用 add-user.sh 建 `testuser`；`curl -sk -c /tmp/cj -H 'Content-Type: application/json' -d '{"username":"testuser","password":"..."}' https://192.0.2.10/auth/login` → 200；带 cookie `curl -sk -b /tmp/cj -o /dev/null -w '%{http_code}' https://192.0.2.10/` → 200（dsh 前端页面）**

- [ ] **Step 4: 冒烟后清理：删 testuser（直接编辑 users.json 或重载），确认其 cookie 变 401**

- [ ] **Step 5: WebSocket 透传验证：`curl -sk -b /tmp/cj -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -o /dev/null -w '%{http_code}' https://192.0.2.10/api/remote.mux` → 期望 101 或 dsh 侧握手响应（非 401/403/502 即通过）**

- [ ] **Step 6: `scripts/dsh-url.sh` 输出与 `docker compose logs dsh` 最新 URL 一致；根证书存在于 `caddy/data/pki/authorities/local/root.crt`**

- [ ] **Step 7: `docker compose restart dsh` → 新 token 生成、dsh-url.sh 取到新值、旧 cookie 仍过 auth-gate（dsh 侧 token 由 URL 换取，与 gate cookie 独立）**

- [ ] **Step 8: README 增补实测路径修正，全部提交；`git log --oneline` 检查提交序列完整**

---

## Self-Review 记录

- 覆盖 DESIGN.md 全部条目：容器化/入口/TLS/认证（已按登录页方案改写）/挂载/运维 ✓
- 步骤均可独立执行且无歧义；类型/接口跨任务一致（Sign/Verify、Users/Limiter、路由路径三处核对 ✓）
- 比例：计划以接口与验证命令为主，未抄写实现体 ✓
