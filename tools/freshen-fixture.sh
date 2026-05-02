#!/usr/bin/env bash
# freshen-fixture.sh — Shift all timestamps in the fixture DB to be relative to now.
# Preserves the relative ordering between timestamps.
# Usage: bash tools/freshen-fixture.sh <path-to-fixture.db>
set -euo pipefail

DB="${1:?Usage: freshen-fixture.sh <path-to-fixture.db>}"

if [ ! -f "$DB" ]; then
  echo "ERROR: DB file not found: $DB" >&2
  exit 1
fi

# Find the max timestamp across all time columns, compute offset, shift everything forward.
sqlite3 "$DB" <<'SQL'
-- Shift all timestamps forward so the newest is ~now, preserving relative ordering.
-- Use strftime to produce RFC3339 format (T separator + Z suffix) for correct comparison.
UPDATE nodes SET last_seen = strftime('%Y-%m-%dT%H:%M:%SZ', last_seen,
  (SELECT printf('+%d seconds', CAST((julianday('now') - julianday(MAX(last_seen))) * 86400 AS INTEGER)) FROM nodes)
) WHERE last_seen IS NOT NULL;

UPDATE transmissions SET first_seen = strftime('%Y-%m-%dT%H:%M:%SZ', first_seen,
  (SELECT printf('+%d seconds', CAST((julianday('now') - julianday(MAX(first_seen))) * 86400 AS INTEGER)) FROM transmissions)
) WHERE first_seen IS NOT NULL;

-- Observers: shift last_seen too so they don't get auto-pruned by RemoveStaleObservers
-- on server startup (default 14d threshold marks all >14d observers inactive=1, which
-- the /api/observers filter then excludes — leaving the map page with no observer markers).
UPDATE observers SET last_seen = strftime('%Y-%m-%dT%H:%M:%SZ', last_seen,
  (SELECT printf('+%d seconds', CAST((julianday('now') - julianday(MAX(last_seen))) * 86400 AS INTEGER)) FROM observers)
) WHERE last_seen IS NOT NULL;
SQL

# Defensive: clear any stale inactive=1 flags. Column may not exist on fresh fixtures
# (added by server migration on first startup); silently no-op if missing.
sqlite3 "$DB" "UPDATE observers SET inactive = 0 WHERE inactive = 1;" 2>/dev/null || true

# neighbor_edges may not exist in all fixture versions
sqlite3 "$DB" "UPDATE neighbor_edges SET last_seen = strftime('%Y-%m-%dT%H:%M:%SZ', last_seen, (SELECT printf('+%d seconds', CAST((julianday('now') - julianday(MAX(last_seen))) * 86400 AS INTEGER)) FROM neighbor_edges)) WHERE last_seen IS NOT NULL;" 2>/dev/null || true

echo "Fixture timestamps freshened in $DB"
sqlite3 "$DB" "SELECT 'nodes: min=' || MIN(last_seen) || ' max=' || MAX(last_seen) FROM nodes;"
sqlite3 "$DB" "SELECT 'observers: count=' || COUNT(*) || ' max=' || MAX(last_seen) FROM observers;"
