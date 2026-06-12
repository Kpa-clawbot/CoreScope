#!/usr/bin/env bash
# disk-monitor.sh — staging VM disk-usage monitor (issue #1684).
#
# Reads `df` for a mount point, classifies usage against thresholds, and
# emits a single line to stderr (and journald via systemd) at the matching
# severity. Designed to be invoked by a 15-minute systemd timer; output
# goes to journald which the operator can wire to alerts as needed.
#
# Pure-bash helpers (parse_df_percent, classify_threshold) are sourced by
# scripts/test-disk-monitor.sh — keep them side-effect free.

set -euo pipefail

# ----- pure helpers (testable) -----------------------------------------------

# parse_df_percent <df-output>
# Extracts the Use% column (column 5) from a 2-line `df -P` output and
# strips the trailing '%'. Echoes the integer percent (0-100). Returns
# non-zero if the input doesn't look like df output.
parse_df_percent() {
    local input="$1"
    # df -P guarantees a 2-line output: header + data. Take the last line.
    local data
    data="$(printf '%s\n' "$input" | tail -n1)"
    # Column 5 is Use% (e.g. "81%").
    local pct
    pct="$(printf '%s\n' "$data" | awk '{print $5}')"
    case "$pct" in
        *%) ;;
        *) return 1 ;;
    esac
    printf '%s\n' "${pct%\%}"
}

# classify_threshold <percent>
# Echoes one of: ok | warn | error | alert based on the issue #1684 spec:
#   <80 ok ; >=80 warn ; >=90 error ; >=95 alert
# Returns non-zero if input is not an integer 0-100.
classify_threshold() {
    local pct="$1"
    case "$pct" in
        ''|*[!0-9]*) return 1 ;;
    esac
    if [ "$pct" -lt 0 ] || [ "$pct" -gt 100 ]; then
        return 1
    fi
    if [ "$pct" -ge 95 ]; then
        echo alert
    elif [ "$pct" -ge 90 ]; then
        echo error
    elif [ "$pct" -ge 80 ]; then
        echo warn
    else
        echo ok
    fi
}

# severity_priority <severity>
# Echoes the syslog priority for `logger -p`. Maps to the canonical
# syslog severity ladder (RFC 5424): alert=1, crit=2, err=3, warning=4,
# info=6. We deliberately use `alert` (not `crit`) for the >=95% case so
# downstream `journalctl -p alert` filters fire at the highest level.
#   ok=info warn=warning error=err alert=alert
severity_priority() {
    case "$1" in
        ok)    echo user.info ;;
        warn)  echo user.warning ;;
        error) echo user.err ;;
        alert) echo user.alert ;;
        *)     return 1 ;;
    esac
}

# ----- main -----------------------------------------------------------------

main() {
    local mount="${1:-/}"
    local df_out
    df_out="$(df -P "$mount")"
    local pct severity prio
    pct="$(parse_df_percent "$df_out")"
    severity="$(classify_threshold "$pct")"
    prio="$(severity_priority "$severity")"
    local msg="disk-monitor mount=$mount used=${pct}% severity=$severity"
    # journald via systemd captures stderr; also emit through logger so
    # syslog-based collectors see the priority.
    echo "$msg" >&2
    if command -v logger >/dev/null 2>&1; then
        logger -t corescope-disk-monitor -p "$prio" -- "$msg"
    fi
    # Exit codes: 0 ok|warn, 1 error|alert (so timers can surface failures).
    case "$severity" in
        ok|warn) return 0 ;;
        *)       return 1 ;;
    esac
}

# Only run main when executed directly (not when sourced by tests).
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
