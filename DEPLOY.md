# Deploy CoreScope

Pre-built images are published to GHCR for `linux/amd64` and `linux/arm64` (Raspberry Pi 4/5).

## Quick Start

### Docker run

```bash
docker run -d --name corescope \
  -p 80:80 \
  -v corescope-data:/app/data \
  -e DISABLE_CADDY=true \
  ghcr.io/kpa-clawbot/corescope:latest
```

Open `http://localhost` — done.

### Docker Compose

```bash
curl -sL https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/docker-compose.example.yml \
  -o docker-compose.yml
docker compose up -d
```

## Image Tags

| Tag | Description |
|-----|-------------|
| `v3.4.1` | Pinned release (recommended for production) |
| `v3.4` | Latest patch in v3.4.x |
| `v3` | Latest minor+patch in v3.x |
| `latest` | Latest release tag |
| `edge` | Built from master — unstable, for testing |

## Configuration

Settings can be overridden via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `DISABLE_CADDY` | `false` | Skip internal Caddy (set `true` behind a reverse proxy) |
| `DISABLE_MOSQUITTO` | `false` | Skip internal MQTT broker (use external) |
| `HTTP_PORT` | `80` | Host port mapping |
| `DATA_DIR` | `./data` | Host path for persistent data |

For advanced configuration, mount a `config.json` into `/app/data/config.json`. See `config.example.json` in the repo.

## Updating

```bash
docker compose pull
docker compose up -d
```

## Data

All persistent data lives in `/app/data`:
- `meshcore.db` — SQLite database (packets, nodes)
- `config.json` — custom config (optional)
- `theme.json` — custom theme (optional)

**Backup:** `cp data/meshcore.db ~/backup/`

## TLS

Option A — **External reverse proxy** (recommended): Run with `DISABLE_CADDY=true`, put nginx/traefik/Cloudflare in front.

Option B — **Built-in Caddy**: Mount a custom Caddyfile at `/etc/caddy/Caddyfile` and expose ports 80+443.
