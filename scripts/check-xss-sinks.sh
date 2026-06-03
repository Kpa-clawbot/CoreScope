#!/usr/bin/env bash
# check-xss-sinks.sh — local mirror of the canonical pr-preflight gate at
# ~/.openclaw/skills/pr-preflight/scripts/check-xss-sinks.sh, scoped to
# testdata/preflight-xss fixtures so CI can exercise the gate without
# requiring the skill directory.
#
# Modes:
#   $0 --file <path>     scan a single file; exit 1 if any sink in that
#                        file interpolates a node-controlled identifier
#                        without escapeHtml/escapeAttr/safeEsc.
#
# COMMIT 1 (RED) STATE: this is a no-op stub. It always exits 0.
# COMMIT 2 (GREEN) replaces the body with the real check.
set -u

mode="${1:-}"
shift || true
case "$mode" in
  --file)
    # No-op stub. Always claim clean.
    exit 0
    ;;
  *)
    echo "usage: $0 --file <path>" >&2
    exit 2
    ;;
esac
