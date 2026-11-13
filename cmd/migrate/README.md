# corescope-migrate

Operator tool that brings a CoreScope database to the current schema and moves
an installation between SQLite and PostgreSQL. Built with the other binaries
(`make build` puts `corescope-migrate` in `dist/`). The Docker image ships it too, at `/app/corescope-migrate`.

Three modes:

| Mode | Command | Use |
|------|---------|-----|
| Legacy | `corescope-migrate -db <sqlite path>` | Bring a SQLite file to the current schema |
| Copy | `corescope-migrate copy -from <db> -to <db>` | Copy a whole database between backends, verified |
| Verify | `corescope-migrate verify -a <db> -b <db>` | Compare two databases |

## `<db>` forms

- `sqlite:<path>` for a SQLite file, e.g. `sqlite:/data/meshcore.db`
- `postgres://…` or `postgresql://…` for PostgreSQL

Credentials come from the URL or the standard PostgreSQL environment
(`PGPASSWORD`, `PGSSLMODE`, ...). Prefer a password-less URL plus `PGPASSWORD` or
`~/.pgpass`: a password inside a URL on the command line shows in `ps` and your
shell history. Passwords are redacted in all output.

## Legacy mode

```bash
corescope-migrate -db data/meshcore.db
```

Applies the schema migrations to one SQLite file and exits. CI uses it to
prepare a fixture without starting the ingestor. In production the ingestor
migrates its own database at start, so you rarely need this.

## copy

```bash
corescope-migrate copy -from <db> -to <db> [-batch 5000] [-allow-pending-migrations]
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-from` | required | Source database. Must be fully migrated |
| `-to` | required | Target database. Must be empty |
| `-batch` | 5000 | Rows per COPY call (PostgreSQL target) or per transaction (SQLite target) |
| `-allow-pending-migrations` | off | Accept a SQLite source whose `_async_migrations` table is missing or unfinished |

Safety behaviour:

- **Stop the ingestor first.** The copy reads one snapshot; rows written after
  it are not copied. The tool prints a warning.
- **The source must be fully migrated.** A SQLite source must pass the schema
  readiness check and have every `_async_migrations` row done. If not, start the
  ingestor once on the source and let it finish.
- **`-allow-pending-migrations` skips that check and copies the data as it is.**
  A table the source lacks is created empty in the target, and a nullable column
  it lacks is copied as NULL (the tool warns about each). A missing NOT NULL
  column is never papered over: the copy fails and tells you to start the
  ingestor once on the source. PostgreSQL will not run the skipped backfills
  afterwards. Use the flag only for a source you know does not need them, such
  as a file prepared with `-db`.
- **The target schema is created if absent. A non-empty target is refused.**
  The tool never drops or deletes anything. A PostgreSQL schema that holds
  tables but no `schema_version` is not a CoreScope database and is refused
  before anything is created. A SQLite target path that already exists, even an
  empty file or a symlink, is always refused, as are leftover `-wal`, `-shm` or
  `.partial` files next to it: give a path that does not exist yet.
- **Into PostgreSQL:** one transaction using COPY, verified before COMMIT. A
  failure leaves the target empty; fix the cause and rerun.
- **Into SQLite:** builds `<path>.partial`, verifies it, then publishes it as
  `<path>` with a hard link and removes the `.partial` name. The link fails
  rather than replace anything, so an existing target is never overwritten. On
  failure the `.partial` file stays (it is not a usable database) and the tool
  prints the exact `rm` command.
- Every primary key is preserved and sequences are set afterwards. A value in a
  SQLite INTEGER column that cannot be converted fails the copy, naming the
  table, primary key and column.
- The `observation_hops` tables (an opt-in SQLite-only index) are not copied;
  the ingestor rebuilds them.

Throughput measured on one install: SQLite to PostgreSQL about 36.7k rows/s
(roughly 9 minutes per 20 million observations); PostgreSQL to SQLite about 61k
rows/s.

## verify

```bash
corescope-migrate verify -a <db> -b <db>
```

Compares row counts and an ordered content digest per table. On a mismatch it
prints the first differing primary key and exits 1. `copy` runs the same check
itself; run `verify` again later to confirm a restored or reloaded database.

## Examples

SQLite to PostgreSQL (the target role must own the schema; see
[docs/postgresql.md](../../docs/postgresql.md)):

```bash
# 1. stop the ingestor
export PGPASSWORD=…            # the writer role's password, from your secret store
corescope-migrate copy \
  -from sqlite:/data/meshcore.db \
  -to   postgres://corescope_writer@db:5432/corescope?sslmode=require
corescope-migrate verify \
  -a sqlite:/data/meshcore.db \
  -b postgres://corescope_writer@db:5432/corescope?sslmode=require
# 2. set db.driver=postgres and db.dsn (or DB_DRIVER / DB_DSN), start ingestor, then server
```

Back to SQLite:

```bash
corescope-migrate copy \
  -from postgres://corescope_writer@db:5432/corescope?sslmode=require \
  -to   sqlite:/data/meshcore.db
```

then set the driver back to `sqlite`.

The usage text shows only password-less URLs.

Exit codes: 0 success, 1 failure or verify mismatch, 2 usage error.
