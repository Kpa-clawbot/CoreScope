# syntax=docker/dockerfile:1
# Build stage always runs natively on the builder's arch ($BUILDPLATFORM) and
# cross-compiles to $TARGETOS/$TARGETARCH. No QEMU for compilation.
#
# All four binaries support native SQLite and PostgreSQL. SQLite requires cgo;
# the upstream Zig cross-compiler targets musl so both release architectures
# remain fully static and independent of the runtime image libc.
#
# BUILDPLATFORM is auto-set by buildx; default to linux/amd64 so plain
# `docker build` (without buildx) doesn't fail on an empty platform string.
ARG BUILDPLATFORM=linux/amd64
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder

ARG APP_VERSION=unknown
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
# Provided by buildx for multi-arch builds
ARG TARGETOS
ARG TARGETARCH

# Keep these in step with the Makefile. netgo/osusergo retain Go DNS/user
# lookup; sqlite_omit_load_extension permits static linking.
ENV GO_BUILD_TAGS=netgo,osusergo,sqlite_omit_load_extension \
    ZIG_GLOBAL_CACHE_DIR=/tmp/zig-cache

# Pinned zig, checksum-verified. Arch comes from `uname -m` rather than
# TARGETARCH because this stage is pinned to BUILDPLATFORM — it has to work when
# someone builds on an arm64 machine too.
ARG ZIG_VERSION=0.16.0
RUN apk add --no-cache curl xz && \
    case "$(uname -m)" in \
      x86_64)  ZA=x86_64;  ZSHA=70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00 ;; \
      aarch64) ZA=aarch64; ZSHA=ea4b09bfb22ec6f6c6ceac57ab63efb6b46e17ab08d21f69f3a48b38e1534f17 ;; \
      *) echo "unsupported builder arch $(uname -m)" >&2; exit 1 ;; \
    esac && \
    curl -sSLo /tmp/zig.tar.xz "https://ziglang.org/download/${ZIG_VERSION}/zig-${ZA}-linux-${ZIG_VERSION}.tar.xz" && \
    echo "${ZSHA}  /tmp/zig.tar.xz" | sha256sum -c - && \
    mkdir -p /opt/zig && tar -xJf /tmp/zig.tar.xz -C /opt/zig --strip-components=1 && \
    ln -s /opt/zig/zig /usr/local/bin/zig && rm /tmp/zig.tar.xz && \
    zig version

# zigcc selects the native SQLite C target from the requested image architecture.
RUN printf '%s\n' '#!/bin/sh' \
    'case "$TARGETARCH" in' \
    '  amd64) t=x86_64-linux-musl ;;' \
    '  arm64) t=aarch64-linux-musl ;;' \
    '  *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;;' \
    'esac' \
    'exec zig cc -target "$t" "$@"' > /usr/local/bin/zigcc && chmod +x /usr/local/bin/zigcc
ENV CC=zigcc CGO_ENABLED=1

# Build server
WORKDIR /build/server
COPY cmd/server/go.mod cmd/server/go.sum ./
COPY internal/geofilter/ ../../internal/geofilter/
COPY internal/sigvalidate/ ../../internal/sigvalidate/
COPY internal/packetpath/ ../../internal/packetpath/
COPY internal/dbconfig/ ../../internal/dbconfig/
COPY internal/dbschema/ ../../internal/dbschema/
COPY internal/prunequeue/ ../../internal/prunequeue/
COPY internal/perfio/ ../../internal/perfio/
COPY internal/mbcapqueue/ ../../internal/mbcapqueue/
COPY internal/lora/ ../../internal/lora/
COPY internal/users/ ../../internal/users/
COPY internal/channel/ ../../internal/channel/
COPY internal/mailer/ ../../internal/mailer/
COPY internal/pgutil/ ../../internal/pgutil/
COPY internal/sqliteutil/ ../../internal/sqliteutil/
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/zig-cache \
    go mod download
COPY cmd/server/ ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/zig-cache \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -tags ${GO_BUILD_TAGS} \
    -ldflags "-s -w -extldflags '-static -Wl,-s' -X main.Version=${APP_VERSION} -X main.Commit=${GIT_COMMIT} -X main.BuildTime=${BUILD_TIME}" \
    -o /corescope-server .

# Build ingestor
WORKDIR /build/ingestor
COPY cmd/ingestor/go.mod cmd/ingestor/go.sum ./
COPY internal/channel/ ../../internal/channel/
COPY internal/geofilter/ ../../internal/geofilter/
COPY internal/sigvalidate/ ../../internal/sigvalidate/
COPY internal/packetpath/ ../../internal/packetpath/
COPY internal/dbconfig/ ../../internal/dbconfig/
COPY internal/dbschema/ ../../internal/dbschema/
COPY internal/prunequeue/ ../../internal/prunequeue/
COPY internal/perfio/ ../../internal/perfio/
COPY internal/mbcapqueue/ ../../internal/mbcapqueue/
COPY internal/pgutil/ ../../internal/pgutil/
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/zig-cache \
    go mod download
COPY cmd/ingestor/ ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/zig-cache \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -tags ${GO_BUILD_TAGS} \
    -ldflags "-s -w -extldflags '-static -Wl,-s'" \
    -o /corescope-ingestor .

# Build decrypt CLI
WORKDIR /build/decrypt
COPY cmd/decrypt/go.mod cmd/decrypt/go.sum ./
COPY internal/channel/ ../../internal/channel/
COPY internal/dbschema/ ../../internal/dbschema/
COPY internal/pgutil/ ../../internal/pgutil/
COPY internal/dbconfig/ ../../internal/dbconfig/
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/zig-cache \
    go mod download
COPY cmd/decrypt/ ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/zig-cache \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -tags ${GO_BUILD_TAGS} \
    -ldflags "-s -w -extldflags '-static -Wl,-s' -X main.version=${APP_VERSION}" \
    -o /corescope-decrypt .

# Offline bootstrap and verified conversion CLI for both storage engines.
WORKDIR /build/migrate
COPY cmd/migrate/go.mod cmd/migrate/go.sum ./
COPY internal/packetpath/ ../../internal/packetpath/
COPY internal/dbschema/ ../../internal/dbschema/
COPY internal/users/ ../../internal/users/
COPY internal/pgutil/ ../../internal/pgutil/
COPY internal/sqliteutil/ ../../internal/sqliteutil/
COPY internal/dbconfig/ ../../internal/dbconfig/
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd/migrate/ ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/tmp/zig-cache \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -tags ${GO_BUILD_TAGS} \
    -ldflags "-s -w -extldflags '-static -Wl,-s'" \
    -o /corescope-migrate .

# The client major matches PostgreSQL 18 for native backup/restore support.
FROM alpine:3.24

RUN apk add --no-cache mosquitto mosquitto-clients supervisor caddy wget postgresql18-client sqlite jq

WORKDIR /app

# Go binaries (statically linked; they do not use this image's libc)
COPY --from=builder /corescope-server /corescope-ingestor /corescope-decrypt /corescope-migrate /app/

# Frontend assets + config
COPY public/ ./public/
COPY config.example.json channel-rainbow.json ./

# Bake git commit SHA — manage.sh and CI write .git-commit before build
# Default to "unknown" if not provided
RUN echo "unknown" > .git-commit

# Supervisor + Mosquitto + Caddy config
COPY docker/supervisord-go.conf /etc/supervisor/conf.d/supervisord.conf
COPY docker/supervisord-go-no-mosquitto.conf /etc/supervisor/conf.d/supervisord-no-mosquitto.conf
COPY docker/supervisord-go-no-caddy.conf /etc/supervisor/conf.d/supervisord-no-caddy.conf
COPY docker/supervisord-go-no-mosquitto-no-caddy.conf /etc/supervisor/conf.d/supervisord-no-mosquitto-no-caddy.conf
COPY docker/mosquitto.conf /etc/mosquitto/mosquitto.conf
COPY docker/Caddyfile /etc/caddy/Caddyfile

# Data directory
RUN mkdir -p /app/data /var/lib/mosquitto /data/caddy && \
    chown -R mosquitto:mosquitto /var/lib/mosquitto

# Entrypoint
COPY docker/entrypoint-go.sh /entrypoint.sh
COPY docker/storage.sh docker/postgres-bootstrap.sh docker/postgres-grants.sql /app/
RUN chmod +x /entrypoint.sh /app/storage.sh /app/postgres-bootstrap.sh

EXPOSE 80 443 1883

VOLUME ["/app/data", "/data/caddy"]

ENTRYPOINT ["/entrypoint.sh"]
