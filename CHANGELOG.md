# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Initial public revision of the stack: caddy, auth-gate and dsh under Docker Compose,
  with a login per member, automatic sign-in on dsh's 401, HTTPS from an internal CA, and
  the end-to-end smoke test (`scripts/smoke.sh`).
- Documentation in English and Chinese: a short README and a full operations runbook, plus
  the design record.
- Continuous integration: Go format, vet and race tests; ShellCheck; hadolint; Compose and
  Caddyfile validation; image builds.
- A documented update procedure (recreate containers, never only restart them), and a
  smoke test that refuses a stack whose token is not being synced.
- `scripts/check.sh` runs the same checks locally, in containers.
- Community files: MIT license, contributing guide, security policy, code of conduct,
  issue and pull request templates.

### Changed

- The site address is configured once, in `DSH_TRUSTED_HOST`, and read by the Caddyfile
  (through an environment placeholder), compose, the dsh entrypoint and the scripts. None
  of them carries a default address any more.
- `DSH_VERSION` and `DSH_TRUSTED_HOST` are required in `.env`; compose refuses to render
  without them instead of falling back to a stale default.
- `scripts/smoke.sh` takes its default target from the deployed configuration and resolves
  containers through Compose, so it works for any project or directory name.
- pnpm is pinned in the dsh image, the dsh version has no silent default in the
  Dockerfile, and both images carry OCI labels.
