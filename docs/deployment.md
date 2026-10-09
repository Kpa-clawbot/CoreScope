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

The supplied images support `linux/amd64` and `linux/arm64`. Use Docker with the Compose plugin. SQLite is the default; PostgreSQL18.6 is an optional separate service. Budget for the application and database together: earlier SQLite memory figures do not size a PostgreSQL installation. Measure the retained dataset, packet cache, database indexes/WAL and backup storage before choosing capacity.

For migration, retain the original data plus recovery and normalized snapshots while PostgreSQL builds its own data and indexes. Required disk space and downtime must be measured on a representative copy. See [offline upgrade and recovery](postgresql-upgrade.md).

## Docker Deployment

### Complete Compose checkout

Use [DEPLOY.md](../DEPLOY.md#quick-start) to clone and pin the complete repository, populate the private `.env` and select a matching reviewed image. Keep the matching checkout, including optional PostgreSQL overrides and their initialization scripts.

```sh
docker compose -f docker-compose.example.yml up -d
docker compose -f docker-compose.example.yml ps
```

The default app validates/adopts existing SQLite storage or initializes a provably fresh install before starting. The optional `docker-compose.example.postgres.yml` override adds PostgreSQL and owner bootstrap. An installed selection is authoritative; a missing connection or an interrupted conversion never permits SQLite fallback.

The application container runs the server and ingestor, optional Mosquitto, and optional Caddy. When selected, PostgreSQL runs separately. Its runtime services receive restricted roles; the owner belongs only to setup/conversion.

| Setting | Purpose |
|---|---|
| `HTTP_PORT` | Example deployment's host HTTP port (default 80) |
| `DATA_DIR` | Application state: config, theme, queues, statistics and account backups |
| `POSTGRES_DATA_DIR` | Separate PostgreSQL storage; never copy live files as a logical backup |
| `CORESCOPE_IMAGE` | Reviewed application image tag or digest matching the checkout |
| `DISABLE_MOSQUITTO` | Use an external broker when true |
| `DISABLE_CADDY` | Use an external reverse proxy when true |
| `CORESCOPE_*_PASSWORD` | Distinct owner and runtime credentials; only for PostgreSQL, merge `.env.postgres.example` |

The production and staging variants use their own directory/port variables listed in `.env.example`. `manage.sh` operates those variants; `./manage.sh start --with-staging` clones telemetry only into an empty staging database and keeps staging accounts independent.

### Updating

Preserve a native backup and private configuration, pin the new checkout and matching image, then use the same Compose variant:

```sh
docker compose -f docker-compose.example.yml pull
docker compose -f docker-compose.example.yml up -d
```

A backend change in either direction requires the [offline storage switch](storage.md), not only an image update. Ordinary updates keep the recorded choice. Keep the previous application and recovery files until validation is complete.

---

## Configuration Reference

CoreScope uses a layered configuration system (highest priority wins):

1. **Environment variables** — `MQTT_BROKER`, `CORESCOPE_READER_DATABASE_URL`, etc.
2. **`/app/data/config.json`** — full config file (volume-mounted)
3. **Built-in defaults** — SQLite for a provably fresh installation; PostgreSQL requires its role URLs

After setup, `storage-selection.json` overrides bootstrap backend/path hints. Credentials and TLS remain private config/environment settings. Keep the same shared state directory on restart and update.

### Environment variable overrides

| Variable | Default | Description |
|----------|---------|-------------|
| `MQTT_BROKER` | `mqtt://localhost:1883` | MQTT broker URL (overrides config file) |
| `MQTT_TOPIC` | `meshcore/#` | MQTT topic subscription pattern |
| `CORESCOPE_READER_DATABASE_URL` | Required for PostgreSQL | Restricted telemetry reader URL |
| `CORESCOPE_WRITER_DATABASE_URL` | Required for PostgreSQL | Restricted telemetry writer URL |
| `CORESCOPE_USERS_DATABASE_URL` | Required when accounts are enabled | Separate account database writer URL |
| `CORESCOPE_APPROVED_CHANNELS_DATABASE_URL` | Required when proposals are enabled | Restricted approved-channel view reader URL |
| `CORESCOPE_STATE_DIR` | `data` for native binaries | Shared filesystem state, separate from database URLs |
| `DISABLE_MOSQUITTO` | `false` | Skip the internal Mosquitto broker |
| `DISABLE_CADDY` | `false` | Skip the built-in Caddy reverse proxy |

### config.json

Place `config.json` in the mounted state directory (`DATA_DIR` in the example deployment). Use private environment configuration for database passwords; do not publish rendered Compose configuration or URLs. Supported keys include `db.backend`, SQLite `dbPath`/`DB_PATH` and `userManagement.dbPath`, optional PostgreSQL role URLs, and `stateDir`. Explicit relative SQLite paths use the process working directory; containers use `/app`. Keep container databases/state inside `/app/data` so the bind mount persists them. Set `CORESCOPE_STATE_DIR=/app/data/<subdirectory>` in the private `.env` when using a custom container state directory; every service shares it.

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

Publish `1883:1883` on the application service in your Compose override when external gateways need to reach the built-in broker. Keep `DISABLE_MOSQUITTO=false`. The database service must remain on the internal network.

### External broker

To use your own MQTT broker (Mosquitto, EMQX, HiveMQ, etc.):

1. Disable the internal broker:
   ```bash
   DISABLE_MOSQUITTO=true
   ```

2. Point the ingestor at your broker:
   ```bash
   MQTT_BROKER=mqtt://your-broker:1883
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

2. Set `DISABLE_CADDY=false` for the application service, publish ports 80 and 443, mount the Caddyfile at `/etc/caddy/Caddyfile:ro` and persist certificate storage at `/data/caddy`. Make these changes in the same Compose deployment so the PostgreSQL dependency and role configuration remain intact.

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

Use native SQLite snapshots for SQLite or custom-format PostgreSQL archives for PostgreSQL. The authenticated `GET /api/backup` and admin `GET /api/admin/users-backup` endpoints download `.dump` files. The server needs PostgreSQL 18 `pg_dump` on `PATH`; the supplied image includes it.

For the production variant managed by `manage.sh`:

```sh
./manage.sh backup ./backups/instance
```

This saves `telemetry.dump`, `accounts.dump` and available config/theme/Caddy files. Each archive is consistent, but two sequential dumps are not a shared cross-database snapshot; stop both writers for a coordinated pair. Preserve credentials, queues/state and TLS material separately. Encrypt private backups off-host.

`./manage.sh restore <backup-directory>` requires the application stopped and fresh/empty native targets. SQLite restores stage and validate the full state bundle in a new empty state directory. PostgreSQL restores require both databases empty. It reapplies runtime grants and leaves the application stopped for validation. It never replaces a nonempty PostgreSQL database or overwrites an existing SQLite state directory. Use a fresh PostgreSQL storage location while retaining the previous cluster for recovery.

Account backups contain password hashes, addresses, sessions and tokens. Restoration can revive deleted accounts or revoked credentials. Review the [account recovery procedure](user-guide/accounts.md#backups) before reopening the service. See [native backup and cutover boundaries](postgresql-upgrade.md#native-backups-and-restores) for downloaded archives and rollback.

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

### Database readiness or privilege errors

Confirm the PostgreSQL service is healthy, the owner bootstrap/import finished, and each process uses its intended runtime role. An incomplete import stays closed until resume verifies every requested store. Do not grant owner privileges to runtime processes or manually flip readiness markers. The ingestor owns telemetry writes and retention; multiple active ingestors targeting one store are refused.

### Container unhealthy

Check logs: `docker compose logs --tail 50`. Common causes:
- Port 3000 already in use inside the container
- Missing/wrong runtime credentials, privilege grants or schema readiness
- Database failure — investigate logs and restore a verified archive into a fresh target

### ARM / Raspberry Pi issues

- Use `linux/arm64` images (Pi 4 and 5). Pi 3 (armv7) is not supported.
- First pull may be slow — the multi-arch manifest selects the right image automatically.
- If memory is tight, set `packetStore.maxMemoryMB` to limit RAM usage.
