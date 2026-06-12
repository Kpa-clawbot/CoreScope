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

---

## Migrating from manage.sh (existing admins)

If you're currently deploying with `manage.sh` (git clone + local build), you have two options going forward:

### Option A: Keep using manage.sh (no changes needed)

`manage.sh update` continues to work exactly as before — it fetches the latest tag, builds locally, and restarts. Nothing breaks.

```bash
./manage.sh update          # latest release
./manage.sh update v3.5.0   # specific version
```

### Option B: Switch to pre-built images (recommended)

Pre-built images skip the build step entirely — faster updates, no Go toolchain needed.

**One-time migration:**

1. Stop the current deployment:
   ```bash
   ./manage.sh stop
   ```

2. Your data is in `~/meshcore-data/` (or whatever `PROD_DATA_DIR` is set to). It's untouched — the database, config, and theme files persist.

3. Copy `docker-compose.example.yml` to where you want to run from:
   ```bash
   cp docker-compose.example.yml ~/docker-compose.yml
   ```

4. Start with the pre-built image:
   ```bash
   cd ~ && docker compose up -d
   ```

5. Verify it picked up your existing data:
   ```bash
   curl http://localhost/api/stats
   ```

**Updates after migration:**
```bash
docker compose pull && docker compose up -d
```

### What about manage.sh features?

| manage.sh command | Pre-built equivalent |
|---|---|
| `./manage.sh update` | `docker compose pull && docker compose up -d` |
| `./manage.sh stop` | `docker compose down` |
| `./manage.sh start` | `docker compose up -d` |
| `./manage.sh logs` | `docker compose logs -f` |
| `./manage.sh status` | `docker compose ps` |
| `./manage.sh setup` | Copy `docker-compose.example.yml`, edit env vars |

`manage.sh` remains available for advanced use cases (building from source, custom patches, development). Pre-built images are recommended for most production deployments.

## Staging VM — disk-usage monitor & cleanup (#1684)

The staging VM ran out of disk during a hot-patch (#1684). To prevent
repeats, two scripts live in `scripts/staging/`:

- `disk-monitor.sh <mount>` — reads `df -P`, classifies usage against
  `<80 ok / >=80 warn / >=90 error / >=95 alert`, emits to stderr +
  journald (via `logger`). Returns non-zero on `error|alert` so
  systemd surfaces the unit as failed.
- `disk-cleanup.sh` — removes `/tmp` snapshot files (`*.db`,
  `staging-snap.*`, `cs-*`, `node-compile-cache`) older than 7 days
  and runs `docker builder prune` + `docker image prune` with
  `--filter "until=72h" --filter "label!=keep"`. Set
  `CORESCOPE_CLEANUP_DRY_RUN=1` to log without deleting.

### Install on the staging host

SSH to `<STAGING_HOST>` as the staging operator user and:

```bash
sudo install -m 0755 scripts/staging/disk-monitor.sh  /usr/local/bin/corescope-disk-monitor
sudo install -m 0755 scripts/staging/disk-cleanup.sh  /usr/local/bin/corescope-disk-cleanup

# 15-minute monitor
sudo tee /etc/systemd/system/corescope-disk-monitor.service >/dev/null <<'UNIT'
[Unit]
Description=CoreScope staging disk-usage monitor (issue #1684)
[Service]
Type=oneshot
ExecStart=/usr/local/bin/corescope-disk-monitor /
UNIT

sudo tee /etc/systemd/system/corescope-disk-monitor.timer >/dev/null <<'UNIT'
[Unit]
Description=Run CoreScope disk-usage monitor every 15 minutes
[Timer]
OnBootSec=5min
OnUnitActiveSec=15min
Unit=corescope-disk-monitor.service
[Install]
WantedBy=timers.target
UNIT

# Daily cleanup at 03:30 local
sudo tee /etc/systemd/system/corescope-disk-cleanup.service >/dev/null <<'UNIT'
[Unit]
Description=CoreScope staging disk cleanup (issue #1684)
[Service]
Type=oneshot
ExecStart=/usr/local/bin/corescope-disk-cleanup
UNIT

sudo tee /etc/systemd/system/corescope-disk-cleanup.timer >/dev/null <<'UNIT'
[Unit]
Description=Run CoreScope disk cleanup daily at off-peak
[Timer]
OnCalendar=*-*-* 03:30:00
Persistent=true
Unit=corescope-disk-cleanup.service
[Install]
WantedBy=timers.target
UNIT

sudo systemctl daemon-reload
sudo systemctl enable --now corescope-disk-monitor.timer corescope-disk-cleanup.timer
```

`<STAGING_HOST>` is the staging VM hostname/IP — operator supplies it,
not committed to the repo.

### Inspecting alerts

```bash
journalctl -t corescope-disk-monitor   --since '-1d'
journalctl -t corescope-disk-cleanup   --since '-7d'
systemctl list-timers | grep corescope-disk
```

`logger` priorities map: `ok→info`, `warn→warning`, `error→err`,
`alert→alert` (syslog severity 1, the highest level). Wire
`journalctl -p alert ...` to whatever ops channel the operator
prefers; use `-p err` to also catch the `error` tier.

### Notes on `staging-snap.db` root cause (#1684 phase 3)

`grep -rn staging-snap.db cmd/ public/ scripts/` returns **zero**
hits in the repo. The 4.4 GB orphan was a manual debugging artifact,
not produced by any committed code. The `disk-cleanup.sh` retention
rule (anything matching `staging-snap.*` in `/tmp` older than 7 days)
prevents recurrence without needing source-side TTL changes.

If a future feature legitimately needs persistent snapshot DBs, put
them under `/var/lib/corescope/snapshots/` with explicit rotation —
not in `/tmp`, which is ephemeral by definition.
