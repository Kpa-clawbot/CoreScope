#!/bin/sh
# Test harness for scripts/check-cdn-bypass.sh — issue #1561.
# Substitutes a fake `curl` on PATH so we can simulate CDN responses
# without network access. The fake curl honors `-o <file>` (writes
# the mocked headers there) and `-w '%{http_code}'` (writes the
# mocked HTTP status to stdout), matching the real curl interface
# the production script depends on.

set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET="$SCRIPT_DIR/check-cdn-bypass.sh"

PASS=0
FAIL=0

# mk_fake_curl HEADERS HTTP_STATUS
# Builds a tmpdir containing a `curl` shim that:
#   - parses -o <file> from its args and writes HEADERS there
#   - parses -w <fmt> and prints HTTP_STATUS to stdout (the script
#     only ever uses -w '%{http_code}')
#   - exits 0 (curl considers a non-2xx still a successful transfer
#     unless -f is passed; production script does not pass -f, so
#     this matches reality).
mk_fake_curl() {
    headers="$1"
    status="$2"
    tmpdir="$(mktemp -d)"
    # Embed the values via heredoc with single-quoted delimiter so
    # nothing is expanded inside the generated script except by the
    # outer shell now.
    cat >"$tmpdir/curl" <<EOF
#!/bin/sh
out=""
while [ \$# -gt 0 ]; do
    case "\$1" in
        -o) out="\$2"; shift 2 ;;
        -w) shift 2 ;;        # always %{http_code} in this script
        -*) shift ;;          # -s, -S, -I, -L, etc.
        *) shift ;;           # URL
    esac
done
if [ -n "\$out" ]; then
    cat >"\$out" <<'BODY'
$headers
BODY
fi
printf '%s' '$status'
exit 0
EOF
    chmod +x "$tmpdir/curl"
    echo "$tmpdir"
}

run_case() {
    name="$1"
    headers="$2"
    status="$3"
    want_exit="$4"
    want_substr="$5"

    if [ ! -f "$TARGET" ]; then
        echo "FAIL: $name — $TARGET missing"
        FAIL=$((FAIL+1))
        return
    fi

    fakedir="$(mk_fake_curl "$headers" "$status")"
    out="$(PATH="$fakedir:$PATH" sh "$TARGET" https://example.test 2>&1)"
    rc=$?
    rm -rf "$fakedir"

    if [ "$rc" != "$want_exit" ]; then
        echo "FAIL: $name — exit code $rc, want $want_exit; output: $out"
        FAIL=$((FAIL+1))
        return
    fi
    case "$out" in
        *"$want_substr"*) ;;
        *)
            printf 'FAIL: %s — output missing %s; got: %s\n' "$name" "$want_substr" "$out"
            FAIL=$((FAIL+1))
            return
            ;;
    esac
    echo "PASS: $name"
    PASS=$((PASS+1))
}

# Case 1: HTTP 200, bypass — no cf-cache HIT, age:0 → exit 0
run_case "bypass-ok-200" "cache-control: no-store
cf-cache-status: BYPASS
age: 0" "200" 0 "OK"

# Case 2: HTTP 200, CDN HIT — exit 1, mention HIT
run_case "cf-hit-200" "cache-control: no-store
cf-cache-status: HIT
age: 47" "200" 1 "HIT"

# Case 3: HTTP 200, stale by age — no cf-cache header but age > 0 → exit 1
run_case "stale-age-200" "cache-control: no-store
age: 120" "200" 1 "stale"

# Case 4: HTTP 200, no cache headers at all → exit 0 (existing behavior preserved)
run_case "bare-200-no-cache-headers" "content-type: application/json" "200" 0 "OK"

# Round-1 regression: a non-200 endpoint had been silently reported
# as "OK: no CDN caching detected" because no cache headers were
# present. Script must now refuse to draw a conclusion.

# Case 5: HTTP 404 → exit 1, status-related error
run_case "http-404-fails-with-status-error" "" "404" 1 "HTTP 404"

# Case 6: HTTP 401 → exit 1, status-related error
run_case "http-401-fails-with-status-error" "" "401" 1 "HTTP 401"

# Case 7: HTTP 403 → exit 1, status-related error
run_case "http-403-fails-with-status-error" "" "403" 1 "HTTP 403"

# Case 8: HTTP 500 → exit 1, status-related error
run_case "http-500-fails-with-status-error" "" "500" 1 "HTTP 500"

echo
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ] || exit 1
