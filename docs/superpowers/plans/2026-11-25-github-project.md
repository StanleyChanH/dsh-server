# Public GitHub Project Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the deployment repository into an English-first open-source project that anyone can deploy from the README, and publish it to `https://github.com/StanleyChanH/dsh-server.git`.

**Architecture:** No new runtime components. The deployment surface (Caddyfile, compose, entrypoint, scripts) stops carrying one site's address: `DSH_TRUSTED_HOST` in `.env` becomes the single source and is read by Caddy through an environment placeholder. Documentation is split into a short front door (`README.md` + `README.zh-CN.md`) and two long documents (`docs/operations*.md`, `docs/design*.md`). Quality gates run in GitHub Actions using exactly the commands that run locally in containers.

**Tech Stack:** Docker Compose, Caddy 2.10, Go 1.24 (auth-gate), GitHub Actions, ShellCheck 0.11, hadolint 2.12, actionlint.

**Spec:** none. The maintainer opted out of the design round; every decision lives in Global Constraints and the task that owns it.

## Global Constraints

- English is the default language for docs, comments, commit messages and script output. Chinese counterparts exist only for `README`, `docs/operations` and `docs/design` (`*.zh-CN.md`).
- The tree contains no value from one particular deployment: no site address, host path, account name, backup file name or firewall state. Examples use `192.0.2.10` (RFC 5737) and `/path/to/dsh-server`.
- License is MIT; copyright holder is the GitHub account `StanleyChanH`.
- The running `dsh` and `auth-gate` containers must not be recreated by this work. Proof: `docker compose config --hash dsh` and `--hash auth-gate` are unchanged from before the edits.
- Never run `docker compose build --pull` or `docker build --pull` on this host (the distroless base is only reachable through a mirror).
- `.env` is never printed, copied or committed; `AUTH_GATE_SECRET` is never echoed.
- Go floor stays 1.24 (`golang:1.24-alpine`), `golang.org/x/crypto` stays `v0.48.0`.
- Branch stays `master`; existing history is not rewritten. New commits use Conventional Commits in English.
- User-facing copy inside `auth-gate` (login page text, HTTP error bodies, the sync page) is out of scope: it belongs to the pending login-page redesign.

## Review Focus

1. **Caddy recreated without the new variable** (empty `DSH_TRUSTED_HOST` makes the Caddyfile invalid and the site goes down, including after a bare `docker compose restart caddy` or a host reboot). Pinned in Task 2: `caddy validate` inside the recreated container plus an unauthenticated 302 and an authenticated round trip.
2. **`docker compose up -d` recreating `dsh` or `auth-gate` as a side effect** (kills running agent sessions). Pinned in Task 1 and Task 2: service config hashes compared with the pre-edit baseline; `up -d --dry-run` lists only `caddy`.
3. **A new user follows the quick start literally and it fails.** Pinned in Task 3: the quick start is executed end to end in a clean sandbox under a different Compose project name.
4. **CI is red on the first push** (cannot be run before the repository is public). Pinned in Task 6: actionlint, plus every CI command executed locally in the same images.
5. **`scripts/smoke.sh` loses its default target** now that the address is not hardwired. Pinned in Task 1: with no `BASE_URL`, the URL comes from `docker compose config`; without `.env` the script fails with one clear message.

---

### Task 0: Add the remote

**Files:** none (git config only)

- [ ] **Step 1: Add the remote**

Run: `git remote add origin https://github.com/StanleyChanH/dsh-server.git`
Expected: `git remote -v` lists `origin` twice (fetch, push). Nothing is pushed in this task.

### Task 1: Deployment surface — one variable, English, lint-clean

**Files:**
- Modify: `caddy/Caddyfile`, `compose.yml`, `.env.example`, `dsh/entrypoint.sh`, `dsh/Dockerfile`, `auth-gate/Dockerfile`, `scripts/add-user.sh`, `scripts/dsh-url.sh`, `scripts/smoke.sh`, `scripts/sync-token.sh`
- Modify: `auth-gate/login.html` (the readout line only)
- Create: `auth-gate/.dockerignore`, `.hadolint.yaml`

**Interfaces:**
- Produces: `.env` contract consumed by every later task.
  - `DSH_TRUSTED_HOST` (required): IP or hostname, no scheme, no port. Drives the Caddy site address, `default_sni`, dsh `--trusted-host`, and the default of `DSH_PUBLIC_URL` (`https://<DSH_TRUSTED_HOST>/`).
  - `DSH_VERSION` (required): pinned npm version.
  - `AUTH_GATE_SECRET` (required), `AUTH_SESSION_HOURS`, `LAN_PROXY`, `NO_PROXY` (optional, unchanged semantics).
  - `GOPROXY` (optional, default `https://goproxy.cn,direct`): module proxy for the auth-gate build stage.
  - `DSH_PUBLIC_URL` (optional override).

- [ ] **Step 1: Record the baseline**

Run: `docker compose config --hash '*'` and `docker compose ps --format '{{.ID}} {{.Service}}'`
Expected: saved for Tasks 1–2 (hashes of `dsh` and `auth-gate`, container ids).

- [ ] **Step 2: Write the config and scripts**

Decisions the implementer must not re-open:
- `caddy/Caddyfile`: site address `https://{$DSH_TRUSTED_HOST}` and `default_sni {$DSH_TRUSTED_HOST}`. Comments in English. Routing logic unchanged.
- `compose.yml`: `dsh` env `DSH_TRUSTED_HOST: "${DSH_TRUSTED_HOST:?…}"`, `DSH_PUBLIC_URL: "${DSH_PUBLIC_URL:-https://${DSH_TRUSTED_HOST}/}"`, build arg `DSH_VERSION: "${DSH_VERSION:?…}"`; `caddy` gets `environment: DSH_TRUSTED_HOST` (same required form); `auth-gate` build arg `GOPROXY: "${GOPROXY:-https://goproxy.cn,direct}"`. Image names, ports, volumes, healthchecks, logging unchanged.
- `dsh/entrypoint.sh`: after the pass-through branch, require `DSH_TRUSTED_HOST` (`${DSH_TRUSTED_HOST:?…}`) and default `DSH_PUBLIC_URL` from it. mnemon branch unchanged.
- `dsh/Dockerfile`: no default for `DSH_VERSION` (empty value fails the build); `pnpm@12.23.0` pinned (the version in the running image); gh checksum written to a file instead of a pipe; OCI labels as the last instruction.
- `auth-gate/Dockerfile`: `FROM gcr.io/distroless/static:latest`; `ARG GOPROXY` feeds the build stage; OCI labels last.
- `auth-gate/.dockerignore`: `data/` and `*_test.go` never enter the build context.
- `.hadolint.yaml`: ignore `DL3008` only, with the reason in a comment.
- `scripts/smoke.sh`: default `BASE_URL` from `docker compose config --format json` → `.services.dsh.environment.DSH_PUBLIC_URL`; containers resolved with `docker compose ps -q <service>` (no hardwired container names); file-level `# shellcheck disable=SC2015` and one inline `SC2024` with reasons. Checks and their order unchanged.
- Other scripts: English comments and messages only. `sync-token.sh` header shows the cron file with `/path/to/dsh-server`.
- `auth-gate/login.html`: the readout drops the hardwired `node`/`net` facts; `node` shows `location.hostname` (fallback `local`) through a `#node` element. The agent span stays the last child (`succeed()` selects it). No other markup or copy changes.

- [ ] **Step 3: Static verification**

Run each; expected as stated:
- `docker run --rm -v "$PWD:/mnt:ro" koalaman/shellcheck:stable -x scripts/*.sh dsh/entrypoint.sh` → exit 0.
- `docker run --rm -i hadolint/hadolint:2.12.0 < dsh/Dockerfile` and the same for `auth-gate/Dockerfile` with `-v "$PWD/.hadolint.yaml:/.hadolint.yaml:ro"` → exit 0.
- `docker run --rm -e DSH_TRUSTED_HOST=192.0.2.10 -v "$PWD/caddy/Caddyfile:/etc/caddy/Caddyfile:ro" caddy:2.10-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile` → `Valid configuration`.
- The same command without `-e DSH_TRUSTED_HOST` → exit 1.
- `docker compose config --format json | jq -e '.services.dsh.environment.DSH_PUBLIC_URL, .services.caddy.environment.DSH_TRUSTED_HOST'` → both equal the live `.env` host.
- `docker compose config --hash dsh` and `--hash auth-gate` → equal to Step 1.
- A scratch copy of `compose.yml` with an empty `DSH_VERSION`: `DSH_VERSION= DSH_TRUSTED_HOST=192.0.2.10 docker compose -f <copy> config -q` → exit 1 (the required check fires).
- smoke.sh without `BASE_URL` resolves the URL: `bash -c 'cd "$(dirname scripts/smoke.sh)"; …'` → see Task 2 (full run is the real test).

### Task 2: Roll out to the live stack (needs the maintainer's go-ahead)

**Files:** none (operations only)

**Interfaces:**
- Consumes: Task 1 files as they sit on disk.

- [ ] **Step 1: Ask**

One question: recreating `caddy` interrupts members for a few seconds, and the full smoke test restarts `dsh` once. Confirm the timing.

- [ ] **Step 2: Recreate caddy only**

Run: `docker compose up -d --dry-run --no-deps caddy` → only `caddy` is listed.
Run: `docker compose up -d --no-deps caddy`.
Expected: `docker compose ps` shows a new caddy container id; `dsh` and `auth-gate` ids equal the baseline.

- [ ] **Step 3: Verify**

- `docker compose exec caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile` → `Valid configuration`.
- `docker inspect dsh-server-caddy-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -c '^DSH_TRUSTED_HOST='` → 1.
- `curl -sk -o /dev/null -w '%{http_code} %{redirect_url}' https://<host>/some/path` → `302 https://<host>/auth/login?next=%2Fsome%2Fpath`.
- `scripts/smoke.sh` → every section `ok`, last line `All smoke checks passed`.

- [ ] **Step 4: Commit**

`feat(deploy): drive the site address from DSH_TRUSTED_HOST; English deployment surface`

### Task 3: Prove the quick start in a sandbox

**Files:** none in the repository (scratch directory under `/tmp`)

- [ ] **Step 1: Build a clean copy**

Copy the working tree without `.git`, `.env` and the state directories to `/tmp/dsh-sandbox`. Add empty state directories, a sandbox `.env` (`COMPOSE_PROJECT_NAME=dshsandbox`, `DSH_TRUSTED_HOST=127.0.0.1`, a fresh secret, `DSH_VERSION`/`LAN_PROXY`/`NO_PROXY` copied from the live `.env` by key) and an override file that renames both images (`dsh-server-sandbox-*`) and publishes no ports.

- [ ] **Step 2: Run the quick start exactly as the README will state it**

`docker compose build` → `scripts/add-user.sh alice` → `docker compose up -d` → root certificate readable with sudo → `scripts/smoke.sh` with a `curl` wrapper that adds `--resolve 127.0.0.1:443:<sandbox caddy ip>`.
Expected: all sections `ok`.

- [ ] **Step 3: Clean up**

`docker compose -p dshsandbox down`, remove the `dsh-server-sandbox-*` images and `/tmp/dsh-sandbox`. Expected: `docker images | grep sandbox` prints nothing; live container ids equal the baseline.

### Task 4: Documentation

**Files:**
- Create: `README.md` (English front door), `README.zh-CN.md`, `docs/operations.md`, `docs/operations.zh-CN.md`, `docs/design.md`, `docs/design.zh-CN.md`
- Delete: the old root `README.md` content (moves to `docs/operations.zh-CN.md`) and `DESIGN.md` (moves to `docs/design.zh-CN.md`)

**Interfaces:**
- Consumes: the `.env` contract from Task 1; the quick start verified in Task 3.
- Produces: the document set every later file links to.

- [ ] **Step 1: Write the six documents**

Decisions:
- `README.md` sections: title and one-line pitch, badges (CI, MIT, dsh upstream), language switch, why, features, architecture diagram, prerequisites, quick start, configuration table, everyday commands, security model, upgrade, troubleshooting pointer, documentation index, contributing, license, disclaimer (not affiliated with DeepSeek).
- dsh facts come from the npm registry only: package `@deepseek-ai/dsh`, tested version `0.2.2-rc.5`, upstream `https://github.com/deepseek-ai/deepseek-harness`, MIT.
- `docs/operations.md` is the full runbook (first deploy, smoke test, start/stop, upgrades incl. gh, members, token URL, auto-entry and degraded mode, backup, troubleshooting table, known risks). `docs/design.md` is the design record (goals, constraints, decisions, verified findings, the three increments).
- The Chinese documents keep today's content, updated for the new variable contract and the tested version, and scrubbed per Global Constraints.
- Known-stale statements are corrected in both languages: the tested version is `0.2.2-rc.5`; firewall notes are phrased as the general Docker-bypasses-ufw rule.

- [ ] **Step 2: Verify links and examples**

- Every relative link in the six documents resolves: a small loop over `](path)` targets → no missing files.
- Every command in the quick start is byte-identical to the one run in Task 3.
- The scrub pattern (deployment address, host path, account name, mirror hostname; it lives in the acceptance script, not here) finds nothing in the six documents.

### Task 5: License and community health

**Files:**
- Create: `LICENSE`, `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `CHANGELOG.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `.github/ISSUE_TEMPLATE/bug_report.yml`, `.github/ISSUE_TEMPLATE/feature_request.yml`, `.github/ISSUE_TEMPLATE/config.yml`

- [ ] **Step 1: Write the files**

Decisions: MIT with `Copyright (c) 2026 StanleyChanH`; Contributor Covenant 2.1; `SECURITY.md` points at GitHub private vulnerability reporting (confirmed enabled) and supports `master`; `CHANGELOG.md` follows Keep a Changelog with one `[Unreleased]` section and no version links (no tag exists yet); issue templates are YAML forms.

- [ ] **Step 2: Verify**

- `python3 -c 'import yaml,sys; [yaml.safe_load(open(f)) for f in sys.argv[1:]]' .github/ISSUE_TEMPLATE/*.yml .github/dependabot.yml .github/workflows/*.yml` → no output, exit 0.
- `LICENSE` first line is `MIT License` and the year/name line is exact.

### Task 6: CI, Dependabot, repo hygiene

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/workflows/docker-dsh.yml`, `.github/dependabot.yml`, `.gitattributes`, `.editorconfig`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: lint configuration from Task 1.
- Produces: `CI` workflow name used by the README badge.

- [ ] **Step 1: Write the workflows**

Decisions:
- `ci.yml` triggers on push to `master`, pull requests and dispatch; `permissions: contents: read`; `runs-on: ubuntu-24.04`; actions pinned by commit SHA (`actions/checkout`, `actions/setup-go`). Jobs: `go` (gofmt check, `go vet`, `go test -race`), `shell` (ShellCheck image), `dockerfile` (hadolint image, matrix over both Dockerfiles), `config` (compose render asserts, Caddyfile validate with and without the variable, both negative cases must fail), `build-auth-gate` (`docker build`, then `docker image inspect` on the labels).
- `docker-dsh.yml` triggers on changes under `dsh/` and on dispatch; builds the image with `DSH_VERSION` parsed from `.env.example` and asserts `dsh --version`, `pnpm --version`, `command -v gh chromium mnemon`.
- `dependabot.yml`: `gomod` in `/auth-gate` and `github-actions` in `/`, weekly.
- `.gitattributes`: `* text=auto eol=lf`, explicit `eol=lf` for `*.sh`, `*.yml`; `.editorconfig`: 2-space YAML/JSON/MD, final newline, LF, trim trailing whitespace (Markdown left alone).

- [ ] **Step 2: Verify locally**

- `docker run --rm -v "$PWD:/repo:ro" -w /repo rhysd/actionlint:latest -color` → no findings.
- Each `run:` command of `ci.yml` executed by hand on this host (containers for Go/ShellCheck/hadolint, `caddy:2.10-alpine` for validate, `docker build -t dsh-server-auth-gate:ci auth-gate` without `--pull`).
- `git ls-files --eol` after `git add --renormalize .` (dry run with `git ls-files --eol` only) → nothing would change line endings except files that already are LF.

### Task 7: auth-gate module path and English comments

**Files:**
- Modify: `auth-gate/go.mod`, the eight `auth-gate/*.go` files

- [ ] **Step 1: Module path**

`module github.com/StanleyChanH/dsh-server/auth-gate`. Nothing imports the module path (single `package main`), so this is a one-line change.

- [ ] **Step 2: Translate comments only**

Comments in all eight files become English. Identifiers, string literals (including test failure messages), and test names stay as they are.

- [ ] **Step 3: Prove the code is unchanged**

Write a throwaway Go program (outside the repository) that prints the token stream of a file with `go/scanner` in mode `0` (comments dropped). For each file: `git show HEAD:auth-gate/<f> | tokens` equals `tokens < auth-gate/<f>`.
Then: `gofmt -l .` empty, `go vet ./...`, `go test -race -count=1 ./...` pass (race build needs `apk add build-base` in the alpine container).

### Task 8: Historical documents

**Files:**
- Modify: the four existing files under `docs/superpowers/`, plus this plan.

- [ ] **Step 1: Scrub and re-link**

Replace the deployment address with `192.0.2.10`, the host path with `/path/to/dsh-server`, the account name with `alice`. Update links that pointed at `README.md`/`DESIGN.md` to the new paths.

- [ ] **Step 2: Verify**

The scrub pattern over the whole tree (`git grep`) → no matches anywhere. The pattern is spelled out only in the acceptance script: written here, it would match this plan. The image-mirror hostname from the old README is not carried over either: `docs/operations*.md` say "a mirror you trust" without naming one, because naming a third-party pull-through proxy would be an endorsement of its trustworthiness.

### Task 9: Whole-branch review and final verification

- [ ] **Step 1: Independent review**

A fresh read-only reviewer reads `git diff <base>..HEAD` and the six documents with: correctness of every command, translation fidelity against the Chinese sources, leftover deployment details, CI risks. Findings are fixed, not just listed.

- [ ] **Step 2: Final checks**

- Secret scan over the new commits: `git log <base>..HEAD -p | grep -cE '<secret patterns>'` → 0.
- Full `scripts/smoke.sh` on the live stack → green (only if the maintainer approved a `dsh` restart; otherwise sections that do not restart).
- `git status` clean.

### Task 10: Push

- [ ] **Step 1: Hand the push to the maintainer**

The host has no GitHub credential. The maintainer runs `git push -u origin master` in their own terminal once.
- [ ] **Step 2: Verify**

`git ls-remote origin refs/heads/master` equals `git rev-parse HEAD`; `GET /repos/StanleyChanH/dsh-server/actions/runs` shows `CI` and its conclusion; fix anything red in a follow-up commit.

---

## As executed (2026-11-25)

Deviations from the plan above:

- The work happened on a branch in a project-local worktree, and the live checkout kept the old files until the merge. Task 2 (the live rollout) therefore runs after the merge instead of right after Task 1. From the moment the live checkout is fast-forwarded until caddy is recreated, the old caddy container still runs on the old Caddyfile; a restart in that window would start it on the new file, without the variable. The two steps therefore run back to back.
- `scripts/check.sh` was added in Task 6 as the single list of checks, for CI and for contributors alike. The CI matrix runs its five default groups; the dsh image workflow runs its opt-in `image` group; `go test -race` runs on the runner with a C toolchain.
- hadolint takes the Dockerfile on standard input without a `-` argument, and `DL3007` is suppressed inline on the distroless `FROM` line.
- Tasks 4 to 8 end with one commit each; the plan only spelled out the commit of Task 2.
- There was no spec (the design round was skipped), so every ruling made during execution was provisional until the final review.
- The final review (one fresh reviewer, read-only, on the whole branch) left one decision to the maintainer (whether to publish the existing history and its commit e-mail domain as they are), five important fixes and a list of documentation corrections. Everything but the decision is applied before the merge: a cron check in the smoke test, an update procedure that recreates instead of restarting, the amd64 and cron prerequisites, a checker that no longer depends on a local `.env`, and the last deployment details in comments and documents.
- The historical documents under `docs/superpowers/` carry a note that maps their old file names to the new ones. Their text was not rewritten beyond the scrub: they contain no links, only narrative mentions of the names in use at the time.
