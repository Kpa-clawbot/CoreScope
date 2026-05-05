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
node test-packet-filter-ux.js
node test-aging.js
node test-frontend-helpers.js
node test-url-state.js
node test-perf-go-runtime.js
node test-channel-psk-ux.js
node test-channel-sidebar-layout.js
node test-channel-modal-ux.js
node test-channel-decrypt-insecure-context.js
node test-channel-qr.js
node test-channel-qr-wiring.js
node test-analytics-channels-integration.js
node test-observers-headings.js

echo ""
echo "═══════════════════════════════════════"
echo "  All tests passed"
echo "═══════════════════════════════════════"
