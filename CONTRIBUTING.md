# Contributing

Thank you for helping with dsh-server. Bug reports, documentation fixes and code
contributions are all welcome. This page explains how the project is built and checked,
and what a pull request should look like.

## Ways to help

- **Report a bug** with the [bug report template](https://github.com/StanleyChanH/dsh-server/issues/new?template=bug_report.yml).
- **Suggest an improvement** with the [feature request template](https://github.com/StanleyChanH/dsh-server/issues/new?template=feature_request.yml).
- **Fix the documentation.** If something in the README or the runbook was unclear or
  wrong for you, that is a bug worth fixing, and a small pull request is the fastest way.
- **Ask a question** with the [question form](https://github.com/StanleyChanH/dsh-server/issues/new?template=question.yml); add as much context as you can.

Two things do not belong in a public issue: a vulnerability (see
[SECURITY.md](SECURITY.md)) and a problem with dsh itself (that belongs in the
[upstream repository](https://github.com/deepseek-ai/deepseek-harness)).

> [!CAUTION]
> Never paste tokens, session cookies, passwords or `.env` contents into an issue or a
> pull request. The start-up URL that dsh prints and the `docker compose logs dsh` output
> contain the dsh token.

## Development setup

You need `git`, Docker with Compose v2, and `jq`. Nothing else: Go and Node run in containers,
and there is nothing to install on the host.

The repository is also a deployment. If you have a stack running from your checkout,
keep in mind that `docker compose` commands and the scripts act on that stack.

## Checks

Continuous integration runs the checks below on every push and pull request. They are
the same commands on your machine:

```sh
scripts/check.sh                 # everything
scripts/check.sh go shell        # only some groups
```

The groups are:

| Group | What it runs |
| --- | --- |
| `go` | `gofmt -l`, `go vet`, `go test` for auth-gate, in `golang:1.24-alpine` |
| `shell` | ShellCheck on `scripts/*.sh` and `dsh/entrypoint.sh` |
| `dockerfile` | hadolint on both Dockerfiles, with `.hadolint.yaml` |
| `config` | compose renders with the template values, the required variables are really required, and the Caddyfile validates with `DSH_TRUSTED_HOST` and fails without it |
| `build` | the auth-gate image builds (never with `--pull`) |

CI additionally runs `go test -race` on a host with a C toolchain.

Two environment details:

- On a host where `proxy.golang.org` is slow or blocked (for example in mainland China),
  set `GOPROXY=https://goproxy.cn,direct` in your shell before running `scripts/check.sh`;
  the script passes it into the Go container.
- The `build` and `image` groups run `docker build` for real. If your network needs a
  proxy for the `RUN` steps, export `LAN_PROXY` (and `NO_PROXY`) first: the `image` group
  passes them into the dsh build the way `docker compose build` does.

If you have Go 1.24 or newer installed, the Go checks also run directly:

```sh
cd auth-gate
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
```

### Trying a change on a real stack

The checks above are static. A change to auth-gate, the Caddyfile, compose or the scripts
is only proven by deploying it and running the smoke test, which exercises the whole chain
including one dsh restart:

```sh
scripts/smoke.sh
```

Do that on a test host or a virtual machine, not on a stack other people depend on, and
put the tail of its output in your pull request.

## Pull requests

1. Fork the repository and create a branch.
2. Make the change and run `scripts/check.sh`. If behaviour changed, run the smoke test on
   a real stack as well.
3. Update the documentation where it describes what you changed. `README.md` and
   `README.zh-CN.md` are kept equivalent, and so are `docs/operations.md` and
   `docs/operations.zh-CN.md`.
4. Open the pull request; the template asks what changed, why, and how you verified it.

Small, focused pull requests are easier to review than large ones. If you plan a bigger
change, open an issue first to talk about the approach.

### Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org/): a type, an
optional scope, a subject, and, when it helps, a body that explains why.

```
feat(auth-gate): remember the device for thirty days
fix(deploy): recreate caddy when DSH_TRUSTED_HOST changes
docs: correct the backup command
```

The types in use are `feat`, `fix`, `docs`, `build`, `ci`, `test`, `refactor` and `chore`;
the usual scopes are `deploy`, `auth-gate`, `dsh` and `smoke`. The first line stays under
about 72 characters and is written in the imperative.

### Language

English is the language of code comments, documentation and commit messages. The login
page and the messages auth-gate sends to browsers are currently in Chinese; they will be
reworked together with the planned login-page redesign.

## Version pins

- **dsh** (`DSH_VERSION` in `.env.example`): bump it only to a version you have run the
  smoke test with, and say so in the commit. Upgrades are deliberate, one version at a
  time.
- **Go**: the build image is `golang:1.24-alpine`. A dependency bump that needs a newer Go
  has to bump that image in the same change.
- **pnpm, gh, mnemon** in `dsh/Dockerfile`: pinned on purpose. For gh, change `GH_VERSION`
  and the sha256 together.

## License

By contributing you agree that your contributions are licensed under the repository's
[MIT License](LICENSE).
