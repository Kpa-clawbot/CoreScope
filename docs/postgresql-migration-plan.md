# PostgreSQL conversion plan

Status: implemented and validated at `ce023e0d623116fe66987761d06016590f98fb11`. The [operator guide](postgresql-upgrade.md) covers cutover, interruption recovery and rollback. The [performance report](postgresql-performance.md) records the completed five-pair Linux comparison, including regressions and resource limits. Maintainer acceptance of the replacement remains a separate decision.

## Goal and baseline

Deliver one CoreScope PR that makes PostgreSQL the runtime database, supplies a verified upgrade path for existing SQLite instances, and includes reproducible performance results against unmodified upstream commit `9dbc287579a237ffa744dd0c91fa7227d09763ac` (v3.14.0).

The user has approved a planned downtime window. This plan covers telemetry and the optional account store, not just the main database. The inventory found 18 telemetry tables plus `packets_v`, 12 account tables, four Go commands, and database calls across 50 production Go files.

Issue #1442 asks for optional PostgreSQL support and contains objections to previous unmeasured engine changes. Reference that discussion without claiming this full replacement satisfies its dual-backend scope. Open a focused conversion/upgrade/benchmark issue before the implementation PR. Acceptance of the proposal by maintainers is not assumed.

## Design commitments

- Target supported PostgreSQL 18, pinning the exact tested patch/image in deployment and benchmark evidence. Use pgx through the existing `database/sql` boundaries and explicit PostgreSQL SQL. Preserve the Go services, PacketStore caches, static frontend and polling architecture.
- Keep telemetry and accounts in separate logical databases on one PostgreSQL service by default. Keep separate credentials and handles: telemetry writer, telemetry reader, account writer, and a restricted approved-channel reader for the ingestor. Migration/bootstrap privileges are separate. Test that runtime roles cannot cross these boundaries.
- Retain SQLite only for legacy upgrade/import support and importer test fixtures. Convert server, ingestor, decrypt/migrate commands, active maintenance tools, both backup paths, account export, diagnostics, deployment and CI.
- Preserve public JSON/WebSocket behavior. Explicitly document the necessary changes to database diagnostics and downloadable native backup formats. Introduce a state-directory setting for the existing small file queues/sidecars; a database URL must never become a filesystem path or appear unredacted in logs.
- Preserve observation identity, null behavior, raw values, observer identities, snapshot consistency and committed feed ordering. Do not increase write concurrency merely because PostgreSQL permits it. Replace SQLite-supplied serialization with explicit transactional/locking rules where required, including account compare-and-set and one-time transitions.

## Milestones and validation

### 1. PostgreSQL runtime and schema

Port schema creation/upgrades, connection setup and SQL to PostgreSQL. Replace hidden observer row IDs with explicit stable keys; use returned IDs rather than SQLite insertion APIs. Keep legacy migration history separate from PostgreSQL schema readiness. Move or retire the server's existing attempted telemetry hash writes rather than enabling them through broad credentials.

Write failing tests before implementation. Validate fresh/repeated/concurrent startup, role-denied writes, deduplication and nulls, time and ordering behavior, delayed commits/ID gaps, snapshot consistency, retention and API/WebSocket parity. Exercise accounts both enabled and disabled, including login/session preservation, single-use tokens, concurrent settings/proposal changes and deletion/anonymization.

### 2. Existing-instance upgrade

Provide an explicit offline import command and operator cutover procedure. Stop telemetry and account writers, retain a WAL-aware consistent SQLite recovery snapshot, normalize supported legacy shapes on a working copy, and load an empty PostgreSQL destination in bounded batches. The supported source-layout/version matrix must be documented and covered by fixtures; unknown/newer schemas get an actionable refusal.

Preserve all supported application data, observer identity links, IDs, sequence high-water marks, password hashes, sessions, preferences, proposals and notifications. Validate counts, stable logical digests and relationships before recording completion. Partial imports must not be accepted by running services; interruption/resume must be deterministic and bound to the same source/target. Never silently drop incompatible records.

Test source immutability, current and supported older snapshots, accounts absent, null/orphan links, gaps/deleted high IDs, malformed/unsupported data, cancellation, rerun/resume and completion gating. Restore both native PostgreSQL backup types into fresh databases and verify data plus API behavior. Document rollback before PostgreSQL accepts new writes; do not promise lossless reverse conversion after cutover. Document the planned ingestion gap and preservation of existing configuration/key material.

### 3. Reproducible performance comparison

Use an isolated Linux runner, running both exact revisions serially on the same machine with the same compiler, workload, logical data, cache policy, retention, resource limits and durability. Keep SQLite WAL/FULL synchronous settings and PostgreSQL durable settings enabled. Count verified database effects and errors; handler returns and the existing WALCommits counter are not proof of durable success.

The committed 499-transmission fixture is only a correctness input. Add deterministic synthetic corpora with declared distributions: 30K/90K transmissions/observations, 128K/2.048M for the primary comparison, and 1M/16M for larger-data qualification where runner capacity permits. Record measured sizes, not inferred GB labels. Use five paired repetitions for primary comparisons and report resource/cold-cache limitations explicitly.

Measure durable ingestion, first/full startup readiness, SQL-backed API latency separately from warm memory/cache controls, concurrent ingest/API load, WebSocket visibility lag, bounded retention, whole-stack CPU/memory/storage/WAL, migration duration and disk headroom. Publish absolute values, p50/p95/p99 where sample counts support them, paired variation, errors, regressions, query plans, manifests and raw results. Do not assume or promise a speedup. If the required comparison cannot execute, the performance deliverable remains incomplete.

The current Windows host has limited free RAM and its existing Podman configuration fails before listing containers. Do not repair or disturb that global environment for this work; use isolated CI resources and bounded local checks.

### 4. Operational integration and PR review

Update configuration examples and comments, environment/secret handling, Compose/supervisor variants, native operation, build packaging, migration/backup instructions and database-specific UI labels. Credentials remain deployment configuration. Document pool/timeout controls; track any genuinely user-facing new tuning control for appropriate admin/customizer exposure.

Run all Go modules, the authoritative standalone frontend suite, PostgreSQL integration tests, imported-fixture browser E2E, account-on/off journeys, backup/restore tests and real-browser API/UI checks. Retain assertion-based red-to-green CI evidence. Perform independent correctness, database/schema, security and TDD reviews; resolve findings, pass exact-head CI and include the benchmark report before marking the single PR ready for maintainer review.

## Approval boundary

The implementation plan has received explicit sign-off under repository `AGENTS.md` rule 5. Approval authorizes the implementation and testing above; it does not authorize merging or deploying the PR to live instances.
