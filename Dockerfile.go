FROM golang:1.22-alpine AS builder

RUN apk add --no-cache build-base

WORKDIR /build

# Build server
COPY cmd/server/ ./cmd/server/
RUN cd cmd/server && go build -o /meshcore-server .

# Build ingestor
COPY cmd/ingestor/ ./cmd/ingestor/
RUN cd cmd/ingestor && go build -o /meshcore-ingestor .

# Runtime image
FROM alpine:3.20

RUN apk add --no-cache mosquitto mosquitto-clients supervisor caddy wget

WORKDIR /app

# Go binaries
COPY --from=builder /meshcore-server /meshcore-ingestor /app/

# Frontend assets + config
COPY public/ ./public/
COPY config.example.json channel-rainbow.json ./

# Supervisor + Mosquitto + Caddy config
COPY docker/supervisord-go.conf /etc/supervisor/conf.d/supervisord.conf
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
