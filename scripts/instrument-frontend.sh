#!/bin/sh
# Instrument frontend JS for coverage tracking
rm -rf public-instrumented
npx nyc instrument public/ public-instrumented/ --compact=false
# Copy non-JS files (CSS, HTML, images) as-is
cp public/*.css public-instrumented/ 2>/dev/null
cp public/*.html public-instrumented/ 2>/dev/null
cp public/*.svg public-instrumented/ 2>/dev/null
cp public/*.png public-instrumented/ 2>/dev/null
echo "Frontend instrumented successfully"
