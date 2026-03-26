#!/bin/bash
# MeshCore Analyzer — Setup & Management Helper
# Usage: ./manage.sh [command]
#
# Idempotent: safe to cancel and re-run at any point.
# Each step checks what's already done and skips it.
set -e

CONTAINER_NAME="meshcore-analyzer"
IMAGE_NAME="meshcore-analyzer"
DATA_VOLUME="meshcore-data"
CADDY_VOLUME="caddy-data"
STATE_FILE=".setup-state"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()  { printf '%b\n' "${GREEN}✓${NC} $1"; }
warn() { printf '%b\n' "${YELLOW}⚠${NC} $1"; }
err()  { printf '%b\n' "${RED}✗${NC} $1"; }
info() { printf '%b\n' "${CYAN}→${NC} $1"; }
step() { printf '%b\n' "\n${BOLD}[$1/$TOTAL_STEPS] $2${NC}"; }

confirm() {
  read -p "   $1 [y/N] " -n 1 -r
  echo
  [[ $REPLY =~ ^[Yy]$ ]]
}

# State tracking — marks completed steps so re-runs skip them
mark_done()  { echo "$1" >> "$STATE_FILE"; }
is_done()    { [ -f "$STATE_FILE" ] && grep -qx "$1" "$STATE_FILE" 2>/dev/null; }

# ─── Helpers ──────────────────────────────────────────────────────────────

# Determine the correct data volume/mount args for docker run.
# Detects existing host data directories and uses bind mounts if found.
get_data_mount_args() {
  # Check for existing host data directories with a DB file
  if [ -d "$HOME/meshcore-data" ] && [ -f "$HOME/meshcore-data/meshcore.db" ]; then
    echo "-v $HOME/meshcore-data:/app/data"
    return
  fi
  if [ -d "$(pwd)/data" ] && [ -f "$(pwd)/data/meshcore.db" ]; then
    echo "-v $(pwd)/data:/app/data"
    return
  fi
  # Default: Docker named volume
  echo "-v ${DATA_VOLUME}:/app/data"
}

# Determine the required port mappings from Caddyfile
get_required_ports() {
  local caddyfile_domain
  caddyfile_domain=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
  if echo "$caddyfile_domain" | grep -qE '^:[0-9]+$'; then
    # HTTP-only on a specific port (e.g., :80, :8080)
    echo "${caddyfile_domain#:}"
  else
    # Domain name — needs 80 + 443 for Caddy auto-TLS
    echo "80 443"
  fi
}

# Get current container port mappings (just the host ports)
get_current_ports() {
  docker inspect "$CONTAINER_NAME" 2>/dev/null | \
    grep -oP '"HostPort":\s*"\K[0-9]+' | sort -u | tr '\n' ' ' | sed 's/ $//'
}

# Check if container port mappings match what's needed.
# Returns 0 if they match, 1 if mismatch.
check_port_match() {
  local required current
  required=$(get_required_ports | tr ' ' '\n' | sort | tr '\n' ' ' | sed 's/ $//')
  current=$(get_current_ports | tr ' ' '\n' | sort | tr '\n' ' ' | sed 's/ $//')
  [ "$required" = "$current" ]
}

# Build the docker run command args (ports + volumes)
get_docker_run_args() {
  local ports_arg=""
  for port in $(get_required_ports); do
    ports_arg="$ports_arg -p ${port}:${port}"
  done

  local data_mount
  data_mount=$(get_data_mount_args)

  echo "$ports_arg \
    -v $(pwd)/config.json:/app/config.json:ro \
    -v $(pwd)/caddy-config/Caddyfile:/etc/caddy/Caddyfile:ro \
    $data_mount \
    -v ${CADDY_VOLUME}:/data/caddy"
}

# Recreate the container with current settings
recreate_container() {
  info "Stopping and removing old container..."
  docker stop "$CONTAINER_NAME" 2>/dev/null || true
  docker rm "$CONTAINER_NAME" 2>/dev/null || true

  local run_args
  run_args=$(get_docker_run_args)

  eval docker run -d \
    --name "$CONTAINER_NAME" \
    --restart unless-stopped \
    $run_args \
    "$IMAGE_NAME"
}

# Check config.json for placeholder values
check_config_placeholders() {
  if [ -f config.json ]; then
    if grep -qE 'your-username|your-password|your-secret|example\.com|changeme' config.json 2>/dev/null; then
      warn "config.json contains placeholder values."
      warn "Edit config.json and replace placeholder values before deploying."
    fi
  fi
}

# Verify the running container is actually healthy
verify_health() {
  local base_url="http://localhost:3000"
  local use_https=false

  # Check if Caddyfile has a real domain (not :80)
  if [ -f caddy-config/Caddyfile ]; then
    local caddyfile_domain
    caddyfile_domain=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
    if [ "$caddyfile_domain" != ":80" ] && [ -n "$caddyfile_domain" ]; then
      use_https=true
    fi
  fi

  # Wait for /api/stats response
  info "Waiting for Node.js to respond..."
  local healthy=false
  for i in $(seq 1 10); do
    if docker exec "$CONTAINER_NAME" wget -qO- http://localhost:3000/api/stats &>/dev/null; then
      healthy=true
      break
    fi
    sleep 2
  done

  if ! $healthy; then
    err "Node.js did not respond after 20 seconds."
    warn "Check logs: ./manage.sh logs"
    return 1
  fi
  log "Node.js is responding."

  # Check for MQTT errors in recent logs
  local mqtt_errors
  mqtt_errors=$(docker logs "$CONTAINER_NAME" --tail 50 2>&1 | grep -i 'mqtt.*error\|mqtt.*fail\|ECONNREFUSED.*1883' || true)
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
  echo "  MeshCore Analyzer Setup"
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
  mark_done "docker"

  # ── Step 2: Config ──
  step 2 "Configuration"

  if [ -f config.json ]; then
    log "config.json already exists (not overwriting)."
    # Sanity check the JSON
    if ! python3 -c "import json; json.load(open('config.json'))" 2>/dev/null && \
       ! node -e "JSON.parse(require('fs').readFileSync('config.json'))" 2>/dev/null; then
      err "config.json has invalid JSON. Fix it and re-run setup."
      exit 1
    fi
    log "config.json is valid JSON."
    check_config_placeholders
  else
    info "Creating config.json from example..."
    cp config.example.json config.json

    # Generate a random API key
    if command -v openssl &> /dev/null; then
      API_KEY=$(openssl rand -hex 16)
    else
      API_KEY=$(head -c 32 /dev/urandom | xxd -p | head -c 32)
    fi
    # Replace the placeholder API key
    if command -v sed &> /dev/null; then
      sed -i "s/your-secret-api-key-here/${API_KEY}/" config.json
    fi

    log "Created config.json with random API key."
    check_config_placeholders
    echo ""
    echo "   You can customize config.json later (map center, branding, etc)."
    echo "   Edit with: nano config.json"
    echo ""
  fi
  mark_done "config"

  # ── Step 3: Domain & HTTPS ──
  step 3 "Domain & HTTPS"

  if [ -f caddy-config/Caddyfile ]; then
    EXISTING_DOMAIN=$(grep -v '^#' caddy-config/Caddyfile 2>/dev/null | head -1 | tr -d ' {')
    if [ "$EXISTING_DOMAIN" = ":80" ]; then
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
    echo "   2) Behind my own reverse proxy — HTTP only, I choose the port"
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
        RESOLVED_IP=$(dig +short "$DOMAIN" 2>/dev/null | grep -E '^[0-9]+\.' | head -1)
        MY_IP=$(curl -s -4 ifconfig.me 2>/dev/null || curl -s -4 icanhazip.com 2>/dev/null || echo "unknown")

        if [ -z "$RESOLVED_IP" ]; then
          warn "${DOMAIN} doesn't resolve yet."
          warn "Create an A record pointing to ${MY_IP}"
          warn "HTTPS won't work until DNS propagates (1-60 min)."
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

        # Check port 80
        if command -v curl &> /dev/null; then
          if curl -s --connect-timeout 3 "http://localhost:80" &>/dev/null || \
             curl -s --connect-timeout 3 "http://${MY_IP}:80" &>/dev/null 2>&1; then
            warn "Something is already listening on port 80."
            warn "Stop it first: sudo systemctl stop nginx apache2"
          fi
        fi
        ;;
      2)
        read -p "   HTTP port [80]: " HTTP_PORT
        HTTP_PORT=${HTTP_PORT:-80}
        echo ":${HTTP_PORT} {
    reverse_proxy localhost:3000
}" > caddy-config/Caddyfile
        log "Caddyfile created (HTTP only on port ${HTTP_PORT})."
        echo "   Point your reverse proxy or tunnel to this server's port ${HTTP_PORT}."
        ;;
      *)
        warn "Invalid choice. Defaulting to HTTP only."
        echo ':80 {
    reverse_proxy localhost:3000
}' > caddy-config/Caddyfile
        ;;
    esac
  fi
  mark_done "caddyfile"

  # ── Step 4: Build ──
  step 4 "Building Docker image"

  # Check if image exists and source hasn't changed
  IMAGE_EXISTS=$(docker images -q "$IMAGE_NAME" 2>/dev/null)
  if [ -n "$IMAGE_EXISTS" ] && is_done "build"; then
    log "Image already built."
    if confirm "Rebuild? (only needed if you updated the code)"; then
      docker build -t "$IMAGE_NAME" .
      log "Image rebuilt."
    fi
  else
    info "This takes 1-2 minutes the first time..."
    docker build -t "$IMAGE_NAME" .
    log "Image built."
  fi
  mark_done "build"

  # ── Step 5: Start container ──
  step 5 "Starting container"

  # Detect existing data directories
  if [ -d "$HOME/meshcore-data" ] && [ -f "$HOME/meshcore-data/meshcore.db" ]; then
    info "Found existing data at \$HOME/meshcore-data/ — will use bind mount."
  elif [ -d "$(pwd)/data" ] && [ -f "$(pwd)/data/meshcore.db" ]; then
    info "Found existing data at ./data/ — will use bind mount."
  fi

  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    log "Container already running."
    # Check port mappings match
    if ! check_port_match; then
      warn "Container port mappings don't match Caddyfile configuration."
      warn "Current ports: $(get_current_ports)"
      warn "Required ports: $(get_required_ports)"
      if confirm "Recreate container with correct ports?"; then
        recreate_container
        log "Container recreated with correct ports."
      fi
    fi
  elif docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    # Exists but stopped — check ports before starting
    if ! check_port_match; then
      warn "Stopped container has wrong port mappings."
      warn "Current ports: $(get_current_ports)"
      warn "Required ports: $(get_required_ports)"
      if confirm "Recreate container with correct ports?"; then
        recreate_container
        log "Container recreated with correct ports."
      else
        info "Starting existing container (ports unchanged)..."
        docker start "$CONTAINER_NAME"
        log "Started (with old port mappings)."
      fi
    else
      info "Container exists but is stopped. Starting..."
      docker start "$CONTAINER_NAME"
      log "Started."
    fi
  else
    recreate_container
    log "Container started."
  fi
  mark_done "container"

  # ── Step 6: Verify ──
  step 6 "Verifying"

  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
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
    echo "     docker logs ${CONTAINER_NAME}"
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

# ─── Start / Stop / Restart ──────────────────────────────────────────────

cmd_start() {
  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    warn "Already running."
  elif docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    # Check if port mappings still match before starting
    if ! check_port_match; then
      warn "Container port mappings don't match Caddyfile configuration."
      warn "Current ports: $(get_current_ports)"
      warn "Required ports: $(get_required_ports)"
      if confirm "Recreate container with correct ports?"; then
        recreate_container
        log "Container recreated and started with correct ports."
        return
      fi
    fi
    docker start "$CONTAINER_NAME"
    log "Started."
  else
    err "Container doesn't exist. Run './manage.sh setup' first."
    exit 1
  fi
}

cmd_stop() {
  docker stop "$CONTAINER_NAME" 2>/dev/null && log "Stopped." || warn "Not running."
}

cmd_restart() {
  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    # Check if ports need updating — if so, recreate instead of just restarting
    if ! check_port_match; then
      warn "Port mappings have changed. Recreating container..."
      recreate_container
      log "Container recreated with correct ports."
    else
      docker restart "$CONTAINER_NAME"
      log "Restarted."
    fi
  elif docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    # Stopped container — check ports and recreate if needed
    if ! check_port_match; then
      warn "Port mappings have changed. Recreating container..."
      recreate_container
      log "Container recreated with correct ports."
    else
      docker start "$CONTAINER_NAME"
      log "Started."
    fi
  else
    err "Not running. Use './manage.sh setup'."
    exit 1
  fi
}

# ─── Status ───────────────────────────────────────────────────────────────

cmd_status() {
  echo ""
  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    log "Container is running."
    echo ""
    docker ps --filter "name=${CONTAINER_NAME}" --format "   Status: {{.Status}}"
    docker ps --filter "name=${CONTAINER_NAME}" --format "   Ports:  {{.Ports}}"
    echo ""

    info "Service health:"
    # Node.js
    if docker exec "$CONTAINER_NAME" wget -qO /dev/null http://localhost:3000/api/stats 2>/dev/null; then
      STATS=$(docker exec "$CONTAINER_NAME" wget -qO- http://localhost:3000/api/stats 2>/dev/null)
      PACKETS=$(echo "$STATS" | grep -oP '"totalPackets":\K[0-9]+' 2>/dev/null || echo "?")
      NODES=$(echo "$STATS" | grep -oP '"totalNodes":\K[0-9]+' 2>/dev/null || echo "?")
      log "  Node.js — ${PACKETS} packets, ${NODES} nodes"
    else
      err "  Node.js — not responding"
    fi

    # Mosquitto
    if docker exec "$CONTAINER_NAME" pgrep mosquitto &>/dev/null; then
      log "  Mosquitto — running"
    else
      err "  Mosquitto — not running"
    fi

    # Caddy
    if docker exec "$CONTAINER_NAME" pgrep caddy &>/dev/null; then
      log "  Caddy — running"
    else
      err "  Caddy — not running"
    fi

    # Check for MQTT errors in recent logs
    MQTT_ERRORS=$(docker logs "$CONTAINER_NAME" --tail 50 2>&1 | grep -i 'mqtt.*error\|mqtt.*fail\|ECONNREFUSED.*1883' || true)
    if [ -n "$MQTT_ERRORS" ]; then
      echo ""
      warn "MQTT errors in recent logs:"
      echo "$MQTT_ERRORS" | head -3 | sed 's/^/   /'
    fi

    # Port mapping check
    if ! check_port_match; then
      echo ""
      warn "Port mappings don't match Caddyfile. Run './manage.sh restart' to fix."
    fi

    # Disk usage
    DB_SIZE=$(docker exec "$CONTAINER_NAME" du -h /app/data/meshcore.db 2>/dev/null | cut -f1)
    if [ -n "$DB_SIZE" ]; then
      echo ""
      info "Database size: ${DB_SIZE}"
    fi
  else
    err "Container is not running."
    if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
      echo "   Start with: ./manage.sh start"
    else
      echo "   Set up with: ./manage.sh setup"
    fi
  fi
  echo ""
}

# ─── Logs ─────────────────────────────────────────────────────────────────

cmd_logs() {
  docker logs -f "$CONTAINER_NAME" --tail "${1:-100}"
}

# ─── Update ───────────────────────────────────────────────────────────────

cmd_update() {
  info "Pulling latest code..."
  git pull

  info "Rebuilding image..."
  docker build -t "$IMAGE_NAME" .

  info "Restarting with new image..."
  recreate_container

  log "Updated and restarted. Data preserved."
}

# ─── Backup ───────────────────────────────────────────────────────────────

cmd_backup() {
  TIMESTAMP=$(date +%Y%m%d-%H%M%S)
  BACKUP_DIR="${1:-./backups/meshcore-${TIMESTAMP}}"
  mkdir -p "$BACKUP_DIR"

  info "Backing up to ${BACKUP_DIR}/"

  # Database
  DB_PATH=$(docker volume inspect "$DATA_VOLUME" --format '{{ .Mountpoint }}' 2>/dev/null)/meshcore.db
  if [ -f "$DB_PATH" ]; then
    cp "$DB_PATH" "$BACKUP_DIR/meshcore.db"
    log "Database ($(du -h "$BACKUP_DIR/meshcore.db" | cut -f1))"
  elif docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    docker cp "${CONTAINER_NAME}:/app/data/meshcore.db" "$BACKUP_DIR/meshcore.db" 2>/dev/null && \
      log "Database (via docker cp)" || warn "Could not backup database"
  else
    warn "Database not found (container not running?)"
  fi

  # Config
  if [ -f config.json ]; then
    cp config.json "$BACKUP_DIR/config.json"
    log "config.json"
  fi

  # Caddyfile
  if [ -f caddy-config/Caddyfile ]; then
    cp caddy-config/Caddyfile "$BACKUP_DIR/Caddyfile"
    log "Caddyfile"
  fi

  # Theme
  THEME_PATH=$(docker volume inspect "$DATA_VOLUME" --format '{{ .Mountpoint }}' 2>/dev/null)/theme.json
  if [ -f "$THEME_PATH" ]; then
    cp "$THEME_PATH" "$BACKUP_DIR/theme.json"
    log "theme.json"
  elif [ -f theme.json ]; then
    cp theme.json "$BACKUP_DIR/theme.json"
    log "theme.json"
  fi

  # Summary
  TOTAL=$(du -sh "$BACKUP_DIR" | cut -f1)
  FILES=$(ls "$BACKUP_DIR" | wc -l)
  echo ""
  log "Backup complete: ${FILES} files, ${TOTAL} total → ${BACKUP_DIR}/"
}

# ─── Restore ──────────────────────────────────────────────────────────────

cmd_restore() {
  if [ -z "$1" ]; then
    err "Usage: ./manage.sh restore <backup-dir-or-db-file>"
    if [ -d "./backups" ]; then
      echo ""
      echo "   Available backups:"
      ls -dt ./backups/meshcore-* 2>/dev/null | head -10 | while read d; do
        if [ -d "$d" ]; then
          echo "     $d/ ($(ls "$d" | wc -l) files)"
        elif [ -f "$d" ]; then
          echo "     $d ($(du -h "$d" | cut -f1))"
        fi
      done
    fi
    exit 1
  fi

  # Accept either a directory (full backup) or a single .db file
  if [ -d "$1" ]; then
    DB_FILE="$1/meshcore.db"
    CONFIG_FILE="$1/config.json"
    CADDY_FILE="$1/Caddyfile"
    THEME_FILE="$1/theme.json"
  elif [ -f "$1" ]; then
    DB_FILE="$1"
    CONFIG_FILE=""
    CADDY_FILE=""
    THEME_FILE=""
  else
    err "Not found: $1"
    exit 1
  fi

  if [ ! -f "$DB_FILE" ]; then
    err "No meshcore.db found in $1"
    exit 1
  fi

  echo ""
  info "Will restore from: $1"
  [ -f "$DB_FILE" ] && echo "   • Database"
  [ -n "$CONFIG_FILE" ] && [ -f "$CONFIG_FILE" ] && echo "   • config.json"
  [ -n "$CADDY_FILE" ] && [ -f "$CADDY_FILE" ] && echo "   • Caddyfile"
  [ -n "$THEME_FILE" ] && [ -f "$THEME_FILE" ] && echo "   • theme.json"
  echo ""

  if ! confirm "Continue? (current state will be backed up first)"; then
    echo "   Aborted."
    exit 0
  fi

  # Backup current state first
  info "Backing up current state..."
  cmd_backup "./backups/meshcore-pre-restore-$(date +%Y%m%d-%H%M%S)"

  docker stop "$CONTAINER_NAME" 2>/dev/null || true

  # Restore database
  DEST_DB=$(docker volume inspect "$DATA_VOLUME" --format '{{ .Mountpoint }}' 2>/dev/null)/meshcore.db
  if [ -d "$(dirname "$DEST_DB")" ]; then
    cp "$DB_FILE" "$DEST_DB"
  else
    docker cp "$DB_FILE" "${CONTAINER_NAME}:/app/data/meshcore.db"
  fi
  log "Database restored"

  # Restore config if present
  if [ -n "$CONFIG_FILE" ] && [ -f "$CONFIG_FILE" ]; then
    cp "$CONFIG_FILE" ./config.json
    log "config.json restored"
  fi

  # Restore Caddyfile if present
  if [ -n "$CADDY_FILE" ] && [ -f "$CADDY_FILE" ]; then
    mkdir -p caddy-config
    cp "$CADDY_FILE" caddy-config/Caddyfile
    log "Caddyfile restored"
  fi

  # Restore theme if present
  if [ -n "$THEME_FILE" ] && [ -f "$THEME_FILE" ]; then
    DEST_THEME=$(docker volume inspect "$DATA_VOLUME" --format '{{ .Mountpoint }}' 2>/dev/null)/theme.json
    if [ -d "$(dirname "$DEST_THEME")" ]; then
      cp "$THEME_FILE" "$DEST_THEME"
    fi
    log "theme.json restored"
  fi

  docker start "$CONTAINER_NAME"
  log "Restored and restarted."
}

# ─── MQTT Test ────────────────────────────────────────────────────────────

cmd_mqtt_test() {
  if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    err "Container not running. Start with: ./manage.sh start"
    exit 1
  fi

  info "Listening for MQTT messages (10 second timeout)..."
  MSG=$(docker exec "$CONTAINER_NAME" mosquitto_sub -h localhost -t 'meshcore/#' -C 1 -W 10 2>/dev/null)
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
  warn "This will remove the container, image, and setup state."
  warn "Your config.json, Caddyfile, and data volume are NOT deleted."
  echo ""
  if ! confirm "Continue?"; then
    echo "   Aborted."
    exit 0
  fi

  docker stop "$CONTAINER_NAME" 2>/dev/null || true
  docker rm "$CONTAINER_NAME" 2>/dev/null || true
  docker rmi "$IMAGE_NAME" 2>/dev/null || true
  rm -f "$STATE_FILE"

  log "Reset complete. Run './manage.sh setup' to start over."
  echo "   Data volume preserved. To delete it: docker volume rm ${DATA_VOLUME}"
}

# ─── Help ─────────────────────────────────────────────────────────────────

cmd_help() {
  echo ""
  echo "MeshCore Analyzer — Management Script"
  echo ""
  echo "Usage: ./manage.sh <command>"
  echo ""
  printf '%b\n' "  ${BOLD}Setup${NC}"
  echo "    setup        First-time setup wizard (safe to re-run)"
  echo "    reset        Remove container + image (keeps data + config)"
  echo ""
  printf '%b\n' "  ${BOLD}Run${NC}"
  echo "    start        Start the container"
  echo "    stop         Stop the container"
  echo "    restart      Restart the container"
  echo "    status       Show health, stats, and service status"
  echo "    logs [N]     Follow logs (last N lines, default 100)"
  echo ""
  printf '%b\n' "  ${BOLD}Maintain${NC}"
  echo "    update       Pull latest code, rebuild, restart (keeps data)"
  echo "    backup [dir] Full backup: database + config + theme (default: ./backups/timestamped/)"
  echo "    restore <d>  Restore from backup dir or .db file (backs up current first)"
  echo "    mqtt-test    Check if MQTT data is flowing"
  echo ""
}

# ─── Main ─────────────────────────────────────────────────────────────────

case "${1:-help}" in
  setup)     cmd_setup ;;
  start)     cmd_start ;;
  stop)      cmd_stop ;;
  restart)   cmd_restart ;;
  status)    cmd_status ;;
  logs)      cmd_logs "$2" ;;
  update)    cmd_update ;;
  backup)    cmd_backup "$2" ;;
  restore)   cmd_restore "$2" ;;
  mqtt-test) cmd_mqtt_test ;;
  reset)     cmd_reset ;;
  help|*)    cmd_help ;;
esac
