# 设计：chromium/字体与 gh 烘进 dsh 镜像

> 历史文档：按当时的仓库布局写成。文中提到的 `README.md` 现为 `docs/operations.zh-CN.md`，`DESIGN.md` 现为 `docs/design.zh-CN.md`；地址与路径以文档占位符表示。


日期：2026-11-24　状态：已批准（用户「开做」）

## 意图

2026-11-24 团队在运行中的 dsh 容器里手动 `apt-get install chromium
fonts-noto-cjk fonts-noto-color-emoji`，装在容器可写层——`docker compose build
dsh` + `up -d` 之后即蒸发，与 2026-11-22 升级事故是同一类问题。按既有分工
「镜像管确定性依赖」，把浏览器、字体与 GitHub CLI 烘进 `dsh/Dockerfile`，
重建零补装。

gh 目前容器里**并未安装**（2026-11-23 清空时随 `opt/` 删除），本次属新装，
不是保留。

## 目标 / 非目标 / 反目标

- 目标：重建后 chromium（含中文、emoji 渲染）与 gh 仍在位；升级 dsh 的构建
  只重装 dsh 本体。
- 非目标：不改 dsh 版本，不动运行期挂载与数据；不给 chromium 加沙箱或
  capability 改动（容器以 root 运行，调用方须带 `--no-sandbox`，现状不变）。
- 反目标：用挂载卷覆盖 `/usr`（会遮蔽钉版 dsh，升级静默失效）；在
  entrypoint 里联网 apt（每次重建都依赖出网）。

## 约束

- 基底 `node:24-slim` = Debian 12 bookworm，amd64；构建出网经 `LAN_PROXY`
  （一律勿 `--pull`）。
- 手动安装那次未带 `--no-install-recommends`，拉进 327 个包（含 systemd、
  dbus、avahi 等）；带该开关约 177 个。
- Debian 12 官方源的 gh 是 2.23.0（2024 年版）；团队此前用的是 GitHub
  Releases 的 2.105.0，恰为当前最新（见备份内 `opt/gh` 的 man 页）。
- Docker 层缓存严格顺序：npm 层之前的任何改动都会使 1.39GB 的 dsh 层重建。

## 方案

**层的位置**

1. 并入现有 apt 层：清单只有一处，但同样要重建 npm 层，且浏览器与基础
   工具的来历混在一层。未选。
2. 独立层，放在 npm 层**之后**：本次构建快（npm 层缓存保住）；但每次升级
   dsh 都会把 chromium/字体重新下载一遍，给升级路径多加一个出网失败点。
   未选。
3. 独立层，放在 npm 层**之前**（选定）：升级 dsh 只重建 npm 层及其后的层；
   代价是本次一次性重装 dsh 层。

**gh 的来源**

- apt 2.23.0：零新依赖，但落后两年，agent 用到新 flag 会报错。
- GitHub Releases 钉版 .deb + sha256（选定）：与团队此前所用一致，校验和
  防下载链路（经代理）被替换。代价：构建期多一个 GitHub 出网依赖，升级 gh
  需手改版本号与校验和。gh 运行时本就要连 GitHub，该依赖不构成新前提。

## 设计

- `dsh/Dockerfile`：现有 apt 层之后新增两层。
  - `RUN apt-get update && apt-get install -y --no-install-recommends chromium
    fonts-noto-cjk fonts-noto-color-emoji fonts-liberation && rm -rf
    /var/lib/apt/lists/*`。`fonts-liberation` 是 chromium 的推荐包，手动安装
    时随之带入（网页 Arial/Times/Courier 的替身），显式列出以保持渲染一致。
  - `ARG GH_VERSION=2.105.0` + `RUN`：curl 下载
    `gh_${GH_VERSION}_linux_amd64.deb` → `sha256sum -c` → `apt-get install`
    本地 deb → 清 lists。校验和
    `1dac939af3e84fb3861927c984b357531341be9403b889e4aa86e8822145a09d`
    （已对官方 `gh_2.105.0_checksums.txt`；经代理下载耗时 3 秒）。
- `dsh/entrypoint.sh`：删除 `/data/dsh/opt/gh/bin/gh` → `/usr/local/bin/gh`
  的补链分支。`opt/` 已不存在，留着则它一旦回来就遮蔽镜像内钉版的 gh。
  mnemon 补链分支不动。
- `scripts/smoke.sh` §0：新增镜像依赖在位检查（git curl ssh sshpass xauth
  python3 pnpm mnemon chromium gh），重建后丢东西当场暴露。
- `README.md`、`DESIGN.md`：同步「镜像内含什么」与 gh 的升级方式。

## 验证

- 构建后以一次性容器核对各命令版本，并用 headless chromium 渲染
  「中文 + emoji + Latin」页面截图目检。
- 换容器后 `scripts/smoke.sh` 全绿。

## 上线

分两段：构建与镜像内验证不碰线上；换容器会让 dsh 中断约一分钟，须用户
点头后执行。

## 留给执行者

注释措辞；smoke 检查的具体写法；文档改动的行文。
