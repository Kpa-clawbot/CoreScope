#!/bin/sh
set -eu

# A legacy instance must go through the explicit offline importer. Retained
# SQLite files are harmless once both PostgreSQL stores have verified readiness.
if [ -f /app/data/meshcore.db ]; then
  /app/corescope-migrate -check-import-kind=telemetry -from-sqlite=/app/data/meshcore.db || {
    echo 'SQLite telemetry exists. Follow docs/postgresql-upgrade.md before starting this instance.' >&2
    exit 1
  }
fi
if [ -f /app/data/users.db ]; then
  /app/corescope-migrate -check-import-kind=accounts -users-from-sqlite=/app/data/users.db || {
    echo 'SQLite account data exists. Follow docs/postgresql-upgrade.md before starting this instance.' >&2
    exit 1
  }
fi
/app/corescope-migrate

# libpq uses these environment settings; URLs/passwords never appear in argv.
export PGHOST="${PGHOST:-postgres}" PGPORT="${PGPORT:-5432}" PGSSLMODE=disable
export PGUSER=corescope_owner PGPASSWORD="${CORESCOPE_OWNER_PASSWORD:?set CORESCOPE_OWNER_PASSWORD}"
export PGDATABASE=corescope_telemetry
psql -X -v ON_ERROR_STOP=1 -f /app/postgres-grants.sql
export PGDATABASE=corescope_accounts
psql -X -v ON_ERROR_STOP=1 -f /app/postgres-grants.sql
