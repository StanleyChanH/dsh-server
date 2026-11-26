# Cloudflare Tunnel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give dsh-server an optional public entrance: a Cloudflare Tunnel in its own container and on its own Compose network, able to reach caddy only, with nothing changing for deployments that do not enable it.

**Architecture:** `compose.tunnel.yml`, loaded through `COMPOSE_FILE` in `.env`, adds `cloudflared`. It connects outbound to Cloudflare and forwards requests to a second, unpublished Caddy listener, `http://:8080`, which serves the same login-gated route chain as the private listener through one Caddyfile snippet. That listener is the only place where the visitor's address is taken from `Cf-Connecting-Ip` instead of the connection's peer. dsh receives the public hostname as a second trusted authority (`TUNNEL_HOSTNAME`). Cloudflare Access is configured in the Cloudflare dashboard; the stack does not check the Access JWT, but the smoke test fails when Access is not in front of the public hostname.

**Tech Stack:** Docker Compose v2 (`COMPOSE_FILE`, override files), Caddy 2.10 (Caddyfile snippets, `import` arguments), cloudflared 2026.11.2, Bash, Go 1.24 (one unit test in auth-gate).

**Spec:** This plan carries the design under "Architecture" and "Global Constraints". Task 6 records it in `docs/design.md` ("Increment: public access through a Cloudflare Tunnel") and `docs/design.zh-CN.md`.

## Global Constraints

- English is the language of comments, documentation and commit messages; every `README.md` / `docs/*.md` change has an equivalent change in its `*.zh-CN.md` counterpart.
- No deployment-specific value in the tree: examples use `192.0.2.x`, `203.0.113.x` and `example.org`.
- Images stay pinned: `cloudflare/cloudflared:2026.11.2` (new), `caddy:2.10-alpine`, `node:24-slim`.
- With the tunnel off, `docker compose config` renders exactly what it renders today: no `cloudflared` service, no `TUNNEL_*` variable, no second network.
- With the tunnel on or off, the private listener behaves as it does today: `X-Forwarded-For` is the peer's address and anything the client sent in `X-Forwarded-For` or `Cf-Connecting-Ip` is ignored.
- Never add `command:` to the `dsh` service. Never build with `--pull`.
- A test stack on a host that also runs a real one publishes no ports, uses its own Compose project name and its own image tags (never `dsh-server-dsh:latest` or `dsh-server-auth-gate:latest`).
- The stack does not enforce the Cloudflare Access JWT.
- Commits follow Conventional Commits (scopes in use: `deploy`, `auth-gate`, `dsh`, `smoke`), are authored with the identity used on `master` (passed through `GIT_AUTHOR_*` / `GIT_COMMITTER_*`; git config is not changed) and end with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.

## What recon established (2026-11-26)

All of this was measured on the pinned images; the plan relies on each line.

- `header_up` takes no request matcher. `header_up @name X-Foo value` adapts without an error and becomes a regular-expression replacement on a header named `@name`.
- `header_up X-Forwarded-For {remote_host}` makes the adapter log `Unnecessary header_up X-Forwarded-For`; it is the reverse proxy's default. `header_up X-Forwarded-For {http.request.header.Cf-Connecting-Ip}` does not.
- `import <snippet> <arg>` replaces `{args[0]}` in the snippet, and the argument may be a snippet name used in a nested `import`.
- A site block whose address is an empty environment placeholder does not adapt: `server block without any key is global configuration, and if used, it must be first`. The tunnel listener therefore exists unconditionally.
- With the layout of Task 1 and stand-in upstreams: forged `X-Forwarded-For` and `Cf-Connecting-Ip` sent to the private listener arrive upstream as the peer's address on all three hops; through `http://:8080`, `Cf-Connecting-Ip: 203.0.113.9` arrives as `X-Forwarded-For` on all three hops while a forged `X-Forwarded-For: 1.1.1.1` is dropped; a request without `Cf-Connecting-Ip` arrives with an empty `X-Forwarded-For`. The 80 -> 443 redirect is still added at run time.
- Cloudflare's documentation: `Cf-Connecting-Ip` is added by the edge and carries the visitor's address; `X-Forwarded-For` is appended to when the request already has one, so its first element is whatever the visitor sent. Zone settings can remove or rewrite `Cf-Connecting-Ip` ("Remove visitor IP headers", Pseudo IPv4 "Overwrite Headers").
- dsh's fence (`@deepseek-ai/dsh-client-connection`) accepts a request when its `Host` is a trusted authority, `Sec-Fetch-Site` is not `cross-site` and `Origin`, if present, has the same authority as `Host`. `--trusted-host` is repeatable. dsh reads no `X-Forwarded-*` or `Cf-Connecting-Ip` header.
- auth-gate takes the client address from the first element of `X-Forwarded-For`, or from `RemoteAddr` when that header is empty; its cookies are always `Secure`.
- `cloudflared 2026.11.2`: entrypoint `cloudflared`, default command `version`, no shell, user `65532:65532`. Flags read from the environment: `TUNNEL_TOKEN`, `TUNNEL_METRICS`. `cloudflared tunnel ready` "calls /ready and returns proper exit code". There is no `--protocol` flag; `--no-autoupdate` is already the default.
- Compose: `COMPOSE_FILE` in `.env` works when the command runs in the project directory. Its relative paths resolve against the current directory, and it is not read when `--env-file` is given. `networks` renders as an object (`{"default":null,"tunnel":null}`), base services included. `ports: !override [...]` replaces the base list. A `${VAR:?}` in an override file that is not loaded breaks nothing.
- The dsh Dockerfile copies `entrypoint.sh` in its last layer, after the apt and npm layers.

## Review Focus

1. A private-network client that sends its own `X-Forwarded-For` or `Cf-Connecting-Ip` moves its login rate-limit bucket. Expected: ignored, the peer's address is counted. Pinned by Task 1's routing script (L1-L3) and Task 5's smoke section 11.
2. A tunnel container that cannot connect (wrong token, no route to Cloudflare) takes the private entrance down with it. Expected: the stack keeps serving; only the public hostname is down. Pinned by Task 2's cloudflared script and Task 4's sandbox run with the real image.
3. `docker compose` run from another directory, or with `--env-file`, in a deployment that has the tunnel on. Expected: the command still addresses the base stack and nothing is removed or broken; the runbook says to run it in the deployment directory, and `--remove-orphans` is only named for turning the tunnel off. Pinned by Task 2's base assertion and Task 4's sandbox.
4. The public hostname answers, but Cloudflare Access is not in front of it. Expected: the smoke test fails and names the fix. Pinned by Task 5's sandbox runs (c) and (d).
5. `TUNNEL_HOSTNAME` was changed (or the tunnel enabled) and dsh was not recreated. Expected: the smoke test says so, instead of leaving members with pages that load and API calls that fail. Pinned by Task 5's smoke section 11 and sandbox run (f).

A tunnel request that arrives without `Cf-Connecting-Ip` is the sixth: expected is that it still reaches the login page and is counted under one address. Task 1's routing script (T4) and the unit test added in Task 3 pin it.

---

### Task 1: Caddyfile: one route chain, two listeners

**Files:**
- Modify: `caddy/Caddyfile`
- Modify: `scripts/check.sh` (function `config`)
- Workspace: `.superpowers/sdd/cloudflare-tunnel/caddy-routing.sh`

**Interfaces:**
- Produces: the caddy listener `http://:8080` (not published); Caddyfile snippets `routes` (argument: name of a snippet to import inside every proxy), `xff_peer` (empty), `xff_cloudflare` (sets `X-Forwarded-For`).
- Consumes: nothing new. `compose.tunnel.yml` (Task 2) relies on the listener existing unconditionally.

- [ ] **Step 1: Add two assertions to `config()` in `scripts/check.sh`**

After the line `echo '   the Caddyfile does not validate without it'`, inside `config()`:

```bash
    # Warnings from the adapter are defects here: it warns about a header_up
    # that repeats the reverse proxy's default, and the Caddyfile is written
    # so that it has none.
    n=$(caddy_validate -e DSH_TRUSTED_HOST="$host" 2>&1 | grep -c 'Unnecessary header_up' || true)
    [ "$n" -eq 0 ] || { echo 'the Caddyfile has a header_up that repeats the reverse proxy default' >&2; return 1; }
    echo '   the Caddyfile adapts without warnings about redundant header_up'

    # Two listeners, one route chain. On the tunnel listener (:8080) every
    # proxy hands the backends the visitor's address from Cf-Connecting-Ip; on
    # the private listener (:443) none touches X-Forwarded-For, which leaves it
    # to Caddy's default: the peer's address, anything the client sent is
    # discarded. The behaviour itself is exercised by the smoke test.
    adapted=$(docker run --rm -v "$PWD/caddy/Caddyfile:/etc/caddy/Caddyfile:ro" -e DSH_TRUSTED_HOST="$host" \
        "$caddy_image" caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile 2>/dev/null)
    visitor='{http.request.header.Cf-Connecting-Ip}'
    echo "$adapted" | jq -e --argjson v "[\"$visitor\", \"$visitor\", \"$visitor\"]" '
        [.apps.http.servers[] | {listen,
            xff: [.. | objects | select(.handler? == "reverse_proxy") | .headers.request.set["X-Forwarded-For"]? // [] | .[]]}] as $servers
        | ($servers[] | select(.listen == [":8080"]) | .xff == $v)
          and ($servers[] | select(.listen == [":443"]) | .xff == [])' >/dev/null \
        || { echo 'the Caddyfile does not take the client address from Cf-Connecting-Ip on the tunnel listener only' >&2; return 1; }
    echo '   the tunnel listener sets the client address from Cf-Connecting-Ip on every hop; the private listener leaves it to Caddy'
```

- [ ] **Step 2: Run `scripts/check.sh config` and see it fail**

Expected: `the Caddyfile does not take the client address from Cf-Connecting-Ip on the tunnel listener only` (there is no `:8080` listener yet). The "without warnings" assertion passes already: today's file has no `header_up`.

- [ ] **Step 3: Rewrite `caddy/Caddyfile` to this structure**

```caddyfile
{
	admin off
	default_sni {$DSH_TRUSTED_HOST}
}

(routes) {
	handle /auth/* {
		reverse_proxy auth-gate:8080 {
			import {args[0]}
		}
	}
	handle {
		forward_auth auth-gate:8080 {
			uri /auth/check
			import {args[0]}
		}
		reverse_proxy dsh:3080 {
			import {args[0]}
			@dsh_unauth status 401
			handle_response @dsh_unauth {
				redir * /auth/dsh-entry 302
			}
		}
	}
}

(xff_peer) {
}

(xff_cloudflare) {
	header_up X-Forwarded-For {http.request.header.Cf-Connecting-Ip}
}

https://{$DSH_TRUSTED_HOST} {
	tls internal
	import routes xff_peer
}

http://:8080 {
	import routes xff_cloudflare
}
```

Comments: every comment in today's file stays, with the directive it describes (the "no `encode`", "/auth/*", "forward_auth", "automatic sign-in" and "`redir *`" notes move into `(routes)`; the "admin off", "default_sni", site address and TLS notes stay at the top). Four comments are new, and each states one thing:

- `(routes)`: the argument names a snippet that every proxy in the chain imports, so a listener can add to what the proxies send upstream. auth-gate takes the client address, for the login rate limit, from the first element of `X-Forwarded-For`, so the address has to be right on all three hops, not only on the way to dsh.
- `(xff_peer)`: why it is empty. For a connection that does not come from a trusted proxy, the reverse proxy discards any `X-Forwarded-For` the client sent and writes the peer's address there; writing that down as `header_up X-Forwarded-For {remote_host}` would repeat the default (Caddy warns about it).
- `(xff_cloudflare)`: Cloudflare's edge adds `Cf-Connecting-Ip` to every request it forwards. `X-Forwarded-For` is not used: a request that already carries one has the visitor's address appended, so its first element is whatever the visitor chose to send.
- `http://:8080`: the listener of the Cloudflare Tunnel (`compose.tunnel.yml` puts `cloudflared` on its own Compose network, and this is the only thing it can reach); the public hostname in the Cloudflare dashboard points to `http://caddy:8080`; not published, so only the host and containers on the Compose networks reach it; the login gate applies exactly as on the private listener; the `Host` header is the public hostname and is passed on to dsh, which has to trust it (`TUNNEL_HOSTNAME`); a request without `Cf-Connecting-Ip` reaches the backends with an empty `X-Forwarded-For`, and auth-gate counts all such requests as one client.

The file's opening comment gains one sentence: a third entry point, the listener for a Cloudflare Tunnel, is at the end of the file.

- [ ] **Step 4: Run `scripts/check.sh config`**

Expected: all lines through `the tunnel listener sets the client address ...` and `All checks passed: config`.

- [ ] **Step 5: Write `.superpowers/sdd/cloudflare-tunnel/caddy-routing.sh` and run it**

Three throwaway `caddy:2.10-alpine` containers, no published ports, on two throwaway networks (a stand-in for the Compose default network and one for the tunnel network): `auth-gate` (alias) answering with `via=... xff=<X-Forwarded-For> host=<Host>`, `200` for `/auth/*`, `401` for `/auth/check` unless the request carries `X-Probe: pass`; `dsh` (alias) answering the same line; and the front with `caddy/Caddyfile` and `DSH_TRUSTED_HOST=dsh.test`, attached to both networks. The script prints the upstream line and exits non-zero unless every row holds:

| Probe | Request | Expected upstream `xff` |
| --- | --- | --- |
| L1 | private listener, `/auth/login`, `X-Forwarded-For: 6.6.6.6`, `Cf-Connecting-Ip: 6.6.6.6` | the gateway address of the default-network stand-in |
| L2 | private listener, `/x` (stopped by `forward_auth`), same forged headers | same gateway address |
| L3 | private listener, `/x`, `X-Probe: pass`, same forged headers | same gateway address |
| L4 | port 80, `Host: dsh.test` | `308` to `https://dsh.test/x` |
| T1 | `http://<front tunnel address>:8080/auth/login`, `Host: dsh.example.org`, `Cf-Connecting-Ip: 203.0.113.9`, `X-Forwarded-For: 1.1.1.1` | `203.0.113.9` |
| T2 | same listener, `/x` | `203.0.113.9` |
| T3 | same listener, `/x`, `X-Probe: pass` | `203.0.113.9`, `host=dsh.example.org` |
| T4 | same listener, `/x`, `X-Probe: pass`, no `Cf-Connecting-Ip`, `X-Forwarded-For: 1.1.1.1` | empty (not a placeholder, not the peer) |

The stand-ins' Caddyfiles are validated before anything is started, and the script removes its containers and networks. Run it; expected: every row `PASS`, exit status 0.

- [ ] **Step 6: Commit**

`feat(deploy): serve the login-gated routes on a second listener for a tunnel`

---

### Task 2: `compose.tunnel.yml`

**Files:**
- Create: `compose.tunnel.yml`
- Modify: `scripts/check.sh` (function `config`, header comment)
- Modify: `.env.example`
- Modify: `compose.yml` (header comment only)
- Workspace: `.superpowers/sdd/cloudflare-tunnel/cloudflared.sh`

**Interfaces:**
- Consumes: the listener `http://:8080` (Task 1).
- Produces: service `cloudflared` (image `cloudflare/cloudflared:2026.11.2`, `command ["tunnel","run"]`, environment `TUNNEL_TOKEN` (required) and `TUNNEL_METRICS=127.0.0.1:2000`, network `tunnel` only, `depends_on: [caddy]`, healthcheck `["CMD","cloudflared","tunnel","ready"]`); caddy on networks `default` and `tunnel`; `dsh.environment.TUNNEL_HOSTNAME` (required); top-level network `tunnel`.
- Task 3 consumes `TUNNEL_HOSTNAME` in the dsh container; Task 5 consumes the service name `cloudflared`.

- [ ] **Step 1: Add the assertions to `config()` in `scripts/check.sh`**

After the first assertion (`compose derives the address from DSH_TRUSTED_HOST`), using the base `$json` captured there:

```bash
    if ! echo "$json" | jq -e '(.services | has("cloudflared") | not)
        and (.services.dsh.environment | has("TUNNEL_HOSTNAME") | not)
        and ((.networks | keys) == ["default"])' >/dev/null; then
        echo 'the base compose files mention the tunnel; without compose.tunnel.yml there must be nothing of it' >&2
        return 1
    fi
    echo '   without compose.tunnel.yml there is no tunnel service, variable or network'
```

At the end of `config()`:

```bash
    # The tunnel override, loaded by its name: a deployment loads it through
    # COMPOSE_FILE in .env, which --env-file would bypass.
    tunnel_json() { docker compose --env-file /dev/null -f compose.yml -f compose.tunnel.yml config --format json; }
    tunnel_ok()   { docker compose --env-file /dev/null -f compose.yml -f compose.tunnel.yml config -q; }
    export TUNNEL_TOKEN=fake-token TUNNEL_HOSTNAME=dsh.example.org

    json=$(tunnel_json)
    if ! echo "$json" | jq -e '
        (.services.caddy.networks | keys | sort) == ["default", "tunnel"]
        and (.services.cloudflared.networks | keys | sort) == ["tunnel"]
        and (.services.auth-gate.networks | keys | sort) == ["default"]
        and (.services.dsh.networks | keys | sort) == ["default"]
        and ((.services.cloudflared.depends_on | keys) == ["caddy"])
        and (.services.caddy.depends_on | has("cloudflared") | not)
        and (.services.cloudflared.ports == null)
        and (([.services.caddy.ports[].published] | sort) == ["443", "80"])
        and (.services.dsh.environment.TUNNEL_HOSTNAME == "dsh.example.org")
        and (.services.cloudflared.environment.TUNNEL_TOKEN == "fake-token")' >/dev/null; then
        echo 'compose.tunnel.yml does not keep cloudflared to its own network, or does not pass the tunnel variables as it should' >&2
        return 1
    fi
    echo '   compose.tunnel.yml: cloudflared reaches caddy only; caddy publishes the same two ports as before'

    if (unset TUNNEL_TOKEN; tunnel_ok) >/dev/null 2>&1 || (unset TUNNEL_HOSTNAME; tunnel_ok) >/dev/null 2>&1; then
        echo 'the tunnel override renders without TUNNEL_TOKEN or TUNNEL_HOSTNAME; both are required' >&2
        return 1
    fi
    echo '   the tunnel override refuses to render without TUNNEL_TOKEN or TUNNEL_HOSTNAME'
```

In the header comment of `scripts/check.sh`, the `config` line becomes: `compose renders, the required variables are really required, the Caddyfile validates and takes the client address from Cf-Connecting-Ip on the tunnel listener only, and compose.tunnel.yml keeps cloudflared to its own network`.

- [ ] **Step 2: Run `scripts/check.sh config`**

Expected: the new base assertion passes, then the run stops with `no such file or directory` naming `compose.tunnel.yml`.

- [ ] **Step 3: Create `compose.tunnel.yml`**

```yaml
services:
  caddy:
    networks:
      default:
      tunnel:

  cloudflared:
    image: cloudflare/cloudflared:2026.11.2
    command: ["tunnel", "run"]
    environment:
      TUNNEL_TOKEN: "${TUNNEL_TOKEN:?TUNNEL_TOKEN is not set in .env (Cloudflare Zero Trust > Networks > Tunnels: the token of the tunnel; see docs/operations.md, Cloudflare Tunnel)}"
      TUNNEL_METRICS: "127.0.0.1:2000"
    networks:
      - tunnel
    depends_on:
      - caddy
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "cloudflared", "tunnel", "ready"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 30s
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"

  dsh:
    environment:
      TUNNEL_HOSTNAME: "${TUNNEL_HOSTNAME:?TUNNEL_HOSTNAME is not set in .env (the public hostname of the Cloudflare Tunnel, for example dsh.example.org; see docs/operations.md, Cloudflare Tunnel)}"

networks:
  tunnel:
```

Comments, in the style of `compose.yml` (the why, for whoever edits the file next):

- File header: what the tunnel is for (members on neither the LAN nor the VPN), that it needs no inbound port, public IP or port forwarding because cloudflared connects to Cloudflare and keeps that connection open; that Cloudflare Access sits in front of the public hostname and is configured in the dashboard, with the login gate applying behind it; how to enable it (the three `.env` lines, then `docker compose up -d` in the deployment directory); that without the `COMPOSE_FILE` line the stack is exactly what `compose.yml` describes; and that `COMPOSE_FILE` paths resolve against the directory a command is run from and are not read with `--env-file`, so every `docker compose` command belongs in the deployment directory.
- `caddy.networks`: joined so cloudflared can reach `http://:8080`; changing a container's networks means recreating it.
- `cloudflared.image`: pinned; a bump is `docker compose up -d cloudflared` and a smoke test.
- `command`: the image's entrypoint is the binary and its default argument prints the version; with `TUNNEL_TOKEN` set, `tunnel run` needs no credentials file and no volume.
- `TUNNEL_TOKEN`: a secret; whoever holds it can connect a server to the tunnel.
- `TUNNEL_METRICS`: cloudflared serves metrics and readiness there; the healthcheck asks for it and `cloudflared tunnel ready` reads the same variable, so the address is written once; `127.0.0.1` keeps the endpoint inside the container.
- `networks: [tunnel]`: cloudflared reaches caddy and nothing else, so a wrong service address in the Cloudflare dashboard (`dsh:3080`, say) is not routable from here and no dashboard mistake can skip the login gate.
- `healthcheck`: `ready` exits non-zero until a connection to Cloudflare's edge is up; a wrong token or an unreachable network keeps the process running but not ready; the image has no shell, hence the exec form.
- `TUNNEL_HOSTNAME`: no scheme and no path; dsh accepts it as a second authority next to `DSH_TRUSTED_HOST`; without it the pages would load but every API call from the public hostname would be refused.

- [ ] **Step 4: Add the block to `.env.example`, and one paragraph to the header of `compose.yml`**

`.env.example`, at the end, under a `# ---- Cloudflare Tunnel (optional) ---` rule, all lines commented out, in the uncommented order that makes enabling one step:

```
# COMPOSE_FILE=compose.yml:compose.tunnel.yml
# TUNNEL_TOKEN=
# TUNNEL_HOSTNAME=
```

The comments above them say: what it is for; that the dashboard steps are in `docs/operations.md`, "Cloudflare Tunnel"; that uncommenting the three lines and running `docker compose up -d` from this directory enables it; that the token is a secret (`TUNNEL_TOKEN`); and that the hostname is the public one, for example `dsh.example.org`, without scheme or path (`TUNNEL_HOSTNAME`).

`compose.yml`, in the header comment after the `DSH_TRUSTED_HOST` paragraph: a Cloudflare Tunnel is an optional second, public entrance, `compose.tunnel.yml`, enabled through `COMPOSE_FILE` in `.env`.

- [ ] **Step 5: Run `scripts/check.sh config`**

Expected: `All checks passed: config`.

- [ ] **Step 6: Write and run `.superpowers/sdd/cloudflare-tunnel/cloudflared.sh`**

It checks the healthcheck against the real image on a throwaway network, with a well-formed but fake token (base64 of `{"a":<32 hex>,"s":<base64 of 32 bytes>,"t":<uuid>}`):

```bash
docker run -d --name cf-fake --network <net> -e TUNNEL_TOKEN=<fake> -e TUNNEL_METRICS=127.0.0.1:2000 \
    cloudflare/cloudflared:2026.11.2 tunnel run
sleep 20
docker exec cf-fake cloudflared tunnel ready; echo "ready exit=$?"
```

Expected: `ready exit=` is not `0` (the process is running and not connected), or the container is restarting and `docker exec` fails; either way there is no state in which it reports healthy. The script prints which of the two happened (Task 6 documents that one), the last error line from `docker logs cf-fake`, then removes the container and the network.

- [ ] **Step 7: Commit**

`feat(deploy): add compose.tunnel.yml with cloudflared on its own network`

---

### Task 3: dsh trusts the public hostname

**Files:**
- Modify: `dsh/entrypoint.sh` (the final `exec`)
- Modify: `scripts/check.sh` (function `shell`, header comment)
- Modify: `auth-gate/handlers_test.go` (`TestClientIP`)

**Interfaces:**
- Consumes: `TUNNEL_HOSTNAME` in the dsh container (Task 2).
- Produces: `dsh web ... --trusted-host "$DSH_TRUSTED_HOST" [--trusted-host "$TUNNEL_HOSTNAME"]`; the second authority is appended only when `TUNNEL_HOSTNAME` is set and non-empty. Task 5 checks the container's value and the resulting trust.

- [ ] **Step 1: Add the entrypoint test to `shell()` in `scripts/check.sh`**

After the ShellCheck line:

```bash
    # What the dsh image's entrypoint hands to dsh, one argument per line,
    # from a stand-in dsh. The address after --host is the container's own and
    # differs per run, so that value is dropped.
    entrypoint_args() {
        local base
        base=$(sed -n 's/^FROM \([^ ]*\)$/\1/p' dsh/Dockerfile | head -1)
        docker run --rm -v "$PWD/dsh/entrypoint.sh:/entrypoint.sh:ro" -e DSH_TRUSTED_HOST=192.0.2.10 "$@" \
            "$base" sh -c '
                mkdir /fake
                printf "#!/bin/sh\nprintf \"%%s\\n\" \"\$@\"\n" > /fake/dsh
                chmod +x /fake/dsh
                PATH="/fake:$PATH" exec /entrypoint.sh' \
            | awk 'drop { drop = 0; next } $0 == "--host" { drop = 1 } { print }'
    }
    want="$(printf '%s\n' web --no-open --host --port 3080 --public-url https://192.0.2.10/ --trusted-host 192.0.2.10)"
    got=$(entrypoint_args) || { echo 'cannot run the dsh entrypoint in its base image' >&2; return 1; }
    if [ "$got" != "$want" ]; then
        echo 'the dsh entrypoint hands dsh other arguments than expected:' >&2
        diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") >&2
        return 1
    fi
    echo '   the dsh entrypoint hands dsh the expected arguments'

    want="$want
--trusted-host dsh.example.org"
    got=$(entrypoint_args -e TUNNEL_HOSTNAME=dsh.example.org) || { echo 'cannot run the dsh entrypoint in its base image' >&2; return 1; }
    if [ "$got" != "$want" ]; then
        echo 'with TUNNEL_HOSTNAME set, the dsh entrypoint does not add it as a second trusted authority:' >&2
        diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") >&2
        return 1
    fi
    echo '   ...and the tunnel hostname as a second trusted authority when TUNNEL_HOSTNAME is set'
```

In the header comment of `scripts/check.sh`, the `shell` line becomes: `ShellCheck on scripts/*.sh and dsh/entrypoint.sh, and the arguments the dsh entrypoint hands to dsh`.

- [ ] **Step 2: Run `scripts/check.sh shell`**

Expected: ShellCheck passes, `the dsh entrypoint hands dsh the expected arguments` passes, then FAIL: `with TUNNEL_HOSTNAME set, the dsh entrypoint does not add it as a second trusted authority` (diff shows the two missing lines).

- [ ] **Step 3: Change the end of `dsh/entrypoint.sh`**

The final `exec dsh web ...` becomes: build the argument list with `set --`, then append the second authority when `TUNNEL_HOSTNAME` is set and non-empty, then `exec dsh "$@"`. The options and their order stay as they are. One comment above the block: a second authority, the public hostname of a Cloudflare Tunnel (`compose.tunnel.yml`); dsh's fence refuses API requests whose `Host` it was not told about, and through the tunnel the `Host` is that hostname.

- [ ] **Step 4: Run `scripts/check.sh shell`**

Expected: `All checks passed: shell`.

- [ ] **Step 5: Add the empty-header case to `TestClientIP` and run the Go checks**

In `auth-gate/handlers_test.go`, after the existing "no XFF" case of `TestClientIP`, in its style:

```go
	// Present but empty: what caddy forwards for a tunnel request that carries
	// no Cf-Connecting-Ip (caddy/Caddyfile, listener :8080).
	req.Header.Set("X-Forwarded-For", "")
	if got := clientIP(req); got != "192.0.2.1" {
		t.Errorf("clientIP(empty XFF) = %q, want the RemoteAddr host %q", got, "192.0.2.1")
	}
```

Run `scripts/check.sh go`. Expected: pass (the code already behaves this way; the case pins what the tunnel listener relies on).

- [ ] **Step 6: Commit**

Two commits: `feat(dsh): trust the tunnel hostname as a second authority` (`dsh/entrypoint.sh`, `scripts/check.sh`) and `test(auth-gate): cover an empty X-Forwarded-For in clientIP`.

---

### Task 4: A sandbox stack

No commit: everything here lives in `.superpowers/sdd/cloudflare-tunnel/sandbox/` and in ignored directories of the feature worktree. It is the test bed for Task 5 and the proof, on a real dsh, for Tasks 1 and 3.

**Files (workspace):**
- `sandbox/compose.sandbox.yml`, `sandbox/compose.standin.yml`, `sandbox/standin-ready.caddy`, `sandbox/access.caddy`
- `.env` in the worktree (ignored)

- [ ] **Step 1: Record the real stack**

If a stack runs on this host, write `docker ps -q` with each container's `Id` and `State.StartedAt` to `sandbox/live-before.txt`. The same command after Step 7 must give the same file.

- [ ] **Step 2: Build the sandbox images under sandbox tags**

From the worktree root: `docker build -t dsh-server-dsh:tunnel-sandbox --build-arg DSH_VERSION=<DSH_VERSION from .env.example> dsh` and `docker build -t dsh-server-auth-gate:tunnel-sandbox --build-arg GOPROXY=<as compose.yml defaults> auth-gate`. No `--pull`. The dsh build should stop at the `entrypoint.sh` layer; if the build cache is cold it takes as long as a first build.

- [ ] **Step 3: Write `sandbox/compose.sandbox.yml`**

```yaml
services:
  dsh:
    image: dsh-server-dsh:tunnel-sandbox
  auth-gate:
    image: dsh-server-auth-gate:tunnel-sandbox
  caddy:
    ports: !override []
    networks:
      default:
        ipv4_address: 172.31.253.10
networks:
  default:
    ipam:
      config:
        - subnet: 172.31.253.0/24
```

A subnet that is free on the host (`docker network inspect` over the existing networks). caddy's fixed address is the stack's `DSH_TRUSTED_HOST`, so the worktree's `.env` can name it once.

- [ ] **Step 4: Write `.env` in the worktree (tunnel off) and start the stack**

```
COMPOSE_PROJECT_NAME=dsh-tunnel-sandbox
COMPOSE_FILE=compose.yml:<absolute path of sandbox/compose.sandbox.yml>
DSH_TRUSTED_HOST=172.31.253.10
DSH_VERSION=<same value as the build argument>
AUTH_GATE_SECRET=<64 hex characters from openssl rand -hex 32>
LAN_PROXY=
```

The three scripts of the repository (`add-user.sh`, `sync-token.sh`, `smoke.sh`) run `docker compose` from the directory that holds the script, so they all act on this project. Create a first user (`printf '%s\n' <password> | scripts/add-user.sh first`; auth-gate needs `users.json` before it starts), then `docker compose up -d`.

Expected: `docker compose ps` shows caddy and dsh healthy, and `docker compose exec -T caddy hostname -i` prints `172.31.253.10`. `docker compose port caddy 443` prints nothing.

- [ ] **Step 5: Run today's `scripts/smoke.sh` against the sandbox**

`scripts/sync-token.sh`, then `env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY scripts/smoke.sh`.

Expected: sections 0 to 10 pass and the last line is `All smoke checks passed: https://172.31.253.10`. This is the proof, on a real dsh, that Tasks 1 and 3 changed nothing for a stack without the tunnel. (`scripts/smoke.sh` needs the host's cron file `/etc/cron.d/dsh-server-token` to exist, as section 0 checks; the token file itself is refreshed by hand.)

- [ ] **Step 6: Turn the tunnel on with the real image and a fake token**

Add to `.env`: `COMPOSE_FILE=compose.yml:<sandbox override>:compose.tunnel.yml`, `TUNNEL_TOKEN=<fake token of Task 2, Step 6>`, `TUNNEL_HOSTNAME=dsh.example.org`. `docker compose up -d`.

Expected: caddy and dsh are recreated, `cloudflared` starts and never becomes healthy (the token is fake), and the private entrance answers all the same: `curl -sk -o /dev/null -w '%{http_code}' https://172.31.253.10/some/path` prints `302` for as long as cloudflared is unhealthy or restarting. This is Review Focus 2.

Two more probes of the same step, Review Focus 3. From the worktree root, `docker compose --env-file /dev/null config --services` lists `auth-gate caddy dsh` and no `cloudflared`: a command with `--env-file` addresses the base stack. From another directory, `docker compose --project-directory <worktree root> config --services` fails with `no such file or directory` naming a file under that other directory: `COMPOSE_FILE` is resolved from where the command runs. Record both outputs.

- [ ] **Step 7: Run today's `scripts/smoke.sh` again**

Expected: it does not report success. Either section 0 fails at once with `FAIL  running containers = 3: want 3, got 4`, or it passes section 0 while cloudflared is restarting and then ends with `All smoke checks passed` although the tunnel is down. The second outcome is the reason Task 5 exists. Record which one happened.

- [ ] **Step 8: Compare the real stack**

The file of Step 1, written again to `sandbox/live-after.txt`, is identical.

---

### Task 5: `scripts/smoke.sh`: the tunnel section

**Files:**
- Modify: `scripts/smoke.sh`

**Interfaces:**
- Consumes: service `cloudflared` (Task 2), `TUNNEL_HOSTNAME` in the dsh container (Tasks 2 and 3), the listener `http://caddy:8080` (Task 1), the auth-gate log line `auth: login user=<%q> ip=<address> result=<result>`.
- Produces: section 11 runs when `docker compose config --services` lists `cloudflared`; the final line names the public hostname as well.

- [ ] **Step 1: Add the tunnel switch and section 11**

After the `BASE_URL` block:

```bash
# compose.tunnel.yml in COMPOSE_FILE (.env) adds a fourth container and a
# public hostname. Sections 0 and 11 take that into account.
tunnel=0
if [ "$(docker compose config --services 2>/dev/null | grep -cx cloudflared || true)" -ge 1 ]; then
    tunnel=1
fi
tunnel_summary=""
```

In section 0: the expected count is `4` instead of `3` when `tunnel` is 1, and `cloudflared` joins the loop that requires `healthy` (the check's name stays `$svc healthy`). The header comment gains one note: with the Cloudflare Tunnel enabled, section 11 also runs.

Section 11, after section 10 and before the closing `echo`:

```bash
if [ "$tunnel" = 1 ]; then
    echo "== 11. Cloudflare Tunnel =="
    tunnel_host=$(docker compose config --format json 2>/dev/null | jq -r '.services.dsh.environment.TUNNEL_HOSTNAME // ""')
    [ -n "$tunnel_host" ] || { echo "FAIL  TUNNEL_HOSTNAME is not set, although compose.tunnel.yml is active" >&2; exit 1; }

    # (a) dsh is told the public hostname when its container is created.
    got=$(docker compose exec -T dsh printenv TUNNEL_HOSTNAME)
    check "dsh was created with TUNNEL_HOSTNAME" "$tunnel_host" "$got"
    ...
    tunnel_summary=" and https://$tunnel_host/ (Cloudflare Tunnel)"
fi
echo "All smoke checks passed: $BASE_URL$tunnel_summary"
```

The rest of the section, in order. Each check prints `ok` or `FAIL` with the fix in the message, as the other sections do.

- (b) *dsh accepts the public hostname.* The WebSocket probe of section 6 (`$ws_h` and the three `Sec-WebSocket-*` headers, `--http1.1`), run with `docker compose exec -T dsh curl -s --noproxy '*'` against `http://caddy:8080/api/remote.mux`, with `Host: $tunnel_host`, `Origin: https://$tunnel_host` and `Sec-Fetch-Site: same-origin`. The cookies come from `$CJ_ENTRY` as a literal `Cookie:` header (`name=value; ` for every line of the jar with at least seven fields): the URL host is not `$BASE_URL` and the listener is plain http, so the jar would send nothing. Expected `101`. Failure message: dsh was created before `TUNNEL_HOSTNAME` had this value; `docker compose up -d` recreates it.
- (c) *Which address the rate limit counts.* A helper `counted_address <curl arguments...>` sends `POST` with wrong credentials for a unique user name `xff-$RANDOM-$RANDOM` and prints the `ip=` field of the last auth-gate log line for that user (read with `docker compose logs --no-color auth-gate | grep -F "user=\"$name\"" | tail -1`, retried for up to five seconds). The two probes, both sent from the dsh container with `--noproxy '*'`:
  - private listener: `https://<DSH_TRUSTED_HOST>/auth/login` with `--resolve "<DSH_TRUSTED_HOST>:443:<caddy's address>"`, headers `X-Forwarded-For: 192.0.2.1` and `Cf-Connecting-Ip: 192.0.2.1`. Expected: the dsh container's address. Both addresses are the `IPAddress` of the container in the project's `default` network, read on the host with `docker inspect` (with the tunnel on, caddy has a second address on the tunnel network, which the dsh container cannot reach).
  - tunnel listener: `http://caddy:8080/auth/login`, headers `Cf-Connecting-Ip: 203.0.113.9` and `X-Forwarded-For: 192.0.2.1`. Expected `203.0.113.9`.
- (d) *The public hostname, from this host.* For `/` and `/auth/login`: `curl -s --noproxy '*' --max-time 20 -H 'Accept: text/html' -o /dev/null -D - "https://$tunnel_host<path>"`. A curl failure is a FAIL that names `docker compose ps cloudflared`, `docker compose logs cloudflared`, and the dashboard service address `http://caddy:8080`. Otherwise status and `Location` decide:

  | Answer | Verdict |
  | --- | --- |
  | `302` to `https://*cloudflareaccess.com/...` | ok: Cloudflare Access is in front |
  | `302` to `/auth/...` | FAIL: on the Internet with nothing but the login gate in front; create the Access application (`docs/operations.md`, "Cloudflare Tunnel"), then run the smoke test again |
  | anything else | FAIL: prints the status, then the same hints as for a curl failure |

  The comment above (d) states why Access is required: dsh runs whatever its users ask it to, so the login page must not be reachable by anyone; Access keeps strangers away and the gate then asks for the member's password.

- [ ] **Step 2: Tunnel off: `scripts/sync-token.sh`, then smoke**

Back to the `.env` of Task 4, Step 4, applied with `docker compose up -d --remove-orphans`. Expected, after it: `docker compose ps --all` lists no cloudflared container; caddy's container is attached to the default network only; `docker compose port caddy 443` still prints nothing. Note whether `docker network ls` still lists the project's `tunnel` network: if it does, "Turn it off" in the runbook (Task 6) says that it can be removed by hand. Then `scripts/sync-token.sh` and smoke. Expected: sections 0 to 10 pass, section 11 does not run, last line unchanged.

- [ ] **Step 3: Tunnel on, with stand-ins, run (a): everything passes**

Write `sandbox/compose.standin.yml`: `cloudflared` becomes `caddy:2.10-alpine` running `sandbox/standin-ready.caddy` (a `:2000` site answering `200` to `/ready`), with healthcheck `["CMD","wget","-q","-O","/dev/null","http://127.0.0.1:2000/ready"]`. Start the Access stand-in by hand on a second throwaway network with a fixed address `172.31.254.20`: `caddy:2.10-alpine` running `sandbox/access.caddy`:

```caddyfile
https://172.31.254.20 {
	tls internal
	redir * https://team.cloudflareaccess.com/cdn-cgi/access/login/stand-in 302
}
```

with its data directory bind-mounted, so that `caddy/pki/authorities/local/root.crt` can be read from the host. `.env`: `COMPOSE_FILE=compose.yml:<sandbox override>:compose.tunnel.yml:<stand-in override>`, `TUNNEL_TOKEN=<any value>`, `TUNNEL_HOSTNAME=172.31.254.20`. `docker compose up -d --remove-orphans`.

Run: `CURL_CA_BUNDLE=<stand-in root.crt> scripts/sync-token.sh && CURL_CA_BUNDLE=<stand-in root.crt> env -u http_proxy ... scripts/smoke.sh`.

Expected: sections 0 to 11 pass; `Cloudflare Access is in front of https://172.31.254.20/` and `.../auth/login`; last line `All smoke checks passed: https://172.31.253.10 and https://172.31.254.20/ (Cloudflare Tunnel)`.

- [ ] **Step 4: Tunnel on, real image, fake token: run (b)**

`.env` as in Task 4, Step 6. Expected: no success. Section 0 fails either at `FAIL  running containers = 4: want 4, got 3` (cloudflared restarting at that moment) or at `FAIL  cloudflared healthy: want "healthy", got "starting"` or `got "unhealthy"`.

- [ ] **Step 5: Stand-ins, `TUNNEL_HOSTNAME=172.31.253.10` (the stack's own caddy): run (c)**

With the stack's own caddy as the public hostname and `CURL_CA_BUNDLE=<the stack caddy's root.crt>`, the public answer is the gate's own redirect. Expected: (a) passes, (b) passes (`dsh.example.org`-style trust works for an address too), (c) passes, then FAIL at the first path of (d) with `... is on the Internet with nothing but the login gate in front of it`.

- [ ] **Step 6: Stand-ins, `TUNNEL_HOSTNAME=192.0.2.1`: run (d)**

Expected: FAIL at (d) with the curl failure message, after about 20 seconds.

- [ ] **Step 7: Stand-ins, `TUNNEL_HOSTNAME` changed in `.env` only: run (e)**

Set `TUNNEL_HOSTNAME=192.0.2.99` in `.env` and do not run `docker compose up`. Expected: FAIL at (a), `dsh was created with TUNNEL_HOSTNAME: want "192.0.2.99", got "172.31.254.20"`.

- [ ] **Step 8: `scripts/check.sh shell`, then commit**

Expected: `All checks passed: shell`.

`feat(smoke): check the tunnel, the counted client address and Cloudflare Access`

---

### Task 6: Documentation

**Files:**
- Modify: `README.md`, `README.zh-CN.md`
- Modify: `docs/operations.md`, `docs/operations.zh-CN.md`
- Modify: `docs/design.md`, `docs/design.zh-CN.md`
- Modify: `CONTRIBUTING.md`, `CHANGELOG.md`

- [ ] **Step 1: `README.md` and `README.zh-CN.md`**

- *Features*: one bullet: an optional public entrance through a Cloudflare Tunnel, no inbound port, Cloudflare Access in front, the login gate still applying.
- *Architecture*: below the existing diagram, a four-line diagram of the second entrance: browser elsewhere, `https://<TUNNEL_HOSTNAME>`, Cloudflare (Access), the outbound tunnel, `cloudflared` (`compose.tunnel.yml`, optional), `http://caddy:8080` on its own network, reaching caddy only.
- *Security model*: the first bullet becomes "Private network by default", keeping its reasons; it adds that the Cloudflare Tunnel is the supported way to add a public entrance, that Cloudflare Access must stay in front of the hostname (dsh runs whatever its users ask; the login rate limit is not a defence against the Internet), and that the smoke test fails when Access is missing.
- *FAQ*: "Can members reach it without the LAN or the VPN?" answered with the tunnel and a pointer to the runbook; port 443 is still not to be forwarded.

- [ ] **Step 2: `docs/operations.md` and `docs/operations.zh-CN.md`**

- New section `## Cloudflare Tunnel` after `## Members`, with:
  - what it adds, and that it is off unless enabled;
  - *Enable it*, numbered: (1) Cloudflare Zero Trust, Networks, Tunnels: create a tunnel of type Cloudflared and copy its token; (2) Public Hostnames: subdomain and domain, service type `HTTP`, URL `caddy:8080` (the Compose service name; HTTP, because the connection stays inside the Compose network); (3) Access, Applications: add a self-hosted application for that hostname and an Allow policy naming who may use it (mail addresses, or a group from the identity provider); (4) `.env`: the three lines from `.env.example`; (5) `docker compose up -d` in the deployment directory: recreates caddy (about ten seconds without service) and dsh (running agent sessions are interrupted), starts cloudflared; (6) `scripts/smoke.sh`: section 11.
  - *Turn it off*: remove the `COMPOSE_FILE` line (and, optionally, the two variables), then `docker compose up -d --remove-orphans`; it removes cloudflared and recreates caddy and dsh. Delete the public hostname or the tunnel in the dashboard afterwards.
  - *What to know*: run `docker compose` in the deployment directory (`COMPOSE_FILE` is resolved from there, and not read with `--env-file`); the output of `docker compose config` shows `TUNNEL_TOKEN`; after changing `TUNNEL_HOSTNAME`, `docker compose up -d` is needed (dsh keeps the environment it was created with); after a cloudflared version bump, `docker compose up -d cloudflared`; the visitor address through the tunnel comes from `Cf-Connecting-Ip`, so Cloudflare features that remove or rewrite it ("Remove visitor IP headers", Pseudo IPv4 "Overwrite Headers") make every tunnel visitor share one login rate-limit bucket (five wrong passwords per minute, all visitors together); Cloudflare closes a proxied request whose origin sends nothing for 100 seconds (its error 524), so a dsh stream that stays silent that long may have to reconnect; dsh still advertises `DSH_PUBLIC_URL`, the private address; the tunnel is outbound only, so the host needs no inbound port.
- *Updating dsh-server*: enabling or disabling the tunnel is an edit of `.env`; point to the new section.
- *Troubleshooting*, four rows: the public hostname answers `502`, `503` or error `1033`; the public hostname shows the dsh-server login page without an Access login first; everyone through the tunnel is rate-limited together; `smoke.sh` section 11 says dsh was created with another `TUNNEL_HOSTNAME`.
- *Known risks*: the sentence "It is not exposed to the public Internet" becomes: without the tunnel it is not exposed to the public Internet; with the tunnel, the public hostname is part of the attack surface, behind Cloudflare Access and the login gate. Port 443 is not to be forwarded either way.
- *Variables*: rows for `COMPOSE_FILE`, `TUNNEL_TOKEN` and `TUNNEL_HOSTNAME`.

- [ ] **Step 3: `docs/design.md` and `docs/design.zh-CN.md`**

A new section at the end, `## Increment: public access through a Cloudflare Tunnel (2026-11-26, implemented)`, in the style of the earlier increments: the decisions and why (an override file rather than a profile; cloudflared on its own network; the second listener; `Cf-Connecting-Ip` rather than `X-Forwarded-For`; one route chain; the second trusted authority; Access in the dashboard and what the stack does and does not check), the measured facts from "What recon established" above that a future change depends on, and what was verified where (routing script, unit test, sandbox stack, smoke test) with the one item still open at the time of writing: the exact answer Cloudflare Access gives the smoke test's request.

- [ ] **Step 4: `CONTRIBUTING.md` and `CHANGELOG.md`**

`CONTRIBUTING.md`: the `config` and `shell` rows of the checks table match the new `scripts/check.sh` header; under "Version pins", `cloudflared` in `compose.tunnel.yml` is pinned on purpose: bump, `docker compose up -d cloudflared`, smoke test.

`CHANGELOG.md`, under `[Unreleased]`, `Added`: the tunnel in one sentence (the container on its own network, the second listener, the smoke test section).

- [ ] **Step 5: Check the pairs and commit**

For each pair, the English and the Chinese file have the same headings in the same order and the same numbers (`100` seconds, five failures, the image tag). Commit: `docs: describe the Cloudflare Tunnel option`.

---

### Task 7: Verification before the merge

- [ ] **Step 1:** `scripts/check.sh` (the five fast groups) passes from the worktree root.
- [ ] **Step 2:** `scripts/check.sh image` passes (the dsh image built from the tree, with its arguments check).
- [ ] **Step 3:** the sandbox matrix of Task 5 still gives the same results after the last change to the branch.
- [ ] **Step 4:** `sandbox/live-after.txt` equals `sandbox/live-before.txt`; the sandbox is stopped (`docker compose down` twice: with the tunnel on, then with the `.env` of Task 4, Step 4), the sandbox images and the stand-ins are removed.
- [ ] **Step 5:** the review of the whole branch (read-only reviewer), then fixes with their own checks.
- [ ] **Step 6:** append an "As executed" note to this plan: what was verified, what was not, and the rollout steps that need the operator.

## Rollout (the operator decides when; none of it is part of this plan's execution)

1. Tag the running images for a rollback (`docker tag dsh-server-dsh:latest dsh-server-dsh:pre-tunnel-<date>`, likewise `auth-gate`).
2. Bring the merged `master` into the deployment directory.
3. The operator adds the three lines to `.env` themselves: the token is a secret and `.env` is never read or printed by anyone else.
4. `docker compose build && docker compose up -d` in the deployment directory. This also replaces the dsh and auth-gate images with the ones built from the public tree, if the deployment still runs older ones.
5. `scripts/smoke.sh`; section 11 is the first run against the real Cloudflare Access and decides whether its `302` pattern is the right one.

## As executed (2026-11-26)

Deviations from the plan above:

- The assertion shipped for Task 1 is behavioural: `scripts/check.sh` runs the real
  Caddyfile in the Compose caddy image against a stand-in for auth-gate and dsh, with
  forged headers on both listeners. The planned assertion over `caddy adapt` only read the
  shape of the configuration and could not see a Caddy upgrade that changes a default. The
  workspace script of Step 5 was therefore not written.
- What was measured on the cloudflared image changed the text, not the design: a token
  that does not parse makes cloudflared exit at once, and so does having no network; only
  a well-formed token that Cloudflare does not accept leaves it running and not ready.
  `cloudflared tunnel ready` exits 0 against an endpoint that answers 200. The sandbox uses
  a well-formed fake token.
- The entrypoint test of Task 3 got a case for an empty `TUNNEL_HOSTNAME`, which the plan's
  interface required but did not test.
- Section 11 of the smoke test differs from the plan in its probe (b). A dsh session
  belongs to the authority it was issued for, so the cookie of section 10 cannot be
  presented with the public hostname; the probe obtains a session through the tunnel
  listener first, the way automatic sign-in does. The private-listener probe of (c) needs
  `-k`. The glob of the Access pattern was first written inside quotes, so that branch
  could never match; the stand-in run caught it. The "does not answer" line first reported
  the exit status of the negated pipeline, always 0; it now reports curl's.
- Turning the tunnel off with `docker compose up -d --remove-orphans` leaves the
  `<project>_tunnel` network behind. The runbook says so.
- Task 6 also fixed, in a commit of its own, a pipe inside a code span of a
  pre-existing troubleshooting row: in GFM it split the cell, and the rest of that row was
  not rendered on GitHub.
- The whole-branch review (one fresh reviewer, read-only) found three Important and six
  Minor issues, and listed what it could not verify. Fixed afterwards, each seen failing
  and then passing on the sandbox stack:
  - A `TUNNEL_HOSTNAME` with a scheme or a path crash-looped dsh, and caddy waited for it:
    one typo in the optional tunnel took the private entrance down (the reviewer's Minor 4,
    re-graded by its effect). The entrypoint now leaves such a value out and says so;
    section 11 reads the command line dsh was started with, which also names a stale
    container and an image older than this option.
  - The runbooks say what a mutating `docker compose` command does when it does not load
    the override, that every other hostname routed to the tunnel needs an Access
    application of its own, and that Cloudflare is in the path of the traffic.
  - `SECURITY.md` and the design record no longer describe a stack that is never exposed.
- Three minors were deferred: section 0 does not wait while cloudflared is still
  `starting`; a Compose failure in the middle of section 11 ends the script without a FAIL
  line; the public-hostname probe forces `--noproxy '*'` and its hints do not mention name
  resolution, egress or a WAF rule. A listener that refuses every `Host` but
  `TUNNEL_HOSTNAME` was not built: it needs the variable in caddy's environment and a
  matcher whose behaviour with the variable unset was not measured.

Verified: the five check groups and the `image` group on the final tree; the sandbox
matrix (tunnel off, stand-ins for cloudflared and Access, the real image with a token
Cloudflare does not accept) on the final tree; the documentation pairs; the stack running
next to the sandbox unchanged, by container ID and start time.

Not verified here, and open until the first run against a real tunnel: `cloudflared tunnel
ready` with an accepted token; what Cloudflare Access answers to the probe of section
11(d), and whether a WAF or bot rule answers first; whether the dashboard labels quoted in
the runbook still apply; `routing()` on a GitHub runner, which has to reach container
addresses. The rollout has not happened: it needs the operator's token and their decision
on when dsh may be restarted.
