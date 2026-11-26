#!/bin/bash
# shellcheck disable=SC2015
# ^ `A && echo ok || fail` is used throughout on purpose: the middle command
#   is an echo that cannot fail, so the right-hand side only runs when A fails.
#
# Integration smoke test: walks a running compose stack through the key paths
# of the runbook - the 302 login gate, login and cookies, the dsh token
# exchange, automatic sign-in on 401, token sync, WebSocket passthrough,
# revocation on user deletion, and token rotation on restart. The first
# unexpected result exits non-zero.
#
# Usage: scripts/smoke.sh              (default target: the stack's public URL, read from .env)
#        BASE_URL=https://host scripts/smoke.sh
#
# Notes:
# - creates a temporary user `smoketest` and deletes it at the end (users.json
#   is hot-reloaded, so nothing is restarted; sections 4 and 10 each create
#   and delete it once, and the exit trap cleans up whatever is left);
# - refuses a stack whose token is not being synced: section 0 fails when the
#   cron file is missing or the token file has not been refreshed for a few
#   minutes, which means automatic sign-in would die after the next restart;
# - restarts dsh once in the middle (`docker compose restart dsh`, to verify
#   token rotation) - logged-in members keep their dsh cookie in practice, but
#   open pages blip and reconnect;
# - the token URL is only used on this host for comparison and exchange, and
#   is always masked in the output (it is dsh's login credential);
# - with the Cloudflare Tunnel enabled (`compose.tunnel.yml` in `.env`),
#   section 11 also runs: it checks that dsh accepts the public hostname,
#   which address the login rate limit counts on each listener, and - fetching
#   the public hostname from this host - that Cloudflare Access is in front
#   of it.
set -euo pipefail

cd "$(dirname "$0")/.."

# Target of the run. With no BASE_URL, take the public URL that compose
# resolves from .env (DSH_PUBLIC_URL, which itself defaults to DSH_TRUSTED_HOST).
if [ -z "${BASE_URL:-}" ]; then
    base_url="$(docker compose config --format json 2>/dev/null | jq -r '.services.dsh.environment.DSH_PUBLIC_URL // ""' || true)"
    [ -n "$base_url" ] || { echo "smoke.sh: cannot tell which stack to test. Run it from the deployment directory with a valid .env, or set BASE_URL." >&2; exit 1; }
    BASE_URL="${base_url%/}"
fi

# compose.tunnel.yml in COMPOSE_FILE (.env) adds a fourth container and a
# public hostname. Sections 0 and 11 take that into account.
tunnel=0
if [ "$(docker compose config --services 2>/dev/null | grep -cx cloudflared || true)" -ge 1 ]; then
    tunnel=1
fi
tunnel_summary=""

SMOKE_USER="smoketest"
SMOKE_PASS="smoke-$(date +%s)-$RANDOM"
CJ_GATE="$(mktemp)"     # auth-gate's gate_session cookie
CJ_DSH="$(mktemp)"      # dsh's dsh-auth-* cookie
CJ_ENTRY="$(mktemp)"    # section 10: gate_session only, deliberately without any dsh cookie
USERS_JSON="auth-gate/data/users.json"

# users_del <username>: remove one user from users.json (the service hot-reloads
# on mtime, so its cookie is revoked at once). compose runs auth-gate as the
# administrator uid and users.json is owned by that account, so normally it is
# read and written directly. An older deployment with a root-owned file (0600)
# falls back to sudo.
# What the sudo branch writes back must be owned 1000:1000 (matching compose
# user:): as root:root, a running auth-gate (uid 1000) could not read the 0600
# users.json, the hot-reload would fail, and the next restart would
# crash-loop.
# jq must take the name as a literal key through --arg: del(.$1) would parse
# the name as jq path syntax, so a name containing "." (john.doe) turns into a
# nested field -> del does nothing and exits 0 while the file is rewritten
# unchanged. Section 8's "deleted user logs in again -> 401" check catches
# that silent failure.
users_del() {
    local tmp
    tmp="$(mktemp)"
    if [ -r "$USERS_JSON" ] && [ -w "$USERS_JSON" ]; then
        jq --arg u "$1" 'del(.[$u])' "$USERS_JSON" > "$tmp"
        cat "$tmp" > "$USERS_JSON"
    else
        # shellcheck disable=SC2024  # the redirect is made by the invoking user into a file it owns (mktemp); only jq needs root to read users.json
        sudo jq --arg u "$1" 'del(.[$u])' "$USERS_JSON" > "$tmp"
        sudo install -m 600 -o 1000 -g 1000 "$tmp" "$USERS_JSON"
    fi
    rm -f "$tmp"
}

cleanup() {
    # Best-effort removal of the smoke user (a failure here must not hide the
    # main flow's exit code).
    if [ -f "$USERS_JSON" ] && command -v jq >/dev/null 2>&1; then
        users_del "$SMOKE_USER" 2>/dev/null || true
    fi
    rm -f "$CJ_GATE" "$CJ_DSH" "$CJ_ENTRY"
}
trap cleanup EXIT

mask() { sed -E 's/token=[^[:space:]]+/token=***MASKED***/g'; }

# check <name> <want> <got>: equal prints ok; otherwise prints the difference
# and exits 1.
check() {
    if [ "$2" = "$3" ]; then
        printf 'ok    %s\n' "$1"
    else
        printf 'FAIL  %s: want %q, got %q\n' "$1" "$2" "$3" >&2
        exit 1
    fi
}

command -v jq >/dev/null 2>&1 || { echo "smoke.sh needs jq (to delete the smoke user)" >&2; exit 1; }

echo "== 0. service state =="
want_running=3
services="dsh auth-gate"
if [ "$tunnel" = 1 ]; then
    want_running=4
    services="$services cloudflared"
fi
n=$(docker compose ps --status running -q | wc -l | tr -d ' ')
check "running containers = $want_running" "$want_running" "$n"
for svc in $services; do
    cid=$(docker compose ps --all -q "$svc")
    [ -n "$cid" ] || { echo "FAIL  $svc has no container (is the stack up?)" >&2; exit 1; }
    st=$(docker inspect -f '{{.State.Health.Status}}' "$cid")
    check "$svc healthy" healthy "$st"
done
# The cron job that syncs the token must exist, and it must be working. It is
# the step that is most easily lost when the stack is rebuilt on a new host
# (step 6 of the first deploy in docs/operations.md), and section 10 runs
# sync-token.sh by hand, which would mask the loss: automatic sign-in would
# then quietly stop working after the next dsh restart. Surface both here.
test -f /etc/cron.d/dsh-server-token \
    && echo "ok    /etc/cron.d/dsh-server-token exists (token synced every minute)" \
    || { echo "FAIL  /etc/cron.d/dsh-server-token is missing: nothing synces the token automatically (see docs/operations.md, first deploy, step 6)" >&2; exit 1; }
# ...and it has to be working. The cron file can exist while nothing is
# syncing (cron not running, or root cannot reach docker). sync-token.sh
# rewrites the token file every minute, so a file that is missing or older
# than a few minutes means automatic sign-in dies after the next dsh restart.
# Checked here, before anything else: section 10 syncs by hand, which would
# hide it.
token_file=auth-gate/data/dsh-token
if [ ! -f "$token_file" ]; then
    echo "FAIL  $token_file does not exist: the cron job has not run yet. Wait a minute after installing it, or run it once by hand: sudo scripts/sync-token.sh (see docs/operations.md, first deploy, step 6)" >&2
    exit 1
fi
token_age=$(( $(date +%s) - $(stat -c %Y "$token_file") ))
if [ "$token_age" -le 180 ]; then
    echo "ok    token file is ${token_age}s old (cron is syncing it)"
else
    echo "FAIL  token file is ${token_age}s old: nothing is syncing it. Is cron running, and can root run docker compose? Try by hand: sudo scripts/sync-token.sh" >&2
    exit 1
fi

# /root is mounted out as a whole (./dsh-root:/root in compose), so a rebuilt
# container no longer loses the home directory's runtime state. /root/.gitkeep
# is the mount probe (a structural marker that does not change with user data):
# if it is missing, the mount is not in effect (dsh-root/ was not copied to
# the new host, say), sessions would be written into the rootfs and lost on
# the next rebuild (the lesson of the 2026-11-22 upgrade incident, hence the
# checkpoint).
docker compose exec -T dsh test -f /root/.gitkeep \
    && echo "ok    /root/.gitkeep is present (dsh-root mount in effect, home state persisted)" \
    || { echo "FAIL  /root/.gitkeep is missing: the ./dsh-root:/root mount is not in effect (check the compose volumes and the dsh-root/ directory)" >&2; exit 1; }

# Name every deterministic dependency baked into the image: things that have
# vanished after a rebuild before (the apt tools, chromium, gh) are surfaced
# right here instead of failing later inside a session.
missing=$(docker compose exec -T dsh sh -c '
    for c in git curl ssh sshpass xauth python3 pnpm mnemon chromium gh; do
        command -v "$c" >/dev/null 2>&1 || printf "%s " "$c"
    done')
missing=${missing% }
[ -z "$missing" ] && echo "ok    image dependencies present (apt tools, pnpm, mnemon, chromium, gh)" \
    || { echo "FAIL  image dependencies missing: $missing (image not rebuilt, or the Dockerfile was reverted)" >&2; exit 1; }

# gh must be the copy in the image: /usr/local/bin/gh comes first on PATH and
# would silently replace the pinned version.
gh_path=$(docker compose exec -T dsh sh -c 'command -v gh')
check "gh comes from the image (not shadowed by /usr/local/bin)" /usr/bin/gh "$gh_path"

# CJK and emoji fonts: when they are missing, the only symptom is missing-glyph
# boxes in screenshots; there is no other signal.
fonts=$(docker compose exec -T dsh sh -c '
    fc-match "sans-serif:lang=zh-cn" | grep -q "Noto Sans CJK" && fc-match emoji | grep -q "Noto Color Emoji" && echo ok || echo missing')
[ "$fonts" = ok ] && echo "ok    CJK and emoji fonts resolve (Noto Sans CJK / Noto Color Emoji)" \
    || { echo "FAIL  fonts missing: fc-match finds neither Noto Sans CJK nor Noto Color Emoji (fonts-noto-* gone from the image; screenshots will show missing-glyph boxes)" >&2; exit 1; }

echo "== 1. dsh start-up URL line =="
# grep -c, not grep -q. `docker compose logs` streams its output, and a -q
# grep exits at the first match; when that match comes early, the command is
# usually still writing, its next write hits a closed pipe, and it dies of
# SIGPIPE. Under pipefail that turns a line that was found into a failed
# pipeline. -c reads to the end, so the command always finishes.
n=$(docker compose logs --no-color dsh 2>/dev/null | grep -ac 'dsh web: https://' || true)
if [ "${n:-0}" -ge 1 ]; then
    echo "ok    logs dsh contain a 'dsh web:' URL line"
else
    echo "FAIL  logs dsh contain no 'dsh web:' URL line" >&2
    exit 1
fi

echo "== 2. unauthenticated GET -> 302 to the login page (a browser only navigates on 3xx) =="
out=$(curl -sk -o /dev/null -w '%{http_code} %{redirect_url}' "$BASE_URL/some/path")
check "GET /some/path without a session" "302 $BASE_URL/auth/login?next=%2Fsome%2Fpath" "$out"

echo "== 3. login page reachable =="
# grep -c exits 1 when nothing matches, which under pipefail would end the
# script right here: `|| true` routes that case into check's FAIL branch
# instead.
n=$(curl -sk "$BASE_URL/auth/login" | grep -c '连接到节点' || true)
check "login page contains its heading" 1 "$n"

echo "== 4. create the smoke user and log in =="
printf '%s\n' "$SMOKE_PASS" | scripts/add-user.sh "$SMOKE_USER" >/dev/null
out=$(curl -sk -c "$CJ_GATE" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$SMOKE_USER\",\"password\":\"$SMOKE_PASS\"}" \
    -o /dev/null -w '%{http_code}' "$BASE_URL/auth/login")
check "POST /auth/login with the right credentials" 200 "$out"
grep -q gate_session "$CJ_GATE" && echo "ok    gate_session cookie issued" \
    || { echo "FAIL  no gate_session cookie issued (the HttpOnly line carries the cookie name in clear text too)" >&2; exit 1; }

out=$(curl -sk -b "$CJ_GATE" -o /dev/null -w '%{http_code}' "$BASE_URL/auth/check")
check "gate cookie passes /auth/check" 200 "$out"

echo "== 5. dsh token exchange (the gate cookie only opens the door; dsh has its own session layer) =="
url=$(scripts/dsh-url.sh)
out=$(curl -sk -b "$CJ_GATE" -c "$CJ_DSH" -o /dev/null -w '%{http_code}' "$url")
check "open the token URL (303 = cookie set, then redirect to /)" 303 "$out"
awk '{print $6}' "$CJ_DSH" | grep -q '^dsh-auth-' \
    && echo "ok    dsh-auth-* cookie issued" \
    || { echo "FAIL  no dsh-auth-* cookie issued" >&2; exit 1; }
out=$(curl -sk -b "$CJ_GATE" -b "$CJ_DSH" -o /dev/null -w '%{http_code}' "$BASE_URL/")
check "GET / with both cookies = dsh front end" 200 "$out"

echo "== 6. WebSocket passthrough (--http1.1 is required: under h2 the Connection/Upgrade headers are dropped) =="
ws_h='Connection: Upgrade'
out=$(curl -sk --http1.1 -b "$CJ_GATE" -b "$CJ_DSH" \
    -H "$ws_h" -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' \
    -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
    -o /dev/null -w '%{http_code}' --max-time 10 "$BASE_URL/api/remote.mux")
check "WS upgrade /api/remote.mux = 101" 101 "$out"

out=$(curl -sk --http1.1 -H "$ws_h" -H 'Upgrade: websocket' \
    -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
    -o /dev/null -w '%{http_code}' --max-time 10 "$BASE_URL/api/remote.mux")
check "WS upgrade without a session is stopped by the gate (302)" 302 "$out"

echo "== 7. restart dsh -> token rotates, gate cookie unaffected =="
# Has to run before the smoke user is deleted: this section needs a valid gate
# cookie to get through the gate to dsh.
old_url=$(scripts/dsh-url.sh)
url_lines() { docker compose logs --no-color dsh 2>/dev/null | grep -c 'dsh web: https://' || true; }
n0=$(url_lines)
docker compose restart dsh >/dev/null
i=0
# Waiting for "healthy" alone is not enough: dsh opens its listener first (so
# the healthcheck passes) and prints the start-up URL line later (since 0.2.3
# that line moved to after the sidecar start). Fetching the URL in that gap
# returns the old token and would be misread as "not rotated". Only both
# conditions together mean the restart is complete.
until [ "$(docker inspect -f '{{.State.Health.Status}}' "$(docker compose ps --all -q dsh)" 2>/dev/null)" = "healthy" ] \
    && [ "$(url_lines)" -gt "$n0" ]; do
    i=$((i + 1))
    [ "$i" -gt 40 ] && { echo "FAIL  still not healthy, or no new start-up URL line, 2 minutes after the restart" >&2; exit 1; }
    sleep 3
done
new_url=$(scripts/dsh-url.sh)
if [ "$old_url" != "$new_url" ]; then
    echo "ok    token rotated ($(printf '%s' "$old_url" | mask) -> $(printf '%s' "$new_url" | mask))"
else
    echo "FAIL  token did not rotate on restart" >&2
    exit 1
fi
# Since automatic sign-in went in, every 401 from dsh is rewritten by Caddy
# into a 302 to /auth/dsh-entry (handle_response @dsh_unauth in the
# Caddyfile); a bare 401 no longer exists. The observable form of "the old URL
# is dead" is therefore this sign-in redirect instead of a 401.
out=$(curl -sk -b "$CJ_GATE" -o /dev/null -w '%{http_code} %{redirect_url}' "$old_url")
check "old token URL is dead (dsh's 401 is caught by Caddy and turned into the sign-in 302)" "302 $BASE_URL/auth/dsh-entry" "$out"
out=$(curl -sk -b "$CJ_GATE" -b "$CJ_DSH" -o /dev/null -w '%{http_code}' "$BASE_URL/")
check "logged-in session (dsh cookie) still valid after the restart" 200 "$out"

echo "== 8. delete the smoke user -> its cookie dies at once (mtime hot-reload, no restart) =="
users_del "$SMOKE_USER"
sleep 1
out=$(curl -sk -b "$CJ_GATE" -o /dev/null -w '%{http_code}' "$BASE_URL/auth/check")
check "deleted user's gate cookie" 302 "$out"
out=$(curl -sk -H 'Content-Type: application/json' \
    -d "{\"username\":\"$SMOKE_USER\",\"password\":\"$SMOKE_PASS\"}" \
    -o /dev/null -w '%{http_code}' "$BASE_URL/auth/login")
check "deleted user tries to log in again" 401 "$out"

echo "== 9. root certificate is where members get it =="
# root.crt is written by the container as root (0600), so an unprivileged user
# on the host cannot read it: look at it from inside the container, which also
# proves it really lives in the /data volume (the path members get it from).
docker compose exec caddy test -s /data/caddy/pki/authorities/local/root.crt \
    && echo "ok    caddy/data/caddy/pki/authorities/local/root.crt exists" \
    || { echo "FAIL  root.crt does not exist" >&2; exit 1; }

echo "== 10. automatic sign-in on 401 (the gate account is the only credential; the token is handed out automatically) =="
# Scenario: a member has a gate account and has never opened a token URL.
# Section 8 deleted the smoke user, so it is recreated and logged in again
# here to get a brand-new cookie jar that holds only gate_session (deliberately
# not CJ_DSH from section 5: that jar has a dsh cookie, which would make the
# test meaningless). Chain: GET / -> dsh answers 401 -> Caddy catches it, 302
# /auth/dsh-entry -> auth-gate hands out the current token, 302
# /?token=<current> -> dsh issues a session cookie -> the front end loads.
# curl -L follows to the end; the result must not be 401 and the jar must
# contain dsh-auth-* (the latter is the proof: if the chain broke at the login
# page or the sync page the status would also be 200, so only the cookie tells
# them apart).
printf '%s\n' "$SMOKE_PASS" | scripts/add-user.sh "$SMOKE_USER" >/dev/null
out=$(curl -sk -c "$CJ_ENTRY" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$SMOKE_USER\",\"password\":\"$SMOKE_PASS\"}" \
    -o /dev/null -w '%{http_code}' "$BASE_URL/auth/login")
check "recreate the smoke user and log in" 200 "$out"

# Section 7 just restarted dsh (the token rotated) and the minute-level cron
# sync may not have caught up: run sync-token.sh once by hand so the latest
# token is in the file and the chain is deterministic. stderr is dropped on
# purpose: on failure the script may print a string that looks like a token;
# rerun it by hand to see.
if scripts/sync-token.sh 2>/dev/null; then
    echo "ok    sync-token.sh exits 0 (token synced)"
else
    echo "FAIL  sync-token.sh exited non-zero: token not synced, automatic sign-in unavailable" >&2
    echo "Investigate: run scripts/sync-token.sh by hand and read stderr; docker compose logs dsh | grep token=" >&2
    exit 1
fi
# The token in the file must equal the newest one in the logs (parsing has a
# single source of truth: dsh-url.sh). The token is a secret: on mismatch only
# the lengths are printed, never the content.
ftok=$(cat auth-gate/data/dsh-token)
utok=$(scripts/dsh-url.sh); utok=${utok##*token=}
if [ "$ftok" = "$utok" ]; then
    echo "ok    dsh-token file matches the newest token in the logs"
else
    echo "FAIL  dsh-token file does not match the newest token in the logs (file ${#ftok} characters vs logs ${#utok} characters)" >&2
    exit 1
fi

out=$(curl -skL -b "$CJ_ENTRY" -c "$CJ_ENTRY" -o /dev/null -w '%{http_code}' "$BASE_URL/")
if [ "$out" != 401 ]; then
    echo "ok    without a dsh cookie the whole sign-in chain completes (401 -> entry -> token -> session); final status $out, not 401"
else
    echo "FAIL  the sign-in chain ends in 401: automatic sign-in is not working" >&2
    exit 1
fi
awk '{print $6}' "$CJ_ENTRY" | grep -q '^dsh-auth-' \
    && echo "ok    the chain produced a dsh-auth-* cookie (really inside dsh, not the login or sync page)" \
    || { echo "FAIL  the sign-in chain produced no dsh-auth-* cookie" >&2; exit 1; }

if [ "$tunnel" = 1 ]; then
    echo "== 11. Cloudflare Tunnel (dsh accepts the public hostname, the rate limit counts the visitor, Cloudflare Access is in front) =="
    tunnel_host=$(docker compose config --format json 2>/dev/null | jq -r '.services.dsh.environment.TUNNEL_HOSTNAME // ""')
    [ -n "$tunnel_host" ] || { echo "FAIL  TUNNEL_HOSTNAME is not set, although compose.tunnel.yml is active" >&2; exit 1; }

    # A container keeps the environment it was created with: dsh was told the
    # public hostname when it was created, not when .env was edited.
    got=$(docker compose exec -T dsh printenv TUNNEL_HOSTNAME 2>/dev/null || true)
    if [ "$got" != "$tunnel_host" ]; then
        echo "FAIL  dsh was created with TUNNEL_HOSTNAME \"$got\", not \"$tunnel_host\": a container keeps the environment it was created with. Run \`docker compose up -d\` to recreate it" >&2
        exit 1
    fi
    echo "ok    dsh was created with TUNNEL_HOSTNAME $tunnel_host"

    # dsh also has to accept that hostname as an authority: the pages load
    # through the gate, but dsh's fence refuses API requests whose Host it was
    # not told about (403). A dsh session belongs to the authority it was
    # issued for (the cookie names it), so the one in $CJ_ENTRY, issued for
    # DSH_TRUSTED_HOST, is of no use here. One for the public hostname is
    # obtained first, the way automatic sign-in obtains it for a member: the
    # token URL is opened with Host set to the public hostname, through the
    # tunnel listener (http://caddy:8080 in the Caddyfile). The WebSocket
    # probe of section 6 then goes the same way. Both run inside the dsh
    # container (in_dsh), and the cookies go in as a literal header: the URL
    # host is not $BASE_URL and the listener is plain http, so a jar would
    # send none.
    in_dsh() { docker compose exec -T dsh curl -s --noproxy '*' --max-time 10 "$@"; }
    gate_cookie="gate_session=$(awk '$6 == "gate_session" {print $7}' "$CJ_ENTRY")"
    url=$(scripts/dsh-url.sh)                    # holds the token: never printed
    tunnel_url="http://caddy:8080/${url#*://*/}" # the same path and query, through the tunnel listener
    answer=$(in_dsh -o /dev/null -c - -w '%{http_code}\n' -H "Host: $tunnel_host" -H "Cookie: $gate_cookie" "$tunnel_url" | tr -d '\r' || true)
    status=$(printf '%s\n' "$answer" | awk '/^[0-9][0-9][0-9]$/')
    dsh_cookie=$(printf '%s\n' "$answer" | awk '$6 ~ /^dsh-auth-/ {printf "%s=%s", $6, $7}')
    if [ -z "$dsh_cookie" ]; then
        echo "FAIL  opening the token URL through the tunnel listener with Host $tunnel_host answered ${status:-nothing} and set no dsh session cookie: dsh does not serve the public hostname. Was dsh recreated since TUNNEL_HOSTNAME was set? Has a dsh upgrade changed its trust fence?" >&2
        exit 1
    fi
    out=$(in_dsh --http1.1 -o /dev/null -w '%{http_code} %{redirect_url}' -H "Host: $tunnel_host" -H "Origin: https://$tunnel_host" \
        -H 'Sec-Fetch-Site: same-origin' -H "Cookie: $gate_cookie; $dsh_cookie" \
        -H "$ws_h" -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
        http://caddy:8080/api/remote.mux 2>/dev/null | tr -d '\r' || true)
    if [ "${out%% *}" != 101 ]; then
        echo "FAIL  WebSocket upgrade through the tunnel listener with Host $tunnel_host answered ${out:-nothing}, want 101" >&2
        exit 1
    fi
    echo "ok    dsh serves the public hostname (session issued and WebSocket upgrade accepted through the tunnel listener)"

    # Which address the login rate limit counts, per listener. Both probes
    # come from the dsh container and carry addresses a client could forge;
    # auth-gate logs the address it counted for every login attempt. With the
    # tunnel on, caddy has a second address on the tunnel network, which the
    # dsh container cannot reach, so the default-network one is used.
    project=$(docker compose config --format json 2>/dev/null | jq -r '.name')
    address() { docker inspect -f "{{with index .NetworkSettings.Networks \"${project}_default\"}}{{.IPAddress}}{{end}}" "$(docker compose ps -q "$1")"; }
    caddy_ip=$(address caddy)
    dsh_ip=$(address dsh)
    trusted_host=$(docker compose config --format json 2>/dev/null | jq -r '.services.caddy.environment.DSH_TRUSTED_HOST // ""')
    # counted_address <curl arguments...>: the address auth-gate counted for
    # one login attempt with a wrong password, under a name no other attempt
    # uses. Prints nothing when no log line shows up; probe_code then holds
    # what the attempt answered. -k: the private listener uses the internal CA.
    counted_address() {
        local name="xff-$RANDOM-$RANDOM" ip i
        probe_code=$(in_dsh -k -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' \
            -d "{\"username\":\"$name\",\"password\":\"wrong\"}" "$@" 2>/dev/null || true)
        ip=""
        for i in 1 2 3 4 5; do
            ip=$(docker compose logs --no-color auth-gate 2>/dev/null | grep -F "user=\"$name\"" | tail -1 | sed -n 's/.* ip=\([^ ]*\) result=.*/\1/p' || true)
            [ -n "$ip" ] && break
            sleep 1
        done
        printf '%s' "$ip"
    }
    # counted <label> <want> <curl arguments...>
    counted() {
        local got
        got=$(counted_address "${@:3}")
        if [ -z "$got" ]; then
            echo "FAIL  $1: the login attempt answered ${probe_code:-nothing}, and auth-gate logged no address for it" >&2
            exit 1
        fi
        check "$1" "$2" "$got"
    }
    [ -n "$caddy_ip" ] && [ -n "$dsh_ip" ] && [ -n "$trusted_host" ] \
        || { echo "FAIL  cannot find the addresses of caddy and dsh in the ${project}_default network, or DSH_TRUSTED_HOST" >&2; exit 1; }
    counted "private listener: the login rate limit counts the peer, whatever the client sent" "$dsh_ip" \
        -H 'X-Forwarded-For: 192.0.2.1' -H 'Cf-Connecting-Ip: 192.0.2.1' \
        --resolve "$trusted_host:443:$caddy_ip" "https://$trusted_host/auth/login"
    counted "tunnel listener: the login rate limit counts Cf-Connecting-Ip, whatever the client sent" 203.0.113.9 \
        -H 'Cf-Connecting-Ip: 203.0.113.9' -H 'X-Forwarded-For: 192.0.2.1' http://caddy:8080/auth/login

    # From this host, over the Internet: the public hostname has to answer
    # through Cloudflare, and Cloudflare Access has to be in front of it. dsh
    # runs whatever its users ask it to, so the login page must not be
    # reachable by anyone: Access keeps strangers away, and the gate then asks
    # for the member's password. The login endpoint is asked as well as the
    # front page: an Access application that covers only one path leaves the
    # other open.
    hints="Check: docker compose ps cloudflared (it has to be healthy), docker compose logs cloudflared, and that the public hostname's service in the Cloudflare dashboard is http://caddy:8080"
    for path in / /auth/login; do
        url="https://$tunnel_host$path"
        # curl's own explanation of a failure (timeout, certificate, name
        # resolution) goes to stderr before the FAIL line.
        rc=0
        headers=$(curl -sS --noproxy '*' --max-time 20 -H 'Accept: text/html' -o /dev/null -D - "$url" | tr -d '\r') || rc=$?
        if [ "$rc" != 0 ]; then
            echo "FAIL  $url does not answer (curl exit $rc); does this host resolve the name? $hints" >&2
            exit 1
        fi
        status=$(printf '%s\n' "$headers" | awk 'NR == 1 {print $2}')
        location=$(printf '%s\n' "$headers" | awk 'tolower($1) == "location:" {print $2}' | tail -1)
        case "$status $location" in
            '302 https://'*'cloudflareaccess.com/'*)
                echo "ok    Cloudflare Access is in front of $url" ;;
            '302 /auth/'*)
                echo "FAIL  $url is on the Internet with nothing but the login gate in front of it. Create the Access application for the hostname (docs/operations.md, \"Cloudflare Tunnel\"), then run the smoke test again" >&2
                exit 1 ;;
            *)
                echo "FAIL  $url answered \"${status:-nothing}\", expected a redirect to Cloudflare Access. $hints" >&2
                exit 1 ;;
        esac
    done
    tunnel_summary=" and https://$tunnel_host/ (Cloudflare Tunnel)"
fi

echo
echo "All smoke checks passed: $BASE_URL$tunnel_summary"
