# CoreScope

[![Go Server Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/MeshCore-ca/CoreScope/master/.badges/go-server-coverage.json)](https://github.com/MeshCore-ca/CoreScope/actions/workflows/deploy.yml)
[![Go Ingestor Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/MeshCore-ca/CoreScope/master/.badges/go-ingestor-coverage.json)](https://github.com/MeshCore-ca/CoreScope/actions/workflows/deploy.yml)
[![E2E Tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/MeshCore-ca/CoreScope/master/.badges/e2e-tests.json)](https://github.com/MeshCore-ca/CoreScope/actions/workflows/deploy.yml)
[![Frontend Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/MeshCore-ca/CoreScope/master/.badges/frontend-coverage.json)](https://github.com/MeshCore-ca/CoreScope/actions/workflows/deploy.yml)
[![Deploy](https://github.com/MeshCore-ca/CoreScope/actions/workflows/deploy.yml/badge.svg)](https://github.com/MeshCore-ca/CoreScope/actions/workflows/deploy.yml)

High-performance MeshCore packet analyzer with a **Go backend** and **vanilla JS frontend** (no build step, no framework).

CoreScope ingests packets from MQTT, decodes/stores them in SQLite, serves REST APIs + WebSocket updates from an in-memory store, and provides a browser UI for live packet analysis, maps, node analytics, and channel activity.

## Current project state (May 2026)

- **Backend architecture is Go-only.** The deprecated Node.js backend server (`server.js`) has been removed.
- **Two Go binaries are active:**
  - `cmd/ingestor`: MQTT ingestion + decode + SQLite writes
  - `cmd/server`: REST API + WebSocket + static frontend serving
- **Frontend is active and framework-free** in `public/` (one file per page/feature).
- **SQLite is persistence; in-memory indexes power fast API reads.**
- **Cache-busting is automatic** at server startup by replacing `__BUST__` in `public/index.html`.
- **Test suite is mixed-language:** Go tests for backend + Node-based test harnesses for frontend/route/behavior coverage.

## Features

- Live packet feed with filtering and detail panes
- Leaflet-based map and live/VCR playback view
- Node directory + deep node details and status signals
- Analytics tabs (RF, topology, hash/collision-related views, etc.)
- Channel message viewer and decryption support
- Theme customizer (CSS variables)
- Shareable deep links for key UI states

## Architecture

1. MQTT packet streams arrive at **Go ingestor** (`cmd/ingestor/`).
2. Ingestor decodes packets and writes to SQLite.
3. **Go server** (`cmd/server/`) polls/loads data and maintains in-memory query structures.
4. Server exposes `/api/*`, broadcasts live updates via WebSocket, and serves `public/` assets.

## Repository layout

```text
cmd/server/        Go API server (REST + WS + static files)
cmd/ingestor/      Go MQTT ingestor (separate binary)
public/            Vanilla JS SPA + CSS (active frontend)
scripts/           Tooling/utilities
test-fixtures/     SQLite fixtures and test data assets
docs/              Deployment, guides, specs, and migration notes
```

## Quick start

### Docker (recommended)

```bash
docker run -d --name corescope \
  --restart=unless-stopped \
  -p 80:80 -p 1883:1883 \
  -v /your/data:/app/data \
  ghcr.io/kpa-clawbot/corescope:latest
```

See full deployment options in `docs/deployment.md`.

### Local source setup

```bash
git clone https://github.com/MeshCore-ca/CoreScope.git
cd CoreScope
./manage.sh setup
```

## Configuration

Primary runtime config is `config.json` (copy from `config.example.json`). Common fields include:

- `port`
- `dbPath` or `db` (`driver`, `url`, `path`, pool limits)
- `mqtt` (local broker/topic)
- `mqttSources` (additional brokers)
- `channelKeys`
- `defaultRegion`

Environment overrides include `PORT`, `DB_DRIVER`, `DATABASE_URL`, and `DB_PATH`.
Docker Compose starts Postgres by default; set `DB_DRIVER=sqlite` and unset
`DATABASE_URL` to keep using the legacy SQLite file at `DB_PATH`.

SQLite to Postgres migration is a one-shot tool:

```bash
cd cmd/migrate-postgres
go run . -sqlite ../../data/meshcore.db -postgres "$DATABASE_URL"
```

The Docker image also includes `/app/corescope-migrate-postgres`. Use
`docker-compose.dev.yml` for a live-style side-by-side dev deployment: it starts
a separate Postgres container and exposes the dev UI on port `8443` without
reusing the live SQLite or Caddy data directories.

## Testing

### Required backend tests

```bash
cd cmd/server && go test ./...
cd cmd/ingestor && go test ./...
```

### Fast frontend logic checks

```bash
node test-packet-filter.js
node test-aging.js
node test-frontend-helpers.js
```

### Full local pipeline

```bash
npm test
npm run test:unit
npm run test:coverage
npm run test:full-coverage
```

### Browser E2E

```bash
node test-e2e-playwright.js
```

Default E2E target is localhost; do not run tests against production endpoints.

## Notes for contributors

- Do not reintroduce Node backend server files (`server.js`, etc.).
- Keep frontend changes framework-free and build-step-free.
- Prefer shared helpers over duplicated logic.
- Validate API response shapes before wiring UI assumptions.
- Performance matters: avoid per-item API calls and hot-path O(n²) loops.

---

Additional references:

- Deployment guide: `docs/deployment.md`
- Go migration notes: `docs/go-migration.md`
- Public mode profile: `docs/public-mode.md`
