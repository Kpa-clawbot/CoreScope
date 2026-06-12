#!/usr/bin/env bash
# disk-cleanup.sh — daily staging VM cleanup (issue #1684).
#
# Removes orphaned /tmp snapshots older than 7 days and prunes Docker
# build cache + dangling images older than 72h (respecting label=keep).
#
# Designed to run from a daily systemd timer at off-peak. Idempotent.
# Set CORESCOPE_CLEANUP_DRY_RUN=1 to log without deleting.

set -euo pipefail

DRY_RUN="${CORESCOPE_CLEANUP_DRY_RUN:-0}"
LOG_TAG="corescope-disk-cleanup"

log() {
    echo "$LOG_TAG: $*" >&2
    if command -v logger >/dev/null 2>&1; then
        logger -t "$LOG_TAG" -- "$*"
    fi
}

run_or_dry() {
    if [ "$DRY_RUN" = "1" ]; then
        log "DRY_RUN: $*"
    else
        log "exec: $*"
        "$@"
    fi
}

# ----- /tmp snapshot retention ----------------------------------------------
# Anything in /tmp matching known snapshot/cache patterns older than 7 days dies.
# -mindepth 1 avoids touching /tmp itself; -maxdepth 2 limits blast radius.
cleanup_tmp() {
    log "scanning /tmp for snapshots older than 7d"
    local find_args=(
        /tmp -mindepth 1 -maxdepth 2 -mtime +7
        \(
          -name '*.db' -o
          -name 'staging-snap.*' -o
          -name 'cs-*' -o
          -name 'node-compile-cache'
        \)
    )
    if [ "$DRY_RUN" = "1" ]; then
        find "${find_args[@]}" -print | while IFS= read -r f; do
            log "DRY_RUN: would rm -rf $f"
        done
    else
        # -print before -exec so we have an audit trail in journald.
        find "${find_args[@]}" -print -exec rm -rf {} +
    fi
}

# ----- Docker prune ---------------------------------------------------------
cleanup_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        log "docker not installed; skipping docker prune"
        return 0
    fi
    run_or_dry docker builder prune -af --filter "until=72h"
    run_or_dry docker image prune -af --filter "until=72h" --filter "label!=keep"
}

main() {
    log "starting (dry_run=$DRY_RUN)"
    cleanup_tmp
    cleanup_docker
    log "done"
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
