#!/bin/sh
# Runs once, against an empty PostgreSQL data directory. Passwords stay in the
# environment; psql reads them without putting them in process arguments.
set -eu

for password in "${CORESCOPE_OWNER_PASSWORD:?set CORESCOPE_OWNER_PASSWORD}" \
  "${CORESCOPE_READER_PASSWORD:?set CORESCOPE_READER_PASSWORD}" \
  "${CORESCOPE_WRITER_PASSWORD:?set CORESCOPE_WRITER_PASSWORD}" \
  "${CORESCOPE_ACCOUNTS_PASSWORD:?set CORESCOPE_ACCOUNTS_PASSWORD}" \
  "${CORESCOPE_CHANNELS_PASSWORD:?set CORESCOPE_CHANNELS_PASSWORD}"; do
  case "$password" in
    *[!a-fA-F0-9]*|'') echo 'Database role passwords must be at least 32 hexadecimal characters; use openssl rand -hex 32.' >&2; exit 1 ;;
  esac
  if [ "${#password}" -lt 32 ]; then
    echo 'Database role passwords must be at least 32 hexadecimal characters.' >&2
    exit 1
  fi
done
unset password

psql -X -v ON_ERROR_STOP=1 --username "${POSTGRES_USER:-postgres}" --dbname postgres <<'SQL'
\getenv owner_password CORESCOPE_OWNER_PASSWORD
\getenv reader_password CORESCOPE_READER_PASSWORD
\getenv writer_password CORESCOPE_WRITER_PASSWORD
\getenv accounts_password CORESCOPE_ACCOUNTS_PASSWORD
\getenv channels_password CORESCOPE_CHANNELS_PASSWORD
CREATE ROLE corescope_owner LOGIN PASSWORD :'owner_password';
CREATE ROLE corescope_reader LOGIN PASSWORD :'reader_password';
CREATE ROLE corescope_writer LOGIN PASSWORD :'writer_password';
CREATE ROLE corescope_accounts LOGIN PASSWORD :'accounts_password';
CREATE ROLE corescope_channels LOGIN PASSWORD :'channels_password';
CREATE DATABASE corescope_telemetry OWNER corescope_owner TEMPLATE template0 ENCODING 'UTF8' LOCALE 'C';
CREATE DATABASE corescope_accounts OWNER corescope_owner TEMPLATE template0 ENCODING 'UTF8' LOCALE 'C';
REVOKE ALL ON DATABASE corescope_telemetry FROM PUBLIC;
REVOKE ALL ON DATABASE corescope_accounts FROM PUBLIC;
GRANT CONNECT ON DATABASE corescope_telemetry TO corescope_reader, corescope_writer;
GRANT CONNECT ON DATABASE corescope_accounts TO corescope_accounts, corescope_channels;
ALTER ROLE corescope_reader SET default_transaction_read_only = on;
ALTER ROLE corescope_channels SET default_transaction_read_only = on;
SQL
