#!/bin/sh
# Packaged selection boundary. Go owns discovery, validation and conversion.
set -eu
state_dir=${CORESCOPE_STATE_DIR:-/app/data}
case "$state_dir" in /app/data|/app/data/*) ;; *)
  echo 'CORESCOPE_STATE_DIR must be /app/data or a persistent subdirectory of that mounted data directory.' >&2; exit 1 ;;
esac
case "/$state_dir/" in */../*|*/./*) echo 'Use a normalized persistent state directory without dot segments.' >&2; exit 1 ;; esac
# Refuse a plain image directory: losing the selection on recreation is unsafe.
awk '$5 == "/app/data" { found=1 } END { exit !found }' /proc/self/mountinfo || {
  echo '/app/data must be a persistent data mount before storage setup.' >&2; exit 1
}
persistent_root=$(cd /app/data && pwd -P)
existing=$state_dir
while [ ! -e "$existing" ] && [ ! -L "$existing" ]; do existing=${existing%/*}; done
resolved=$(readlink -f "$existing") || { echo 'Cannot resolve the selected state directory.' >&2; exit 1; }
case "$resolved" in "$persistent_root"|"$persistent_root"/*) ;; *)
  echo 'The state directory resolves outside the persistent data mount.' >&2; exit 1 ;;
esac
selection_file=$state_dir/storage-selection.json
action=${1:-start}
[ "$#" -eq 0 ] || shift
status() { /app/corescope-migrate -storage-action=status -selection-file "$selection_file"; }
mutate() {
  operation=$1; shift
  /app/corescope-migrate -storage-action="$operation" -selection-file "$selection_file" \
    -config-dir /app -state-dir "$state_dir/storage-jobs" -offline "$@"
}
blocked() {
  job=$(printf '%s\n' "$report" | jq -r '.job_id')
  echo "Storage is not ready; keep services stopped and use storage status, then resume or abort job $job." >&2
  exit 1
}
case "$action" in
  status) status ;;
  field)
    case "${1:-}" in has_accounts|state|backend|generation|job_id|state_dir|source_backend|target_backend|telemetry.sqlite_path|accounts.sqlite_path|telemetry.postgres.host|telemetry.postgres.port|telemetry.postgres.database|telemetry.postgres.schema|accounts.postgres.host|accounts.postgres.port|accounts.postgres.database|accounts.postgres.schema) ;;
      *) echo 'Unsupported storage status field.' >&2; exit 1 ;;
    esac
    report=$(status) || exit 1
    case "$1" in
      has_accounts) printf '%s\n' "$report" | jq -r '.accounts != null' ;;
      source_backend|target_backend) printf '%s\n' "$report" | jq -r ".$1" ;;
      *) printf '%s\n' "$report" | jq -er ".$1 // empty" ;;
    esac
    ;;
  managed-postgres)
    report=$(status) || exit 1
    printf '%s\n' "$report" | jq -e '
      .state == "ready" and .backend == "postgres"
      and .telemetry.postgres == {host:"postgres",port:5432,database:"corescope_telemetry",schema:"public"}
      and (.accounts == null or .accounts.postgres == {host:"postgres",port:5432,database:"corescope_accounts",schema:"public"})
    ' >/dev/null || {
      echo 'Managed operations require the recorded local PostgreSQL telemetry/accounts targets. Use explicit owner tooling for custom endpoints; no local substitute was used.' >&2
      exit 1
    }
    ;;
  start)
    report=$(status) || exit 1
    case $(printf '%s\n' "$report" | jq -er '.state') in
      ready) ;; # Runtime opens the recorded backend; bootstrap hints cannot replace it.
      unrecorded)
        [ "${CORESCOPE_DB_BACKEND:-sqlite}" = sqlite ] || {
          echo 'PostgreSQL needs its matching bootstrap override before application startup.' >&2; exit 1
        }
        # This explicit packaged setup intent still validates all existing data.
        # Neither missing selection nor a missing database is a fallback signal.
        mutate setup -backend=sqlite
        ;;
      *) blocked ;;
    esac
    ;;
  postgres-setup)
    report=$(status) || exit 1
    case $(printf '%s\n' "$report" | jq -er '.state') in
      unrecorded) mutate setup -backend=postgres ;;
      ready)
        [ "$(printf '%s\n' "$report" | jq -er '.backend')" = postgres ] || {
          echo 'SQLite is recorded. Use the explicit offline storage switch before adding PostgreSQL.' >&2; exit 1
        }
        ;;
      *) blocked ;;
    esac
    ;;
  setup|adopt|init|switch|resume|abort) mutate "$action" "$@" ;;
  *) echo 'Unsupported packaged storage action.' >&2; exit 1 ;;
esac
