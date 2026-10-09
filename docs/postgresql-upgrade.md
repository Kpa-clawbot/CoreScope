# PostgreSQL installation and offline upgrade

CoreScope uses PostgreSQL for telemetry and optional accounts. The tested database version is PostgreSQL 18.6. Keep the application checkout, image, shared Compose file, bootstrap scripts and configuration example from the same reviewed revision.

Existing SQLite instances need a planned outage. Stop **both** the ingestor and server: the account janitor, sessions, proposals and notification evaluator also write data. MQTT messages arriving during this window may be lost unless the broker and publishers provide a separately verified replay mechanism.

For an existing instance, the upgrade path is:

1. [Stop the old deployment and save its recovery bundle](#preflight-and-recovery).
2. [Preserve configuration and select the correct source directories](#point-the-new-deployment-at-the-right-files).
3. [Run the offline import](#import-with-the-application-stopped). On interruption, use the [resume procedure](#interruption-and-resume).
4. Read the [rollback boundary](#acceptance-and-rollback), then [verify the sources, grant permissions and start](#check-the-retained-source-then-grant-and-start).
5. Check live ingestion and account access, then [verify native backup recovery](#native-backups-and-restores).

## Databases and credentials

The supplied deployment creates `corescope_telemetry` and `corescope_accounts` on one PostgreSQL service. They are separate logical databases, with `C` text collation. Runtime URLs must name one host and database; use the default `public` schema for native backups.

| Role | Purpose |
|---|---|
| `corescope_owner` | Offline import, schema bootstrap and privilege grants. Keep it out of runtime processes. |
| `corescope_reader` | Server telemetry reads and native telemetry backups. |
| `corescope_writer` | Ingestor telemetry writes and retention; no schema ownership. |
| `corescope_accounts` | Account data writes; no schema or readiness-marker writes. |
| `corescope_channels` | Ingestor reads of the approved-channel view; no access to passwords or sessions. |

Preserve an existing private `.env` and merge only missing settings from `.env.example`; do not replace its paths, ports, broker settings or passwords. For a new PostgreSQL data directory, fill each missing password field with a different value from `openssl rand -hex 32` and keep `.env` permissions at `0600`. PostgreSQL creates roles only on first initialization: editing `.env` later does **not** rotate their passwords. If an interrupted installation has lost its matching `.env`, recover that file or perform a coordinated administrator password rotation with all application services stopped; do not delete the PostgreSQL volume to fix authentication. The Compose files supply runtime URLs; credentials do not belong in Git, screenshots, issue reports or command arguments. On native installations, use private environment configuration with `CORESCOPE_READER_DATABASE_URL`, `CORESCOPE_WRITER_DATABASE_URL`, `CORESCOPE_USERS_DATABASE_URL` and `CORESCOPE_APPROVED_CHANNELS_DATABASE_URL`.

`databaseURL` replaces `dbPath`; `CORESCOPE_DATABASE_URL` is the per-process fallback. Remove legacy `dbPath`, `userManagement.dbPath` and `DB_PATH` settings after preserving their old values for recovery. `stateDir` / `CORESCOPE_STATE_DIR` is a separate filesystem directory for queues, statistics and account backups. A database URL is never a directory name.

Main runtime pool limits are four server telemetry connections, one ingestor connection and one account connection. Bootstrap, identity checks and backups open additional connections. Connection attempts default to a ten-second upper bound; a shorter URL `connect_timeout` is retained. Native application dumps also have a ten-minute deadline. Backend credentials and pool limits are not theme/customizer controls.

## Supported SQLite sources

| Source | Handling |
|---|---|
| Telemetry v3 with `observations.observer_idx` and the known optional columns | Supported. Missing supported columns/tables are added only to a working copy. |
| Telemetry v2 with `observer_id`, or an older Node-era layout | Refused. Upgrade a recovery copy using the last SQLite release, review the result, then import it. |
| Account `schema_version` 0–5 | Supported, including empty version 0. Each declared version must have its complete historical shape. |
| No account database has ever existed | Omit `-users-from-sqlite`; the configured account destination is initialized empty. |
| Accounts currently disabled, but a users database exists | Import it with `-users-from-sqlite` so accounts, audit records and tokens remain available if re-enabled. |
| Unknown/newer columns, tables, views, triggers or virtual tables | Refused, with no silent field or record loss. |
| Duplicate observation identity, empty required transmission fields or case-colliding node keys | Refused. Perform an explicit, reviewed repair on a disposable recovery copy with the last SQLite release. |

Malformed JSON strings, empty strings, nulls, timestamp text, fractional scores, mixed-case values, ID zero and ID gaps are preserved when PostgreSQL can represent them. Invalid UTF-8, embedded NUL text, incompatible numeric values and records over the native 64 MiB SQLite length limit are refused. The importer never silently coerces incompatible records into zero or null.

Identity sequences are reseeded above imported IDs, available `sqlite_sequence` high-water marks and historical references, including orphan observer references and deleted users mentioned by audit records. SQLite integer primary keys without `AUTOINCREMENT` do not retain a deleted high-water mark once all references disappear; that information cannot be recovered or invented.

## Preflight and recovery

1. While still using the **old checkout and its original `.env`**, record the Compose file, project name, application service/container, image tag **and image ID**, and the actual telemetry/account SQLite paths. Read the old `dbPath`, `userManagement.dbPath` and `DB_PATH` values before removing them. A custom `stateDir` does not prove where either database lives.
2. Stop the old deployment with its own command, before switching checkout or configuration. For example, from that checkout, use `docker compose -f "$OLD_COMPOSE" -p "$OLD_PROJECT" stop "$OLD_SERVICE"` with the recorded values; old `manage.sh` installations can use their existing `./manage.sh stop`. Verify the old application container is stopped, and stop any separately launched server, ingestor or maintenance writer. The new Compose project may have a different name and may require settings the old one never used.
3. Make a private, quiescent recovery bundle containing the old Compose files/scripts, `.env`, image identity, configuration/theme, keys and **whole state/source directories**, including SQLite `-wal` and `-shm` siblings. Keep an untouched original. Do not use the new PostgreSQL-only `manage.sh backup` to back up SQLite. Restore the preserved old configuration and image together if rolling back.
4. Select new, empty PostgreSQL storage and allocate a private migration-state directory. It will hold a WAL-aware recovery snapshot, normalized working snapshot and source/destination manifest for each store. Snapshot creation uses SQLite's backup API so hidden observer/mail-event row IDs do not change.
5. Measure free space against the actual source, both snapshot copies, PostgreSQL data/indexes, WAL and temporary work. The required space and duration depend on the data; no fixed multiplier or downtime estimate is guaranteed.

For a host-side copy, after stopping every writer, choose a new private directory outside the active state tree and copy each complete source/state directory with `cp -a`. Include custom account directories too. Never omit a nonempty WAL or run `VACUUM` on the only recovery copy. If an uncheckpointed WAL cannot be read from a read-only mount, import a quiescent copy of the whole source directory from private writable storage, while retaining the original unchanged.

## Compose cutover

The common path below uses `docker-compose.example.yml`. Keep the complete pinned checkout and matching `CORESCOPE_IMAGE`; a downloaded YAML alone is insufficient because it extends `docker/postgres.compose.yml` and mounts the initialization scripts. For production/staging variants, retain their existing project name, Compose file and service names throughout.

### Point the new deployment at the right files

Preserve/merge `.env`; do not regenerate passwords after PostgreSQL has initialized. In the example variant, set `DATA_DIR` to the **existing absolute host state directory**, `POSTGRES_DATA_DIR` to the selected PostgreSQL storage and `CORESCOPE_IMAGE` to the matching reviewed image. `PROD_DATA_DIR` and `STAGING_DATA_DIR` do not control this variant. Set these before running bootstrap. Keep PostgreSQL storage separate from state/recovery directories.

Inside the supplied container, the selected host `DATA_DIR` is always `/app/data` and the runtime state directory is `/app/data`. Keep config/theme/queues there; remove legacy database-path settings from the active config and any `/app/data/.env` only after saving their original values in the recovery bundle. Preserve unrelated configuration and broker/mail credentials.

Confirm the exact source mounts before initializing schemas. This check does not start the application or PostgreSQL:

```sh
set -eu
docker compose -f docker-compose.example.yml run --rm --no-deps \
  --entrypoint /bin/sh bootstrap -eu -c \
  'test -f /app/data/config.json; test -f /app/data/meshcore.db; test -f /app/data/users.db'
```

Remove the `users.db` check and import flag only when no account source exists, even if account features are currently disabled. If the old installation intentionally had no config file, omit that one file check. For custom SQLite filenames or directories, add a read-only mount such as `-v "$SOURCE_DIR:/legacy:ro"` to **every import and source-check command** and use `/legacy/the-actual-name.db`. Verify those filenames with the same mount first. Keep both sources mounted as directories so their WAL siblings remain visible.

### Import with the application stopped

```sh
set -eu
docker compose -f docker-compose.example.yml up -d --wait postgres
install -d -m 700 migration-state

docker compose -f docker-compose.example.yml run --rm \
  --entrypoint /app/corescope-migrate \
  -v "$PWD/migration-state:/migration" bootstrap \
  -offline -from-sqlite /app/data/meshcore.db \
  -users-from-sqlite /app/data/users.db -state-dir /migration
```

The bootstrap service supplies owner URLs through its environment. Its PostgreSQL health check only proves the server is accepting connections; successful importer authentication is still required. The importer copies in bounded batches, commits progress with each batch, verifies counts and logical digests in primary-key order, reseeds identities and analyzes the imported tables. Both requested imports must verify before readiness finalization begins.

Success is exit status zero, a JSON report with `"verified":true` for each imported store, followed by:

```text
telemetry import verified and finalized; keep the recovery snapshot for rollback
accounts import verified and finalized; keep the recovery snapshot for rollback
```

Without an account source, the account line is `accounts PostgreSQL schema initialized`. **On any error, keep all application services stopped.** A successful report for telemetry alone is not a successful two-store cutover.

### Interruption and resume

Keep the same source files, configured PostgreSQL host/port, database/schema and migration-state directory, then repeat the import command with `-resume` using the same pinned importer image. A container IP change behind the same configured DNS endpoint does not change import identity. Resume also handles a store not yet started and an interruption between the two readiness commits. An already-ready store is accepted only after its source/snapshot/target identity, saved verification report, completed progress, every table digest and identity sequence are rechecked without rewriting it. New PostgreSQL data or changed evidence refuses resume; it is not a way to re-import after cutover.

If preparation stopped **before `<store>/manifest.json` exists**, preserve that incomplete `<store>` directory and the originals. Do not delete snapshots or guess a manifest. Confirm that store's PostgreSQL destination is still empty, move its incomplete per-store directory to a separate private recovery location, and retry with `-resume`; keep completed stores' state directories in place. This creates new preparation state only for the untouched store. If the destination has data but its manifest is missing, restore the matching state from recovery or investigate before proceeding. Never manually set readiness true.

### Check the retained source, then grant and start

If an old source needed an explicit repair/upgrade, the verified import belongs to the **repaired copy**, not the untouched original. Keep the original database and its WAL/SHM siblings together in the private rollback bundle outside `/app/data`. Put the exact repaired source used for import at the active default guard path, with its matching sidecars, or retain it at its custom path and run the explicit matching source check. Never leave the unrepaired original at `/app/data/meshcore.db` or `/app/data/users.db` and then bypass a fingerprint refusal. Do not repair or rewrite the accepted source after import.

```sh
set -eu
docker compose -f docker-compose.example.yml run --rm \
  --entrypoint /app/corescope-migrate bootstrap \
  -check-import-kind=telemetry -from-sqlite /app/data/meshcore.db

docker compose -f docker-compose.example.yml run --rm \
  --entrypoint /app/corescope-migrate bootstrap \
  -check-import-kind=accounts -users-from-sqlite /app/data/users.db

docker compose -f docker-compose.example.yml run --rm \
  --entrypoint /app/corescope-migrate bootstrap -check-ready

docker compose -f docker-compose.example.yml run --rm bootstrap
docker compose -f docker-compose.example.yml up -d corescope
```

Skip the account-source check only when there was no account source. The source checks print `<store> verified import matches the retained SQLite source`; readiness prints `<store> PostgreSQL schema is ready` for both configured destinations. Normal bootstrap checks retained `/app/data/meshcore.db` and `/app/data/users.db`, then grants restricted runtime privileges. A manually initialized empty database does not satisfy this upgrade guard. Custom source paths require the explicit checks above because automatic bootstrap only checks those two default paths. Absent and zero-length WAL sidecars share a fingerprint; every nonempty WAL byte remains significant.

The example publishes HTTP port 80 through bundled Caddy. Open the configured host port and verify `/api/healthz` plus actual packet ingestion. If you explicitly disable Caddy, your proxy or port mapping must instead reach container port 3000.

For native installations, invoke `corescope-migrate` with owner URLs in `CORESCOPE_DATABASE_URL` and, when applicable, `CORESCOPE_USERS_DATABASE_URL`. The same `-offline`, source, state-directory and resume flags apply. `-check-ready` performs read-only schema checks. Run `docker/postgres-grants.sql` as the owner once in each database after bootstrap/import, then start the services with their restricted runtime URLs and a shared state directory.

## Acceptance and rollback

Verify packet and observation counts, representative raw/null values, observers and paths, live ingestion/WebSocket visibility, retention, account login, existing sessions/CSRF, proposals, notifications and account export. Test accounts enabled and disabled as applicable. Restore a native backup into fresh isolated databases and exercise the API/login there before declaring recovery proven.

Rollback **before PostgreSQL accepts new writes**: stop the new application with its new Compose project, leave its PostgreSQL storage and migration evidence intact, then return to the preserved old checkout/Compose project, original `.env`, pinned SQLite image and untouched source/state directories. Restore the old database-path settings from that bundle; the PostgreSQL configuration cannot be passed to the SQLite release. Confirm the new writers are stopped before starting the old application. Once PostgreSQL has accepted new telemetry or account changes, the old SQLite copy is stale. There is no automatic lossless reverse converter; stop and decide explicitly how to handle the new data before reverting.

Keep at least one verified recovery copy. If you later archive the old SQLite files outside the active state directory, move their WAL siblings with them and preserve the import report and original fingerprint evidence. Do not remove the only rollback copy to reclaim space.

## Native backups and restores

`GET /api/backup` and `/api/admin/users-backup` download PostgreSQL custom-format `.dump` archives (`application/octet-stream`). Account snapshots remain subject to their configured schedule and rotation. `pg_dump` 18 must be available to the application; the supplied image includes it.

For the production variant selected by `manage.sh` (not `docker-compose.example.yml`), `./manage.sh backup <directory>` creates `telemetry.dump`, `accounts.dump` and available config/theme/Caddy files. Each database archive is transactionally consistent; two sequential dumps are not one cross-database snapshot. Stop both writers when you need a coordinated pair. Preserve private credentials, queues/state and TLS material separately as part of instance recovery. Never copy a live PostgreSQL data directory as a logical backup.

`./manage.sh restore <directory>` requires both databases to be empty and the application stopped. It refuses SQLite files and nonempty destinations, uses transactional `pg_restore`, reapplies runtime grants and retains previous configuration copies. Services remain stopped for validation. For a single downloaded archive, use `pg_restore --no-owner --no-privileges --exit-on-error --single-transaction --dbname=<empty-database>` with connection credentials supplied through private libpq environment settings; then apply the appropriate grants and validation.

For the **example Compose variant**, use its PostgreSQL container directly. This helper reads the owner password already present inside that container; it does not put a URL or password in command arguments. Run it from the matching checkout with the matching private `.env`:

```sh
pg_owner() {
  docker compose -f docker-compose.example.yml exec -T postgres sh -eu -c '
    export PGHOST=127.0.0.1 PGUSER=corescope_owner PGSSLMODE=disable
    export PGPASSWORD="$CORESCOPE_OWNER_PASSWORD"
    exec "$@"
  ' sh "$@"
}
```

For a coordinated backup pair, stop the application, create a new private backup directory, then dump both stores. Keep the application stopped until both commands succeed; a failed `.partial` file is not a backup.

```sh
set -eu
umask 077
docker compose -f docker-compose.example.yml stop corescope
BACKUP_DIR=$(mktemp -d "$PWD/corescope-backup.XXXXXX")
for store in telemetry accounts; do
  pg_owner pg_dump --no-password --format=custom --no-owner --no-privileges \
    --dbname="corescope_$store" > "$BACKUP_DIR/$store.dump.partial"
  mv "$BACKUP_DIR/$store.dump.partial" "$BACKUP_DIR/$store.dump"
done
# Preserve matching .env, config/theme, keys and remaining state privately too.
docker compose -f docker-compose.example.yml up -d corescope
```

Restore into **new empty PostgreSQL storage**, keeping the previous storage intact. With the original Compose file/project and `.env`, stop `corescope` and then `postgres` before changing any storage path. Record the old `POSTGRES_DATA_DIR`, select a new empty path in `.env`, use matching recovered credentials, and keep `corescope` stopped. Restore the matching config/theme and remaining state from the private bundle before bootstrap; preserve their previous copies. Set `BACKUP_DIR` to the verified backup bundle. Before either restore, check that both destinations are empty:

```sh
set -eu
docker compose -f docker-compose.example.yml up -d --wait postgres
for store in telemetry accounts; do
  test -f "$BACKUP_DIR/$store.dump"
  objects=$(pg_owner psql -X -v ON_ERROR_STOP=1 -At --dbname="corescope_$store" \
    -c "SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind IN ('r','p','v','m','S','f')")
  test "$objects" = 0 || { echo 'Restore destination is not empty; stop here.' >&2; exit 1; }
done
for store in telemetry accounts; do
  pg_owner pg_restore --no-password --no-owner --no-privileges \
    --exit-on-error --single-transaction --dbname="corescope_$store" \
    < "$BACKUP_DIR/$store.dump"
done
docker compose -f docker-compose.example.yml run --rm bootstrap
docker compose -f docker-compose.example.yml run --rm \
  --entrypoint /app/corescope-migrate bootstrap -check-ready
```

If one restore fails, the other database may already have restored successfully: keep services stopped and preserve both the failure evidence and old storage. Do not retry into an occupied database. After successful grants/readiness and the account checks below, validate the restored instance in isolation before starting it for normal traffic.

Account restoration can revive deleted accounts, old passwords, revoked sessions and already-used links. Record relevant deletions from the current audit log before replacement. With the restored server still stopped, disable those accounts and invalidate sessions/tokens as required, then repeat account deletion through the application so mail addresses are anonymized and the deletion is audited. See [account recovery](user-guide/accounts.md#backups).

## Test fixtures and benchmark evidence

The committed historical fixture contains a duplicate observation identity group. CI explicitly repairs a disposable copy with `scripts/prepare-postgres-fixture.go`, reports before/after counts, freshens/seeds that copy, then runs `scripts/migrate-fixture-hashes.go` using the shared runtime hash implementation. These are test-fixture tools, not production repair flags. The strict importer still refuses the unreviewed original fixture.

Run the PostgreSQL integration suites with `CORESCOPE_TEST_POSTGRES_URL` pointing to a disposable PostgreSQL 18 administrator connection and native 18.x dump/restore clients on `PATH`. Missing database configuration fails the tests. Performance reports must compare the pinned SQLite baseline and exact candidate with matched compiler, workload, durability and resource limits; migration time includes snapshotting, normalization, verification and `ANALYZE`. No speedup is assumed.
