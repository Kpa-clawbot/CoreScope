#!/bin/sh
# Instrument frontend JS for coverage tracking
rm -rf public-instrumented
npx nyc instrument public/ public-instrumented/ --compact=false
# Copy non-JS files (CSS, HTML, images) as-is
cp public/*.css public-instrumented/ 2>/dev/null
cp public/*.html public-instrumented/ 2>/dev/null
cp public/*.svg public-instrumented/ 2>/dev/null
cp public/*.png public-instrumented/ 2>/dev/null
# Copy nested asset directories (e.g. public/img/*.svg used by the new
# CoreScope logo + hero). nyc instrument skips non-JS subdirs entirely,
# so without this the SPA fallback would serve index.html for
# `/img/corescope-logo.svg`, breaking the navbar logo + the
# logo-rebrand E2E (the content-type assertion catches this cleanly).
if [ -d public/img ]; then
  mkdir -p public-instrumented/img
  cp -r public/img/. public-instrumented/img/
fi
# Copy webfonts (e.g. public/fonts/aldrich-regular.woff2 used by the
# navbar logo SVG @font-face, #1137 follow-up). Same SPA-fallback gotcha
# as /img — without this, GET /fonts/aldrich-regular.woff2 returns
# index.html and the @font-face download fails silently, so the logo
# falls back to monospace and the Aldrich E2E assertion fails.
if [ -d public/fonts ]; then
  mkdir -p public-instrumented/fonts
  cp -r public/fonts/. public-instrumented/fonts/
fi
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
