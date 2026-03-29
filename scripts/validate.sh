#!/bin/sh
# Pre-push validation — catches common JS errors before they hit prod
set -e

echo "=== Syntax check ==="
for f in public/*.js; do node -c "$f"; done
echo "✅ All JS files parse OK"

echo "=== Checking for undefined common references ==="
ERRORS=0

# esc() should only exist inside IIFEs that define it, not in files that don't
for f in public/live.js public/map.js public/home.js public/nodes.js public/channels.js public/observers.js; do
  if grep -q '\besc(' "$f" 2>/dev/null && ! grep -q 'function esc' "$f" 2>/dev/null; then
    REFS=$(grep -n '\besc(' "$f" | grep -v escapeHtml | grep -v "desc\|Esc\|resc\|safeEsc" || true)
    if [ -n "$REFS" ]; then
      echo "❌ $f uses esc() but doesn't define it:"
      echo "$REFS"
      ERRORS=$((ERRORS + 1))
    fi
  fi
done

if [ "$ERRORS" -gt 0 ]; then
  echo "❌ $ERRORS validation error(s) found"
  exit 1
fi

echo "✅ Validation passed"
