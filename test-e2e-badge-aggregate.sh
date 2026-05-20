#!/usr/bin/env bash
# Test for scripts/aggregate-e2e-pass.sh — verifies aggregate across 45+
# Playwright suites in test-fixtures/e2e-output-sample.txt is correct, not the
# broken old behavior of "grep digits-before-slash | tail -1" (which returned 2).
#
# Regression for #1296.
set -u
script_dir=$(cd "$(dirname "$0")" && pwd)
aggregator="$script_dir/scripts/aggregate-e2e-pass.sh"
fixture="$script_dir/test-fixtures/e2e-output-sample.txt"

if [ ! -x "$aggregator" ]; then
  chmod +x "$aggregator"
fi

# --- Test 1: fixture aggregate ---------------------------------------------
out=$("$aggregator" "$fixture")
# Count expected pass: sum N for every per-suite summary in the fixture.
# Computed by hand from the fixture (45 suites, see file).
EXPECTED_PASS=108
EXPECTED_FAIL=0
EXPECTED="PASS=$EXPECTED_PASS FAIL=$EXPECTED_FAIL"

if [ "$out" != "$EXPECTED" ]; then
  echo "FAIL: fixture aggregate"
  echo "  expected: $EXPECTED"
  echo "  got:      $out"
  exit 1
fi
echo "PASS: fixture aggregates to $out"

# --- Test 2: the broken old regex would have returned something tiny -------
# (sanity check that we are NOT just reproducing the bug). Requires grep -P
# (PCRE), which is available in GitHub-hosted Ubuntu runners and most Linux
# distros but not in BusyBox; skip gracefully if absent.
if echo "x1/2" | grep -qoP '[0-9]+(?=/)' 2>/dev/null; then
  old=$(grep -oP '[0-9]+(?=/)' "$fixture" | tail -1)
  if [ "$old" = "$EXPECTED_PASS" ]; then
    echo "FAIL: old broken regex coincidentally matches expected — fixture is not discriminating"
    exit 1
  fi
  echo "PASS: old broken regex returned '$old' (NOT $EXPECTED_PASS) — fixture proves the bug"
else
  echo "SKIP: grep -P unavailable, cannot verify old broken regex sanity"
fi

# --- Test 3: synthetic with failures, ensures FAIL accounting --------------
tmp=$(mktemp)
cat > "$tmp" <<'EOF'
=== Results: 4 passed, 1 failed ===
=== Results: 2 passed, 0 failed ===
test-foo.js: 3/5 passed
test-bar.js: PASS
test-baz.js: FAIL — boom
passed 7 failed 2
EOF
out2=$("$aggregator" "$tmp")
rm -f "$tmp"
EXP2="PASS=17 FAIL=4"
if [ "$out2" != "$EXP2" ]; then
  echo "FAIL: synthetic mixed pass/fail"
  echo "  expected: $EXP2"
  echo "  got:      $out2"
  exit 1
fi
echo "PASS: synthetic mixed pass/fail aggregates to $out2"

# --- Test 4: per-test progress lines must NOT be counted -------------------
tmp=$(mktemp)
cat > "$tmp" <<'EOF'
  ✓ test alpha
  ✓ test beta
  ✗ test gamma failed
  PASS: detail line
  FAIL: detail line
=== Results: 2 passed, 1 failed ===
EOF
out3=$("$aggregator" "$tmp")
rm -f "$tmp"
EXP3="PASS=2 FAIL=1"
if [ "$out3" != "$EXP3" ]; then
  echo "FAIL: per-test progress double-count"
  echo "  expected: $EXP3"
  echo "  got:      $out3"
  exit 1
fi
echo "PASS: per-test progress lines correctly ignored ($out3)"

# --- Test 5: empty / missing file ------------------------------------------
out4=$("$aggregator" /nonexistent/path/nope.txt)
if [ "$out4" != "PASS=0 FAIL=0" ]; then
  echo "FAIL: missing file should yield PASS=0 FAIL=0, got $out4"
  exit 1
fi
echo "PASS: missing file → PASS=0 FAIL=0"

echo
echo "ALL TESTS PASS"
