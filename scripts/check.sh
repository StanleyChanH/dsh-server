#!/usr/bin/env bash
# The checks CI runs, on your machine. Needs docker; nothing else has to be
# installed.
#
# Usage: scripts/check.sh [group ...]
#   go          gofmt, go vet and go test for auth-gate, inside its build image
#   shell       ShellCheck on scripts/*.sh and dsh/entrypoint.sh
#   dockerfile  hadolint on both Dockerfiles, with .hadolint.yaml
#   config      compose renders, the required variables are really required,
#               and the Caddyfile validates only with DSH_TRUSTED_HOST
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
    echo '== shell: ShellCheck'
    docker run --rm -v "$PWD:/mnt:ro" koalaman/shellcheck:v0.11.0 -x scripts/*.sh dsh/entrypoint.sh
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
    local host=192.0.2.10 secret json caddy_image
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
}

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
