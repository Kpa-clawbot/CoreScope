# Deploy CoreScope

Pre-built images are published to GHCR for `linux/amd64` and `linux/arm64` (Raspberry Pi 4/5).

## Quick Start

### Complete Compose checkout

PostgreSQL 18.6 runs as a separate service. Existing SQLite installations must complete the [offline upgrade](docs/postgresql-upgrade.md) before starting this version.

Set `CORESCOPE_REF` to the reviewed application revision, then retain the complete checkout: the Compose variants require `docker/postgres.compose.yml` and its initialization scripts.

```bash
git clone https://github.com/Kpa-clawbot/CoreScope.git
cd CoreScope
git checkout --detach "$CORESCOPE_REF"
test -f .env || cp .env.example .env
chmod 600 .env
# New PostgreSQL storage only: fill missing password fields with distinct
# openssl rand -hex 32 values. Preserve an existing .env and merge missing keys.
# Set CORESCOPE_IMAGE in .env to the matching reviewed image tag or digest.
docker compose -f docker-compose.example.yml up -d
```

The PostgreSQL initializer creates separate telemetry/account databases and roles. Bootstrap installs schemas and grants; the application starts only when bootstrap succeeds. Open `http://localhost` and verify `/api/healthz` plus real packet ingestion. The default HTTP port is 80; adjust `HTTP_PORT` when using the example variant.

## Image Tags

| Tag | Description |
|-----|-------------|
| Reviewed release tag or image digest | Pin the application and its matching repository files together |
| `latest` | Latest release tag |
| `edge` | Built from master — unstable, for testing |

## Configuration

Settings can be overridden via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `DISABLE_CADDY` | `false` in the example | Caddy forwards published port 80 to the Go server on 3000. If disabled, also change your proxy/port mapping to target 3000. |
| `DISABLE_MOSQUITTO` | `true` in `docker-compose.staging.yml`; `false` elsewhere | Skip internal MQTT broker. Default flipped to `true` for the staging deploy in v3.7+ because a standalone `mqtt-broker` container owns MQTT on that host — see "Standalone MQTT broker (staging)" below. |
| `HTTP_PORT` | `80` | Host port mapping |
| `DATA_DIR` | `./data` | Host path for persistent data |

For advanced configuration, mount a `config.json` into `/app/data/config.json`. See `config.example.json` in the repo.

## Updating

```bash
docker compose -f docker-compose.example.yml pull
docker compose -f docker-compose.example.yml up -d
```

## Data

PostgreSQL persists telemetry and accounts in its own host directory (`POSTGRES_DATA_DIR`, or the production/staging equivalent). `/app/data` holds config, theme, queues, statistics and account backup files. Keep these locations separate and preserve the private `.env` with your recovery material.

Use native `pg_dump` archives or the authenticated backup endpoints. `./manage.sh backup <directory>` and `restore` operate the production Compose variant managed by that script; they do not select `docker-compose.example.yml`. The [backup and restore instructions](docs/postgresql-upgrade.md#native-backups-and-restores) include the example variant. Do not copy live PostgreSQL storage or a legacy SQLite file as a current backup.

## TLS

Option A — **External reverse proxy**: Keep the default internal Caddy and proxy to the published HTTP port. Alternatively, set `DISABLE_CADDY=true` and have the proxy reach container port 3000; change the Compose port mapping from `:80` to `:3000` if the proxy runs on the host. Setting the flag alone leaves the existing port-80 mapping without a listener.

Option B — **Built-in Caddy**: Mount a custom Caddyfile at `/etc/caddy/Caddyfile` and expose ports 80+443.

---

## Standalone MQTT broker (staging)

Starting in v3.7, `docker-compose.staging.yml` assumes a **standalone
`mqtt-broker` container** (image: `eclipse-mosquitto:2`) already runs
on the staging VM, out-of-band from this repo. That container:

- Owns port `8883` externally (TLS-terminated MQTT for real observers).
- Is attached to a shared docker network named `meshcore-net`.
- Is operator-managed state — it is **not** defined in any compose
  file in this repository. Its config, TLS certs, and ACLs live on the
  host, outside git.

`corescope-staging-go` reaches it in-network at `mqtt-broker:1883` via
docker DNS (no host port hop). To make that work, `docker-compose.staging.yml`
joins the external `meshcore-net` network and defaults `DISABLE_MOSQUITTO=true`
so the built-in mosquitto stays off.

### Prereq — one-time provisioning on the staging host

```bash
docker network create meshcore-net
# ...then bring up the operator-managed mqtt-broker container on that
# network (not covered here; that's operator state). THEN:
docker compose -f docker-compose.staging.yml up -d
```

If `meshcore-net` doesn't exist when compose starts, docker will refuse
to bring `staging-go` up (`external: true` — compose won't create it).

### Reverting to the old single-container behaviour

Third-party operators cloning this repo who want the legacy shape
(in-container mosquitto + `1883:1883` on the host, no external broker)
should override both the env default and re-add the port mapping.

In `.env` (or the shell):

```
DISABLE_MOSQUITTO=false
```

And in `docker-compose.staging.yml`, restore the `1883:1883` mapping
under `services.staging-go.ports`:

```yaml
    ports:
      - "${STAGING_GO_HTTP_PORT:-80}:80"
      - "${STAGING_GO_MQTT_PORT:-1883}:1883"   # ← re-added
      - "6060:6060"
      - "6061:6061"
```

That gives you back the pre-v3.7 self-contained staging shape. In that
mode you do **not** need `meshcore-net`, but note the compose file still
declares it as `external: true`, so either remove that declaration in
your fork or ensure the network exists.

---

## Migrating from manage.sh (existing admins)

If you're currently deploying with `manage.sh` (git clone + local build), you have two options going forward:

### Option A: Keep using manage.sh

Complete the PostgreSQL migration and configure the private database credentials before using `manage.sh start` or `update`. Its backup, restore, staging-copy and status commands use PostgreSQL clients from the selected database container. Restores require empty destinations; existing staging data is retained.

```bash
./manage.sh update          # latest release
./manage.sh update "$CORESCOPE_REF"   # reviewed version
```

### Option B: Switch to pre-built images (recommended)

Pre-built images skip the build step entirely — faster updates, no Go toolchain needed.

**One-time migration:**

1. Stop the current deployment:
   ```bash
   ./manage.sh stop
   ```

2. Preserve `PROD_DATA_DIR`, the separate PostgreSQL directory, credentials and the native backup pair. For a SQLite instance, perform the offline upgrade first.

3. Keep the complete pinned checkout and choose the matching image in `.env`:
   ```bash
   # CORESCOPE_IMAGE=<reviewed tag or digest>
   # DATA_DIR=<existing state directory>
   # POSTGRES_DATA_DIR=<existing PostgreSQL directory>
   ```

4. Start with the pre-built image:
   ```bash
   docker compose -f docker-compose.example.yml up -d
   ```

5. Verify it picked up your existing data:
   ```bash
   curl http://localhost/api/stats
   ```

**Updates after migration:**
```bash
docker compose -f docker-compose.example.yml pull
docker compose -f docker-compose.example.yml up -d
```

### What about manage.sh features?

| manage.sh command | Pre-built equivalent |
|---|---|
| `./manage.sh update` | `docker compose -f docker-compose.example.yml pull` then `up -d` with the same file |
| `./manage.sh stop` | `docker compose -f docker-compose.example.yml stop corescope` |
| `./manage.sh start` | `docker compose -f docker-compose.example.yml up -d` |
| `./manage.sh logs` | `docker compose -f docker-compose.example.yml logs -f` |
| `./manage.sh status` | `docker compose -f docker-compose.example.yml ps` |
| `./manage.sh setup` | Retain the full checkout, configure private credentials and select the Compose variant |

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
