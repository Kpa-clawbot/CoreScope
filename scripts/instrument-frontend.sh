#!/bin/sh
# Instrument frontend JS for coverage tracking
rm -rf public-instrumented
npx nyc instrument public/ public-instrumented/ --compact=false
# Copy non-JS files (CSS, HTML, images) as-is
cp public/*.css public-instrumented/ 2>/dev/null
cp public/*.html public-instrumented/ 2>/dev/null
cp public/*.svg public-instrumented/ 2>/dev/null
cp public/*.png public-instrumented/ 2>/dev/null
# Copy vendored libraries unmodified — `nyc instrument` skips subdirectories
# without a package.json, so vendor/qrcode.js, vendor/jsqr.min.js, etc. are
# never emitted into public-instrumented/. Without them the SPA fallback
# returns index.html for `<script src="vendor/qrcode.js">`, producing
# "Unexpected token '<'" pageerrors and a missing `qrcode` global —
# which makes the QR Generate path hit the "[QR library not loaded]"
# fallback in channel-qr.js (issue #1087 bug 1 manifests in CI only).
mkdir -p public-instrumented/vendor
cp public/vendor/* public-instrumented/vendor/ 2>/dev/null
echo "Frontend instrumented successfully"
