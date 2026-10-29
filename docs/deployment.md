# CoreScope Deployment Guide

Comprehensive guide to deploying and operating CoreScope. For a quick start, see [DEPLOY.md](../DEPLOY.md).

## Table of Contents

- [System Requirements](#system-requirements)
- [Docker Deployment](#docker-deployment)
- [Configuration Reference](#configuration-reference)
- [MQTT Setup](#mqtt-setup)
- [TLS / HTTPS](#tls--https)
- [Behind a CDN (Cloudflare, Fastly)](#behind-a-cdn-cloudflare-fastly)
- [Monitoring & Health Checks](#monitoring--health-checks)
- [Backup & Restore](#backup--restore)
- [Troubleshooting](#troubleshooting)

---

## System Requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| RAM | 256 MB | 512 MB+ |
| Disk | 500 MB (image + DB) | 2 GB+ for long-term data |
| CPU | 1 core | 2+ cores |
| Architecture | `linux/amd64`, `linux/arm64` | — |
| Docker | 20.10+ | Latest stable |

CoreScope runs well on Raspberry Pi 4/5 (ARM64). The Go server uses ~300 MB RAM for 56K+ packets.

---

## Docker Deployment

### Quick Start (one command)

```bash
docker run -d --name corescope \
  -p 80:80 \
  -v corescope-data:/app/data \
  ghcr.io/kpa-clawbot/corescope:latest
```

Open `http://localhost` — you'll see an empty dashboard ready to receive packets.

No `config.json` is required. The server starts with sensible defaults:
- HTTP on port 3000 (Caddy proxies port 80 → 3000 internally)
- Internal Mosquitto MQTT broker on port 1883
- Ingestor connects to `mqtt://localhost:1883` automatically
- SQLite database at `/app/data/meshcore.db`

### Full `docker run` Reference (recommended)

The bare `docker run` command is the primary deployment method. One image, documented parameters — run it however you want.

```bash
docker run -d --name corescope \
  --restart=unless-stopped \
  -p 80:80 -p 443:443 -p 1883:1883 \
  -e DISABLE_MOSQUITTO=false \
  -e DISABLE_CADDY=false \
  -v /your/data:/app/data \
  -v /your/Caddyfile:/etc/caddy/Caddyfile:ro \
  -v /your/caddy-data:/data/caddy \
  ghcr.io/kpa-clawbot/corescope:latest
```

#### Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `-p 80:80` | Yes | HTTP web UI |
| `-p 443:443` | No | HTTPS (only if using built-in Caddy with a domain) |
| `-p 1883:1883` | No | MQTT broker (expose if external gateways connect directly) |
| `-v /your/data:/app/data` | Yes | Persistent data: SQLite DB, config.json, theme.json |
| `-v /your/Caddyfile:/etc/caddy/Caddyfile:ro` | No | Custom Caddyfile for HTTPS |
| `-v /your/caddy-data:/data/caddy` | No | Caddy TLS certificate storage |
| `-e DISABLE_MOSQUITTO=true` | No | Skip the internal Mosquitto broker (use your own) |
| `-e DISABLE_CADDY=true` | No | Skip the built-in Caddy reverse proxy |
| `-e MQTT_BROKER=mqtt://host:1883` | No | Override MQTT broker URL |
| `-e RUN_AS_UID=1000` | No | Run the Go services as this uid instead of root — see [Running the Go services unprivileged](#running-the-go-services-unprivileged) |
| `-e RUN_AS_GID=1000` | No | Group for `RUN_AS_UID` (defaults to it) |

#### `/app/data/.env` convenience file

Instead of passing `-e` flags, you can drop a `.env` file in your data volume:

```bash
# /your/data/.env
DISABLE_MOSQUITTO=true
DISABLE_CADDY=true
MQTT_BROKER=mqtt://my-broker:1883
```

The entrypoint sources this file before starting services. This works with any launch method (`docker run`, compose, or manage.sh).

Two exceptions, both because the entrypoint runs this file as root from a
directory the Go services can write once they run unprivileged:

- `RUN_AS_UID` and `RUN_AS_GID` are never taken from `.env`. Set them with
  `-e` or in compose's `environment:`.
- With `RUN_AS_UID` set, `.env` is read only if it is owned by root and
  writable by no one else (`sudo chown root:root .env && sudo chmod 644 .env`).
  Otherwise the log says it was skipped, and why. "Owned by root" means uid 0
  inside the container. With rootless Docker or a user-namespace remap
  (`userns-remap`), host root is not that uid: look at the owner the container
  reports (`docker exec <container> stat -c '%u' /app/data/.env`) and give the host
  file whichever owner id maps to container uid 0.

### Docker Compose (legacy alternative)

Docker Compose files are maintained for backward compatibility but are no longer the recommended approach.

```bash
curl -sL https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/docker-compose.example.yml \
  -o docker-compose.yml
docker compose up -d
```

#### Compose environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_PORT` | `80` | Host port for the web UI |
| `DATA_DIR` | `./data` | Host path for persistent data |
| `DISABLE_MOSQUITTO` | `false` | Set `true` to use an external MQTT broker |
| `DISABLE_CADDY` | `false` | Set `true` to skip the built-in Caddy proxy |

### manage.sh (legacy alternative)

The `manage.sh` wrapper script provides a setup wizard and convenience commands. It uses Docker Compose internally. See [DEPLOY.md](../DEPLOY.md) for usage. New deployments should prefer bare `docker run`.

### Image tags

| Tag | Use case |
|-----|----------|
| `v3.4.1` | Pinned release — recommended for production |
| `v3.4` | Latest patch in the v3.4.x series |
| `v3` | Latest minor+patch in v3.x |
| `latest` | Latest release tag |
| `edge` | Built from master on every push — unstable |

### Updating

```bash
docker compose pull
docker compose up -d
```

For `docker run` users:

```bash
docker pull ghcr.io/kpa-clawbot/corescope:latest
docker stop corescope && docker rm corescope
docker run -d --name corescope ... # same flags as before
```

Data is preserved in the volume — updates are non-destructive.

### Running the Go services unprivileged

`corescope-server` and `corescope-ingestor` can run as a normal user instead of
root, so that a code-execution bug in either one does not land as root in a
container that has your data directory and `config.json` mounted. `supervisord`
itself stays root, because mosquitto and caddy still need to bind their ports.

It is opt-in, with `RUN_AS_UID`:

| `RUN_AS_UID` | Services run as |
|--------------|-----------------|
| unset (the default) | root — as in earlier releases |
| set | `RUN_AS_UID` / `RUN_AS_GID` |

Nothing about an upgrade changes until you set it: the container does not pick a
uid for you, and does not change the owner of anything on your host. That is
deliberate rather than cautious. Every release before this one ran as root, so
the `meshcore.db` in your data directory — with its `-wal` and `-shm`, `users.db`
and the backups beside them — is root-owned. Switching user without also handing
those files over leaves the services unable to write the database, so the switch
has to be something you ask for.

**To turn it on**, pick the uid you want and restart. Setting `RUN_AS_UID` is
taken as permission to `chown` the data directory, and what an earlier root-run
container left in it, to that uid. This happens at every start, so a file
created there later as root (with `docker exec`, say) is handed over at the
next restart:

```bash
docker run -d --name corescope \
  -e RUN_AS_UID=1000 -e RUN_AS_GID=1000 \
  -v /your/data:/app/data \
  ... # same flags as before
  ghcr.io/kpa-clawbot/corescope:latest
```

Check which user it picked:

```bash
docker logs corescope 2>&1 | grep '\[entrypoint\] Go services run as'
# [entrypoint] Go services run as corescope (uid=1000 gid=1000)
```

If you would rather the container never `chown` anything, do the hand-over
yourself first. Then there is nothing left for it to change:

```bash
docker stop corescope
sudo chown -R 1000:1000 /your/data     # uid of your own account is usually 1000
docker start corescope                 # with RUN_AS_UID=1000 set as above
```

To go back, unset `RUN_AS_UID` and `chown -R root:root /your/data`.

Notes:

- `RUN_AS_GID` defaults to `RUN_AS_UID` when omitted. `RUN_AS_UID=0` is the
  same as leaving it unset.
- If the uid already belongs to another account inside the image (`caddy` is
  uid 100, `mosquitto` 101), that account is used as it is, keeping its own
  group, and the log says so. Pick a different uid if you need `RUN_AS_GID`
  honoured.
- With `RUN_AS_UID` set to a uid the container cannot use, startup fails rather
  than quietly falling back to root.
- What the hand-over leaves alone: anything mounted into the data directory on
  its own, such as `-v ./config.json:/app/data/config.json:ro`, which keeps the
  owner it has on your host (make sure the uid can read it), and `.env` (see
  above). Each mount it skips is named in the log.
- If the hand-over cannot change a file, for example on NFS with
  `root_squash`, the log says how many and which. Fix their owner on the host.
- `/app` stays root's. The services write only to the data directory and
  `/tmp`.
- A fresh install is root-owned and so starts as root, like any other. Set
  `RUN_AS_UID` from the first run to avoid a hand-over later.

The uid selection lives in `docker/select-service-user.sh`, with its branches
covered by `docker/test-select-service-user.sh`.

---

## Configuration Reference

CoreScope uses a layered configuration system (highest priority wins):

1. **Environment variables** — `MQTT_BROKER`, `DB_PATH`, etc.
2. **`/app/data/config.json`** — full config file (volume-mounted)
3. **Built-in defaults** — work out of the box with no config

### Environment variable overrides

| Variable | Default | Description |
|----------|---------|-------------|
| `MQTT_BROKER` | `mqtt://localhost:1883` | MQTT broker URL (overrides config file) |
| `MQTT_TOPIC` | `meshcore/#` | MQTT topic subscription pattern |
| `DB_PATH` | `data/meshcore.db` | SQLite database path |
| `DISABLE_MOSQUITTO` | `false` | Skip the internal Mosquitto broker |
| `DISABLE_CADDY` | `false` | Skip the built-in Caddy reverse proxy |

### config.json

For advanced configuration, create a `config.json` and mount it at `/app/data/config.json`:

```bash
docker run -d --name corescope \
  -p 80:80 \
  -v corescope-data:/app/data \
  -v ./config.json:/app/data/config.json:ro \
  ghcr.io/kpa-clawbot/corescope:latest
```

See `config.example.json` in the repository for all available options including:
- MQTT sources (multiple brokers)
- Channel encryption keys
- Branding and theming
- Health thresholds
- Region filters
- Retention policies
- Geo-filtering
- Map tile providers (OSM, Stamen, Carto, etc.)

### Map Tile Providers

Map tile providers are enabled and configured via the `config.json` file. You can provide your custom API credentials (e.g. `osm_url`, `stamen_api_key`, `mapbox_api_key`) to activate external tile services. Once configured on the server, users can select their preferred tile provider from the Customizer UI on the client, and their choice will be persisted automatically.

#### CARTO now requires an API key

CARTO's raster basemaps (`carto-dark`, `carto-light`, `carto-voyager`,
`carto-voyager-dark`, `positron-dark`) require an API key. Without one, tiles
are still returned with a **200 status** but every one of them is stamped
`API KEY REQUIRED` — so nothing errors, no healthcheck fires, and the map just
quietly looks broken.

Get a free key at <https://carto.com/basemaps/apikey> — no CARTO account, no
credit card, emailed back immediately, 5 million tile requests per calendar
month across raster and vector. Then set it as `key`:

```json
"map": {
  "tiles": {
    "providers": {
      "carto": { "enabled": true, "key": "YOUR_CARTO_KEY" }
    }
  }
}
```

Notes:

- The query parameter is `key`. **`api_key` is silently ignored** and still
  serves the watermarked tile.
- The key is sent to the browser, so treat it as public. CARTO asks for a domain
  when the key is issued, but whether that restriction is enforced on every
  request has not been verified here. Assume anyone can read the key from
  devtools and spend your quota. The 5M/month fair-use ceiling makes that
  low-impact for most deployments, but it is worth knowing.
- A wrong or expired key fails the same silent way as no key at all. Verify by
  **looking at a tile**, not by checking for a 200.
- CARTO has said it is considering freezing data updates to the raster
  basemaps. If you would rather not depend on them, set
  `"carto": { "enabled": false }` and use another provider — `esri` needs no
  key at all, and `osm`/`stamen` accept their own tokens.

---

## MQTT Setup

CoreScope receives MeshCore packets via MQTT. The container ships with an internal Mosquitto broker — no setup needed for basic use.

### Internal broker (default)

The built-in Mosquitto broker listens on port 1883 inside the container. Point your MeshCore gateways at it:

```bash
# Expose MQTT port for external gateways
docker run -d --name corescope \
  -p 80:80 -p 1883:1883 \
  -v corescope-data:/app/data \
  ghcr.io/kpa-clawbot/corescope:latest
```

### External broker

To use your own MQTT broker (Mosquitto, EMQX, HiveMQ, etc.):

1. Disable the internal broker:
   ```bash
   -e DISABLE_MOSQUITTO=true
   ```

2. Point the ingestor at your broker:
   ```bash
   -e MQTT_BROKER=mqtt://your-broker:1883
   ```

   Or via `config.json`:
   ```json
   {
     "mqttSources": [
       {
         "name": "my-broker",
         "broker": "mqtt://your-broker:1883",
         "username": "user",
         "password": "pass",
         "topics": ["meshcore/#"]
       }
     ]
   }
   ```

### Multiple brokers

CoreScope can connect to multiple MQTT brokers simultaneously:

```json
{
  "mqttSources": [
    {
      "name": "local",
      "broker": "mqtt://localhost:1883",
      "topics": ["meshcore/#"]
    },
    {
      "name": "remote",
      "broker": "mqtts://remote-broker:8883",
      "username": "reader",
      "password": "secret",
      "topics": ["meshcore/+/+/packets"]
    }
  ]
}
```

### MQTT topic format

MeshCore gateways typically publish to `meshcore/<gateway>/<region>/packets`. The default subscription `meshcore/#` catches all of them.

---

## TLS / HTTPS

### Option 1: External reverse proxy (recommended)

Run CoreScope behind nginx, Traefik, or Cloudflare Tunnel for TLS termination:

```nginx
# nginx example
server {
    listen 443 ssl;
    server_name corescope.example.com;

    ssl_certificate /etc/ssl/certs/corescope.pem;
    ssl_certificate_key /etc/ssl/private/corescope.key;

    location / {
        proxy_pass http://localhost:80;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }
}
```

The `Upgrade` and `Connection` headers are required for WebSocket support.

### Option 2: Built-in Caddy (auto-TLS)

The container includes Caddy for automatic Let's Encrypt certificates:

1. Create a Caddyfile:
   ```
   corescope.example.com {
     reverse_proxy localhost:3000
   }
   ```

2. Mount it and expose TLS ports:
   ```bash
   docker run -d --name corescope \
     -p 80:80 -p 443:443 \
     -v corescope-data:/app/data \
     -v caddy-certs:/data/caddy \
     -v ./Caddyfile:/etc/caddy/Caddyfile:ro \
     ghcr.io/kpa-clawbot/corescope:latest
   ```

Caddy handles certificate issuance and renewal automatically.

---

## API Documentation

CoreScope auto-generates an OpenAPI 3.0 specification from its route definitions. The spec is always in sync with the running server — no manual maintenance required.

### Endpoints

| URL | Description |
|-----|-------------|
| `/api/spec` | OpenAPI 3.0 JSON schema — machine-readable API definition |
| `/api/docs` | Interactive Swagger UI — browse and test all 40+ endpoints |

### Usage

**Browse the API interactively:**
```
http://your-instance/api/docs
```

**Fetch the spec programmatically:**
```bash
curl http://your-instance/api/spec | jq .
```

**For bot/integration developers:** The spec includes all request parameters, response schemas, and example values. Import it into Postman, Insomnia, or any OpenAPI-compatible tool.

### Public instance
The live instance at [analyzer.00id.net](https://analyzer.00id.net) has all API endpoints publicly accessible:
- Spec: [analyzer.00id.net/api/spec](https://analyzer.00id.net/api/spec)
- Docs: [analyzer.00id.net/api/docs](https://analyzer.00id.net/api/docs)

---

## Behind a CDN (Cloudflare, Fastly)

If you front CoreScope with a CDN — Cloudflare, Fastly, Akamai, or
similar — you **must** configure the CDN to bypass cache for `/api/*`.
The server emits `Cache-Control: no-store` on every API response
(see #1551), but Cloudflare's zone-level Cache Rules and legacy Page
Rules can override origin headers. When that happens, observers, packets,
stats and other API responses get cached at the edge for minutes to hours,
producing observer-flap, stale dashboards and inconsistent state across
viewers.

### 1. Verify whether your CDN is caching `/api/*`

From **outside** the CDN (a different network than your origin), run:

```sh
curl -sI 'https://<your-domain>/api/observers' | grep -iE 'cf-cache|age|cache-control'
```

Healthy output (cache is bypassed):

```
cache-control: no-store
cf-cache-status: BYPASS
age: 0
```

Unhealthy output (CDN is caching despite `no-store`):

```
cache-control: no-store
cf-cache-status: HIT
age: 4732
```

`HIT` or `age > 0` means an intermediary is serving cached JSON. Fix it
before relying on the dashboard.

You can also run the bundled helper, which exits non-zero with a precise
diagnostic when caching is detected:

```sh
scripts/check-cdn-bypass.sh https://<your-domain>
```

### 2. Cloudflare: add a Cache Rule (recommended)

Cloudflare Dashboard → your zone → **Caching → Cache Rules → Create rule**:

- **When incoming requests match:** Field = `URI Path`, Operator = `starts with`, Value = `/api/`
- **Then:** Cache eligibility → **Bypass cache**

Save and deploy. Re-run the curl above; you should now see
`cf-cache-status: BYPASS`.

Legacy Page Rules equivalent (if your zone has no Cache Rules):

- URL pattern: `*your-domain*/api/*`
- Setting: **Cache Level → Bypass**

### 3. Fastly / other CDNs

Apply the equivalent bypass-cache rule for the `/api/` path prefix.
The key invariant is: any response from `/api/*` must reach the
browser uncached (no shared-cache HIT, no positive `Age` header).

### 4. Re-verify

After applying the rule, run step 1's curl from outside the CDN again
and confirm `cf-cache-status: BYPASS` (or absence of HIT) and `age: 0`.

### 5. Watch the server log

The server logs a one-shot warning at the first request bearing a
CDN-specific header (`CF-Connecting-IP`, `CF-Ray`, `Fastly-Client-IP`,
or `True-Client-IP`):

Generic reverse-proxy headers (`X-Forwarded-For`, `X-Real-IP`) are
deliberately NOT used as the signal — every nginx/Caddy/Traefik/k8s
deploy sets them, so they'd produce false positives on every
reverse-proxied install.

```
[security] WARNING: detected request via CDN (CF-Ray header present).
Ensure /api/* is bypassed in your CDN config — see docs/deployment-behind-cdn.md.
Cached API responses cause observer-flap and incorrect dashboards.
```

This is informational — the request is not blocked. Treat it as a
prompt to run the verification curl above. The warning logs at most
once per process boot, regardless of how many CDN-fronted requests
arrive.

### Why this can't be fixed server-side

CDN cache policy is operator-controlled. The application emits the
most conservative cache header it can (`Cache-Control: no-store`),
but Cloudflare Cache Rules / Page Rules have higher precedence than
origin headers in many zone configurations. The only durable fix is
the operator-side bypass rule.

---

## Monitoring & Health Checks

### Docker health check

The container includes a built-in health check that hits `/api/stats`:

```bash
docker inspect --format='{{.State.Health.Status}}' corescope
```

Docker reports `healthy` or `unhealthy` automatically. The check runs every 30 seconds.

### Manual health check

```bash
curl -f http://localhost/api/stats
```

Returns JSON with packet counts, node counts, and version info:

```json
{
  "totalPackets": 56234,
  "totalNodes": 142,
  "totalObservers": 12,
  "packetsLastHour": 830,
  "packetsLast24h": 19644,
  "engine": "go",
  "version": "v3.4.1"
}
```

### Log monitoring

```bash
# All logs
docker compose logs -f

# Server only
docker compose logs -f | grep '\[server\]'

# Ingestor only
docker compose logs -f | grep '\[ingestor\]'
```

### Resource monitoring

```bash
docker stats corescope
```

---

## Backup & Restore

### Backup

Use **`GET /api/backup`** for the telemetry database. It uses SQLite's
[`VACUUM INTO`](https://www.sqlite.org/lang_vacuum.html) to produce a consistent
snapshot while the ingestor is running, including committed data in the WAL.
Do not copy the live `meshcore.db` file: a checkpoint followed by a file copy
also races new writes. Allow room for a full snapshot in the server's temporary
directory and at the download destination; large snapshots cost CPU and disk I/O.

When accounts are enabled, sign in as an administrator and open `/api/backup`
on the same site to download a snapshot. For command-line or scheduled backups,
configure a strong `apiKey` in `config.json` (at least 16 characters). Create a
private header file, `~/.config/corescope/backup.header`, containing one line:
`X-API-Key: <your configured key>`. Set its permissions to `600`; keep the key out
of shell history, URLs, and scripts checked into Git.

On the backup host, install `curl` and the `sqlite3` CLI. Replace the example
HTTPS origin below with your instance. The download is published as a `.db`
only after it is complete, nonempty, and passes SQLite's consistency check:

```bash
(
  set -eu
  umask 077
  backup_dir="$HOME/corescope-backups"
  mkdir -p "$backup_dir"
  tmp=$(mktemp "$backup_dir/corescope-$(date -u +%Y%m%dT%H%M%SZ).XXXXXXXX.part")
  trap 'rm -f "$tmp"' EXIT
  curl -q --fail --silent --show-error \
    --header "@$HOME/.config/corescope/backup.header" \
    --output "$tmp" https://corescope.example.org/api/backup
  test -s "$tmp"
  test "$(sqlite3 -readonly "$tmp" 'PRAGMA quick_check;')" = ok
  mv "$tmp" "${tmp%.part}.db"
)
```

This is a point-in-time telemetry snapshot, not continuous replication or a full
instance backup. Keep protected copies off the CoreScope host, and also preserve:
- `config.json`, `theme.json`, deployment settings, and any configured keys/secrets.
- When accounts are enabled, **`users.db` separately**: use the
  [account backup and restore guide](user-guide/accounts.md#backups), which covers
  its own snapshot endpoint and scheduled backups. Do not copy that database live.

### Restore

1. Verify the downloaded file is nonempty and run
   `sqlite3 -readonly backup.db 'PRAGMA quick_check;'`; the result must be `ok`.
   Keep an untouched copy of this snapshot.
2. Stop **every server and ingestor using the target database**, including any
   separate instances. For the single-container example, use `docker stop corescope`;
   for `docker-compose.example.yml`, use
   `docker compose -f docker-compose.example.yml stop corescope`.
3. Locate the host directory or named volume mounted at `/app/data`. For the
   example Compose file, this is `DATA_DIR` (default `./data`). While all database
   users remain stopped, move the old `meshcore.db` **and any matching `-wal`,
   `-shm`, and `-journal` files together** into a separate recovery directory.
   Keep that set intact; never leave an old WAL beside the replacement database.
4. Copy the checked snapshot into that data mount as `meshcore.db`. Preserve the
   previous database's owner and permissions so the configured service user can
   read and write it. Leave configuration, keys, and `users.db` in place.
5. Start the stopped services (`docker start corescope`, or the matching Compose
   `start corescope` command). Check the logs, `/api/healthz`, and expected historical
   packets in the dashboard. Keep the recovery directory until these checks pass.

To roll back, stop all database users again, move the replacement database and
its sidecars aside, then restore the entire original file set before restarting.

### Automated backups

Run the checked-download block above from a private script using cron or a
systemd timer. Use absolute paths and the backup user's protected header file;
schedule one run at a time during quieter hours. Alert on nonzero exit status.
Prune old **completed `.db` snapshots only after a new backup succeeds**, according
to your retention policy, and periodically test a restore in an isolated instance.

---

## Troubleshooting

### Container starts but dashboard is empty

This is normal on first start with no MQTT sources configured. The dashboard shows data once packets arrive via MQTT. Either:
- Point a MeshCore gateway at the container's MQTT broker (port 1883)
- Configure an external MQTT source in `config.json`

### "no MQTT connections established" in logs

The ingestor couldn't connect to any MQTT broker. Check:
1. Is the internal Mosquitto running? (`DISABLE_MOSQUITTO` should be `false`)
2. Is the external broker reachable? Test with `mosquitto_sub -h broker -t meshcore/#`
3. Are credentials correct in `config.json`?

### WebSocket disconnects / real-time updates stop

If behind a reverse proxy, ensure WebSocket upgrade headers are forwarded:
```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
```

Also check proxy timeouts — set them to at least 300s for long-lived WebSocket connections.

### High memory usage

The in-memory packet store grows with retained packets. Configure retention limits in `config.json`:

```json
{
  "packetStore": {
    "retentionHours": 24,
    "maxMemoryMB": 512
  },
  "retention": {
    "nodeDays": 7,
    "packetDays": 30
  }
}
```

`packetStore.maxMemoryMB` bounds the store **and the caches that belong to it** — the decoded-packet cache and per-packet index entries, not just the stored rows. It is enforced in two places: the startup load stops at the budget, and the store evicts oldest-first when it exceeds it. Leaving it unset means no limit. Actual usage is on `/api/perf` as `packetStore.trackedMB`, next to `maxMB`.

### Database locked errors

SQLite doesn't support concurrent writers well. Ensure only one CoreScope instance accesses the database file. If running multiple containers, each needs its own database.

### Container unhealthy

Check logs: `docker compose logs --tail 50`. Common causes:
- Port 3000 already in use inside the container
- Database file permissions (must be writable by the container user)
- Corrupted database — restore from backup

### ARM / Raspberry Pi issues

- Use `linux/arm64` images (Pi 4 and 5). Pi 3 (armv7) is not supported.
- First pull may be slow — the multi-arch manifest selects the right image automatically.
- If memory is tight, set `packetStore.maxMemoryMB` to limit RAM usage.
