# Choose, update and recover storage

CoreScope uses **SQLite by default**. PostgreSQL 18.6 is optional. Both keep telemetry and accounts separate; the ingestor writes telemetry and the server reads it. Accounts remain at their recorded target while user management is disabled. If a legacy installation never initialized accounts, enabling them is an explicit setup operation: stop writers and run `./manage.sh setup` after editing the account configuration. Restart alone does not invent a missing account database.

Use the [measured backend comparison](performance/optional-backend-b5-2026-10-09.md) to compare latency, resource use and conversion overhead before choosing PostgreSQL.

Choose for the hardware and workload, not the age of the installation. SQLite
keeps the deployment small; PostgreSQL separates database resources and can help
with concurrent writes and maintenance, but adds a service and its own memory,
storage and backup costs. Neither backend is universally faster. The hot packet
cache now accounts for SQLite observation fan-out when admitting history: under
the same memory budget it may retain a shorter hot window. Omitted history stays
in SQL; a smaller cache does not change database retention.

After startup loading finishes, the server emits one `[store] startup admission`
summary with the configured memory budget, admitted transmission and observation
counts, tracked memory estimate and retained `firstSeenRange`. `coverageFrom` is
the conservative complete-coverage boundary; an older first-seen timestamp can
belong to a long-lived hash and does not establish complete history coverage.
`stop=memory_budget` means the bounded cache is ready, `window_complete` means the
requested window finished, and `error` leaves startup unready.
`formerPolicyTransmissionEstimate` describes the previous admission formula
(ten assumed observations per transmission and a 1,000-transmission minimum);
it is not a measured previous memory footprint. Use `/api/healthz` for readiness
and SQL-backed views to inspect history outside the admitted cache.

## Install

Keep a complete checkout pinned to the same release as the image. For the usual single-host installation, preserve an existing private `.env` or copy `.env.example`, set `DATA_DIR` to the intended persistent directory, then run:

```sh
docker compose -f docker-compose.example.yml up -d
```

No PostgreSQL variables or service are required. Before supervisor starts either process, packaged setup validates an existing supported SQLite installation or initializes only a provably fresh one. A missing selection alone is not permission to discard data. If setup reports an error, keep the application stopped and preserve the files.

For a **new PostgreSQL installation**, merge `.env.postgres.example` into the private `.env`. Generate a distinct `openssl rand -hex 32` password for each blank field only before the first initialization of an empty PostgreSQL directory. Then add the matching override:

```sh
docker compose -f docker-compose.example.yml -f docker-compose.example.postgres.yml up -d
```

The override starts a separate PostgreSQL service and a short-lived owner bootstrap. Runtime containers receive reader, writer, account and approved-channel roles, never owner credentials. There is no published database port. Existing SQLite data is not converted by adding this file.

For source installations, `./manage.sh setup` offers SQLite (default) or PostgreSQL. It preserves existing private environment settings and keeps an installed backend. Its production files are `docker-compose.yml` / `docker-compose.no-mosquitto.yml`; PostgreSQL adds `docker-compose.postgres.yml`. Staging uses its own base and `docker-compose.staging.postgres.yml`, persistent directories and project. The example deployment is separate from the variants managed by `manage.sh`.

Success means setup exits successfully, `/api/healthz` reports ready, and actual packets appear through the API/UI. A running container or PostgreSQL health check alone is not evidence of completed setup or authentication.

## Keep an installed backend on update

`storage-selection.json` in the shared state directory records the backend and both targets atomically. It contains no passwords. Old `db.backend`, `CORESCOPE_DB_BACKEND`, `dbPath` and account-path bootstrap hints cannot replace a recorded target. Credentials and TLS settings stay in private config/environment and can rotate without changing the endpoint identity.

Use the same state mount and Compose files on restart/update. `./manage.sh update` keeps the recorded backend. For a pre-built image, pull the matching image and run `up -d` with the same files, including the PostgreSQL override when selected. Removing an override does not perform a reverse conversion.

Before a PostgreSQL schema-version upgrade, take native backups of all recorded
stores, including disabled accounts, and preserve the matching state/configuration
and previous image. Stop both application writers
for the update. Managed bootstrap checks readiness and, when necessary, invokes
the offline owner upgrade for the existing recorded targets before runtime
starts. A supported older schema is reported as `upgrade required` with its
stored and target versions, followed by the verified upgrade result. The
provisional readiness error is not printed after a successful upgrade.
Missing, corrupt or future schemas still fail; their diagnostics remain visible
and runtime services stay stopped. Bootstrap rechecks readiness before granting
runtime access. It cannot replace missing initialized databases or add an absent account
store. Previously initialized accounts are upgraded even while their UI is
disabled. Native deployments use the same operation with owner credentials in
private environment configuration:

```sh
corescope-migrate -storage-action=upgrade -offline \
  -selection-file /absolute/state/storage-selection.json -config-dir /absolute/config
```

Telemetry and accounts upgrade in separate database transactions; there is no
cross-database atomicity guarantee. Keep both writers stopped until upgrade and
readiness succeed. A failure keeps application startup blocked.
Preserve both databases and retry the supported upgrade after correcting the
reported problem. An older image may reject the newer schema, so reverting only
the image is not a complete rollback: restore both stores from their matching pre-upgrade backups
when required. Do not delete the selection or initialize replacement stores.

Container state must stay at `/app/data` or a persistent subdirectory, selected with `CORESCOPE_STATE_DIR`. All supplied services share that value. The entrypoint refuses an ephemeral directory or a symlink escaping the data mount. Native relative SQLite paths resolve from the process working directory; container processes run in `/app`. A `config.json` that exists but cannot be read or parsed stops startup; it is not ignored in favour of defaults.

Before the first upgrade from an older deployment, stop that old stack using its actual Compose project/service and checkout, before replacing configuration files. Preserve its checkout/Compose files, image identity, private `.env`, config/theme and a quiescent copy of the complete data directory including WAL/SHM siblings. Do not use a raw copy of a live SQLite main file as a backup.

## Explicit offline backend change

Plan a maintenance window and measure disk headroom on a representative copy. Keep source data, recovery snapshots and the new target together until validation is complete. Data/index/WAL and process memory costs differ between engines; one synthetic benchmark does not size every installation.

The published B5 experiment observed a PostgreSQL DB/cluster plus WAL peak about
**2.12 times** the SQLite database plus sidecars (about **112% more**). This is an
observation, not a sizing multiplier or guarantee. Using that ratio only as an
illustration, source + one recovery copy + PostgreSQL alone is `1 + 1 + 2.12 =
4.12` times the source: a 16.8 GB source would total about **69.2 GB** before
additional working space. The current forward importer also retains a normalized
working snapshot. Allow separately for that copy, account data, index building,
temporary work and additional WAL. The measured PostgreSQL peak already included
1 GiB of WAL; it does not bound WAL on another installation. Rehearse the actual
conversion and keep its observed free-space margin.

For the managed production deployment:

```sh
./manage.sh storage status
./manage.sh backup ./backups/before-storage-change
./manage.sh storage switch postgres   # or: sqlite
```

The switch asks for confirmation, stops the owned application processes, starts only the database service when needed, and invokes the verified converter. It does not bootstrap the destination schema before conversion. Existing accounts participate even when their UI is disabled. A PostgreSQL destination must be empty; preserve an old cluster and choose a new PostgreSQL directory instead of overwriting it. Switching to SQLite creates new destination paths under the existing persistent state directory, preserving the old source files.

The converter snapshots/normalizes supported source data, copies bounded batches, verifies table counts, typed logical digests and reference/identity high-water marks, applies narrow destination grants, and checks runtime connections. Both requested stores must verify before the single selection replacement. The state directory itself does not move.

The optional SQLite hop index is derived data, not a telemetry source. Conversion
accepts only the known v1 layout: `observation_hops`,
`observation_hops_coverage`, and all three `trg_observation_hops_ai`, `_ad`, `_au`
triggers with their expected definitions. Partial layouts, changed definitions
and extra indexes on these tables are refused. The converter removes these
objects only from its private normalized snapshot; the original source and
recovery copy retain them. Reports list both tables in `excluded_derived` with
their schema version and rebuild reason, outside the canonical row/digest
comparison.

`retention.hopIndexDays` controls this cache on SQLite only. PostgreSQL uses its
native reach query and logs that the SQLite cache option is inactive; no
PostgreSQL hop-index schema is installed. After conversion back to SQLite,
the cache is absent until the ingestor normally installs and rebuilds it when
`hopIndexDays` is positive.

On success, inspect the credential-free report and recorded targets. The management command leaves writers stopped:

```sh
./manage.sh storage status
./manage.sh start
```

Validate packet/observation counts, raw bytes, paths, live ingestion, accounts/login, existing sessions, settings, proposals and notifications as applicable. Preserve source and recovery data after success. A pre-cutover copy becomes stale once the new backend accepts writes; it is not a lossless reverse migration. To return while retaining new writes, perform a verified switch in the other direction.

### Interrupted conversion

A pending journal blocks startup. Do not delete it, rewrite the selection by hand, or manually mark a target ready. Keep the same source, configured PostgreSQL host/port/schema, target paths, recovery directory and matching converter version. Status supplies the job ID:

```sh
./manage.sh storage status
./manage.sh storage resume JOB_ID
# Or, only while the old selection is still authoritative:
./manage.sh storage abort JOB_ID
```

Resume rechecks saved source/target identity, snapshots, progress, data and sequences. Changed data or mismatched evidence is refused. Abort is allowed only before selection publication and preserves staged/recovery files. If preparation failed before a complete manifest existed, preserve the incomplete directory and original files; follow the reported recovery instructions instead of inventing progress or deleting the only snapshots.

The native CLI has the same actions through `-storage-action`, always with an absolute `-selection-file`, and `-offline` for mutation. Its `-state-dir` is private recovery/job storage, not the stable application state directory. Use `-config` / `-config-dir` for the same fail-closed configuration rules as runtime. Owner connections belong in `CORESCOPE_DATABASE_URL` and, for new storage actions, `CORESCOPE_USERS_OWNER_DATABASE_URL`; `CORESCOPE_USERS_DATABASE_URL` remains the runtime account writer. Never put credential URLs in command arguments. Custom/external PostgreSQL endpoints require explicit native owner tooling; managed backup/restore refuses to substitute the local default database.

## Native backups and restore

`./manage.sh backup DIRECTORY` creates native SQLite `telemetry.db` / `accounts.db` snapshots or PostgreSQL `telemetry.dump` / `accounts.dump`, plus a private `state.tar`, matching `compose.env`, and available config/theme/Caddy files. SQLite uses a read-only native backup that includes committed WAL pages. The state archive excludes the selected live SQLite files/sidecars and PostgreSQL storage. An absent, never-created SQLite account store is recorded explicitly. Use a new empty backup directory outside live data.

Each database snapshot is consistent; sequential snapshots plus copied state are not one cross-database transaction. Stop both writers when a coordinated pair/queue state is required. Keep the bundle and matching private `.env` encrypted off-host; do not commit or upload real databases, account state or credentials.

For SQLite restore, stop the application, retain the current data directory, and select a **new empty** `PROD_DATA_DIR`. `./manage.sh restore DIRECTORY` stages the state archive and native files, validates schemas and integrity, then publishes the restored directory. It refuses occupied targets, archive links/escaping paths and targets outside the persistent mount. Failure leaves source backups and staged recovery material intact. The services remain stopped for review. Older standalone snapshots or custom layouts use the native recovery procedure instead of guessing a selection record.

For PostgreSQL restore, keep the application stopped and preserve its matching state/selection and private settings. Select a new PostgreSQL storage directory, start only the database service, and restore into two empty databases with `./manage.sh restore DIRECTORY`. Do not run schema bootstrap first. The helper refuses occupied targets, restores transactionally per database, reapplies grants and analyzes data. If one restore fails, keep everything stopped; do not rerun into the already-restored database. For a lost host, restore and review the matching private state/selection before database restoration, using the same container paths. See [native PostgreSQL recovery](postgresql-upgrade.md#native-backups-and-restores).

Restoring accounts can revive deleted users, old passwords, revoked sessions and used links. Preserve relevant current audit entries before replacing state; disable affected accounts and clear restored sessions/tokens before serving traffic, then repeat deletion through the application to anonymize mail history and audit it. See [account backups](user-guide/accounts.md#backups).

## Supported source layouts

| Store | Supported current/upgrade boundary |
|---|---|
| SQLite telemetry | Known v3 layout and supported append-only migrations; older/unknown layouts require the prior version's explicit upgrade/repair |
| SQLite accounts | Known versions0–6 and the exact fork identity-v6 layout upgrade to v7; includes companion links, non-reusing identities and explicit mail-event IDs |
| PostgreSQL telemetry/accounts | Telemetry readiness version 3, with explicit offline owner upgrade from versions 1 and 2 (adds reported-node names); account readiness version1 and logical schema version6 |

Unknown/future columns, hidden generated columns, unsupported objects or conflicting data refuse conversion. Preserve a raw recovery snapshot before an explicit repair/upgrade of a disposable working copy. Deleted legacy integer IDs without allocator or retained-reference evidence have no recoverable historical high-water mark. Do not infer one or discard references to make validation pass.

The large-instance preparation work tracked by [#2214](https://github.com/Kpa-clawbot/CoreScope/issues/2214)
is still pending. This update does not add a snapshot bypass or accept arbitrary
triggers. An unsupported schema remains a stop condition; retain the original
and recovery copies rather than deleting objects merely to pass validation.
