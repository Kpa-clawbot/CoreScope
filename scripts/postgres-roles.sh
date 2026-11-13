#!/bin/sh
# scripts/postgres-roles.sh — provisions the PostgreSQL roles CoreScope runs
# with, the group roles that let the server write its requests and the
# ingestor read two account tables, the database they share and, optionally,
# the schema the user accounts live in. Used by CI, by
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
#           no SET, no ADMIN): USAGE on schema corescope_control, SELECT and
#           INSERT (id, payload) — never created_at, which the database clock
#           fills in — on its prune_requests and mbcap_snapshots tables and
#           SELECT on prune_results: the server's requests to the ingestor
#           (pgschema migrations 0002, 0003). Nothing else, and nothing in the
#           writer's default privileges, so a table created later grants it
#           nothing. The ingestor grants those at every start
#           (pgschema.EnsureControlGrants) once this role exists; this script
#           grants the same when the tables already exist, so either may run
#           first. Shared by every CoreScope database on the cluster.
#   corescope_users_reader
#           a fixed-name NOLOGIN group role, granted to the writer (INHERIT,
#           no SET, no ADMIN), never to the reader: the ingestor reads the
#           approved channel proposals and the linked companions through it.
#           The accounts store, owner of those tables, grants it USAGE on
#           schema corescope_users and SELECT on proposals and companion_links
#           when it opens; this script grants it nothing. Created on every
#           run, accounts or not, and shared by every CoreScope database on
#           the cluster.
#   accounts
#           optional, only with CORESCOPE_ACCOUNTS_PASSWORD: the role the
#           server's user accounts store connects as (DB_DSN_ACCOUNTS), apart
#           from its read-only measurement connection. Owns schema
#           corescope_users in the CoreScope database and creates its tables
#           there. CONNECT on the database and nothing in schema public or
#           corescope_control, no role memberships and no members, no
#           TEMPORARY, at most 32 connections, statement_timeout 120s /
#           idle_in_transaction_session_timeout 60s, not read-only.
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
#   CORESCOPE_ACCOUNTS         accounts role name       (default: corescope_accounts)
#   CORESCOPE_ACCOUNTS_PASSWORD accounts password; unset or empty: no accounts
#                              role or schema is set up (an existing one is
#                              left as it is)
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
# CoreScope writer, reader, accounts role, control client group or users
# reader group. Without CORESCOPE_ADOPT=1 it refuses to touch an existing role
# that lacks its mark, an existing database not owned by the writer, or an
# existing schema corescope_users not owned by the accounts role, so it never
# resets another application's password or takes over its database. With it,
# such a schema is given to the accounts role (the tables in it are not). It
# never touches a superuser, even with CORESCOPE_ADOPT=1.
#
# Idempotent: a second run creates nothing, resets the passwords to the
# given values, and re-applies every grant and limit (revoking any write
# privilege or role membership the reader, the accounts role or a group role
# has since been given, keeping the reader's membership of the control group
# and the writer's of the users reader group). It never drops anything.
#
# Output (stdout): the connection URLs, without passwords:
#   CORESCOPE_WRITER_URL=postgres://<writer>@<host>:<port>/<db>
#   CORESCOPE_READER_URL=postgres://<reader>@<host>:<port>/<db>
#   CORESCOPE_ACCOUNTS_URL=postgres://<accounts>@<host>:<port>/<db>  (with accounts)
# and, on stderr, a note when the accounts role is skipped.
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
CORESCOPE_ACCOUNTS=${CORESCOPE_ACCOUNTS:-corescope_accounts}
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
# Fixed: the accounts store grants it, the ingestor reads through it.
USERS_READER_ROLE=corescope_users_reader
for _r in "$CORESCOPE_WRITER" "$CORESCOPE_READER"; do
  [ "$_r" != "$CONTROL_ROLE" ] || die "$CONTROL_ROLE is reserved for the control group role"
  [ "$_r" != "$USERS_READER_ROLE" ] || die "$USERS_READER_ROLE is reserved for the users reader group role"
done
[ -n "${CORESCOPE_WRITER_PASSWORD:-}" ] || die "CORESCOPE_WRITER_PASSWORD is required"
[ -n "${CORESCOPE_READER_PASSWORD:-}" ] || die "CORESCOPE_READER_PASSWORD is required"
export CORESCOPE_WRITER_PASSWORD CORESCOPE_READER_PASSWORD
# The accounts role is optional; '' below means "not set up on this run".
if [ -n "${CORESCOPE_ACCOUNTS_PASSWORD:-}" ]; then
  valid_name CORESCOPE_ACCOUNTS "$CORESCOPE_ACCOUNTS"
  for _r in "$CORESCOPE_WRITER" "$CORESCOPE_READER" "$CONTROL_ROLE" "$USERS_READER_ROLE"; do
    [ "$CORESCOPE_ACCOUNTS" != "$_r" ] || die "CORESCOPE_ACCOUNTS must differ from $_r"
  done
  ACCOUNTS_ROLE=$CORESCOPE_ACCOUNTS
  export CORESCOPE_ACCOUNTS_PASSWORD
else
  ACCOUNTS_ROLE=
  printf '%s\n' "postgres-roles: CORESCOPE_ACCOUNTS_PASSWORD not set; skipping the accounts role and schema corescope_users" >&2
fi

command -v psql >/dev/null 2>&1 || die "psql not found"

# -X: no ~/.psqlrc. VERBOSITY terse: an error reports its position, not the
# statement text, so a failed ALTER ROLE cannot print a password.
psql -X -q -At -v ON_ERROR_STOP=1 \
  -v db="$CORESCOPE_DB" -v writer="$CORESCOPE_WRITER" -v reader="$CORESCOPE_READER" \
  -v adopt="$CORESCOPE_ADOPT" -v ctl="$CONTROL_ROLE" \
  -v users_reader="$USERS_READER_ROLE" -v accounts="$ACCOUNTS_ROLE" \
  "$PG_ADMIN_URL" <<'SQL'
\set VERBOSITY terse
\getenv writer_pw CORESCOPE_WRITER_PASSWORD
\getenv reader_pw CORESCOPE_READER_PASSWORD
\getenv accounts_pw CORESCOPE_ACCOUNTS_PASSWORD
\set writer_mark 'CoreScope writer role (scripts/postgres-roles.sh)'
\set reader_mark 'CoreScope reader role (scripts/postgres-roles.sh)'
\set accounts_mark 'CoreScope accounts role (scripts/postgres-roles.sh)'
\set ctl_mark 'CoreScope control client group role (scripts/postgres-roles.sh)'
\set users_reader_mark 'CoreScope users reader group role (scripts/postgres-roles.sh)'
SELECT :'accounts' <> '' AS has_accounts \gset
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
       set_config('corescope.ctl_mark', :'ctl_mark', false),
       set_config('corescope.users_reader', :'users_reader', false),
       set_config('corescope.users_reader_mark', :'users_reader_mark', false),
       set_config('corescope.accounts', :'accounts', false),
       set_config('corescope.accounts_mark', :'accounts_mark', false) \g /dev/null

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
                    current_setting('corescope.ctl_mark')),
                   (current_setting('corescope.users_reader'), 'users reader group',
                    current_setting('corescope.users_reader_mark')),
                   (current_setting('corescope.accounts'), 'accounts role',
                    current_setting('corescope.accounts_mark'))) AS v(name, kind, mark)
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

-- The same for schema corescope_users, checked in the CoreScope database
-- before anything is changed (session settings do not survive \connect).
\if :has_accounts
SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'db') AS db_exists \gset
\if :db_exists
\set admin_db :DBNAME
\connect :db
SELECT set_config('corescope.accounts', :'accounts', false),
       set_config('corescope.adopt', :'adopt', false) \g /dev/null
DO $$
DECLARE
  a text := current_setting('corescope.accounts');
  owner text;
BEGIN
  SELECT pg_get_userbyid(nspowner) INTO owner FROM pg_namespace WHERE nspname = 'corescope_users';
  IF owner IS NOT NULL AND owner <> a AND current_setting('corescope.adopt') <> '1' THEN
    RAISE EXCEPTION 'schema corescope_users in database % exists and is owned by %, not %; set CORESCOPE_ADOPT=1 to take it over', current_database(), owner, a;
  END IF;
END $$;
\connect :admin_db
\endif
\endif

SELECT format('CREATE ROLE %I', :'writer')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'writer') \gexec
SELECT format('CREATE ROLE %I', :'reader')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'reader') \gexec
SELECT format('CREATE ROLE %I NOLOGIN', :'ctl')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'ctl') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'writer', :'writer_mark') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'reader', :'reader_mark') \gexec
SELECT format('CREATE ROLE %I NOLOGIN', :'users_reader')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'users_reader') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'ctl', :'ctl_mark') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'users_reader', :'users_reader_mark') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',
              :'writer', :'writer_pw') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 32 PASSWORD %L',
              :'reader', :'reader_pw') \gexec
-- The server sets read-only itself; the role defaults cover any other client.
SELECT format('ALTER ROLE %I SET default_transaction_read_only = on', :'reader') \gexec
SELECT format('ALTER ROLE %I SET statement_timeout = %L', :'reader', '120s') \gexec
SELECT format('ALTER ROLE %I SET idle_in_transaction_session_timeout = %L', :'reader', '60s') \gexec
-- The groups are only privilege holders: nobody logs in as them.
SELECT format('ALTER ROLE %I WITH NOLOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD NULL',
              g) FROM (VALUES (:'ctl'), (:'users_reader')) AS v(g) \gexec
\if :has_accounts
SELECT format('CREATE ROLE %I', :'accounts')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'accounts') \gexec
SELECT format('COMMENT ON ROLE %I IS %L', :'accounts', :'accounts_mark') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 32 PASSWORD %L',
              :'accounts', :'accounts_pw') \gexec
-- The accounts store writes: never read-only, whatever an adopted role had.
SELECT format('ALTER ROLE %I RESET default_transaction_read_only', :'accounts') \gexec
SELECT format('ALTER ROLE %I SET statement_timeout = %L', :'accounts', '120s') \gexec
SELECT format('ALTER ROLE %I SET idle_in_transaction_session_timeout = %L', :'accounts', '60s') \gexec
\endif
-- One transaction, so the reader and the writer are never seen without their
-- group memberships.
BEGIN;
-- No inherited privileges beyond the control group's: drop every membership
-- the reader holds, predefined roles such as pg_write_all_data and the users
-- reader group included, except the control group membership this script
-- grants (same grantor, so the GRANT below resets its options in place);
-- every membership the two groups and the accounts role hold; every
-- membership in the accounts role, whose privileges nobody inherits; and the
-- writer's membership of the users reader group granted by anyone else. The
-- users reader group's other members (other CoreScope databases' writers on
-- this cluster) are left alone.
SELECT format('REVOKE %I FROM %I GRANTED BY %I', g.rolname, u.rolname, pg_get_userbyid(m.grantor))
  FROM pg_auth_members m
  JOIN pg_roles g ON g.oid = m.roleid
  JOIN pg_roles u ON u.oid = m.member
 WHERE (u.rolname = :'reader'
        AND NOT (g.rolname = :'ctl' AND m.grantor = (SELECT oid FROM pg_roles WHERE rolname = current_user)))
    OR u.rolname IN (:'ctl', :'users_reader', :'accounts')
    OR g.rolname = :'accounts'
    OR (u.rolname = :'writer' AND g.rolname = :'users_reader'
        AND m.grantor <> (SELECT oid FROM pg_roles WHERE rolname = current_user)) \gexec
-- Re-granting an existing membership only resets its options; hide the
-- "already been granted" notice that comes with it.
SET LOCAL client_min_messages = warning;
SELECT format('GRANT %I TO %I WITH ADMIN FALSE, INHERIT TRUE, SET FALSE GRANTED BY CURRENT_USER', :'ctl', :'reader') \gexec
SELECT format('GRANT %I TO %I WITH ADMIN FALSE, INHERIT TRUE, SET FALSE GRANTED BY CURRENT_USER', :'users_reader', :'writer') \gexec
COMMIT;

SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'writer')
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'db') \gexec
SELECT format('ALTER DATABASE %I OWNER TO %I', :'db', :'writer') \gexec
-- Only the CoreScope roles may connect; the reader and the accounts role get
-- no TEMPORARY (the server uses no temporary tables), the writer keeps it as
-- the owner.
SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', :'db') \gexec
SELECT format('REVOKE ALL ON DATABASE %I FROM %I', :'db', :'reader') \gexec
SELECT format('GRANT CONNECT, TEMPORARY ON DATABASE %I TO %I', :'db', :'writer') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'db', :'reader') \gexec
\if :has_accounts
SELECT format('REVOKE ALL ON DATABASE %I FROM %I', :'db', :'accounts') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'db', :'accounts') \gexec
\endif

\connect :db
-- The roles that get nothing in the measurement schema but what is granted
-- back below (the reader's SELECT), and nothing in corescope_control but the
-- control group's grants: quoted, comma-separated.
SELECT string_agg(quote_ident(n), ', ') AS revokees
  FROM (VALUES (:'reader'), (:'ctl'), (:'users_reader'), (nullif(:'accounts', ''))) AS v(n)
 WHERE n IS NOT NULL \gset

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
-- Explicit, because before PostgreSQL 15 public is not owned by the database
-- owner and the writer's CREATE came only from PUBLIC.
SELECT format('GRANT USAGE, CREATE ON SCHEMA public TO %I', :'writer') \gexec
-- The reader's privileges are rebuilt from nothing on every run: whatever it
-- was given since (on tables, sequences, functions, or in the writer's
-- default privileges, schema-wide or global) goes, then SELECT on tables
-- comes back. The group roles and the accounts role get nothing here at all.
SELECT format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %s', :'revokees') \gexec
SELECT format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %s', :'revokees') \gexec
SELECT format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM %s', :'revokees') \gexec
SELECT format('REVOKE ALL ON SCHEMA public FROM %s', :'revokees') \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I %s REVOKE ALL ON %s FROM %s',
              :'writer', s, o, :'revokees')
  FROM (VALUES ('IN SCHEMA public'), ('')) AS sc(s),
       (VALUES ('TABLES'), ('SEQUENCES'), ('FUNCTIONS'), ('TYPES')) AS ob(o) \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE ALL ON SCHEMAS FROM %s',
              :'writer', :'revokees') \gexec
SELECT format('GRANT USAGE ON SCHEMA public TO %I', :'reader') \gexec
SELECT format('GRANT SELECT ON ALL TABLES IN SCHEMA public TO %I', :'reader') \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public GRANT SELECT ON TABLES TO %I',
              :'writer', :'reader') \gexec

-- The control tables exist once the ingestor has run (pgschema 0002). The
-- reader reaches them only through the group; the group gets exactly what
-- pgschema.EnsureControlGrants grants (keep the two in step;
-- pgschema.CheckControlGrants checks the result). The REVOKE ALL on a table
-- also drops its column privileges, such as an INSERT (created_at). As superuser these GRANTs are
-- recorded as the owner's, the writer's, as the ingestor's are.
BEGIN;
SELECT pg_advisory_xact_lock(:lock_key) \g /dev/null
SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'corescope_control') AS has_control \gset
\if :has_control
SELECT format('REVOKE ALL ON SCHEMA corescope_control FROM PUBLIC, %s', :'revokees') \gexec
SELECT format('REVOKE ALL ON ALL TABLES IN SCHEMA corescope_control FROM PUBLIC, %s', :'revokees') \gexec
SELECT format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA corescope_control FROM %s', :'revokees') \gexec
SELECT format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA corescope_control FROM %s', :'revokees') \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA corescope_control REVOKE ALL ON %s FROM %s',
              :'writer', o, :'revokees')
  FROM (VALUES ('TABLES'), ('SEQUENCES'), ('FUNCTIONS'), ('TYPES')) AS ob(o) \gexec
SELECT format('GRANT USAGE ON SCHEMA corescope_control TO %I', :'ctl') \gexec
SELECT format('GRANT %s ON TABLE corescope_control.%I TO %I', p, t, :'ctl')
  FROM (VALUES ('prune_requests', 'SELECT, INSERT (id, payload)'),
               ('mbcap_snapshots', 'SELECT, INSERT (id, payload)'),
               ('prune_results', 'SELECT')) AS g(t, p)
 WHERE to_regclass(format('corescope_control.%I', t)) IS NOT NULL \gexec
\endif
COMMIT;

-- The accounts schema: the accounts role's, created here so that the store
-- never needs CREATE on the database. Nobody else gets anything in it from
-- this script; the store grants the users reader group its SELECTs, which
-- are left as they are.
\if :has_accounts
SELECT format('CREATE SCHEMA corescope_users AUTHORIZATION %I', :'accounts')
 WHERE NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'corescope_users') \gexec
-- Reached only when the schema is the accounts role's or CORESCOPE_ADOPT=1.
SELECT format('ALTER SCHEMA corescope_users OWNER TO %I', :'accounts') \gexec
SELECT format('REVOKE ALL ON SCHEMA corescope_users FROM PUBLIC, %I, %I, %I', :'writer', :'reader', :'ctl') \gexec
SELECT format('REVOKE ALL ON ALL TABLES IN SCHEMA corescope_users FROM PUBLIC, %I, %I, %I', :'writer', :'reader', :'ctl') \gexec
SELECT format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA corescope_users FROM PUBLIC, %I, %I, %I', :'writer', :'reader', :'ctl') \gexec
SELECT format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA corescope_users FROM %I, %I, %I', :'writer', :'reader', :'ctl') \gexec
SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I %s REVOKE ALL ON %s FROM %I, %I, %I',
              :'accounts', s, o, :'writer', :'reader', :'ctl')
  FROM (VALUES ('IN SCHEMA corescope_users'), ('')) AS sc(s),
       (VALUES ('TABLES'), ('SEQUENCES'), ('FUNCTIONS'), ('TYPES')) AS ob(o) \gexec
\endif

-- Connection URLs, without passwords. HOST is a directory for a Unix socket;
-- a URL carries that percent-encoded.
SELECT replace(:'HOST', '/', '%2F') AS url_host \gset
SELECT format('CORESCOPE_WRITER_URL=postgres://%s@%s:%s/%s', :'writer', :'url_host', :'PORT', :'db')
UNION ALL
SELECT format('CORESCOPE_READER_URL=postgres://%s@%s:%s/%s', :'reader', :'url_host', :'PORT', :'db')
UNION ALL
SELECT format('CORESCOPE_ACCOUNTS_URL=postgres://%s@%s:%s/%s', :'accounts', :'url_host', :'PORT', :'db')
 WHERE :'accounts' <> '';
SQL
