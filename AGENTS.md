# AGENTS.md — CoreScope

Guide for AI agents working on this codebase. Read this before writing any code.

## Architecture

Go backend + static frontend. No build step. No framework. No bundler.

**⚠️ The Node.js server (server.js) is DEPRECATED and has been removed. All backend code is in Go.**
**⚠️ DO NOT create or modify any Node.js server files. All backend changes go in `cmd/server/` or `cmd/ingestor/`.**

```
cmd/server/        — Go API server (REST + WebSocket broadcast + static file serving)
  main.go          — Entry point, flags, SPA handler
  routes.go        — All /api/* endpoints
  store.go         — In-memory packet store + analytics + SQL queries (SQLite or Postgres)
  config.go        — Configuration loading
  decoder.go       — MeshCore packet decoder
  backend.go       — isPostgres, sqlFor (per-backend SQL), queueAnchor
  db_postgres.go   — OpenPostgresDB: the main Postgres open (forced read-only + privilege check)
  control_pg.go    — The control pool: the server's only Postgres writes (corescope_control INSERTs)
cmd/ingestor/      — Go MQTT ingestor (separate binary, the only writer to the shared DB: SQLite or Postgres)
  db_postgres.go   — OpenPostgresStore (one writer connection, applies pgschema)
  portable_sql.go  — SQL fragments that run on both backends + the per-backend hot-path statements
cmd/migrate/       — corescope-migrate: legacy `-db` upgrade; `copy` / `verify` between SQLite and Postgres
cmd/decrypt/       — corescope-decrypt (reads a SQLite path or a postgres:// URL, read-only)
internal/dbx/      — "pgx-qmark" Postgres driver (rewrites `?` → `$n`), dbx.IsPostgres; dbxtest.OpenPostgres for tests
internal/dbschema/ — SQLite schema (Apply, AssertReady) + probes that work on both backends (TableExists, TableHasColumn)
  sqlite_endstate.sql — SQLite end-state schema of a fresh ingestor (dbschema.SQLiteEndState)
  pgschema/        — Postgres schema: forward-only migrations/NNNN_name.sql, Apply (advisory lock), AssertReady, LatestVersion
public/            — Frontend (vanilla JS, one file per page) — ACTIVE, NOT DEPRECATED
  app.js           — SPA router, shared globals, theme loading
  roles.js         — ROLE_COLORS, TYPE_COLORS, health thresholds, shared helpers
  nodes.js         — Nodes list + side pane + full detail page
  map.js           — Leaflet map with markers, legend, filters
  packets.js       — Packets table + detail pane + hex breakdown
  packet-filter.js — Wireshark-style filter engine (standalone, testable)
  customize.js     — Theme customizer panel (self-contained IIFE)
  analytics.js     — Analytics tabs (RF, topology, hash issues, etc.)
  channels.js      — Channel message viewer
  live.js          — Live packet feed + VCR mode
  home.js          — Home/onboarding page
  hop-resolver.js  — Client-side hop prefix → node name resolution
  style.css        — Main styles, CSS variables for theming
  live.css         — Live page styles
  home.css         — Home page styles
  index.html       — SPA shell, script/style tags with __BUST__ placeholder (auto-replaced at server startup)
test-fixtures/     — Real data SQLite fixture from staging (used for E2E tests; CI also copies it into Postgres)
scripts/           — Tooling (coverage collector, fixture capture, frontend instrumentation)
  postgres-roles.sh — Provisions the Postgres database + writer/reader roles (idempotent, never drops)
docker-compose.postgres.yml — Compose override: Postgres service + split writer/reader DSNs
```

### Data Flow
1. MQTT brokers → Go ingestor (`cmd/ingestor/`) ingests packets → decodes → writes to the DB (SQLite or Postgres)
2. Go server (`cmd/server/`) polls the DB for new packets (`id > last seen`), broadcasts via WebSocket
3. Frontend fetches via REST API (`/api/*`), filters/sorts client-side

### Two Database Backends
SQLite is the default. PostgreSQL 16+ is an operator opt-in (`db.driver` / `db.dsn`, env `DB_DRIVER` / `DB_DSN`). Same binaries, same schema, same API results: every endpoint must return identical results on both, except `/api/backup` (501 on Postgres; use pg_dump) and `/api/perf/sqlite` (404). User accounts are `users.db` (SQLite) by default, or schema `corescope_users` in Postgres when `userManagement.dsn` / `DB_DSN_ACCOUNTS` is set.

**Portable SQL first.** SQL in a shared path must run unchanged on both. Placeholders stay `?` (the `pgx-qmark` driver in `internal/dbx` rewrites them). In order of preference:
1. A portable form (list below).
2. Only when no shared form exists, or the shared form measurably slows SQLite: a per-backend choice made once, with both texts side by side. Server: `db.sqlFor(sqlite, postgres)` (`cmd/server/backend.go`). Ingestor: a prepare-time branch on `Store.pg` (`cmd/ingestor/portable_sql.go`). Example: `RETURNING id` and the portable `last_seen` clamp cost SQLite ~12% and ~5% CPU in `BenchmarkInsertTransmissionHotPath`, so the transmissions insert and the clamp keep their original SQLite text (pinned by `TestSQLiteHotPathSQLUnchanged`) and Postgres gets the portable form. "Measurably" means a benchmark like that one (rule 0).

| SQLite-only | Portable form |
|---|---|
| `INSERT OR IGNORE` / `INSERT OR REPLACE` / `REPLACE INTO` | `INSERT … ON CONFLICT (…) DO NOTHING` / `DO UPDATE` |
| unqualified column in `ON CONFLICT DO UPDATE` | table-qualified column (ambiguous on Postgres) |
| `a IS NOT b` (null-safe compare) | `a IS DISTINCT FROM b` |
| two-argument `max(a, b)` / `min(a, b)` | `sqlMax2` / `sqlMin2` (`GREATEST` ignores NULLs) |
| `res.LastInsertId()` | `RETURNING id` |
| bare `?` in a SELECT list, `? IS NULL` | `CAST(? AS TEXT)` etc. (Postgres can't type a bare placeholder) |
| `datetime('now')` in an ingestor runtime write | `sqliteDatetimeNow()` bound as a parameter |
| default NULL placement, ORDER BY with ties | explicit `NULLS FIRST` / `NULLS LAST`; ORDER BY ends in a key |
| `LIKE` relying on ASCII case-insensitivity | `LOWER(col) LIKE <lower-cased input>` (Postgres `LIKE` is case-sensitive) |

`TestPortableSQLGuard` (`cmd/ingestor/portable_sql_guard_test.go`, go/ast) fails on the SQLite-only forms in ingestor runtime SQL outside a per-function allowlist that can only shrink. The server has no guard; its dual-backend tests are the check (see Testing).

**Schema introspection.** Never `sqlite_master` or `PRAGMA` in code that runs on Postgres. Use `dbschema.TableExists` / `dbschema.TableHasColumn`: they work on both backends and inside a transaction (any error aborts a Postgres transaction, so they ask `information_schema` first).

**Postgres types** (`internal/dbschema/pgschema/migrations/`): `TEXT`/`DATETIME` → `TEXT COLLATE "C"` (byte order, like SQLite's BINARY); `INTEGER` → `BIGINT`; `REAL` → `DOUBLE PRECISION`; `INTEGER PRIMARY KEY AUTOINCREMENT` → `BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY`; defaults byte-identical to SQLite's; index names identical. The one documented exception: `observations.score` is `DOUBLE PRECISION` (SQLite declares INTEGER but stores floats), so the server scans it as `sql.NullFloat64`. Scan every `REAL` / `DOUBLE PRECISION` column into a float type (`float64`, `sql.NullFloat64`).

**SQLite-only** (skip on Postgres behind `Store.pg` / `db.isPostgres()`): the `users.db` file (accounts can opt into Postgres, below); its scheduled backup and `/api/admin/users-backup` (501 with accounts in Postgres; use `pg_dump -n corescope_users`); the file queues (Postgres uses `corescope_control`); the hop index (`observation_hops*`, `retention.hopIndexDays`); VACUUM, incremental vacuum, WAL checkpoints and planner-stats refresh (`db.vacuumOnStartup`, `db.incrementalVacuumPages`, `db.analysisLimit`; autovacuum covers Postgres); the legacy migration history in `applySchema`. On Postgres `dbPath` only places local files beside it (`queueAnchor`), and `users.db` by default; don't assume a DB file exists there. The ingestor refuses to start on an empty Postgres while the SQLite file at `dbPath`, or at `CORESCOPE_SQLITE_GUARD_PATH` (set by the Docker entrypoint, where `dbPath` moves into `/app/data/shared`), still holds transmissions (`refuseEmptyPostgresOverSQLite`).

### Read/Write Separation Invariant (#1283)
- **All DB writes live in `cmd/ingestor/`.** INSERT / UPDATE / DELETE / VACUUM
  (SQLite) / schema migrations (both backends) / retention all run in the
  ingestor process.
- **`cmd/server/` never writes measurement data.** It opens the analyzer DB with
  `mode=ro` (SQLite) and must not acquire a write lock on it. Adding a write-side helper
  (e.g. a `cachedRW`-style RW connection) regresses this invariant and races the
  ingestor → SQLITE_BUSY.
- **On Postgres the database enforces it.** The server opens Postgres only through
  `OpenPostgresDB` (`cmd/server/db_postgres.go`): every connection gets
  `default_transaction_read_only=on`, and startup is refused if the role holds
  INSERT/UPDATE/DELETE/TRUNCATE on any table in its schema (ownership, superuser
  and inherited grants count) or the schema is not ready.
  `TestPostgresOpenIsOnlyInOpenPostgresDB` pins the opens (this one plus the
  control pool below, nothing else). Deployments use two
  roles from `scripts/postgres-roles.sh`: a writer that owns the schema (ingestor)
  and a read-only reader (server). In Docker the boundary for the writer
  credential is a **separate ingestor container** (`docker-compose.postgres.yml`):
  prod runs the server with `DISABLE_INGESTOR=true` and only the reader DSN, and
  refuses to start if `DB_DSN_INGESTOR` is set. Inside prod the server also runs
  as its own unprivileged uid with an allow-listed environment
  (`docker/entrypoint-go.sh`, `docker/server-exec.sh`); its only writable paths
  are `/app/data/shared` (`users.db`, unless accounts are in Postgres) and `config.json`.
  That in-container separation is defence in depth, not the boundary. Never
  loosen any of this to make a server write work — move the write to the
  ingestor.
- **The server's environment is an allow-list on Postgres.** In Docker it
  starts through `docker/server-exec.sh` with `env -i` and a fixed list of
  variables. A new environment variable the server reads must be added to that
  list, or the server silently won't see it.
- **Exception on Postgres: the control tables.** On Postgres the server and
  ingestor exchange through the database, not files. Server → ingestor
  requests (geo-prune, multibyte snapshot) go in schema `corescope_control`
  (`prune_requests`, `prune_results`, `mbcap_snapshots`; pgschema migration
  0002); ingestor → server data goes in tables the ingestor writes
  (`ingestor_stats`). The server's one deliberate write is INSERT into
  `prune_requests` and `mbcap_snapshots`, only through the control pool in
  `cmd/server/control_pg.go` (read-only forced off), and only through membership
  in the NOLOGIN group role `corescope_control_client` holding exactly those
  grants (`pgschema.ControlGrantsOK`). Measurement tables stay unwritable, and
  startup is still refused otherwise; without the group the control pool stays
  closed and the prune endpoints answer 503. `readonly_invariant_test.go` pins
  it: INSERT SQL and read-only-off only in `control_pg.go`
  (`TestServerSourceHasNoAnalyzerWrites`, `TestControlPoolConfinedToControlPG`),
  `.ctl` used nowhere else, exactly two Postgres opens. **Never add another
  server write path.** A new server → ingestor request gets its own control
  table with the same grant pattern, added together in a pgschema migration,
  `EnsureControlGrants`, `ControlGrantsOK` and `scripts/postgres-roles.sh`. New
  ingestor → server data goes in a table the ingestor writes.
- **File queues are SQLite-only.** SQLite deployments exchange through files
  (`prunequeue.NewFS`, `mbcapqueue.NewFS`), accessed only through an `os.Root`
  on the queue directory (`openQueueRoot`), written as a randomly named
  `O_EXCL` temp file + rename, and read only if they are regular files. New
  file exchange must do the same. Never reintroduce shared files on Postgres.
- **One writer connection, on both backends.** The ingestor keeps
  `SetMaxOpenConns(1)` on Postgres too (`openPostgresStoreDB`). The reason there is
  not locking: the server tails `transmissions` and `observations` by
  `id > last seen`, which misses nothing only if ids commit in increasing order.
  With two connections id 102 can commit before 101, a poll in between moves the
  cursor past 101, and that row never reaches the live feed. Don't raise it or add
  a second writer pool. For the same reason a deployment (Compose, Kubernetes)
  never runs more than one ingestor; two would also fight over the MQTT client id.
- **Exception on both backends: the accounts store (optional user management).** When
  `userManagement.enabled`, the server owns a *separate* store through
  `internal/users` only: a SQLite `users.db`, or with `userManagement.dsn` /
  `DB_DSN_ACCOUNTS` schema `corescope_users` in Postgres (`users.OpenPostgres`).
  `users.Open` refuses the analyzer DB path, and
  `TestUsersOpenIsTheOnlyServerWritePath` pins the one call site. Account data
  never goes into the analyzer DB, and measurement writes never go through
  `internal/users`.
- **On Postgres the accounts credential owns only `corescope_users`.** It is its
  own role (`corescope_accounts`, `scripts/postgres-roles.sh`), never the
  reader's, and goes to the server only, never the ingestor. The roles script
  creates the schema; `users.OpenPostgres` never does, refuses a missing one,
  and refuses a role that is superuser, CREATEROLE or BYPASSRLS, or owns or can
  CREATE in its database, through any membership
  (`TestOpenPostgresRefusesPowerfulRoles`): CREATE would let it plant a schema
  named after the writer ("$user" in its search_path). At startup
  `verifyAccountsPGRole` (`cmd/server/accounts_pg.go`) refuses a role that is
  the reader, that could write the measurement or `corescope_control` schema,
  or that owns or can CREATE in the measurement database, directly or through
  any role it reaches (`TestAccountsPGRoleCheck`). The reader group gets
  column-only SELECT (`pgReaderColumns`, kept equal to the roles script by
  `TestReaderColumnsMatchRolesScript`). Don't add
  a second server write path to Postgres: the measurement connection stays
  `default_transaction_read_only=on`.
- **The ingestor reads the accounts store, never writes it.** On SQLite it opens `users.db`
  read-only. On Postgres it checks `pg_namespace` on every read: if schema
  `corescope_users` exists it reads `proposals` and `companion_links` over its own
  DB connection in a READ ONLY transaction, through the group role
  `corescope_users_reader` (granted to the writer; no accounts DSN); if not, it reads
  `users.db` read-only as on SQLite. A schema without tables reads as an empty set
  (`TestChannelKeySetPostgresIsReadOnly`, `TestLinkedCompanionSetPostgresIsReadOnly`,
  `TestChannelKeySetPostgresMissingSchemaIsEmptyAndLogsOnce`,
  `TestChannelKeySetPostgresNoAccountsSchemaReadsUsersDB`,
  `TestChannelKeySetPostgresAccountsSchemaWinsOverUsersDB`,
  `TestLinkedCompanionSetPostgresNoAccountsSchemaReadsUsersDB` in
  `cmd/ingestor/users_postgres_test.go`). With
  `userManagement.channelProposals.enabled` the ingestor opens `users.db`
  read-only (`mode=ro`, raw SQL, no `internal/users` import) once a minute
  for the approved hashtag channel names. With
  `clientRxCoverage.requireLinkedCompanion` it reads `companion_links` the same
  way (`SELECT pubkey`, every 60 s, plus at most one re-read per 5 s on a miss).
  `TestChannelKeySetIsReadOnly` and `TestLinkedCompanionSetIsReadOnly` pin the
  read-only open.
- Enforcement: `cmd/server/readonly_invariant_test.go` reflect-asserts that
  `PruneOldPackets`, `PruneOldMetrics`, and `RemoveStaleObservers` are NOT
  methods on the server's `*DB`. If you need a new write, add it to
  `cmd/ingestor/`.

### What's Deprecated (DO NOT TOUCH)
The following were part of the old Node.js backend and have been removed:
- `server.js`, `db.js`, `decoder.js`, `server-helpers.js`, `packet-store.js`, `iata-coords.js`
- All `test-server-*.js`, `test-decoder*.js`, `test-db*.js`, `test-regional*.js` files
- If you see references to these in comments or docs, they're stale — ignore them

## Rules — Read These First

### 0. Performance is a feature — not an afterthought
Every change must consider performance impact BEFORE implementation. This codebase handles 30K+ packets, 2K+ nodes, and real-time WebSocket updates. A single O(n²) loop or per-item API call can freeze the UI or stall the server.

**Before writing code, ask:**
- What's the worst-case data size this code will process?
- Am I adding work inside a hot loop (render, ingest, WS broadcast)?
- Am I fetching from the server what I could compute client-side?
- Am I recomputing something that could be cached/incremental?
- Does my change invalidate caches more broadly than necessary?

**Hard rules:**
- **No per-item API calls.** Fetch bulk, filter client-side.
- **No O(n²) in hot paths.** Use Maps/Sets for lookups, not nested array scans.
- **No full DOM rebuilds.** Diff or virtualize — never innerHTML entire tables.
- **No unbounded data structures.** Every map/slice/array must have eviction or size limits.
- **No expensive work under locks.** Copy data under lock, process outside.
- **Cache expensive computations.** Invalidate surgically, not globally.
- **Debounce/coalesce rapid events.** WebSocket messages, scroll, resize — never fire raw.

**If your change touches a hot path (packet rendering, ingest, analytics), include a perf justification in the PR description:** what the complexity is, what the expected scale is, and why it won't degrade.

**Perf claims require proof.** "This is faster" without data is not acceptable. Every PR claiming to fix or improve performance MUST include one of:
- A benchmark test (before/after timings with realistic data sizes)
- Profile output or timing measurements (e.g. "renderTableRows: 450ms → 12ms on 30K packets")
- A test assertion that enforces the perf characteristic (e.g. "filters 30K packets in <50ms")
No proof = no merge.

### 1. No commit without tests
Every change that touches logic MUST have tests. For Go backend: `cd cmd/server && go test ./...` and `cd cmd/ingestor && go test ./...`, or `make test` for all 17 modules (set `CORESCOPE_TEST_PG_DSN` or the Postgres tests skip; see Testing → PostgreSQL). For frontend: `node tests/unit/test-packet-filter.js && node tests/unit/test-aging.js && node tests/unit/test-frontend-helpers.js`. If you add new logic, add tests. No exceptions.

The SQLite driver (`github.com/mattn/go-sqlite3`) is cgo. **`CGO_ENABLED=0` still *builds*** — that is the trap. It links a stub, and the binary dies on the first query with `go-sqlite3 requires cgo to work. This is a stub`, so a green build proves nothing. A plain `GOOS=linux go build` likewise cannot cross-compile it. Use `make build` / `make crossbuild` — the latter needs [`zig`](https://ziglang.org/download/) as the cross C compiler and produces static musl binaries. Never re-add `CGO_ENABLED=0`. Postgres doesn't change this: its driver is pure Go, but the same binaries still link SQLite (the default backend and `users.db`).

**musl stack size — never remove `-Wl,-z,stack-size=8388608`.** musl gives non-main threads a 128 KiB stack (glibc: 8 MiB). cgo calls into SQLite run on that stack, and deep queries overflowed it and crashed the server with SIGSEGV (`/api/nodes/reported`, rx-coverage) once there was enough data. Every musl link passes the flag: Makefile `STACK_LDFLAG`, the Dockerfile's `ARG STACK_LDFLAG`, the CI `corescope-decrypt` builds; `make test` passes it too. Any new musl link (binary, Dockerfile stage, CI build) must add it. Running `go test` by hand on musl (Alpine): add `-ldflags=-extldflags=-Wl,-z,stack-size=8388608`.

### 2. No commit without browser validation
After pushing, verify the change works in an actual browser. Use `browser profile=openclaw` against the running instance. Take a screenshot if the change is visual. If you can't validate it, say so — don't claim it works.

### 3. Cache busters are automatic — do NOT manually edit them
Cache busters are injected automatically by the Go server at startup. The `__BUST__` placeholder in `index.html` is replaced with a Unix timestamp when the server reads the file. No manual bumping needed — every server restart picks up new asset versions. Do NOT replace `__BUST__` with hardcoded timestamps.

### 4. Verify API response shape before building UI
Before writing client code that consumes an API endpoint, check what the endpoint ACTUALLY returns. Use `curl` or check the server code. Don't assume fields exist — grouped packets (`groupByHash=true`) have different fields than raw packets. This has caused multiple breakages.

### 5. Plan before implementing
Present a plan with milestones to the human. Wait for sign-off before starting. The plan must include:
- What changes in each milestone
- What tests will be written
- What browser validation will be done
- What config/customizer implications exist (see rule 8)

Do NOT start coding until the human says "go" or "start" or equivalent.

### 6. One commit per logical change
Don't push half-finished work. Don't push "let me try this" experiments. Get it right locally, test it, THEN push ONE commit. The QR overlay took 6 commits because each one was pushed without looking at the result. That's 6x the review burden for one visual change.

### 7. Understand before fixing
When something doesn't work as expected, INVESTIGATE before "fixing." Read the firmware source. Check the actual data. Understand WHY before changing code. The hash_size saga (21 commits) happened because we guessed at behavior instead of reading the MeshCore source.

### 8. Config values belong in the customizer eventually
If a feature introduces configurable values (thresholds, timeouts, display limits), note in the plan that these should be exposed in the customizer in a later milestone. It's OK to hardcode initially, but don't forget — track it in the plan.

### 9. Explicit git add only
Never use `git add -A` or `git add .`. Always list files explicitly: `git add file1.js file2.js`. Review with `git diff --cached --stat` before committing.

### 10. Don't regress performance
The packets page loads 30K+ packets. Don't add per-packet API calls. Don't add O(n²) loops. Client-side filtering is preferred over server-side. If you need data from the server, fetch it once and cache it.

### 11. PR descriptions must be clean markdown
When opening a pull request, the description must be **valid, readable markdown**. Use real newlines (not `\n` literals), proper code fences, and correct heading syntax. Write it using `--body-file -` (piped from a heredoc or file), never inline `--body` with escaped characters. If the description renders as garbage, fix it before requesting review. This is the first thing reviewers see.

### 12. Post a follow-up comment when review feedback is addressed
When you push fixes for review comments, post a comment on the PR listing what was changed and the commit hash. Reviewers should not have to dig through commits to find what was fixed. Format: "Review feedback addressed (commit `abc1234`)" followed by a numbered list of what was done.

### 13. Use git worktrees for parallel work — never pollute the main checkout
Multiple agents work in parallel. The main clone (`C:\Projects\meshcore-analyzer\`) must stay on `master` and never be modified directly.

**Implementation agents** must create a dedicated worktree before making any changes:
```bash
git worktree add _wt-<branch-name> -b <branch-name> origin/master
cd _wt-<branch-name>
# ... do all work here ...
```
After PR is merged, clean up: `git worktree remove _wt-<branch-name>`

**Review agents** must NEVER read files from the working tree. Use git commands to read the remote branch directly:
```bash
git fetch origin <branch>
git show origin/<branch>:<path/to/file>      # read a specific file
git diff origin/master..origin/<branch>       # see the full diff
```
The working tree may have a different branch checked out. Reading it will give you wrong code.

**Periodic cleanup**: run `git worktree prune` to remove stale worktree references.

## MeshCore Firmware — Source of Truth

The MeshCore firmware source is cloned at `firmware/` (gitignored — not part of this repo). This is THE authoritative reference for anything related to the protocol, packet format, device behavior, advert structure, flags, hash sizes, route types, or how repeaters/companions/rooms/sensors behave.

**Before implementing any feature that touches protocol behavior:**
1. Check the firmware source in `firmware/src/` and `firmware/docs/`
2. Key files: `Mesh.h` (constants, packet structure), `Packet.cpp` (encoding/decoding), `helpers/AdvertDataHelpers.h` (advert flags/types), `helpers/CommonCLI.cpp` (CLI commands), `docs/packet_format.md`, `docs/payloads.md`
3. If `firmware/` doesn't exist, clone it: `git clone --depth 1 https://github.com/meshcore-dev/MeshCore.git firmware`
4. To update: `cd firmware && git pull`

**Do NOT guess at protocol behavior.** The hash_size saga (21 commits) and the advert flags bug (room servers misclassified as repeaters) both happened because we assumed instead of reading the firmware source. The firmware is C++ — read it.

## MeshCore Protocol

**Do not memorize or hardcode protocol details from this file.** Read the firmware source.

- Packet format: `firmware/docs/packet_format.md`
- Payload types & structures: `firmware/docs/payloads.md`
- Advert flags & types: `firmware/src/helpers/AdvertDataHelpers.h`
- Route types & constants: `firmware/src/Mesh.h`
- CLI commands & behavior: `firmware/docs/cli_commands.md`
- FAQ (advert intervals, etc.): `firmware/docs/faq.md`

If you need to know how something works — a flag, a field, a timing, a behavior — **open the file and read it.** Don't rely on comments in our code, don't rely on what someone told you, don't guess. The firmware C++ source is the only thing that matters.

## Frontend Conventions

### Theming
All colors MUST use CSS variables. Never hardcode `#hex` values outside of `:root` definitions. The customizer controls colors via `THEME_CSS_MAP` in customize.js. If you add a new color, add it as a CSS variable and map it in the customizer.

### Shared Helpers (roles.js)
- `getNodeStatus(role, lastSeenMs)` → 'active' | 'stale'
- `getHealthThresholds(role)` → `{ staleMs, degradedMs, silentMs }`
- `ROLE_COLORS`, `ROLE_STYLE`, `TYPE_COLORS` — global color maps

### Shared Helpers (nodes.js)
- `getStatusInfo(n)` → `{ status, statusLabel, explanation, roleColor, ... }`
- `renderNodeBadges(n, roleColor)` → HTML string
- `renderStatusExplanation(n)` → HTML string

### last_heard vs last_seen
- `last_seen` = DB timestamp, only updates on adverts/direct upserts
- `last_heard` = the newer of `last_seen` and the certain last heard from the in-memory packet store (#2260): the node's own packets plus relay hops whose prefix can only be this node. A 1-byte hop shared with other repeaters is NOT evidence, and neither is `last_relayed` (it counts those shared prefixes too). Liveness — status, Last Heard, the notify offline check — uses only this.
- Status from `getNodeStatus(n)` is `'active'`, `'stale'` or, for a repeater or room, `'uncertain'`: nothing certain within the silent window, but `last_relayed` (its shared prefix in a path) is. Treat `'uncertain'` as not proven alive (dim it like stale); never as active.
- Always prefer `n.last_heard || n.last_seen` for display and status calculation

### Packet Filter (packet-filter.js)
Standalone module. No dependencies on app globals (copies what it needs). Testable in Node.js:
```bash
node tests/unit/test-packet-filter.js
```
Uses firmware-standard type names (GRP_TXT, TXT_MSG, REQ) with aliases for convenience.

## Testing

### Test Pipeline
```bash
npm test                    # all backend tests + coverage summary
npm run test:unit           # fast: unit tests only (no server needed)
npm run test:coverage       # all tests + HTML coverage report
npm run test:full-coverage  # backend + instrumented frontend coverage via Playwright
```

### Test Files
```bash
# Backend (deterministic, run before every push)
sh test-all.sh                               # every suite in tests/unit
node tests/unit/test-packet-filter.js        # filter engine
node tests/unit/test-aging.js                # node aging system
node tests/unit/test-frontend-helpers.js     # frontend logic (via vm.createContext)

# Frontend E2E (requires running server or Playwright)
node tests/e2e/test-e2e-playwright.js       # Playwright browser tests (default: localhost:3000)
```

### PostgreSQL tests
Postgres tests **skip** unless `CORESCOPE_TEST_PG_DSN` is set, so a green run without it has not tested Postgres. Any role that can `CREATE SCHEMA` works; `dbxtest.OpenPostgres(t)` gives each test its own schema.
```bash
docker run -d --name cs-pg -e POSTGRES_USER=corescope -e POSTGRES_PASSWORD=corescope -e POSTGRES_DB=corescope_test -p 127.0.0.1:5432:5432 postgres:17-alpine
export CORESCOPE_TEST_PG_DSN='postgres://corescope:corescope@127.0.0.1:5432/corescope_test?sslmode=disable'
make test
```
- **Server — dual-backend harness** (`cmd/server/dual_testhelp_test.go`): `newDualTestDB` builds the end-state schema on both backends with the real read-only opens; `d.exec` seeds both; `requireSame` calls a server method on each and fails unless the results are deeply equal. Every new or changed server query gets a `requireSame` test whose seed makes the result non-empty (two empty results are trivially equal). Examples: `pg_db_queries_test.go`, `pg_store_queries_test.go`.
- **Ingestor — differential test** (`cmd/ingestor/ingest_backends_diff_test.go`): `TestIngestSameOnBothBackends` drives a SQLite and a Postgres store through `handleMessage` with real packets and requires every table to match. A new write path extends its sequence.
- **Schema**: `TestPgSchemaParity` and `TestSQLiteEndStateMatchesIngestor` — see MIGRATIONS.md.
- **CI** runs Go tests with a Postgres service, a `musl-test` job (Alpine), and every E2E shard on both SQLite and Postgres (the fixture copied into Postgres, served through the read-only role).

### Rules
**ALL existing tests must pass before pushing.** No exceptions. No "known failures."

**Database changes are tested on both backends.** A new or changed query, write path or schema change passes locally with `CORESCOPE_TEST_PG_DSN` set, not just in CI.

**Every new feature must add tests.** Unit tests for logic, Playwright tests for UI changes. Test count only goes up.

**Coverage targets:** Backend 85%+, Frontend 42%+ (both should only go up). CI reports both and updates badges automatically.

### When writing a new feature
1. Write the feature code
2. Write unit tests for the logic
3. Write/update Playwright tests if it's a UI change
4. Run `npm test` — all tests must pass
5. Run `node tests/e2e/test-e2e-playwright.js` against a local server — E2E must pass
6. THEN push to master

### Testing infrastructure
- **Backend coverage**: c8 tracks server-side code in-process
- **Frontend coverage**: Istanbul instruments `public/*.js` → Playwright exercises them → `window.__coverage__` extracted → nyc reports. Instrumented files are generated fresh each CI run, never checked in.
- **CI pipeline**: backend tests + coverage → instrument frontend → start local server → Playwright E2E + coverage collection → badges update → deploy (only if all pass)
- **Playwright tests default to localhost:3000** — NEVER run against prod. CI sets `BASE_URL=http://localhost:13581`. Running locally: start your server, then `node tests/e2e/test-e2e-playwright.js`
- **ARM machines**: Basic Playwright tests work with system chromium (`CHROMIUM_PATH=/usr/bin/chromium-browser`). Heavy coverage collection scripts may crash — use CI for those.

Tests that need live mesh data can use `https://analyzer.00id.net` — all API endpoints are public, no auth required.

### What Needs Tests
- Parsers and decoders (packet-filter, decoder)
- Threshold/status calculations (aging, health)
- Data transformations (hash size computation, field resolvers)
- Anything with edge cases (null handling, boundary values)
- SQL queries, write paths and schema changes — on both backends (`requireSame`, the ingestor differential test, the schema parity tests)
- UI interactions that exercise frontend code branches

## Engineering Principles

These aren't optional. Every change must follow these principles.

### DRY — Don't Repeat Yourself
If the same logic exists in two places, it MUST be extracted into a shared function. We had **5 separate implementations** of hash prefix disambiguation across the codebase — that's a maintenance nightmare and a bug factory. One implementation, imported everywhere.

**Before writing new code, search the codebase for existing implementations.** `grep -rn 'functionName\|pattern' public/ server.js` takes 2 seconds and prevents duplication.

### SOLID Principles
- **Single Responsibility**: Each function does ONE thing. A 200-line function that fetches, transforms, renders, and caches is wrong. Split it.
- **Open/Closed**: Add behavior by extending, not modifying. Use callbacks, options objects, or configuration — not `if (caller === 'live')` branches inside shared code.
- **Dependency Injection**: Functions should accept their dependencies as parameters, not reach into globals. `resolveHops(hops, nodeList)` — not `resolveHops(hops)` where it secretly reads `window.allNodes`. This makes functions testable in isolation.
- **Interface Segregation**: Don't force callers to depend on things they don't need. If a function returns 20 fields but the caller uses 3, consider a simpler return shape or let the caller pick.

### Code Reuse
- **Shared helpers go in shared files.** Frontend: `roles.js`, `hop-resolver.js`. Backend: `server-helpers.js`, `decoder.js`.
- **Don't copy-paste between files.** If `live.js` needs the same algorithm as `packets.js`, import it from a shared module. If the shared module doesn't exist yet, create one.
- **Parameterize, don't duplicate.** If two callers need slightly different behavior, add a parameter — don't fork the function.

### Testability
- **Write functions that are easy to test.** Pure functions (input → output, no side effects) are ideal. If a function reads from the DOM, the DB, and localStorage, it's untestable without mocking everything.
- **Dependency injection enables testing.** Pass the node list, the map reference, the API function as parameters. Tests can substitute fakes.
- **Test the real code, not copies.** Don't paste a function into a test file and test the copy. Import/require the actual module. If the module isn't importable (IIFE, browser-only), refactor it so it is — or use `vm.createContext` like `tests/unit/test-frontend-helpers.js` does.
- **Every bug fix gets a regression test.** If it broke once, it'll break again. The test proves it stays fixed.

### Type Safety (without TypeScript)
- **Cast at the boundary.** Data from the DB, API, or localStorage may be strings when you expect numbers. Cast early: `Number(val)`, `parseInt(val)`, `String(val)`. Don't let type mismatches propagate deep into logic where they cause cryptic `.toFixed is not a function` errors.
- **Null-check before method calls.** `val != null ? Number(val).toFixed(1) : '—'` — not `val.toFixed(1)`.

### Performance Awareness
- **No per-item API calls.** Fetch bulk data once, filter/transform client-side.
- **No O(n²) in hot paths.** The packets page has 30K+ rows. A nested loop over all packets × all nodes = 20 billion operations. Use Maps/Sets for lookups.
- **Cache expensive computations.** If you compute the same thing on every render, cache it and invalidate on data change.

## XP (Extreme Programming) Practices

### Test-First Development
Write the test BEFORE the code. Not after. Not "I'll add tests later." The test defines the expected behavior, then you write the minimum code to make it pass.

**Flow:** Red (write failing test) → Green (make it pass) → Refactor (clean up).

This prevents shipping bugs like `.toFixed on a string` — if the test existed first with string inputs, the bug could never have been introduced. Every bug fix starts by writing a test that reproduces the bug, THEN fixing it.

### YAGNI — You Aren't Gonna Need It
Don't build for hypothetical future requirements. Build the simplest thing that solves the current problem. The 5 separate disambiguation implementations happened because each page rolled its own "just in case" version instead of importing the one that already existed.

If you're writing code that handles a case nobody asked for: stop. Delete it. Add it when there's a real need.

### Refactor Mercilessly
When you touch a file and see duplication, dead code, unclear names, or structural mess — clean it up in the same commit. Don't leave it for "later." Later never comes. Tech debt compounds.

**The Boy Scout Rule:** Leave every file cleaner than you found it.

### Simple Design
The simplest solution that works is the correct one. Complexity is a bug. Before building something, ask:
1. Does this already exist somewhere in the codebase?
2. Can I solve this with an existing function + a parameter?
3. Am I over-engineering for a case that doesn't exist yet?

If the answer to any of these is yes, simplify.

### Pair Programming (Human + AI Model)
For this project, pair programming means: **subagent writes the code → parent agent reviews and tests locally → THEN pushes to master.** The subagent is the "driver," the parent is the "navigator."

**What this means in practice:**
- Subagent output is NEVER pushed directly without review
- Parent agent runs the tests, checks the diff, verifies the behavior
- If the subagent's work is wrong, parent fixes it before pushing — not after
- "The subagent said it works" is not verification. Running the tests is.

### Continuous Integration as a Gate
CI must pass before code is considered shipped. But CI is the LAST line of defense, not the first. The process is:
1. Test locally (unit + E2E)
2. Review the diff
3. Push
4. CI confirms

If CI catches something you missed locally, that's a process failure — figure out why your local testing didn't catch it and fix the gap.

### 10-Minute Build
Everything must be testable locally in under 10 minutes. If local tests are broken, flaky, or crashing — that's a P0 blocker. Fix the test infrastructure before shipping features. Broken tests = no tests = shipping blind.

### Collective Code Ownership
No file is "someone else's problem." Every file follows the same patterns, uses the same shared modules, meets the same quality bar. `live.js` doesn't get to be a special snowflake with its own reimplementation of everything. If it drifts from the shared patterns, bring it back in line.

### Small Releases
One logical change per commit. Each commit is deployable. Each commit has its tests. Don't bundle "fix A + feature B + cleanup C" into one push — if B breaks, you can't revert without losing A and C.

## Common Pitfalls

| Pitfall | Times it happened | Prevention |
|---------|-------------------|------------|
| Forgot cache busters | 7 | Now automatic — `__BUST__` replaced at server startup |
| Grouped packets missing fields | 3 | curl the actual API first |
| last_seen vs last_heard mismatch | 4 | Always use `last_heard \|\| last_seen` |
| CSS selectors don't match SVG | 2 | Manipulate SVG in JS after generation |
| Feature built on wrong assumption | 5+ | Read source/data before coding |
| Pushed without testing | 5+ | Run tests + browser check every time |
| Tests defaulting to prod | 2 | Always default to localhost, never prod |
| Gave up testing locally | 2 | Basic tests work on ARM — only heavy coverage scripts crash |
| Copy-pasted functions for "coverage" | 1 | Test the real code, not copies in a helper file |
| Subagent timed out mid-work | 4 | Give clear scope, don't try to run slow pipelines locally |
| SQLite-only SQL in a shared path | — | Portable form first; `sqlFor` / `Store.pg` only with a benchmark; `requireSame` test |
| Tested on SQLite only | — | Set `CORESCOPE_TEST_PG_DSN`; Postgres tests skip without it |
| Schema change on one backend only | — | SQLite migration + `pgschema` migration + `sqlite_endstate.sql` (MIGRATIONS.md) |
| `sqlite_master` / `PRAGMA` on Postgres | — | `dbschema.TableExists` / `TableHasColumn` |
| Second ingestor connection on Postgres | — | Keep `SetMaxOpenConns(1)`: the server tails by id |
| New server write on Postgres | — | Only through a `corescope_control` table + `corescope_control_client` grants, in `control_pg.go` |
| Server reads a new env var that isn't in `docker/server-exec.sh` | — | Add it to the allow-list; on Postgres the server otherwise never sees it |
| New `internal/` dependency missing from a Dockerfile stage | — | Add the `COPY internal/<m>/` line to every stage that builds it; `tests/unit/test-dockerfile-modules.js` checks |
| Missing or removed musl stack flag | 1 | Keep `-Wl,-z,stack-size=8388608` on every musl link |

## File Naming
- Tests: `test-{feature}.js` in `tests/unit/` (standalone, listed in `test-all.sh`) or `tests/e2e/` (browser, listed in `scripts/non-unit-tests.json`) — never in repo root
- No build step, no transpilation — write ES2020 for server, ES5/6 for frontend (broad browser support)

### Deep Linking
All new UI states that a user might want to share or bookmark MUST be reflected in the URL hash.
This includes: tabs, filters, selected items, view modes. Use query parameters on the hash
(e.g., `#/packets?observer=ABC&timeRange=24h`) for filter state.
Existing patterns: `#/nodes/{pubkey}?section=node-neighbors`, `#/analytics?tab=collisions`, `#/packets/{hash}`.

## What NOT to Do
- **Don't check in private information** — no names, API keys, tokens, passwords, IP addresses, personal data, or any identifying information. This is a PUBLIC repo.
- **Don't introduce new `map[string]interface{}` in API response builders, handler returns, or internal data structures that cross domain boundaries.** Use a named Go struct with explicit JSON tags. CoreScope already carries 694 occurrences (see #1383); the count must monotonically decrease. If your change adds even one new occurrence in a touched file, the PR is wrong-shaped — fix the design, don't paper over with `interface{}`. Exempt: third-party library boundaries that genuinely return `interface{}`, and ad-hoc test fixture assertions.
- Don't add npm dependencies without asking
- Don't create a build step
- Don't add framework abstractions (React, Vue, etc.)
- Don't hardcode colors — use CSS variables
- Don't make per-packet server API calls from the frontend
- Don't push without running tests
- Don't start implementing without plan approval
