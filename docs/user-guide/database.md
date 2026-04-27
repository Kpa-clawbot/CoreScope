# Database

CoreScope uses SQLite in WAL (Write-Ahead Log) mode for both the server
(read-only) and ingestor (read-write).

## WAL mode

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
2. Set `db.vacuumOnStartup: true` in your `config.json`:
   ```json
   {
     "db": {
       "vacuumOnStartup": true
     }
   }
   ```
3. Restart CoreScope. The one-time `VACUUM` will run and block startup.
4. After migration, remove or set `vacuumOnStartup: false` — it's not needed again.

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

## Checking current mode

```bash
sqlite3 data/meshcore.db "PRAGMA auto_vacuum;"
```

- `0` = NONE (default for old databases)
- `1` = FULL (automatic, but slower writes)
- `2` = INCREMENTAL (recommended — CoreScope triggers vacuum after deletes)
