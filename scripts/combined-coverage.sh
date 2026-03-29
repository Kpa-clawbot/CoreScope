#!/bin/sh
# Combined coverage: Go backend + frontend via Playwright
# TODO: Update to use Go server binary instead of removed Node.js server.
# The old flow used `node server.js` — now use the Go binary from cmd/server/.
set -e

echo "⚠️  combined-coverage.sh needs updating for Go server migration."
echo "   The Node.js server (server.js) has been removed."
echo "   Update this script to start the Go binary instead."
exit 1
