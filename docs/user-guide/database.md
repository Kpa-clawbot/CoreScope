# Database

CoreScope can run against Postgres or SQLite. New Docker Compose deployments
default to Postgres when `DATABASE_URL` is set; SQLite remains supported for
single-host deployments and rollback.

## Backend Selection

Configuration precedence is environment first, then `config.json`, then the
legacy `dbPath` fallback:

| Field | Default | Description |
|-------|---------|-------------|
| `DB_DRIVER` / `db.driver` | `sqlite` | `postgres` or `sqlite`. `DATABASE_URL` selects Postgres unless `DB_DRIVER` is set. |
| `DATABASE_URL` / `db.url` | empty | Postgres URL, for example `postgres://corescope:corescope@postgres:5432/corescope?sslmode=disable`. |
| `DB_PATH` / `db.path` / `dbPath` | `data/meshcore.db` | SQLite database path and rollback fallback. |
| `db.maxOpenConns` | server `16`, ingestor `8` | Postgres pool max when greater than zero. |
| `db.maxIdleConns` | server `4`, ingestor `2` | Postgres idle pool max when greater than zero. |

Both the server and ingestor initialize the Postgres schema on startup. The
schema keeps explicit observer row IDs so existing packet and observation joins
remain compatible with SQLite-era data.

## SQLite To Postgres Migration

Stop CoreScope before migrating, then run:

```bash
cd cmd/migrate-postgres
go run . -sqlite ../../data/meshcore.db -postgres "$DATABASE_URL"
```

The tool preserves table IDs, copies the core tables, resets Postgres
sequences, and validates row counts. Use `-truncate` only when intentionally
reloading an existing Postgres database.

Docker images also include `/app/corescope-migrate-postgres` so operators do
not need Go installed on deployment hosts. For a side-by-side dev copy, start
the dev Postgres service first, then run the migration through Compose:

```bash
docker compose -f docker-compose.dev.yml up -d postgres
docker compose -f docker-compose.dev.yml run --rm --no-deps \
  --entrypoint /app/corescope-migrate-postgres \
  corescope-dev \
  -sqlite /app/data/meshcore.db \
  -postgres "postgres://corescope:corescope@postgres:5432/corescope_dev?sslmode=disable" \
  -truncate
```

Keep live SQLite and dev Postgres on separate data directories. The live
database can be copied into the dev data directory before this command, but two
containers must not write to the same SQLite file. For WAL-mode SQLite, copy
`meshcore.db`, `meshcore.db-wal`, and `meshcore.db-shm` together or migrate
from a stopped-container backup.

## Performance Evidence

Postgres is not assumed to make the dashboard faster by itself. Use
`/api/perf/db` for engine-neutral row counts, DB size, and pool stats, and use
`/api/perf` for endpoint latency and packet-store behavior. If a dashboard path
is served from the in-memory packet store or TTL cache, the database engine may
not be the bottleneck.

## SQLite WAL Mode

WAL mode allows concurrent reads while writes happen. It is set automatically
at connection time via `PRAGMA journal_mode=WAL`. No operator action needed.

The WAL file (`meshcore.db-wal`) grows during writes and is checkpointed
(merged back into the main DB) periodically and at clean shutdown.

## Auto-vacuum

By default, SQLite does not shrink the database file after `DELETE` operations.
Deleted pages are marked free and reused by future writes, but the file size
on disk stays the same. This is surprising when lowering retention settings.

### New databases

Databases created after this feature was added automatically have
`PRAGMA auto_vacuum = INCREMENTAL`. After each retention reaper cycle,
CoreScope runs `PRAGMA incremental_vacuum(N)` to return free pages to the OS.

### Existing databases

The `auto_vacuum` mode is stored in the database header and can only be changed
by rewriting the entire file with `VACUUM`. CoreScope will **not** do this
automatically — on large databases (5+ GB seen in the wild) it takes minutes
and holds an exclusive lock.

**To migrate an existing database:**

1. At startup, CoreScope logs a warning:
   ```
   [db] auto_vacuum=NONE — DB needs one-time VACUUM to enable incremental auto-vacuum.
   ```
2. **Ensure at least 2× the database file size in free disk space.** Full VACUUM
   creates a temporary copy of the entire file — on a near-full disk it will fail.
3. Set `db.vacuumOnStartup: true` in your `config.json`:
   ```json
   {
     "db": {
       "vacuumOnStartup": true
     }
   }
   ```
4. Restart CoreScope. The one-time `VACUUM` will run and block startup.
5. After migration, remove or set `vacuumOnStartup: false` — it's not needed again.

### Configuration

| Field | Default | Description |
|-------|---------|-------------|
| `db.vacuumOnStartup` | `false` | One-time full VACUUM to enable incremental auto-vacuum |
| `db.incrementalVacuumPages` | `1024` | Pages returned to OS per reaper cycle |

## Manual VACUUM

You can also run a manual vacuum from the SQLite CLI:

```bash
sqlite3 data/meshcore.db "PRAGMA auto_vacuum = INCREMENTAL; VACUUM;"
```

This is equivalent to `vacuumOnStartup: true` but can be done offline.

> ⚠️ Full VACUUM requires **2× the database file size** in free disk space (it
> creates a temporary copy). Check with `ls -lh data/meshcore.db` before running.

## Checking current mode

```bash
sqlite3 data/meshcore.db "PRAGMA auto_vacuum;"
```

- `0` = NONE (default for old databases)
- `1` = FULL (automatic, but slower writes)
- `2` = INCREMENTAL (recommended — CoreScope triggers vacuum after deletes)

See [#919](https://github.com/Kpa-clawbot/CoreScope/issues/919) for background on this feature.
