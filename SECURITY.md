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
- **Separate DSNs in Docker.** The image gives the ingestor `DB_DSN_INGESTOR`
  and the server `DB_DSN_SERVER`, each as its own `DB_DSN`, so the server
  process never sees the writer credentials. In the Docker image on
  PostgreSQL the server also runs as a separate unprivileged user from the
  ingestor (`SERVER_RUN_AS_UID`, default 1001, never root or the ingestor's uid; the entrypoint refuses otherwise), so it cannot read the writer DSN from the ingestor's process environment. In that mode `config.json` must not carry a `dsn`, and the server writes only under `/app/data/shared`.
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

`users.db` (user accounts) is always SQLite and is not covered by these roles;
protect its file and its backups as described in the user-management docs.
