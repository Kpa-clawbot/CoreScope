# CoreScope

[![Go Server Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/.badges/go-server-coverage.json)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)
[![Go Ingestor Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/.badges/go-ingestor-coverage.json)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)
[![E2E Tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/.badges/e2e-tests.json)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)
[![Frontend Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/.badges/frontend-coverage.json)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)
[![Deploy](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml/badge.svg)](https://github.com/Kpa-clawbot/CoreScope/actions/workflows/deploy.yml)

> MeshCore packet analysis with a Go backend, SQLite persistence by default, with optional PostgreSQL, real-time WebSocket updates and channel decryption.

Self-hosted, open-source MeshCore packet analyzer. Collects MeshCore packets via MQTT, decodes them in real time, and presents a full web UI with live packet feed, interactive maps, channel chat, packet tracing, and per-node analytics.

## ⚡ Performance

The Go backend combines a packet cache with SQLite or PostgreSQL storage and SQL-backed analytics. SQLite is the default. The ingestor owns telemetry writes; the server reads telemetry and uses a separate optional account store. Ordinary updates keep the recorded backend. Use the [offline storage workflow](docs/storage.md) to change engines without discarding existing data.

Budget for the application and PostgreSQL together, including retained data, indexes, WAL and the packet cache. Historical SQLite measurements in [PERFORMANCE.md](PERFORMANCE.md) are baseline context, not PostgreSQL results. Use the [paired benchmark workflow](.github/workflows/postgres-benchmark.yml) for an exact candidate comparison.

## ✨ Features

### 📡 Live Trace Map
Real-time animated map with packet route visualization, VCR-style playback controls, and a retro LCD clock. Replay the last 24 hours of mesh activity, scrub through the timeline, or watch packets flow live at up to 4× speed.

![Live VCR playback — watch packets flow across the Bay Area mesh](docs/screenshots/MeshVCR.gif)

### 📦 Packet Feed
Filterable real-time packet stream with byte-level breakdown, Excel-like resizable columns, and a detail pane. Toggle "My Nodes" to focus on your mesh.

![Packets view](docs/screenshots/packets1.png)

### 🗺️ Network Overview
At-a-glance mesh stats — node counts, packet volume, observer coverage.

![Network overview](docs/screenshots/mesh-overview.png)

### 📊 Node Analytics
Per-node deep dive with interactive charts: activity timeline, packet type breakdown, SNR distribution, hop count analysis, peer network graph, and hourly heatmap.

![Node analytics](docs/screenshots/node-analytics.png)

### 💬 Channel Chat
Decoded group messages with sender names, @mentions, timestamps — like reading a Discord channel for your mesh.

![Channels](docs/screenshots/channels1.png)

### 📱 Mobile Ready
Full experience on your phone — proper touch controls, iOS safe area support, and a compact VCR bar.

<img src="docs/screenshots/Live-view-iOS.png" alt="Live view on iOS" width="300">

### And More

- **11 Analytics Tabs** — RF, topology, channels, hash stats, distance, route patterns, and more
- **Node Directory** — searchable list with role tabs, detail panel, QR codes, advert timeline
- **Packet Tracing** — follow individual packets across observers with SNR/RSSI timeline
- **Observer Status** — health monitoring, packet counts, uptime, per-observer analytics
- **Hash Collision Matrix** — detect address collisions across the mesh
- **Channel Key Auto-Derivation** — hashtag channels (`#channel`) keys derived via SHA256
- **Multi-Broker MQTT** — connect to multiple brokers with per-source IATA filtering
- **Dark / Light Mode** — auto-detects system preference, map tiles swap too
- **Theme Customizer** — design your theme in-browser, export as `theme.json`
- **Global Search** — search packets, nodes, and channels (Ctrl+K)
- **Shareable URLs** — deep links to packets, channels, and observer detail pages
- **Protobuf API Contract** — typed API definitions in `proto/`
- **Accessible** — ARIA patterns, keyboard navigation, screen reader support

## Quick Start

### Pre-built image — SQLite by default

Retain the complete repository at the same reviewed revision as the image. The optional PostgreSQL overrides use the matching shared initialization scripts.

```bash
git clone https://github.com/Kpa-clawbot/CoreScope.git
cd CoreScope
git checkout --detach "$CORESCOPE_REF"
test -f .env || cp .env.example .env
chmod 600 .env
# No PostgreSQL service or credentials are needed for the default SQLite install.
# Set CORESCOPE_IMAGE to the matching reviewed image tag or digest.
docker compose -f docker-compose.example.yml up -d
```

Open `http://localhost` and verify `/api/healthz` plus actual ingestion. Packaged setup validates existing SQLite data or initializes a provably fresh installation before the application starts. Keep the existing `DATA_DIR` on updates. For a new PostgreSQL install, merge `.env.postgres.example` into the private `.env` and add `-f docker-compose.example.postgres.yml`; existing installations use the verified [storage switch](docs/storage.md). See [DEPLOY.md](DEPLOY.md) for ports, MQTT, HTTPS and backups.

### Build from source

Use the same complete checkout and private `.env`, then run `./manage.sh setup`. The wizard offers SQLite (default) or PostgreSQL, configures, builds and starts the deployment. It keeps an existing recorded backend; PostgreSQL credentials are generated only for a new empty database directory, preserving existing private settings.

```bash
./manage.sh status
./manage.sh logs
./manage.sh backup ./backups/instance
./manage.sh update
```

### Configure

Copy `config.example.json` to `config.json` and edit:

```json
{
  "port": 3000,
  "mqtt": {
    "broker": "mqtt://localhost:1883",
    "topic": "meshcore/+/+/packets"
  },
  "mqttSources": [
    {
      "name": "remote-feed",
      "broker": "mqtts://remote-broker:8883",
      "topics": ["meshcore/+/+/packets"],
      "username": "user",
      "password": "pass",
      "iataFilter": ["SJC", "SFO", "OAK"]
    }
  ],
  "channelKeys": {
    "public": "8b3387e9c5cdea6ac9e5edbaa115cd72"
  },
  "defaultRegion": "SJC"
}
```

| Field | Description |
|-------|-------------|
| `port` | HTTP server port (default: 3000) |
| `mqtt.broker` | Local MQTT broker URL (`""` to disable) |
| `mqttSources` | External MQTT broker connections (optional) |
| `channelKeys` | Channel decryption keys (hex). Hashtag channels auto-derived via SHA256 |
| `defaultRegion` | Default IATA region code for the UI |
| `db.backend` | Initial choice: `sqlite` (default) or `postgres`; the installation record wins after setup |
| `dbPath` | SQLite telemetry path; `DB_PATH` is its environment override |
| `databaseURL` | Optional PostgreSQL telemetry URL; prefer the per-process role credentials below |
| `stateDir` | Filesystem directory for queues, statistics and account backups (default: `data`) |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `PORT` | Override config port |
| `CORESCOPE_DB_BACKEND` | Bootstrap choice only; use an offline switch to change an installed backend |
| `CORESCOPE_READER_DATABASE_URL` | Server telemetry reader URL |
| `CORESCOPE_WRITER_DATABASE_URL` | Ingestor telemetry writer URL |
| `CORESCOPE_USERS_DATABASE_URL` | Optional account writer URL in a separate database |
| `CORESCOPE_APPROVED_CHANNELS_DATABASE_URL` | Ingestor's restricted approved-channel reader URL |
| `CORESCOPE_STATE_DIR` | Shared state-directory path; never a database URL |

## Architecture

```mermaid
flowchart LR
    MQTT[MQTT observers] --> Ingestor[Go ingestor]
    Ingestor -->|telemetry writer| Telemetry[(SQLite or PostgreSQL telemetry)]
    Telemetry -->|read-only role| Server[Go API server]
    Server <-->|account writer| Accounts[(Separate account database)]
    Accounts -->|approved-channel view| Ingestor
    Server <-->|REST and WebSocket| Browser
```

**Two-process model:** The ingestor handles MQTT, decoding, telemetry writes and retention. The HTTP server loads its packet cache and serves REST/WebSocket requests using a read-only telemetry role. Accounts use a separate SQLite file or PostgreSQL database. Supervisord runs the application processes with Caddy and optional Mosquitto; PostgreSQL runs as a separate service only when selected. Bootstrap/conversion credentials are not runtime credentials.

## MQTT Setup

1. **Flash an observer node** with `MESH_PACKET_LOGGING=1` build flag
2. **Connect via USB** to a host running [meshcoretomqtt](https://github.com/Cisien/meshcoretomqtt)
3. **Configure meshcoretomqtt** with your IATA region code and MQTT broker address
4. **Packets appear** on topic `meshcore/{IATA}/{PUBKEY}/packets`

Or POST raw hex packets to `POST /api/packets` for manual injection.

## Project Structure

```
corescope/
├── cmd/
│   ├── server/              # Go HTTP server + WebSocket + REST API
│   │   ├── main.go          # Entry point
│   │   ├── routes.go        # 40+ API endpoint handlers
│   │   ├── store.go         # In-memory packet store (5 indexes)
│   │   ├── db.go            # Read-only native storage access
│   │   ├── decoder.go       # MeshCore packet decoder
│   │   ├── websocket.go     # WebSocket broadcast
│   │   └── *_test.go        # Server tests
│   └── ingestor/            # Go MQTT ingestor
│       ├── main.go          # MQTT subscription + packet processing
│       ├── decoder.go       # Packet decoder (shared logic)
│       ├── db.go            # Native writer and retention path
│       └── *_test.go        # Ingestor tests
├── proto/                   # Protobuf API definitions
├── public/                  # Vanilla JS frontend (no build step)
│   ├── index.html           # SPA shell
│   ├── app.js               # Router, WebSocket, utilities
│   ├── packets.js           # Packet feed + hex breakdown
│   ├── map.js               # Leaflet map + route visualization
│   ├── live.js              # Live trace + VCR playback
│   ├── channels.js          # Channel chat
│   ├── nodes.js             # Node directory + detail views
│   ├── analytics.js         # 11-tab analytics dashboard
│   └── style.css            # CSS variable theming (light/dark)
├── docker/
│   ├── supervisord-go.conf  # Process manager (server + ingestor)
│   ├── mosquitto.conf       # MQTT broker config
│   ├── Caddyfile            # Reverse proxy + HTTPS
│   └── entrypoint-go.sh     # Container entrypoint
├── Dockerfile               # Multi-stage Go build + Alpine runtime
├── config.example.json      # Example configuration
├── tests/                   # Node.js test suite: unit/ (test-all.sh) and e2e/ (Playwright)
├── test-all.sh              # Runs every suite in tests/unit
└── tools/                   # Generators, E2E tests, utilities
```

## For Developers

### Building

```bash
make build        # all four binaries for your machine, into dist/
make build-server # just one
make crossbuild   # static linux/amd64 + linux/arm64 binaries
```

Runtime database access uses pgx through `database/sql`. The offline importer retains
the cgo [`mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3) driver for legacy
sources, so cross-compiling that binary needs a C compiler. `make crossbuild` uses
[`zig`](https://ziglang.org/download/) as that compiler (install it and it just
works) and links statically against musl, so the result is one self-contained file
that runs on Alpine or scratch. The container build does the same thing — see
`Dockerfile`.

### Test Suite

Default Go suites exercise native SQLite without a PostgreSQL service. The explicit PostgreSQL matrix covers role boundaries, concurrency, migration and native backup/restore. The standalone frontend list remains `test-all.sh`; Playwright covers the imported fixture and accounts on/off. CI pins Go 1.27.2 and PostgreSQL 18.6.

```bash
# Default SQLite tests need no service. For the separate PostgreSQL matrix:
export CORESCOPE_TEST_BACKEND=postgres
# Point this at an isolated PostgreSQL18 administrator database.
# Keep the URL in private environment configuration; pg_dump/pg_restore 18 must be on PATH.
# CORESCOPE_TEST_POSTGRES_URL is required; database tests do not silently skip.
# Go backend tests
cd cmd/server && go test ./... -v
cd cmd/ingestor && go test ./... -v

# Or across all modules
make test

# Node.js frontend + integration tests
npm test

# Playwright E2E (requires running server on localhost:3000)
node tests/e2e/test-e2e-playwright.js
```

### Generate Test Data

```bash
node tools/generate-packets.js --api --count 200
```

### Migrating from Node.js

Historical Node.js/SQLite deployments must first reach a supported SQLite layout using the prior version's explicit upgrade procedure. They can keep SQLite. Use the [offline storage switch](docs/storage.md) only when changing engines. Preserve original data, configuration and keys; a container/image update does not perform a backend conversion.

## Contributing

Contributions welcome. Please read [AGENTS.md](AGENTS.md) for coding conventions, testing requirements, and engineering principles before submitting a PR.

**Live instance:** [analyzer.00id.net](https://analyzer.00id.net) — all API endpoints are public, no auth required.

**API Documentation:** CoreScope auto-generates an OpenAPI 3.0 spec. Browse the interactive Swagger UI at [`/api/docs`](https://analyzer.00id.net/api/docs) or fetch the machine-readable spec at [`/api/spec`](https://analyzer.00id.net/api/spec).

## License

GPL-3.0-or-later
