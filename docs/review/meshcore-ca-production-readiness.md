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

The intended live rollout is an in-place replacement of the existing
`corescope` container, not a side-by-side dev deployment:

1. Rehearse migration locally from a copied live SQLite backup.
2. Stop the current live `corescope` container during the maintenance window.
3. Rename the stopped SQLite-backed container for rollback.
4. Back up `meshcore.db`, `meshcore.db-wal`, and `meshcore.db-shm` together.
5. Start `corescope-postgres` beside the app under
   `/opt/docker/corescope/data/postgres`.
6. Run `/app/corescope-migrate-postgres` against `/app/data/meshcore.db`.
7. Start the new Postgres-backed `corescope` container on live ports `80/443`.
8. Verify dashboard pages, `/api/perf/db`, MQTT ingestion, and WebSocket live
   updates before accepting the cutover.

Do not run the SQLite-backed and Postgres-backed app containers as simultaneous
writers. Rollback should stop the Postgres-backed compose project and restart
the renamed SQLite-backed container against the preserved SQLite data.

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
