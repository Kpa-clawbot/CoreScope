# PostgreSQL installation and offline upgrade

CoreScope uses PostgreSQL for telemetry and optional accounts. The tested database version is PostgreSQL 18.6. Keep the application checkout, image, shared Compose file, bootstrap scripts and configuration example from the same reviewed revision.

Existing SQLite instances need a planned outage. Stop **both** the ingestor and server: the account janitor, sessions, proposals and notification evaluator also write data. MQTT messages arriving during this window may be lost unless the broker and publishers provide a separately verified replay mechanism.

## Databases and credentials

The supplied deployment creates `corescope_telemetry` and `corescope_accounts` on one PostgreSQL service. They are separate logical databases, with `C` text collation. Runtime URLs must name one host and database; use the default `public` schema for native backups.

| Role | Purpose |
|---|---|
| `corescope_owner` | Offline import, schema bootstrap and privilege grants. Keep it out of runtime processes. |
| `corescope_reader` | Server telemetry reads and native telemetry backups. |
| `corescope_writer` | Ingestor telemetry writes and retention; no schema ownership. |
| `corescope_accounts` | Account data writes; no schema or readiness-marker writes. |
| `corescope_channels` | Ingestor reads of the approved-channel view; no access to passwords or sessions. |

Copy `.env.example` to a private `.env` and fill every PostgreSQL password field with a different value from `openssl rand -hex 32`. Keep file permissions at `0600`. The Compose files supply runtime URLs; credentials do not belong in Git, screenshots, issue reports or command arguments. On native installations, use private environment configuration with `CORESCOPE_READER_DATABASE_URL`, `CORESCOPE_WRITER_DATABASE_URL`, `CORESCOPE_USERS_DATABASE_URL` and `CORESCOPE_APPROVED_CHANNELS_DATABASE_URL`.

`databaseURL` replaces `dbPath`; `CORESCOPE_DATABASE_URL` is the per-process fallback. Remove legacy `dbPath`, `userManagement.dbPath` and `DB_PATH` settings after preserving their old values for recovery. `stateDir` / `CORESCOPE_STATE_DIR` is a separate filesystem directory for queues, statistics and account backups. A database URL is never a directory name.

Main runtime pool limits are four server telemetry connections, one ingestor connection and one account connection. Bootstrap, identity checks and backups open additional connections. Connection attempts default to a ten-second upper bound; a shorter URL `connect_timeout` is retained. Native application dumps also have a ten-minute deadline. Backend credentials and pool limits are not theme/customizer controls.

## Supported SQLite sources

| Source | Handling |
|---|---|
| Telemetry v3 with `observations.observer_idx` and the known optional columns | Supported. Missing supported columns/tables are added only to a working copy. |
| Telemetry v2 with `observer_id`, or an older Node-era layout | Refused. Upgrade a recovery copy using the last SQLite release, review the result, then import it. |
| Account `schema_version` 0–5 | Supported, including empty version 0. Each declared version must have its complete historical shape. |
| Accounts absent/disabled | Omit `-users-from-sqlite`; an explicitly configured account destination is initialized empty. |
| Unknown/newer columns, tables, views, triggers or virtual tables | Refused, with no silent field or record loss. |
| Duplicate observation identity, empty required transmission fields or case-colliding node keys | Refused. Perform an explicit, reviewed repair on a disposable recovery copy with the last SQLite release. |

Malformed JSON strings, empty strings, nulls, timestamp text, fractional scores, mixed-case values, ID zero and ID gaps are preserved when PostgreSQL can represent them. Invalid UTF-8, embedded NUL text, incompatible numeric values and records over the native 64 MiB SQLite length limit are refused. The importer never silently coerces incompatible records into zero or null.

Identity sequences are reseeded above imported IDs, available `sqlite_sequence` high-water marks and historical references, including orphan observer references and deleted users mentioned by audit records. SQLite integer primary keys without `AUTOINCREMENT` do not retain a deleted high-water mark once all references disappear; that information cannot be recovered or invented.

## Preflight and recovery

1. Record the exact application revision/image, broker settings, channel keys, account mail settings, config/theme files and state-directory contents. Keep the private `.env` and PostgreSQL credentials separately recoverable.
2. Arrange the ingestion gap and stop every instance writer. For Compose, stop the application service (`prod`, `staging-go` or `corescope`); leave the new PostgreSQL service available to the importer.
3. Retain the original SQLite directories, including any `-wal` and `-shm` siblings. Prefer a clean writer shutdown. Mount complete source directories, not just a `.db` file, so committed WAL data is available.
4. Use new, empty PostgreSQL destinations. The importer refuses occupied destinations, future schemas and a different source or destination on resume.
5. Allocate a private migration-state directory. It holds a WAL-aware immutable recovery snapshot, a normalized working snapshot and a source/destination manifest for each store. Snapshot creation uses SQLite's backup API so hidden observer/mail-event row IDs do not change.
6. Measure available disk space against the actual source, both snapshot copies, PostgreSQL data/indexes, WAL and temporary work. The required space and duration depend on the data; no fixed multiplier or downtime estimate is guaranteed.

If an uncheckpointed WAL cannot be opened from a read-only mount, keep the original untouched and make a quiescent filesystem recovery copy of the whole source directory in private writable storage. Import that identical copy. Never omit a nonempty WAL or run `VACUUM` on the only recovery copy.

## Compose cutover

Clone and pin the complete repository. A downloaded Compose YAML alone is insufficient: the variants extend `docker/postgres.compose.yml` and mount `docker/postgres-init.sh`. Select the matching image through `CORESCOPE_IMAGE` when using `docker-compose.example.yml`.

The following uses the example deployment. Substitute the production/staging Compose variant and the actual source filenames for your instance. Preserve the original data directories first.

```sh
docker compose -f docker-compose.example.yml stop corescope
docker compose -f docker-compose.example.yml up -d --wait postgres
install -d -m 700 migration-state

docker compose -f docker-compose.example.yml run --rm \
  --entrypoint /app/corescope-migrate \
  -v "$PWD/migration-state:/migration" bootstrap \
  -offline -from-sqlite /app/data/meshcore.db \
  -users-from-sqlite /app/data/users.db -state-dir /migration
```

Omit the users source flag only when there is no account source to preserve. For sources outside `/app/data`, add a read-only directory mount and pass its exact file path. The bootstrap service supplies owner URLs through its environment.

The importer copies in bounded batches, commits progress with each batch, verifies counts and logical digests in primary-key order, reseeds identities and analyzes only the imported tables. Normalization restores original values only in the columns historical migrations can change; it does not rewrite the observation corpus. Both requested stores must verify before cutover finalization. Until then their readiness markers remain false and runtime services refuse them.

For an interruption, keep the same source files, PostgreSQL destinations and migration-state directory, then repeat the command with `-resume`. A changed source, changed snapshot, unexpected target rows or corrupted progress refuses resume. Do not delete the state directory or manually set readiness true.

After a successful import:

```sh
docker compose -f docker-compose.example.yml run --rm \
  --entrypoint /app/corescope-migrate bootstrap \
  -check-import-kind=telemetry -from-sqlite /app/data/meshcore.db

docker compose -f docker-compose.example.yml run --rm \
  --entrypoint /app/corescope-migrate bootstrap \
  -check-import-kind=accounts -users-from-sqlite /app/data/users.db

docker compose -f docker-compose.example.yml run --rm bootstrap
docker compose -f docker-compose.example.yml up -d corescope
```

Skip the account-source check when accounts were absent. The normal bootstrap verifies any retained SQLite files against the completed import marker and source fingerprint, then grants runtime privileges. A manually initialized empty PostgreSQL database does not satisfy this upgrade guard. Absent and zero-length WAL sidecars have the same fingerprint; every nonempty WAL byte remains significant.

For native installations, invoke `corescope-migrate` with owner URLs in `CORESCOPE_DATABASE_URL` and, when applicable, `CORESCOPE_USERS_DATABASE_URL`. The same `-offline`, source, state-directory and resume flags apply. `-check-ready` performs read-only schema checks. Run `docker/postgres-grants.sql` as the owner once in each database after bootstrap/import, then start the services with their restricted runtime URLs and a shared state directory.

## Acceptance and rollback

Verify packet and observation counts, representative raw/null values, observers and paths, live ingestion/WebSocket visibility, retention, account login, existing sessions/CSRF, proposals, notifications and account export. Test accounts enabled and disabled as applicable. Restore a native backup into fresh isolated databases and exercise the API/login there before declaring recovery proven.

Rollback is straightforward **before PostgreSQL accepts new writes**: stop the new services and restart the pinned SQLite application with the retained original files, configuration and keys. Once PostgreSQL has accepted new telemetry or account changes, the old SQLite copy is stale. There is no automatic lossless reverse converter; stop and decide explicitly how to handle the new data before reverting.

Keep at least one verified recovery copy. If you later archive the old SQLite files outside the active state directory, move their WAL siblings with them and preserve the import report and original fingerprint evidence. Do not remove the only rollback copy to reclaim space.

## Native backups and restores

`GET /api/backup` and `/api/admin/users-backup` download PostgreSQL custom-format `.dump` archives (`application/octet-stream`). Account snapshots remain subject to their configured schedule and rotation. `pg_dump` 18 must be available to the application; the supplied image includes it.

`./manage.sh backup <directory>` creates `telemetry.dump`, `accounts.dump` and available config/theme/Caddy files. Each database archive is transactionally consistent; two sequential dumps are not one cross-database snapshot. Stop both writers when you need a coordinated pair. Preserve private credentials, queues/state and TLS material separately as part of instance recovery. Never copy a live PostgreSQL data directory as a logical backup.

`./manage.sh restore <directory>` requires both databases to be empty and the application stopped. It refuses SQLite files and nonempty destinations, uses transactional `pg_restore`, reapplies runtime grants and retains previous configuration copies. Services remain stopped for validation. For a single downloaded archive, use `pg_restore --no-owner --no-privileges --exit-on-error --single-transaction --dbname=<empty-database>` with connection credentials supplied through private libpq environment settings; then apply the appropriate grants and validation.

Account restoration can revive deleted accounts, old passwords, revoked sessions and already-used links. Record relevant deletions from the current audit log before replacement. With the restored server still stopped, disable those accounts and invalidate sessions/tokens as required, then repeat account deletion through the application so mail addresses are anonymized and the deletion is audited. See [account recovery](user-guide/accounts.md#backups).

## Test fixtures and benchmark evidence

The committed historical fixture contains a duplicate observation identity group. CI explicitly repairs a disposable copy with `scripts/prepare-postgres-fixture.go`, reports before/after counts, freshens/seeds that copy, then runs `scripts/migrate-fixture-hashes.go` using the shared runtime hash implementation. These are test-fixture tools, not production repair flags. The strict importer still refuses the unreviewed original fixture.

Run the PostgreSQL integration suites with `CORESCOPE_TEST_POSTGRES_URL` pointing to a disposable PostgreSQL 18 administrator connection and native 18.x dump/restore clients on `PATH`. Missing database configuration fails the tests. Performance reports must compare the pinned SQLite baseline and exact candidate with matched compiler, workload, durability and resource limits; migration time includes snapshotting, normalization, verification and `ANALYZE`. No speedup is assumed.
