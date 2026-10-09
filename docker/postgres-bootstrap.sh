#!/bin/sh
set -eu

# Only the owner service may initialize/adopt PostgreSQL. Existing SQLite data
# requires the explicit verified switch; a missing record is never a fallback.
/app/storage.sh postgres-setup
/app/storage.sh managed-postgres
# Legacy check-ready remains a read-only schema check; use owner-only environment
# for this child without changing the runtime account role URL or leaking argv.
databases=corescope_telemetry
account_owner=
case $(/app/storage.sh field has_accounts) in
  true)
    databases="$databases corescope_accounts"
    account_owner=${CORESCOPE_USERS_OWNER_DATABASE_URL:?set account owner URL}
    ;;
  false) ;;
  *) echo 'Cannot read the recorded account target.' >&2; exit 1 ;;
esac
CORESCOPE_USERS_DATABASE_URL="$account_owner" /app/corescope-migrate -check-ready

export PGHOST=postgres PGPORT=5432 PGSSLMODE=disable
export PGUSER=corescope_owner PGPASSWORD="${CORESCOPE_OWNER_PASSWORD:?set CORESCOPE_OWNER_PASSWORD}"
for PGDATABASE in $databases; do
  export PGDATABASE
  psql -X -v ON_ERROR_STOP=1 -f /app/postgres-grants.sql
done
