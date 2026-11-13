# PostgreSQL

CoreScope's measurement database (packets, nodes, observers, and the rest) can live in
SQLite or in PostgreSQL. SQLite is the default and needs no changes. PostgreSQL is an
explicit choice, and nothing migrates automatically. Both backends use the same binaries;
configuration picks one.

This guide covers choosing, setting up, moving to and from, backing up and troubleshooting
a PostgreSQL install. For SQLite maintenance (WAL, auto-vacuum) see the
[Database guide](user-guide/database.md).

- [When to choose PostgreSQL](#when-to-choose-postgresql)
- [Requirements](#requirements)
- [How it works](#how-it-works)
- [Set up with Docker Compose](#set-up-with-docker-compose)
- [Set up without Compose](#set-up-without-compose) (includes the Docker image and Kubernetes)
- [Moving an existing SQLite install](#moving-an-existing-sqlite-install)
- [Switching back to SQLite](#switching-back-to-sqlite)
- [Backups and restore](#backups-and-restore)
- [What is SQLite-only](#what-is-sqlite-only)
- [Monitoring](#monitoring)
- [Troubleshooting](#troubleshooting)

## When to choose PostgreSQL

Stay on SQLite unless you have a reason. It is one file, needs no extra service, and is the
most exercised path.

Consider PostgreSQL when:

- The install is large or keeps data for a long time, and SQLite's single writer plus its
  maintenance passes (retention prunes, `ANALYZE`, `VACUUM`) stall ingest.
- You already run PostgreSQL and its backup tooling and would rather manage one kind of
  database.

There is no general claim that PostgreSQL is faster. In one operator soak test of an earlier
implementation, retention pruning ran about 2.5x faster on PostgreSQL; choose by workload and
hosting, not by that number.

The result of every API call is the same on both backends. Two things stay on SQLite either
way: the optional user-accounts database `users.db` (see [Backups and restore](#backups-and-restore))
and a few SQLite-specific features (see [What is SQLite-only](#what-is-sqlite-only)).

## Requirements

- PostgreSQL 16 or newer (tested on 17). The ingestor refuses an older server.
- `psql` 15 or newer on the machine that runs `scripts/postgres-roles.sh` (the script reads
  passwords with `\getenv`). The `postgres:17-alpine` image used by the Compose override
  already has it.
- For moving data: `corescope-migrate`. The Docker image ships it at `/app/corescope-migrate`; otherwise build it from `cmd/migrate`.

## How it works

CoreScope runs with two PostgreSQL roles, and the split is enforced, not advisory.

| Role | Used by | Needs |
|------|---------|-------|
| Writer (default `corescope_writer`) | Ingestor | Owns the database and schema. It creates and migrates the schema (versioned migrations recorded in `schema_version`, applied under a PostgreSQL advisory lock). |
| Reader (default `corescope_reader`) | Server | `USAGE` on the schema and `SELECT` on its tables. Nothing else. |

The ingestor is the only process that writes measurement data, as it is with SQLite. It keeps
one writer connection on PostgreSQL too, because the server follows new rows by "id greater
than the last one seen" and ids must therefore commit in increasing order.

The server is read-only:

- It sets `default_transaction_read_only=on` on every connection.
- At startup it checks the privileges of its role and **refuses to start** if that role holds
  `INSERT`, `UPDATE`, `DELETE` or `TRUNCATE` on any table in its schema. Ownership, superuser
  and privileges inherited through role membership all count. The error prints the `GRANT`
  statements for a suitable read-only role.
- It also refuses to start when the schema is missing or older than it expects. Start the
  ingestor first.

`scripts/postgres-roles.sh` creates both roles and the database. Run it rather than writing
the grants by hand.

## Set up with Docker Compose

`docker-compose.postgres.yml` is an override for `docker-compose.yml`. It adds:

- a `postgres` service (`postgres:17-alpine`, data in the named volume `postgres-data`),
- a one-shot `postgres-init` service that runs `scripts/postgres-roles.sh` on every `up`
  (it is idempotent, so each start re-applies the roles' passwords and grants),
- `DB_DRIVER=postgres`, `DISABLE_INGESTOR=true` and only the reader DSN (`DB_DSN_SERVER`) on
  `prod`, which now runs Caddy, Mosquitto and the server,
- an `ingestor` service: the same image running `/app/corescope-ingestor` alone, with the writer
  DSN. It runs as a non-root uid (`INGESTOR_UID`, default 1000) with the share group added
  (gid `CORESCOPE_SHARE_GID`, default 61001), read-only, all capabilities dropped. It joins
  `prod`'s network namespace, so `mqtt://localhost:1883` in `config.json` still reaches the
  broker, and it mounts the same data directory.

The two containers are the security boundary for the writer credential: it is never in `prod`'s
environment or files. Because `ingestor` shares `prod`'s network namespace, restarting or
recreating `prod` alone leaves the ingestor attached to a dead namespace. Use
`docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d`, which restarts
both, not `docker restart` on `prod`.

1. Add three secrets to `.env`. Generate each one, for example with `openssl rand -hex 24`;
   the DSNs carry them unencoded, so keep them to URL-safe characters.

   ```
   POSTGRES_PASSWORD=<postgres-superuser-password>
   CORESCOPE_WRITER_PASSWORD=<writer-password>
   CORESCOPE_READER_PASSWORD=<reader-password>
   ```

   `POSTGRES_PASSWORD` is the `postgres` superuser, used only by `postgres-init`. Optional
   keys: `CORESCOPE_DB` (default `corescope`), `CORESCOPE_WRITER` (`corescope_writer`),
   `CORESCOPE_READER` (`corescope_reader`), `SERVER_RUN_AS_UID` (1001), `INGESTOR_UID` and
   `INGESTOR_GID` (1000), `CORESCOPE_SHARE_GID` (61001). The two uids must differ from each
   other and from 0.

2. Start the stack:

   ```bash
   docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d
   ```

   The ingestor creates the schema on its first start. Until it exists, the server refuses to
   start (see [Troubleshooting](#troubleshooting)).

3. Do **not** set `db.driver` or `db.dsn` in `config.json`. On PostgreSQL the database is chosen
   from the container's environment only (`DB_DRIVER`, `DB_DSN*`); a `config.json` that selects
   PostgreSQL, or contains a `dsn`, is refused at start.

`.env` lives beside the compose file and holds the writer password. Keep it out of git and make
it readable only by the account that runs Compose (`chmod 600 .env`). Do not put it, or any file
holding a DSN, in the data directory mounted at `/app/data`.

PostgreSQL is not published to the host; it is reachable only on the Compose network. To use
`psql` or `pg_dump` from the host, add this to the `postgres` service:

```yaml
    ports: ["127.0.0.1:5432:5432"]
```

Keep the `/app/data` mount. See [In the Docker image](#in-the-docker-image) for what the
container does with it on PostgreSQL (`/app/data/shared`, the separate server user). The
ingestor's I/O statistics for the perf page go through `/app/data/shared/ingestor-stats.json`,
which the override sets in both containers.

If you already have SQLite data, copy it before the first start; see
[Moving an existing SQLite install](#moving-an-existing-sqlite-install).

## Set up without Compose

1. Provision the roles and database as a PostgreSQL superuser:

   ```bash
   export PG_ADMIN_URL='postgres://postgres@db.example.org:5432/postgres?sslmode=require'
   export PGPASSWORD='<superuser-password>'
   export CORESCOPE_WRITER_PASSWORD='<writer-password>'
   export CORESCOPE_READER_PASSWORD='<reader-password>'
   sh scripts/postgres-roles.sh
   ```

   | Variable | Default | |
   |----------|---------|---|
   | `PG_ADMIN_URL` | — | Superuser connection, URL or conninfo. Required. |
   | `CORESCOPE_DB` | `corescope` | Database name |
   | `CORESCOPE_WRITER` | `corescope_writer` | Writer role |
   | `CORESCOPE_WRITER_PASSWORD` | — | Required |
   | `CORESCOPE_READER` | `corescope_reader` | Reader role |
   | `CORESCOPE_READER_PASSWORD` | — | Required |

   Names must match `^[a-z_][a-z0-9_]{0,62}$`, must not start with `pg_`, and must not be an
   existing superuser. The script is idempotent and never drops anything: a second run resets
   both passwords and re-applies the grants, revoking any write privilege the reader has since
   been given. Passwords never appear on a command line or in the output, but a server with
   `log_statement=ddl` or `all` logs them as it would for any `ALTER ROLE`.

   It prints the two connection URLs without passwords:

   ```
   CORESCOPE_WRITER_URL=postgres://corescope_writer@db.example.org:5432/corescope
   CORESCOPE_READER_URL=postgres://corescope_reader@db.example.org:5432/corescope
   ```

   Host and port are the ones the admin connection reached. Replace them if the application
   connects through another address, and add `sslmode` yourself.

   The script refuses a `PG_ADMIN_URL` that contains a password: supply it with `PGPASSWORD` (as
   above) or `~/.pgpass`, so it never appears in `ps` or shell history. It also refuses to adopt
   a database or role that already exists but that it did not create, unless you set
   `CORESCOPE_ADOPT=1`; set that only when you mean to hand an existing database to CoreScope.

2. Point CoreScope at the database. The ingestor gets the **writer** DSN, the server the
   **reader** DSN, so the two processes need different values.

   | Setting | Meaning |
   |---------|---------|
   | `db.driver` in `config.json` | `postgres` (aliases `postgresql`, `pg`; `sqlite`, `sqlite3` or empty is SQLite) |
   | `db.dsn` in `config.json` | `postgres://user:pass@host:5432/dbname?sslmode=require` |
   | `DB_DRIVER` | Same as `db.driver` |
   | `DB_DSN` | Same as `db.dsn` |

   Precedence follows each binary's `DB_PATH` rule. In the ingestor the environment overrides
   `config.json`. In the server `config.json` wins and the environment only fills empty keys.
   Because each process needs its own DSN, the simplest setup is the environment, set per
   process from a file only that process's user can read (for example a systemd
   `EnvironmentFile=`):

   ```
   # /etc/corescope/ingestor.env   (mode 600, owned by the ingestor's user)
   DB_DRIVER=postgres
   DB_DSN=postgres://corescope_writer:<writer-password>@db.example.org:5432/corescope?sslmode=require

   # /etc/corescope/server.env     (mode 600, owned by the server's user)
   DB_DRIVER=postgres
   DB_DSN=postgres://corescope_reader:<reader-password>@db.example.org:5432/corescope?sslmode=require
   ```

   A password in a DSN that the binaries read from the config or the environment is acceptable
   (it is not on any command line), but keep those files out of git, out of world-readable
   locations and out of the data directory, and above all keep the writer's DSN away from the
   server's user.

   `db.dsn` must be a `postgres://` URL or a `keyword=value` string. If you use a shared
   `config.json` for both processes, do not put a DSN in it; use the environment.

3. Start the ingestor first, then the server.

   Percent-encode special characters in a password inside a URL (for example `@` as `%40`).

   | Optional setting | Default | Meaning |
   |------------------|---------|---------|
   | `db.statementTimeoutSec` | `120` | PostgreSQL only: the server's sessions get this `statement_timeout`, in seconds. `0` disables it; a negative value is a startup error. It is set in `config.json` (it is not a secret, so it may live there even where a `dsn` may not). |

### In the Docker image

On PostgreSQL, run the ingestor in its own container, as `docker-compose.postgres.yml` does:
the writer credential then never reaches the container that runs Caddy and the server.

| Container | Settings |
|-----------|----------|
| Ingestor (writer) | `DB_DRIVER=postgres`, `DB_DSN` (the writer DSN); run `/app/corescope-ingestor` directly |
| Server (reader) | `DB_DRIVER=postgres`, `DISABLE_INGESTOR=true`, `DB_DSN_SERVER` (or `DB_DSN`, the reader DSN) |

With `DISABLE_INGESTOR=true` the container has no ingestor, and refuses to start if
`DB_DSN_INGESTOR` is set. `DB_DSN`, `DB_DSN_INGESTOR`, `DB_DSN_SERVER` and `DB_PATH` must not
contain a double quote, a backslash or a newline; use a `postgres://` URL and percent-encode the
password. Keep DSNs out of `config.json`, version control and shell history; put them in `.env`
or a secret store.

**Single container (not recommended).** Running both services in one container still works: set
`DB_DSN_INGESTOR` (writer) and `DB_DSN_SERVER` (reader), each defaulting to `DB_DSN`, and the
entrypoint gives each process its own as `DB_DSN`. The container logs a warning, and it is not
the credential boundary: both DSNs are in the container, and Caddy runs as root.

Whichever layout, the entrypoint arranges the following on PostgreSQL:

- Postgres mode is decided from the container's environment only (`DB_DRIVER`, `DB_DSN*`). A
  `config.json` that selects PostgreSQL while the environment does not is refused, as is a
  `DB_DRIVER=sqlite` combined with a DSN.
- The server runs as its own account, `corescope-server`, with the uid in `SERVER_RUN_AS_UID`
  (default `1001`; set it in the container's environment, not in `.env`). It must not be 0 (or
  the ingestor's uid in the single-container layout). Its environment is an allow-list
  (`docker/server-exec.sh`), so `PG*`, `POSTGRES_*` and other secrets do not reach it. A new
  environment variable the server must read has to be added to that list.
- Everything the server writes lives in **`/app/data/shared`**: `users.db` (with its `-wal`/`-shm`
  and `backups/`), `prune-requests/` and `mbcap-snapshot/`. The directory is group
  `corescope-share` (gid `CORESCOPE_SHARE_GID`, default 61001, above the range of ordinary
  users) and sticky. The ingestor's `DB_PATH` points into it (on PostgreSQL `DB_PATH` only
  anchors these shared paths). A `users.db` left in `/app/data` by an earlier SQLite run is moved
  there once, with its backups.
- If you set `userManagement.dbPath`, it must be under `/app/data/shared`; the entrypoint warns
  otherwise, because the server can write only there.
- `config.json` must not contain a `dsn`: the container refuses to start. It is made group
  `corescope-share`, mode 0660, so the geo-filter editor can save into it and the ingestor can
  read it; `/app/data` itself is not opened up. It must be readable by the server user.
- `/app/data/.env` must **not** be readable by the server user (`chmod 600`); the container
  refuses to start otherwise.
- The empty-database guard (below) still checks your original SQLite file, through
  `CORESCOPE_SQLITE_GUARD_PATH` (`/app/data/meshcore.db` in the override; the entrypoint sets it
  from your `DB_PATH` in the single-container layout).
- Caddy's admin API is off in the image's Caddyfiles. A custom Caddyfile without a global
  `admin off` block gets a root-only admin socket and a warning at start; add
  `{ admin off }` at its top.

SQLite mode is unchanged: one account for both services and the database where it always was.

### Kubernetes

The same rules apply in a cluster. A layout that has been run in production:

- **Ingestor:** its own Deployment, `replicas: 1` and `strategy: Recreate`. There is exactly one
  writer: the servers follow new rows by "id greater than last seen", and a second ingestor also
  fights the first over the same MQTT client id. Never scale it.
- **Server:** a separate Deployment, which can scale horizontally (verified with five read-only
  replicas). Separate containers and pods are the credential boundary: only the ingestor has the
  writer DSN (from a Secret), the server has the reader DSN.
- **Users database:** if user management is enabled, `users.db` is SQLite on the shared volume
  and several server replicas must not share it. Run one server replica in that case.
- **Network:** keep PostgreSQL on a `ClusterIP` service only, with a `NetworkPolicy` that admits
  just the CoreScope pods (and the copy Job below).
- **Roles:** run `scripts/postgres-roles.sh` inside the PostgreSQL pod over its local socket
  (`kubectl exec`), taking the passwords from the pod's environment, so they never go on a
  command line.
- **Moving data:** run `corescope-migrate copy` as a one-shot Job, with the ingestor scaled to
  zero first and `PGPASSWORD` from a Secret (password-less URL).
- **First connection:** a new pod's first connection may be refused for a second or two until
  the NetworkPolicy controller admits it. The binaries are being changed to retry the first
  connect for up to 30 seconds; until you run a version that does, let the pod restart.

## Moving an existing SQLite install

`corescope-migrate copy` streams every row of a SQLite database into an empty PostgreSQL
database and verifies the result. It never deletes or overwrites anything.

1. **Provision PostgreSQL** with the roles script (or the Compose override's `postgres` and
   `postgres-init` services started on their own).

2. **Stop every writer.** The copy reads one snapshot; rows written after it are not copied.
   The tool prints a warning about this. Stop the ingestor, and the server too for a clean
   cut-over. With the Compose override that is `docker compose -f docker-compose.yml -f
   docker-compose.postgres.yml stop ingestor prod` (leave `postgres` running). On a first move
   they have not been started yet; bring up only the database with `up -d postgres
   postgres-init`.

3. **Copy**, connecting as the writer role:

   ```bash
   export PGPASSWORD='<writer-password>'   # or use ~/.pgpass
   corescope-migrate copy \
     -from sqlite:/app/data/meshcore.db \
     -to 'postgres://corescope_writer@db.example.org:5432/corescope?sslmode=require'
   ```

   **In the Docker image**, `corescope-migrate` is at `/app/corescope-migrate`. Run it in a
   one-off container from the same image, with the data directory mounted and the Compose
   network, with the writers stopped and the `postgres` and `postgres-init` services up:

   ```bash
   export PGPASSWORD='<writer-password>'     # the value of CORESCOPE_WRITER_PASSWORD
   docker compose -f docker-compose.yml -f docker-compose.postgres.yml \
     run --rm --no-deps -e PGPASSWORD --entrypoint /app/corescope-migrate prod \
     copy -from sqlite:/app/data/meshcore.db \
          -to 'postgres://corescope_writer@postgres:5432/corescope?sslmode=disable'
   ```

   (`-e PGPASSWORD` with no value passes the variable from your shell, so the password is not on
   the command line. Use the database name and role from your `.env` if you changed them. Run
   `verify` the same way.) Then start the stack with `up -d`.

   Do not put the password in the URL: it would show up in `ps` and shell history. Credentials
   come from the standard PostgreSQL environment (`PGPASSWORD`, ...) or `~/.pgpass`. Passwords
   are redacted in the tool's output. Flags:

   | Flag | Default | Meaning |
   |------|---------|---------|
   | `-from` | — | Source: `sqlite:<path>` or a `postgres://` / `postgresql://` URL. Required. |
   | `-to` | — | Target, same forms. Required. |
   | `-batch` | `5000` | Rows per `COPY` call (PostgreSQL target) or per transaction (SQLite target) |
   | `-allow-pending-migrations` | off | Copy a SQLite source whose async migrations are unfinished (see below) |

   What it does:

   - The source must be fully migrated. A SQLite source must pass the schema-ready check and
     every row in `_async_migrations` must be `done`. If not, start the ingestor once on the
     source and let its migrations finish.
   - The target must be empty. The schema is created if absent. A non-empty target is refused.
   - Into PostgreSQL the copy is one transaction, verified before `COMMIT`. A failure leaves
     the target empty; rerun it.
   - Primary keys are preserved and sequences are set afterwards.
   - The `observation_hops` tables (the SQLite-only hop index) are skipped.
   - Text in a SQLite `INTEGER` column that cannot be converted fails the copy and names the
     table, primary key and column.

   Measured throughput was about 36.7k rows/s into PostgreSQL, roughly 9 minutes per 20
   million observations.

   **`-allow-pending-migrations` (read this before using it).** Use it only when the source cannot finish its async
   migrations by running the ingestor, for example a file prepared with the legacy
   `corescope-migrate -db` command, which never creates `_async_migrations`. The data is copied
   as the source has it, with a warning. PostgreSQL will **not** run those backfills, because
   its schema records them as done, and unfinished `_async_migrations` rows are not copied. For
   a normal install, do not use it: start the ingestor on the source instead. Using it can leave
   you with a PostgreSQL database that is missing the results of migrations the source never
   finished, and CoreScope will not repair that on its own.

4. **Verify**:

   ```bash
   corescope-migrate verify \
     -a sqlite:/app/data/meshcore.db \
     -b 'postgres://corescope_writer@db.example.org:5432/corescope?sslmode=require'
   ```

   It compares per-table row counts and an ordered content digest, prints the first differing
   primary key, and exits 1 on a mismatch. (`copy` already verifies before it commits; this is
   for a second look, or after a later change.)

5. **Switch the configuration** to PostgreSQL as in [Set up with Docker Compose](#set-up-with-docker-compose)
   or [Set up without Compose](#set-up-without-compose).

6. **Start the ingestor, then the server.** With the Compose override, `up -d` starts both.

Keep the old `meshcore.db` file until you are satisfied. The ingestor has a guard for the case
where you forget the copy: on PostgreSQL it refuses to start on an **empty** PostgreSQL database
while the SQLite file at `dbPath` still holds transmissions:

```
refusing to start on an empty PostgreSQL database while <path> still holds N transmissions:
copy it first with "corescope-migrate copy -from sqlite:<path> -to <postgres url>", or move
<path> aside to start with an empty database
```

There is no configuration switch. Either copy the data, or move the SQLite file aside, which
is the deliberate way to start with an empty PostgreSQL database.

The legacy form `corescope-migrate -db <sqlite path>` is unchanged: it brings a SQLite file to
the current schema and is used by CI.

## Switching back to SQLite

Copy PostgreSQL into a new SQLite file the same way, then set the driver back.

1. Stop the ingestor (and the server).
2. Copy to a path that does not exist yet:

   ```bash
   corescope-migrate copy \
     -from 'postgres://corescope_writer@db.example.org:5432/corescope?sslmode=require' \
     -to sqlite:/app/data/meshcore.db
   ```

   Set `PGPASSWORD` (or use `~/.pgpass`) first; keep the password out of the URL.

   The tool builds `<path>.partial` and renames it after verification (about 61k rows/s
   measured). It refuses a target file that already exists, and on a failure it prints the exact
   `rm` command to clear the partial file. Move any old `meshcore.db` (and its `-wal` and `-shm`)
   aside first.

   In the Docker image, run it the same way as in
   [Moving an existing SQLite install](#moving-an-existing-sqlite-install) (`run --rm --no-deps
   --entrypoint /app/corescope-migrate prod copy -from ... -to sqlite:/app/data/meshcore.db`)
   while the PostgreSQL service is up and `ingestor` and `prod` are stopped.

3. Remove `db.driver`, `db.dsn`, `DB_DRIVER`, `DB_DSN`, `DB_DSN_INGESTOR` and `DB_DSN_SERVER`
   (or set the driver to `sqlite`), and drop `docker-compose.postgres.yml` from the `-f` list.
4. Start the ingestor, then the server.

## Backups and restore

`GET /api/backup` is a SQLite feature. On PostgreSQL it returns HTTP 501:

```json
{"error":"backups of a PostgreSQL database are taken with pg_dump; see docs/postgresql.md"}
```

### Back up

Use `pg_dump` in custom format, as the writer role or a superuser. Supply the password through
`~/.pgpass` or an exported `PGPASSWORD`, not on the command line:

```bash
pg_dump -Fc \
  -h db.example.org -U corescope_writer -d corescope \
  -f corescope-$(date -u +%Y%m%dT%H%M%SZ).dump
```

With the Compose override, run it inside the container, or publish the port as described
[above](#set-up-with-docker-compose):

```bash
docker compose -f docker-compose.yml -f docker-compose.postgres.yml exec -T postgres \
  pg_dump -Fc -U postgres -d corescope > corescope.dump
```

Keep copies off the host, and test restores. The dump holds everything in the measurement
database, so protect it like the database itself.

A portable alternative is a SQLite snapshot made with `corescope-migrate copy` from PostgreSQL
to a new SQLite file (see [Switching back to SQLite](#switching-back-to-sqlite)).

### Restore

1. Stop the ingestor and the server.
2. Restore into an **empty** database:

   ```bash
   createdb -h db.example.org -U postgres -O corescope_writer corescope
   pg_restore -h db.example.org -U postgres -d corescope --no-owner \
     --role=corescope_writer corescope.dump
   ```

   This assumes the old `corescope` database was dropped or renamed first. Use a superuser as
   shown, or the writer role.

3. Run the roles script again, so the reader gets `SELECT` on the restored tables and the
   default privileges that cover future ones:

   ```bash
   sh scripts/postgres-roles.sh
   ```

   Use the same environment (names and passwords) as at setup. The script refuses an existing
   database that the writer does not own, and an existing role it did not set up, unless
   `CORESCOPE_ADOPT=1` is set. A database created with `-O corescope_writer` as above passes.

4. Start the ingestor, then the server. The ingestor applies any newer migrations.

### Other files to back up

- `config.json`, `theme.json`, deployment settings and secrets, as in
  [Backup & Restore](deployment.md#backup--restore).
- `users.db`, if user accounts are enabled. It is **still SQLite on PostgreSQL installs** and is
  backed up as before, through `/api/admin/users-backup` or its scheduled snapshots; see the
  [account backup guide](user-guide/accounts.md#backups). A `pg_dump` does not contain it. In
  the Docker image on PostgreSQL it is `/app/data/shared/users.db`, with snapshots in
  `/app/data/shared/backups/`.
- `config.json` is rewritten by geo-filter saves. A save is normally atomic. When the config
  directory is not writable (the Docker image on PostgreSQL), the server first copies the
  current file to `config.json.bak` in the shared directory (`/app/data/shared`) and then
  rewrites `config.json` in place, which is not atomic and is logged as such. After a failed
  save, restore from that `.bak`.

## What is SQLite-only

These do not apply on PostgreSQL:

| Feature | On PostgreSQL |
|---------|---------------|
| `retention.hopIndexDays` (the hop index) | Ignored. The ingestor logs one warning and treats it as 0. The node reach panel uses its path scan. |
| `db.vacuumOnStartup`, `db.incrementalVacuumPages`, `db.analysisLimit` | Ignored |
| WAL checkpoints, incremental vacuum, planner-statistics refresh | Do not run. The ingestor logs once that maintenance is handled by PostgreSQL autovacuum. |
| `GET /api/backup` | 501, use `pg_dump` |
| `GET /api/perf/sqlite` | 404 `{"error":"not available: the database is PostgreSQL"}` |
| `users.db` | Stays SQLite |

Retention settings (`retention.*Days`) work the same on both backends. They run in the ingestor.

## Monitoring

The **Perf** page shows "Database (PostgreSQL)" instead of "Database (SQLite)", hides the WAL
and freelist rows, and labels row counts as estimates.

`GET /api/perf` carries the same data. Its `sqlite` block (the name is kept for compatibility)
has `"engine": "sqlite"` or `"engine": "postgres"`. On PostgreSQL:

- `dbSizeMB` is `pg_database_size`,
- `rows` are planner estimates (`pg_stat_user_tables.n_live_tup`), not exact counts,
- `walSizeMB`, `freelistMB` and `walPages` are 0.

The result is cached for 30 seconds.

Table bloat, autovacuum, replication and connection limits are PostgreSQL's to report; monitor
them with the tools you already use for PostgreSQL.

`corescope-decrypt -db` also accepts a `postgres://` URL, opened read-only.

## Troubleshooting

Startup refusals are deliberate. Messages below are the text CoreScope prints; passwords are
never included.

### Server

**`postgres role "<role>" can write to tables in schema "<schema>" (...); the server must connect as a read-only role.`**
The server's DSN uses a role with write access (the writer, a superuser, an owner, or a role
that inherits write grants). Use the reader role in the server's DSN. The message includes the
`CREATE ROLE` and `GRANT` statements for a read-only role; `scripts/postgres-roles.sh` does the
same. If you gave the reader a write privilege later, re-run the script, which revokes it.

**`database schema is at version N, need M: start the ingestor first so it can create or upgrade the schema`**
The schema is missing or older than this server. Start the ingestor (it creates and migrates
the schema), then the server.

**`default_transaction_read_only is "off" after connecting, want "on"; remove any override of it from db.dsn and from the role or database settings`**
A setting overrides the read-only default. Remove any `default_transaction_read_only` override
from `db.dsn` and from the role and database settings (`ALTER ROLE ... SET`, `ALTER DATABASE ... SET`).

**`postgres role "<role>" has no current schema; set search_path in db.dsn to the CoreScope schema`**
The role's `search_path` resolves to no existing schema. Set `search_path` in the DSN, or use
the default `public`.

**Queries fail with `canceling statement due to statement timeout`**
On PostgreSQL the server's sessions have a `statement_timeout` of 120 seconds by default. Raise
`db.statementTimeoutSec` in `config.json` for a slow query on a very large database, or set it to
`0` to disable the limit. Prefer fixing the cause (missing `ANALYZE`, an undersized server) over
disabling it.

**`db.dsn: unparseable postgres URL`**, **`db.dsn: malformed keyword=value connection string (expected keyword=value)`**, **`db.dsn: unterminated quoted value in connection string`**
The DSN is not valid. Use a `postgres://user:pass@host:5432/db?sslmode=...` URL and
percent-encode special characters in the password.

### Ingestor

**`refusing to start on an empty PostgreSQL database while <path> still holds N transmissions: ...`**
See [Moving an existing SQLite install](#moving-an-existing-sqlite-install): copy the SQLite
file first, or move it aside to start empty.

**`refusing to start on an empty PostgreSQL database: cannot tell whether <path> still holds data (...); move <path> aside to start with an empty database`**
The guard could not read the SQLite file at `dbPath`. Fix the cause shown in the parentheses
(permissions, a corrupt file), or move the file aside.

**`opening postgres: db.dsn is not a valid postgres:// URL`**
Fix the DSN as above.

**`pgschema: PostgreSQL N is not supported; CoreScope needs PostgreSQL 16 or newer`**
Upgrade the PostgreSQL server.

**`pgschema: database schema is at version N but this binary only knows up to M; a newer CoreScope has upgraded it, run that version`**
A newer CoreScope migrated this database. Run that version or newer; migrations are
forward-only.

**`applying postgres schema: ...` or permission denied when creating tables**
The ingestor's role cannot create tables. It must own the database and schema; use the writer
role from `scripts/postgres-roles.sh`.

### Configuration

**`db.driver "<value>" is not recognised; use "sqlite" (or sqlite3) or "postgres" (or postgresql, pg)`**
Fix the driver name.

**`db.driver=postgres requires db.dsn (or DB_DSN)`**
Set a DSN. In the Docker image, set `DB_DSN` (the ingestor container) and `DB_DSN_SERVER` (the
server container); in the single-container layout, `DB_DSN_INGESTOR` and `DB_DSN_SERVER`.

**`[entrypoint] DB_DSN / DB_DSN_INGESTOR / DB_DSN_SERVER must not contain a double quote, backslash or newline`**
Use a `postgres://` URL and percent-encode the password.

**Docker image refuses to start on PostgreSQL**
The entrypoint logs `[entrypoint] postgres: ...` and exits. Typical causes and fixes:
`config.json sets db.dsn` (remove it; pass the DSNs in the containers' environment);
`config.json selects PostgreSQL (db.driver), but the environment does not` (set `DB_DRIVER=postgres`
and the DSN on the container, and remove `db.driver` from `config.json`);
`DISABLE_INGESTOR=true but DB_DSN_INGESTOR is set` (the writer DSN must not be given to the
server container); `DB_DRIVER=... but a DB_DSN* is set` (unset one of them); `/app/data/.env is readable by corescope-server` (`chmod 600` it);
`config.json is not readable by corescope-server` (make it readable by others or by group
`corescope-share`); `SERVER_RUN_AS_UID` is not a number, is 0, equals the ingestor's uid, or
its uid or gid belongs to another account or group (choose another value);
`corescope-server cannot reach /app/data/shared` (`/app/data` must be searchable by others);
`/app/config.json must be a regular file in /app/data` (not a symlink or a file elsewhere).
`WARNING: userManagement.dbPath ... is outside /app/data/shared` is not fatal: move `users.db`
into the shared directory and point `dbPath` at it.

**A geo-filter save is logged as non-atomic**
Expected when `config.json`'s directory is not writable by the server (the Docker image on
PostgreSQL): it backed the file up to `config.json.bak` in `/app/data/shared` and rewrote it in
place. If the save failed midway, copy the `.bak` back.

**The ingestor container is up but sees no MQTT broker, or stopped working after `prod` restarted (Compose).**
The ingestor shares `prod`'s network namespace. Run `docker compose -f docker-compose.yml -f
docker-compose.postgres.yml up -d` so both are recreated together.

**The server connects with the wrong DSN.**
Remove `db.driver` and `db.dsn` from `config.json`; the server prefers `config.json` over the
environment, and on PostgreSQL in the image `config.json` may not select the database.

**The container warns "the ingestor runs in this container, so its writer DSN is in this container too."**
You are on the single-container layout. Use the Compose override, or run the ingestor as its own
container and set `DISABLE_INGESTOR=true` on the server's.

**The container warns that `/etc/caddy/Caddyfile` does not disable Caddy's admin API.**
Add a global block `{ admin off }` at the top of your custom Caddyfile.

### `corescope-migrate`

**`target is not empty (table <name> has rows); refusing to overwrite. ...`**
`copy` only writes to an empty target. Use a new PostgreSQL database, or a SQLite path that does
not exist yet. Nothing is dropped for you.

**`source <db> ... ; start the ingestor once on the source so its migrations finish, then rerun, or pass -allow-pending-migrations to copy it as it is`**
The SQLite source has unfinished async migrations or no `_async_migrations` table. Start the
ingestor on it and let the migrations finish. Use `-allow-pending-migrations` only as described
[above](#moving-an-existing-sqlite-install). A PostgreSQL source is always strict.

**`target SQLite file <path> already exists; refusing to overwrite. ...`**, **`<path>-wal exists (left over from an earlier database at this path); ...`**
Give a path that does not exist yet, or remove the leftover file.

**A failed copy into SQLite leaves `<path>.partial`.**
The tool prints the `rm` command. Run it and rerun the copy. A failed copy into PostgreSQL leaves
the target empty.

### `scripts/postgres-roles.sh`

**`PG_ADMIN_URL must not contain a password (it would be visible in ps); set PGPASSWORD or use ~/.pgpass`**
Remove the password from the URL and set `PGPASSWORD` or use `~/.pgpass`.

**`role <name> exists but was not set up by this script as the CoreScope writer|reader; set CORESCOPE_ADOPT=1 to take it over (its password is reset)`**, **`database <db> exists and is owned by <owner>, not <writer>; set CORESCOPE_ADOPT=1 to take it over`**
The script marks the roles it sets up and will not touch another application's role or database.
If the existing role or database really is meant for CoreScope, rerun with `CORESCOPE_ADOPT=1`
(the role's password is reset). It never touches a superuser, even then.

**`PG_ADMIN_URL must connect as a superuser (connected as <role>)`**, **`role <name> is a superuser; choose another name for CoreScope`**
Connect as a superuser, and pick role names that are not existing superusers; the script will not
demote an account.

**`... must match ^[a-z_][a-z0-9_]{0,62}$ and not start with pg_`**
Choose a valid role or database name.

**`psql not found`**, or an error near `\getenv`
Install `psql` 15 or newer.
