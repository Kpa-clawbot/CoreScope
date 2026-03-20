#!/bin/bash
# A/B benchmark: old (pre-perf) vs new (current)
# Usage: ./benchmark-ab.sh
set -e

PORT_OLD=13003
PORT_NEW=13004
RUNS=3
DB_PATH="$(pwd)/data/meshcore.db"

OLD_COMMIT="23caae4"
NEW_COMMIT="$(git rev-parse HEAD)"

echo "═══════════════════════════════════════════════════════"
echo "  A/B Benchmark: Pre-optimization vs Current"
echo "═══════════════════════════════════════════════════════"
echo "OLD: $OLD_COMMIT (v2.0.1 — before any perf work)"
echo "NEW: $NEW_COMMIT (current)"
echo "Runs per endpoint: $RUNS"
echo ""

# Get a real node pubkey for testing
ORIG_DIR="$(pwd)"
PUBKEY=$(sqlite3 "$DB_PATH" "SELECT public_key FROM nodes ORDER BY last_seen DESC LIMIT 1")
echo "Test node: ${PUBKEY:0:16}..."
echo ""

# Setup old version in temp dir
OLD_DIR=$(mktemp -d)
echo "Cloning old version to $OLD_DIR..."
git worktree add "$OLD_DIR" "$OLD_COMMIT" --quiet 2>/dev/null || {
  git worktree add "$OLD_DIR" "$OLD_COMMIT" --detach --quiet
}
# Copy config + db symlink
# Copy config + db + share node_modules
cp config.json "$OLD_DIR/"
mkdir -p "$OLD_DIR/data"
cp "$ORIG_DIR/data/meshcore.db" "$OLD_DIR/data/meshcore.db"
ln -sf "$ORIG_DIR/node_modules" "$OLD_DIR/node_modules"

ENDPOINTS=(
  "Stats|/api/stats"
  "Packets(50)|/api/packets?limit=50"
  "PacketsGrouped|/api/packets?limit=50&groupByHash=true"
  "NodesList|/api/nodes?limit=50"
  "NodeDetail|/api/nodes/$PUBKEY"
  "NodeHealth|/api/nodes/$PUBKEY/health"
  "NodeAnalytics|/api/nodes/$PUBKEY/analytics?days=7"
  "BulkHealth|/api/nodes/bulk-health?limit=50"
  "NetworkStatus|/api/nodes/network-status"
  "Channels|/api/channels"
  "Observers|/api/observers"
  "RF|/api/analytics/rf"
  "Topology|/api/analytics/topology"
  "ChannelAnalytics|/api/analytics/channels"
  "HashSizes|/api/analytics/hash-sizes"
)

bench_endpoint() {
  local port=$1 path=$2 runs=$3 nocache=$4
  local total=0
  for i in $(seq 1 $runs); do
    local url="http://127.0.0.1:$port$path"
    if [ "$nocache" = "1" ]; then
      if echo "$path" | grep -q '?'; then
        url="${url}&nocache=1"
      else
        url="${url}?nocache=1"
      fi
    fi
    local ms=$(curl -s -o /dev/null -w "%{time_total}" "$url" 2>/dev/null)
    local ms_int=$(echo "$ms * 1000" | bc | cut -d. -f1)
    total=$((total + ms_int))
  done
  echo $((total / runs))
}

# Launch old server
echo "Starting OLD server (port $PORT_OLD)..."
cd "$OLD_DIR"
PORT=$PORT_OLD node server.js &>/dev/null &
OLD_PID=$!
cd - >/dev/null

# Launch new server
echo "Starting NEW server (port $PORT_NEW)..."
PORT=$PORT_NEW node server.js &>/dev/null &
NEW_PID=$!

# Wait for both
sleep 12  # old server has no memory store; new needs prewarm

# Verify
curl -s "http://127.0.0.1:$PORT_OLD/api/stats" >/dev/null 2>&1 || { echo "OLD server failed to start"; kill $OLD_PID $NEW_PID 2>/dev/null; exit 1; }
curl -s "http://127.0.0.1:$PORT_NEW/api/stats" >/dev/null 2>&1 || { echo "NEW server failed to start"; kill $OLD_PID $NEW_PID 2>/dev/null; exit 1; }

echo ""
echo "Warming up caches on new server..."
for ep in "${ENDPOINTS[@]}"; do
  path="${ep#*|}"
  curl -s -o /dev/null "http://127.0.0.1:$PORT_NEW$path" 2>/dev/null
done
sleep 2

printf "\n%-22s %9s %9s %9s %9s\n" "Endpoint" "Old(ms)" "New-cold" "New-cache" "Speedup"
printf "%-22s %9s %9s %9s %9s\n" "──────────────────────" "─────────" "─────────" "─────────" "─────────"

for ep in "${ENDPOINTS[@]}"; do
  name="${ep%%|*}"
  path="${ep#*|}"
  
  old_ms=$(bench_endpoint $PORT_OLD "$path" $RUNS 0)
  new_cold=$(bench_endpoint $PORT_NEW "$path" $RUNS 1)
  new_cached=$(bench_endpoint $PORT_NEW "$path" $RUNS 0)
  
  if [ "$old_ms" -gt 0 ] && [ "$new_cached" -gt 0 ]; then
    speedup="${old_ms}/${new_cached}"
    speedup_x=$(echo "scale=0; $old_ms / $new_cached" | bc 2>/dev/null || echo "?")
    printf "%-22s %7dms %7dms %7dms %7d×\n" "$name" "$old_ms" "$new_cold" "$new_cached" "$speedup_x"
  else
    printf "%-22s %7dms %7dms %7dms %9s\n" "$name" "$old_ms" "$new_cold" "$new_cached" "∞"
  fi
done

echo ""
echo "═══════════════════════════════════════════════════════"

# Cleanup
kill $OLD_PID $NEW_PID 2>/dev/null
git worktree remove "$OLD_DIR" --force 2>/dev/null
echo "Done."
