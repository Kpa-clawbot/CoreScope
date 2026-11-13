# MeshCore MQTT Ingestor (Go)

MQTT ingestion service for CoreScope. Connects to MQTT brokers, decodes raw MeshCore packets, and writes them to the analyzer database, which is SQLite (default) or PostgreSQL. The Go web server (`cmd/server/`) reads that database and serves the API and UI.

The ingestor is the only process that writes the analyzer database: inserts, schema migrations, retention and maintenance all run here. The server opens it read-only.

## Architecture

```
MQTT Broker(s)  →  Go Ingestor  →  SQLite file or PostgreSQL  ←  Go Web Server (read-only)
                    (this binary)          (shared)
```

- **Single static binary** — no runtime dependencies
- **SQLite** via `github.com/mattn/go-sqlite3` (cgo; cross-compiled with `zig cc`, see the root `Makefile`)
- **MQTT** via `github.com/eclipse/paho.mqtt.golang`
- **PostgreSQL** via `pgx` (pure Go), selected by `db.driver` (see below)
- Runs **alongside** the Go server and shares the database with it
- Does NOT serve HTTP/WebSocket — that is `cmd/server`

## Build

Requires Go 1.22+.

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

The config file is the same `config.json` the server reads. The ingestor reads the `mqttSources` array (or legacy `mqtt` object), `dbPath`, `db` and `retention` fields.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DB_PATH` | SQLite database path (with PostgreSQL it anchors the directory for the file queues shared with the server) | `data/meshcore.db` |
| `DB_DRIVER` | `sqlite` or `postgres`; overrides `db.driver` | `sqlite` |
| `DB_DSN` | PostgreSQL connection URL; overrides `db.dsn`. Never logged | — |
| `MQTT_BROKER` | Single MQTT broker URL (overrides config) | — |
| `MQTT_TOPIC` | MQTT topic (used with `MQTT_BROKER`) | `meshcore/#` |
| `CORESCOPE_INGESTOR_STATS` | Path to the per-second stats JSON file consumed by the server's `/api/perf/io` and `/api/perf/write-sources` endpoints (#1120) | `/tmp/corescope-ingestor-stats.json` |

### Stats file (`CORESCOPE_INGESTOR_STATS`)

Every second the ingestor publishes a JSON snapshot of its counters
(`tx_inserted`, `obs_inserted`, `walCommits`, `backfillUpdates.*`, etc.) plus
a `procIO` block sampled from `/proc/self/io` (read/write/cancelled bytes per
second + syscall counts). The server reads this file and surfaces the data on
the Perf page so operators can self-diagnose write-volume anomalies.

The writer uses `O_NOFOLLOW | O_CREAT | O_TRUNC` mode `0o600`, so a
pre-planted symlink at the path cannot be used to clobber an arbitrary file.

**Security note:** the default lives in `/tmp`, which is world-writable on
most hosts (sticky bit only protects deletion, not creation). On
shared/multi-tenant hosts, override `CORESCOPE_INGESTOR_STATS` to point at a
private directory (e.g. `/var/lib/corescope/ingestor-stats.json`) that only
the corescope user can write to.

### Minimal Config

```json
{
  "dbPath": "data/meshcore.db",
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
- `dbPath` — SQLite DB path (default: `data/meshcore.db`)
- `db.driver` — `sqlite` (default) or `postgres`
- `db.dsn` — PostgreSQL URL, required for `postgres`, e.g. `postgres://corescope_writer@host:5432/corescope?sslmode=require`. It may hold a password: keep it in `DB_DSN` or a private file, out of git, and never on a command line

## PostgreSQL

With `db.driver` set to `postgres` (or `DB_DRIVER=postgres`) the ingestor writes to PostgreSQL 16+ instead of a SQLite file. Everything else (MQTT, decoding, retention) is unchanged.

- The role in the DSN must own the schema: the ingestor creates and migrates it at start (versioned migrations in `schema_version`, under a PostgreSQL advisory lock). `scripts/postgres-roles.sh` creates a writer role for this and a read-only role for the server.
- **Empty-database guard.** The ingestor refuses to start on an empty PostgreSQL database while the SQLite file at `dbPath` still holds transmissions, so a forgotten copy cannot silently start a fresh history. Either copy the data first with `corescope-migrate copy -from sqlite:<path> -to <postgres url>` (see `cmd/migrate/README.md`), or move the SQLite file aside to start with an empty database on purpose.
- It keeps one database connection on both backends: ids must commit in increasing order because the server tails new rows by `id`.
- Operator guide: [docs/postgresql.md](../../docs/postgresql.md).

### SQLite-only maintenance

These run only on SQLite and are skipped on PostgreSQL, which leaves them to autovacuum (the ingestor logs this once at start): WAL checkpoints, incremental vacuum, planner statistics refresh, and the settings `db.vacuumOnStartup`, `db.incrementalVacuumPages` and `db.analysisLimit`. `retention.hopIndexDays` (the observation hop index) is SQLite-only too: on PostgreSQL the ingestor logs one warning and treats it as 0. The `retention.*Days` windows work on both.

## Test

```bash
cd cmd/ingestor
go test -v ./...
```

## What It Does

1. Connects to configured MQTT brokers with auto-reconnect
2. Subscribes to mesh packet topics (e.g., `meshcore/+/+/packets`)
3. Receives raw hex packets via JSON messages (`{ "raw": "...", "SNR": ..., "RSSI": ... }`)
4. Decodes MeshCore packet headers, paths, and payloads
5. Computes content hashes (path-independent, SHA-256-based)
6. Writes to the database: `transmissions` + `observations` tables
7. Upserts `nodes` from decoded ADVERT packets (with validation)
8. Upserts `observers` from MQTT topic metadata

## Schema Compatibility

The ingestor owns the schema on both backends and creates and migrates it at start:

- `transmissions` — deduplicated by content hash
- `observations` — per-observer sightings with `observer_idx` (references `observers.idx`)
- `nodes` — mesh nodes discovered from adverts
- `observers` — MQTT feed sources, with an explicit `idx INTEGER PRIMARY KEY`

SQLite changes go through `applySchema` / `dbschema.Apply` (see `MIGRATIONS.md`), PostgreSQL changes through the embedded migrations in `internal/dbschema/pgschema/migrations`. A test (`TestPgSchemaParity`) keeps the two schemas equivalent. The server only reads: on SQLite the file is opened read-only (WAL mode lets it read while the ingestor writes).

## Files

```
cmd/ingestor/
  main.go          — entry point, MQTT connect, message handler
  decoder.go       — MeshCore packet decoder
  decoder_test.go  — decoder tests (golden fixtures)
  db.go            — database writer (SQLite and shared logic)
  db_postgres.go   — PostgreSQL open, schema apply and the empty-database guard
  db_test.go       — DB tests (schema validation, insert/upsert, E2E)
  config.go        — config struct + loader
  util.go          — shared utilities
  go.mod / go.sum  — Go module definition
```
