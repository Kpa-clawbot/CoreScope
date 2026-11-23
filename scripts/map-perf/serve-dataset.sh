#!/usr/bin/env bash
# Run CoreScope's Go server locally against the large synthetic dataset, built
# from any git ref (or the current worktree), to compare map performance.
#
#   serve-dataset.sh prepare [--force]
#   serve-dataset.sh build <ref|worktree> <outdir>
#   serve-dataset.sh serve <ref|worktree> <port>
#   serve-dataset.sh stop <port>
#   serve-dataset.sh status
#
# Needs docker (Go is built in golang:1.27, glibc, so the binaries run on the
# host) and python3 >= SQLite 3.38 (timestamp shifter). The cgo sqlite driver
# needs cgo: never build with CGO_ENABLED=0 (it links a stub).
set -euo pipefail

SEED_DB="${SEED_DB:-$HOME/.cache/corescope-dataset/d18-seed1/analyzer.db}"
SHIFTED_DIR="${SHIFTED_DIR:-$HOME/.cache/corescope-dataset/d18-seed1-shifted}"
SHIFTED_DB="$SHIFTED_DIR/analyzer.db"
SHIFT_SH="${SHIFT_SH:-$HOME/Development/corescope-synthetic-dataset/pipeline/shift.sh}"
WORK="${WORK:-$HOME/.cache/corescope-map-perf}"
GO_IMAGE="${GO_IMAGE:-golang:1.27}"
BASELINE_REF="${BASELINE_REF:-upstream/master}"
WAIT_SECS="${WAIT_SECS:-180}"
# Optional: a CoreScope config.json whose map.tiles.providers.carto.key is
# copied into the run config (only that key), so Carto serves real tiles
# instead of "API KEY REQUIRED" ones. Unset = no key. The run config lives
# under $WORK, outside the repo; never commit a key.
CARTO_KEY_CONFIG="${CARTO_KEY_CONFIG:-}"

die() { echo "error: $*" >&2; exit 1; }
repo_root() { git rev-parse --show-toplevel; }

# resolve_src REF: print the source dir for REF ("worktree" = current tree).
resolve_src() {
  local ref="$1" root sha src
  root="$(repo_root)"
  if [ "$ref" = worktree ]; then printf '%s\n' "$root"; return; fi
  sha="$(git -C "$root" rev-parse --short=10 "$ref^{commit}")" || die "unknown ref: $ref"
  src="$WORK/src-$sha"
  if [ ! -d "$src" ]; then
    mkdir -p "$WORK"
    git -C "$root" worktree add --detach "$src" "$sha" >&2
  fi
  printf '%s\n' "$src"
}

# go_build SRC OUTDIR: build corescope-server and corescope-migrate from SRC.
go_build() {
  local src="$1" out="$2"
  mkdir -p "$out"
  # Runs as root (the cache volumes are root-owned); outputs are chowned back.
  docker run --rm -e GOFLAGS=-buildvcs=false -e HOST_IDS="$(id -u):$(id -g)" \
    -v cs-gomod:/go/pkg/mod -v cs-gobuild:/root/.cache/go-build \
    -v "$src:/src" -v "$out:/out" -w /src "$GO_IMAGE" \
    sh -c 'cd cmd/server && go build -o /out/corescope-server . &&
           cd ../migrate && go build -o /out/corescope-migrate . &&
           chown -R "$HOST_IDS" /out'
}

cmd_build() {
  [ $# -eq 2 ] || die "usage: build <ref|worktree> <outdir>"
  local src out
  src="$(resolve_src "$1")"; mkdir -p "$2"; out="$(cd "$2" && pwd)"
  go_build "$src" "$out"
  echo "built $1 -> $out"
}

# build_cached REF: print the bin dir; refs are cached by commit sha.
build_cached() {
  local ref="$1" src sha bin
  src="$(resolve_src "$ref")"
  if [ "$ref" = worktree ]; then
    bin="$WORK/bin-worktree"
  else
    sha="$(git -C "$src" rev-parse --short=10 HEAD)"
    bin="$WORK/bin-$sha"
  fi
  if [ "$ref" = worktree ] || [ ! -x "$bin/corescope-server" ] || [ ! -x "$bin/corescope-migrate" ]; then
    go_build "$src" "$bin" >&2 || return 1
  fi
  printf '%s\n' "$bin"
}

cmd_prepare() {
  local force=0
  [ "${1:-}" = --force ] && force=1
  if [ -f "$SHIFTED_DB" ] && [ "$force" = 0 ]; then
    echo "shifted copy exists, skipping (use --force): $SHIFTED_DB ($(du -h "$SHIFTED_DB" | cut -f1))"
    return
  fi
  [ -f "$SEED_DB" ] || die "seed dataset not found: $SEED_DB"
  local t0 bin now
  t0=$(date +%s)
  bin="$(build_cached "$BASELINE_REF")" || die "build failed"
  rm -rf "$SHIFTED_DIR"; mkdir -p "$SHIFTED_DIR"
  cp "$SEED_DB" "$SHIFTED_DB"
  # shift.sh takes the old window end from a manifest next to the DB.
  [ -f "$(dirname "$SEED_DB")/manifest.json" ] && cp "$(dirname "$SEED_DB")/manifest.json" "$SHIFTED_DIR/"
  chmod u+w "$SHIFTED_DB"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  SHIFT_NATIVE=1 bash "$SHIFT_SH" "$SHIFTED_DB" "$now"
  "$bin/corescope-migrate" -db "$SHIFTED_DB"
  echo "prepared $SHIFTED_DB ($(du -h "$SHIFTED_DB" | cut -f1)) newest row = $now in $(( $(date +%s) - t0 )) s"
}

cmd_serve() {
  [ $# -eq 2 ] || die "usage: serve <ref|worktree> <port>"
  local ref="$1" port="$2" src bin run t0 pid
  case "$port" in ''|*[!0-9]*) die "bad port: $port";; esac
  [ -f "$SHIFTED_DB" ] || die "no shifted dataset; run: $0 prepare"
  run="$WORK/run-$port"
  [ -f "$run/server.pid" ] && kill -0 "$(cat "$run/server.pid")" 2>/dev/null && die "port $port already running (pid $(cat "$run/server.pid"))"
  src="$(resolve_src "$ref")" || exit 1
  bin="$(build_cached "$ref")" || die "build failed"
  mkdir -p "$run"
  # Same config the ref ships (map section untouched); only listener and DB
  # are set, and the example's geo_filter (a small Bay Area polygon) is dropped
  # because it hides ~99% of the synthetic nodes from the API.
  # The planner and route line-of-sight are opt-in; terrain tiles are fetched
  # on demand. The packet store window is pinned to the newest 48 h: LoadChunked
  # walks ids ASC from the start of the retention window and stops at the memory
  # cap, so a window bigger than the cap keeps the OLDEST rows, not the newest.
  # 48 h is ~85k transmissions here, safely under the cap.
  # Nothing in the server needs MQTT or the network.
  jq --argjson p "$port" --arg db "$SHIFTED_DB" \
    '.port = $p | .dbPath = $db | .userManagement.enabled = false | del(.geo_filter)
     | .planner.enabled = true | .los.enabled = true
     | .packetStore.retentionHours = 48 | .packetStore.hotStartupHours = 48
     | .packetStore.maxMemoryMB = 1536' \
    "$src/config.example.json" > "$run/config.json"
  if [ -n "$CARTO_KEY_CONFIG" ]; then
    [ -r "$CARTO_KEY_CONFIG" ] || die "CARTO_KEY_CONFIG not readable: $CARTO_KEY_CONFIG"
    local key
    key="$(jq -r '.map.tiles.providers.carto.key // empty' "$CARTO_KEY_CONFIG")"
    [ -n "$key" ] || die "no map.tiles.providers.carto.key in $CARTO_KEY_CONFIG"
    jq --arg k "$key" '.map.tiles.providers.carto.key = $k' "$run/config.json" > "$run/config.json.tmp"
    mv "$run/config.json.tmp" "$run/config.json"
    chmod 600 "$run/config.json"
    echo "carto key: set from CARTO_KEY_CONFIG"
  fi
  echo "$ref" > "$run/ref"
  t0=$(date +%s.%N)
  # Not backgrounded as a compound list: $! must be the server's own pid.
  cd "$run"
  setsid nohup "$bin/corescope-server" -config-dir "$run" -db "$SHIFTED_DB" \
    -port "$port" -public "$src/public" > "$run/server.log" 2>&1 < /dev/null &
  pid=$!
  echo "$pid" > "$run/server.pid"
  # The store loads for a minute or more and the first answer can itself be
  # slow while startup recomputes run, so poll against a wall-clock deadline.
  while (( $(date +%s) - ${t0%.*} < WAIT_SECS )); do
    if curl -sf --max-time 5 "http://localhost:$port/api/stats" > /dev/null; then
      printf 'serving %s at http://localhost:%s/ (pid %s) startup %.1f s\n' \
        "$ref" "$port" "$pid" "$(echo "$(date +%s.%N) - $t0" | bc)"
      return
    fi
    kill -0 "$pid" 2>/dev/null || { tail -20 "$run/server.log" >&2; die "server exited"; }
    sleep 0.5
  done
  die "no answer from :$port after ${WAIT_SECS}s (see $run/server.log)"
}

cmd_stop() {
  [ $# -eq 1 ] || die "usage: stop <port>"
  local pidf="$WORK/run-$1/server.pid"
  [ -f "$pidf" ] || die "no pid file for port $1"
  local pid; pid="$(cat "$pidf")"
  if kill -0 "$pid" 2>/dev/null; then kill "$pid"; echo "stopped $1 (pid $pid)"; else echo "port $1 not running"; fi
  rm -f "$pidf"
}

cmd_status() {
  local pidf pid port found=0
  for pidf in "$WORK"/run-*/server.pid; do
    [ -f "$pidf" ] || continue
    pid="$(cat "$pidf")"; port="$(basename "$(dirname "$pidf")")"; port="${port#run-}"
    if kill -0 "$pid" 2>/dev/null; then
      printf 'port %s  pid %s  rss %s KB  ref %s\n' "$port" "$pid" \
        "$(ps -o rss= -p "$pid" | tr -d ' ')" "$(cat "$(dirname "$pidf")/ref")"
      found=1
    fi
  done
  [ "$found" = 1 ] || echo "no instances running"
}

sub="${1:-}"; [ $# -gt 0 ] && shift
case "$sub" in
  prepare) cmd_prepare "$@";;
  build) cmd_build "$@";;
  serve) cmd_serve "$@";;
  stop) cmd_stop "$@";;
  status) cmd_status;;
  *) die "usage: $0 {prepare [--force]|build <ref|worktree> <outdir>|serve <ref|worktree> <port>|stop <port>|status}";;
esac
