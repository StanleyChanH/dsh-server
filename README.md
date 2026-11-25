<h1 align="center">dsh-server</h1>

<p align="center">
  <a href="https://github.com/StanleyChanH/dsh-server/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/StanleyChanH/dsh-server/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/License-MIT-blue.svg"></a>
  <a href="https://github.com/deepseek-ai/deepseek-harness"><img alt="dsh: DeepSeek Harness" src="https://img.shields.io/badge/dsh-DeepSeek%20Harness-4d6bfe"></a>
</p>

<p align="center">
  <b>Self-hosted, always-on <a href="https://github.com/deepseek-ai/deepseek-harness">DeepSeek Harness</a> for a team</b><br>
  One Docker Compose stack, a login per member, HTTPS on a bare IP, and no token URLs to hand out.
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

## Why

`dsh web`, the web UI of [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (dsh), ships with a single credential: a start-up URL that contains a token. Whoever holds that URL has full access, and the token changes every time dsh restarts. That is fine for one person. For a team it means forwarding a secret URL by chat after every restart or upgrade.

dsh-server puts three small services around dsh:

- **auth-gate** gives every member their own account: bcrypt passwords, signed session cookies, and per-IP login rate limiting.
- **caddy** terminates HTTPS with an internal CA, so it works on a bare IP address without a domain, and turns dsh's `401` into an automatic sign-in.
- **dsh** runs the agent with all of its state on the host, so restarts, rebuilds and upgrades lose nothing.

Members open one address, type their password, and are in dsh.

## Features

- **Accounts instead of a shared secret.** One login per member, each one revocable on its own without a restart.
- **Automatic sign-in.** After the login, members never see a token URL: dsh's token is synced once a minute and handed out by the gate.
- **HTTPS without a domain.** Certificates come from Caddy's internal CA and carry the IP address as a SAN; members import one root certificate.
- **Pinned, deliberate upgrades.** The dsh version is a variable in `.env`; nothing floats.
- **State that survives rebuilds.** Sessions, credentials, plugins and even the container's home directory live in host directories.
- **A batteries-included agent image.** git, chromium with CJK and emoji fonts, GitHub CLI, pnpm and mnemon are baked in.
- **A smoke test for the whole chain.** `scripts/smoke.sh` verifies the login gate, the token exchange, automatic sign-in, WebSocket passthrough, revocation and token rotation against a running stack.
- **One address, configured once.** `DSH_TRUSTED_HOST` drives the Caddy site, its certificate and dsh.

## Architecture

```
            member browser (private network: LAN, VPN, Tailscale, ...)
                             | https://<DSH_TRUSTED_HOST>
                             v
+---------------------------- host: docker compose -----------------------------+
|  caddy  :80 -> :443  (TLS from the internal CA)                                |
|    |- /auth/* ------------------> auth-gate :8080   login page, login, check    |
|    '- everything else                                                           |
|         forward_auth /auth/check --> auth-gate   no session? 302 to the login   |
|         reverse_proxy -------------> dsh :3080      agent web UI (SSE, WebSocket)|
|                                           | 401                                  |
|                                           +-> 302 /auth/dsh-entry -> /?token=...  |
|                                                                                 |
|  cron, every minute: scripts/sync-token.sh --> auth-gate/data/dsh-token           |
+---------------------------------------------------------------------------------+
  state on the host: dsh-home/  dsh-root/  workspace/  auth-gate/data/  caddy/data/
```

A request without a session is redirected to the login page. Once the gate cookie is valid, the first request that reaches dsh comes back as `401`; Caddy turns that into a redirect to `/auth/dsh-entry`, auth-gate answers with the current token, and dsh issues its own session cookie. From then on the member is not involved.

## Requirements

- A Linux **amd64 (x86-64)** host with Docker Engine and Compose v2 (tested with Docker 29.7 and Compose v5.5), plus `curl`, `openssl` and `jq`. The host does not need Go or Node.
- Ports 80 and 443 free, and members able to reach them over a **private** network (LAN, VPN, Tailscale subnet route, ...).
- 3.4 GB of disk for the dsh image, plus room to build it, and internet access while building (Docker Hub, Debian mirrors, the npm registry, GitHub releases, gcr.io).
- A running **cron** daemon: the token-sync job runs as root, which must be able to run `docker compose`.
- `sudo` for two one-time steps (reading the root certificate, installing the cron job).

> [!IMPORTANT]
> This stack is built for a private network. Do not expose it to the public Internet: see the [security model](#security-model).

## Quick start

```sh
git clone https://github.com/StanleyChanH/dsh-server.git
cd dsh-server
```

1. **Configure.** Copy the template and fill in two values:

   ```sh
   cp .env.example .env
   ${EDITOR:-vi} .env
   ```

   - `DSH_TRUSTED_HOST`: the IP address or hostname members type into the browser (no scheme, no port).
   - `AUTH_GATE_SECRET`: the output of `openssl rand -hex 32`.

2. **Build the images.** Do not add `--pull`; if gcr.io is not reachable from your build host, read [image sources](docs/operations.md#before-you-build-image-sources) first.

   ```sh
   docker compose build
   ```

3. **Create the first account.** This works before the stack is running.

   ```sh
   scripts/add-user.sh alice
   ```

4. **Start.**

   ```sh
   docker compose up -d
   docker compose ps    # dsh and auth-gate should be healthy
   ```

5. **Trust the root certificate.** Take it off the host and import it as a trusted CA on your devices (how, per operating system: [Members](docs/operations.md#members)).

   ```sh
   sudo cat caddy/data/caddy/pki/authorities/local/root.crt > dsh-server-root.crt
   ```

6. **Install the token-sync job.** It runs every minute and is what makes automatic sign-in work after a dsh restart.

   ```sh
   sudo tee /etc/cron.d/dsh-server-token >/dev/null <<EOF
   SHELL=/bin/sh
   PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
   * * * * * root $(pwd)/scripts/sync-token.sh
   EOF
   ```

   About a minute later `auth-gate/data/dsh-token` should exist and get a new timestamp every minute. If it does not, run `sudo scripts/sync-token.sh` once by hand to see why: cron has to be running, and root has to be able to run `docker compose`.

7. **Verify.** The smoke test exercises the whole chain, including one dsh restart:

   ```sh
   scripts/smoke.sh
   ```

   If it stops in section 0 with a message about the token file, the cron job from step 6 is not working yet.

Now open `https://<DSH_TRUSTED_HOST>` and sign in as `alice`. If the browser warns about the certificate, the root certificate from step 5 is not imported on that device yet.

## Configuration

Everything lives in `.env` (template: [`.env.example`](.env.example)). After changing a value, run `docker compose up -d`: compose recreates the containers whose environment changed (for `DSH_TRUSTED_HOST` that is caddy and dsh, and dsh restarting interrupts running sessions). A plain `restart` keeps the old environment.

| Variable | Purpose | Default / example |
| --- | --- | --- |
| `DSH_TRUSTED_HOST` | **Required.** The address members use, without scheme or port, for example `192.0.2.10` or `dsh.example.org`. Drives the Caddy site and certificate, dsh's trusted host, and the default public URL. | none |
| `DSH_VERSION` | **Required.** The dsh version to install. | `0.2.2-rc.5` |
| `AUTH_GATE_SECRET` | **Required.** Key that signs login sessions (`openssl rand -hex 32`). | - |
| `DSH_PUBLIC_URL` | The URL dsh advertises. Set it only if it has to differ from `https://<DSH_TRUSTED_HOST>/`. | derived |
| `AUTH_SESSION_HOURS` | Login session lifetime in hours. | `720` (30 days) |
| `LAN_PROXY` | Outbound proxy for the image build and for processes started inside dsh sessions. Empty disables it. | empty |
| `NO_PROXY` | Addresses that bypass the proxy. | private ranges |
| `GOPROXY` | Go module proxy for building auth-gate. | `https://goproxy.cn,direct` |

## Everyday commands

| Task | Command |
| --- | --- |
| Add a member, or reset a password | `scripts/add-user.sh <name>` |
| Follow the logs | `docker compose logs -f` |
| Restart dsh (rotates the token; automatic sign-in recovers within a minute) | `docker compose restart dsh` |
| Stop / start | `docker compose stop` / `docker compose up -d` |
| End-to-end check | `scripts/smoke.sh` |
| Upgrade dsh | Edit `DSH_VERSION` in `.env`, then `docker compose build dsh && docker compose up -d dsh && scripts/smoke.sh` |
| Update after `git pull` | `docker compose build && docker compose up -d`, then `scripts/smoke.sh` |
| Move to another address | Edit `DSH_TRUSTED_HOST` in `.env`, then `docker compose up -d` (recreates caddy and dsh) |
| Remove a member, back up, troubleshoot | [docs/operations.md](docs/operations.md) |

## Security model

- **Private network only.** dsh executes commands generated by a model, is a developer preview, and has not been audited. The rate limiter assumes a trusted network, TLS comes from a private CA, and the dsh token travels in a URL once. Keep 80/443 off the public Internet; if members are far away, put a VPN such as Tailscale or WireGuard in front.
- **The gate decides who comes in, not whose data is whose.** All members share one dsh instance: the same sessions, credentials and workspace. There is no per-member isolation inside dsh.
- **The container is the boundary.** The agent can only touch what is mounted into it: `dsh-home/`, `workspace/` and `dsh-root/`. The dsh container runs as root and has no Docker socket mounted, so do not add `privileged`, host directories or the Docker socket to it.
- **Secrets on disk.** `.env` holds the session key; `dsh-home/` holds the model API keys; `auth-gate/data/` holds the password hashes and the current dsh token. None of them are in git, and backups of them need protection.
- **Ports published by Docker bypass ufw.** If you enable a host firewall, add explicit `DOCKER-USER` rules.

## Upgrading dsh

dsh is a developer preview and breaks things between releases, so upgrades are manual and one version at a time:

1. Change `DSH_VERSION` in `.env`.
2. `docker compose build dsh` (no `--pull`), then `docker compose up -d dsh`.
3. Run `scripts/smoke.sh`. Only an all-green run completes the upgrade; if it is red, set the old version back and repeat.

Sessions and settings survive, and signed-in members do not notice the swap. Details, including how to roll back and what to do when the log format changes: [Upgrading dsh](docs/operations.md#upgrading-dsh).

## Updating dsh-server

A container keeps the environment it was created with, and `docker compose restart` does not change that. Changes are therefore applied by **recreating** containers:

| After … | Run |
| --- | --- |
| `git pull` | `docker compose build && docker compose up -d`, then `scripts/smoke.sh` |
| Editing `.env` | `docker compose up -d` (recreates caddy and dsh when the address or the proxy changed; dsh restarts) |
| Editing only `caddy/Caddyfile` | `docker compose up -d --no-deps --force-recreate caddy` |

`docker compose up -d` recreates the containers whose configuration changed and leaves the others running; `--dry-run` lists which ones it would recreate.

Do not rely on `restart` after pulling a change: the restarted container keeps its old environment, and a caddy container created before `DSH_TRUSTED_HOST` existed cannot load the current Caddyfile. Details: [Updating dsh-server](docs/operations.md#updating-dsh-server).

## FAQ

**Can I use a domain name?**
Yes: `DSH_TRUSTED_HOST` accepts a hostname. TLS still comes from the internal CA (`tls internal`), so members import the root certificate either way.

**Can I expose it to the Internet?**
Not as it is. Put it behind a VPN, or add a public-CA certificate and additional protection first; see the security model above.

**Which dsh version should I pin?**
The one in `.env.example` is the version this stack is tested with. Check the npm registry for `@deepseek-ai/dsh` before upgrading, and always run the smoke test afterwards.

## Documentation

| Document | Contents |
| --- | --- |
| [Operations runbook](docs/operations.md) ([中文](docs/operations.zh-CN.md)) | First deploy, smoke test, start/stop, updating the stack, upgrades, members, token URL, automatic sign-in and its degraded mode, backup and restore, troubleshooting, known risks |
| [Design](docs/design.md) ([中文](docs/design.zh-CN.md)) | Goals and non-goals, constraints, the decisions behind the three services, and the design increments |

## Contributing

Contributions are welcome: bugs, documentation fixes and ideas all help. Read [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, the checks CI runs, and the commit convention. Please do not open an issue for a vulnerability; see [SECURITY.md](SECURITY.md) instead.

## License

[MIT](LICENSE)

## Acknowledgements

- [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) by DeepSeek, the agent this stack deploys. dsh-server is an independent project and is not affiliated with or endorsed by DeepSeek.
- [Caddy](https://caddyserver.com/), the reverse proxy; the dsh image also bundles [Chromium](https://www.chromium.org/), [GitHub CLI](https://cli.github.com/), [pnpm](https://pnpm.io/) and [mnemon](https://github.com/mnemon-dev/mnemon), each under its own license.
