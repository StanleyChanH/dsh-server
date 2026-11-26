# dsh-server operations runbook

dsh-server runs DeepSeek Harness (dsh) as an always-on agent service: a three-service
Docker Compose stack. Members reach it over a private network (LAN or VPN) at
`https://<DSH_TRUSTED_HOST>`; optionally there is a second, public entrance through a
Cloudflare Tunnel (see "Cloudflare Tunnel" below). The private address is configured in
exactly one place, `DSH_TRUSTED_HOST` in `.env`; every command and example below uses the
documentation address `192.0.2.10` in its place.

For an overview and the quick start see the [README](../README.md); for the reasoning
behind the design see [design.md](design.md). A Chinese version of this runbook is at
[operations.zh-CN.md](operations.zh-CN.md).

## Architecture

```
member browser --https--> caddy (publishes 80/443, TLS internal)
                           |- /auth/* -----------------> auth-gate:8080 (login page, login, check, ping)
                           '- everything else forward_auth |  invalid cookie -> 302 to the login page
                                                            |  valid cookie   v  let through
                                                        dsh:3080 (agent web UI)
```

- **caddy**: the only service with published ports (80 only redirects, 443 serves the
  site). Certificates come from the built-in CA (with an IP SAN). Compression is off for
  the whole site, to keep SSE and WebSocket streams real-time.
- **auth-gate**: the login gate, written in Go. It embeds the login page
  (`auth-gate/login.html`), keeps a bcrypt user table (`auth-gate/data/users.json`) and
  issues the HMAC-signed cookie `gate_session`. Caddy's `forward_auth` calls
  `/auth/check` first; a GET without a session is answered with a 302 to the login page
  (anything else gets 401: a browser only navigates on 3xx, and a 401 would just render
  a blank page).
- **dsh**: the agent itself. All of its state lives in `dsh-home/` (mounted at
  `/data/dsh`), the shared workspace is `workspace/` (mounted at `/workspace`), and the
  runtime state of the container's home directory is `dsh-root/` (mounted at `/root`):
  npm cache, workspace symlinks, gh credentials and the like survive a rebuild.
- dsh and auth-gate **publish no ports**; they talk to each other only on the Compose
  network.
- **cloudflared** (optional, `compose.tunnel.yml`): a Cloudflare Tunnel, an outbound
  connection to Cloudflare. It is on a Compose network of its own and reaches caddy's
  second listener (`:8080`, not published) and nothing else. See
  ["Cloudflare Tunnel"](#cloudflare-tunnel).

## Before you build: image sources

- If **gcr.io is not reachable** from the build host (for example from mainland China),
  pull the runtime base of auth-gate, `gcr.io/distroless/static:latest`, through an image
  mirror you trust, then `docker tag` it under its original name. Pulling through a
  mirror means trusting that mirror's content, so only use a source you vouch for.
- **Never run `docker build --pull` or `docker compose build --pull`.** It tries gcr.io
  directly and fails. Builds in this repository never use `--pull`.
- `LAN_PROXY` (see the variable table) only speeds up the **RUN steps and outbound
  traffic from inside the containers**. The docker daemon does not use it for image
  pulls (the daemon's proxy and registry mirrors are configured separately), so the
  preparation above stays as it is.
- Before rebuilding on a new machine, deal with the image sources first:

  ```sh
  docker pull <mirror>/gcr.io/distroless/static:latest
  docker tag  <mirror>/gcr.io/distroless/static:latest gcr.io/distroless/static:latest
  ```

  The other bases (`golang:1.24-alpine`, `node:24-slim`, `caddy:2.10-alpine`) come from
  Docker Hub. The Go module proxy for the auth-gate build stage defaults to
  `https://goproxy.cn,direct`; override it with `GOPROXY` in `.env`.

## First deploy

Requirements: a Linux **amd64 (x86-64)** host (the dsh image installs the amd64 build of
GitHub CLI) with Docker Engine and Compose v2, `curl`, `openssl` and `jq`; members can
reach ports 80/443 of the host over a private network. The host does not need Go or
Node.

Two things have to hold on the host for the token-sync job: a cron daemon is running,
and root can run `docker compose` (a Compose plugin installed for the whole system, not
only for one user).

The scripts assume that the administrator account has uid 1000 (see `user:` in
compose.yml). If yours has another uid, change compose and the scripts named there
together, or auth-gate cannot read the token file.

1. Configure: `cp .env.example .env` and fill in at least two values: `DSH_TRUSTED_HOST`
   (the IP address or hostname members type into the browser, without scheme or port)
   and `AUTH_GATE_SECRET` (the output of `openssl rand -hex 32`). `DSH_VERSION` is
   prefilled with the version this stack is tested with.
2. Build the images: `docker compose build` (**without `--pull`**, see above). The dsh
   image is about 3.4 GB (chromium and the CJK fonts are part of it), so the first build
   downloads a lot.
3. Create the first account (the stack does not have to be up yet):

   ```sh
   scripts/add-user.sh <name>
   # type the password at the prompt (no echo); rerun with the same name to change it
   ```

   auth-gate fails fast: while `users.json` does not exist it crash-loops. When the
   service container is not running, `scripts/add-user.sh` automatically falls back to a
   one-off container running `-adduser`, which writes to the same `auth-gate/data`
   mount. Creating the account before the first start therefore works either way round.
4. Start: `docker compose up -d`, then confirm with `docker compose ps` that dsh and
   auth-gate are healthy.
5. Take out the root certificate and import it yourself:
   `caddy/data/caddy/pki/authorities/local/root.crt` (how to import: see "Members"; the
   file is root:root 0600, so reading it from the host needs sudo).
6. Install the cron job that syncs the token. Write the following into
   `/etc/cron.d/dsh-server-token` (as root; `$(pwd)` expands in your shell to the
   absolute path of the checkout). Every minute it writes the newest dsh token, atomically,
   into `auth-gate/data/dsh-token`. Without this step, automatic sign-in silently stops
   working after the first dsh restart (members stay on the "syncing credentials" page).

   ```sh
   sudo tee /etc/cron.d/dsh-server-token >/dev/null <<EOF
   SHELL=/bin/sh
   PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
   * * * * * root $(pwd)/scripts/sync-token.sh
   EOF
   ```

   About a minute after installing it, the file `auth-gate/data/dsh-token` should appear
   and get a new timestamp every minute. If it does not, run `sudo scripts/sync-token.sh`
   once by hand and read what it says: cron has to be running, and root has to be able to
   run `docker compose`.

7. Run `scripts/smoke.sh` for an end-to-end check of the stack (see "Smoke test";
   section 0 verifies that the cron job is in place and that the token file is being
   refreshed).

## Smoke test

`scripts/smoke.sh` (safe to rerun) walks a running stack through the key paths: an
unauthenticated GET redirected to the login page, login and cookie, the dsh token URL
exchange, automatic sign-in on 401 (including that the synced token matches), the
WebSocket upgrade, immediate revocation when a user is deleted, and token rotation on a
dsh restart with the old URL going dead. The first unexpected result exits non-zero. By
default it targets the public URL that `docker compose config` resolves from `.env`;
set `BASE_URL=https://host` to override. It will:

- create a temporary user `smoketest` and delete it at the end (users.json is
  hot-reloaded, nothing is restarted);
- **restart dsh once** (to verify token rotation; logged-in members keep their dsh
  cookie in practice, but open pages blip and reconnect);
- mask the token in all of its output (the full token only ever appears in the container
  logs);
- refuse a stack whose token is not being synced: section 0 fails when the cron file is
  missing or the token file has not been refreshed for a few minutes, because automatic
  sign-in would die after the next dsh restart.

## Start, stop, logs

```sh
docker compose up -d      # start (restart: unless-stopped is set, so the stack comes back after a host reboot)
docker compose ps         # state and health of the three services
docker compose logs -f    # follow the logs (the dsh logs contain the sensitive token URL: do not paste them anywhere)
docker compose restart dsh  # restart one service (a dsh restart rotates the token, see "The token URL")
docker compose stop       # stop the stack (all data is in host directories and stays)
docker compose down       # stop and remove containers and network (mounted directories such as ./dsh-home are untouched)
```

Notes:

- auth-gate reloads the user table whenever the file's mtime changes; adding a member or
  changing a password restarts nothing.
- A plain `restart` does not apply a changed `.env`: the restarted container keeps the
  environment it was created with. Recreate instead; see
  [Updating dsh-server](#updating-dsh-server).

## Updating dsh-server

A container keeps the environment it was created with, and `docker compose restart` does
not change that. So changes are applied by **recreating** containers:

| After … | Run |
| --- | --- |
| `git pull` | `docker compose build && docker compose up -d`, then `scripts/smoke.sh` |
| Editing `.env` | `docker compose up -d` (recreates caddy and dsh when the address or the proxy changed; dsh restarts, which interrupts running sessions) |
| Editing only `caddy/Caddyfile` | `docker compose up -d --no-deps --force-recreate caddy` |
| Enabling or disabling the Cloudflare Tunnel | An edit of `.env` like any other: `docker compose up -d`, with `--remove-orphans` when disabling it. See ["Cloudflare Tunnel"](#cloudflare-tunnel) |

`docker compose up -d` recreates the containers whose configuration changed and leaves
the others running; `docker compose up -d --dry-run` lists which ones it would recreate.

Do not rely on `restart` after pulling a change. The restarted container keeps its old
environment, so a changed `.env` has no effect. And a caddy container that was created
before `DSH_TRUSTED_HOST` existed cannot load the current Caddyfile once it is
restarted: it crash-loops.

After a pull that changed `compose.yml`, a Dockerfile or `.env.example`, compare your
`.env` with `.env.example` first. A variable that became required makes every
`docker compose` command fail with a message that names it.

## Upgrading dsh

dsh is a developer preview, so expect breaking changes: **upgrade by hand, one version at
a time, never automatically.** The version lives in `DSH_VERSION` in `.env` (currently
`0.2.2-rc.5`). It is passed as a build argument to `dsh/Dockerfile`, which pins the npm
package (`npm install -g @deepseek-ai/dsh@${DSH_VERSION}`), so this is the only place to
change:

1. Change `DSH_VERSION` in `.env`.
2. `docker compose build dsh` (never with `--pull`).
3. `docker compose up -d dsh` (rebuild the container; compose swaps it because the image
   changed).
4. **Surviving a rebuild is built in** (since 2026-11-23; the former restore-container.sh
   is retired). `/root` is mounted out as a whole (`dsh-root/`), so workspace symlinks,
   the npm cache, gh credentials and other runtime state survive. The apt tools
   (openssh-client, sshpass, xauth, python3), chromium with the CJK and emoji fonts, gh
   and pnpm are baked into the image, so a rebuild needs no network re-installation (the
   opt/ re-linking branch for mnemon is still in the entrypoint and is skipped when
   `opt/` does not exist). Check:
   `docker compose exec dsh test -f /root/.gitkeep && echo mount OK`.
   Symptom of a move to another host that forgot `dsh-root/`: session state goes into
   the rootfs and is lost on the next rebuild (smoke section 0 catches it).
5. Confirm with `docker compose ps` that everything is healthy. Observed behaviour:
   after a restart or rebuild **the old token URL is dead immediately**, but browser
   sessions that already exchanged the token for a `dsh-auth-*` cookie **stay valid**
   (the session state is persisted in `dsh-home/`). Logged-in members notice nothing. The
   new token is synced automatically every minute by cron (see "Automatic sign-in and
   degraded mode"); **there is no need to fetch the URL and hand it out any more**.
6. **Run `scripts/smoke.sh`; only an all-green run completes the upgrade.** Section 0
   has the mount probe and the check that the image dependencies are present, and
   section 10 verifies the whole chain 401 -> automatic sign-in -> dsh session. If the
   new dsh version changed the log format or a status code, the break shows up right
   here (then follow "degraded mode").
7. If something is wrong, roll back: set the version back and repeat steps 2 to 6.

Upgrading caddy or auth-gate: change the image tag in `compose.yml` or the sources under
`auth-gate/`, then `docker compose build && docker compose up -d`, again without
`--pull`. When you edit `caddy/Caddyfile` on its own, remember that it is a **single-file
bind mount**: an editor usually saves to a new inode while the container still sees the
old one, so recreate caddy afterwards to pick up the change:
`docker compose up -d --no-deps --force-recreate caddy`.

Upgrading gh: change `GH_VERSION` in `dsh/Dockerfile` and the sha256 below it (the
`linux_amd64.deb` line of `gh_<version>_checksums.txt` in that release), then go through
steps 2 to 6 above. The download comes from GitHub, so `LAN_PROXY` has to work during
the build. The chromium and other apt packages only refresh when their own layer is
rebuilt.

## Members

A new member needs two things: **a gate account and the root certificate.** The start-up
URL is no longer handed out: after signing in, "automatic sign-in on 401" performs the dsh
token exchange (see "Automatic sign-in and degraded mode").

1. Create the account (works whether or not the auth-gate container is running; when it
   is not, the script falls back to a one-off container):

   ```sh
   scripts/add-user.sh <name>
   # type the password at the prompt (no echo); rerun with the same name to change it.
   # The service hot-reloads: nothing to restart.
   ```

2. Hand out the root certificate `caddy/data/caddy/pki/authorities/local/root.crt`; the
   member imports it as a trusted CA:
   - macOS: Keychain Access -> System keychain -> Import -> double-click the certificate
     -> Always Trust.
   - Windows: double-click -> Install Certificate -> Local Machine -> Trusted Root
     Certification Authorities.
   - Firefox has its own certificate store: Settings -> Privacy & Security ->
     Certificates -> Import.
   - Only the certificate file itself is handed out; the CA private key never leaves the
     host.
   - The file is root:root 0600 (written by the caddy container), so reading it from the
     host needs sudo.

The member's first visit: open `https://192.0.2.10` -> enter account and password on the
login page -> automatic sign-in issues the dsh session cookie, valid for 30 days. If a dsh
restart has just happened, the member first sees the "syncing credentials" page, which
recovers by itself within about a minute. Nothing to do.

Removing a member (kicks them off at once; their gate cookie stops working immediately):
auth-gate runs as the administrator account of the host (uid 1000, see `user:` in
compose.yml), so users.json is owned by that account and can be edited directly:

```sh
tmp=$(mktemp) && jq --arg u 'name-to-remove' 'del(.[$u])' auth-gate/data/users.json > "$tmp" && cat "$tmp" > auth-gate/data/users.json && rm "$tmp"
```

`--arg` passes the name as a **literal key**, so names containing `.`, quotes or `$`
are safe too. Never write `del(.john.doe)`: jq parses that as the field `doe` under
`.john`, and when the key does not exist `del` **does nothing and still exits 0**. The
file is rewritten unchanged, the administrator believes the member is gone, and the
member's cookie and login keep working for the rest of the session.

The service hot-reloads on mtime; no container needs a restart.

## Cloudflare Tunnel

The stack is built for a private network. For members who are on neither the LAN nor the
VPN there is one optional second entrance: a **Cloudflare Tunnel**, in a container of its
own (`compose.tunnel.yml`). It is off unless it is enabled. It needs no inbound port,
public IP or port forwarding: `cloudflared` connects to Cloudflare and keeps that
connection open, and Cloudflare sends the requests through it. Cloudflare Access sits in
front of the public hostname and decides who may enter; the login gate applies behind it
as everywhere else. The tunnel container is on a Compose network of its own and reaches
caddy only, so a wrong address in the Cloudflare dashboard cannot skip the login gate.

It requires a Cloudflare account and a domain whose DNS is hosted there.

### Enable it

1. **Create the tunnel.** In Cloudflare Zero Trust: *Networks* -> *Tunnels* -> *Create a
   tunnel* -> *Cloudflared* -> give it a name. Copy the token, the long string in the
   installation command (after `--token`, or after `service install`).
2. **Add the public hostname.** In the tunnel: *Public Hostname* -> *Add a public
   hostname*. Subdomain and domain as members will type them; service type `HTTP`, URL
   `caddy:8080`. `caddy` is the Compose service name, and the connection stays inside the
   Compose network, so it is HTTP and not HTTPS.
3. **Protect it with Access.** *Access* -> *Applications* -> *Add an application* ->
   *Self-hosted*. The application domain is the public hostname with the path left empty
   (the whole domain). Add a policy with the action *Allow* that names who may use it:
   mail addresses, or a group from your identity provider.
4. **Enable it in `.env`.** Copy the three lines of the *Cloudflare Tunnel* block from
   `.env.example` into `.env`, uncommented and filled in: `COMPOSE_FILE`, `TUNNEL_TOKEN`
   (the token from step 1) and `TUNNEL_HOSTNAME` (the public hostname from step 2, without
   scheme or path).
5. **Apply it**, in the deployment directory:

   ```sh
   docker compose up -d
   ```

   This starts `cloudflared` and recreates caddy (about ten seconds without service) and
   dsh (running agent sessions are interrupted). `docker compose ps` shows `cloudflared`
   as `healthy` once it is connected, usually within a minute, and as `unhealthy` after
   about two minutes when it is not.
6. **Verify it:**

   ```sh
   scripts/smoke.sh
   ```

   Section 11 checks the tunnel: that dsh serves the public hostname, which address the
   login rate limit counts on each entrance, and that the public hostname answers behind
   Cloudflare Access.

### Turn it off

Remove the `COMPOSE_FILE` line from `.env` (and, if you like, the two variables), then:

```sh
docker compose up -d --remove-orphans
```

It removes the `cloudflared` container and recreates caddy and dsh without the tunnel.
The Compose network `<project>_tunnel` stays behind; `docker network rm <project>_tunnel`
removes it. Delete the public hostname, or the whole tunnel, in the Cloudflare dashboard
afterwards.

### What to know

- **Run `docker compose` in the deployment directory.** `COMPOSE_FILE` holds relative
  paths, which Compose resolves against the directory the command is run from, and it is
  not read at all when `--env-file` is given. Such a command addresses the base stack
  only.
- `docker compose config` prints `TUNNEL_TOKEN`; do not paste its output anywhere.
- After changing `TUNNEL_HOSTNAME`, run `docker compose up -d`. dsh keeps the environment
  it was created with, and the smoke test says so when it was not recreated.
- After a cloudflared version bump, `docker compose up -d cloudflared`, then the smoke
  test.
- Through the tunnel the visitor's address comes from `Cf-Connecting-Ip`. Cloudflare
  features that remove or rewrite that header ("Remove visitor IP headers", Pseudo IPv4
  in mode "Overwrite Headers") make all tunnel visitors share one login rate-limit bucket:
  five wrong passwords per minute, all visitors together.
- Cloudflare closes a proxied request whose origin sends nothing for 100 seconds (its
  error 524), so a dsh stream that stays silent for that long may have to reconnect.
- dsh still advertises `DSH_PUBLIC_URL`, the private address.
- A dsh session belongs to the address it was issued for, so the private and the public
  hostname are two sessions. Automatic sign-in takes care of the second one.

## The token URL

dsh uses a new token on every process restart, and the old URL stops working. After a
restart, an upgrade or a rebuilt container:

```sh
scripts/dsh-url.sh
# prints something like https://192.0.2.10/?token=...
```

**This URL is a secret** (it is the holder's dsh login credential): give it only to the
person who should have it; never broadcast it or paste it into a chat. It also stays in
`docker compose logs dsh`. Any account on the host that can talk to docker can read it in
the first place, so keep that in mind before exporting or forwarding logs.

## Automatic sign-in and degraded mode

The gate account is the only credential a member has: handing out the token "by hand"
was replaced by "automatic sign-in", and after logging in a member enters dsh without
ever seeing a URL. The chain:

```
member requests / (no dsh cookie) -> dsh answers 401
  -> Caddy catches the 401 -> 302 /auth/dsh-entry
  -> auth-gate: gate cookie OK and token ready -> 302 /?token=<current> (plus a 30-second loop guard cookie)
  -> dsh issues the dsh-auth-* session cookie -> 30 days without noticing
```

The token sync chain: dsh rotates the token on every restart. The host cron job
(`/etc/cron.d/dsh-server-token`) runs `scripts/sync-token.sh` every minute, which
extracts the newest token from the dsh logs and writes it atomically to
`auth-gate/data/dsh-token` (auth-gate hot-reloads on mtime; a failed parse leaves the old
file alone).

- **The sync window is about one minute**: between a dsh restart or upgrade and the next
  cron run, a member who is not yet signed in to dsh sees the "syncing credentials" page
  (it refreshes itself every 20 seconds). Worst case it heals by itself in about 100
  seconds (unlucky timing: wait for the container plus the next cron run). No manual step
  is involved.
- A second sign-in attempt within 30 seconds is stopped by the loop-guard cookie and
  gets the "syncing credentials" page, so there is no 401 loop. Signed-in sessions (dsh
  cookie) are never affected.
- **Degraded mode** (any link in the chain fails; typically a dsh upgrade changed the
  log format, so `sync-token.sh` cannot parse it and the token file keeps the old value):
  automatic sign-in stops working but **nobody is locked out**. First tell the two
  situations apart:
  - **Inside the sync window** (up to a minute after a dsh restart; the next cron run
    fills it in): a new member or a new device waiting on the sync page is expected
    behaviour. Let it heal; there is nothing to do.
  - **Parsing keeps failing** (`sync-token.sh` errors repeatedly, the token file stops
    updating): this **does not heal**. Fall back by hand right away: fetch the current
    token URL with `scripts/dsh-url.sh` and have the member open it once (the manual
    mode from before automatic sign-in; if `dsh-url.sh` itself fails, take the URL from
    `docker compose logs dsh | grep token=`). Then fix the parsing in `dsh-url.sh`: that
    repair is a required part of restoring automatic sign-in, not an optional one. Once
    it is fixed, the next cron run restores sign-in without restarting anything.

## Backup

All state is in host directories, so a cold backup is an rsync (for a hot backup, at least
avoid times when sessions are writing heavily). **sudo is required**: `dsh-home` contains
root-owned files (`.credentials.yaml` 0600, `storages/` drwx------; the dsh container
writes as root), and the pki tree under `caddy/data` is root:root 0600 as well. A plain
`rsync -a` as an unprivileged user fails file by file with exit 23, and the resulting
archive **lacks the credentials, sessions and CA**:

```sh
sudo rsync -aH dsh-home/       <backup>/dsh-home/        # sessions, plugins, credentials, config (includes the model API keys)
sudo rsync -aH dsh-root/       <backup>/dsh-root/        # the container's /root state (npm cache, workspace symlinks, gh credentials)
sudo rsync -aH workspace/      <backup>/workspace/       # what the agent produced in the shared workspace
sudo rsync -aH auth-gate/data/ <backup>/auth-gate-data/  # users.json (lost = everyone sets a new password)
sudo rsync -aH caddy/data/     <backup>/caddy-data/      # the internal CA (lost = hand out a new root certificate)
```

Keep `.env` safe separately (it contains `AUTH_GATE_SECRET`). It is not in git, and the
backup medium should be encrypted.

Restore with `sudo rsync -aH` as well (root keeps the ownership). If the ownership was
lost (the backup medium does not preserve it), restore it by writer, or the containers
cannot write to their directories:

```sh
sudo chown -R root:root dsh-home/ workspace/ caddy/data/   # the dsh and caddy containers write as root
sudo chown -R 1000:1000 auth-gate/data/                    # auth-gate runs as the administrator account (compose user:)
```

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| The whole site is unreachable | `docker compose ps`: is caddy up? `docker compose logs caddy`; does the host firewall let 80/443 through (ports published by Docker go through the FORWARD/DOCKER chains and bypass ufw; normally docker-proxy listens on 80/443 directly); is the member on the private network? |
| Any `docker compose` command fails with `DSH_TRUSTED_HOST is not set` / `DSH_VERSION is not set` | `.env` is missing a required value or has a misspelled key (no `.env` at all: `cp .env.example .env`). These two have no default on purpose |
| caddy crash-loops after a restart; its log shows a Caddyfile parse error (wrong argument count for `default_sni`) | The container was created before `DSH_TRUSTED_HOST` existed (a checkout updated with `git pull` and never followed by `docker compose up -d`), so it has no such variable. Recreate it: `docker compose up -d --no-deps --force-recreate caddy`. Also make sure `.env` defines `DSH_TRUSTED_HOST` |
| `curl https://192.0.2.10` returns `000` / exit 35, and openssl shows `tlsv1 alert internal error` | The site is a bare IP, the client sends no SNI (RFC 6066 forbids IP literals there), and Caddy cannot choose a certificate. `default_sni {$DSH_TRUSTED_HOST}` in the global block of the Caddyfile is what fixes this. It and the site address both come from `.env`; do not hardcode anything in the Caddyfile |
| caddy does not start for a long time | caddy's `depends_on` requires both dsh and auth-gate to be healthy: `docker compose ps` shows which one is unhealthy; `docker compose logs dsh` / `logs auth-gate` show why |
| auth-gate does not start (crash-loop: `stat /data/users.json: no such file`) | On a first deploy users.json does not exist yet. Create the first user (`add-user.sh` falls back to a one-off container by itself), then `docker compose up -d` |
| dsh is not healthy | Run the healthcheck command by hand: `docker compose exec dsh sh -c 'ip=$(hostname -i | awk "{print \$1}"); curl -s --noproxy "*" -o /dev/null --max-time 5 "http://$ip:3080/"; echo "rc=$? ip=$ip"'`. **Never add `-f`**: dsh answers 401 to an unauthenticated request for `/` (no token, no cookie), and `curl -f` treats 401 as failure, so the container would never be healthy |
| The browser warns about the certificate | The root certificate is not imported, or the CA changed: hand out `caddy/data/caddy/pki/authorities/local/root.crt` again (after a CA change the old one has to be removed; the file is root:root 0600, so reading it from the host needs sudo) |
| Keeps bouncing to the login page / returns to it right after signing in | `docker compose logs auth-gate`; the cookie is Secure, so the site has to be opened over `https://`; 5 wrong passwords trigger the rate limit, wait a minute |
| Signed in, but stuck on the "syncing credentials" page | The token sync chain of automatic sign-in is broken or inside its window: wait a minute first (cron fills it in); if it persists, `docker compose logs auth-gate`, and fall back by hand: `scripts/dsh-url.sh` prints the URL to give to that member (see "Automatic sign-in and degraded mode") |
| A hand-made curl WebSocket probe keeps getting 404 | The probe needs `--http1.1`: with the default ALPN the connection negotiates h2, where the `Connection`/`Upgrade` headers are dropped, and dsh sees a plain GET (404). Only cookies + `--http1.1` + the three `Sec-WebSocket-*` headers get a 101 |
| `add-user.sh` fails | The password must not be empty; when the container is not running it falls back to the one-off-container mode (which needs docker permission locally) |
| `dsh-url.sh` finds no URL | The container is not up or the logs rotated: look at `docker compose logs dsh` |
| `smoke.sh` exits at once, saying it cannot tell which stack to test | There is no valid `.env` in the current directory (the script runs from the deployment directory and takes its target from `docker compose config`); or set `BASE_URL=` explicitly |
| `smoke.sh` stops in section 0: `token file does not exist`, or `token file is ... old` | The cron job is not syncing the token: cron is not running, or root cannot run `docker compose`. Run `sudo scripts/sync-token.sh` by hand and read what it says. After a `git pull`, make sure `.env` still has every required variable |
| `smoke.sh` fails in section 0 at `cloudflared healthy` | The tunnel is enabled but not connected: `docker compose logs cloudflared` says why (a token that does not parse makes it exit and restart; a token Cloudflare does not accept, or no route to Cloudflare, leaves it running and not ready). The private entrance is not affected. See "Cloudflare Tunnel" |
| The public hostname answers `502 Bad Gateway` or error `1033` | With the tunnel enabled: `docker compose ps cloudflared`. Not `healthy` means the tunnel is down (`docker compose logs cloudflared`). `healthy` means cloudflared cannot reach caddy, which then was not recreated when the tunnel was enabled and is missing from the tunnel network: `docker compose up -d`. Also check that the service of the public hostname is `http://caddy:8080` |
| The public hostname shows the dsh-server login page, with no Cloudflare Access login before it | Nothing but the login gate is in front of the public hostname, so the login page is on the Internet. Add the Access application ("Cloudflare Tunnel", step 3) now; `scripts/smoke.sh` fails in section 11 until it is there |
| Everyone through the tunnel is rate-limited together (`尝试过多`) | Cloudflare does not send `Cf-Connecting-Ip`, or rewrites it, so all tunnel visitors share one bucket: five wrong passwords per minute in total. Check the zone's Managed Transforms and its Pseudo IPv4 setting ("Cloudflare Tunnel", what to know) |
| `smoke.sh` fails in section 11: `dsh was created with TUNNEL_HOSTNAME "..."` | `.env` was changed after dsh was created. A container keeps the environment it was created with: `docker compose up -d` recreates dsh |
| A member forgot their password | `scripts/add-user.sh <name>` resets it |
| The build fails with a mention of gcr.io | `--pull` was used, or the new machine has not had its image sources prepared; see "Before you build: image sources" |

## Known risks

- dsh is a developer preview: expect breaking changes; upgrades are manual, one version at
  a time, and can be rolled back.
- The agent executes commands generated by a model. The container is the isolation
  boundary: the agent can only touch what is mounted into it (`dsh-home/`, `workspace/`,
  `dsh-root/`). The dsh container runs as root and has no docker socket mounted: do not
  add `privileged`, host directories or the docker socket to it.
- **Everyone shares the same dsh instance**: sessions, credentials and the workspace are
  shared between members. The login gate answers "who may come in", not "whose data is
  whose".
- The token URL stays in `docker compose logs dsh` (a secret; do not forward the logs).
- The attack surface is the private network plus the login gate. Without the Cloudflare
  Tunnel the stack is not exposed to the public Internet, and port 443 should not be
  forwarded to it. With the tunnel, the public hostname is part of the attack surface:
  Cloudflare Access and the login gate are in front of it, and Access has to stay there
  (`scripts/smoke.sh` fails when it is missing).
- **Ports published by Docker go through the FORWARD/DOCKER chains and bypass ufw
  rules.** If ufw is enabled to tighten things up, explicit `DOCKER-USER` chain rules are
  required; `ufw deny 443` alone does not stop this stack.

## Variables (`.env`)

| Variable | Purpose | Default / example |
| --- | --- | --- |
| `DSH_TRUSTED_HOST` | **Required.** The IP address or hostname members type into the browser (no scheme, no port). Drives everything from one place: the Caddy site address and certificate, dsh's `--trusted-host`, and the default of `DSH_PUBLIC_URL`. After changing it, run `docker compose up -d` | `192.0.2.10` |
| `DSH_VERSION` | **Required.** The deployed version (a build argument consumed by npm install in the Dockerfile; an upgrade changes only this) | `0.2.2-rc.5` |
| `AUTH_GATE_SECRET` | **Required.** The session-signing key (`openssl rand -hex 32`); changing it means everyone logs in to the gate again | - |
| `DSH_PUBLIC_URL` | The URL dsh advertises; set only when it has to differ from the default | `https://<DSH_TRUSTED_HOST>/` |
| `LAN_PROXY` | LAN proxy injected into the dsh build (npm, apt, gh downloads) and into the container's outbound traffic (git clone, ...); empty disables it; does not cover the daemon's image pulls | empty |
| `NO_PROXY` | Addresses exempt from the proxy (private and local ranges must be in it; the healthcheck has its own `--noproxy` hard stop) | `localhost,127.0.0.1,...` |
| `AUTH_SESSION_HOURS` | Login session lifetime in hours | `720` (30 days) |
| `GOPROXY` | Go module proxy for building auth-gate | `https://goproxy.cn,direct` |
| `COMPOSE_FILE` | Optional. `compose.yml:compose.tunnel.yml` enables the Cloudflare Tunnel (["Cloudflare Tunnel"](#cloudflare-tunnel)) | unset |
| `TUNNEL_TOKEN` | With the tunnel: **Required.** The token of the tunnel (Cloudflare Zero Trust, Networks, Tunnels). A secret: whoever holds it can connect a server to the tunnel | - |
| `TUNNEL_HOSTNAME` | With the tunnel: **Required.** The public hostname, for example `dsh.example.org` (no scheme, no path); the second authority dsh accepts. After changing it, run `docker compose up -d` | - |
