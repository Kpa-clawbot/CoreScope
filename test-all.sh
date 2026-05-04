#!/bin/sh
# Run all tests with coverage
set -e

echo "═══════════════════════════════════════"
echo "  CoreScope — Test Suite"
echo "═══════════════════════════════════════"
echo ""

# Unit tests (deterministic, fast)
echo "── Unit Tests ──"
node test-packet-filter.js
node test-aging.js
node test-frontend-helpers.js
node test-perf-go-runtime.js
node test-channel-psk-ux.js
node test-channel-decrypt-insecure-context.js

echo ""
echo "═══════════════════════════════════════"
echo "  All tests passed"
echo "═══════════════════════════════════════"
