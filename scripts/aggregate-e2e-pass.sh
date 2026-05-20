#!/usr/bin/env bash
# Aggregate E2E pass/fail counts across all per-suite summary lines.
#
# Each test-*-e2e.js emits a summary line in one of these shapes:
#   "N passed, M failed"             — most suites (=== Results: 4 passed, 0 failed ===)
#   "passed N failed M"              — observer-iata style
#   "N/T tests passed[, M failed]"   — issue-1224 / 1236 / 1273 style
#   "N/T PASS"                       — logo-* suites
#   "N/T passed"                     — nav-fluid / nav-priority / nav-more-floor
#   "<file>.js: PASS"                — single-test suites (hamburger-dropdown)
#   "<file>.js: FAIL ..."            — suite-level failure
#
# Per-test progress lines (leading whitespace + PASS:/FAIL:/✓/✗) are skipped to
# avoid double-counting. Each suite's summary is matched by the FIRST pattern
# that fits, so a single line cannot contribute to two counters.
#
# Usage: aggregate-e2e-pass.sh [path-to-e2e-output.txt]
# Prints:  PASS=<n> FAIL=<n>
set -u
file=${1:-e2e-output.txt}
pass=0
fail=0
if [ ! -f "$file" ]; then
  echo "PASS=0 FAIL=0"
  exit 0
fi
while IFS= read -r line || [ -n "$line" ]; do
  # Skip per-test progress lines (leading whitespace + marker).
  case "$line" in
    *"✓"*|*"✗"*) continue;;
    " "*"PASS"*|" "*"FAIL"*) continue;;
  esac

  # "N/T tests passed[, M failed]"   (must come BEFORE "N passed" to avoid
  # capturing T as the pass count)
  if [[ "$line" =~ ([0-9]+)/[0-9]+\ tests\ passed(,\ ([0-9]+)\ failed)? ]]; then
    pass=$((pass + BASH_REMATCH[1]))
    if [ -n "${BASH_REMATCH[3]:-}" ]; then
      fail=$((fail + BASH_REMATCH[3]))
    fi
    continue
  fi
  # "N/T PASS"
  if [[ "$line" =~ ([0-9]+)/[0-9]+\ PASS ]]; then
    pass=$((pass + BASH_REMATCH[1]))
    continue
  fi
  # "N/T passed" (no "tests" between)
  if [[ "$line" =~ ([0-9]+)/[0-9]+\ passed ]]; then
    pass=$((pass + BASH_REMATCH[1]))
    continue
  fi
  # "N passed, M failed" / "N passed"  (most suites)
  if [[ "$line" =~ ([0-9]+)\ passed(,\ ([0-9]+)\ failed)? ]]; then
    pass=$((pass + BASH_REMATCH[1]))
    if [ -n "${BASH_REMATCH[3]:-}" ]; then
      fail=$((fail + BASH_REMATCH[3]))
    fi
    continue
  fi
  # "passed N failed M"
  if [[ "$line" =~ passed\ ([0-9]+)\ failed\ ([0-9]+) ]]; then
    pass=$((pass + BASH_REMATCH[1]))
    fail=$((fail + BASH_REMATCH[2]))
    continue
  fi
  # Standalone single-suite "<file>.js: PASS"
  if [[ "$line" =~ \.js:\ PASS$ ]]; then
    pass=$((pass + 1))
    continue
  fi
  # Standalone single-suite "<file>.js: FAIL ..."
  if [[ "$line" =~ \.js:\ FAIL ]]; then
    fail=$((fail + 1))
    continue
  fi
done < "$file"
echo "PASS=$pass FAIL=$fail"
