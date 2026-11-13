# MIGRATIONS — async vs sync policy

CoreScope's ingestor applies schema/data migrations inline at boot: SQLite
in `cmd/ingestor/db.go` (`applySchema`) and `internal/dbschema` (`dbschema.Apply`),
Postgres in `internal/dbschema/pgschema` (`pgschema.Apply`). Every migration that runs synchronously blocks the
ingestor from accepting packets until it returns. On a dev DB that's
milliseconds; at prod scale (1.9M+ observations, 80K+ adverts, 2600+ nodes
on Cascadia) it can pin the boot for minutes and trigger restart loops —
the "upgrade broke prod" failure class (#791, #1483, and others).

## Every schema change ships for both backends

The measurement database can be SQLite or PostgreSQL (see AGENTS.md → Two
Database Backends). **A schema change — table, column, type, default, index,
constraint, view — is not done until all three of these land in the same PR:**

1. **The SQLite migration**, exactly as before (`applySchema` /
   `dbschema.Apply`, under the async/preflight rule below).
2. **A new Postgres migration file**
   `internal/dbschema/pgschema/migrations/NNNN_name.sql` (next number, no gaps)
   and a bump of `pgschema.LatestVersion` to it. Migrations are forward-only:
   **never edit a migration file once it has shipped** — fix it with the next
   one. Write it in the Postgres type rules (AGENTS.md); index names identical
   to SQLite's.
3. **An update of `internal/dbschema/sqlite_endstate.sql`**, the end-state
   SQLite schema of a freshly booted ingestor (`dbschema.SQLiteEndState`).

Enforced by tests that fail when any of the three is missing:

- `TestPgSchemaParity` (`cmd/ingestor`) compares a fresh `pgschema.Apply`
  with the schema a fresh SQLite `OpenStore` ends up with: tables, columns,
  type class, nullability, defaults, primary keys, foreign keys, UNIQUE
  constraints, named indexes and `packets_v` columns, plus `COLLATE "C"` on
  every Postgres text column. The only type exception is
  `observations.score`, pinned by `TestPgSchemaParityExceptions`; adding
  another is a schema decision made there, on purpose. Needs
  `CORESCOPE_TEST_PG_DSN` (skips without it — run it locally with it set,
  AGENTS.md → PostgreSQL tests).
- `TestSQLiteEndStateMatchesIngestor` (`cmd/ingestor`) compares
  `sqlite_endstate.sql` with the schema a real ingestor boot produces.
- `TestEmbeddedMigrationsMatchLatestVersion` (`internal/dbschema/pgschema`)
  fails when the last migration file and `LatestVersion` disagree.

**The one Postgres-only part: the `corescope_control` schema** (server →
ingestor requests; migration `0002_control.sql`). SQLite has no control
tables — it keeps the file queues — so a control-schema change needs only
step 2, and `TestPgSchemaParity` doesn't see it (it reads the current schema
only). Instead it changes together with `pgschema.EnsureControlGrants`,
`pgschema.ControlGrantsOK` and `scripts/postgres-roles.sh` (AGENTS.md →
Read/Write Separation Invariant). Tables the ingestor writes for the server,
such as `ingestor_stats`, are ordinary tables in both schemas and follow all
three steps.

How Postgres migrations run: `pgschema.Apply` (ingestor only; the server calls
`pgschema.AssertReady` and refuses to start until the schema is current) takes
a Postgres advisory lock for the whole run and applies each pending file in
**its own transaction**, recording it in `schema_version`. DDL on Postgres is
transactional, so a failing migration rolls back whole and is retried on the
next boot. A database whose `schema_version` is above `LatestVersion` (written
by a newer binary) is refused.

The scale rule below applies to Postgres too. A migration file runs
synchronously inside that boot transaction, so a backfill or an index build on
a large table there blocks ingest exactly like a synchronous SQLite migration
(a plain `CREATE INDEX` also blocks writes to the table while it builds). Data
backfills go through `Store.RunAsyncMigration`, scheduled where both backends
run it (as `advert_route_evidence_v1` is in `cmd/ingestor/main.go`, not in the
SQLite-only `scheduleSQLiteAsyncMigrations`), with portable SQL in `fn`. A new
Postgres database starts at the end state: the baseline seeds the legacy
migration names in `_migrations` / `_async_migrations` as done.

## The rule

**Any new `CREATE INDEX`, `ALTER TABLE`, or data-rewriting `UPDATE`/`DELETE`
in a migration file MUST do ONE of the following:**

### Option 1 — Run via `Store.RunAsyncMigration` (preferred for backfills)

```go
// Scheduled AFTER the *Store is constructed (OpenStore on SQLite; a path
// both backends take if Postgres needs the backfill too, see above).
if err := s.RunAsyncMigration(ctx, "my_migration_v1",
    func(ctx context.Context, db *sql.DB) error {
        _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ...`)
        return err
    }); err != nil {
    log.Printf("[migration/async] scheduling failed: %v", err)
}
```

- The migration is recorded as `pending_async` in the `_async_migrations`
  table **immediately** — the ingestor boots and starts ingesting.
- `fn` runs in a goroutine; the WaitGroup is shared with the rest of the
  ingestor (`Store.WaitForAsyncMigrations()` waits for everything).
- On success the row flips to `done`. A returned error matching
  `context.Canceled` (including a wrapped error) becomes `cancelled`, such
  as when shutdown interrupts a backfill. Other errors, including deadline
  expiry, and panics become `failed` even if the context was cancelled.
  Both interrupted states retain the error message and completion time.
- Idempotent: rows in `done` state short-circuit; `failed`/`cancelled`/`pending_async`
  rows are retried on the next boot.

Reference implementations: `Store.BackfillPathJSONAsync` (path_json
backfill) and the converted `obs_observer_ts_idx_v1` index build in
`OpenStore`.

### Option 2 — Annotate as preflight-cheap

Some migrations are genuinely cheap at any scale (e.g. `ALTER TABLE ADD
COLUMN`, `CREATE INDEX` on a table you know is bounded to a few thousand
rows). Annotate the migration block with a comment **on the line
immediately above the migration block** so the preflight gate recognises
the opt-out:

```go
// PREFLIGHT: async=true reason="ALTER ADD COLUMN, constant default — no table rewrite on SQLite or Postgres"
if r := db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'foo_v1'"); ...
```

The reason MUST be a real one-line justification you can defend in
review, for **each backend** the change runs on — an operation that is O(1) on
SQLite is not automatically O(1) on Postgres, or the reverse. "It's fine" is
not a reason.

### Option 3 — Opt out per PR

If the migration is genuinely safe and you don't want to add an inline
annotation, put a single line in the PR body:

```
PREFLIGHT-MIGRATION-SCALE: <30s N=80K verified on Cascadia staging snapshot
```

This must include both `<30s` and `N=<some scale>` so a reviewer can
challenge the measurement.

## The gate

`~/.openclaw/skills/pr-preflight/scripts/check-async-migrations.sh` runs
on every PR via the preflight orchestrator. It greps the diff for new or
modified migration blocks (files matching `cmd/ingestor/db.go`,
`cmd/ingestor/maintenance.go`, `internal/dbschema/**`, `**/migrations/**`,
`**/*.sql`, plus any Go file touching `CREATE INDEX` / `ALTER TABLE` /
`CREATE UNIQUE INDEX`). For each hit it requires one of the three
opt-outs above. Hard-fail (exit 1) — no warning-only mode.

## Concurrency model

CoreScope runs **one ingestor process** per deployment (`cmd/ingestor/`,
single binary, single `*Store`). There is no cluster mode, no leader
election, no second writer. The store holds one connection on both
backends (`SetMaxOpenConns(1)`): SQLite adds a 5s `busy_timeout`; on
Postgres the single connection also keeps ids committing in increasing
order, which the server's `id > last seen` tail depends on (AGENTS.md →
Read/Write Separation Invariant). All writes (live MQTT ingest + async
migration goroutines + maintenance backfills) serialize through the one
connection in a single process.

What this means for async migrations:

- **No cross-process race** to worry about. Two ingestor instances
  running against the same DB is not a supported deployment shape. (On
  Postgres, `pgschema.Apply`'s advisory lock still keeps two ingestors from
  migrating at once.)
- **Within a single process**, concurrent `RunAsyncMigration(name=X)`
  callers race the initial `SELECT status` → `UPDATE/INSERT` step. The
  current implementation re-schedules `fn` on a pending/failed/cancelled row so a
  duplicate caller may legitimately re-run it; once status is `done` all
  further calls short-circuit. See
  `TestRunAsyncMigration_ConcurrentSameNameSerialized` for the contract.
- **`fn` runs concurrently with live ingest writers.** Because
  `MaxOpenConns=1`, a long `CREATE INDEX` will serialize behind / ahead
  of insert batches (on SQLite via its busy-timeout; on Postgres by waiting
  for the one connection). This is acceptable for
  index builds (the boot path is unblocked, which was the whole point),
  but it means long migrations DO add latency to live writes. Document
  expected runtime in the `reason=` annotation and prefer batched/chunked
  fn implementations for multi-minute work (see `BackfillPathJSONAsync`
  for the canonical batched pattern with inter-batch `time.Sleep`).

## Scale budgets

Per-migration target: **<30s** at current prod scale (Cascadia: ~2,600
nodes, ~80K observations; previous prod snapshot: ~1.9M observations).

Worked example (#1483, `obs_observer_ts_idx_v1`): composite index build
on `observations(observer_idx, timestamp)`. At ~1.9M rows the sync build
pinned ingestor boot for several minutes → restart loop. Converted to
async via `RunAsyncMigration` in `OpenStore` so boot returns immediately
and the index materializes in the background; the existing `_migrations`
short-circuit at the top of the migration block ensures DBs that already
completed the sync v3.8.3 build do NOT re-run it through the goroutine
path on subsequent boots.

If you cannot meet the <30s budget, document the expected upper bound
and operator runbook expectation (e.g. "index build expected ~10 min on
a 5M-row table; ingestor remains responsive; monitor via
`SELECT status, error FROM _async_migrations WHERE name = ...`").

## Why this exists

Pattern that keeps repeating:

1. Author writes `CREATE INDEX foo ON observations(...)` in a migration.
2. Local dev DB has ~100 rows. Migration returns in 1ms. CI is green.
3. Reviewer focuses on plan correctness, not scale.
4. Ship.
5. Prod boots, the database (SQLite or Postgres) scans 1.9M rows, the ingestor sits at `[migration]
   Adding index...` for 8 minutes, healthcheck times out, container
   restarts, loops.
6. Operator pages. Hotfix. Apology.

The gate doesn't try to detect table size (undecidable from a diff). It
enforces **annotation discipline**: every author who adds a migration
must consciously decide which bucket it falls into and write that down.
That is the cheapest possible intervention that breaks the cycle.
