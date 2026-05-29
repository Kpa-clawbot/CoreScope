#!/usr/bin/env bash
# scripts/check-dockerfile-internal-pkgs.sh
#
# Asserts every internal/<pkg> referenced via a "replace" directive in
# cmd/server/go.mod or cmd/ingestor/go.mod has a matching
# "COPY internal/<pkg>/" line in Dockerfile for each builder section that
# needs it.
#
# This catches the recurring class of Docker build failure (issue #1316):
#   go: github.com/meshcore-analyzer/<pkg>: reading ../../internal/<pkg>/go.mod:
#   open /internal/<pkg>/go.mod: no such file or directory
#
# The bug pattern: a PR adds a new internal/<pkg>, updates both go.mod files
# with a replace directive, but forgets the matching COPY line in Dockerfile.
# All non-Docker CI passes (go build works in-tree). The Docker build fails
# AFTER the PR merges with a cryptic module error.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DOCKERFILE="$ROOT/Dockerfile"
MODS=("$ROOT/cmd/server/go.mod" "$ROOT/cmd/ingestor/go.mod")
ERRORS=0

# PKG_COUNT[pkg] = number of go.mod files that reference internal/<pkg>.
# The Dockerfile must have at least that many COPY directives — one per
# builder section that compiles a binary depending on the package.
declare -A PKG_COUNT

for mod in "${MODS[@]}"; do
    while IFS= read -r line; do
        if [[ "$line" =~ ^[[:space:]]*replace[[:space:]].*=\>[[:space:]]+\.\./\.\./internal/([^[:space:]]+) ]]; then
            pkg="${BASH_REMATCH[1]}"
            PKG_COUNT["$pkg"]=$(( ${PKG_COUNT["$pkg"]-0} + 1 ))
        fi
    done < "$mod"
done

for pkg in "${!PKG_COUNT[@]}"; do
    expected="${PKG_COUNT[$pkg]}"
    # Anchored ERE: skip commented-out COPY lines (e.g. "# COPY internal/foo/").
    count=$(grep -cE "^[[:space:]]*COPY internal/${pkg}/" "$DOCKERFILE" || true)
    count="${count:-0}"
    if [[ "$count" -lt "$expected" ]]; then
        echo "ERROR: 'COPY internal/${pkg}/' appears ${count} time(s) in Dockerfile, expected at least ${expected} (one per builder section that uses it)" >&2
        ERRORS=$((ERRORS + 1))
    fi
done

if [[ $ERRORS -gt 0 ]]; then
    echo "" >&2
    echo "  $ERRORS missing COPY directive(s) — Docker build will fail at module resolution." >&2
    echo "  Add the missing COPY line(s) to each relevant builder section of Dockerfile." >&2
    exit 1
fi

echo "✓ Dockerfile COPY invariant: all internal/<pkg> COPY directives present."
