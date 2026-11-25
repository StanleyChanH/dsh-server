#!/bin/sh
# Print dsh's current start-up URL (it contains the token), taken from the
# most recent matching line of `docker compose logs dsh`. dsh rotates the token
# on every process restart (the old URL stops working), so run this after a
# restart to get the new one.
#
# The output is a secret (it is dsh's login credential): give it only to the
# person who needs it, and never paste it into chats, tickets or logs.
set -eu

cd "$(dirname "$0")/.."

# Only lines with token= match (other https links in the logs are skipped);
# tail keeps the most recent start.
url=$(docker compose logs --no-color dsh 2>/dev/null \
    | grep -o 'https://[^[:space:]]*token=[^[:space:]]*' | tail -n 1)

if [ -n "$url" ]; then
    printf '%s\n' "$url"
else
    echo "error: no start-up URL in the dsh logs yet (container not up, or logs rotated)." >&2
    echo "Check: docker compose ps dsh && docker compose logs dsh" >&2
    exit 1
fi
