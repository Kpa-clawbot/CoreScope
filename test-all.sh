#!/bin/sh
# Run all tests with coverage
set -e

echo "═══════════════════════════════════════"
echo "  MeshCore Analyzer — Test Suite"
echo "═══════════════════════════════════════"
echo ""

# Unit tests (deterministic, fast)
echo "── Unit Tests ──"
node test-decoder.js
node test-decoder-spec.js
node test-packet-store.js
node test-packet-filter.js
node test-aging.js
node test-frontend-helpers.js
node test-regional-filter.js
node test-server-helpers.js
node test-server-routes.js
node test-db.js
node test-db-migration.js

# Integration tests (spin up temp servers)
echo ""
echo "── Integration Tests ──"
node tools/e2e-test.js
node tools/frontend-test.js

echo ""
echo "═══════════════════════════════════════"
echo "  All tests passed"
echo "═══════════════════════════════════════"
node test-server-routes.js
# test trigger
