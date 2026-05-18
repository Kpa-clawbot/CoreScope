# live.meshcore.ca Deployment Runbook

This runbook is for replacing the current live CoreScope service with this
repository after the production-readiness PR is approved. It is not the
side-by-side dev deployment; this service owns the public live ports `80` and
`443`.

## Observed Live Baseline

Read-only inspection on May 17, 2026 showed:

| Item | Current live value |
|------|--------------------|
| Container name | `corescope` |
| Current image | `ghcr.io/kpa-clawbot/corescope:edge` |
| Public ports | host `80 -> 80`, host `443 -> 443` |
| Internal MQTT | disabled by `supervisord-no-mosquitto.conf` |
| Caddy | `/etc/caddy/Caddyfile`, proxying to `localhost:3000` |
| Runtime config | `/app/config.json`, mounted read-only |
| SQLite DB | `/app/data/meshcore.db` |
| Host config | `/opt/docker/corescope/data/corescope/config.json` |
| Host data | `/opt/docker/corescope/data/corescope/data` |
| Host Caddyfile | `/opt/docker/corescope/data/caddy/config/Caddyfile` |
| Host Caddy data | `/opt/docker/corescope/data/caddy/app` |

The live MQTT broker credentials stay in the mounted config file. Do not copy
them into repo-tracked files, PR descriptions, logs, or screenshots.

## Production Compose Target

The repository `docker-compose.live.yml` mirrors the live layout:

- app service and container name: `corescope`
- Postgres service and container name: `corescope-postgres`
- app binds host ports `80` and `443`
- no host `1883` publish for production
- `DISABLE_MOSQUITTO=true`
- Caddy remains enabled and uses the existing mounted Caddyfile
- `/app/config.json` remains the external MQTT config source
- `/app/data/meshcore.db` remains mounted for migration and rollback
- Postgres data defaults to `/opt/docker/corescope/data/postgres`

Create a private `.env` on the host before cutover. Use a real generated
Postgres password and keep `DATABASE_URL` in sync with it:

```bash
CORESCOPE_IMAGE=corescope:live-candidate

PROD_HTTP_PORT=80
PROD_HTTPS_PORT=443
PROD_CONFIG_FILE=/opt/docker/corescope/data/corescope/config.json
PROD_DATA_DIR=/opt/docker/corescope/data/corescope/data
PROD_CADDYFILE=/opt/docker/corescope/data/caddy/config/Caddyfile
PROD_CADDY_DATA_DIR=/opt/docker/corescope/data/caddy/app
PROD_POSTGRES_DATA_DIR=/opt/docker/corescope/data/postgres

DISABLE_MOSQUITTO=true
DISABLE_CADDY=false
DB_DRIVER=postgres
DB_PATH=/app/data/meshcore.db
POSTGRES_DB=corescope
POSTGRES_USER=corescope
POSTGRES_PASSWORD=replace-with-a-real-password
DATABASE_URL=postgres://corescope:replace-with-a-real-password@postgres:5432/corescope?sslmode=disable
```

## Local Migration Rehearsal

Rehearse with a copied live SQLite snapshot before the real cutover. For a WAL
mode database, copy the DB and sidecars together:

```bash
meshcore.db
meshcore.db-wal
meshcore.db-shm
```

A stopped container is the safest source. If the old live container is still
writing, a plain file copy can miss uncheckpointed WAL data.

Run the rehearsal against local Docker Postgres:

```bash
docker compose -f docker-compose.live.yml up -d postgres
docker compose -f docker-compose.live.yml build corescope
docker compose -f docker-compose.live.yml run --rm --no-deps \
  --entrypoint /app/corescope-migrate-postgres \
  corescope \
  -sqlite /app/data/meshcore.db \
  -postgres "$DATABASE_URL" \
  -truncate
```

The migration validates row counts and transmission hash samples. Keep the
SQLite source mounted read-only for rehearsals where possible.

## Live Cutover

Use an approved maintenance window.

1. Build or pull the approved image for this repository.
2. Stop the old live container:
   ```bash
   docker stop corescope
   ```
3. Preserve rollback by renaming the stopped old container:
   ```bash
   docker rename corescope corescope-sqlite-rollback-$(date -u +%Y%m%d%H%M%S)
   ```
4. Back up the SQLite DB and WAL sidecars:
   ```bash
   backup_dir=/opt/docker/corescope/backups/$(date -u +%Y%m%d%H%M%S)
   mkdir -p "$backup_dir"
   cp -a /opt/docker/corescope/data/corescope/data/meshcore.db* \
     "$backup_dir"/
   ```
5. Start Postgres:
   ```bash
   docker compose -f docker-compose.live.yml up -d postgres
   ```
6. Build the approved app image locally if `CORESCOPE_IMAGE` is not already
   pulled:
   ```bash
   docker compose -f docker-compose.live.yml build corescope
   ```
7. Run the one-shot migration:
   ```bash
   docker compose -f docker-compose.live.yml run --rm --no-deps \
     --entrypoint /app/corescope-migrate-postgres \
     corescope \
     -sqlite /app/data/meshcore.db \
     -postgres "$DATABASE_URL" \
     -truncate
   ```
8. Start the Postgres-backed live app on the same public ports:
   ```bash
   docker compose -f docker-compose.live.yml up -d corescope
   ```
9. Verify:
   ```bash
   curl -fsS https://live.meshcore.ca/api/stats
   curl -fsS https://live.meshcore.ca/api/perf/db
   docker logs corescope --tail 200
   docker logs corescope-postgres --tail 100
   ```

Also verify the browser dashboard, WebSocket live feed, MQTT subscription logs,
map, packets, nodes, channels, and Perf page before declaring the cutover done.

## Rollback

Rollback leaves the SQLite data untouched:

```bash
docker compose -f docker-compose.live.yml down
docker rename corescope-sqlite-rollback-YYYYMMDDHHMMSS corescope
docker start corescope
```

If the rollback container was removed, re-run the previous image with the same
mounts from the baseline section. Do not run the SQLite-backed app and the
Postgres-backed app as simultaneous writers.
