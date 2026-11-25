#!/bin/sh
# Sync dsh's token into auth-gate's token file (auth-gate/data/dsh-token on
# the host, /data/dsh-token in the container). auth-gate uses it to answer
# "past the login gate, but 401 from dsh" requests with a 302 to
# /?token=<current>, which makes the gate account the only credential members
# need (see "Automatic sign-in on 401" in docs/design.md).
#
# Parsing reuses scripts/dsh-url.sh (the single source of truth: if a dsh
# release changes the log format there is one place to fix). This script does
# three things: fetch the URL, strip the token and check its shape, and write
# it atomically.
# Fail-safe: if parsing, validation or the ownership step fails, it exits 1
# and leaves the existing file alone. Better no automatic sign-in than a
# corrupted token (auth-gate then shows the sync page, and the manual
# dsh-url.sh path still works).
#
# Installation: /etc/cron.d/dsh-server-token runs this script every minute as
# root (cron has no TTY: silent on success, stderr on failure). To rebuild on
# another host, copy these three lines and adjust the path:
#     SHELL=/bin/sh
#     PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
#     * * * * * root /path/to/dsh-server/scripts/sync-token.sh
# Two things have to hold for that job to work: cron has to be running, and
# root has to be able to run `docker compose` (a Compose plugin installed for
# the whole system, not only for one user). Check both by running the script
# once as root: it exits 0 and the token file gets a new timestamp.
# Note: the token file's content and this script's error output are secrets.
set -eu
umask 077  # temp files are born 0600: auth-gate/data is traversable by others, and the token must never be exposed, even for a moment

cd "$(dirname "$0")/.."

# auth-gate runs as user 1000:1000 (compose.yml user:); it cannot read the
# file unless it owns it.
owner_uid=1000
owner_gid=1000
token_file=auth-gate/data/dsh-token

# 1) The most recent start-up URL (dsh rotates the token on every process
#    restart; tail keeps the latest one). When dsh-url.sh cannot find one
#    (container down, logs rotated) it exits 1 by itself and this script stops
#    here.
url=$(scripts/dsh-url.sh)

# 2) Strip the token and check its shape. In the dsh 0.2.2-rc.5 logs the token
#    is a 43-character base64url string (letters, digits, - and _). The length
#    is not pinned to 43: a lower bound of 20 plus the character whitelist is
#    enough to catch "grabbed the wrong column / grabbed another link" parse
#    errors, and it tolerates small changes on the dsh side.
token=${url##*token=}
if ! printf '%s' "$token" | grep -Eq '^[A-Za-z0-9_-]{20,}$'; then
    # The error never prints the token, not even fragments: this output ends
    # up in root's mail/syslog through cron, and the extracted string may
    # embed a real token (when the URL has a &query after the token, that tail
    # is extracted along with it). Length plus character set is enough to tell
    # "wrong column / trailing junk" from "too short".
    extra=$(printf '%s' "$token" | tr -d 'A-Za-z0-9_-' | wc -c | tr -d ' ')
    if [ "$extra" -gt 0 ]; then
        shape="$extra character(s) outside [A-Za-z0-9_-]"
    else
        shape="valid character set but shorter than 20"
    fi
    echo "error: the token has an unexpected shape (length ${#token}, $shape); the log format has probably changed. Existing file left untouched." >&2
    echo "Investigate: docker compose logs dsh | grep token= (and fix scripts/dsh-url.sh to match)" >&2
    exit 1
fi

# 3) Atomic write: the temporary file is written in the same directory, its
#    owner and mode are fixed, and only then does a rename replace the old
#    file (auth-gate's readers see either the complete old file or the
#    complete new one). chown and chmod must happen before the rename: doing
#    them afterwards would leave a moment where the file is root-owned 0600,
#    which auth-gate (uid 1000) cannot read. If chown fails (not root, or the
#    owner is someone else) the script exits before the rename, and the old
#    file is untouched.
tmp="$token_file.tmp.$$"
trap 'rm -f "$tmp"' EXIT INT TERM
printf '%s\n' "$token" > "$tmp"
chown "$owner_uid:$owner_gid" "$tmp"
chmod 600 "$tmp"
mv -f "$tmp" "$token_file"
trap - EXIT INT TERM
