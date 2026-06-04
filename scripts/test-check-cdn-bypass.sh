#!/bin/sh
# Test harness for scripts/check-cdn-bypass.sh — issue #1561.
# Substitutes a fake `curl` on PATH so we can simulate CDN responses
# without network access.
#
# Red commit: scripts/check-cdn-bypass.sh does not exist yet, so all
# cases fail with "script missing" or exit code != expected.

set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET="$SCRIPT_DIR/check-cdn-bypass.sh"

PASS=0
FAIL=0

mk_fake_curl() {
    # $1 = response body (the headers we want to feed the script)
    tmpdir="$(mktemp -d)"
    cat >"$tmpdir/curl" <<EOF
#!/bin/sh
cat <<'BODY'
$1
BODY
EOF
    chmod +x "$tmpdir/curl"
    echo "$tmpdir"
}

run_case() {
    name="$1"
    headers="$2"
    want_exit="$3"
    want_substr="$4"

    if [ ! -f "$TARGET" ]; then
        echo "FAIL: $name — $TARGET missing"
        FAIL=$((FAIL+1))
        return
    fi

    fakedir="$(mk_fake_curl "$headers")"
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
            echo "FAIL: $name — output missing %q; got: $out" "$want_substr"
            FAIL=$((FAIL+1))
            return
            ;;
    esac
    echo "PASS: $name"
    PASS=$((PASS+1))
}

# Case 1: bypass — no cf-cache HIT, age:0 → exit 0
run_case "bypass-ok" "cache-control: no-store
cf-cache-status: BYPASS
age: 0" 0 "OK"

# Case 2: CDN HIT — exit 1, mention HIT
run_case "cf-hit" "cache-control: no-store
cf-cache-status: HIT
age: 47" 1 "HIT"

# Case 3: stale by age — no cf-cache header but age > 0 → exit 1
run_case "stale-age" "cache-control: no-store
age: 120" 1 "stale"

echo
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ] || exit 1
