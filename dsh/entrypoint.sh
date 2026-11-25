#!/bin/sh
# dsh web refuses wildcard binds (--host 0.0.0.0 is rejected) and insists on
# the container's own IP. Docker assigns that IP at start time, so it is
# resolved here and cannot be baked into the image. `hostname -i` may list
# several addresses; awk keeps the first.
IP=$(hostname -i | awk '{print $1}')

# With arguments, run them as given. This is how maintenance commands such as
# `docker run <image> node --version` work. Only a start without arguments
# launches dsh web.
if [ "$#" -gt 0 ]; then
  exec "$@"
fi

# The site address is required (compose.yml passes DSH_TRUSTED_HOST in). The
# public URL that dsh advertises defaults to https://<that host>/.
: "${DSH_TRUSTED_HOST:?DSH_TRUSTED_HOST is not set (the IP or hostname members use to reach this server, no scheme or port)}"
: "${DSH_PUBLIC_URL:=https://${DSH_TRUSTED_HOST}/}"

# Re-link a CLI that lives on the persistent volume /data/dsh/opt (maintained
# by the team) into /usr/local, which is part of the rootfs and therefore wiped
# on every rebuild. /root is persisted by the dsh-root bind mount (compose
# volumes), so this is the last remaining /usr/local duty: idempotent, and
# skipped when the directory is absent.
# gh is deliberately not re-linked here: it is baked into the image, and a
# /usr/local/bin/gh symlink would shadow the pinned binary.
if [ -d /data/dsh/opt/mnemon-cli/@mnemon-dev/mnemon ]; then
  mkdir -p /usr/local/lib/node_modules/@mnemon-dev
  ln -sfn /data/dsh/opt/mnemon-cli/@mnemon-dev/mnemon /usr/local/lib/node_modules/@mnemon-dev/mnemon
  ln -sf ../lib/node_modules/@mnemon-dev/mnemon/bin/mnemon.js /usr/local/bin/mnemon
fi

exec dsh web --no-open \
  --host "$IP" \
  --port 3080 \
  --public-url "$DSH_PUBLIC_URL" \
  --trusted-host "$DSH_TRUSTED_HOST"
