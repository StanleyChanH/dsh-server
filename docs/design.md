# dsh-server design

Date: 2026-11-21 (increments: 2026-11-22 automatic sign-in, 2026-11-23 `/root`
persistence, 2026-11-24 dependencies baked into the image, 2026-11-26 public access
through a Cloudflare Tunnel)
Status: implemented

> Chinese version: [design.zh-CN.md](design.zh-CN.md). Runbook: [operations.md](operations.md).

## Goals and motivation

Run DeepSeek Harness (dsh) as the team's always-on agent server: a Docker Compose
stack that stays up, so team members can use the same agent instance from anywhere
through a browser (session history, plugins and configuration all persist).

## Goals

- dsh stays up with `restart: unless-stopped`; upgrades stay under control (version
  pinned)
- Team members reach it over a private network (VPN subnet route) at
  `https://<DSH_TRUSTED_HOST>`
- Everything worth maintaining lives under the deployment directory
  (`/path/to/dsh-server/`)
- The agent has a persistent shared workspace, and what it produces lands on the host

## Non-goals

- No port forwarded to the public Internet. The optional Cloudflare Tunnel
  (`compose.tunnel.yml`, 2026-11-26) is the one public entrance, behind Cloudflare Access
- No multi-tenancy or per-user permissions (dsh currently has a single token plus a
  cookie; the login gate, auth-gate, adds a password per person on top)
- dsh is not built from source (the official npm package is used)

## Constraints (what recon found)

- dsh has no official image; the npm package is `@deepseek-ai/dsh`, currently deployed
  at `0.2.2-rc.5` (npm `latest`; from 0.2.3 on, Node requirements go up: 24.x >=
  24.22.3, or >= 26.13.1)
- dsh is a developer preview, so expect breaking changes: the version must be pinned
  and upgrades are manual
- `dsh web` binds 127.0.0.1:3080 by default; `--host` only accepts a concrete address
  (it rejects the wildcard), and the official JSDoc explicitly names "the container's
  own IP" (`hostname -i`) for container setups
- The reverse proxy has to preserve the Host header, pass WebSockets through, name the
  authority the browser shows with `--trusted-host`, and announce the external address
  with `--public-url` (no path prefix, so cookies need no rewriting)
- Authentication: the start-up URL contains a token, which is exchanged for a signed
  cookie valid for 30 days; a process restart rotates the token (the new URL used to
  have to be taken from the logs and handed out)
- All state lives in `$DSH_HOME` (profiles, sessions, credentials, configuration)
- Security: dsh is unaudited and executes model-generated commands. The container is
  the isolation boundary: the agent can only touch what is mounted into it

## Decisions

- **How dsh is containerised**: `node:24-slim` plus the npm package installed globally
  at a pinned version. "Build from source" was rejected: heavy, slow, and more to
  maintain per upgrade; the npm package is the official release artefact.
- **Entry point**: keep the existing private-network route (a VPN subnet route); members
  type `https://<DSH_TRUSTED_HOST>`. The alternative "the server joins the tailnet too"
  (a ts.net name plus free certificates) was rejected, to leave the network
  architecture as it is.
- **TLS**: Caddy with `tls internal` (a built-in CA that signs IP-SAN certificates);
  members import the root certificate once (it is exported from the host and handed
  out).
- **Authentication**: auth-gate (a login gate written in Go, sources in `auth-gate/`)
  replaced the originally planned Caddy `basic_auth`. Caddy's `forward_auth` calls
  `/auth/check` first; a GET without a session is answered with a 302 to the embedded
  login page (`auth-gate/login.html`; anything else gets 401, because a browser only
  navigates on 3xx). The bcrypt user table hands out one account per person, each
  revocable on its own; signing in issues the HMAC-signed cookie `gate_session` (30
  days by default). The dsh start-up token sits on top as a second layer.
- **Binding**: the entrypoint script reads `hostname -i` and passes it to `--host` (on
  the Compose network it is used by caddy alone; port 3080 is not published, which
  matches the official "keep the port limited to a trusted proxy").

## Deployment layout

```
/path/to/dsh-server/
├── docs/design*.md         # this document (design.md is the English version)
├── docs/operations*.md     # runbook (start/stop, upgrades, members, token URL, backup, troubleshooting)
├── README*.md              # overview and quick start
├── compose.yml             # the three services: dsh + auth-gate + caddy
├── .env / .env.example     # variables such as DSH_VERSION (.env is not committed)
├── dsh/
│   ├── Dockerfile          # node:24-slim + git/curl/ca-certificates + the npm package
│   └── entrypoint.sh       # reads the container IP -> exec dsh web --no-open --host <ip> \
│                           #   --public-url https://<DSH_TRUSTED_HOST>/ --trusted-host <DSH_TRUSTED_HOST>
├── auth-gate/              # the Go login gate (embedded login.html, bcrypt user table, HMAC cookie)
│   └── data/               # where users.json lives (mounted as /data)
├── caddy/
│   ├── Caddyfile           # https://{$DSH_TRUSTED_HOST} { tls internal; forward_auth auth-gate; reverse_proxy dsh:3080 }
│   ├── data/               # Caddy's persistent state (includes the internal CA; root.crt is handed out from here)
│   └── config/
├── scripts/                # add-user.sh (add a member / change a password), dsh-url.sh (fetch the start-up URL),
│                           #   sync-token.sh (cron: sync the token), smoke.sh (end-to-end test),
│                           #   check.sh (the checks CI runs)
├── dsh-home/               # mounted as $DSH_HOME: sessions, plugins, credentials, configuration
├── dsh-root/               # mounted as /root: the container's home directory survives rebuilds
└── workspace/              # mounted as /workspace: the agent's shared workspace (the container's WORKDIR)
```

Composition notes: caddy publishes 80 (redirect only) and 443; dsh and auth-gate publish
no ports; `DSH_HOME=/data/dsh`; json-file log limits stop the logs from growing without
bound. The model provider's API key is configured once in the web UI and persists in
dsh-home.

## Runbook (delivered as `docs/operations.md`)

- Commands to start, upgrade and stop
- After a restart, fetch the new start-up URL: `scripts/dsh-url.sh` extracts it from
  `docker compose logs dsh`
- Onboarding a member: create the account with `scripts/add-user.sh` and hand out the
  root certificate (the start-up URL has been taken over by automatic sign-in since;
  see the increment below)
- Backup: rsync `dsh-home/` and `workspace/`
- The known risks (developer preview, token rotation on restart, an agent that can
  write to the workspace)

## What deployment verified (2026-11-21, smoke test of the original plan)

- Inside a Compose network `hostname -i` reliably returns a single IPv4 address
  (172.18.0.2 at the time), so probing 3080 on it works as a healthcheck. But `/`
  answers 401 when unauthenticated, so the probe **must not use `curl -f`** (the
  container would never be healthy). Liveness was relaxed to "one HTTP answer from the
  port".
- Caddy's internal CA signs an IP-SAN certificate as expected (a leaf plus an
  intermediate, two certificates in all). Two pitfalls found in practice:
  1. A bare-IP site has no SNI (RFC 6066 forbids IP literals), and without an SNI Caddy
     cannot choose a certificate, so the handshake fails with `alert internal error`. It
     takes a global `default_sni` (now `{$DSH_TRUSTED_HOST}`).
  2. `forward_auth` keeps the original method and URI, and the gate picks the refusal
     code by method: GET gets 302 (a browser only navigates on 3xx), everything else
     gets 401 (API semantics). Both carry a Location to come back to.
- WebSocket passthrough verified: `/api/remote.mux` with both cookies and the HTTP/1.1
  upgrade headers returns 101. The probe has to use `--http1.1`: once ALPN negotiates
  h2, the `Connection`/`Upgrade` headers are dropped, dsh sees a plain GET (404), and
  "there is no WS endpoint" was wrongly concluded from that once.
- dsh's two session layers, observed: the gate cookie only opens the door; the token
  URL is exchanged once for `dsh-auth-*` cookies (state persisted in dsh-home). When dsh
  restarts, the old token URL is dead at once (401), an existing dsh cookie stays valid,
  and the gate cookie is unaffected.
- Host firewall: nothing in this stack configures one; ports published by Docker are
  listened on by docker-proxy directly. Reminder: they go through the FORWARD/DOCKER
  chains and bypass ufw, so tightening access on a host that runs ufw means writing
  DOCKER-USER chain rules.
- auth-gate runs as the administrator uid of the host (`user: "1000:1000"` in compose),
  so the owner of users.json lines up with the host account and removing a member by
  editing it needs no sudo (before, the container wrote it as root with 0600 and the
  host needed sudo).
- First-deploy ordering pitfall: auth-gate fails fast and crash-loops while users.json
  does not exist; `add-user.sh` got a fallback to a one-off container for when the
  service container is unavailable, so the first user can be created before `up`.

## Increment: automatic sign-in on 401 (2026-11-22, confirmed)

**Motivation**: two gates (the gate account and the dsh token URL) were too much
ceremony for new members. Handing out the dsh token "by hand" became "automatic
sign-in": the gate account is the only credential.

**Accepted cost**: the dsh token is no longer a second secret facing members (the gate
is effectively "an account handed out by the admin"), and the token appears once in the
member's browser history.

**Flow**:

```
member requests / (no dsh cookie) -> dsh answers 401
  -> Caddy handle_response catches the 401 -> 302 /auth/dsh-entry
  -> auth-gate: gate cookie OK and token file ready -> 302 /?token=<current>
  -> dsh issues a session cookie -> 30 days without noticing
```

**Components and details**:

1. `scripts/sync-token.sh` (host): extracts the newest token from
   `docker compose logs dsh --no-color` (the same logic as dsh-url.sh) and checks its
   shape; **a failed parse writes nothing** (fail-safe: rather nothing than a stale
   token); on success it writes `auth-gate/data/dsh-token` atomically (0600, the token
   string only). Cron: `/etc/cron.d/dsh-server-token`, every minute.
2. auth-gate `GET /auth/dsh-entry` (any method):
   - no valid gate cookie -> 302 `/auth/login?next=/`
   - token file ready -> 302 `/?token=<token>`, plus the `gate_entry_retry` cookie
     (value = unix seconds, MaxAge 35s, HttpOnly)
   - retry cookie present and younger than 30s (just tried, still looping), or token
     not ready (missing or empty) -> 200 with a minimal "syncing credentials, back in
     about a minute" page (stops a restart-sync-window loop).
     "Missing -> sync page" only holds at start-up: if the file is missing at
     start-up the token stays empty; if the file is moved away while running, stat
     fails and reload keeps serving the value cached by mtime (fail-open; the guard
     page rate-limits the loop). Empty is another matter: after TrimSpace it is the
     empty string, so the sync page as usual.
   - the token file is read with the same mtime-cache pattern as users.json
3. Caddyfile: inside the `reverse_proxy dsh:3080` block,
   `@dsh_unauth status 401` + `handle_response @dsh_unauth { redir * /auth/dsh-entry 302 }`.
   (A `redir` whose first token starts with `/` is treated as a path matcher and never
   matches, so the explicit `*` is required.) The interception applies to every 401 dsh
   produces (browser semantics; accepted).
4. New smoke.sh sections: gate cookie without a dsh cookie -> `/` returns
   302 -> `/auth/dsh-entry` -> 302 -> `/?token=...`, following that is not a 401;
   what sync-token.sh wrote matches the logs.

**Degraded mode** (any link fails, for example a dsh upgrade changed the log format or
a status code): automatic sign-in stops working but nobody is locked out; it falls back
to the manual mode (`dsh-url.sh` fetches the URL, which is handed to the member).
Running smoke.sh after every upgrade finds a break at once. The upgrade procedure
became: change `DSH_VERSION` -> rebuild -> `scripts/smoke.sh` all green, or it is not
done.

**Onboarding a member is now two things**: a gate account and the root certificate (no
token URL to hand out any more).

## Increment: `/root` persistence, deep reset, retirement of the restore script (2026-11-23, implemented)

**Motivation**: the structural fix for the 2026-11-22 upgrade incident first lived in
restore-container.sh (replayed automatically by the entrypoint), but that was "repair
after the fact": every rebuild still had to install apt/pnpm over the network and
replay symlinks. The cure is to stop state from landing on the evaporating rootfs in
the first place.

**Decisions**:

1. **`./dsh-root:/root` mounted out as a whole** (seeded by `docker cp` from the
   running container, including a 200M npm cache). Any state the team adds under
   `/root` from now on survives rebuilds automatically. Accepted side effect: new files
   an image update puts in `/root` are shadowed by the mount and do not appear.
2. **Deterministic dependencies baked into the Dockerfile**: the apt tools
   (openssh-client, sshpass, xauth, python3) and pnpm, which the team used to install
   at run time. A rebuild after an upgrade needs no network re-installation.
3. **The entrypoint takes over re-linking `/usr/local`**: the gh and mnemon binaries
   live on the persistent volume `/data/dsh/opt/`, and the entrypoint runs an
   idempotent `ln -sf` (instant, no network needed). `/usr/local` is deliberately
   **not** mounted: that would shadow the pinned dsh in the image and silently undo
   upgrades. (Superseded on 2026-11-24 for gh: it is baked into the image now.)
4. **restore-container.sh retired** (together with CONTAINER-UPDATE-GUIDE.md, kept as
   a backup tarball on the host). Three-way split of responsibilities: **the image
   owns deterministic dependencies, mounts own runtime state, the entrypoint only
   re-links after mounting.**
5. The object checked by smoke section 0 moved with it: `/root/.gitkeep` is in place
   (the mount probe; at first `/root/normal`, changed to that structural marker the
   same day); the restore script is no longer what is checked.
6. **Deep reset** (same day): sessions, attachments, profiles and usage statistics
   were cleared (backup kept, 178M); workspaces, credentials, gh credentials and
   pairing were kept. Team members had to reinstall skin plugins (a known cost).

**Verified**: two rebuilds plus smoke sections 0 to 10 all green; gh, mnemon, pnpm and
the apt tools present; `/root` symlink timestamps unchanged across rebuilds.

## Increment: chromium, fonts and gh baked into the image (2026-11-24, implemented)

**Motivation**: the team had apt-installed chromium and the CJK/emoji fonts by hand in
the running container (without `--no-install-recommends`, which pulled in 327
packages). They lived in the writable layer and evaporated on a rebuild, the same class
of problem as the 2026-11-22 incident. gh was not installed in the container at the
time (deleted with `opt/` in the 2026-11-23 cleanup), so this was a new installation,
not a preservation. Design document:
`docs/superpowers/specs/2026-11-24-bake-chromium-gh-design.md` (Chinese).

**Decisions**:

1. **chromium and the fonts go into the image**: `chromium fonts-noto-cjk
   fonts-noto-color-emoji fonts-liberation`, with `--no-install-recommends` (158
   packages more inside the image). fonts-liberation is a recommended package of
   chromium, listed explicitly to stand in for the Arial/Times faces common on the web.
2. **gh from a pinned GitHub release**: 2.105.0 plus a sha256 check. Debian 12 only
   ships 2.23.0.
3. **Both layers sit before the npm layer**: a dsh upgrade only rebuilds the npm layer
   and what follows; after it, every upgrade would download the browser and fonts
   again.
4. **The entrypoint stops re-linking `opt/gh`**: gh is in the image, and a re-linked
   `/usr/local/bin/gh` would shadow the pinned one.
5. **Smoke section 0 checks that the image dependencies are present**: something lost
   in a rebuild shows up at once. It also asserts that gh resolves to `/usr/bin/gh`
   inside the image and that the CJK/emoji fonts resolve: neither loss raises an
   error, it only shows up as a shadowed binary or missing-glyph boxes in screenshots.

**Verified**: in a one-off container, Chromium 156.0.8078.108 and gh 2.105.0 were
present; `fc-match` found Noto Sans CJK SC / Noto Sans Mono CJK SC / Liberation Sans /
Noto Color Emoji; a headless render of "Chinese + emoji + Latin" showed no missing
glyphs, and starting as root without `--no-sandbox` fails with a clear error. The image
went from 2.19GB to 3.4GB (the chromium/fonts layer is 804MB, the gh layer 42MB). On
2026-11-25 the container was moved to the new image and smoke sections 0 to 10 were all
green, including the new dependency check.

## Increment: public access through a Cloudflare Tunnel (2026-11-26, implemented)

**Motivation**: members who are on neither the LAN nor the VPN could not reach the stack
at all, and forwarding a port is ruled out by the security model: dsh runs whatever its
users ask it to. A Cloudflare Tunnel opens no inbound port, and Cloudflare Access can sit
in front of the public hostname. The login gate stays: Access answers "who may reach the
server", the gate "which member is this", and dsh has its own session on top. Plan:
`docs/superpowers/plans/2026-11-26-cloudflare-tunnel.md`.

**Decisions**:

1. **An override file, not a Compose profile**: `compose.tunnel.yml`, loaded through
   `COMPOSE_FILE` in `.env`. A profile cannot make a missing variable fail: `${TUNNEL_TOKEN:?}`
   inside a service under an inactive profile breaks every Compose command, and without it
   a missing token only shows as a container that never connects. With the override, a
   deployment that does not set `COMPOSE_FILE` renders exactly what it rendered before.
2. **cloudflared on a network of its own, caddy on two**: cloudflared reaches caddy and
   nothing else. A wrong service address in the Cloudflare dashboard (`dsh:3080`, say) is
   not routable from there, so no dashboard mistake can skip the login gate.
3. **A second caddy listener, `http://:8080`, not published, with the same route chain**:
   the routes moved into a snippet that both listeners import, and the argument of the
   snippet names what each proxy adds upstream. The listener exists whether or not the
   tunnel is enabled: a site block whose address is an empty environment placeholder does
   not adapt.
4. **The client address comes from `Cf-Connecting-Ip` on the tunnel listener only**: that
   `header_up` sits in a snippet the tunnel listener imports; the private listener imports
   an empty one and keeps Caddy's default (the peer's address; a client-sent
   `X-Forwarded-For` is discarded). Writing the default down as `header_up X-Forwarded-For
   {remote_host}` would be the same thing, and Caddy warns that it is unnecessary. The
   address has to be right on all three hops (`/auth/*`, `forward_auth`, dsh), because
   auth-gate counts login failures by the first element of `X-Forwarded-For`. That header
   itself cannot be used through Cloudflare: a request that already carries one has the
   visitor's address appended, so its first element is whatever the visitor sent.
5. **The public hostname is a second authority for dsh**: the entrypoint adds
   `--trusted-host "$TUNNEL_HOSTNAME"` when the variable is set. dsh's fence refuses API
   requests whose `Host` it was not told about, and through the tunnel the `Host` is the
   public hostname. The entrypoint leaves out a value that is not a bare host name, or
   host:port, and says so on stderr: dsh refuses to start on anything else, and caddy
   waits for a healthy dsh, so the typo would take the private entrance down as well. The
   smoke test reads dsh's command line to tell that from a stale container and from an
   image built before this change.
6. **Cloudflare Access is configured in the dashboard; the stack does not check the Access
   JWT.** The gate and dsh's own session apply behind it. The smoke test fetches the
   public hostname from the host and fails when the answer is not the redirect of Access.
   Two things follow from the tunnel itself. Cloudflare terminates TLS for the public
   hostname and is in the path of everything members send and receive, the dsh token URL
   and the session cookies included. And the tunnel listener answers any `Host`: the
   stack knows one public hostname, and any other hostname routed to the tunnel reaches
   the login page unless it has an Access application of its own. The dashboard has a
   setting per route, *Protect with Access*, that makes cloudflared check the Access JWT
   before it forwards a request; this stack neither sets nor tests it.
7. **Smoke section 11** checks what a deployment can get wrong after enabling it: dsh was
   created with the current `TUNNEL_HOSTNAME` and serves it (a session is obtained through
   the tunnel listener, and a WebSocket upgrade is accepted), the rate limit counts the
   right address on each listener, and Access is in front of both the front page and the
   login endpoint.
8. **The protocol is cloudflared's default unless a deployment says otherwise.** `auto`
   starts with QUIC and falls back to HTTP/2. `TUNNEL_PROTOCOL` in `.env` fixes it (`quic`
   or `http2`) through `TUNNEL_TRANSPORT_PROTOCOL`. The default is written out as `auto`,
   because an empty value is not `auto`: cloudflared exits on "unknown protocol".

**Measured, and relied upon** (Caddy 2.10.2, cloudflared 2026.11.2, Compose 5.5.0):

- `header_up` takes no request matcher: `header_up @name X-Foo value` adapts without an
  error and becomes a regular-expression replacement on a header named `@name`.
- A site block without an address does not adapt: "server block without any key is global
  configuration, and if used, it must be first".
- `COMPOSE_FILE` is resolved against the directory a command is run from, and it is not
  read with `--env-file`. `docker compose up -d --remove-orphans` removes the cloudflared
  container and leaves the `<project>_tunnel` network behind.
- cloudflared exits at once on a token that does not parse (status 255) and when it has no
  network at all (status 1). With a well-formed token that Cloudflare does not accept it
  runs, answers 503 on `/ready`, and is restarted by Docker from time to time.
  `cloudflared tunnel ready` reads `TUNNEL_METRICS`; it exits 0 against an endpoint that
  answers 200 and 1 otherwise. The protocol is set with `TUNNEL_TRANSPORT_PROTOCOL`
  (`auto`, `quic`, `http2`); an empty or unknown value makes cloudflared exit at once.
- Where UDP to Cloudflare is throttled, measured on one such network: with `auto`, two of
  three starts ended in `Register tunnel error ... context deadline exceeded` and exit
  code 1, the third fell back to HTTP/2 after a QUIC timeout and then held four HTTP/2
  connections without an error. With `http2` forced, three starts each registered four
  connections in 6 to 8 seconds.
- A dsh session is bound to the authority it was issued for: the cookie payload names it,
  and dsh answers 401 to the same cookie presented with another `Host`.
- Neither dsh nor auth-gate reads `X-Forwarded-Proto`, and both cookies are `Secure`
  regardless, so the tunnel listener does not pass it on.

**Verified**: `scripts/check.sh` runs the Caddyfile against a stand-in for auth-gate and
dsh and asserts what each listener hands the backends, with forged headers on both; six
mutations of the Caddyfile are each reported. The Compose checks assert the topology, the
two required variables and the protocol; ten mutations of the override are each reported.
The entrypoint's argument list is checked in the base image of the dsh image, also for an
empty `TUNNEL_HOSTNAME` and for values that are not bare host names. A unit test pins that
an empty `X-Forwarded-For` falls back to the peer. On a sandbox stack with a real dsh built
from the tree (no published ports, a project and image tags of its own), the unchanged
smoke test passed with the tunnel off;
with stand-ins for cloudflared and Access, sections 0 to 11 passed, and each new check was
also seen failing for its own reason, among them dsh running the previous entrypoint and a
Caddyfile that counts a forged address. With the entrypoint passing a pasted URL on, dsh
crash-looped and a recreated caddy never started; with the entrypoint as committed, dsh
runs and the private entrance answers. No running deployment was involved.
**Checked against real tunnels on 2026-11-26.** With a stand-in for caddy on a network of
its own, and no deployment involved: the edge answers the smoke test's unauthenticated
request with `302` to `https://<team>.cloudflareaccess.com/...`, the pattern the smoke
test expects, and `cloudflared tunnel ready` exits 0 once a real token is registered.
Then on the first deployment to enable it, with `TUNNEL_PROTOCOL=http2` on the network
measured above: the smoke test's sections 0 to 11 passed, the connector's log carried the
route the dashboard had configured, and the operator signed in through Cloudflare Access
and reached the service from a browser. **Still open**: the runbook's menu names follow
Cloudflare's documentation of November 2026 and were not compared with the dashboard click
for click.
