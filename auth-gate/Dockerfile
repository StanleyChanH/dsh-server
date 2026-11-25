# Build stage: a static binary (CGO is off, because the runtime stage is
# distroless and has no libc). GOPROXY can be overridden through compose.yml
# (.env); the default mirror is fast from mainland China.
FROM golang:1.24-alpine AS build
ARG GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0 GOPROXY=${GOPROXY}
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -o /gate .

# Runtime stage: distroless static - no shell, no package manager, a minimal
# image surface. The binary is the entrypoint; subcommands (-adduser, -ping)
# follow the image name in `docker run`.
# The tag is deliberately `latest`: distroless publishes it as the stable alias
# of the static image, and build hosts that cannot reach gcr.io pull it through
# a mirror and tag it locally under exactly that name (docs/operations.md).
# hadolint ignore=DL3007
FROM gcr.io/distroless/static:latest
ENV AUTH_USERS=/data/users.json
WORKDIR /
COPY --from=build /gate /gate
ENTRYPOINT ["/gate"]

# OCI labels. Kept last so that editing them never invalidates a layer cache.
LABEL org.opencontainers.image.title="dsh-server auth-gate" \
      org.opencontainers.image.description="Login gate for the dsh-server stack: bcrypt accounts, signed session cookies, login rate limiting" \
      org.opencontainers.image.source="https://github.com/StanleyChanH/dsh-server" \
      org.opencontainers.image.licenses="MIT"
