# MeshCore MQTT Ingestor (Go)

Standalone MQTT ingestion service for CoreScope. Connects to MQTT brokers, decodes raw MeshCore packets, and writes to PostgreSQL. The separate Go API server uses a restricted reader credential.

The migration command owns schema changes. The ingestor only connects to an initialized, ready telemetry schema.

## Architecture

```
MQTT Broker(s)  →  Go Ingestor  →  PostgreSQL  ←  Go API Server
                    (this binary)     (shared)
```

- **Single binary** — PostgreSQL runtime driver is pure Go
- **PostgreSQL 18** via pgx through `database/sql`
- **MQTT** via `github.com/eclipse/paho.mqtt.golang`
- Runs alongside the Go API server with separate writer and reader roles
- Does not serve HTTP/WebSocket — that stays in the Go API server

## Build

Requires Go 1.25+.

```bash
cd cmd/ingestor
go build -o corescope-ingestor .
```

Cross-compile for Linux (e.g., for the production VM):

```bash
GOOS=linux GOARCH=amd64 go build -o corescope-ingestor .
```

## Run

```bash
./corescope-ingestor -config /path/to/config.json
```

The ingestor reads `mqttSources` (or the legacy `mqtt` object), `databaseURL` and `stateDir`. `-database-url` and `-state-dir` override configuration. `dbPath` and `-db` are never interpreted as database URLs: import existing SQLite snapshots offline first. Do not place connection URLs in command logs.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CORESCOPE_WRITER_DATABASE_URL` | Ingestor-specific PostgreSQL writer URL; overrides the generic URL | required when no generic/config URL exists |
| `CORESCOPE_DATABASE_URL` | Generic PostgreSQL URL for standalone runs | config `databaseURL` |
| `CORESCOPE_STATE_DIR` | Local queue/statistics directory shared with the API server | `data` |
| `CORESCOPE_APPROVED_CHANNELS_DATABASE_URL` | Separate restricted account reader for `approved_channels` view | config `userManagement.approvedChannelsDatabaseURL` |
| `MQTT_BROKER` | Single MQTT broker URL (overrides config) | — |
| `MQTT_TOPIC` | MQTT topic (used with `MQTT_BROKER`) | `meshcore/#` |
| `CORESCOPE_INGESTOR_STATS` | Path to the per-second stats JSON file consumed by the server's `/api/perf/io` and `/api/perf/write-sources` endpoints (#1120) | `<stateDir>/ingestor-stats.json` |

### Stats file (`CORESCOPE_INGESTOR_STATS`)

Every second the ingestor publishes a JSON snapshot of its counters
(`tx_inserted`, `obs_inserted`, `walCommits`, `backfillUpdates.*`, etc.) plus
a `procIO` block sampled from `/proc/self/io` (read/write/cancelled bytes per
second + syscall counts). The server reads this file and surfaces the data on
the Perf page so operators can self-diagnose write-volume anomalies.

The writer uses `O_NOFOLLOW | O_CREAT | O_TRUNC` mode `0o600`, so a
pre-planted symlink at the path cannot be used to clobber an arbitrary file.

The state directory is created privately and stores small queue/statistics files.
Database URLs never become filesystem paths.

### Minimal Config

```json
{
  "databaseURL": "postgres://telemetry_writer@localhost/telemetry",
  "stateDir": "data",
  "mqttSources": [
    {
      "name": "local",
      "broker": "mqtt://localhost:1883",
      "topics": ["meshcore/#"]
    }
  ]
}
```

### Full Config

The ingestor reads these fields from the existing `config.json`:

- `mqttSources[]` — array of MQTT broker connections
  - `name` — display name for logging
  - `broker` — MQTT URL (`mqtt://`, `mqtts://`)
  - `username` / `password` — auth credentials
  - `topics` — array of topic patterns to subscribe
  - `iataFilter` — optional regional filter
  - `clientId`: optional MQTT ClientID. Default `corescope-<name>-<random>`, new suffix per ingestor start. Two running ingestors must not share a value on one broker
- `mqtt` — legacy single-broker config (auto-converted to `mqttSources`)
- `databaseURL` — restricted telemetry writer URL; schema-owner credentials are refused
- `stateDir` — local queue/statistics directory (default: `data`)

## Test

```bash
cd cmd/ingestor
CORESCOPE_TEST_POSTGRES_URL=postgres://test_admin@localhost/postgres go test -v ./...
```

## What It Does

1. Connects to configured MQTT brokers with auto-reconnect
2. Subscribes to mesh packet topics (e.g., `meshcore/+/+/packets`)
3. Receives raw hex packets via JSON messages (`{ "raw": "...", "SNR": ..., "RSSI": ... }`)
4. Decodes MeshCore packet headers, paths, and payloads (ported from `decoder.js`)
5. Computes content hashes (path-independent, SHA-256-based)
6. Writes atomically to PostgreSQL: `transmissions` + `observations` tables
7. Upserts `nodes` from decoded ADVERT packets (with validation)
8. Upserts `observers` from MQTT topic metadata

## Schema Compatibility

The Go ingestor creates the same v3 schema as the Node.js server:

- `transmissions` — deduplicated by content hash
- `observations` — per-observer sightings with `observer_idx` (rowid reference)
- `nodes` — mesh nodes discovered from adverts
- `observers` — MQTT feed sources

Both processes can write to the same DB concurrently (SQLite WAL mode).

## What's Not Ported (Yet)

- Companion bridge format (Format 2 — `meshcore/advertisement`, channel messages, etc.)
- Channel key decryption (GRP_TXT encrypted payload decryption)
- WebSocket broadcast to browsers
- In-memory packet store
- Cache invalidation

These stay in the Node.js server for now.

## Files

```
cmd/ingestor/
  main.go          — entry point, MQTT connect, message handler
  decoder.go       — MeshCore packet decoder (ported from decoder.js)
  decoder_test.go  — decoder tests (25 tests, golden fixtures)
  db.go            — SQLite writer (schema-compatible with db.js)
  db_test.go       — DB tests (schema validation, insert/upsert, E2E)
  config.go        — config struct + loader
  util.go          — shared utilities
  go.mod / go.sum  — Go module definition
```

PostgreSQL autovacuum, automatic analyze and checkpoints are managed by the
PostgreSQL service, not by runtime owner privileges. The previous SQLite
`db.vacuumOnStartup`, `db.incrementalVacuumPages` and `db.analysisLimit`
settings have no PostgreSQL effect. The migration/restore procedure must run
`ANALYZE` after bulk imports; runtime credentials cannot alter planner targets.
Packet retention remains bounded to 250 transmissions per transaction.
