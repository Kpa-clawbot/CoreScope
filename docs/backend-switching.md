# Choosing and switching storage

A fresh installation defaults to SQLite. PostgreSQL is an explicit installation choice. An update retains the installed backend and both selected database targets, including an account store whose feature is disabled.

The stable state directory contains one `storage-selection.json`. After setup, that record is authoritative for the backend and SQLite file paths. Config/env backend selectors and file paths are bootstrap inputs, so an old `db.backend: sqlite` does not undo a completed PostgreSQL switch. PostgreSQL credentials and TLS settings remain outside the record; they may rotate but must still identify the recorded host, port, database and schema.

`accounts: null` means no account store has been initialized. A non-null target remains recorded even when account features are disabled. Runtime opens never create a missing account database. Fresh SQLite setup initializes its account store upfront; legacy adoption preserves any valid existing account file but records null when it is absent. Backend switches preserve that absence. Explicit setup can later initialize the first account target under an exclusive installation lease and atomically record it. PostgreSQL initialization requires supplied account owner and runtime credentials; ordinary setup with neither preserves null. A previously recorded missing target always fails instead of being replaced.

Keep the runtime state-directory setting stable across updates and switches. The migration tool's `-selection-file` identifies that record. Its separate `-state-dir` chooses private recovery/job storage; it defaults to `migration` beside the selection record.

## Inspect and initialize

The packaged startup runs guarded setup before either service starts. Direct installations can use the same command while every service is stopped:

```sh
corescope-migrate -storage-action=status \
  -selection-file /app/data/storage-selection.json

corescope-migrate -storage-action=setup -offline \
  -selection-file /app/data/storage-selection.json \
  -config-dir /app
```

Status reads only local metadata. It needs no PostgreSQL credentials or connection and returns one JSON object. `ready`, `unrecorded`, and `pending` return exit status 0; `corrupt` returns 1. **Unrecorded is not proof of a fresh installation.** Pending or corrupt state must never trigger an empty SQLite fallback or a new installation. Status includes credential-free selected targets and the stable state directory for native backup tools.

Setup validates an existing recorded choice, adopts a supported legacy SQLite installation, or initializes provably empty targets. `-storage-action=init` refuses existing schemas; `adopt` requires an existing supported telemetry store. A legacy SQLite writer/setup may apply supported native schema upgrades before recording its choice. Conversion copies, in contrast, leave their sources unchanged.

Use `-config FILE` for an explicit configuration file; missing or invalid explicit files fail. Otherwise `-config-dir DIR` checks `config.json`, then `data/config.json`, continuing only when a candidate is absent. An existing unreadable or malformed primary configuration stops setup. Explicit relative telemetry/account paths resolve from the process working directory. An omitted telemetry path defaults to `config-dir/data/meshcore.db`; accounts default to `users.db` beside telemetry. On unrecorded adoption, conflicting config `dbPath` and `DB_PATH` fail unless `-sqlite-path` explicitly resolves the conflict. `-users-sqlite-path` is the corresponding account override.

For a fresh PostgreSQL install, add `-backend=postgres`. This explicit choice overrides bootstrap config/env defaults. Setup refuses to initialize an empty PostgreSQL target while known primary SQLite data exists. A ready PostgreSQL installation with retained SQLite files needs a matching completed-import record or explicit adoption; switching live data uses the verified procedure below.

## Prepare an offline switch

Stop **every** writer and reader, including other hosts, scheduled jobs, and administrative database clients. Keep both original databases and their native backups. The tool takes local installation leases and native source locks; a local lease is not a distributed deployment coordinator.

Prepare fresh, unselected destination targets. Existing SQLite files are never overwritten, and existing PostgreSQL schema/data is not an import destination. Telemetry and accounts remain separate databases/files. Conversion includes every recorded account store even when account features are disabled.

Supply PostgreSQL credentials through the private environment, not process arguments or logs:

| Variable | New storage actions |
|---|---|
| `CORESCOPE_DATABASE_URL` | Telemetry migration owner for the PostgreSQL side |
| `CORESCOPE_USERS_OWNER_DATABASE_URL` | Account migration owner for the PostgreSQL side |
| `CORESCOPE_READER_DATABASE_URL` | Restricted telemetry reader |
| `CORESCOPE_WRITER_DATABASE_URL` | Restricted telemetry writer |
| `CORESCOPE_USERS_DATABASE_URL` | Restricted account runtime writer |
| `CORESCOPE_APPROVED_CHANNELS_DATABASE_URL` | Optional restricted approved-channel projection reader |

The owner creates/imports destination schemas, applies the existing narrow runtime grant policy, then verifies actual runtime access before selection changes. Runtime roles never gain schema ownership or DML on readiness/import metadata; the account writer cannot modify `schema_version`. Credentials are not stored in selection records, job manifests or command output. Role URL target overrides through query parameters or service settings are not supported.

SQLite to PostgreSQL:

```sh
corescope-migrate -storage-action=switch -backend=postgres -offline \
  -selection-file /app/data/storage-selection.json \
  -state-dir /app/data/migration
```

PostgreSQL to new SQLite files:

```sh
corescope-migrate -storage-action=switch -backend=sqlite -offline \
  -selection-file /app/data/storage-selection.json \
  -sqlite-path /app/data/returned/meshcore.db \
  -users-sqlite-path /app/data/returned/users.db \
  -state-dir /app/data/migration
```

For either direction, the owner URLs address the PostgreSQL side: the destination for the first command, the source for the second. Do not pre-bootstrap an import destination. Original invocation without `-storage-action` retains its prior PostgreSQL bootstrap/forward-import behavior, including its legacy account-owner environment convention.

## What verification covers

Native SQLite recovery snapshots retain WAL contents and hidden rowids; PostgreSQL recovery is a private `pg_dump` custom archive. Copying uses bounded batches and a 64 MiB per-row safety limit. Every application table is compared by complete row count and ordered canonical digest, including NULL/empty distinctions, raw text/JSON/timestamps and representable fractional values. Unsupported values or custom source schemas fail before selection instead of being coerced or discarded.

Identity verification includes retained IDs, deleted high-water marks, historical references and backfill cursors. PostgreSQL `last_value` and `is_called` are retained in the private manifest; SQLite non-reusing allocators are seeded above every retained/reference maximum. No dummy rows are inserted. Account SQLite schema 6 uses explicit `mail_events.id`; PostgreSQL keeps its native account schema version 5. Those schema version values describe the engine, not copied account data.

Reverse conversion adds required SQLite physical migration markers while preserving every source ledger entry. The private reverse manifest lists those additions. It builds the native observer/time index offline even if the retained async ledger already says the task is done. PostgreSQL-specific readiness/import bookkeeping is retained in its native recovery archive, not copied into application SQLite tables.

SQLite reverse targets contain `corescope_reverse_progress` until complete data verification. Native open/setup/adoption paths reject that marker, including through another state directory. Runtime schema readiness alone is never treated as proof that an import completed. The PostgreSQL source's managed table/column/constraint/index/view layout is checked against a versioned native manifest; custom extensions require explicit review.

Only after both stores verify and destination runtime access succeeds does one atomic replacement select the new backend and both targets together. Original files/databases and recovery copies remain available. This is a planned-downtime switch, not live replication or an automatic rollback service.

## Interruption and recovery

Inspect status to obtain the pending `job_id`. Keep services stopped and retain the same selection path, recovery directory and destination targets. Supply credentials again, then resume:

```sh
corescope-migrate -storage-action=resume -offline \
  -selection-file /app/data/storage-selection.json \
  -state-dir /app/data/migration -job-id JOB_ID
```

Resume rechecks the exact source, sequences, native recovery copy, destination and committed copy progress. A changed source or target is refused. An interruption before a complete preparation manifest may require preserving the failed job and starting a new job with fresh targets after abort; partial artifacts are not silently reused.

First account initialization uses the same journal, with identical source and target backends. Resume can finish an existing ready account target, without PostgreSQL credentials for SQLite. It never recreates a missing target or guesses that an empty target is fresh. If initialization stopped before readiness, preserve/inspect it, abort the uncommitted metadata update, then rerun explicit setup. A journal with no staged destination permits only metadata-only abort; no database connection is needed.

Before selection replacement, abort can reopen the original selected installation:

```sh
corescope-migrate -storage-action=abort -offline \
  -selection-file /app/data/storage-selection.json \
  -state-dir /app/data/migration -job-id JOB_ID
```

Abort preserves staged databases and recovery files. After selection replacement, abort refuses to restore an old selection; resume verifies and finishes that job instead. Inspect status after a cleanup error, since the verified new selection may already be committed. Returning later to the previous backend is another explicit verified conversion into fresh targets, preserving data written since the first switch.
