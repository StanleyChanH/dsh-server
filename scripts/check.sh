#!/usr/bin/env bash
# The checks CI runs, on your machine. Needs docker; nothing else has to be
# installed.
#
# Usage: scripts/check.sh [group ...]
#   go          gofmt, go vet and go test for auth-gate, inside its build image
#   shell       ShellCheck on scripts/*.sh and dsh/entrypoint.sh, and the
#               arguments the dsh entrypoint hands to dsh
#   dockerfile  hadolint on both Dockerfiles, with .hadolint.yaml
#   config      compose renders, the required variables are really required,
#               the Caddyfile validates only with DSH_TRUSTED_HOST, each of
#               its listeners hands the backends the right client address, and
#               compose.tunnel.yml keeps cloudflared to its own network
#   build       the auth-gate image builds (never with --pull)
#   image       the dsh image builds and has what it should (about 3.4 GB and
#               several minutes, so it is not part of the default run)
# With no group, the five fast groups run. The first failing check stops the
# script with a non-zero exit.
#
# GOPROXY is passed into the Go container; the default is the official module
# proxy, so set GOPROXY=https://goproxy.cn,direct where that is slow or blocked.
# The proxy variables of the shell are passed into that container as well.
# `build` and `image` run `docker build` for real. `build` uses compose's
# defaults for the auth-gate build arguments. `image` passes LAN_PROXY and
# NO_PROXY into the dsh build the way `docker compose build` does; without
# them the build goes direct.
set -euo pipefail

cd "$(dirname "$0")/.."

go() {
    echo '== go: gofmt, go vet, go test (auth-gate)'
    local image
    image=$(sed -n 's/^FROM \([^ ]*\) AS build$/\1/p' auth-gate/Dockerfile)
    [ -n "$image" ] || { echo 'cannot find the build stage in auth-gate/Dockerfile' >&2; return 1; }
    docker run --rm -v "$PWD/auth-gate:/app:ro" -w /app \
        -e GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}" \
        -e HTTP_PROXY -e HTTPS_PROXY -e http_proxy -e https_proxy -e NO_PROXY -e no_proxy \
        "$image" sh -c '
            unformatted=$(gofmt -l .)
            if [ -n "$unformatted" ]; then
                echo "not gofmt-formatted:" >&2
                echo "$unformatted" >&2
                exit 1
            fi
            go vet ./... && go test -count=1 ./...'
}

shell() {
    echo '== shell: ShellCheck, and the arguments the dsh entrypoint hands to dsh'
    docker run --rm -v "$PWD:/mnt:ro" koalaman/shellcheck:v0.11.0 -x scripts/*.sh dsh/entrypoint.sh

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
    # hands <case> <want> <got>
    hands() {
        if [ "$2" != "$3" ]; then
            echo "the dsh entrypoint hands dsh other arguments than expected ($1):" >&2
            diff <(printf '%s\n' "$2") <(printf '%s\n' "$3") >&2 || true
            return 1
        fi
    }
    want="$(printf '%s\n' web --no-open --host --port 3080 --public-url https://192.0.2.10/ --trusted-host 192.0.2.10)"
    got=$(entrypoint_args) || { echo 'cannot run the dsh entrypoint in its base image' >&2; return 1; }
    hands 'TUNNEL_HOSTNAME not set' "$want" "$got"
    got=$(entrypoint_args -e TUNNEL_HOSTNAME=) || { echo 'cannot run the dsh entrypoint in its base image' >&2; return 1; }
    hands 'TUNNEL_HOSTNAME empty' "$want" "$got"
    echo '   the dsh entrypoint hands dsh the expected arguments, also when TUNNEL_HOSTNAME is empty'
    got=$(entrypoint_args -e TUNNEL_HOSTNAME=dsh.example.org) || { echo 'cannot run the dsh entrypoint in its base image' >&2; return 1; }
    hands 'TUNNEL_HOSTNAME set' "$want
--trusted-host
dsh.example.org" "$got"
    echo '   ...and the tunnel hostname as a second trusted authority when TUNNEL_HOSTNAME is set'
}

dockerfile() {
    echo '== dockerfile: hadolint'
    local f
    for f in dsh/Dockerfile auth-gate/Dockerfile; do
        printf '   %s\n' "$f"
        docker run --rm -i -v "$PWD/.hadolint.yaml:/.hadolint.yaml:ro" hadolint/hadolint:2.12.0 < "$f"
    done
}

# Renders compose from the shell alone: --env-file /dev/null stops a .env in
# the checkout (a deployment, say) from supplying a value that a check
# expects to be missing.
compose_json() { docker compose --env-file /dev/null config --format json; }
compose_ok()   { docker compose --env-file /dev/null config -q; }

config() {
    echo '== config: compose and Caddyfile'
    local host=192.0.2.10 secret json caddy_image n
    secret=$(printf 'ab%.0s' $(seq 32))
    export DSH_TRUSTED_HOST=$host DSH_VERSION=0.2.2-rc.5 AUTH_GATE_SECRET=$secret

    json=$(compose_json)
    if ! echo "$json" | jq -e --arg h "$host" '
        .services.dsh.environment.DSH_PUBLIC_URL == "https://\($h)/" and
        .services.dsh.environment.DSH_TRUSTED_HOST == $h and
        .services.caddy.environment.DSH_TRUSTED_HOST == $h' >/dev/null; then
        echo 'compose does not derive the site address from DSH_TRUSTED_HOST as it should' >&2
        return 1
    fi
    echo '   compose derives the address from DSH_TRUSTED_HOST'

    # Without compose.tunnel.yml there is nothing of the tunnel: no service,
    # no variable, no second network.
    if ! echo "$json" | jq -e '(.services | has("cloudflared") | not)
        and (.services.dsh.environment | has("TUNNEL_HOSTNAME") | not)
        and ((.networks | keys) == ["default"])' >/dev/null; then
        echo 'the base compose files mention the tunnel; without compose.tunnel.yml there must be nothing of it' >&2
        return 1
    fi
    echo '   without compose.tunnel.yml there is no tunnel service, variable or network'

    json=$(DSH_PUBLIC_URL=https://dsh.example.org/ compose_json)
    if [ "$(echo "$json" | jq -r '.services.dsh.environment.DSH_PUBLIC_URL')" != https://dsh.example.org/ ]; then
        echo 'compose ignores a DSH_PUBLIC_URL override' >&2
        return 1
    fi
    echo '   compose honours a DSH_PUBLIC_URL override'

    if (unset DSH_TRUSTED_HOST; compose_ok) >/dev/null 2>&1 \
        || (unset DSH_VERSION; compose_ok) >/dev/null 2>&1; then
        echo 'compose renders without DSH_TRUSTED_HOST or DSH_VERSION; both are required' >&2
        return 1
    fi
    echo '   compose refuses to render without DSH_TRUSTED_HOST or DSH_VERSION'

    # The caddy image comes from compose, so the check follows a version bump.
    caddy_image=$(echo "$json" | jq -r '.services.caddy.image')
    caddy_validate() {
        docker run --rm -v "$PWD/caddy/Caddyfile:/etc/caddy/Caddyfile:ro" "$@" \
            "$caddy_image" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
    }
    caddy_validate -e DSH_TRUSTED_HOST="$host" >/dev/null 2>&1 || { caddy_validate -e DSH_TRUSTED_HOST="$host"; return 1; }
    echo '   the Caddyfile validates with DSH_TRUSTED_HOST'

    if caddy_validate >/dev/null 2>&1; then
        echo 'the Caddyfile validates without DSH_TRUSTED_HOST' >&2
        return 1
    fi
    echo '   the Caddyfile does not validate without it'

    # Warnings from the adapter are defects here: it warns about a header_up
    # that repeats the reverse proxy's default, and the Caddyfile is written
    # so that it has none.
    n=$(caddy_validate -e DSH_TRUSTED_HOST="$host" 2>&1 | grep -c 'Unnecessary header_up' || true)
    if [ "$n" -gt 0 ]; then
        echo 'the Caddyfile has a header_up that repeats the reverse proxy default' >&2
        return 1
    fi
    echo '   the Caddyfile adapts without warnings'

    # The tunnel override, loaded by its name: a deployment loads it through
    # COMPOSE_FILE in .env, which --env-file would bypass.
    tunnel_json() { docker compose --env-file /dev/null -f compose.yml -f compose.tunnel.yml config --format json; }
    tunnel_ok()   { docker compose --env-file /dev/null -f compose.yml -f compose.tunnel.yml config -q; }
    export TUNNEL_TOKEN=fake-token TUNNEL_HOSTNAME=dsh.example.org

    json=$(tunnel_json)
    if ! echo "$json" | jq -e '
        (.services.caddy.networks | keys | sort) == ["default", "tunnel"]
        and (.services.cloudflared.networks | keys | sort) == ["tunnel"]
        and (.services["auth-gate"].networks | keys | sort) == ["default"]
        and (.services.dsh.networks | keys | sort) == ["default"]
        and (((.services.cloudflared.depends_on) // {}) | keys) == ["caddy"]
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

    routing "$caddy_image"
}

# What each Caddyfile listener tells the backends about the client. The real
# Caddyfile runs in the caddy image, with one stand-in for auth-gate and dsh
# that answers every request with the X-Forwarded-For it received; it refuses
# /auth/check with 401 (a missing session) unless the request carries
# "X-Probe: pass", which lets it through to the "dsh" answer. Nothing is
# published: the probes go to the container addresses.
#   :443, the private listener: the client address is the peer's, whatever the
#     client sent in X-Forwarded-For or Cf-Connecting-Ip.
#   :8080, the Cloudflare Tunnel listener: the client address is
#     Cf-Connecting-Ip, whatever the client sent in X-Forwarded-For.
routing() (
    local image=$1 p="dsh-server-check-$$" conf lan tun peer got i
    conf=$(mktemp)
    printf '%s\n' \
        ':8080, :3080 {' \
        '	@pass {' \
        '		path /auth/check' \
        '		header X-Probe pass' \
        '	}' \
        '	respond @pass "ok" 200' \
        '	respond /auth/check "xff={http.request.header.x-forwarded-for}" 401' \
        '	respond "xff={http.request.header.x-forwarded-for} host={http.request.host}" 200' \
        '}' > "$conf"
    trap 'docker rm -f "$p-front" "$p-echo" >/dev/null 2>&1; docker network rm "$p-lan" "$p-tun" >/dev/null 2>&1; rm -f "$conf"' EXIT
    docker network create "$p-lan" >/dev/null
    docker network create "$p-tun" >/dev/null
    docker run -d --name "$p-echo" --network "$p-lan" --network-alias auth-gate --network-alias dsh \
        -v "$conf:/etc/caddy/Caddyfile:ro" "$image" >/dev/null
    docker run -d --name "$p-front" --network "$p-lan" -v "$PWD/caddy/Caddyfile:/etc/caddy/Caddyfile:ro" \
        -e DSH_TRUSTED_HOST=dsh.test "$image" >/dev/null
    docker network connect "$p-tun" "$p-front"
    lan=$(docker inspect -f "{{with index .NetworkSettings.Networks \"$p-lan\"}}{{.IPAddress}}{{end}}" "$p-front")
    tun=$(docker inspect -f "{{with index .NetworkSettings.Networks \"$p-tun\"}}{{.IPAddress}}{{end}}" "$p-front")
    # The probes come from the host, so the peer the private listener sees is
    # the gateway of the stand-in network.
    peer=$(docker network inspect -f '{{(index .IPAM.Config 0).Gateway}}' "$p-lan")

    q() { curl -s --noproxy '*' --max-time 5 "$@"; }
    # expect <probe> <want> <got>
    expect() {
        if [ "$2" != "$3" ]; then
            echo "routing: $1: want '$2', got '$3'" >&2
            exit 1
        fi
    }
    forge=(-H 'X-Forwarded-For: 6.6.6.6' -H 'Cf-Connecting-Ip: 6.6.6.6')
    site=(--resolve "dsh.test:443:$lan")
    # Caddy needs a moment to issue its certificate before the first answer.
    i=0
    while :; do
        got=$(q -sk "${site[@]}" "${forge[@]}" https://dsh.test/auth/login || true)
        [ "$got" != "" ] && break
        i=$((i + 1))
        [ "$i" -lt 20 ] || { echo 'routing: the caddy container did not answer on :443 within 10 seconds' >&2; exit 1; }
        sleep 0.5
    done
    expect 'private listener, /auth/login' "xff=$peer host=dsh.test" "$got"
    got=$(q -sk "${site[@]}" "${forge[@]}" https://dsh.test/x || true)
    expect 'private listener, stopped by the gate' "xff=$peer" "$got"
    got=$(q -sk "${site[@]}" "${forge[@]}" -H 'X-Probe: pass' https://dsh.test/x || true)
    expect 'private listener, through to dsh' "xff=$peer host=dsh.test" "$got"
    got=$(q -o /dev/null -w '%{http_code} %{redirect_url}' -H 'Host: dsh.test' "http://$lan/x" || true)
    expect 'port 80 redirects to https' '308 https://dsh.test/x' "$got"
    echo '   private listener: the client address is the peer on every hop, whatever the client sent; port 80 still redirects'

    visitor=(-H 'Host: dsh.example.org' -H 'Cf-Connecting-Ip: 203.0.113.9' -H 'X-Forwarded-For: 1.1.1.1')
    got=$(q "${visitor[@]}" "http://$tun:8080/auth/login" || true)
    expect 'tunnel listener, /auth/login' 'xff=203.0.113.9 host=dsh.example.org' "$got"
    got=$(q "${visitor[@]}" "http://$tun:8080/x" || true)
    expect 'tunnel listener, stopped by the gate' 'xff=203.0.113.9' "$got"
    got=$(q "${visitor[@]}" -H 'X-Probe: pass' "http://$tun:8080/x" || true)
    expect 'tunnel listener, through to dsh' 'xff=203.0.113.9 host=dsh.example.org' "$got"
    got=$(q -H 'Host: dsh.example.org' -H 'X-Forwarded-For: 1.1.1.1' -H 'X-Probe: pass' "http://$tun:8080/x" || true)
    expect 'tunnel listener without Cf-Connecting-Ip' 'xff= host=dsh.example.org' "$got"
    echo '   tunnel listener: the client address is Cf-Connecting-Ip on every hop, whatever the client sent in X-Forwarded-For'
)

# image_labels_ok <image> : the OCI source label is present.
image_labels_ok() {
    docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.source"}}' "$1" \
        | grep -qx 'https://github.com/StanleyChanH/dsh-server'
}

build() {
    echo '== build: auth-gate image'
    docker build -q -t dsh-server-auth-gate:check --build-arg GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" auth-gate >/dev/null
    image_labels_ok dsh-server-auth-gate:check || { echo 'the auth-gate image lacks its OCI labels' >&2; return 1; }
    docker image rm dsh-server-auth-gate:check >/dev/null
    echo '   built; OCI labels present'
}

image() {
    echo '== image: dsh image (this takes a while)'
    local version
    version=$(sed -n 's/^DSH_VERSION=//p' .env.example)
    [ -n "$version" ] || { echo 'no DSH_VERSION in .env.example' >&2; return 1; }
    local args=(--build-arg DSH_VERSION="$version")
    if [ -n "${LAN_PROXY:-}" ]; then
        local no_proxy=${NO_PROXY:-localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16}
        args+=(--build-arg HTTP_PROXY="$LAN_PROXY" --build-arg HTTPS_PROXY="$LAN_PROXY"
            --build-arg http_proxy="$LAN_PROXY" --build-arg https_proxy="$LAN_PROXY"
            --build-arg NO_PROXY="$no_proxy" --build-arg no_proxy="$no_proxy")
    fi
    docker build -q -t dsh-server-dsh:check "${args[@]}" dsh >/dev/null
    docker run --rm dsh-server-dsh:check sh -c '
        dsh --version && pnpm --version && mnemon --version \
        && for c in git curl ssh sshpass xauth python3 chromium gh; do
            command -v "$c" >/dev/null 2>&1 || { echo "missing from the image: $c" >&2; exit 1; }
        done \
        && fc-match "sans-serif:lang=zh-cn" | grep -q "Noto Sans CJK" \
        && fc-match emoji | grep -q "Noto Color Emoji"' >/dev/null
    image_labels_ok dsh-server-dsh:check || { echo 'the dsh image lacks its OCI labels' >&2; return 1; }
    docker image rm dsh-server-dsh:check >/dev/null
    echo '   built; dsh, pnpm, mnemon, the tools and the fonts are present'
}

if [ "$#" -gt 0 ]; then
    groups=("$@")
else
    groups=(go shell dockerfile config build)
fi
# Every name is checked before the first group runs, so a typo does not leave
# a half-run behind.
for g in "${groups[@]}"; do
    case "$g" in
        go | shell | dockerfile | config | build | image) ;;
        *) echo "unknown check: $g (see the header of $0)" >&2; exit 2 ;;
    esac
done
for g in "${groups[@]}"; do
    "$g"
done
echo
echo "All checks passed: ${groups[*]}"
