#!/usr/bin/env bash
# test-disk-monitor.sh — unit tests for scripts/staging/disk-monitor.sh
# (issue #1684). Pure bash, no external deps. Sources the script and
# exercises its pure helpers against table-driven cases.
#
# Run: bash scripts/staging/test-disk-monitor.sh
# Exits non-zero if any case fails.

set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=disk-monitor.sh
. "$SCRIPT_DIR/disk-monitor.sh"

PASS=0
FAIL=0

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [ "$expected" = "$actual" ]; then
        PASS=$((PASS + 1))
        # echo "PASS: $label"
    else
        FAIL=$((FAIL + 1))
        echo "FAIL: $label — expected '$expected' got '$actual'" >&2
    fi
}

# ----- classify_threshold ---------------------------------------------------
# Spec from issue #1684: <80 ok ; >=80 warn ; >=90 error ; >=95 alert
assert_eq "classify 0"   "ok"    "$(classify_threshold 0)"
assert_eq "classify 50"  "ok"    "$(classify_threshold 50)"
assert_eq "classify 79"  "ok"    "$(classify_threshold 79)"
assert_eq "classify 80"  "warn"  "$(classify_threshold 80)"
assert_eq "classify 85"  "warn"  "$(classify_threshold 85)"
assert_eq "classify 89"  "warn"  "$(classify_threshold 89)"
assert_eq "classify 90"  "error" "$(classify_threshold 90)"
assert_eq "classify 94"  "error" "$(classify_threshold 94)"
assert_eq "classify 95"  "alert" "$(classify_threshold 95)"
assert_eq "classify 100" "alert" "$(classify_threshold 100)"

# Invalid inputs return non-zero (no echo expected).
if classify_threshold "abc" >/dev/null 2>&1; then
    FAIL=$((FAIL + 1))
    echo "FAIL: classify 'abc' — expected non-zero exit" >&2
else
    PASS=$((PASS + 1))
fi
if classify_threshold 150 >/dev/null 2>&1; then
    FAIL=$((FAIL + 1))
    echo "FAIL: classify 150 — expected non-zero exit" >&2
else
    PASS=$((PASS + 1))
fi

# ----- parse_df_percent -----------------------------------------------------
# Simulates `df -P /` output. Use% column 5.
DF_OK='Filesystem     1024-blocks      Used Available Capacity Mounted on
/dev/root         30401152  17040640  13360512      57% /'
DF_HIGH='Filesystem     1024-blocks      Used Available Capacity Mounted on
/dev/root         30401152  29401152   1000000      97% /'
DF_FULL='Filesystem     1024-blocks      Used Available Capacity Mounted on
/dev/root         30401152  30401152         0     100% /'

assert_eq "parse_df 57%"  "57"  "$(parse_df_percent "$DF_OK")"
assert_eq "parse_df 97%"  "97"  "$(parse_df_percent "$DF_HIGH")"
assert_eq "parse_df 100%" "100" "$(parse_df_percent "$DF_FULL")"

# Pipeline: parse_df_percent | classify_threshold (the real call path).
assert_eq "pipe 57->ok"     "ok"    "$(classify_threshold "$(parse_df_percent "$DF_OK")")"
assert_eq "pipe 97->alert"  "alert" "$(classify_threshold "$(parse_df_percent "$DF_HIGH")")"
assert_eq "pipe 100->alert" "alert" "$(classify_threshold "$(parse_df_percent "$DF_FULL")")"

# ----- severity_priority ----------------------------------------------------
assert_eq "prio ok"    "user.info"    "$(severity_priority ok)"
assert_eq "prio warn"  "user.warning" "$(severity_priority warn)"
assert_eq "prio error" "user.err"     "$(severity_priority error)"
assert_eq "prio alert" "user.crit"    "$(severity_priority alert)"

echo "----"
echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
