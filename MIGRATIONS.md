# MIGRATIONS — async vs sync policy

CoreScope's ingestor applies schema/data migrations inline at boot in
`cmd/ingestor/db.go`. Every migration that runs synchronously blocks the
ingestor from accepting packets until it returns. On a dev DB that's
milliseconds; at prod scale (1.9M+ observations, 80K+ adverts, 2600+ nodes
on Cascadia) it can pin the boot for minutes and trigger restart loops —
the "upgrade broke prod" failure class (#791, #1483, and others).

## The rule

**Any new `CREATE INDEX`, `ALTER TABLE`, or data-rewriting `UPDATE`/`DELETE`
in a migration file MUST do ONE of the following:**

### Option 1 — Run via `Store.RunAsyncMigration` (preferred for backfills)

```go
// Scheduled in OpenStore() AFTER the *Store is constructed.
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
- On success the row flips to `done`; on error/panic to `failed` with the
  error message captured.
- Idempotent: rows in `done` state short-circuit; `failed`/`pending_async`
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
// PREFLIGHT: async=true reason="ALTER ADD COLUMN — O(1) sqlite operation"
if r := db.QueryRow("SELECT 1 FROM _migrations WHERE name = 'foo_v1'"); ...
```

The reason MUST be a real one-line justification you can defend in
review. "It's fine" is not a reason.

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

## Why this exists

Pattern that keeps repeating:

1. Author writes `CREATE INDEX foo ON observations(...)` in a migration.
2. Local dev DB has ~100 rows. Migration returns in 1ms. CI is green.
3. Reviewer focuses on plan correctness, not scale.
4. Ship.
5. Prod boots, sqlite scans 1.9M rows, the ingestor sits at `[migration]
   Adding index...` for 8 minutes, healthcheck times out, container
   restarts, loops.
6. Operator pages. Hotfix. Apology.

The gate doesn't try to detect table size (undecidable from a diff). It
enforces **annotation discipline**: every author who adds a migration
must consciously decide which bucket it falls into and write that down.
That is the cheapest possible intervention that breaks the cycle.
