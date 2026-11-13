#!/bin/sh
# scripts/postgres-roles.sh — provisions the two PostgreSQL roles CoreScope
# runs with, the group role that lets the server write its requests, and the
# database they share. Used by CI, by
# docker-compose.postgres.yml and by the deployment docs; keep it the one
# place that knows what the roles need.
#
#   writer  the ingestor's role. Owns the database, so it can create the
#           schema's tables (pgschema.Apply, corescope-migrate copy) and every
#           table a later migration adds.
#   reader  the server's role. CONNECT on the database, USAGE on schema public
#           and SELECT on its tables, now and — through default privileges on
#           the writer's future tables — after every migration. Nothing else:
#           one role membership (corescope_control_client, below), no
#           TEMPORARY, at most 32 connections, and statement_timeout 120s /
#           idle_in_transaction_session_timeout 60s. The server refuses to
#           start if its role may write any table beyond what that group holds.
#   corescope_control_client
#           a fixed-name NOLOGIN group role, granted to the reader (INHERIT,
#           no SET, no ADMIN): USAGE on schema corescope_control, SELECT,
#           INSERT on its prune_requests and mbcap_snapshots tables and SELECT
#           on prune_results — the server's requests to the ingestor (pgschema
#           migration 0002). The ingestor grants those at every start
#           (pgschema.EnsureControlGrants) once this role exists; this script
#           grants the same when the tables already exist, so either may run
#           first. Shared by every CoreScope database on the cluster.
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
# CoreScope writer, reader or control client group. Without CORESCOPE_ADOPT=1 it refuses to touch an
# existing role that lacks its mark, or an existing database not owned by the
# writer, so it never resets another application's password or takes over its
# database. It never touches a superuser, even with CORESCOPE_ADOPT=1.
#
# Idempotent: a second run creates nothing, resets both passwords to the
# given values, and re-applies every grant and limit (revoking any write
# privilege or role membership the reader or the group role has since been
# given, keeping the reader's membership of the group). It never drops
# anything.
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

# url_decode <text>: %XX sequences decoded (anything else left as it is).
url_decode() {
  printf '%s\n' "$1" | awk '
    BEGIN { hex = "0123456789abcdef" }
    {
      out = ""; s = $0
      while ((i = index(s, "%")) > 0) {
        h = tolower(substr(s, i + 1, 2))
        a = index(hex, substr(h, 1, 1)); b = index(hex, substr(h, 2, 1))
        if (length(h) == 2 && a && b) { out = out substr(s, 1, i - 1) sprintf("%c", (a - 1) * 16 + b - 1); s = substr(s, i + 3) }
        else { out = out substr(s, 1, i); s = substr(s, i + 1) }
      }
      print out s
    }'
}

# admin_url_has_password <url-or-conninfo>: a password in the URL's userinfo,
# or a password parameter (URL query or conninfo keyword), checked on the
# text as given and percent-decoded (libpq decodes URL keys: %70assword=).
admin_url_has_password() {
  _admin_url_has_password "$1" || _admin_url_has_password "$(url_decode "$1")"
}

_admin_url_has_password() {
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
# Fixed: pgschema.EnsureControlGrants and the server know it by this name.
CONTROL_ROLE=corescope_control_client
for _r in "$CORESCOPE_WRITER" "$CORESCOPE_READER"; do
  [ "$_r" != "$CONTROL_ROLE" ] || die "$CONTROL_ROLE is reserved for the control group role"
done
[ -n "${CORESCOPE_WRITER_PASSWORD:-}" ] || die "CORESCOPE_WRITER_PASSWORD is required"
[ -n "${CORESCOPE_READER_PASSWORD:-}" ] || die "CORESCOPE_READER_PASSWORD is required"
export CORESCOPE_WRITER_PASSWORD CORESCOPE_READER_PASSWORD

command -v psql >/dev/null 2>&1 || die "psql not found"

# -X: no ~/.psqlrc. VERBOSITY terse: an error reports its position, not the
# statement text, so a failed ALTER ROLE cannot print a password.
psql -X -q -At -v ON_ERROR_STOP=1 \
  -v db="$CORESCOPE_DB" -v writer="$CORESCOPE_WRITER" -v reader="$CORESCOPE_READER" \
  -v adopt="$CORESCOPE_ADOPT" -v ctl="$CONTROL_ROLE" \
  "$PG_ADMIN_URL" <<'SQL'
\set VERBOSITY terse
\getenv writer_pw CORESCOPE_WRITER_PASSWORD
\getenv reader_pw CORESCOPE_READER_PASSWORD
\set writer_mark 'CoreScope writer role (scripts/postgres-roles.sh)'
\set reader_mark 'CoreScope reader role (scripts/postgres-roles.sh)'
\set ctl_mark 'CoreScope control client group role (scripts/postgres-roles.sh)'
-- pgschema.advisoryLockKey: the ingestor's migrations and control grants hold
-- it, so this script's grants on the control tables never race them.
\set lock_key 4859228302472343408

SELECT set_config('corescope.writer', :'writer', false),
       set_config('corescope.reader', :'reader', false),
       set_config('corescope.db', :'db', false),
       set_config('corescope.adopt', :'adopt', false),
       set_config('corescope.writer_mark', :'writer_mark', false),
       set_config('corescope.reader_mark', :'reader_mark', false),
       set_config('corescope.ctl', :'ctl', false),
       set_config('corescope.ctl_mark', :'ctl_mark', false) \g /dev/null

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
                   (r, 'reader', current_setting('corescope.reader_mark')),
                   (current_setting('corescope.ctl'), 'control client group',
                    current_setting('corescope.ctl_mark'))) AS v(name, kind, mark)
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
SELECT format('CREATE ROLE %I NOLOGIN', :'ctl')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'ctl') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'writer', :'writer_mark') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'reader', :'reader_mark') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'ctl', :'ctl_mark') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
              :'writer', :'writer_pw') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 32 PASSWORD %L',
              :'reader', :'reader_pw') \gexec
-- The server sets read-only itself; the role defaults cover any other client.
SELECT format('ALTER ROLE %I SET default_transaction_read_only = on', :'reader') \gexec
SELECT format('ALTER ROLE %I SET statement_timeout = %L', :'reader', '120s') \gexec
SELECT format('ALTER ROLE %I SET idle_in_transaction_session_timeout = %L', :'reader', '60s') \gexec
-- The group is only a privilege holder: nobody logs in as it.
SELECT format('ALTER ROLE %I WITH NOLOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD NULL',
              :'ctl') \gexec
-- One transaction, so the reader is never seen without its group membership.
BEGIN;
-- No inherited privileges beyond the control group's: drop every membership
-- the reader holds, predefined roles such as pg_write_all_data included,
-- except the control group membership this script grants (same grantor, so
-- the GRANT below resets its options in place), and every membership the
-- group itself holds.
SELECT format('REVOKE %I FROM %I GRANTED BY %I', g.rolname, u.rolname, pg_get_userbyid(m.grantor))
  FROM pg_auth_members m
  JOIN pg_roles g ON g.oid = m.roleid
  JOIN pg_roles u ON u.oid = m.member
 WHERE (u.rolname = :'reader'
        AND NOT (g.rolname = :'ctl' AND m.grantor = (SELECT oid FROM pg_roles WHERE rolname = current_user)))
    OR u.rolname = :'ctl' \gexec
-- Re-granting an existing membership only resets its options; hide the
-- "already been granted" notice that comes with it.
SET LOCAL client_min_messages = warning;
SELECT format('GRANT %I TO %I WITH ADMIN FALSE, INHERIT TRUE, SET FALSE GRANTED BY CURRENT_USER', :'ctl', :'reader') \gexec
COMMIT;

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
-- The reader's privileges are rebuilt from nothing on every run: whatever it
-- was given since (on tables, sequences, functions, or in the writer's
-- default privileges, schema-wide or global) goes, then SELECT on tables
-- comes back.
SELECT format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', :'reader') \gexec
SELECT format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I', :'reader') \gexec
SELECT format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM %I', :'reader') \gexec
SELECT format('REVOKE ALL ON SCHEMA public FROM %I', :'reader') \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I %s REVOKE ALL ON %s FROM %I',
              :'writer', s, o, :'reader')
  FROM (VALUES ('IN SCHEMA public'), ('')) AS sc(s),
       (VALUES ('TABLES'), ('SEQUENCES'), ('FUNCTIONS'), ('TYPES')) AS ob(o) \gexec
SELECT format('GRANT USAGE ON SCHEMA public TO %I', :'reader') \gexec
SELECT format('GRANT SELECT ON ALL TABLES IN SCHEMA public TO %I', :'reader') \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT SELECT ON TABLES TO %I',
              :'writer', :'reader') \gexec

-- The control tables exist once the ingestor has run (pgschema 0002). The
-- reader reaches them only through the group; the group gets exactly what
-- pgschema.EnsureControlGrants grants (keep the two in step;
-- pgschema.ControlGrantsOK checks the result). As superuser these GRANTs are
-- recorded as the owner's, the writer's, as the ingestor's are.
BEGIN;
SELECT pg_advisory_xact_lock(:lock_key) \g /dev/null
SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'corescope_control') AS has_control \gset
\if :has_control
SELECT format('REVOKE ALL ON SCHEMA corescope_control FROM PUBLIC, %I, %I', :'reader', :'ctl') \gexec
SELECT format('REVOKE ALL ON ALL TABLES IN SCHEMA corescope_control FROM PUBLIC, %I, %I', :'reader', :'ctl') \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA corescope_control REVOKE ALL ON %s FROM %I',
              :'writer', o, :'reader')
  FROM (VALUES ('TABLES'), ('SEQUENCES'), ('FUNCTIONS'), ('TYPES')) AS ob(o) \gexec
SELECT format('GRANT USAGE ON SCHEMA corescope_control TO %I', :'ctl') \gexec
SELECT format('GRANT %s ON TABLE corescope_control.%I TO %I', p, t, :'ctl')
  FROM (VALUES ('prune_requests', 'SELECT, INSERT'), ('mbcap_snapshots', 'SELECT, INSERT'),
               ('prune_results', 'SELECT')) AS g(t, p)
 WHERE to_regclass(format('corescope_control.%I', t)) IS NOT NULL \gexec
\endif
COMMIT;

-- Connection URLs, without passwords. HOST is a directory for a Unix socket;
-- a URL carries that percent-encoded.
SELECT replace(:'HOST', '/', '%2F') AS url_host \gset
SELECT format('CORESCOPE_WRITER_URL=postgres://%s@%s:%s/%s', :'writer', :'url_host', :'PORT', :'db')
UNION ALL
SELECT format('CORESCOPE_READER_URL=postgres://%s@%s:%s/%s', :'reader', :'url_host', :'PORT', :'db');
SQL
