#!/usr/bin/env bash
# Packages public/ for a static host (Cloudflare Pages, a CDN, the web
# container) instead of the CoreScope server. This is a packaging step, not a
# build step: the output is public/ byte for byte, except that index.html gets
# a cache buster and deployment.js says where the API is.
#
# A static host runs it as the build command (output dir dist/web); the web
# container (Dockerfile.web) runs it when the image is built. Locally:
# make web, or make dev-split to try it against a local server.
#
# Environment:
#   API_BASE     required. https://host[:port] of the CoreScope server
#                (http only for localhost / 127.0.0.1), or same-origin for a
#                host that forwards /api and /ws to the server itself.
#   AUTH         required, true or false. false builds the anonymous UI: no
#                cookies, no login or admin controls. There is no default, so
#                nobody turns logins on by accident. same-origin needs true:
#                the UI then uses the server's logins exactly as embedded.
#   CHANNEL      beta | next | dev | static. Default static.
#   COMMIT       shown in the UI and used as the cache buster. Default: the
#                first 7 characters of CF_PAGES_COMMIT_SHA (set by Pages), else
#                git rev-parse --short HEAD.
#   CLASSIC_URL  optional. The embedded UI, for "Open in classic" links.
#   OUT          output directory. Default dist/web.
set -euo pipefail
cd "$(dirname "$0")/../.."

fail() { echo "build: $*" >&2; exit 1; }

OUT="${OUT:-dist/web}"
CHANNEL="${CHANNEL:-static}"
API_BASE="${API_BASE:-}"
AUTH="${AUTH:-}"
CLASSIC_URL="${CLASSIC_URL:-}"
COMMIT="${COMMIT:-}"
if [ -z "$COMMIT" ] && [ -n "${CF_PAGES_COMMIT_SHA:-}" ]; then COMMIT="${CF_PAGES_COMMIT_SHA:0:7}"; fi
if [ -z "$COMMIT" ]; then COMMIT="$(git rev-parse --short HEAD 2>/dev/null || true)"; fi

# Every value lands inside a JS string literal, so each one is held to a
# shape that cannot break out of it.
origin_ok() {
  [[ "$1" =~ ^https://[a-z0-9.-]+(:[0-9]{1,5})?$ ]] && return 0
  [[ "$1" =~ ^http://(localhost|127\.0\.0\.1)(:[0-9]{1,5})?$ ]]
}
[ -n "$API_BASE" ] || fail "API_BASE is required (the CoreScope server, e.g. https://analyzer.example.org, or same-origin)"
API_BASE="${API_BASE%/}"
if [ "$API_BASE" = same-origin ]; then
  [ "$AUTH" = true ] || fail "same-origin needs AUTH=true: a UI on the server's own origin uses its logins"
  API_BASE=''
else
  origin_ok "$API_BASE" || fail "API_BASE must be https://host[:port] in lowercase, with no path (http only for localhost), or same-origin: $API_BASE"
fi
if [ -n "$CLASSIC_URL" ]; then
  CLASSIC_URL="${CLASSIC_URL%/}"
  origin_ok "$CLASSIC_URL" || fail "CLASSIC_URL must be https://host[:port], with no path: $CLASSIC_URL"
fi
case "$AUTH" in
  true|false) ;;
  *) fail "AUTH must be true or false (false for the anonymous build)" ;;
esac
case "$CHANNEL" in
  beta|next|dev|static) ;;
  *) fail "CHANNEL must be beta, next, dev or static: $CHANNEL" ;;
esac
[[ "$COMMIT" =~ ^[A-Za-z0-9._-]{1,40}$ ]] || fail "COMMIT must be 1-40 characters of [A-Za-z0-9._-]: '$COMMIT'"

rm -rf "$OUT"
mkdir -p "$OUT"
cp -R public/. "$OUT/"

# The same substitution spaHandler makes at server start, with the commit in
# place of a timestamp so a rebuild of one commit is byte-identical.
sed -i.bak "s/__BUST__/$COMMIT/g" "$OUT/index.html" && rm -f "$OUT/index.html.bak"

classic_js=null
[ -n "$CLASSIC_URL" ] && classic_js="'$CLASSIC_URL'"
cat > "$OUT/deployment.js" <<EOF
/* Written by scripts/static/build.sh. */
window.CS_DEPLOYMENT = { channel: '$CHANNEL', commit: '$COMMIT', version: null, apiBase: '$API_BASE', auth: $AUTH, classicUrl: $classic_js };
EOF

cp deploy/static/_headers "$OUT/_headers"

# -- checks ---------------------------------------------------------------------
if grep -rl "__BUST__" "$OUT" >/dev/null; then fail "a __BUST__ placeholder is left in $(grep -rl "__BUST__" "$OUT" | head -1)"; fi
[ ! -e "$OUT/functions" ] && [ ! -e "$OUT/_worker.js" ] || fail "functions/ and _worker.js are not allowed: the frontend is static only"
count=$(find "$OUT" -type f | wc -l)
[ "$count" -lt 20000 ] || fail "$count files; Cloudflare Pages allows fewer than 20000"
# 25 MiB in bytes: busybox find (the web image) has no M suffix.
big=$(find "$OUT" -type f -size +26214400c | head -1)
[ -z "$big" ] || fail "$big is over 25 MiB, the Cloudflare Pages per-file limit"
# Byte for byte public/, plus _headers; only index.html and deployment.js differ.
expected=$( (cd public && find . -type f; echo ./_headers) | sort)
actual=$(cd "$OUT" && find . -type f | sort)
[ "$expected" = "$actual" ] || fail "the output is not exactly public/ plus _headers:
$(diff <(echo "$expected") <(echo "$actual") || true)"
while IFS= read -r f; do
  case "$f" in ./index.html|./deployment.js|./_headers) continue ;; esac
  cmp -s "public/$f" "$OUT/$f" || fail "$f differs from public/$f"
done <<< "$actual"

echo "build: $OUT ($count files) channel=$CHANNEL commit=$COMMIT apiBase=${API_BASE:-same-origin} auth=$AUTH"
