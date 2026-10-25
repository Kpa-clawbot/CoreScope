#!/usr/bin/env bash
# Run the selected native engine's full SQL binding/security regressions.
# The default SQLite job deliberately requires no PostgreSQL service.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
case "${CORESCOPE_TEST_BACKEND:-sqlite}" in
  sqlite|postgres)
    export CORESCOPE_DB_BACKEND="${CORESCOPE_TEST_BACKEND:-sqlite}"
    exec bash "$SCRIPT_DIR/test-blacklist-$CORESCOPE_DB_BACKEND.sh"
    ;;
  *) echo 'FAIL: unsupported CORESCOPE_TEST_BACKEND; choose sqlite or postgres' >&2; exit 2 ;;
esac
