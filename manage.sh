#!/bin/bash
# CoreScope — Setup & Management Helper
# Usage: ./manage.sh [command]
#
# All container management goes through docker compose.
# Container config lives in docker-compose.yml — this script is just a wrapper.
#
# Idempotent: safe to cancel and re-run at any point.
# Each step checks what's already done and skips it.
set +x
set -e

IMAGE_NAME="corescope"
STATE_FILE=".setup-state"
STAGING_CONTAINER="corescope-staging-go"

# Source .env for port/path overrides (same file docker compose reads)
# Strip \r (Windows line endings) to avoid "$'\r': command not found"
if [ -f .env ]; then
  set -a
  while IFS='=' read -r key value || [ -n "$key" ]; do
    key=$(printf '%s' "$key" | sed 's/\r$//' | sed 's/^[[:space:]]*//' | sed 's/[[:space:]]*$//')
    [[ "$key" =~ ^#.*$ || -z "$key" ]] && continue
    value=$(printf '%s' "$value" | sed 's/\r$//' | sed 's/^[[:space:]]*//' | sed 's/[[:space:]]*$//')
    value="${value/#\~/$HOME}"
    export "$key=$value"
  done < .env
  set +a
fi

# Auto-fix CRLF in .env if detected
if [ -f .env ] && grep -qP '\r' .env 2>/dev/null; then
  warn ".env has Windows line endings (CRLF) — fixing automatically..."
  sed -i 's/\r$//' .env
  log ".env converted to Unix line endings."
fi

# Resolved paths for prod/staging data (must match docker-compose.yml)
PROD_DATA="${PROD_DATA_DIR:-$HOME/meshcore-data}"
STAGING_DATA="${STAGING_DATA_DIR:-$HOME/meshcore-staging-data}"
STAGING_COMPOSE_FILE="docker-compose.staging.yml"

# Build metadata — exported so docker compose build picks them up via args
export APP_VERSION=$(git describe --tags --match "v*" 2>/dev/null || echo "unknown")
export GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
export BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# Docker Compose — detect v2 plugin vs v1 standalone
if docker compose version &>/dev/null 2>&1; then
  DC="docker compose"
elif command -v docker-compose &>/dev/null; then
  DC="docker-compose"
else
  echo "ERROR: Neither '$DC' nor 'docker-compose' found." >&2
  exit 1
fi

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()  { printf '%b\n' "${GREEN}✓${NC} $1"; }
warn() { printf '%b\n' "${YELLOW}⚠${NC} $1"; }
err()  { printf '%b\n' "${RED}✗${NC} $1" >&2; }
info() { printf '%b\n' "${CYAN}→${NC} $1"; }
step() { printf '%b\n' "\n${BOLD}[$1/$TOTAL_STEPS] $2${NC}"; }

is_true() {
  case "${1:-}" in
    1|true|TRUE|yes|YES|y|Y|on|ON) return 0 ;;
    *) return 1 ;;
  esac
}

# The base files require no PostgreSQL credentials and are also the read-only
# discovery boundary before the recorded backend is known.
dc_prod_base() {
  local file=docker-compose.yml
  if is_true "${DISABLE_MOSQUITTO:-false}"; then file=docker-compose.no-mosquitto.yml; fi
  $DC -f "$file" "$@"
}

dc_staging_base() {
  local file="$STAGING_COMPOSE_FILE"
  if is_true "${DISABLE_MOSQUITTO:-false}"; then file=docker-compose.staging.no-mosquitto.yml; fi
  $DC -f "$file" -p corescope-staging "$@"
}

dc_postgres() {
  local environment="$1" file override
  shift
  require_database_credentials || return 1
  case "$environment" in
    prod)
      file=docker-compose.yml; override=docker-compose.postgres.yml
      if is_true "${DISABLE_MOSQUITTO:-false}"; then file=docker-compose.no-mosquitto.yml; fi
      $DC -f "$file" -f "$override" "$@"
      ;;
    staging)
      file="$STAGING_COMPOSE_FILE"; override=docker-compose.staging.postgres.yml
      if is_true "${DISABLE_MOSQUITTO:-false}"; then file=docker-compose.staging.no-mosquitto.yml; fi
      $DC -f "$file" -f "$override" -p corescope-staging "$@"
      ;;
    *) err "Unknown storage environment"; return 1 ;;
  esac
}

storage_at() {
  local environment="$1" data_directory="$2" service
  local volumes=()
  shift 2
  if [ -n "$data_directory" ]; then volumes=(-v "$data_directory:/app/data"); fi
  case "$environment" in prod) service=prod ;; staging) service=staging-go ;; *) err "Unknown storage environment"; return 1 ;; esac
  "dc_${environment}_base" run --rm --no-deps "${volumes[@]}" --entrypoint /app/storage.sh "$service" "$@"
}

storage_action() {
  local environment="$1"
  shift
  storage_at "$environment" "" "$@"
}

storage_field() {
  storage_action "$1" field "$2"
}

selected_backend() {
  local environment="$1" state backend job
  state=$(storage_field "$environment" state) || { err "Cannot read storage selection. Build the current image with setup; keep the existing data and selection intact."; return 1; }
  case "$state" in
    ready) backend=$(storage_field "$environment" backend) || return 1 ;;
    unrecorded)
      # The packaged entrypoint performs explicit guarded setup. This hint
      # selects its Compose dependencies; it never authorizes fresh data itself.
      backend=${CORESCOPE_DB_BACKEND:-sqlite}
      ;;
    pending)
      job=$(storage_field "$environment" job_id) || return 1
      err "Storage switch $job is pending; keep services stopped and use storage resume or abort."; return 1
      ;;
    *) err "Storage selection is invalid; preserve it and inspect storage status."; return 1 ;;
  esac
  case "$backend" in sqlite|postgres) printf '%s\n' "$backend" ;; *) err "Choose sqlite or postgres during setup."; return 1 ;; esac
}

dc_prod() {
  local backend
  backend=$(selected_backend prod) || return 1
  case "$backend" in sqlite) dc_prod_base "$@" ;; postgres) dc_postgres prod "$@" ;; esac
}

dc_staging() {
  local backend
  backend=$(selected_backend staging) || return 1
  case "$backend" in sqlite) dc_staging_base "$@" ;; postgres) dc_postgres staging "$@" ;; esac
}

require_managed_postgres() {
  storage_action "$1" managed-postgres
}

sqlite_source_exists() {
  local environment="$1" kind="$2" source service
  if [ "$kind" = accounts ] && [ "$(storage_field "$environment" has_accounts)" = false ]; then return 3; fi
  source=$(storage_field "$environment" "$kind.sqlite_path") || return 1
  service=prod; [ "$environment" != staging ] || service=staging-go
  "dc_${environment}_base" run --rm --no-deps --entrypoint /bin/sh "$service" -eu -c '
    if [ -f "$1" ]; then exit 0; fi
    # A nonnil recorded target is initialized. Missing it is data loss, not
    # an intentionally absent optional store, even when no sidecar survives.
    exit 1
  ' sh "$source"
}

sqlite_dump_file() {
  local environment="$1" kind="$2" destination="$3" temporary="$3.partial" source directory service
  case "$kind" in telemetry|accounts) ;; *) err "Unknown SQLite store"; return 1 ;; esac
  [ "$(basename "$destination")" = "$kind.db" ] || { err "Use the native store filename $kind.db"; return 1; }
  source=$(storage_field "$environment" "$kind.sqlite_path") || return 1
  [ ! -e "$destination" ] || { err "Backup target already exists: $destination"; return 1; }
  directory=$(cd "$(dirname "$destination")" && pwd -P) || return 1
  service=prod; [ "$environment" != staging ] || service=staging-go
  (
    umask 077
    set -o noclobber
    : > "$temporary" || exit 1
    trap 'rm -f -- "$temporary"' EXIT HUP INT TERM
    # SQLite's native backup includes committed WAL frames and preserves rowids.
    # The source is read-only; a fixed output name avoids dot-command quoting.
    "dc_${environment}_base" run --rm --no-deps -v "$directory:/backup" --entrypoint sqlite3 "$service" \
      -readonly -cmd '.timeout 5000' "$source" ".backup '/backup/$kind.db.partial'" || exit 1
    sync -f "$temporary" || exit 1
    ln -- "$temporary" "$destination" || exit 1
    rm -- "$temporary" || exit 1
    trap - EXIT HUP INT TERM
  )
}

# Read the owner credential inside the database container, never from argv.
pg_container_exec() {
  local environment="$1" database="$2"
  shift 2
  case "$database" in corescope_telemetry|corescope_accounts) ;; *) err "Unsupported managed database target"; return 1 ;; esac
  dc_postgres "$environment" exec -T postgres sh -c 'export PGHOST=127.0.0.1 PGPORT=5432 PGUSER=corescope_owner PGPASSWORD="$CORESCOPE_OWNER_PASSWORD" PGDATABASE="$1"; shift; exec "$@"' sh "$database" "$@"
}

pg_exec() {
  require_managed_postgres "$1" || return 1
  if [ "$2" = corescope_accounts ] && [ "$(storage_field "$1" has_accounts)" != true ]; then
    err "No initialized account database is selected; managed account operations were refused."
    return 1
  fi
  pg_container_exec "$@"
}

pg_empty() {
  local count execute=pg_exec
  [ "${3:-}" != fresh-staging ] || execute=pg_container_exec
  count=$("$execute" "$1" "$2" psql -X -A -t -v ON_ERROR_STOP=1 -c "SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND c.relkind IN ('r','p','v','m','S','f')") || return 1
  [ "$count" = 0 ]
}

pg_dump_file() {
  local environment="$1" database="$2" destination="$3" temporary="$3.partial"
  if [ -e "$destination" ]; then err "Backup target already exists: $destination"; return 1; fi
  (
    umask 077
    set -o noclobber
    exec 3>"$temporary" || exit 1
    trap 'rm -f -- "$temporary"' EXIT HUP INT TERM
    pg_exec "$environment" "$database" pg_dump --format=custom --no-password --no-owner --no-privileges >&3 || exit 1
    exec 3>&-
    sync -f "$temporary" || exit 1
    ln -- "$temporary" "$destination" || exit 1
    rm -- "$temporary" || exit 1
    trap - EXIT HUP INT TERM
  )
}

pg_restore_file() {
  local environment="$1" database="$2" archive="$3" scope="${4:-}" execute=pg_exec
  [ "$scope" != fresh-staging ] || execute=pg_container_exec
  if [ "$(head -c 5 "$archive")" != PGDMP ]; then err "SQLite files require docs/postgresql-upgrade.md; expected a PostgreSQL archive."; return 1; fi
  pg_empty "$environment" "$database" "$scope" || { err "Refusing to overwrite a nonempty PostgreSQL destination."; return 1; }
  "$execute" "$environment" "$database" pg_restore --no-password --exit-on-error --single-transaction --no-owner --no-privileges --dbname="$database" < "$archive" || return 1
  "$execute" "$environment" "$database" psql -X -v ON_ERROR_STOP=1 < docker/postgres-grants.sql >/dev/null || return 1
  "$execute" "$environment" "$database" psql -X -v ON_ERROR_STOP=1 -c ANALYZE >/dev/null || return 1
}

require_database_credentials() {
  local key value
  if [ -z "$POSTGRES_ADMIN_PASSWORD" ]; then err "Set PostgreSQL credentials in .env before starting; see .env.example."; return 1; fi
  for key in CORESCOPE_OWNER_PASSWORD CORESCOPE_READER_PASSWORD CORESCOPE_WRITER_PASSWORD CORESCOPE_ACCOUNTS_PASSWORD CORESCOPE_CHANNELS_PASSWORD; do
    value=$(printenv "$key" || true)
    if ! [[ "$value" =~ ^[a-fA-F0-9]{32,}$ ]]; then err "Set $key in .env to at least 32 hexadecimal characters; see docs/postgresql-upgrade.md."; return 1; fi
  done
}

# Update only one private bootstrap setting, preserving unrelated .env content.
write_private_env() {
  local key="$1" value="$2" temporary
  umask 077
  temporary=$(mktemp .env.tmp.XXXXXX) || return 1
  if [ -f .env ]; then
    awk -F= -v key="$key" '$1 != key { print }' .env > "$temporary" || { rm -f -- "$temporary"; return 1; }
  fi
  printf '%s=%s\n' "$key" "$value" >> "$temporary"
  chmod 600 "$temporary"
  mv -- "$temporary" .env
  export "$key=$value"
}

prepare_database_credentials() {
  local directory="${PROD_POSTGRES_DATA_DIR:-$HOME/corescope-postgres}" key value occupied=false
  if [ -d "$directory" ] && [ -n "$(find "$directory" -mindepth 1 -maxdepth 1 -print -quit)" ]; then occupied=true; fi
  for key in POSTGRES_ADMIN_PASSWORD CORESCOPE_OWNER_PASSWORD CORESCOPE_READER_PASSWORD CORESCOPE_WRITER_PASSWORD CORESCOPE_ACCOUNTS_PASSWORD CORESCOPE_CHANNELS_PASSWORD; do
    value=$(printenv "$key" || true)
    if [ -z "$value" ]; then
      if $occupied; then err "Existing PostgreSQL storage needs its matching private .env; changing passwords does not rotate existing roles. Keep the volume intact."; return 1; fi
      value=$(openssl rand -hex 32) || return 1
      write_private_env "$key" "$value" || return 1
    fi
  done
  require_database_credentials
}

choose_storage() {
  local state backend reply
  state=$(storage_field prod state) || return 1
  if [ "$state" = ready ]; then
    backend=$(storage_field prod backend) || return 1
    info "Keeping recorded $backend storage. Use storage switch for an explicit offline backend change."
    return 0
  fi
  if [ "$state" != unrecorded ]; then selected_backend prod >/dev/null; return 1; fi
  echo "Storage for this installation:"
  echo "  1) SQLite (default; no separate database service)"
  echo "  2) PostgreSQL (separate local service and private role credentials)"
  read -r -p "Choose [1]: " reply
  case "${reply:-1}" in
    1|sqlite) backend=sqlite ;;
    2|postgres) backend=postgres ;;
    *) err "Choose 1 (SQLite) or 2 (PostgreSQL)."; return 1 ;;
  esac
  write_private_env CORESCOPE_DB_BACKEND "$backend" || return 1
  if [ "$backend" = postgres ]; then prepare_database_credentials || return 1; fi
  info "Setup will validate existing stores before adopting them, or initialize only a provably fresh $backend installation."
}

confirm() {
  read -p "   $1 [y/N] " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]]
}

confirm_yes_default() {
  read -p "   $1 [Y/n] " -n 1 -r
  echo
  [[ -z "$REPLY" || $REPLY =~ ^[Yy]$ ]]
}

# State tracking — marks completed steps so re-runs skip them
mark_done()  { echo "$1" >> "$STATE_FILE"; }
is_done()    { [ -f "$STATE_FILE" ] && grep -qx "$1" "$STATE_FILE" 2>/dev/null; }

# ─── Helpers ──────────────────────────────────────────────────────────────

resolve_domain_ipv4() {
  local domain="$1"
  local resolved_ip=""

  if command -v dig >/dev/null 2>&1; then
    resolved_ip=$(dig +short "$domain" 2>/dev/null | grep -E '^[0-9]+\.' | head -1)
  fi
  if [ -z "$resolved_ip" ] && command -v host >/dev/null 2>&1; then
    resolved_ip=$(host "$domain" 2>/dev/null | awk '/has address/ {print $4; exit}')
  fi
  if [ -z "$resolved_ip" ] && command -v nslookup >/dev/null 2>&1; then
    resolved_ip=$(nslookup "$domain" 2>/dev/null | awk '/^Address: / {print $2}' | grep -E '^[0-9]+\.' | head -1)
  fi
  if [ -z "$resolved_ip" ] && command -v getent >/dev/null 2>&1; then
    resolved_ip=$(getent hosts "$domain" 2>/dev/null | awk '{print $1}' | grep -E '^[0-9]+\.' | head -1)
  fi

  echo "$resolved_ip"
}

has_dns_resolution_tool() {
  command -v dig >/dev/null 2>&1 || \
  command -v host >/dev/null 2>&1 || \
  command -v nslookup >/dev/null 2>&1 || \
  command -v getent >/dev/null 2>&1
}

PORT_CHECK_METHOD=""

resolve_port_check_method() {
  if [ -n "$PORT_CHECK_METHOD" ]; then
    return 0
  fi

  if command -v ss &>/dev/null; then
    PORT_CHECK_METHOD="ss"
  elif command -v lsof &>/dev/null; then
    PORT_CHECK_METHOD="lsof"
  elif command -v netstat &>/dev/null; then
    PORT_CHECK_METHOD="netstat"
  elif command -v nc &>/dev/null; then
    PORT_CHECK_METHOD="nc"
  else
    PORT_CHECK_METHOD="none"
  fi
}

# Returns 0 when in use, 1 when free, 2 when unavailable
is_port_in_use() {
  local port="$1"
  resolve_port_check_method

  case "$PORT_CHECK_METHOD" in
    ss)
      ss -tlnp 2>/dev/null | grep -E "[[:space:]]LISTEN[[:space:]].*[:.]${port}([[:space:]]|$)" >/dev/null
      return $?
      ;;
    lsof)
      lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
      return $?
      ;;
    netstat)
      netstat -tlnp 2>/dev/null | grep -E "[[:space:]]${port}[[:space:]]" >/dev/null
      if [ $? -eq 0 ]; then
        return 0
      fi
      netstat -tlnp 2>/dev/null | grep -E "[:.]${port}[[:space:]]" >/dev/null
      return $?
      ;;
    nc)
      local bind_pid=""
      ( nc -l 127.0.0.1 "$port" >/dev/null 2>&1 ) &
      bind_pid=$!
      sleep 0.2
      if kill -0 "$bind_pid" 2>/dev/null; then
        kill "$bind_pid" 2>/dev/null || true
        wait "$bind_pid" 2>/dev/null || true
        return 1
      fi
      wait "$bind_pid" 2>/dev/null || true
      return 0
      ;;
    *)
      return 2
      ;;
  esac
}

port_in_use_details() {
  local port="$1"
  resolve_port_check_method

  case "$PORT_CHECK_METHOD" in
    ss)
      ss -tlnp 2>/dev/null | grep -E "[[:space:]]LISTEN[[:space:]].*[:.]${port}([[:space:]]|$)" | head -1
      ;;
    lsof)
      lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | sed -n '2p'
      ;;
    netstat)
      netstat -tlnp 2>/dev/null | grep -E "[:.]${port}[[:space:]]" | head -1
      ;;
    *)
      echo ""
      ;;
  esac
}

find_next_available_port() {
  local start="$1"
  local candidate=$((start + 1))
  while [ "$candidate" -le 65535 ]; do
    is_port_in_use "$candidate"
    local rc=$?
    if [ "$rc" -eq 0 ]; then
      candidate=$((candidate + 1))
      continue
    fi
    if [ "$rc" -eq 1 ]; then
      echo "$candidate"
      return 0
    fi
    break
  done
  echo ""
  return 1
}

is_valid_port() {
  local value="$1"
  [[ "$value" =~ ^[0-9]+$ ]] && [ "$value" -ge 1 ] && [ "$value" -le 65535 ]
}

show_env_port_summary() {
  local http_port="$1"
  local https_port="$2"
  local mqtt_port="$3"
  local data_dir="$4"
  local disable_mosquitto="$5"
  echo ""
  echo "   Current .env values:"
  echo "     PROD_HTTP_PORT=${http_port}"
  echo "     PROD_HTTPS_PORT=${https_port}"
  echo "     PROD_MQTT_PORT=${mqtt_port}"
  echo "     DISABLE_MOSQUITTO=${disable_mosquitto}"
  echo "     PROD_DATA_DIR=${data_dir}"
  echo ""
}

get_env_value() {
  local key="$1"
  local env_file="${2:-.env}"
  if [ ! -f "$env_file" ]; then
    echo ""
    return 1
  fi
  sed -n "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*//p" "$env_file" | head -1
}

write_env_managed_values() {
  local http_port="$1"
  local https_port="$2"
  local mqtt_port="$3"
  local data_dir="$4"
  local disable_mosquitto="$5"
  local env_file=".env"
  local tmp_file=".env.tmp.$$"

  if [ ! -f "$env_file" ]; then
    cp .env.example "$env_file"
  fi

  local seen_http=0
  local seen_https=0
  local seen_mqtt=0
  local seen_data=0
  local seen_disable_mosquitto=0

  (umask 077; set -o noclobber; : > "$tmp_file") || return 1
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      PROD_HTTP_PORT=*)
        echo "PROD_HTTP_PORT=${http_port}" >> "$tmp_file"
        seen_http=1
        ;;
      PROD_HTTPS_PORT=*)
        echo "PROD_HTTPS_PORT=${https_port}" >> "$tmp_file"
        seen_https=1
        ;;
      PROD_MQTT_PORT=*)
        echo "PROD_MQTT_PORT=${mqtt_port}" >> "$tmp_file"
        seen_mqtt=1
        ;;
      PROD_DATA_DIR=*)
        echo "PROD_DATA_DIR=${data_dir}" >> "$tmp_file"
        seen_data=1
        ;;
      DISABLE_MOSQUITTO=*)
        echo "DISABLE_MOSQUITTO=${disable_mosquitto}" >> "$tmp_file"
        seen_disable_mosquitto=1
        ;;
      *)
        echo "$line" >> "$tmp_file"
        ;;
    esac
  done < "$env_file"

  [ "$seen_http" -eq 1 ] || echo "PROD_HTTP_PORT=${http_port}" >> "$tmp_file"
  [ "$seen_https" -eq 1 ] || echo "PROD_HTTPS_PORT=${https_port}" >> "$tmp_file"
  [ "$seen_mqtt" -eq 1 ] || echo "PROD_MQTT_PORT=${mqtt_port}" >> "$tmp_file"
  [ "$seen_data" -eq 1 ] || echo "PROD_DATA_DIR=${data_dir}" >> "$tmp_file"
  [ "$seen_disable_mosquitto" -eq 1 ] || echo "DISABLE_MOSQUITTO=${disable_mosquitto}" >> "$tmp_file"

  mv "$tmp_file" "$env_file"
}

prompt_for_port() {
  local label="$1"
  local current="$2"
  local prompt_default="$3"

  while true; do
    if [ -n "$prompt_default" ] && [ "$prompt_default" != "$current" ]; then
      read -p "   ${label} port [${prompt_default}] (current ${current}): " selected
      selected=${selected:-$prompt_default}
    else
      read -p "   ${label} port [${current}]: " selected
      selected=${selected:-$current}
    fi

    if ! is_valid_port "$selected"; then
      warn "Invalid port '${selected}'. Enter a value between 1 and 65535."
      continue
    fi

    is_port_in_use "$selected"
    local rc=$?
    if [ "$rc" -eq 0 ]; then
      warn "Port ${selected} is in use."
      local details
      details=$(port_in_use_details "$selected")
      [ -n "$details" ] && echo "     ${details}"
      if confirm "Use ${selected} anyway? (start will fail if still occupied)"; then
        echo "$selected"
        return 0
      fi
      continue
    fi
    if [ "$rc" -eq 2 ]; then
      warn "Port detection unavailable on this host. Proceeding with chosen value."
    fi

    echo "$selected"
    return 0
  done
}

preflight_validate_prod_ports() {
  local http_port="${PROD_HTTP_PORT:-80}"
  local https_port="${PROD_HTTPS_PORT:-443}"
  local mqtt_port=""
  if ! is_true "${DISABLE_MOSQUITTO:-false}"; then
    mqtt_port="${PROD_MQTT_PORT:-1883}"
  fi
  local failed=0

  info "Preflight: validating configured ports are free..."
  local ports_to_check=("$http_port" "$https_port")
  [ -n "$mqtt_port" ] && ports_to_check+=("$mqtt_port")
  for port in "${ports_to_check[@]}"; do
    if is_port_in_use "$port"; then
      err "Port ${port} is in use."
      local details
      details=$(port_in_use_details "$port")
      [ -n "$details" ] && echo "   ${details}"
      failed=1
    fi
  done

  if [ "$failed" -eq 1 ]; then
    echo ""
    echo "   Remediation:"
    echo "     • Stop the process using the conflicting port(s)"
    echo "     • Or run ./manage.sh setup and re-negotiate ports"
    echo "     • Then re-run this command"
    return 1
  fi

  log "Preflight port validation passed."
  return 0
}

# Check config.json for placeholder values
check_config_placeholders() {
  local cfg="${1:-$PROD_DATA/config.json}"
  if [ -f "$cfg" ]; then
    if grep -qE 'your-username|your-password|your-secret|example\.com|changeme' "$cfg" 2>/dev/null; then
      warn "config.json contains placeholder values."
      warn "Edit ${cfg} and replace placeholder values before deploying."
    fi
  fi
}

# Verify the running container is actually healthy
verify_health() {
  local container="corescope-prod"
  local use_https=false

  # Check if Caddyfile has a real domain (not :80)
  if [ -f caddy-config/Caddyfile ]; then
    local caddyfile_domain
    caddyfile_domain=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
    if [ "$caddyfile_domain" != ":80" ] && [ -n "$caddyfile_domain" ]; then
      use_https=true
    fi
  fi

  # Wait for /api/stats response (Go backend loads packets into memory — may take 60s+)
  info "Waiting for server to respond..."
  local healthy=false
  for i in $(seq 1 45); do
    if docker exec "$container" wget -qO- http://localhost:3000/api/stats &>/dev/null; then
      healthy=true
      break
    fi
    sleep 2
  done

  if ! $healthy; then
    err "Server did not respond after 90 seconds."
    warn "Check logs: ./manage.sh logs"
    return 1
  fi
  log "Server is responding."

  # Check for MQTT errors in recent logs
  local mqtt_errors
  mqtt_errors=$(docker logs "$container" --tail 50 2>&1 | grep -i 'mqtt.*error\|mqtt.*fail\|ECONNREFUSED.*1883' || true)
  if [ -n "$mqtt_errors" ]; then
    warn "MQTT errors detected in logs:"
    echo "$mqtt_errors" | head -5 | sed 's/^/   /'
  fi

  # If HTTPS domain configured, try to verify externally
  if $use_https; then
    info "Checking HTTPS for ${caddyfile_domain}..."
    if command -v curl &>/dev/null; then
      if curl -sf --connect-timeout 5 "https://${caddyfile_domain}/api/stats" &>/dev/null; then
        log "HTTPS is working: https://${caddyfile_domain}"
      else
        warn "HTTPS not reachable yet for ${caddyfile_domain}"
        warn "It may take a minute for Caddy to provision the certificate."
      fi
    fi
  fi

  return 0
}

# ─── Setup Wizard ─────────────────────────────────────────────────────────

TOTAL_STEPS=6

cmd_setup() {
  echo ""
  echo "═══════════════════════════════════════"
  echo "  CoreScope Setup"
  echo "═══════════════════════════════════════"
  echo ""

  if [ -f "$STATE_FILE" ]; then
    info "Resuming previous setup. Delete ${STATE_FILE} to start over."
    echo ""
  fi

  # ── Step 1: Check Docker ──
  step 1 "Checking Docker"

  if ! command -v docker &> /dev/null; then
    err "Docker is not installed."
    echo ""
    echo "   Install it:"
    echo "     curl -fsSL https://get.docker.com | sh"
    echo "     sudo usermod -aG docker \$USER"
    echo ""
    echo "   Then log out, log back in, and run ./manage.sh setup again."
    exit 1
  fi

  # Check if user can actually run Docker
  if ! docker info &> /dev/null; then
    err "Docker is installed but your user can't run it."
    echo ""
    echo "   Fix: sudo usermod -aG docker \$USER"
    echo "   Then log out, log back in, and try again."
    exit 1
  fi

  log "Docker $(docker --version | grep -oP 'version \K[^ ,]+')"
  log "Compose: $DC"

  # Default to latest release tag (instead of staying on master)
  if ! is_done "version_pin"; then
    git fetch origin --tags --force 2>/dev/null || true
    local latest_tag
    latest_tag=$(git tag -l 'v*' --sort=-v:refname | head -1)
    if [ -n "$latest_tag" ]; then
      local current_ref
      current_ref=$(git describe --tags --exact-match 2>/dev/null || echo "")
      if [ "$current_ref" != "$latest_tag" ]; then
        info "Pinning to latest release: ${latest_tag}"
        git checkout "$latest_tag" 2>/dev/null
      else
        log "Already on latest release: ${latest_tag}"
      fi
    fi
    mark_done "version_pin"
  fi
  
  mark_done "docker"

  # ── Step 2: Config ──
  step 2 "Configuration"

  if [ -f "$PROD_DATA/config.json" ]; then
    log "config.json found in data directory."
    # Sanity check the JSON
    if ! python3 -c "import json; json.load(open('$PROD_DATA/config.json'))" 2>/dev/null && \
       ! node -e "JSON.parse(require('fs').readFileSync('$PROD_DATA/config.json'))" 2>/dev/null; then
      err "config.json has invalid JSON. Fix it and re-run setup."
      exit 1
    fi
    log "config.json is valid JSON."
    check_config_placeholders "$PROD_DATA/config.json"
  elif [ -f config.json ]; then
    # Legacy: config in repo root — move it to data dir
    info "Found config.json in repo root — moving to data directory..."
    mkdir -p "$PROD_DATA"
    cp config.json "$PROD_DATA/config.json"
    log "Config moved to ${PROD_DATA}/config.json"
    check_config_placeholders "$PROD_DATA/config.json"
  else
    info "Creating config.json in data directory from example..."
    mkdir -p "$PROD_DATA"
    cp config.example.json "$PROD_DATA/config.json"

    # Generate a random API key
    if command -v openssl &> /dev/null; then
      API_KEY=$(openssl rand -hex 16)
    else
      API_KEY=$(head -c 32 /dev/urandom | xxd -p | head -c 32)
    fi
    # Replace the placeholder API key
    if command -v sed &> /dev/null; then
      sed -i "s/your-secret-api-key-here/${API_KEY}/" "$PROD_DATA/config.json"
    fi

    log "Created config.json with random API key."
    check_config_placeholders "$PROD_DATA/config.json"
    echo ""
    echo "   Config saved to: ${PROD_DATA}/config.json"
    echo "   Edit with: nano ${PROD_DATA}/config.json"
    echo ""
  fi
  mark_done "config"

  # ── Step 3: Ports & Networking ──
  step 3 "Ports & Networking"

  local default_http=80
  local default_https=443
  local default_mqtt=1883
  local selected_http="$default_http"
  local selected_https="$default_https"
  local selected_mqtt="$default_mqtt"
  local selected_disable_mosquitto="${DISABLE_MOSQUITTO:-false}"
  local selected_data_dir="${PROD_DATA_DIR:-$HOME/meshcore-data}"

  local env_http=""
  local env_https=""
  local env_mqtt=""
  local env_disable_mosquitto=""
  local env_data_dir=""

  if [ -f .env ]; then
    env_http=$(get_env_value "PROD_HTTP_PORT" ".env")
    env_https=$(get_env_value "PROD_HTTPS_PORT" ".env")
    env_mqtt=$(get_env_value "PROD_MQTT_PORT" ".env")
    env_disable_mosquitto=$(get_env_value "DISABLE_MOSQUITTO" ".env")
    env_data_dir=$(get_env_value "PROD_DATA_DIR" ".env")
    env_data_dir="${env_data_dir/#\~/$HOME}"
    [ -n "$env_data_dir" ] && selected_data_dir="$env_data_dir"
    [ -n "$env_disable_mosquitto" ] && selected_disable_mosquitto="$env_disable_mosquitto"
    show_env_port_summary "${env_http:-<unset>}" "${env_https:-<unset>}" "${env_mqtt:-<unset>}" "${env_data_dir:-<unset>}" "${env_disable_mosquitto:-<unset>}"
  else
    info ".env not found. It will be created from .env.example."
  fi

  local has_current_ports=false
  if is_true "$selected_disable_mosquitto"; then
    if is_valid_port "$env_http" && is_valid_port "$env_https"; then
      has_current_ports=true
    fi
  elif is_valid_port "$env_http" && is_valid_port "$env_https" && is_valid_port "$env_mqtt"; then
    has_current_ports=true
  fi

  local renegotiate=true
  if [ -f .env ] && $has_current_ports; then
    if confirm "Keep current ports from .env?"; then
      renegotiate=false
      selected_http="$env_http"
      selected_https="$env_https"
      if is_valid_port "$env_mqtt"; then
        selected_mqtt="$env_mqtt"
      fi
      log "Keeping current ports from .env."
    fi
  fi

  if $renegotiate; then
    resolve_port_check_method
    if [ "$PORT_CHECK_METHOD" = "none" ]; then
      warn "No supported port detection tool found (ss/lsof/netstat/nc)."
      warn "You'll still be prompted, but conflicts cannot be detected now."
    else
      info "Detecting listeners using ${PORT_CHECK_METHOD}..."
    fi

    local suggested_http="$default_http"
    local suggested_https="$default_https"
    local suggested_mqtt="$default_mqtt"

    if is_port_in_use "$default_http"; then
      warn "Port ${default_http} is in use."
      local details_http
      details_http=$(port_in_use_details "$default_http")
      [ -n "$details_http" ] && echo "     ${details_http}"
      suggested_http=$(find_next_available_port "$default_http")
      [ -n "$suggested_http" ] && info "Suggested HTTP port: ${suggested_http}"
    fi

    if is_port_in_use "$default_https"; then
      warn "Port ${default_https} is in use."
      local details_https
      details_https=$(port_in_use_details "$default_https")
      [ -n "$details_https" ] && echo "     ${details_https}"
      suggested_https=$(find_next_available_port "$default_https")
      [ -n "$suggested_https" ] && info "Suggested HTTPS port: ${suggested_https}"
    fi

    selected_http=$(prompt_for_port "HTTP" "$default_http" "$suggested_http")
    selected_https=$(prompt_for_port "HTTPS" "$default_https" "$suggested_https")

    if confirm_yes_default "Use built-in MQTT broker?"; then
      selected_disable_mosquitto="false"
      if is_port_in_use "$default_mqtt"; then
        warn "Port ${default_mqtt} is in use."
        local details_mqtt
        details_mqtt=$(port_in_use_details "$default_mqtt")
        [ -n "$details_mqtt" ] && echo "     ${details_mqtt}"
        suggested_mqtt=$(find_next_available_port "$default_mqtt")
        [ -n "$suggested_mqtt" ] && info "Suggested MQTT port: ${suggested_mqtt}"
      fi
      selected_mqtt=$(prompt_for_port "MQTT" "$default_mqtt" "$suggested_mqtt")
    else
      selected_disable_mosquitto="true"
      log "Internal MQTT broker disabled."
    fi
  fi

  if [ -f caddy-config/Caddyfile ]; then
    EXISTING_DOMAIN=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
    if [ "$EXISTING_DOMAIN" = ":80" ] || [ "$EXISTING_DOMAIN" = ":${selected_http}" ]; then
      log "Caddyfile exists (HTTP only, no HTTPS)."
    else
      log "Caddyfile exists for ${EXISTING_DOMAIN}"
    fi
  else
    mkdir -p caddy-config
    echo ""
    echo "   How should the analyzer be accessed?"
    echo ""
    echo "   1) Direct with built-in HTTPS — Caddy auto-provisions a TLS cert"
    echo "      (requires ports 80 + 443 open, and a domain pointed at this server)"
    echo ""
    echo "   2) Behind my own reverse proxy — HTTP only"
    echo "      (for Cloudflare Tunnel, nginx, Traefik, etc.)"
    echo ""
    read -p "   Choose [1/2]: " -n 1 -r
    echo ""

    case $REPLY in
      1)
        read -p "   Enter your domain (e.g., analyzer.example.com): " DOMAIN
        if [ -z "$DOMAIN" ]; then
          err "No domain entered. Re-run setup to try again."
          exit 1
        fi

        echo "${DOMAIN} {
    reverse_proxy localhost:3000
}" > caddy-config/Caddyfile
        log "Caddyfile created for ${DOMAIN}"

        # Validate DNS
        info "Checking DNS..."
        RESOLVED_IP=$(resolve_domain_ipv4 "$DOMAIN")
        MY_IP=$(curl -s -4 ifconfig.me 2>/dev/null || curl -s -4 icanhazip.com 2>/dev/null || echo "unknown")

        if [ -z "$RESOLVED_IP" ]; then
          if has_dns_resolution_tool; then
            warn "${DOMAIN} doesn't resolve yet."
            warn "Create an A record pointing to ${MY_IP}"
            warn "HTTPS won't work until DNS propagates (1-60 min)."
          else
            warn "DNS tool not found; skipping domain resolution check."
          fi
          echo ""
          if ! confirm "Continue anyway?"; then
            echo "   Run ./manage.sh setup again when DNS is ready."
            exit 0
          fi
        elif [ "$RESOLVED_IP" = "$MY_IP" ]; then
          log "DNS resolves correctly: ${DOMAIN} → ${MY_IP}"
        else
          warn "${DOMAIN} resolves to ${RESOLVED_IP} but this server is ${MY_IP}"
          warn "HTTPS provisioning will fail if the domain doesn't point here."
          if ! confirm "Continue anyway?"; then
            echo "   Fix DNS and run ./manage.sh setup again."
            exit 0
          fi
        fi
        ;;
      2)
        echo ":${selected_http} {
    reverse_proxy localhost:3000
}" > caddy-config/Caddyfile
        log "Caddyfile created (HTTP only on port ${selected_http})."
        echo "   Point your reverse proxy or tunnel to this server's port ${selected_http}."
        ;;
      *)
        warn "Invalid choice. Defaulting to HTTP only."
        echo ":${selected_http} {
    reverse_proxy localhost:3000
}" > caddy-config/Caddyfile
        ;;
    esac
  fi

  write_env_managed_values "$selected_http" "$selected_https" "$selected_mqtt" "$selected_data_dir" "$selected_disable_mosquitto"
  log "Saved negotiated ports to .env"
  show_env_port_summary "$selected_http" "$selected_https" "$selected_mqtt" "$selected_data_dir" "$selected_disable_mosquitto"

  echo "   Resolved port mapping:"
  echo "     UI HTTP:  ${selected_http}"
  echo "     UI HTTPS: ${selected_https}"
  if is_true "$selected_disable_mosquitto"; then
    echo "     MQTT:     disabled (external broker)"
  else
    echo "     MQTT:     ${selected_mqtt}"
  fi
  echo ""
  if ! confirm "Proceed to build/start with these ports?"; then
    echo "   Setup cancelled. Re-run ./manage.sh setup when ready."
    exit 0
  fi

  export PROD_HTTP_PORT="$selected_http"
  export PROD_HTTPS_PORT="$selected_https"
  export PROD_MQTT_PORT="$selected_mqtt"
  export DISABLE_MOSQUITTO="$selected_disable_mosquitto"
  export PROD_DATA_DIR="$selected_data_dir"
  PROD_DATA="$PROD_DATA_DIR"
  mark_done "caddyfile"

  # ── Step 4: Build ──
  step 4 "Building Docker image"

  # Check if image exists and source hasn't changed
  IMAGE_EXISTS=$(docker images -q "$IMAGE_NAME" 2>/dev/null)
  if [ -n "$IMAGE_EXISTS" ] && is_done "build"; then
    log "Image already built."
    if confirm "Rebuild? (only needed if you updated the code)"; then
      dc_prod_base build prod
      log "Image rebuilt."
    fi
  else
    info "This takes 1-2 minutes the first time..."
    dc_prod_base build prod
    log "Image built."
  fi
  mark_done "build"

  choose_storage || return 1

  # ── Step 5: Start container ──
  step 5 "Starting container"

  if docker ps --format '{{.Names}}' | grep -q "^corescope-prod$"; then
    info "Production container already running — skipping preflight port check."
  else
    if ! preflight_validate_prod_ports; then
      exit 1
    fi
  fi

  local backend
  backend=$(selected_backend prod) || return 1
  dc_prod_base stop prod || return 1
  if [ "$backend" = postgres ]; then
    require_database_credentials || return 1
    dc_postgres prod up -d --wait postgres || return 1
    dc_postgres prod run --rm --no-deps --entrypoint /app/storage.sh bootstrap setup -backend=postgres || return 1
  else
    storage_action prod setup -backend=sqlite || return 1
  fi
  dc_prod up -d prod || return 1
  log "Validated storage and application started."
  mark_done "container"

  # ── Step 6: Verify ──
  step 6 "Verifying"

  if docker ps --format '{{.Names}}' | grep -q "^corescope-prod$"; then
    verify_health

    CADDYFILE_DOMAIN=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')

    echo ""
    echo "═══════════════════════════════════════"
    echo "  Setup complete!"
    echo "═══════════════════════════════════════"
    echo ""
    if [ "$CADDYFILE_DOMAIN" != ":80" ] && [ -n "$CADDYFILE_DOMAIN" ]; then
      echo "   🌐 https://${CADDYFILE_DOMAIN}"
    else
      MY_IP=$(curl -s -4 ifconfig.me 2>/dev/null || echo "your-server-ip")
      echo "   🌐 http://${MY_IP}"
    fi
    echo ""
    echo "   Next steps:"
    echo "   • Connect an observer to start receiving packets"
    echo "   • Customize branding in config.json"
    echo "   • Set up backups: ./manage.sh backup"
    echo ""
    echo "   Useful commands:"
    echo "     ./manage.sh status     Check health"
    echo "     ./manage.sh logs       View logs"
    echo "     ./manage.sh backup     Full backup (DB + config + theme)"
    echo "     ./manage.sh update     Update to latest version"
    echo ""
  else
    err "Container failed to start."
    echo ""
    echo "   Check what went wrong:"
    echo "     $DC logs prod"
    echo ""
    echo "   Common fixes:"
    echo "     • Invalid config.json — check JSON syntax"
    echo "     • Port conflict — stop other web servers"
    echo "     • Re-run: ./manage.sh setup"
    echo ""
    exit 1
  fi

  mark_done "verify"
}

# ─── Staging Helpers ──────────────────────────────────────────────────────

# Copy telemetry into an empty staging database; accounts stay independent.
prepare_staging_db() {
  local backend state path destination copy_dir attempt contents
  mkdir -p "$STAGING_DATA"
  if container_running "$STAGING_CONTAINER"; then info "Keeping running staging data."; return 0; fi
  state=$(storage_field staging state) || return 1
  if [ "$state" = ready ]; then info "Keeping recorded staging storage; automatic replacement is refused."; return 0; fi
  [ "$state" = unrecorded ] || { err "Staging recovery must finish before cloning data."; return 1; }
  contents=$(find "$STAGING_DATA" -mindepth 1 -maxdepth 1 ! -name config.json ! -name theme.json ! -name Caddyfile ! -name .env -print -quit)
  if [ -n "$contents" ]; then info "Keeping existing staging files; packaged setup will validate them before startup."; return 0; fi
  backend=$(selected_backend prod) || return 1
  prepare_staging_config
  copy_dir=$(mktemp -d)
  chmod 700 "$copy_dir"
  if [ "$backend" = sqlite ]; then
    path=$(storage_field prod telemetry.sqlite_path) || return 1
    case "$path" in /app/data/*) ;; *) err "Managed staging requires telemetry inside the persistent data mount."; return 1 ;; esac
    destination=$(realpath -m -- "$STAGING_DATA/${path#/app/data/}") || return 1
    case "$destination" in "$(realpath -m -- "$STAGING_DATA")"/*) ;; *) err "Staging target escaped its data directory."; return 1 ;; esac
    [ ! -e "$destination" ] || { err "Staging telemetry already exists."; return 1; }
    sqlite_dump_file prod telemetry "$copy_dir/telemetry.db" || return 1
    mkdir -p "$(dirname "$destination")"
    cp -- "$copy_dir/telemetry.db" "$destination" || return 1
    chmod 600 "$destination"
    # Only telemetry is cloned. A new account target is independently initialized.
    storage_action staging setup -backend=sqlite -sqlite-path "$path" -users-sqlite-path /app/data/users.db || return 1
  else
    # Explicit fresh staging only: start the local database, never bootstrap its
    # schema before checking emptiness and restoring the native telemetry dump.
    dc_postgres staging up -d postgres
    for attempt in $(seq 1 30); do
      if pg_container_exec staging corescope_telemetry psql -X -A -t -c 'SELECT 1' >/dev/null 2>&1; then break; fi
      [ "$attempt" -lt 30 ] || { err "Staging PostgreSQL did not become ready"; return 1; }
      sleep 1
    done
    if ! pg_empty staging corescope_telemetry fresh-staging; then
      info "Keeping existing staging PostgreSQL data; validating its adoption before startup."
      rmdir "$copy_dir"
      dc_postgres staging run --rm bootstrap
      return $?
    fi
    pg_dump_file prod corescope_telemetry "$copy_dir/telemetry.dump" || return 1
    pg_restore_file staging corescope_telemetry "$copy_dir/telemetry.dump" fresh-staging || return 1
    dc_postgres staging run --rm bootstrap || return 1
  fi
  rm -f -- "$copy_dir/telemetry.db" "$copy_dir/telemetry.dump"
  rmdir -- "$copy_dir"
  log "Telemetry cloned into fresh staging storage. Staging accounts remain independent."
}

# Copy config.prod.json → config.staging.json with siteName change
prepare_staging_config() {
  local prod_config="$PROD_DATA/config.json"
  local staging_config="$STAGING_DATA/config.json"
  mkdir -p "$STAGING_DATA"

  # Docker may have created config.json as a directory
  [ -d "$staging_config" ] && rmdir "$staging_config" 2>/dev/null || true

  if [ ! -f "$prod_config" ]; then
    warn "No production config at ${prod_config} — staging may use defaults."
    return
  fi
  info "Copying production config to staging..."
  cp "$prod_config" "$staging_config"
  sed -i 's/"siteName":\s*"[^"]*"/"siteName": "CoreScope — STAGING"/' "$staging_config"
  log "Staging config created at ${staging_config} with STAGING site name."
  # Copy Caddyfile for staging (HTTP-only on staging port)
  local staging_caddy="$STAGING_DATA/Caddyfile"
  if [ ! -f "$staging_caddy" ]; then
    info "Creating staging Caddyfile (HTTP-only on port ${STAGING_GO_HTTP_PORT:-82})..."
    echo ":${STAGING_GO_HTTP_PORT:-82} {" > "$staging_caddy"
    echo "    reverse_proxy localhost:3000" >> "$staging_caddy"
    echo "}" >> "$staging_caddy"
    log "Staging Caddyfile created at ${staging_caddy}"
  fi
}

# Check if a container is running by name
container_running() {
  docker ps --format '{{.Names}}' | grep -q "^${1}$"
}

# Get health status of a container
container_health() {
  docker inspect "$1" --format '{{.State.Health.Status}}' 2>/dev/null || echo "unknown"
}

# ─── Start / Stop / Restart ──────────────────────────────────────────────

# Migrate legacy root config.json to data directory path
migrate_config() {
  local mode="${1:-interactive}"
  local legacy_config="./config.json"
  local target_config="$PROD_DATA/config.json"
  local reply=""

  mkdir -p "$PROD_DATA"

  if [ -f "$target_config" ]; then
    return 0
  fi

  if [ ! -f "$legacy_config" ]; then
    return 0
  fi

  if [ "$mode" = "auto" ]; then
    echo "→ Migrating config.json from repo root to ${PROD_DATA}/config.json..."
    cp "$legacy_config" "$target_config"
    echo "✓ Config migrated."
    return 0
  fi

  echo ""
  echo "Found legacy config location:"
  echo "  Source: ./config.json (repo root)"
  echo "  Destination: ${PROD_DATA}/config.json"
  echo "  Note: CoreScope reads config from the data directory at runtime."
  read -p "Move to ${PROD_DATA}/config.json? [Y/n] " reply

  if [ -z "$reply" ] || [[ "$reply" =~ ^[Yy]$ ]]; then
    cp "$legacy_config" "$target_config"
    log "Copied config.json to ${target_config}"
    return 0
  fi

  warn "Migration aborted."
  echo "Move ./config.json to ${PROD_DATA}/config.json, then run this command again."
  return 1
}

# Ensure config.json exists in the data directory before starting
ensure_config() {
  local data_dir="$1"
  local config="$data_dir/config.json"
  mkdir -p "$data_dir"

  # Docker may have created config.json as a directory from a prior failed mount
  [ -d "$config" ] && rmdir "$config" 2>/dev/null || true

  if [ -f "$config" ]; then
    return 0
  fi

  # Try to copy from repo root (legacy location)
  if [ -f ./config.json ]; then
    info "No config in data directory — copying from ./config.json"
    cp ./config.json "$config"
    return 0
  fi

  # Prompt admin
  echo ""
  warn "No config.json found in ${data_dir}/"
  echo ""
  echo "   CoreScope needs a config.json to connect to MQTT brokers."
  echo ""
  echo "   Options:"
  echo "     1) Create from example (you'll edit MQTT settings after)"
  echo "     2) I'll put one there myself (abort for now)"
  echo ""
  read -p "   Choose [1/2]: " -n 1 -r
  echo ""

  case $REPLY in
    1)
      cp config.example.json "$config"
      # Generate a random API key
      if command -v openssl &>/dev/null; then
        API_KEY=$(openssl rand -hex 16)
      else
        API_KEY=$(head -c 32 /dev/urandom | xxd -p | head -c 32)
      fi
      sed -i "s/your-secret-api-key-here/${API_KEY}/" "$config" 2>/dev/null || true
      log "Created ${config} from example with random API key."
      warn "Edit MQTT settings before connecting observers:"
      echo "     nano ${config}"
      echo ""
      ;;
    *)
      echo "   Place your config.json at: ${config}"
      echo "   Then run this command again."
      exit 0
      ;;
  esac
}

cmd_start() {
  local WITH_STAGING=false
  if [ "$1" = "--with-staging" ]; then
    WITH_STAGING=true
  fi

  if docker ps --format '{{.Names}}' | grep -q "^corescope-prod$"; then
    info "Production container already running — skipping preflight port check."
  else
    if ! preflight_validate_prod_ports; then
      exit 1
    fi
  fi

  migrate_config || exit 1
  # Always check prod config
  ensure_config "$PROD_DATA"

  # Compose completes PostgreSQL/bootstrap dependencies before staging copies data.
  info "Starting production container (corescope-prod) on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443}..."
  dc_prod up -d prod

  if $WITH_STAGING; then
    # Prepare staging data and config
    prepare_staging_db
    prepare_staging_config

    info "Starting staging container (${STAGING_CONTAINER}) on port ${STAGING_GO_HTTP_PORT:-82}..."
    dc_staging up -d staging-go
    if is_true "${DISABLE_MOSQUITTO:-false}"; then
      log "Production started on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443} (MQTT disabled)"
      log "Staging started on port ${STAGING_GO_HTTP_PORT:-82} (MQTT disabled)"
    else
      log "Production started on ports ${PROD_HTTP_PORT:-80}/${PROD_HTTPS_PORT:-443}/${PROD_MQTT_PORT:-1883}"
      log "Staging started on port ${STAGING_GO_HTTP_PORT:-82} (MQTT: ${STAGING_GO_MQTT_PORT:-1885})"
    fi
  else
    log "Production started. Staging NOT running (use --with-staging to start both)."
  fi
}

cmd_stop() {
  local TARGET="${1:-all}"

  case "$TARGET" in
    prod)
      info "Stopping production container (corescope-prod)..."
      dc_prod_base stop prod
      log "Production stopped."
      ;;
    staging)
      info "Stopping staging container (${STAGING_CONTAINER})..."
      dc_staging_base rm -sf staging-go 2>/dev/null || true
      docker rm -f "$STAGING_CONTAINER" meshcore-staging-go corescope-staging meshcore-staging 2>/dev/null || true
      log "Staging stopped and cleaned up."
      ;;
    all)
      info "Stopping all containers..."
      dc_prod_base stop prod
      dc_staging_base rm -sf staging-go 2>/dev/null || true
      docker rm -f "$STAGING_CONTAINER" meshcore-staging-go corescope-staging meshcore-staging 2>/dev/null || true
      log "All containers stopped."
      ;;
    *)
      err "Usage: ./manage.sh stop [prod|staging|all]"
      exit 1
      ;;
  esac
}

cmd_restart() {
  local TARGET="${1:-prod}"
  case "$TARGET" in
    prod)
      info "Restarting production container (corescope-prod)..."
      dc_prod up -d --force-recreate prod
      log "Production restarted."
      ;;
    staging)
      info "Restarting staging container (${STAGING_CONTAINER})..."
      # Stop and remove old container
      dc_staging_base rm -sf staging-go 2>/dev/null || true
      docker rm -f "$STAGING_CONTAINER" 2>/dev/null || true
      # Wait for container to be fully gone and memory to be reclaimed
      # This prevents OOM when old + new containers overlap on small VMs
      for i in $(seq 1 15); do
        if ! docker ps -a --format '{{.Names}}' | grep -q "$STAGING_CONTAINER"; then
          break
        fi
        sleep 1
      done
      sleep 3  # extra pause for OS to reclaim memory
      # Verify config exists before starting
      local staging_config="${STAGING_DATA_DIR:-$HOME/meshcore-staging-data}/config.json"
      if [ ! -f "$staging_config" ]; then
        warn "Staging config not found at $staging_config — creating from prod config..."
        prepare_staging_config
      fi
      dc_staging up -d staging-go
      log "Staging restarted."
      ;;
    all)
      info "Restarting all containers..."
      dc_prod up -d --force-recreate prod
      dc_staging_base rm -sf staging-go 2>/dev/null || true
      docker rm -f "$STAGING_CONTAINER" 2>/dev/null || true
      dc_staging up -d staging-go
      log "All containers restarted."
      ;;
    *)
      err "Usage: ./manage.sh restart [prod|staging|all]"
      exit 1
      ;;
  esac
}

# ─── Status ───────────────────────────────────────────────────────────────

# Show status for a single container (used in compose mode)
show_container_status() {
  local NAME="$1"
  local LABEL="$2"

  if container_running "$NAME"; then
    local health
    health=$(container_health "$NAME")
    log "${LABEL} (${NAME}): Running — Health: ${health}"
    docker ps --filter "name=${NAME}" --format "   Ports:  {{.Ports}}"

    # Server stats
    if docker exec "$NAME" wget -qO /dev/null http://localhost:3000/api/stats 2>/dev/null; then
      local stats packets nodes
      stats=$(docker exec "$NAME" wget -qO- http://localhost:3000/api/stats 2>/dev/null)
      packets=$(echo "$stats" | grep -oP '"totalPackets":\K[0-9]+' 2>/dev/null || echo "?")
      nodes=$(echo "$stats" | grep -oP '"totalNodes":\K[0-9]+' 2>/dev/null || echo "?")
      info "  ${packets} packets, ${nodes} nodes"
    fi
  else
    if docker ps -a --format '{{.Names}}' | grep -q "^${NAME}$"; then
      warn "${LABEL} (${NAME}): Stopped"
    else
      info "${LABEL} (${NAME}): Not running"
    fi
  fi
}

cmd_status() {
  echo ""
  echo "═══════════════════════════════════════"
  echo "  CoreScope Status"
  echo "═══════════════════════════════════════"
  echo ""

  # Version
  local current_version
  current_version=$(git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD 2>/dev/null || echo "unknown")
  info "Version: ${current_version}"
  echo ""

  # Production
  show_container_status "corescope-prod" "Production"
  echo ""

  # Staging
  if container_running "$STAGING_CONTAINER"; then
    show_container_status "$STAGING_CONTAINER" "Staging"
  else
    info "Staging (${STAGING_CONTAINER}): Not running (use --with-staging to start both)"
  fi
  echo ""

  local backend database size target
  storage_action prod status || return 1
  backend=$(selected_backend prod) || return 1
  if [ "$backend" = postgres ]; then
    for database in corescope_telemetry corescope_accounts; do
      if [ "$database" = corescope_accounts ] && [ "$(storage_field prod has_accounts)" = false ]; then continue; fi
      if size=$(pg_exec prod "$database" psql -X -A -t -c 'SELECT pg_size_pretty(pg_database_size(current_database()))' 2>/dev/null); then info "Production $database: $size"; else warn "PostgreSQL size unavailable for $database"; fi
    done
  else
    for database in telemetry accounts; do
      target=$(storage_field prod "$database.sqlite_path") || continue
      if size=$(dc_prod_base run --rm --no-deps --entrypoint /bin/sh prod -c 'stat -c %s "$1"' sh "$target" 2>/dev/null); then info "Production SQLite $database: $size bytes (main file; WAL may be additional)"; fi
    done
  fi

}

# ─── Logs ─────────────────────────────────────────────────────────────────

cmd_logs() {
  local TARGET="${1:-prod}"
  local LINES="${2:-100}"
  case "$TARGET" in
    prod)
      info "Tailing production logs..."
      dc_prod logs -f --tail="$LINES" prod
      ;;
    staging)
      if container_running "$STAGING_CONTAINER"; then
        info "Tailing staging logs..."
        dc_staging logs -f --tail="$LINES" staging-go
      else
        err "Staging container is not running."
        info "Start with: ./manage.sh start --with-staging"
        exit 1
      fi
      ;;
    *)
      err "Usage: ./manage.sh logs [prod|staging] [lines]"
      exit 1
      ;;
  esac
}

# ─── Promote ──────────────────────────────────────────────────────────────

cmd_promote() {
  echo ""
  info "Promotion Flow: Staging → Production"
  echo ""
  echo "This will:"
  echo "  1. Backup current production database"
  echo "  2. Restart production with latest image (same as staging)"
  echo "  3. Wait for health check"
  echo ""

  # Show what's currently running
  local staging_image staging_created prod_image prod_created
  staging_image=$(docker inspect "$STAGING_CONTAINER" --format '{{.Config.Image}}' 2>/dev/null || echo "not running")
  staging_created=$(docker inspect "$STAGING_CONTAINER" --format '{{.Created}}' 2>/dev/null || echo "N/A")
  prod_image=$(docker inspect corescope-prod --format '{{.Config.Image}}' 2>/dev/null || echo "not running")
  prod_created=$(docker inspect corescope-prod --format '{{.Created}}' 2>/dev/null || echo "N/A")

  echo "  Staging: ${staging_image} (created ${staging_created})"
  echo "  Prod:    ${prod_image} (created ${prod_created})"
  echo ""

  if ! confirm "Proceed with promotion?"; then
    echo "   Aborted."
    exit 0
  fi

  local BACKUP_DIR="./backups/pre-promotion-$(date +%Y%m%d-%H%M%S)"
  cmd_backup "$BACKUP_DIR"

  # Restart prod with latest image
  info "Restarting production with latest image..."
  dc_prod up -d --force-recreate prod

  # Wait for health
  info "Waiting for production health check..."
  local i health
  for i in $(seq 1 30); do
    health=$(container_health "corescope-prod")
    if [ "$health" = "healthy" ]; then
      log "Production healthy after ${i}s"
      break
    fi
    if [ "$i" -eq 30 ]; then
      err "Production failed health check after 30s"
      warn "Check logs: ./manage.sh logs prod"
      warn "Native archives are in $BACKUP_DIR. Restore only into a fresh target; see docs/postgresql-upgrade.md."
      exit 1
    fi
    sleep 1
  done

  log "Promotion complete ✓"
  echo ""
  echo "  Production is now running the same image as staging."
  echo "  Backup: ${BACKUP_DIR}/"
  echo ""
}

# ─── Update ───────────────────────────────────────────────────────────────

cmd_update() {
  local version="${1:-}"

  info "Fetching latest changes and tags..."
  git fetch origin --tags --force

  if [ -z "$version" ]; then
    # No arg: checkout latest release tag
    local latest_tag
    latest_tag=$(git tag -l 'v*' --sort=-v:refname | head -1)
    if [ -z "$latest_tag" ]; then
      err "No release tags found. Use './manage.sh update latest' for tip of master."
      exit 1
    fi
    info "Checking out latest release: ${latest_tag}"
    git checkout "$latest_tag" || { err "Failed to checkout tag '${latest_tag}'."; exit 1; }
  elif [ "$version" = "latest" ]; then
    # Explicit opt-in to bleeding edge (tip of master)
    # Note: this creates a detached HEAD at origin/master, which is intentional —
    # we want a read-only snapshot of upstream, not a local tracking branch.
    info "Checking out tip of master (detached HEAD at origin/master)..."
    git checkout origin/master || { err "Failed to checkout origin/master."; exit 1; }
  else
    # Specific tag requested
    if ! git tag -l "$version" | grep -q .; then
      err "Tag '${version}' not found."
      echo ""
      echo "   Available releases:"
      git tag -l 'v*' --sort=-v:refname | head -10 | sed 's/^/     /'
      exit 1
    fi
    info "Checking out version: ${version}"
    git checkout "$version" || { err "Failed to checkout '${version}'."; exit 1; }
  fi

  migrate_config auto

  info "Rebuilding image..."
  dc_prod_base build prod

  info "Restarting with new image..."
  dc_prod up -d --force-recreate prod

  log "Updated and restarted. Data preserved."
  # Show current version
  local current
  current=$(git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD)
  log "Running version: ${current}"
}

# ─── Backup ───────────────────────────────────────────────────────────────

backup_state() {
  local destination="$1" backend="$2" data_root target relative database_root
  local excludes=()
  data_root=$(cd "$PROD_DATA" && pwd -P) || return 1
  destination=$(realpath -m -- "$destination") || return 1
  case "$destination" in "$data_root"|"$data_root"/*) err "Choose a backup directory outside the live data directory."; return 1 ;; esac
  if [ "$backend" = sqlite ]; then
    for target in telemetry accounts; do
      if [ "$target" = accounts ] && [ "$(storage_field prod has_accounts)" = false ]; then continue; fi
      relative=$(storage_field prod "$target.sqlite_path") || return 1
      case "$relative" in
        /app/data/*)
          relative=${relative#/app/data/}
          excludes+=("--exclude=./$relative" "--exclude=./$relative-wal" "--exclude=./$relative-shm" "--exclude=./$relative-journal")
          ;;
      esac
    done
  else
    database_root=$(realpath -m -- "${PROD_POSTGRES_DATA_DIR:-$HOME/corescope-postgres}") || return 1
    case "$database_root" in
      "$data_root") err "PostgreSQL storage and application state must use separate directories."; return 1 ;;
      "$data_root"/*) excludes+=("--exclude=./${database_root#"$data_root"/}") ;;
    esac
  fi
  # Preserve queues, selection/history, settings and custom files as recovery
  # material. The selected live databases are supplied only by native snapshots.
  (
    umask 077
    set -o noclobber
    exec 3>"$destination/state.tar.partial" || exit 1
    trap 'rm -f -- "$destination/state.tar.partial"' EXIT HUP INT TERM
    tar -C "$data_root" "${excludes[@]}" -cf - . >&3 || exit 1
    exec 3>&-
    sync -f "$destination/state.tar.partial" || exit 1
    ln -- "$destination/state.tar.partial" "$destination/state.tar" || exit 1
    rm -- "$destination/state.tar.partial" || exit 1
    trap - EXIT HUP INT TERM
  )
}

cmd_backup() {
  local timestamp backup_dir source name backend status has_accounts
  timestamp=$(date +%Y%m%d-%H%M%S)
  backup_dir="${1:-./backups/corescope-$timestamp}"
  [ -n "$backup_dir" ] || backup_dir="./backups/corescope-$timestamp"
  backend=$(selected_backend prod) || return 1
  [ "$(storage_field prod state)" = ready ] || { err "Complete validated setup before taking a native backup."; return 1; }
  umask 077
  if [ -d "$backup_dir" ] && [ -n "$(find "$backup_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]; then err "Choose a new empty backup directory; prior recovery files were preserved."; return 1; fi
  mkdir -p "$backup_dir"
  chmod 700 "$backup_dir"
  case "$backend" in
    sqlite)
      info "Writing native SQLite snapshots to $backup_dir/"
      sqlite_dump_file prod telemetry "$backup_dir/telemetry.db" || return 1
      if sqlite_source_exists prod accounts; then
        sqlite_dump_file prod accounts "$backup_dir/accounts.db" || return 1
      else
        status=$?
        [ "$status" = 3 ] || { err "Account storage is inaccessible or incomplete; backup refused."; return 1; }
        : > "$backup_dir/accounts.absent"
      fi
      ;;
    postgres)
      info "Writing native PostgreSQL archives to $backup_dir/"
      has_accounts=$(storage_field prod has_accounts) || return 1
      case "$has_accounts" in true|false) ;; *) err "Cannot read the recorded account target."; return 1 ;; esac
      pg_dump_file prod corescope_telemetry "$backup_dir/telemetry.dump" || return 1
      if [ "$has_accounts" = true ]; then
        pg_dump_file prod corescope_accounts "$backup_dir/accounts.dump" || return 1
      else
        : > "$backup_dir/accounts.absent"
      fi
      ;;
  esac
  backup_state "$backup_dir" "$backend" || return 1
  printf '%s\n' "$backend" > "$backup_dir/backend.txt"
  for name in config.json theme.json; do
    source="$PROD_DATA/$name"
    [ -f "$source" ] || source="$name"
    if [ -f "$source" ]; then cp -- "$source" "$backup_dir/$name"; chmod 600 "$backup_dir/$name"; fi
  done
  if [ -f .env ]; then cp -- .env "$backup_dir/compose.env"; chmod 600 "$backup_dir/compose.env"; fi
  if [ -f caddy-config/Caddyfile ]; then cp -- caddy-config/Caddyfile "$backup_dir/Caddyfile"; chmod 600 "$backup_dir/Caddyfile"; fi
  log "Native $backend backups and available configuration saved. Keep the bundle private and encrypted off-host."
}

# ─── Restore ──────────────────────────────────────────────────────────────

stage_backup_state() {
  local bundle="$1" destination="$2" stage
  bundle=$(cd "$bundle" && pwd -P) || return 1
  [ -f "$bundle/state.tar" ] || { err "This restore requires the matching private state archive; use the native recovery guide for older snapshots."; return 1; }
  mkdir -p "$(dirname "$destination")"
  stage=$(mktemp -d "$destination.restore-XXXXXXXX") || return 1
  chmod 700 "$stage"
  # Managed archives contain relative regular files/directories only. Keep an
  # unsupported archive and its staged recovery material for manual inspection.
  tar -tf "$bundle/state.tar" > "$stage/.restore-names" || return 1
  LC_ALL=C tar -tvf "$bundle/state.tar" > "$stage/.restore-types" || return 1
  if ! awk '/^\// || /(^|\/)\.\.(\/|$)/ { bad=1 } END { exit bad }' "$stage/.restore-names" ||
     ! awk 'substr($0,1,1)!="-" && substr($0,1,1)!="d" { bad=1 } END { exit bad }' "$stage/.restore-types"; then
    err "State archive contains unsupported paths or links. Preserve it for manual recovery."
    return 1
  fi
  rm -- "$stage/.restore-names" "$stage/.restore-types"
  tar --no-same-owner --no-same-permissions -xf "$bundle/state.tar" -C "$stage" || return 1
  printf '%s\n' "$stage"
}

restore_sqlite_bundle() {
  local bundle="$1" destination stage state backend kind target relative result device
  bundle=$(cd "$bundle" && pwd -P) || return 1
  [ -f "$bundle/state.tar" ] || { err "This restore requires the matching private state archive; use the native recovery guide for older snapshots."; return 1; }
  if container_running corescope-prod; then err "Stop CoreScope before restoring."; return 1; fi
  destination=$(realpath -m -- "$PROD_DATA") || return 1
  if [ -L "$PROD_DATA" ] || { [ -d "$destination" ] && [ -n "$(find "$destination" -mindepth 1 -maxdepth 1 -print -quit)" ]; }; then
    err "SQLite restore requires a new empty state directory. Preserve the current directory and select a fresh PROD_DATA_DIR."
    return 1
  fi
  if ! confirm "Restore both native SQLite snapshots and state into this empty directory?"; then return 0; fi
  stage=$(stage_backup_state "$bundle" "$destination") || return 1
  state=$(storage_at prod "$stage" field state) || return 1
  backend=$(storage_at prod "$stage" field backend) || return 1
  [ "$state" = ready ] && [ "$backend" = sqlite ] || { err "State archive is not a complete SQLite installation snapshot."; return 1; }
  if [ "$(storage_at prod "$stage" field has_accounts)" = true ] && [ -e "$bundle/accounts.absent" ]; then
    err "The selection records initialized accounts; an absence marker cannot replace their native snapshot."
    return 1
  fi
  for kind in telemetry accounts; do
    if [ "$kind" = accounts ] && [ "$(storage_at prod "$stage" field has_accounts)" = false ]; then
      [ -f "$bundle/accounts.absent" ] && [ ! -e "$bundle/accounts.db" ] || { err "Account backup and selection disagree."; return 1; }
      continue
    fi
    target=$(storage_at prod "$stage" field "$kind.sqlite_path") || return 1
    case "$target" in /app/data/*) ;; *) err "Managed restore requires SQLite targets inside the persistent data mount."; return 1 ;; esac
    relative=${target#/app/data/}
    target=$(realpath -m -- "$stage/$relative") || return 1
    case "$target" in "$stage"/*) ;; *) err "SQLite target escaped the staged state directory."; return 1 ;; esac
    [ ! -e "$target" ] && [ ! -e "$target-wal" ] && [ ! -e "$target-shm" ] || { err "State archive already contains a selected database; native snapshot restore was refused."; return 1; }
    if [ "$kind" = accounts ] && [ -f "$bundle/accounts.absent" ]; then
      [ ! -e "$bundle/accounts.db" ] || { err "Account backup metadata is inconsistent."; return 1; }
      continue
    fi
    [ -f "$bundle/$kind.db" ] && [ "$(head -c 15 "$bundle/$kind.db")" = 'SQLite format 3' ] || { err "A native SQLite snapshot is missing or invalid."; return 1; }
    result=$(dc_prod_base run --rm --no-deps -v "$bundle:/backup:ro" --entrypoint sqlite3 prod -readonly "/backup/$kind.db" 'PRAGMA quick_check') || return 1
    [ "$result" = ok ] || { err "SQLite snapshot integrity check failed."; return 1; }
    mkdir -p "$(dirname "$target")"
    cp -- "$bundle/$kind.db" "$target" || return 1
    chmod 600 "$target"
  done
  # Validate both schemas on the staged mount before publishing any restored
  # selection or starting a process. This also handles supported old schemas.
  storage_at prod "$stage" setup -backend=sqlite || return 1
  device=$(stat -c %d "$stage") || return 1
  [ ! -e "$destination" ] || { [ -d "$destination" ] && [ "$(stat -c %d "$destination")" = "$device" ] && [ -z "$(find "$destination" -mindepth 1 -maxdepth 1 -print -quit)" ]; } || {
    err "Restore destination changed or is a separate mount; staged recovery remains at $stage."
    return 1
  }
  mv -T -- "$stage" "$destination" || { err "Restore publication failed; preserve staged recovery at $stage."; return 1; }
  log "SQLite state restored. Services remain stopped: review restored accounts, sessions and tokens before start."
}

cmd_restore() {
  local bundle="$1" file source destination stamp has_accounts stage database
  local databases=(corescope_telemetry)
  if [ ! -d "$bundle" ]; then err "Usage: ./manage.sh restore <native-backup-directory>; see docs/storage.md for older standalone snapshots."; return 1; fi
  local backend
  backend=$(cat "$bundle/backend.txt" 2>/dev/null || printf postgres)
  case "$backend" in
    sqlite) restore_sqlite_bundle "$bundle"; return $? ;;
    postgres) ;;
    *) err "Unknown backup backend."; return 1 ;;
  esac
  if container_running corescope-prod; then err "Stop CoreScope before restoring; leave target PostgreSQL running."; return 1; fi
  require_managed_postgres prod || return 1
  has_accounts=$(storage_field prod has_accounts) || return 1
  case "$has_accounts" in
    true)
      [ ! -e "$bundle/accounts.absent" ] || { err "The selection records initialized accounts; their native archive is required."; return 1; }
      databases+=(corescope_accounts)
      ;;
    false)
      [ -f "$bundle/accounts.absent" ] && [ ! -e "$bundle/accounts.dump" ] || { err "Account backup and selection disagree."; return 1; }
      destination=$(realpath -m -- "$PROD_DATA") || return 1
      stage=$(stage_backup_state "$bundle" "$destination") || return 1
      storage_at prod "$stage" managed-postgres || return 1
      [ "$(storage_at prod "$stage" field has_accounts)" = false ] || { err "Account absence marker disagrees with the retained backup selection."; return 1; }
      ;;
    *) err "Cannot read the recorded account target."; return 1 ;;
  esac
  for database in "${databases[@]}"; do
    file="${database#corescope_}.dump"
    if [ ! -f "$bundle/$file" ] || [ "$(head -c 5 "$bundle/$file")" != PGDMP ]; then err "Backup must contain a native $file; SQLite files are not accepted."; return 1; fi
  done
  for database in "${databases[@]}"; do
    if ! pg_empty prod "$database"; then err "Restore requires empty PostgreSQL databases. Preserve the current cluster and configure a fresh destination."; return 1; fi
  done
  if ! confirm "Restore the selected native archives into these empty databases?"; then return 0; fi
  for database in "${databases[@]}"; do
    pg_restore_file prod "$database" "$bundle/${database#corescope_}.dump" || return 1
  done
  mkdir -p "$PROD_DATA" caddy-config
  stamp="$(date +%Y%m%d-%H%M%S)-$$"
  for file in config.json theme.json Caddyfile; do
    source="$bundle/$file"
    [ -f "$source" ] || continue
    destination="$PROD_DATA/$file"
    [ "$file" != Caddyfile ] || destination=caddy-config/Caddyfile
    if [ -f "$destination" ]; then cp -- "$destination" "$destination.pre-restore-$stamp"; fi
    cp -- "$source" "$destination"
  done
  log "Native archives restored. Services remain stopped: review restored accounts, sessions and tokens before ./manage.sh start."
}

# ─── MQTT Test ────────────────────────────────────────────────────────────

cmd_mqtt_test() {
  if ! container_running "corescope-prod"; then
    err "Container not running. Start with: ./manage.sh start"
    exit 1
  fi

  info "Listening for MQTT messages (10 second timeout)..."
  MSG=$(docker exec corescope-prod mosquitto_sub -h localhost -t 'meshcore/#' -C 1 -W 10 2>/dev/null)
  if [ -n "$MSG" ]; then
    log "Received MQTT message:"
    echo "   $MSG" | head -c 200
    echo ""
  else
    warn "No messages received in 10 seconds."
    echo ""
    echo "   This means no observer is publishing packets."
    echo "   See the deployment guide for connecting observers."
  fi
}

# ─── Reset ────────────────────────────────────────────────────────────────

cmd_reset() {
  echo ""
  warn "This will remove all containers, images, and setup state."
  warn "Your config.json, Caddyfile, and data directory are NOT deleted."
  echo ""
  if ! confirm "Continue?"; then
    echo "   Aborted."
    exit 0
  fi

  dc_prod down --rmi local 2>/dev/null || true
  dc_staging down --rmi local 2>/dev/null || true
  rm -f "$STATE_FILE"

  log "Reset complete. Run './manage.sh setup' to start over."
  echo "   Data directory: $PROD_DATA (not removed)"
}

# ─── Help ─────────────────────────────────────────────────────────────────

cmd_storage() {
  local action="${1:-status}" argument="${2:-}" state source destination target requires_postgres=false
  local STORAGE_OPERATION="$action" extra=()
  case "$action" in
    status) storage_action prod status; return $? ;;
    switch)
      case "$argument" in sqlite|postgres) ;; *) err "Usage: ./manage.sh storage switch sqlite|postgres"; return 1 ;; esac
      state=$(storage_field prod state) || return 1
      [ "$state" = ready ] || { err "Finish setup or explicitly resume/abort the pending job before another switch."; return 1; }
      source=$(storage_field prod backend) || return 1
      [ "$source" != "$argument" ] || { info "Already using recorded $source storage."; return 0; }
      [ "$source" != postgres ] || require_managed_postgres prod || return 1
      prepare_database_credentials || return 1
      if ! confirm "Stop production writers and run the verified offline switch to $argument?"; then return 0; fi
      dc_prod_base stop prod || return 1
      # Starting only PostgreSQL leaves the conversion destination uninitialized.
      dc_postgres prod up -d --wait postgres || return 1
      if [ "$argument" = sqlite ]; then
        destination="$(storage_field prod state_dir)/sqlite-$(date -u +%Y%m%d-%H%M%S)-$$" || return 1
        extra=(-sqlite-path "$destination/meshcore.db" -users-sqlite-path "$destination/users.db")
      fi
      if ! dc_postgres prod run --rm --no-deps --entrypoint /app/storage.sh bootstrap switch "-backend=$argument" "${extra[@]}"; then
        err "Switch failed. Services remain stopped; preserve source/recovery files and inspect storage status for its resume/abort job ID."
        return 1
      fi
      write_private_env CORESCOPE_DB_BACKEND "$argument" || return 1
      [ "$argument" != sqlite ] || dc_postgres prod stop postgres || return 1
      log "Verified backend switch completed. Services remain stopped; inspect storage status and run start when ready."
      ;;
    resume|abort)
      [[ "$argument" =~ ^[0-9a-f]{32}$ ]] || { err "Use storage $action <job-id> from storage status."; return 1; }
      state=$(storage_field prod state) || return 1
      [ "$state" = pending ] || { err "No pending storage job matches this recovery action."; return 1; }
      [ "$(storage_field prod job_id)" = "$argument" ] || { err "Job ID does not match the pending switch."; return 1; }
      if [ "$action" = abort ]; then
        if ! confirm "Keep writers stopped and abort storage job $argument?"; then return 0; fi
        dc_prod_base stop prod || return 1
        storage_action prod abort "-job-id=$argument" || return 1
        log "Storage job aborted before publication. Source, staged targets and recovery files remain; inspect status before setup/start."
        return 0
      fi
      source=$(storage_field prod source_backend) || { err "Recovery source identity is unavailable; inspect storage status."; return 1; }
      target=$(storage_field prod target_backend) || { err "Recovery target identity is unavailable; inspect storage status."; return 1; }
      case "$source:$target" in :sqlite|:postgres|sqlite:sqlite|sqlite:postgres|postgres:sqlite|postgres:postgres) ;; *) err "The job has no bound target yet. Preserve recovery files, abort this job, then start a new setup/switch."; return 1 ;; esac
      if [ "$source" = postgres ] || [ "$target" = postgres ]; then requires_postgres=true; require_database_credentials || return 1; fi
      if ! confirm "Keep writers stopped and $action storage job $argument?"; then return 0; fi
      dc_prod_base stop prod || return 1
      if $requires_postgres; then
        dc_postgres prod up -d --wait postgres || return 1
        dc_postgres prod run --rm --no-deps --entrypoint /app/storage.sh bootstrap "$action" "-job-id=$argument" || return 1
      else
        storage_action prod "$action" "-job-id=$argument" || return 1
      fi
      source=$(storage_field prod backend) || return 1
      write_private_env CORESCOPE_DB_BACKEND "$source" || return 1
      if $requires_postgres && [ "$source" = sqlite ]; then dc_postgres prod stop postgres || return 1; fi
      log "Storage recovery completed. Original and staged recovery files remain; validate before start."
      ;;
    *) err "Usage: ./manage.sh storage status|switch sqlite|postgres|resume JOB_ID|abort JOB_ID"; return 1 ;;
  esac
}

cmd_help() {
  echo ""
  echo "CoreScope — Management Script"
  echo ""
  echo "Usage: ./manage.sh <command>"
  echo ""
  printf '%b\n' "  ${BOLD}Setup${NC}"
  echo "    setup              First-time setup wizard (safe to re-run)"
  echo "    reset              Remove container + image (keeps data + config)"
  echo ""
  printf '%b\n' "  ${BOLD}Run${NC}"
  echo "    start              Start production container"
  echo "    start --with-staging  Start production + staging-go (clones into empty staging telemetry + config)"
  echo "    stop [prod|staging|all]  Stop specific or all containers (default: all)"
  echo "    restart [prod|staging|all]  Restart specific or all containers"
  echo "    status             Show health, stats, and service status"
  echo "    logs [prod|staging] [N]  Follow logs (default: prod, last 100 lines)"
  echo ""
  printf '%b\n' "  ${BOLD}Maintain${NC}"
  echo "    update [version]   Update to version (no arg=latest tag, 'latest'=master tip, or e.g. v3.1.0)"
  echo "    promote            Promote staging → production (backup + restart)"
  echo "    backup [dir]       Native selected-backend snapshots + private state/config"
  echo "    restore <d>        Restore native backups into fresh/empty targets"
  echo "    storage status     Show recorded backend and pending recovery job"
  echo "    storage switch sqlite|postgres  Verified offline conversion; keeps recovery copies"
  echo "    storage resume|abort JOB_ID     Explicit interrupted-switch recovery"
  echo "    mqtt-test          Check if MQTT data is flowing"
  echo ""
  echo "Prod uses docker-compose.yml; staging uses ${STAGING_COMPOSE_FILE}."
  echo ""
}

# ─── Main ─────────────────────────────────────────────────────────────────

case "${1:-help}" in
  setup)     cmd_setup ;;
  storage)   shift; cmd_storage "$@" ;;
  start)     cmd_start "$2" ;;
  stop)      cmd_stop "$2" ;;
  restart)   cmd_restart "$2" ;;
  status)    cmd_status ;;
  logs)      cmd_logs "$2" "$3" ;;
  update)    cmd_update "$2" ;;
  promote)   cmd_promote ;;
  backup)    cmd_backup "$2" ;;
  restore)   cmd_restore "$2" ;;
  mqtt-test) cmd_mqtt_test ;;
  reset)     cmd_reset ;;
  help|*)    cmd_help ;;
esac
