# 401 自动换乘 Implementation Plan

> 历史文档：按当时的仓库布局写成。文中提到的 `README.md` 现为 `docs/operations.zh-CN.md`，`DESIGN.md` 现为 `docs/design.zh-CN.md`；地址与路径以文档占位符表示。


> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** gate 账号成为团队唯一登录凭证：dsh 的 401 被 Caddy 拦截后经 auth-gate `/auth/dsh-entry` 自动换乘当前 token，成员无感进入 dsh。

**Architecture:** dsh 重启才换 token（日志可提取）→ host cron 每分钟把它同步进 auth-gate 数据目录（fail-safe）→ Caddy `handle_response` 把 dsh 的 401 换成去 `/auth/dsh-entry` 的 302 → 该端点校验 gate cookie 后 302 到 `/?token=<当前>`，用 `gate_entry_retry` cookie 防同步窗口死循环。

**Tech Stack:** Go（auth-gate 增量）、bash+cron、Caddyfile handle_response、smoke.sh。

**Spec:** `/path/to/dsh-server/DESIGN.md` 的"增量设计：401 自动换乘（2026-11-22，已确认）"一节。

## Global Constraints

- 栈在本机 LIVE 运行（192.0.2.10），可短暂重建 auth-gate/caddy，**绝不 `docker compose down`**、不动 dsh 容器
- host 无 Go：测试经 `docker run --rm -v "$PWD/auth-gate:/app" -w /app -v dsh-go-cache:/go -e GOPROXY=https://goproxy.cn,direct golang:1.24-alpine go test ./...`；跑后 `chown -R dsh:dsh auth-gate/`
- 绝不 `docker build --pull`（gcr.io 本地打标）；login.html 冻结不改；.env 不提交
- token 文件：容器内路径 = `filepath.Dir(AUTH_USERS)/dsh-token`（默认 `/data/dsh-token`，宿主机 `auth-gate/data/dsh-token`）；内容仅 token 串；**宿主侧最终属主 1000:1000、0600**（cron 以 root 跑，写完必须 chown/chmod，否则 uid 1000 的 auth-gate 读不到——Task 2 验证项）
- retry cookie：名 `gate_entry_retry`，值=unix 秒，MaxAge 35s，HttpOnly，Path=/
- 解析失败 fail-safe：**不动现有 token 文件**（旧值自然失效 + retry guard 挡循环，cron 下轮修复）
- 拦截语义：dsh 的一切 401 都换乘 302（含非浏览器客户端）——已裁定接受，README 记录

## Review Focus

1. **同步窗口死循环**（dsh 重启后 ≤1 分钟）：第二次到达 /auth/dsh-entry 必须出"同步中"页而非再 302 → Task 1 `TestDshEntryRetryGuard`
2. **token 文件缺失/为空**：不得 302 到 `/?token=`（空 token）→ 同步页 → Task 1 `TestDshEntryNoTokenFile`
3. **无 gate cookie 直接打 /auth/dsh-entry**：不得泄露 token → 302 登录页 → Task 1 `TestDshEntryRequiresGateCookie`
4. **sync 写坏值/属主错**：解析失败不动旧文件；成功后 1000:1000 0600 → Task 2 验证步骤
5. **换乘链整体**（Caddy 拦截 + entry + token 有效）：带 gate cookie 无 dsh cookie 的 GET / 最终非 401 → Task 3 smoke 断言

---

### Task 1: auth-gate `/auth/dsh-entry` 端点

**Files:**
- Modify: `auth-gate/handlers.go`（新 handler）、`auth-gate/main.go`（路由 + 装配）
- Test: `auth-gate/handlers_test.go`

**Interfaces:**
- Consumes: Task 1 前置已有 `Verify`、`Users.Exists`、`gate_session` cookie（Sign 产）
- Produces: 路由 `/auth/dsh-entry`（不限方法）；`type TokenStore struct`：`LoadToken(path string) error`（启动加载 + mtime 变化自动重载，模式同 Users）与 `Current() string`（缺失/空返回 ""）；handler 语义：
  - 无有效 gate cookie → 302 `/auth/login?next=/`
  - `Current()==""` → 200 `text/html; charset=utf-8` 极简同步页（文案含"凭证同步中，约 1 分钟后自动恢复"）
  - retry cookie `gate_entry_retry` 值（unix 秒）距今 <30s → 200 同一同步页
  - 否则 → 302 `/?token=<Current()>` + Set-Cookie `gate_entry_retry=<now>`（MaxAge 35s，HttpOnly，Path=/）
- 同步页为 handlers.go 内 const 字符串（<1KB，无外链，深底浅字即可，无需复用 login.html 风格）

- [ ] **Step 1: 失败测试（4 用例）**：`TestDshEntryRedirectsWithToken`（有效 gate cookie + token 文件含 `tok123` → 302 `/?token=tok123`，Set-Cookie 含 gate_entry_retry）；`TestDshEntryRequiresGateCookie`（无 cookie → 302 `/auth/login?next=%2F`，body 无 tok123）；`TestDshEntryRetryGuard`（带 5 秒前的 retry cookie + 有效 gate cookie → 200，body 含"同步中"，无 Location 302）；`TestDshEntryNoTokenFile`（有效 cookie、无 token 文件 → 200 同步页）
- [ ] **Step 2: docker go test 确认 FAIL**
- [ ] **Step 3: 实现 TokenStore（mtime 缓存读，模式照抄 Users）+ handler + main 装配（token 路径 `filepath.Dir(usersPath)/dsh-token`）**
- [ ] **Step 4: docker go test 全绿（含既有全部用例）**
- [ ] **Step 5: `docker compose build auth-gate && docker compose up -d auth-gate`（勿 --pull），`curl -sk https://192.0.2.10/auth/dsh-entry -o /dev/null -w '%{http_code}'` → 302（无 cookie 走登录），提交**

### Task 2: sync-token 脚本、cron 与 Caddy 拦截

**Files:**
- Create: `scripts/sync-token.sh`（可执行）
- Modify: `caddy/Caddyfile`（reverse_proxy dsh:3080 块内加响应拦截）
- Host: `/etc/cron.d/dsh-server-token`（安装步骤，不入库）

**Interfaces:**
- Consumes: Task 1 的 token 文件路径（宿主机 `auth-gate/data/dsh-token`，容器 `/data/dsh-token`）
- Produces: `scripts/sync-token.sh`：`docker compose logs dsh --no-color` 提取最新 token（复用 dsh-url.sh 的解析逻辑，抽公共函数或复制其 grep，二选一由实现者定并说明）；校验形态 `^[A-Za-z0-9_-]{20,}$` 之类非空 token 串（从实际日志确认字符集）；失败→exit 1 不动现有文件；成功→temp+rename 原子写，`chown 1000:1000` `chmod 600`。cron 行：`* * * * * root /path/to/dsh-server/scripts/sync-token.sh`（/etc/cron.d/dsh-server-token，0644）。Caddyfile 追加（在现有 reverse_proxy dsh:3080 块内）：`@dsh_unauth status 401` + `handle_response @dsh_unauth { redir /auth/dsh-entry 302 }`

- [ ] **Step 1: 写 sync-token.sh；手动跑一次：文件出现、内容=当前 logs 里的 token、属主 1000:1000、0600**
- [ ] **Step 2: 构造解析失败（临时改 grep 匹配不存在的模式或 stub）验证旧文件未被触碰、exit 1；恢复后重跑成功**
- [ ] **Step 3: 改 Caddyfile + `docker compose exec caddy caddy validate --config /etc/caddy/Caddyfile`（或宿主 caddy validate 等价物）→ restart caddy（`docker compose restart caddy`，秒级）**
- [ ] **Step 4: 实链验证：清 dsh cookie 场景下 `curl -sk -b gate-only-cookiejar -o /dev/null -w '%{http_code} %{redirect_url}' https://192.0.2.10/` → 302 去 `/auth/dsh-entry`（gate cookie 用 alice 或临时 smoketest 账号现取）**
- [ ] **Step 5: 安装 /etc/cron.d/dsh-server-token；提交脚本与 Caddyfile（cron 文件在提交信息里注明安装位置）**

### Task 3: smoke 断言、README 与降级验证

**Files:**
- Modify: `scripts/smoke.sh`（新段落）、`README.md`

**Interfaces:**
- Consumes: Task 1 端点行为 + Task 2 同步与拦截
- Produces: smoke.sh 新段（§10 自动换乘）：建临时 gate 用户 → 登录取 cookie → **不带 dsh cookie** `curl -skL -b jar -c jar https://192.0.2.10/ -o /dev/null -w '%{http_code}'` 跟完跳转链 → 非 401 即过；`scripts/sync-token.sh` 退出 0 且文件 token 与 logs 最新一致。README：新成员接入改两样（账号+根证书）；升级段加"必跑 smoke.sh 全绿"；新增"自动换乘与降级模式"小节（含 dsh 升级破坏日志格式时的手动回退：dsh-url.sh）

- [ ] **Step 1: smoke.sh 加 §10（沿用既有 check() 风格与临时用户清理模式，勿破坏既有段落）；跑 `scripts/smoke.sh` 全绿 exit 0**
- [ ] **Step 2: 降级演练（只读验证，跑完恢复）：临时移走 `auth-gate/data/dsh-token` → 带新 gate cookie 的浏览器链（curl 模拟）得"同步中"页而非死循环 → 移回/等 cron 下一分钟自愈 → smoke §10 复绿**
- [ ] **Step 3: README 三处更新（新成员两样、升级必跑 smoke、自动换乘小节）；DESIGN.md 状态行改"已实施"**
- [ ] **Step 4: 提交；`git log --oneline` 核对本计划提交序列**

---

## Self-Review 记录

- Spec 覆盖：DESIGN 增量四组件（端点/同步/拦截/smoke+README）各归 Task 1/2/2/3 ✓；两个已确认代价属决策记录无需实现项
- Review Focus 5 条各有归属测试/验证 ✓
- 类型一致：TokenStore.LoadToken/Current、gate_entry_retry、/auth/dsh-entry 三任务一致 ✓
- 比例：接口+验证为主，无实现体 ✓
