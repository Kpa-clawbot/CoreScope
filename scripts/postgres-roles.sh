#!/bin/sh
# scripts/postgres-roles.sh — provisions the two PostgreSQL roles CoreScope
# runs with, and the database they share. Used by CI, by
# docker-compose.postgres.yml and by the deployment docs; keep it the one
# place that knows what the roles need.
#
#   writer  the ingestor's role. Owns the database, so it can create the
#           schema's tables (pgschema.Apply, corescope-migrate copy) and every
#           table a later migration adds.
#   reader  the server's role. CONNECT on the database, USAGE on schema public
#           and SELECT on its tables, now and — through default privileges on
#           the writer's future tables — after every migration. Nothing else:
#           no role memberships, no TEMPORARY, at most 32 connections, and
#           statement_timeout 120s / idle_in_transaction_session_timeout 60s.
#           The server refuses to start if its role may INSERT, UPDATE, DELETE
#           or TRUNCATE any table in its schema.
#
# Inputs (environment):
#   PG_ADMIN_URL               superuser connection (URL or conninfo), required.
#                              It must not carry a password (it would show in
#                              `ps`): use PGPASSWORD or ~/.pgpass for that.
#   CORESCOPE_DB               database name            (default: corescope)
#   CORESCOPE_WRITER           writer role name         (default: corescope_writer)
#   CORESCOPE_WRITER_PASSWORD  writer password, required
#   CORESCOPE_READER           reader role name         (default: corescope_reader)
#   CORESCOPE_READER_PASSWORD  reader password, required
#   CORESCOPE_ADOPT            set to 1 to take over an existing role or
#                              database this script did not set up (below)
#
# Names must match ^[a-z_][a-z0-9_]{0,62}$ and must not start with pg_.
# Passwords never appear on a command line or in this script's output; they
# reach psql through its environment (\getenv, so psql 15 or newer). They are
# sent to the server inside ALTER ROLE, so a server with log_statement=ddl or
# =all records them in its log, as it would for any client.
#
# Ownership: the script marks each role it sets up with a comment naming it the
# CoreScope writer or reader. Without CORESCOPE_ADOPT=1 it refuses to touch an
# existing role that lacks its mark, or an existing database not owned by the
# writer, so it never resets another application's password or takes over its
# database. It never touches a superuser, even with CORESCOPE_ADOPT=1.
#
# Idempotent: a second run creates nothing, resets both passwords to the
# given values, and re-applies every grant and limit (revoking any write
# privilege or role membership the reader has since been given). It never
# drops anything.
#
# Output (stdout): the two connection URLs, without passwords:
#   CORESCOPE_WRITER_URL=postgres://<writer>@<host>:<port>/<db>
#   CORESCOPE_READER_URL=postgres://<reader>@<host>:<port>/<db>
# Host and port are the ones the admin connection reached; when the
# application connects through another address, replace them. Give the
# password separately, e.g. PGPASSWORD for corescope-migrate copy.

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

# admin_url_has_password <url-or-conninfo>: a password in the URL's userinfo,
# or a password parameter (URL query or conninfo keyword).
admin_url_has_password() {
  case "$1" in
    *://*)
      _auth=${1#*://}
      _auth=${_auth%%/*}
      _auth=${_auth%%\?*}
      case "$_auth" in
        *@*) case "${_auth%@*}" in *:*) return 0 ;; esac ;;
      esac
      ;;
  esac
  printf '%s\n' "$1" | grep -Eiq '(^|[?&[:space:]])password[[:space:]]*='
}

: "${PG_ADMIN_URL:?PG_ADMIN_URL (a superuser connection URL) is required}"
if admin_url_has_password "$PG_ADMIN_URL"; then
  die "PG_ADMIN_URL must not contain a password (it would be visible in ps); set PGPASSWORD or use ~/.pgpass"
fi
CORESCOPE_DB=${CORESCOPE_DB:-corescope}
CORESCOPE_WRITER=${CORESCOPE_WRITER:-corescope_writer}
CORESCOPE_READER=${CORESCOPE_READER:-corescope_reader}
case "${CORESCOPE_ADOPT:-0}" in
  0 | '') CORESCOPE_ADOPT=0 ;;
  1) ;;
  *) die "CORESCOPE_ADOPT must be 1 or unset" ;;
esac

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
  -v adopt="$CORESCOPE_ADOPT" \
  "$PG_ADMIN_URL" <<'SQL'
\set VERBOSITY terse
\getenv writer_pw CORESCOPE_WRITER_PASSWORD
\getenv reader_pw CORESCOPE_READER_PASSWORD
\set writer_mark 'CoreScope writer role (scripts/postgres-roles.sh)'
\set reader_mark 'CoreScope reader role (scripts/postgres-roles.sh)'

SELECT set_config('corescope.writer', :'writer', false),
       set_config('corescope.reader', :'reader', false),
       set_config('corescope.db', :'db', false),
       set_config('corescope.adopt', :'adopt', false),
       set_config('corescope.writer_mark', :'writer_mark', false),
       set_config('corescope.reader_mark', :'reader_mark', false) \g /dev/null

-- Refuse rather than demote or take over: a superuser named here is most
-- likely the operator's own account, and a role or database this script did
-- not set up may belong to another application.
DO $$
DECLARE
  w text := current_setting('corescope.writer');
  r text := current_setting('corescope.reader');
  d text := current_setting('corescope.db');
  adopt boolean := current_setting('corescope.adopt') = '1';
  rec record;
  owner text;
BEGIN
  IF NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
    RAISE EXCEPTION 'PG_ADMIN_URL must connect as a superuser (connected as %)', current_user;
  END IF;
  FOR rec IN
    SELECT v.name, v.kind, v.mark, ro.rolsuper,
           coalesce(shobj_description(ro.oid, 'pg_authid'), '') AS comment
      FROM (VALUES (w, 'writer', current_setting('corescope.writer_mark')),
                   (r, 'reader', current_setting('corescope.reader_mark'))) AS v(name, kind, mark)
      JOIN pg_roles ro ON ro.rolname = v.name
  LOOP
    IF rec.rolsuper THEN
      RAISE EXCEPTION 'role % is a superuser; choose another name for CoreScope', rec.name;
    END IF;
    IF NOT adopt AND rec.comment <> rec.mark THEN
      RAISE EXCEPTION 'role % exists but was not set up by this script as the CoreScope %; set CORESCOPE_ADOPT=1 to take it over (its password is reset)', rec.name, rec.kind;
    END IF;
  END LOOP;
  SELECT pg_get_userbyid(datdba) INTO owner FROM pg_database WHERE datname = d;
  IF owner IS NOT NULL AND owner <> w AND NOT adopt THEN
    RAISE EXCEPTION 'database % exists and is owned by %, not %; set CORESCOPE_ADOPT=1 to take it over', d, owner, w;
  END IF;
END $$;

SELECT format('CREATE ROLE %I', :'writer')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'writer') \gexec
SELECT format('CREATE ROLE %I', :'reader')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'reader') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'writer', :'writer_mark') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'reader', :'reader_mark') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
              :'writer', :'writer_pw') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 32 PASSWORD %L',
              :'reader', :'reader_pw') \gexec
-- The server sets read-only itself; the role defaults cover any other client.
SELECT format('ALTER ROLE %I SET default_transaction_read_only = on', :'reader') \gexec
SELECT format('ALTER ROLE %I SET statement_timeout = %L', :'reader', '120s') \gexec
SELECT format('ALTER ROLE %I SET idle_in_transaction_session_timeout = %L', :'reader', '60s') \gexec
-- No inherited privileges: drop every membership the reader holds,
-- predefined roles such as pg_write_all_data included.
SELECT format('REVOKE %I FROM %I GRANTED BY %I', g.rolname, :'reader', pg_get_userbyid(m.grantor))
  FROM pg_auth_members m
  JOIN pg_roles g ON g.oid = m.roleid
  JOIN pg_roles u ON u.oid = m.member
 WHERE u.rolname = :'reader' \gexec

SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'writer')
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'db') \gexec
SELECT format('ALTER DATABASE %I OWNER TO %I', :'db', :'writer') \gexec
-- Only the two roles may connect; the reader gets no TEMPORARY (the server
-- uses no temporary tables), the writer keeps it as the owner.
SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', :'db') \gexec
SELECT format('REVOKE ALL ON DATABASE %I FROM %I', :'db', :'reader') \gexec
SELECT format('GRANT CONNECT, TEMPORARY ON DATABASE %I TO %I', :'db', :'writer') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'db', :'reader') \gexec

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
