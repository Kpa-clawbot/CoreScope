#!/bin/sh
# scripts/postgres-roles.sh — provisions the two PostgreSQL roles CoreScope
# runs with, and the database they share. Used by CI, by
# docker-compose.postgres.yml and by the deployment docs; keep it the one
# place that knows what the roles need.
#
#   writer  the ingestor's role. Owns the database, so it can create the
#           schema's tables (pgschema.Apply, corescope-migrate copy) and every
#           table a later migration adds.
#   reader  the server's role. USAGE on schema public and SELECT on its
#           tables, now and — through default privileges on the writer's
#           future tables — after every migration. Nothing else: the server
#           refuses to start if its role may INSERT, UPDATE, DELETE or
#           TRUNCATE any table in its schema.
#
# Inputs (environment):
#   PG_ADMIN_URL               superuser connection (URL or conninfo), required
#   CORESCOPE_DB               database name            (default: corescope)
#   CORESCOPE_WRITER           writer role name         (default: corescope_writer)
#   CORESCOPE_WRITER_PASSWORD  writer password, required
#   CORESCOPE_READER           reader role name         (default: corescope_reader)
#   CORESCOPE_READER_PASSWORD  reader password, required
#
# Names must match ^[a-z_][a-z0-9_]{0,62}$ and must not start with pg_.
# Passwords never appear on a command line or in this script's output; they
# reach psql through its environment (\getenv, so psql 15 or newer). They are
# sent to the server inside ALTER ROLE, so a server with log_statement=ddl or
# =all records them in its log, as it would for any client.
#
# Idempotent: a second run creates nothing, resets both passwords to the
# given values, and re-applies every grant (and revokes any write privilege
# the reader has since been given). It never drops anything.
#
# Output (stdout): the two connection URLs, without passwords:
#   CORESCOPE_WRITER_URL=postgres://<writer>@<host>:<port>/<db>
#   CORESCOPE_READER_URL=postgres://<reader>@<host>:<port>/<db>
# Host and port are the ones the admin connection reached; when the
# application connects through another address, replace them.

set -eu
LC_ALL=C
export LC_ALL

die() {
  printf '%s\n' "postgres-roles: $*" >&2
  exit 1
}

# valid_name <what> <value>: the identifier rule above, without grep (a value
# holding a newline would match line by line).
valid_name() {
  case "$2" in
    '' | *[!a-z0-9_]* | [0-9]* | pg_*) die "$1 \"$2\" must match ^[a-z_][a-z0-9_]{0,62}\$ and not start with pg_" ;;
  esac
  [ "${#2}" -le 63 ] || die "$1 \"$2\" is longer than 63 characters"
}

: "${PG_ADMIN_URL:?PG_ADMIN_URL (a superuser connection URL) is required}"
CORESCOPE_DB=${CORESCOPE_DB:-corescope}
CORESCOPE_WRITER=${CORESCOPE_WRITER:-corescope_writer}
CORESCOPE_READER=${CORESCOPE_READER:-corescope_reader}

valid_name CORESCOPE_DB "$CORESCOPE_DB"
valid_name CORESCOPE_WRITER "$CORESCOPE_WRITER"
valid_name CORESCOPE_READER "$CORESCOPE_READER"
[ "$CORESCOPE_WRITER" != "$CORESCOPE_READER" ] ||
  die "CORESCOPE_WRITER and CORESCOPE_READER must be different roles"
[ -n "${CORESCOPE_WRITER_PASSWORD:-}" ] || die "CORESCOPE_WRITER_PASSWORD is required"
[ -n "${CORESCOPE_READER_PASSWORD:-}" ] || die "CORESCOPE_READER_PASSWORD is required"
export CORESCOPE_WRITER_PASSWORD CORESCOPE_READER_PASSWORD

command -v psql >/dev/null 2>&1 || die "psql not found"

# -X: no ~/.psqlrc. VERBOSITY terse: an error reports its position, not the
# statement text, so a failed ALTER ROLE cannot print a password.
psql -X -q -At -v ON_ERROR_STOP=1 \
  -v db="$CORESCOPE_DB" -v writer="$CORESCOPE_WRITER" -v reader="$CORESCOPE_READER" \
  "$PG_ADMIN_URL" <<'SQL'
\set VERBOSITY terse
\getenv writer_pw CORESCOPE_WRITER_PASSWORD
\getenv reader_pw CORESCOPE_READER_PASSWORD

SELECT set_config('corescope.writer', :'writer', false),
       set_config('corescope.reader', :'reader', false) \g /dev/null

-- Refuse rather than demote: an existing superuser named here is most likely
-- the operator's own account, and as the reader it would defeat the
-- read-only check anyway.
DO $$
DECLARE r text;
BEGIN
  IF NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
    RAISE EXCEPTION 'PG_ADMIN_URL must connect as a superuser (connected as %)', current_user;
  END IF;
  SELECT rolname INTO r FROM pg_roles
   WHERE rolname IN (current_setting('corescope.writer'), current_setting('corescope.reader'))
     AND rolsuper LIMIT 1;
  IF r IS NOT NULL THEN
    RAISE EXCEPTION 'role % is a superuser; choose another name for CoreScope', r;
  END IF;
END $$;

SELECT format('CREATE ROLE %I', :'writer')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'writer') \gexec
SELECT format('CREATE ROLE %I', :'reader')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'reader') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
              :'writer', :'writer_pw') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
              :'reader', :'reader_pw') \gexec
-- The server sets this itself; the role default covers any other client.
SELECT format('ALTER ROLE %I SET default_transaction_read_only = on', :'reader') \gexec

SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'writer')
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'db') \gexec
SELECT format('ALTER DATABASE %I OWNER TO %I', :'db', :'writer') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I, %I', :'db', :'writer', :'reader') \gexec

\connect :db
SELECT set_config('corescope.writer', :'writer', false),
       set_config('corescope.reader', :'reader', false) \g /dev/null

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
-- Explicit, because before PostgreSQL 15 public is not owned by the database
-- owner and the writer's CREATE came only from PUBLIC.
SELECT format('GRANT USAGE, CREATE ON SCHEMA public TO %I', :'writer') \gexec
SELECT format('GRANT USAGE ON SCHEMA public TO %I', :'reader') \gexec
SELECT format('GRANT SELECT ON ALL TABLES IN SCHEMA public TO %I', :'reader') \gexec
SELECT format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA public FROM %I', :'reader') \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT SELECT ON TABLES TO %I',
              :'writer', :'reader') \gexec

-- Connection URLs, without passwords. HOST is a directory for a Unix socket;
-- a URL carries that percent-encoded.
SELECT replace(:'HOST', '/', '%2F') AS url_host \gset
SELECT format('CORESCOPE_WRITER_URL=postgres://%s@%s:%s/%s', :'writer', :'url_host', :'PORT', :'db')
UNION ALL
SELECT format('CORESCOPE_READER_URL=postgres://%s@%s:%s/%s', :'reader', :'url_host', :'PORT', :'db');
SQL
