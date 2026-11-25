#!/bin/bash
# Add a user to auth-gate, or change that user's password.
# The password is read once on the host without echo (it never appears on the
# command line or in shell history) and is piped to `/gate -adduser` inside the
# container, which bcrypts it and writes users.json.
# Running it again for the same name sets a new password. The service reloads
# the user table whenever the file's mtime changes, so nothing needs a restart.
# Works whether or not the auth-gate container is running: when it is not, the
# script falls back to a one-off container (see below).
set -euo pipefail

if [ "$#" -ne 1 ]; then
    echo "usage: $0 <username>" >&2
    exit 1
fi

cd "$(dirname "$0")/.."

printf 'Password for %s (input hidden): ' "$1" >&2
IFS= read -rs password
printf '\n' >&2

if [ -z "$password" ]; then
    echo "Empty password: nothing changed." >&2
    exit 1
fi

# -T: no TTY; the password travels over stdin.
# exec is preferred (the service container is running). When the container is
# not running - typically on a first deploy, while auth-gate is crash-looping
# because users.json does not exist yet - fall back to a one-off container of
# the same image running -adduser. It writes to the same ./auth-gate/data
# mount, so creating the first user before `docker compose up -d` breaks the
# deadlock.
if ! printf '%s\n' "$password" | docker compose exec -T auth-gate /gate -adduser "$1"; then
    printf '%s\n' "$password" | docker compose run --rm -T auth-gate -adduser "$1"
fi
