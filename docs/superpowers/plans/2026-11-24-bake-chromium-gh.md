# chromium/字体与 gh 烘进镜像 Implementation Plan

> 历史文档：按当时的仓库布局写成。文中提到的 `README.md` 现为 `docs/operations.zh-CN.md`，`DESIGN.md` 现为 `docs/design.zh-CN.md`；地址与路径以文档占位符表示。


> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 重建 dsh 容器后 chromium（含中文/emoji 渲染）与 gh 仍在位，且升级 dsh 的构建只重装 dsh 本体。

**Architecture:** 沿用职责三分——确定性依赖进镜像、挂载管运行期状态、entrypoint 只做挂载后补链。`dsh/Dockerfile` 在现有 apt 层与 npm 层之间新增两层（chromium+字体走 apt；gh 取 GitHub Releases 钉版 .deb 并校验 sha256）；entrypoint 退役 `opt/gh` 补链；smoke §0 增加镜像依赖在位检查。

**Tech Stack:** Dockerfile（node:24-slim / Debian 12 bookworm）、POSIX sh、bash（smoke.sh）。

**Spec:** `docs/superpowers/specs/2026-11-24-bake-chromium-gh-design.md`

## Global Constraints

- 栈在本机 LIVE 运行：**绝不 `docker compose down`、绝不 `--pull`**；`docker compose up -d dsh` 与 `scripts/smoke.sh`（会 restart dsh）只在 Task 4、用户点头之后执行。Task 1–3 只打镜像标签，不碰运行中的容器。
- 构建前先打回退标签：`docker tag dsh-server-dsh:latest dsh-server-dsh:pre-bake-20261124`。
- 钉版值（逐字）：`GH_VERSION=2.105.0`；sha256 `1dac939af3e84fb3861927c984b357531341be9403b889e4aa86e8822145a09d`；apt 包 `chromium fonts-noto-cjk fonts-noto-color-emoji fonts-liberation`，一律 `--no-install-recommends`。
- 新增两层位于现有 apt 层（`dsh/Dockerfile:8-10`）之后、`ARG DSH_VERSION` 之前。
- 一次性容器一律 `docker run --rm --pull=never --entrypoint sh`；需出网的加 `-e http_proxy -e https_proxy -e no_proxy`。
- 临时文件只放 `/tmp/dsh-bake-verify/`（仓库外）。不读、不打印 `.env`（只允许 `grep -c` 取计数）。
- 不动 `.env`（dsh 版本不变）、`dsh-home/`、`dsh-root/`、`workspace/`。
- 注释只写 why，至多两行。提交信息沿用仓库风格（中文正文 + `Co-Authored-By`）。

## Review Focus

1. **chromium 以 root 运行**：不带 `--no-sandbox` 应明确报错而非静默，带上则能出图 → Task 1 Step 6
2. **中文/emoji 渲染成豆腐块**：`fc-match` 应命中 Noto Sans CJK SC / Noto Color Emoji / Liberation Sans，截图目检无缺字 → Task 1 Step 5–6
3. **gh.deb 在下载链路上被替换或损坏**：构建必须失败 → Task 1 Step 2（篡改测试）
4. **换容器后跑的仍是旧镜像或镜像缺依赖**：smoke §0 应当场 FAIL 并点名缺失命令 → Task 3 Step 2（现网红，真阳性）、Task 4 Step 2（绿）
5. **`opt/gh` 目录回来时遮蔽镜像内的 gh**：entrypoint 不得再创建 `/usr/local/bin/gh` → Task 2 Step 1/3/5

---

### Task 1: Dockerfile 新增 chromium/字体层与 gh 层

**Files:**
- Modify: `dsh/Dockerfile`（第 10 行之后插入两层）
- Modify: `README.md`（「升级 caddy / auth-gate」段落之后新增「升级 gh」段；变量表 `LAN_PROXY` 行）
- Modify: `DESIGN.md`（文末新增增量设计小节）

**Interfaces:**
- Produces: 镜像 `dsh-server-dsh:latest` 内 `/usr/bin/chromium`、`/usr/bin/gh`（2.105.0）；回退标签 `dsh-server-dsh:pre-bake-20261124`。Task 2–4 均基于此镜像。

- [ ] **Step 1: 基线（红）与回退标签**
  - `grep -c '^LAN_PROXY=http' .env` → `1`
  - `docker tag dsh-server-dsh:latest dsh-server-dsh:pre-bake-20261124`
  - `docker run --rm --pull=never --entrypoint sh dsh-server-dsh:latest -c 'command -v chromium; command -v gh'` → 无输出（现镜像两者皆无）

- [ ] **Step 2: 校验和守卫的篡改测试（对现镜像，有 curl）**
  - `docker run --rm --pull=never --entrypoint sh -e http_proxy -e https_proxy -e no_proxy dsh-server-dsh:latest -c '<下载 gh_2.105.0_linux_amd64.deb 到 /tmp/gh.deb；先用全 0 哈希、再用钉版哈希各跑一次 `echo "<哈希>  /tmp/gh.deb" | sha256sum -c -` 并打印退出码>'`
  - Expected：全 0 → `WARNING: 1 computed checksum did NOT match`、退出码 1；钉版哈希 → `/tmp/gh.deb: OK`、退出码 0

- [ ] **Step 3: 编辑 `dsh/Dockerfile`**，在第 10 行之后插入：

```dockerfile
# chromium：agent 无头浏览/截图；CJK 与 emoji 字体防豆腐块；fonts-liberation 显式
# 列出以顶替网页常见的 Arial/Times。置于 npm 层之前，升级 dsh 时不重装。
RUN apt-get update \
    && apt-get install -y --no-install-recommends chromium fonts-noto-cjk fonts-noto-color-emoji fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

# gh：Debian 12 源里只有 2.23.0（过旧），改取 GitHub Releases 钉版；sha256 防下载
# 链路（经代理）被替换。升级方式见 README「升级 dsh」。
ARG GH_VERSION=2.105.0
RUN curl -fsSL -o /tmp/gh.deb \
        "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.deb" \
    && echo "1dac939af3e84fb3861927c984b357531341be9403b889e4aa86e8822145a09d  /tmp/gh.deb" | sha256sum -c - \
    && apt-get update \
    && apt-get install -y --no-install-recommends /tmp/gh.deb \
    && rm -rf /var/lib/apt/lists/* /tmp/gh.deb
```

- [ ] **Step 4: 构建**
  - `mkdir -p /tmp/dsh-bake-verify && docker compose build dsh > /tmp/dsh-bake-verify/build.log 2>&1; echo "exit=$?"`（后台跑，npm 层约 1.39GB 会重装）
  - Expected：`exit=0`；`build.log` 里现有 apt 层为 `CACHED`，新增两层与 npm 层实际执行

- [ ] **Step 5: 版本与字体解析（绿）**
  - `docker run --rm --pull=never --entrypoint sh dsh-server-dsh:latest -c 'chromium --version; gh --version | head -1; for f in "sans-serif:lang=zh-cn" "monospace:lang=zh-cn" "Arial" "emoji"; do printf "%-24s -> %s\n" "$f" "$(fc-match "$f")"; done'`
  - Expected：`Chromium 156…`；`gh version 2.105.0`；四行依次含 `Noto Sans CJK SC`、`Noto Sans Mono CJK`、`Liberation Sans`、`Noto Color Emoji`
  - 若 `zh-cn` 命中的不是 SC：如实记录并汇报，不擅自加字体配置

- [ ] **Step 6: 渲染截图**
  - 写 `/tmp/dsh-bake-verify/render.html`：`lang="zh-CN"`，四行——sans-serif 中文（直、骨、门、关、海、画）、`font-family:Arial` 的拉丁（含粗体）、`monospace` 中英混排、30px 的 emoji（🚀 ✅ 😀 🎯）与中文混排
  - `docker run --rm --pull=never --entrypoint sh -v /tmp/dsh-bake-verify:/out dsh-server-dsh:latest -c 'chromium --headless --no-sandbox --disable-gpu --hide-scrollbars --window-size=760,260 --screenshot=/out/render.png file:///out/render.html 2>/dev/null; echo "exit=$?"; ls -la /out/render.png'`
  - Expected：`exit=0`、PNG 存在；用 Read 目检 `render.png`，四行均有字形、无方框
  - 再去掉 `--no-sandbox` 跑一次（输出改到 `/out/neg.log`）：Expected 退出码非 0，日志含 `--no-sandbox`

- [ ] **Step 7: 文档**
  - `README.md` 变量表 `LAN_PROXY` 行：`注入 dsh 构建（npm/apt）` → `注入 dsh 构建（npm/apt/gh 下载）`
  - `README.md`「升级 caddy / auth-gate」段落之后新增一段：升级 gh 改 `dsh/Dockerfile` 的 `GH_VERSION` 与 sha256（取自该版本 release 的 `gh_<版本>_checksums.txt`），再走升级 2–6 步；下载走 GitHub，构建需 `LAN_PROXY` 可用；chromium 等 apt 包只在所在层重建时刷新
  - `DESIGN.md` 文末新增「增量设计：chromium/字体与 gh 烘进镜像（2026-11-24，已实施）」：动因（手动装的 chromium 在可写层、gh 未装）；裁定 1–3——chromium+字体进镜像（包数用实测值，fonts-liberation 显式列出）、gh 取 GitHub Releases 钉版 2.105.0（Debian 12 源仅 2.23.0）、两层置于 npm 层之前（裁定 4、5 由 Task 2、3 落地时追加）；验证（Step 5–6 的实测结果）

- [ ] **Step 8: Commit** `feat(deploy): chromium/字体与 gh 烘进镜像（gh 钉 2.105.0 + sha256）`

### Task 2: entrypoint 退役 `opt/gh` 补链

**Files:**
- Modify: `dsh/entrypoint.sh:13-19`
- Modify: `README.md:101`（升级步骤 4 里的 symlink 句）
- Modify: `DESIGN.md`（增量小节的裁定列表追加第 4 条）

**Interfaces:**
- Consumes: Task 1 的镜像
- Produces: `/entrypoint.sh` 不再创建 `/usr/local/bin/gh`；mnemon 补链分支不变

- [ ] **Step 1: 红测试（挂载仓库里的现行脚本 + 假的 opt/gh）**
  - `d=$(mktemp -d /tmp/dsh-bake-verify/fakegh.XXXX) && mkdir -p "$d/gh/bin" && printf '#!/bin/sh\n' > "$d/gh/bin/gh" && chmod +x "$d/gh/bin/gh"`
  - `docker run --rm --pull=never --entrypoint sh -v "$PWD/dsh/entrypoint.sh:/entrypoint.sh:ro" -v "$d/gh:/data/dsh/opt/gh:ro" dsh-server-dsh:latest -c 'mkdir /stub; printf "#!/bin/sh\necho STUB\n" > /stub/dsh; chmod +x /stub/dsh; PATH=/stub:$PATH /entrypoint.sh >/dev/null; test ! -e /usr/local/bin/gh && echo "no-gh-symlink: PASS" || echo "no-gh-symlink: FAIL"'`
  - Expected：`no-gh-symlink: FAIL`（stub 的 `dsh` 让脚本走完补链段后正常退出）

- [ ] **Step 2: 编辑 `dsh/entrypoint.sh`**：删除 `if [ -x /data/dsh/opt/gh/bin/gh ]` 两行分支；上方注释里的 `gh/mnemon`、`这两个 symlink` 改为只说 mnemon

- [ ] **Step 3: 绿测试**：`sh -n dsh/entrypoint.sh` 无输出；重跑 Step 1 的 docker run → `no-gh-symlink: PASS`

- [ ] **Step 4: 重建** `docker compose build dsh`（只有 `COPY entrypoint.sh` 及其后重建）

- [ ] **Step 5: 从镜像验证**（不挂载脚本）：Step 1 的 docker run 去掉 entrypoint 的 `-v` → `no-gh-symlink: PASS`；`docker run --rm --pull=never --entrypoint sh dsh-server-dsh:latest -c 'command -v gh'` → `/usr/bin/gh`

- [ ] **Step 6: 文档**：`README.md:101` 的 `gh/mnemon 的 /usr/local symlink 由 entrypoint 幂等补链；apt 四件套（openssh-client/sshpass/xauth/python3）与 pnpm 已烘进镜像` 改为：apt 四件套、chromium 与中日韩/emoji 字体、gh、pnpm 均已烘进镜像（mnemon 的 opt/ 补链分支仍在，`opt/` 不存在时跳过）；`DESIGN.md` 增量小节的裁定列表追加第 4 条（entrypoint 退役 `opt/gh` 补链：gh 已在镜像，补出来的 `/usr/local/bin/gh` 会遮蔽钉版）

- [ ] **Step 7: Commit** `refactor(deploy): entrypoint 退役 opt/gh 补链，gh 改由镜像提供`

### Task 3: smoke §0 镜像依赖在位检查

**Files:**
- Modify: `scripts/smoke.sh`（`/root/.gitkeep` 探针之后、`== 1.` 之前）
- Modify: `README.md` 升级步骤 6 里对 §0 的描述
- Modify: `DESIGN.md`（增量小节的裁定列表追加第 5 条）

**Interfaces:**
- Consumes: 运行中的 dsh 容器
- Produces: §0 新增一项；缺依赖时输出 `FAIL  镜像依赖缺失：<命令>…` 并以 1 退出

- [ ] **Step 1: 编辑 `scripts/smoke.sh`**，在探针之后加入（两行注释说明：重建后蒸发过的东西在此点名，免得会话里才发现）：

```bash
missing=$(docker compose exec -T dsh sh -c '
    for c in git curl ssh sshpass xauth python3 pnpm mnemon chromium gh; do
        command -v "$c" >/dev/null 2>&1 || printf "%s " "$c"
    done')
missing=${missing% }
[ -z "$missing" ] && echo "ok    镜像依赖在位（apt 四件套/pnpm/mnemon/chromium/gh）" \
    || { echo "FAIL  镜像依赖缺失：$missing（镜像没重建，或 Dockerfile 被改回）" >&2; exit 1; }
```

  `README.md` 升级步骤 6：`§0 含容器状态自愈检查（restore 脚本在位 + symlink 已恢复）` → `§0 含挂载探针与镜像依赖在位检查`；`DESIGN.md` 裁定列表追加第 5 条（smoke §0 增加镜像依赖在位检查：重建后丢东西当场暴露）

- [ ] **Step 2: 红测试（现网）**：`bash -n scripts/smoke.sh` 无输出；`scripts/smoke.sh; echo "exit=$?"`
  - Expected：§0 前几项 ok，随后 `FAIL  镜像依赖缺失：gh（…）`、`exit=1`，脚本到此为止（未进 §1，不会 restart dsh）。现网有手动装的 chromium，故只点名 gh——真阳性

- [ ] **Step 3: Commit** `test(deploy): smoke §0 增加镜像依赖在位检查`
  - 提交后到 Task 4 完成前，现网 smoke 在此检查处为红，属预期

### Task 4: 上线（须用户点头）

**Files:**
- Modify: `DESIGN.md`（增量小节的「验证」行补上线结果）

**Interfaces:**
- Consumes: Task 1–3 的镜像与脚本
- Produces: 运行中的 dsh 容器换到新镜像；smoke §0–§10 全绿

- [ ] **Step 1: 换容器**：`docker compose up -d dsh`；轮询 `docker inspect -f '{{.State.Health.Status}}' dsh-server-dsh-1` 直到 `healthy`（start_period 60s，上限等 3 分钟）

- [ ] **Step 2: 冒烟**：`scripts/smoke.sh` → §0–§10 全绿、退出码 0，其中含 `ok    镜像依赖在位`

- [ ] **Step 3: 收尾**：`DESIGN.md` 验证行补「换容器后 smoke §0–§10 全绿」；`rm -rf /tmp/dsh-bake-verify`；回退标签与悬空旧镜像保留，待用户确认稳定后再清

- [ ] **Step 4: Commit** `docs(design): 记录 chromium/gh 上线验证结果`

回退：`docker tag dsh-server-dsh:pre-bake-20261124 dsh-server-dsh:latest && docker compose up -d dsh`
