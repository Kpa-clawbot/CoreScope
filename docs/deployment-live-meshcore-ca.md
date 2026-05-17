# Deploying live.meshcore.ca

## Recommended baseline
- `DISABLE_MOSQUITTO=true`
- Do **not** publish port 1883 publicly for production.
- Use external MQTT brokers (`mqtt1.meshcore.ca`, `mqtt2.meshcore.ca`) via WSS/TLS on 443.
- Use Postgres for new production deployments:
  - `DB_DRIVER=postgres`
  - `DATABASE_URL=postgres://...`
  - keep SQLite available only as the rollback source until the cutover is accepted.

## Caddy / reverse proxy
- Route public HTTPS traffic to the Go server service.
- Prefer Cloudflare Tunnel or equivalent reverse-proxy ingress.
- Keep Caddyfile generated/mounted from `caddy-config/` for environment-specific hostnames.

## Side-by-side dev validation

The live host can run the dev CoreScope container next to the live service as
long as the dev instance uses its own ports and database:

- live public service: port `443`
- dev public service: port `8443`
- live SQLite data: read-only backup source only
- dev data: separate Postgres container and separate Docker volumes

For 0.5 Alpha verification, build from the candidate branch, run the dev
Compose project on `8443`, migrate from a copied SQLite backup into the dev
Postgres container, then point only the dev config at the MQTT broker. Keep
credentials in the mounted dev config or environment file; do not commit them.

Do not bind both containers to the same SQLite file. For a live-shaped test,
copy or back up the live SQLite database, restore it into dev Postgres with
`/app/corescope-migrate-postgres`, then start the dev container with the live
MQTT broker credentials mounted through its own config file.

## SQLite to Postgres cutover

1. Stop the target CoreScope container or take an application-level backup.
2. Preserve the SQLite database and WAL sidecar files together if the container
   is still using WAL mode.
3. Start the Postgres service.
4. Run the migration tool from the same image that will run CoreScope:
   ```bash
   /app/corescope-migrate-postgres \
     -sqlite /path/to/meshcore.db \
     -postgres "$DATABASE_URL" \
     -truncate
   ```
5. Confirm row counts for the core tables:
   `transmissions`, `observations`, `observers`, `nodes`,
   `observer_metrics`, `dropped_packets`, and `neighbor_edges`.
6. Start CoreScope with `DB_DRIVER=postgres` and the same `DATABASE_URL`.
7. Verify `/api/perf/db`, `/api/stats`, `/api/packets`, live WebSocket
   broadcasts, and MQTT subscription logs before switching traffic.

Rollback is to stop the Postgres-backed container and restart the previous
SQLite-backed container against the preserved SQLite data. Do not run both
writers at the same time.
