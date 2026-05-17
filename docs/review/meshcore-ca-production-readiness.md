# MeshCore.ca Production Readiness

This document records the current state of the MeshCore.ca production rollout
work as of May 17, 2026.

## Current state

- CoreScope is Go-only for the active backend: `cmd/ingestor` writes decoded
  MQTT packets and `cmd/server` serves REST, WebSocket, and static frontend
  traffic.
- Postgres is the primary backend for new MeshCore.ca deployments.
- SQLite remains supported for rollback and compatibility during migration.
- The dashboard still uses in-memory server indexes for the hottest reads, so
  Postgres should be validated with `/api/perf/db` and `/api/perf` rather than
  assumed to improve every page.
- The default UI is MeshCore Canada branded with the bundled
  `public/img/meshcore-canada-logo.png`, MeshCore.ca blue theme defaults, and
  `Canada Meshcore Corescope` home copy.

## Production-shaped validation

The local production-readiness run used a backup of the live SQLite database
and restored it into a fresh local Postgres database with the Docker image's
`/app/corescope-migrate-postgres` binary.

Validated migration counts:

| Table | Rows |
|-------|------|
| `transmissions` | 16637 |
| `observations` | 89617 |
| `observers` | 29 |
| `nodes` | 722 |
| `observer_metrics` | 4730 |
| `dropped_packets` | 64 |
| `neighbor_edges` | 928 |

After migration, the local dev container was started with:

- `DB_DRIVER=postgres`
- `DATABASE_URL` pointing at the migrated local Postgres database
- `DISABLE_CADDY=true`
- `DISABLE_MOSQUITTO=true`
- live MQTT broker config mounted read-only
- UI exposed locally on `http://localhost:3330`

The ingestor connected to `wss://mqtt1.meshcore.ca:443`, subscribed to
`meshcore/+/+/#`, and Postgres row counts continued increasing while the
server broadcast live WebSocket updates.

Verified endpoints and surfaces:

- `/api/stats`
- `/api/packets?limit=3`
- `/api/perf/db`
- `public/img/meshcore-canada-logo.png`
- WebSocket live updates
- in-app browser home view at `/#/home`

## Production cutover posture

The intended live rollout is side-by-side:

1. Keep the existing live service on port `443`.
2. Start the dev service on port `8443` with its own Postgres container and
   data volume.
3. Migrate from a live SQLite backup into dev Postgres.
4. Verify dashboard pages, `/api/perf/db`, MQTT ingestion, and WebSocket live
   updates.
5. Promote the tested Postgres-backed service after approval.

Do not run the live SQLite-backed service and the dev service as writers
against the same SQLite database. Rollback should stop the Postgres-backed
container and restart the previous SQLite-backed container against the preserved
SQLite data.

## Validation commands

The final local validation set for this state:

```bash
cd cmd/server && go test -timeout 8m ./...
cd cmd/ingestor && go test ./...
cd internal/packetpath && go test ./...
cd internal/geofilter && go test ./...
cd internal/dbconfig && go test ./...
cd internal/channel && go test ./...
cd internal/perfio && go test ./...
cd internal/sigvalidate && go test ./...
cd cmd/migrate-postgres && go test ./...
npm test
BASE_URL=http://localhost:3330 node test-logo-rebrand-e2e.js
BASE_URL=http://localhost:3330 node test-logo-default-sage-teal-e2e.js
docker build -t corescope-dockerfile-check:postgres-dev .
```

The local smoke test also verified a rebuilt Docker container against migrated
Postgres and the live MQTT broker. Credentials were mounted locally and were
not committed.
