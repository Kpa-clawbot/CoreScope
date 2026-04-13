FROM golang:1.22-alpine AS builder

RUN apk add --no-cache build-base

ARG APP_VERSION=unknown
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

# Build server
WORKDIR /build/server
COPY cmd/server/go.mod cmd/server/go.sum ./
COPY internal/geofilter/ ../../internal/geofilter/
COPY internal/sigvalidate/ ../../internal/sigvalidate/
RUN go mod download
COPY cmd/server/ ./
RUN go build -ldflags "-X main.Version=${APP_VERSION} -X main.Commit=${GIT_COMMIT} -X main.BuildTime=${BUILD_TIME}" -o /corescope-server .

# Build ingestor
WORKDIR /build/ingestor
COPY cmd/ingestor/go.mod cmd/ingestor/go.sum ./
COPY internal/geofilter/ ../../internal/geofilter/
COPY internal/sigvalidate/ ../../internal/sigvalidate/
RUN go mod download
COPY cmd/ingestor/ ./
RUN go build -o /corescope-ingestor .

# Build decrypt CLI
WORKDIR /build/decrypt
COPY cmd/decrypt/go.mod cmd/decrypt/go.sum ./
COPY internal/channel/ ../../internal/channel/
RUN go mod download
COPY cmd/decrypt/ ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /corescope-decrypt .

# Runtime image
FROM alpine:3.20

RUN apk add --no-cache mosquitto mosquitto-clients supervisor caddy wget

WORKDIR /app

# Go binaries
COPY --from=builder /corescope-server /corescope-ingestor /corescope-decrypt /app/

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
RUN chmod +x /entrypoint.sh

EXPOSE 80 443 1883

VOLUME ["/app/data", "/data/caddy"]

ENTRYPOINT ["/entrypoint.sh"]
