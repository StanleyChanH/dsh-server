# dsh web console image: Node 24 with a globally installed, version-pinned
# @deepseek-ai/dsh. The version never floats.
FROM node:24-slim

# git: dsh sessions clone repositories. curl and ca-certificates: the
# healthcheck and HTTPS fetches.
# openssh-client, sshpass, xauth and python3 are deterministic dependencies
# that used to be apt-installed by hand in the running container, where they
# vanished on every rebuild. Baked in, so a rebuild needs no network
# re-installation.
# --no-install-recommends skips recommended packages; the apt lists are removed
# in the same layer so they do not end up in the image.
RUN apt-get update \
    && apt-get install -y --no-install-recommends git curl ca-certificates openssh-client sshpass xauth python3 \
    && rm -rf /var/lib/apt/lists/*

# chromium: headless browsing and screenshots for the agent. The CJK and emoji
# fonts prevent missing-glyph boxes; fonts-liberation is listed explicitly to
# stand in for the Arial/Times faces that are common on the web. This layer
# sits before the npm layer so that a dsh upgrade does not reinstall it.
RUN apt-get update \
    && apt-get install -y --no-install-recommends chromium fonts-noto-cjk fonts-noto-color-emoji fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

# gh: Debian 12 ships only 2.23.0 (too old), so take a pinned release from
# GitHub instead. The sha256 guards against a substituted download (the link
# may run through a proxy). To upgrade, change GH_VERSION and the checksum
# (the linux_amd64.deb line of gh_<version>_checksums.txt in the release);
# see docs/operations.md.
ARG GH_VERSION=2.105.0
RUN curl -fsSL -o /tmp/gh.deb \
        "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.deb" \
    && echo "1dac939af3e84fb3861927c984b357531341be9403b889e4aa86e8822145a09d  /tmp/gh.deb" > /tmp/gh.sha256 \
    && sha256sum -c /tmp/gh.sha256 \
    && apt-get update \
    && apt-get install -y --no-install-recommends /tmp/gh.deb \
    && rm -rf /var/lib/apt/lists/* /tmp/gh.deb /tmp/gh.sha256

# dsh version to install. compose.yml passes it from .env. There is
# deliberately no default: a build without the argument fails here instead of
# quietly installing whatever is newest.
ARG DSH_VERSION
# dsh itself, plus two tools dsh sessions rely on: pnpm (the plugin manager
# needs it; it used to be installed at run time) and the mnemon CLI (the
# Mnemon Native Provider of the dsh-mnemon plugin reports
# "commandFound: false" when the binary is not on PATH). All three are pinned.
# The npm cache is cleaned afterwards (--force: npm refuses to clean a
# read-only cache directory without it).
RUN test -n "$DSH_VERSION" \
    && npm install -g @deepseek-ai/dsh@${DSH_VERSION} pnpm@12.23.0 @mnemon-dev/mnemon@0.2.14 \
    && npm cache clean --force

# Where dsh keeps its state (compose mounts a volume here). The workspace is
# the default working directory of sessions.
ENV DSH_HOME=/data/dsh
WORKDIR /workspace

# --chmod: under BuildKit this is chmod +x without an extra layer.
COPY --chmod=755 entrypoint.sh /entrypoint.sh

# exec form, plus `exec dsh` inside the script: dsh replaces the shell as PID
# 1, so the signal from `docker stop` reaches it directly.
ENTRYPOINT ["/entrypoint.sh"]

# OCI labels. Kept last so that editing them never invalidates a layer cache.
LABEL org.opencontainers.image.title="dsh-server dsh" \
      org.opencontainers.image.description="DeepSeek Harness (dsh) web console with git, chromium, gh, pnpm and mnemon" \
      org.opencontainers.image.source="https://github.com/StanleyChanH/dsh-server" \
      org.opencontainers.image.licenses="MIT"
