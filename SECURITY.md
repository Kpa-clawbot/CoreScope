# Security policy

## Reporting a vulnerability

Please do not open a public issue, pull request or discussion for a security
problem. Email **security@fiten.eu** instead, with:

- the affected version or commit,
- what an attacker needs (unauthenticated, logged-in user, admin, an MQTT
  observer, a radio node in range),
- steps or a test that reproduces it.

You will get an acknowledgement within 7 days. Once a fix is merged and
released, the issue is described in the release notes. Tell us if you want to
be credited.

## Supported versions

Fixes go into `master` and ship in the next release. Only the latest release
receives security fixes.

## Scope

In scope: the Go server (`cmd/server/`), the MQTT ingestor (`cmd/ingestor/`),
the shared packages in `internal/`, and the frontend in `public/`.

Out of scope: the MeshCore firmware and radio protocol themselves (report those
to [MeshCore](https://github.com/meshcore-dev/MeshCore)), and issues that need
an attacker who already controls the operator's `config.json` or host.

## Database credentials and roles

CoreScope stores its measurement data in SQLite or, optionally, PostgreSQL
(see [docs/postgresql.md](docs/postgresql.md)). With PostgreSQL:

- **Least privilege.** The ingestor is the only writer and uses a role that
  owns the schema. The server uses a separate read-only role. The server
  forces `default_transaction_read_only=on` on every connection and refuses to
  start if its role can INSERT, UPDATE, DELETE or TRUNCATE any table in its
  schema, whether through ownership, superuser or an inherited grant. Do not
  give it the writer role to get past that check. `scripts/postgres-roles.sh`
  creates both roles. It refuses a `PG_ADMIN_URL` that contains a password
  (use `PGPASSWORD` or `~/.pgpass`) and refuses to adopt a database or role it
  did not create unless `CORESCOPE_ADOPT=1` is set.
- **A DSN is a secret.** It carries a password. Pass it through `DB_DSN`
  (or `DB_DSN_INGESTOR` / `DB_DSN_SERVER`) from the environment or an `.env`
  file that is not committed. Do not put `db.dsn` in a `config.json` that is
  checked into git or readable by other users. Never put a password inside a
  `postgres://` URL on a command line: it shows in `ps` and shell history. Use a
  password-less URL with `PGPASSWORD` or `~/.pgpass` for tools such as
  `corescope-migrate` and `psql`.
- **Statement timeout.** The server caps each PostgreSQL statement
  (`db.statementTimeoutSec`, default 120 seconds, `0` disables) because the
  public API is unauthenticated.
- **The boundary is a separate container.** In Docker on PostgreSQL the
  ingestor runs as its own container (`docker-compose.postgres.yml`), not root,
  with the writer DSN as its only database credential. The main container
  (caddy, mosquitto, server) starts with `DISABLE_INGESTOR=true`, has only the
  read-only DSN (`DB_DSN_SERVER`) and refuses to start if `DB_DSN_INGESTOR` is
  set. Processes inside one container share supervisord, and caddy runs as
  root, so running both services in a single container on PostgreSQL is not
  recommended; it still works and the entrypoint logs a warning. There the
  server runs as a separate unprivileged user (`SERVER_RUN_AS_UID`, default
  1001) so it cannot read the ingestor's environment.
- **Container configuration.** The container picks its database from its
  environment, never from `config.json`, and refuses a `config.json` that
  carries a `dsn`. Apart from `config.json` (the geo-filter editor saves into it), the server writes only under `/app/data/shared`.
- **Caddy admin API off.** The image Caddyfiles set `admin off`, so a local
  process can no longer reconfigure the reverse proxy through its admin
  endpoint. This applies to SQLite deployments too.
- **Redaction.** `corescope-migrate` and the other tools print connection
  URLs with the password removed. Do not rely on that for anything you paste
  elsewhere without checking it.
- **`log_statement`.** `scripts/postgres-roles.sh` keeps passwords off the
  command line and out of its output, but a PostgreSQL server with
  `log_statement` set to `ddl` or `all` writes the `ALTER ROLE ... PASSWORD`
  statement, password included, to its own log. Restrict that log, or set
  `log_statement` to `none` while provisioning.
- **Network.** The Compose override does not publish the PostgreSQL port to the
  host. Keep it that way, or bind it to `127.0.0.1`, and use `sslmode=require`
  (or stricter) for a database on another host.

`users.db` (user accounts) is a SQLite file by default and is not covered by these roles;
protect its file and its backups as described in the user-management docs.

With accounts in PostgreSQL (`userManagement.dsn` / `DB_DSN_ACCOUNTS`) they use a separate
role (`corescope_accounts`, created by `scripts/postgres-roles.sh` only when you set its
password). The script creates schema `corescope_users` owned by it; the server never creates
it, so the role needs and gets no `CREATE` on the database (a re-run revokes one granted since).
It has no privilege in the measurement schema or in `corescope_control`. Give its DSN to the
servers only: the ingestor never needs it, and the reader role must not be used for it. The
server checks this at startup and refuses to start if the accounts role is the reader, could
write measurement or control tables, or owns or can `CREATE` in the measurement database,
through any role it reaches. The account store itself refuses a role that is a superuser, has
`CREATEROLE` or `BYPASSRLS`, or owns or can `CREATE` in its database, through any membership:
with `CREATE` it could make a schema named after the writer role, first in the writer's
`search_path`, and plant tables the ingestor writes through. The ingestor reads two account
tables (approved channels, linked companions) read-only through the group role
`corescope_users_reader`, which holds `SELECT` on only the columns it reads
(`proposals (id, kind, subject, status, decided_at)`, `companion_links (pubkey)`), never notes,
user ids or names. The accounts schema holds password hashes and
mail addresses: restrict and encrypt `pg_dump -n corescope_users` output like the database.
