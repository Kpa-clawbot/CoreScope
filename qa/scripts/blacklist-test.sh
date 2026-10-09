#!/usr/bin/env bash
# blacklist-test.sh — verify nodeBlacklist hides a pubkey from API surface
# while retaining its packets in the DB. Implements QA plan §10.1 + §10.2.
#
# Usage:
#   blacklist-test.sh BASELINE_URL TARGET_URL
#
# BASELINE_URL is currently unused for assertions but kept as a positional
# arg for parity with other qa-suite scripts (always called with two URLs).
#
# Required env (target host control + test data):
#   TEST_NODE_PUBKEY      — hex pubkey of a real, currently-visible node on TARGET_URL
#   TARGET_SSH_HOST       — e.g. runner@example
#   TARGET_SSH_KEY        — path to ssh private key (default: /root/.ssh/id_ed25519)
#   TARGET_CONFIG_PATH    — absolute path to config.json on the target
#   TARGET_CONTAINER      — docker container name on the target
# Optional env:
#   CORESCOPE_DB_BACKEND   — target's selected backend: sqlite (default) or postgres
#   TARGET_DB_PATH        — selected SQLite path on the target for the native reader
#   CORESCOPE_READER_DATABASE_URL — private local PostgreSQL reader URL for §10.2
#   PGSERVICE / PGDATABASE — alternatively configure private native libpq settings
#   CORESCOPE_QA_PYTHON   — Python 3 executable for URL parsing (default python3)
#   ADMIN_API_TOKEN       — if /api/admin/transmissions exists, use it instead of the DB probe
#                            (read from env, not argv — never appears in ps)
#   CURL_TIMEOUT          — per-request curl timeout, seconds (default 60)
#   RESTART_WAIT_S        — max wait for /api/stats after restart (default 120)
#
# Distinguishes:
#   ssh-failed     → cannot reach/control target
#   restart-stuck  → /api/stats not 200 within RESTART_WAIT_S
#   hide-failed    → blacklisted pubkey still surfaced via API (§10.1 fail)
#   retain-failed  → no transmissions.from_pubkey rows (ADVERTs) for the
#                    blacklisted pubkey in the DB (§10.2 fail), or the
#                    §10.2 probe cannot verify its selected native reader.
#                    Native parameter binding is mandatory; neither an
#                    interpolated query nor another backend is a fallback.
#   teardown-failed→ post-test removal did not restore listing
#
# Exit code = number of failures (0 = pass).
# PUBLIC repo: zero PII — no real pubkeys, IPs, or hostnames as defaults.
#
# Structure: helpers live at top level and the imperative body lives in main(),
# so test-blacklist-sql.sh can source this file and exercise individual helpers
# without running the suite. Same idiom as scripts/staging/disk-monitor.sh.

set -uo pipefail

SSH_OPTS=()  # populated by main() from TARGET_SSH_KEY
ssh_t() { ssh "${SSH_OPTS[@]}" "$TARGET_SSH_HOST" "$@"; }

# -----------------------------------------------------------------------------
# Teardown — MANDATORY in all exit paths.
# -----------------------------------------------------------------------------
teardown() {
  local rc=$?
  if [[ "$TEARDOWN_DONE" == "1" ]]; then rm -rf "$TMP"; exit "$rc"; fi
  TEARDOWN_DONE=1
  echo "=== teardown: removing $TEST_PUBKEY from nodeBlacklist ==="
  if remove_from_blacklist && restart_target && wait_for_stats; then
    if node_visible; then
      echo "  ✅ teardown ok — node returned to listings"
    else
      echo "  ❌ teardown-failed: node still hidden after removal"
      rc=$((rc + 1))
    fi
  else
    echo "  ❌ teardown-failed: could not restore config / restart / stats"
    rc=$((rc + 1))
  fi
  rm -rf "$TMP"
  exit "$rc"
}

# -----------------------------------------------------------------------------
# Helpers
# -----------------------------------------------------------------------------
fetch_code() {
  local url="$1" out="$2"
  curl -s -m "$CURL_TIMEOUT" -o "$out" -w "%{http_code}" "$url" 2>/dev/null || echo "000"
}

wait_for_stats() {
  local deadline code
  echo "  waiting up to ${RESTART_WAIT_S}s for $TARGET_URL/api/stats ..."
  deadline=$(( $(date +%s) + RESTART_WAIT_S ))
  while (( $(date +%s) < deadline )); do
    code=$(fetch_code "$TARGET_URL/api/stats" "$TMP/stats.json")
    if [[ "$code" == "200" ]]; then echo "  stats OK"; return 0; fi
    sleep 3
  done
  echo "  ❌ restart-stuck: /api/stats never returned 200"
  return 1
}

restart_target() {
  echo "  restarting container $TARGET_CONTAINER ..."
  # TARGET_CONTAINER is validated above; still quote defensively.
  if ! ssh_t "docker restart $(printf %q "$TARGET_CONTAINER")" >/dev/null; then
    echo "  ❌ ssh-failed: docker restart failed"
    return 1
  fi
  return 0
}

# Mutate config.json on target. Values pass via env (printf %q + single-quoted
# heredoc) so $TEST_PUBKEY etc. never enter the remote shell as code.
set_blacklist_state() {
  local mode="$1"  # add | remove
  ssh_t "CFG=$(printf %q "$TARGET_CONFIG_PATH") PK=$(printf %q "$TEST_PUBKEY") MODE=$(printf %q "$mode") bash -s" <<'REMOTE'
set -euo pipefail
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
if command -v jq >/dev/null; then
  if [ "$MODE" = "add" ]; then
    jq --arg pk "$PK" '.nodeBlacklist = ((.nodeBlacklist // []) + [$pk] | unique)' "$CFG" > "$TMP"
  else
    jq --arg pk "$PK" '.nodeBlacklist = ((.nodeBlacklist // []) - [$pk])' "$CFG" > "$TMP"
  fi
else
  python3 - "$CFG" "$PK" "$MODE" "$TMP" <<'PY'
import json, sys
cfg, pk, mode, out = sys.argv[1:]
with open(cfg) as f: d = json.load(f)
bl = list(dict.fromkeys(d.get("nodeBlacklist") or []))
if mode == "add":
    if pk not in bl: bl.append(pk)
else:
    bl = [x for x in bl if x != pk]
d["nodeBlacklist"] = bl
with open(out, "w") as f: json.dump(d, f, indent=2)
PY
fi
# Preserve mode and ownership; mv across same FS is atomic.
chmod --reference="$CFG" "$TMP" 2>/dev/null || true
chown --reference="$CFG" "$TMP" 2>/dev/null || true
mv "$TMP" "$CFG"
trap - EXIT
REMOTE
  local rc=$?
  if (( rc != 0 )); then
    echo "  ❌ ssh-failed: could not edit $TARGET_CONFIG_PATH ($mode)"
    return 1
  fi
  return 0
}

add_to_blacklist()      { set_blacklist_state add; }
remove_from_blacklist() { set_blacklist_state remove; }

node_visible() {
  # Returns 0 if the pubkey is currently visible via API.
  local code
  code=$(fetch_code "$TARGET_URL/api/nodes/$TEST_PUBKEY" "$TMP/node.json")
  if [[ "$code" == "200" ]]; then return 0; fi
  fetch_code "$TARGET_URL/api/nodes?limit=10000" "$TMP/nodes.json" >/dev/null
  if grep -qF -- "\"$TEST_PUBKEY\"" "$TMP/nodes.json" 2>/dev/null; then
    return 0
  fi
  return 1
}

# -----------------------------------------------------------------------------
# §10.2 DB probe — bind the pubkey, do not interpolate it (issue #1977)
# -----------------------------------------------------------------------------
# Native psql uses the extended query protocol (\bind), not text substitution.
# Queries and hex-only parameter values travel on stdin; connection credentials
# stay in private libpq environment/service settings, never process arguments.
POSTGRES_RUNNER=""  # local | container | host
SQL_BACKEND="${CORESCOPE_DB_BACKEND:-sqlite}"
POSTGRES_PROBE_TOKEN="corescope-probe-ok"
RETAIN_COUNT=""
POSTGRES_CONNECT_PY="$(cat "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/postgres-connect.py")"

# A psql meta-command argument whose only variable bytes are [0-9a-f]. od -v
# prevents repeated long values collapsing to '*'. Empty input binds as ''.
sql_hex_literal() {
  [[ "$SQL_BACKEND" != sqlite ]] || printf x
  printf "'%s'" "$(printf '%s' "$1" | od -An -v -tx1 | tr -d ' \n')"
}

postgres_read_guard() {
  cat <<'SQL'
BEGIN READ ONLY;
DO $qa$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=current_user AND
             (rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls))
    OR has_database_privilege(current_database(),'CREATE')
    OR EXISTS (SELECT 1 FROM pg_namespace WHERE nspname !~ '^pg_' AND nspname <> 'information_schema'
               AND (has_schema_privilege(oid,'CREATE') OR pg_has_role(current_user,nspowner,'MEMBER')))
    OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
               WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
                 AND CASE WHEN c.relkind IN ('r','p','v','m','f')
                     THEN has_table_privilege(c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')
                          OR pg_has_role(current_user,c.relowner,'MEMBER') ELSE false END)
    OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
               WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
                 AND CASE WHEN c.relkind='S' THEN has_sequence_privilege(c.oid,'USAGE,UPDATE') ELSE false END)
 THEN RAISE EXCEPTION 'QA requires a restricted PostgreSQL reader role' USING ERRCODE='42501'; END IF;
 IF (SELECT count(*) FROM corescope_schema WHERE kind='telemetry' AND version=1 AND ready) <> 1
 THEN RAISE EXCEPTION 'telemetry schema is not ready' USING ERRCODE='55000'; END IF;
END $qa$;
SQL
}
postgres_transmission_count_sql() {
  postgres_read_guard
  printf '%s\n' "SELECT COUNT(*) FROM transmissions WHERE from_pubkey = lower(convert_from(decode(\$1,'hex'),'UTF8'))"
  printf '\\bind %s\n\\g\nCOMMIT;\n' "$(sql_hex_literal "$1")"
}
postgres_probe_sql() {
  postgres_read_guard
  printf '%s\n' "SELECT convert_from(decode(\$1,'hex'),'UTF8')"
  printf '\\bind %s\n\\g\nCOMMIT;\n' "$(sql_hex_literal "$POSTGRES_PROBE_TOKEN")"
}

# POSIX shell quoting also works inside the image's /bin/sh. These arguments
# contain only public launcher code/container names, never a URL or password.
postgres_shell_quote() {
  local value="$1"
  value=${value//\'/\'\\\'\'}
  printf "'%s'" "$value"
}
postgres_command() {
  local python
  python=$(postgres_shell_quote "$POSTGRES_CONNECT_PY")
  printf '%s' "set +x; if [ -n \"\${CORESCOPE_READER_DATABASE_URL:-}\" ]; then exec \"\${CORESCOPE_QA_PYTHON:-python3}\" -c $python; elif [ -n \"\${PGSERVICE:-}\" ] || [ -n \"\${PGDATABASE:-}\" ]; then case \"\${PGDATABASE:-}\" in *://*) echo 'put connection URLs in CORESCOPE_READER_DATABASE_URL' >&2; exit 2;; esac; export PGCONNECT_TIMEOUT=10 PGCLIENTENCODING=UTF8; exec psql -X -qAt -w -v ON_ERROR_STOP=1 -v VERBOSITY=sqlstate; else echo 'configure a private PostgreSQL reader connection' >&2; exit 2; fi"
}
run_postgres() {
  local cmd
  cmd=$(postgres_command)
  case "$POSTGRES_RUNNER" in
    local) sh -c "$cmd" ;;
    container) ssh_t "docker exec -i $(printf %q "$TARGET_CONTAINER") sh -c $(postgres_shell_quote "$cmd")" ;;
    host) ssh_t "sh -c $(postgres_shell_quote "$cmd")" ;;
    *) echo 'run_postgres: no reader runner resolved' >&2; return 127 ;;
  esac | tr -d '\r'
}
resolve_postgres_runner() {
  local runner out
  POSTGRES_RUNNER=""
  # An explicit local connection is authoritative: never silently fall through
  # to a different database after a bad URL, elevated role, or failed import.
  if [[ -n "${CORESCOPE_READER_DATABASE_URL:-}${PGSERVICE:-}${PGDATABASE:-}" ]]; then
    local candidates=(local)
  else
    local candidates=(container host)
  fi
  for runner in "${candidates[@]}"; do
    POSTGRES_RUNNER="$runner"
    if out=$(postgres_probe_sql | run_postgres 2>>"$TMP/postgres-probe.err") && [[ "$out" == "$POSTGRES_PROBE_TOKEN" ]]; then return 0; fi
  done
  POSTGRES_RUNNER=""
  return 1
}
postgres_error_summary() {
  # libpq connection errors can contain caller-supplied strings. Publish only
  # SQLSTATE classes, retaining the complete diagnostic in the private temp file.
  local summary
  summary=$(sed -nE 's/.*(ERROR|FATAL):[[:space:]]+([0-9A-Z]{5}).*/PostgreSQL error \2/p' "$1" | tail -3)
  if [[ -n "$summary" ]]; then printf '%s\n' "$summary" >&2; else
    echo 'Check PostgreSQL 18 client availability and private reader connection settings.' >&2
  fi
}

# Native SQLite CLI bindings from the original SQLite QA path (#1977). The
# blob-to-text cast keeps spaces/newlines out of dot-command argument parsing.
# -readonly forbids persistent writes; .parameter uses only a temporary table.
SQLITE_ARGS=(-readonly -batch -bail -init /dev/null -noheader -list)
SQLITE_PROBE_TOKEN="corescope-probe-ok"
SQLITE_RUNNER=""
sqlite_transmission_count_sql() {
  printf '.parameter init\n'
  printf '.parameter set :pubkey "cast(%s as text)"\n' "$(sql_hex_literal "$1")"
  printf 'SELECT COUNT(*) FROM transmissions WHERE from_pubkey = lower(:pubkey);\n'
}
transmission_count_sql() {
  case "$SQL_BACKEND" in
    sqlite) sqlite_transmission_count_sql "$1" ;;
    postgres) postgres_transmission_count_sql "$1" ;;
    *) echo 'Unsupported QA database backend' >&2; return 2 ;;
  esac
}
sqlite_probe_sql() {
  printf '.parameter init\n'
  printf '.parameter set :probe "cast(%s as text)"\n' "$(sql_hex_literal "$SQLITE_PROBE_TOKEN")"
  printf 'SELECT :probe;\n'
}
resolve_sqlite_runner() {
  local probe out
  probe=$(sqlite_probe_sql)
  SQLITE_RUNNER=""
  if out=$(ssh_t "docker exec -i $(printf %q "$TARGET_CONTAINER") sqlite3 ${SQLITE_ARGS[*]} :memory:" \
      <<<"$probe" 2>>"$TMP/sqlite-probe.err" | tr -d '\r') && [[ "$out" == "$SQLITE_PROBE_TOKEN" ]]; then
    SQLITE_RUNNER=container; return 0
  fi
  if out=$(ssh_t "sqlite3 ${SQLITE_ARGS[*]} :memory:" <<<"$probe" 2>>"$TMP/sqlite-probe.err" | tr -d '\r') && [[ "$out" == "$SQLITE_PROBE_TOKEN" ]]; then
    SQLITE_RUNNER=host; return 0
  fi
  return 1
}
run_sqlite() {
  case "$SQLITE_RUNNER" in
    container) ssh_t "docker exec -i $(printf %q "$TARGET_CONTAINER") sqlite3 ${SQLITE_ARGS[*]} $(printf %q "$TARGET_DB_PATH")" ;;
    host) ssh_t "sqlite3 ${SQLITE_ARGS[*]} $(printf %q "$TARGET_DB_PATH")" ;;
    *) echo 'run_sqlite: no runner resolved' >&2; return 127 ;;
  esac | tr -d '\r'
}
read_sqlite_retain_count() {
  if [[ -z "${TARGET_DB_PATH:-}" ]]; then
    echo '  ❌ retain-failed: TARGET_DB_PATH is required for the selected SQLite reader'
    return 1
  fi
  if ! resolve_sqlite_runner; then
    echo '  ❌ retain-failed: no SQLite client able to bind a parameter on the target'
    cat "$TMP/sqlite-probe.err" >&2
    return 1
  fi
  echo "  SQLite reader runner: $SQLITE_RUNNER"
  if ! RETAIN_COUNT=$(transmission_count_sql "$TEST_PUBKEY" | run_sqlite 2>"$TMP/sqlite.err"); then
    echo "  ❌ retain-failed: SQLite query failed via $SQLITE_RUNNER"
    cat "$TMP/sqlite.err" >&2
    RETAIN_COUNT=""
    return 1
  fi
  if ! [[ "$RETAIN_COUNT" =~ ^[0-9]+$ ]]; then
    echo '  ❌ retain-failed: SQLite query did not return one numeric count'
    RETAIN_COUNT=""
    return 1
  fi
}
read_retain_count() {
  RETAIN_COUNT=""
  local code
  if [[ -n "$ADMIN_API_TOKEN" ]]; then
    code=$(printf 'header = "Authorization: Bearer %s"\n' "$ADMIN_API_TOKEN" | \
      curl -s -m "$CURL_TIMEOUT" -K - -o "$TMP/admin.json" -w "%{http_code}" \
        "$TARGET_URL/api/admin/transmissions?from_node=$TEST_PUBKEY&count=1" 2>/dev/null || echo '000')
    if [[ "$code" == 200 ]]; then
      RETAIN_COUNT=$(jq -r '.count // ((.transmissions // []) | length)' "$TMP/admin.json" 2>/dev/null || echo '')
    fi
    if [[ "$RETAIN_COUNT" =~ ^[0-9]+$ ]]; then return 0; fi
    RETAIN_COUNT=""
  fi
  case "$SQL_BACKEND" in
    sqlite) read_sqlite_retain_count; return $? ;;
    postgres) ;;
    *) echo '  ❌ retain-failed: unsupported QA database backend'; return 1 ;;
  esac
  if ! resolve_postgres_runner; then
    echo '  ❌ retain-failed: no ready, restricted PostgreSQL reader with native parameter binding'
    postgres_error_summary "$TMP/postgres-probe.err"
    return 1
  fi
  echo "  PostgreSQL reader runner: $POSTGRES_RUNNER"
  if ! RETAIN_COUNT=$(transmission_count_sql "$TEST_PUBKEY" | run_postgres 2>"$TMP/postgres.err"); then
    echo "  ❌ retain-failed: PostgreSQL query failed via $POSTGRES_RUNNER"
    postgres_error_summary "$TMP/postgres.err"
    RETAIN_COUNT=""
    return 1
  fi
  if ! [[ "$RETAIN_COUNT" =~ ^[0-9]+$ ]]; then
    echo '  ❌ retain-failed: PostgreSQL query did not return one numeric count'
    RETAIN_COUNT=""
    return 1
  fi
  return 0
}

# -----------------------------------------------------------------------------
# main
# -----------------------------------------------------------------------------
main() {
  BASELINE_URL="${1:-}"
  TARGET_URL="${2:-}"
  if [[ -z "$BASELINE_URL" || -z "$TARGET_URL" ]]; then
    echo "usage: $0 BASELINE_URL TARGET_URL  (TEST_NODE_PUBKEY+TARGET_* via env)" >&2
    exit 2
  fi

  TEST_PUBKEY="${TEST_NODE_PUBKEY:-}"
  TARGET_SSH_HOST="${TARGET_SSH_HOST:-}"
  TARGET_SSH_KEY="${TARGET_SSH_KEY:-/root/.ssh/id_ed25519}"
  TARGET_CONFIG_PATH="${TARGET_CONFIG_PATH:-}"
  TARGET_CONTAINER="${TARGET_CONTAINER:-}"
  TARGET_DB_PATH="${TARGET_DB_PATH:-}"
  ADMIN_API_TOKEN="${ADMIN_API_TOKEN:-}"
  case "$SQL_BACKEND" in sqlite|postgres) ;; *) echo 'error: CORESCOPE_DB_BACKEND must be sqlite or postgres' >&2; exit 2 ;; esac

  if [[ -z "$TEST_PUBKEY" || -z "$TARGET_SSH_HOST" || -z "$TARGET_CONFIG_PATH" || -z "$TARGET_CONTAINER" ]]; then
    echo "error: TEST_NODE_PUBKEY, TARGET_SSH_HOST, TARGET_CONFIG_PATH, TARGET_CONTAINER are required" >&2
    exit 2
  fi

  # Hard input validation — these strings are interpolated into the remote shell.
  # §10.2's SQL binds TEST_PUBKEY as a parameter rather than interpolating it, so
  # for the SQL layer this gate is defence in depth rather than the only guard
  # (issue #1977). Keep it: redundant is not the same as wrong.
  # Pubkey must be hex (MeshCore pubkeys are hex-encoded ed25519 prefixes).
  if ! [[ "$TEST_PUBKEY" =~ ^[0-9a-fA-F]+$ ]]; then
    echo "error: TEST_NODE_PUBKEY must be hex (got: redacted)" >&2
    exit 2
  fi
  # Container name must match docker's allowed chars: [a-zA-Z0-9][a-zA-Z0-9_.-]*
  if ! [[ "$TARGET_CONTAINER" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]]; then
    echo "error: TARGET_CONTAINER has illegal chars" >&2
    exit 2
  fi
  # Config path must be an absolute, sane path (no spaces, quotes, $, ;, etc.).
  if ! [[ "$TARGET_CONFIG_PATH" =~ ^/[A-Za-z0-9_./-]+$ ]]; then
    echo "error: TARGET_CONFIG_PATH must be a sane absolute path" >&2
    exit 2
  fi
  if [[ "$SQL_BACKEND" == postgres && -n "$TARGET_DB_PATH" ]]; then
    echo 'error: TARGET_DB_PATH is a SQLite path; configure the selected PostgreSQL reader connection' >&2
    exit 2
  fi
  if [[ -n "$TARGET_DB_PATH" && ! "$TARGET_DB_PATH" =~ ^/[A-Za-z0-9_./-]+$ ]]; then
    echo 'error: TARGET_DB_PATH must be a sane absolute SQLite path' >&2
    exit 2
  fi

  CURL_TIMEOUT="${CURL_TIMEOUT:-60}"
  RESTART_WAIT_S="${RESTART_WAIT_S:-120}"

  SSH_OPTS=(-i "$TARGET_SSH_KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o BatchMode=yes)

  TMP=$(mktemp -d)
  fails=0
  TEARDOWN_DONE=0
  trap teardown EXIT INT TERM

  # ---------------------------------------------------------------------------
  # §10.1 — hide
  # ---------------------------------------------------------------------------
  echo "=== §10.1 add $TEST_PUBKEY to nodeBlacklist ==="
  if ! add_to_blacklist; then fails=$((fails+1)); exit "$fails"; fi
  if ! restart_target;    then fails=$((fails+1)); exit "$fails"; fi
  if ! wait_for_stats;    then fails=$((fails+1)); exit "$fails"; fi

  detail_code=$(fetch_code "$TARGET_URL/api/nodes/$TEST_PUBKEY" "$TMP/detail.json")
  list_code=$(fetch_code "$TARGET_URL/api/nodes?limit=10000" "$TMP/list.json")
  in_list=0
  if [[ "$list_code" == "200" ]] && grep -qF -- "\"$TEST_PUBKEY\"" "$TMP/list.json"; then
    in_list=1
  fi
  if [[ "$detail_code" == "404" || "$in_list" == "0" ]]; then
    echo "  ✅ hide ok: detail=$detail_code in_list=$in_list"
  else
    echo "  ❌ hide-failed: detail=$detail_code in_list=$in_list — pubkey still surfaced"
    fails=$((fails+1))
  fi

  topo_code=$(fetch_code "$TARGET_URL/api/topology" "$TMP/topo.json")
  if [[ "$topo_code" != "200" ]]; then
    echo "  ⚠️  /api/topology HTTP $topo_code — skipping topology assertion"
  elif grep -qF -- "$TEST_PUBKEY" "$TMP/topo.json"; then
    echo "  ❌ hide-failed: /api/topology references blacklisted pubkey"
    fails=$((fails+1))
  else
    echo "  ✅ topology clean"
  fi

  # ---------------------------------------------------------------------------
  # §10.2 — DB retain
  # ---------------------------------------------------------------------------
  echo "=== §10.2 verify packets retained in DB ==="
  if ! read_retain_count; then
    # read_retain_count already printed the classified reason. Counting here and
    # nowhere else, so one failed reader probe remains one classified failure.
    fails=$((fails+1))
  elif [[ "$RETAIN_COUNT" =~ ^[0-9]+$ ]] && (( RETAIN_COUNT > 0 )); then
    echo "  ✅ DB retains $RETAIN_COUNT packets from $TEST_PUBKEY"
  else
    echo "  ❌ retain-failed: count=$RETAIN_COUNT (expected > 0)"
    fails=$((fails+1))
  fi

  echo "=== summary: $fails failure(s) before teardown ==="
  # trap handles teardown + exit
  exit "$fails"
}

# Only run main when executed directly (not when sourced by tests).
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
