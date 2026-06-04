#!/bin/sh
# check-cdn-bypass.sh — verify a CoreScope deployment's /api/* is
# NOT being cached by an upstream CDN (Cloudflare, Fastly, etc.).
#
# Issue #1561. Run this from outside the CDN (e.g. from a different
# network than the origin). Exits 0 if no CDN caching is detected,
# 1 otherwise with a specific finding.
#
# Usage:
#   scripts/check-cdn-bypass.sh https://analyzer.example.com
#
# Requires: POSIX sh, curl, grep, awk. No other dependencies.

set -u

if [ $# -ne 1 ]; then
    echo "usage: $0 <https://your-host>" >&2
    exit 2
fi

HOST="$1"
URL="${HOST%/}/api/observers"

# -s silent, -S show errors, -I HEAD request, -L follow redirects
HDRS="$(curl -sSIL "$URL" 2>&1)"
rc=$?
if [ "$rc" != "0" ] && [ -z "$HDRS" ]; then
    echo "FAIL: curl could not reach $URL (exit $rc)" >&2
    exit 1
fi

# Lowercase the header names so grep is case-insensitive without -i tricks.
LOWER="$(printf '%s\n' "$HDRS" | awk '{
  n = index($0, ":");
  if (n > 0) {
    name = tolower(substr($0, 1, n-1));
    rest = substr($0, n);
    print name rest;
  } else {
    print $0;
  }
}')"

CF_STATUS="$(printf '%s\n' "$LOWER" | awk -F': *' '/^cf-cache-status:/ {print $2; exit}' | tr -d '\r')"
AGE="$(printf '%s\n' "$LOWER" | awk -F': *' '/^age:/ {print $2; exit}' | tr -d '\r')"

case "$CF_STATUS" in
    HIT|hit)
        echo "FAIL: cf-cache-status: HIT — CDN is caching /api/observers; payload may be ${AGE:-unknown} seconds stale. Add a Cloudflare Cache Rule (Bypass cache) for /api/* — see docs/deployment-behind-cdn.md."
        exit 1
        ;;
esac

if [ -n "$AGE" ]; then
    # age=0 is fine — anything > 0 means an intermediary served from cache.
    case "$AGE" in
        0|0[!0-9]*) : ;;
        *)
            echo "FAIL: response is $AGE seconds stale (age header > 0) — an intermediary cache is serving /api/observers. Configure your CDN to bypass /api/* — see docs/deployment-behind-cdn.md."
            exit 1
            ;;
    esac
fi

echo "OK: no CDN caching detected for /api/observers (cf-cache-status=${CF_STATUS:-absent}, age=${AGE:-0})"
exit 0
