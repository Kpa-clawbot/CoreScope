#!/usr/bin/env bash
# disk-monitor.sh — staging VM disk-usage monitor (issue #1684).
# RED STUB: helpers exist so test can be executed and fail on assertion.
# Implementation lands in the GREEN commit.

set -euo pipefail

parse_df_percent() {
    # Stub: not implemented yet.
    echo "0"
}

classify_threshold() {
    # Stub: always returns "ok" so threshold assertions FAIL.
    echo "ok"
}

severity_priority() {
    # Stub: always returns "user.info".
    echo "user.info"
}

main() {
    echo "disk-monitor stub" >&2
    return 0
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
