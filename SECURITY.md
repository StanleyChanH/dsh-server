# Security policy

## Supported versions

dsh-server is developed on a single branch and has no tagged releases yet. Security fixes
land on `master` and apply to the latest commit there; nothing else is supported.

## Scope

This project is a deployment stack: a login gate (auth-gate), a reverse proxy
configuration and a container image around the [dsh](https://github.com/deepseek-ai/deepseek-harness)
agent. It is built for a **private network** and is not hardened for direct exposure to the
public Internet (see the security model in the [README](README.md#security-model)). The one
supported public entrance is a [Cloudflare Tunnel](docs/operations.md#cloudflare-tunnel) with
Cloudflare Access in front of the public hostname; do not forward a port to the stack instead.

In scope:

- auth-gate: authentication, session cookies, the token file it hands out, request
  handling.
- The stack configuration: `compose.yml`, `compose.tunnel.yml`, `caddy/Caddyfile`,
  `dsh/entrypoint.sh`, the scripts, and what the images put inside a container.

Out of scope, but please tell us if the stack makes it worse:

- Vulnerabilities in dsh itself: report them to the
  [upstream project](https://github.com/deepseek-ai/deepseek-harness).
- Vulnerabilities in Caddy, Docker, Go, Node.js or Debian packages: report them to the
  respective projects. A report that dsh-server should bump a pinned version is welcome.

## Reporting a vulnerability

Please report privately through GitHub's vulnerability reporting: open the repository's
**Security** tab and choose **Report a vulnerability**
([direct link](https://github.com/StanleyChanH/dsh-server/security/advisories/new)). Do
not open a public issue.

Helpful things to include:

- what the problem is, and what an attacker could do with it;
- the version or commit you tested, and how the stack was reached (LAN, VPN, Cloudflare
  Tunnel, ...);
- steps or a proof of concept. A proof of concept that only reads a file or prints a
  marker is enough.

The project is maintained by one person in their spare time, so responses are on a best
effort basis. Coordinated disclosure is appreciated: please give the maintainer time to
fix the problem before publishing it.

## Hardening notes

- Keep ports 80 and 443 off the public Internet. Put a VPN in front if members are far
  away, or use the Cloudflare Tunnel with Cloudflare Access in front of the public
  hostname ([runbook](docs/operations.md#cloudflare-tunnel)).
- With the tunnel, Cloudflare is in the path of everything members send and receive, the
  dsh token URL and the session cookies included.
- `AUTH_GATE_SECRET`, `TUNNEL_TOKEN`, the dsh token and everything under `dsh-home/` are
  credentials. Treat logs and backups that contain them accordingly.
- Rotating `AUTH_GATE_SECRET` invalidates every login session. A dsh restart rotates the
  dsh token.
