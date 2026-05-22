#!/bin/sh
# Run all tests with coverage
#
# SCOPE: This script runs the *curated fast unit subset* only — deterministic,
# self-contained node `test-*.js` files that need no network, no running
# server, no Playwright browser, and no jsdom. It is meant to be quick enough
# to run on every change.
#
# It is NOT the full test matrix. E2E and integration tests run separately:
#   - Playwright `*-e2e.js` tests run in CI (.github/workflows/deploy.yml,
#     `e2e-test` job) against a Go server with a fixture DB.
#   - Go tests (server/ingestor/channel/decrypt) run in CI's `go-test` job.
# Not every `test-*.js` file in the repo root is wired here — some are slow,
# require a server, or are otherwise unsuitable for this fast subset.
set -e

echo "═══════════════════════════════════════"
echo "  CoreScope — Test Suite"
echo "═══════════════════════════════════════"
echo ""

# Unit tests (deterministic, fast)
echo "── Unit Tests ──"
node test-packet-filter.js
node test-packet-filter-ux.js
node test-packet-filter-time.js
node test-aging.js
node test-frontend-helpers.js
node test-page-state.js
node test-url-state.js
node test-perf-go-runtime.js
node test-home-announcement.js
node test-roles-foldin-nav.js
node test-hash-color.js
node test-channel-psk-ux.js
node test-channel-sidebar-layout.js
node test-channel-fluid-layout.js
node test-channel-modal-ux.js
node test-channel-decrypt-insecure-context.js
node test-channel-decrypt-ecb.js
node test-channel-decrypt-m345.js
node test-channel-qr.js
node test-channel-qr-wiring.js
node test-channel-issue-1087.js
node test-channel-issue-1101.js
node test-channel-color-picker.js
node test-color-picker-ux.js
node test-compare-flood-filter.js
node test-compare-overlap.js
node test-live-region-filter.js
node test-issue-1136-observer-iata-map.js
node test-analytics-channels-integration.js
node test-observers-headings.js
node test-traces.js

echo ""
echo "═══════════════════════════════════════"
echo "  All tests passed"
echo "═══════════════════════════════════════"
