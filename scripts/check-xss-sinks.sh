#!/usr/bin/env bash
# check-xss-sinks.sh — local mirror of the canonical pr-preflight gate at
# ~/.openclaw/skills/pr-preflight/scripts/check-xss-sinks.sh.
#
# Two modes:
#   $0 --file <path>       Scan a single file. Exit 1 if any flagged sink
#                          interpolates a node-controlled identifier
#                          without escapeHtml/escapeAttr/safeEsc and is
#                          not covered by a same-PR DOM-grep test (passed
#                          via $PREFLIGHT_TEST_FILES, colon-separated)
#                          or a PR-body opt-out (file matching the
#                          \`PREFLIGHT-XSS-OPTOUT: <file>:<line> reason="…"\`
#                          pattern, path in $PREFLIGHT_PR_BODY).
#   $0 --diff [BASE]       Walk git diff $BASE...HEAD for public/**/*.{js,html}
#                          and apply the same rules to added lines only.
#                          BASE defaults to origin/master.
#
# The canonical pr-preflight gate (skill-side) consumes the same allowlist
# format documented inline below.
#
# Allowlist resolution (first hit wins):
#   $XSS_ALLOWLIST                                 (explicit override)
#   ~/.openclaw/skills/pr-preflight/data/xss-node-controlled-fields.txt
#   scripts/check-xss-sinks.allowlist.txt          (repo-local fallback)
#   built-in default below                         (minimum viable set)

set -u

DEFAULT_ALLOW='adv_name name observer observer_name sender from_node channel channel_name model firmware client_version radio iata hopNames nodeLabel obsName n.name o.name obs.name public_key pubkey area_key region_name text body message preview hash urlHash'

resolve_allowlist() {
  local candidates=(
    "${XSS_ALLOWLIST:-}"
    "$HOME/.openclaw/skills/pr-preflight/data/xss-node-controlled-fields.txt"
    "$(git rev-parse --show-toplevel 2>/dev/null)/scripts/check-xss-sinks.allowlist.txt"
  )
  for c in "${candidates[@]}"; do
    [ -n "$c" ] && [ -f "$c" ] && { echo "$c"; return 0; }
  done
  return 1
}

build_allow_re() {
  local allow_file="$1"
  if [ -n "$allow_file" ]; then
    grep -vE '^\s*(#|$)' "$allow_file" \
      | sed 's/[][\.^$*+?(){}|]/\\&/g' \
      | paste -sd'|' -
  else
    # built-in fallback
    echo "$DEFAULT_ALLOW" | tr ' ' '\n' \
      | sed 's/[][\.^$*+?(){}|]/\\&/g' \
      | paste -sd'|' -
  fi
}

ALLOW_FILE="$(resolve_allowlist || true)"
ALLOW_RE="$(build_allow_re "$ALLOW_FILE")"
ALLOW_WORD_RE="(^|[^A-Za-z0-9_\$])(${ALLOW_RE})([^A-Za-z0-9_]|\$)"

sink_match() {
  local line="$1"
  echo "$line" | grep -qE '\.innerHTML[[:space:]]*\+?=[[:space:]]*`'          && { echo 'innerHTML=`tpl`'; return 0; }
  echo "$line" | grep -qE "\.innerHTML[[:space:]]*\+?=[[:space:]]*['\"]"      && { echo "innerHTML='str'"; return 0; }
  echo "$line" | grep -qE 'insertAdjacentHTML\s*\(.*,\s*`'                    && { echo 'insertAdjacentHTML(`tpl`)'; return 0; }
  echo "$line" | grep -qE '\.(bindPopup|bindTooltip)\(\s*`'                   && { echo 'bindPopup/bindTooltip(`tpl`)'; return 0; }
  echo "$line" | grep -qE "\.setAttribute\(['\"]on[a-z]+['\"]"                && { echo "setAttribute('on*')"; return 0; }
  echo "$line" | grep -qE "\.setAttribute\(['\"](href|src|action|formaction)['\"]\s*,\s*[^'\")]*\\\$" && { echo "setAttribute(url,\$interp)"; return 0; }
  return 1
}

allow_match() {
  local line="$1"
  # Restrict the search to DYNAMIC substrings only — otherwise allowlist
  # tokens like 'text' false-positive against static CSS class strings
  # ("text-center", "text-muted"). Dynamic substrings are:
  #   - everything inside ${...}                (template-literal interp)
  #   - everything after a + operator           (string concat)
  #   - the 2nd-onward arg to setAttribute(...) (URL/event attr value)
  local dyn
  dyn=$(
    # ${...} groups
    echo "$line" | grep -oE '\$\{[^}]*\}'
    # tokens after + (string concat). Capture: + IDENT (allow .property).
    echo "$line" | grep -oE '\+[[:space:]]*[A-Za-z_\$][A-Za-z0-9_\$.]*'
    # setAttribute 2nd arg onwards: split on first comma after setAttribute(
    echo "$line" | sed -nE "s/.*\.setAttribute\([^,]*,(.*)/\1/p"
  )
  [ -z "$dyn" ] && return 0
  # Strip exception/error-object property accesses — these surface in
  # catch-block error-rendering paths and are NOT node-controlled.
  # Without this filter, ${e.message}, ${err.message}, ${error.stack}
  # etc. trip the 'message' allowlist token across every page.
  dyn=$(echo "$dyn" | sed -E 's/\b(e|err|error|ex|exc|exception)\.(message|stack|name|code|cause)\b//g')
  echo "$dyn" | grep -oE "$ALLOW_WORD_RE" | head -1 \
    | sed -E "s/^[^A-Za-z0-9_\$]?//; s/[^A-Za-z0-9_]?\$//"
}

has_escape() {
  # Accept canonical helpers PLUS the local alias 'esc(' that several
  # post-#1537 files adopt as a shorthand for escapeHtml.
  echo "$1" | grep -qE '(escapeHtml|escapeAttr|safeEsc|\besc)\s*\('
}

test_covers() {
  local basename="$1"
  local files=()
  if [ -n "${PREFLIGHT_TEST_FILES:-}" ]; then
    IFS=':' read -r -a files <<< "$PREFLIGHT_TEST_FILES"
  else
    local listed
    listed=$(git diff "${BASE:-origin/master}"...HEAD --name-only --diff-filter=AM 2>/dev/null \
              | grep -E '(^|/)(test|tests/)[^/]*\.js$|^test[^/]*\.js$' || true)
    [ -z "$listed" ] && return 1
    while IFS= read -r t; do files+=("$t"); done <<<"$listed"
  fi
  for tf in "${files[@]}"; do
    [ -f "$tf" ] || continue
    if grep -qF "$basename" "$tf" && grep -qE "(' onfocus=|onerror=alert)" "$tf"; then
      return 0
    fi
  done
  return 1
}

body_optout() {
  local file="$1" lineno="$2"
  [ -n "${PREFLIGHT_PR_BODY:-}" ] && [ -f "$PREFLIGHT_PR_BODY" ] || return 1
  grep -qE "PREFLIGHT-XSS-OPTOUT:[[:space:]]*${file//\//\\/}:${lineno}[[:space:]]+reason=" "$PREFLIGHT_PR_BODY"
}

# Scan whole file (used by --file mode). Emits findings; returns 1 if any.
scan_file_lines() {
  local file="$1" line_offset="${2:-0}"
  local lineno=0
  local fail=0
  local basename; basename=$(basename "$file")
  while IFS= read -r content; do
    lineno=$((lineno + 1))
    [ -z "$content" ] && continue
    sink=$(sink_match "$content") || continue
    token=$(allow_match "$content")
    [ -z "$token" ] && continue
    has_escape "$content" && continue
    if test_covers "$basename"; then
      echo "ℹ️  $file:$((lineno + line_offset)): flagged token '$token' in $sink — accepted via same-PR DOM-grep test"
      continue
    fi
    if body_optout "$file" "$((lineno + line_offset))"; then
      echo "ℹ️  $file:$((lineno + line_offset)): flagged token '$token' in $sink — author opt-out in PR body"
      continue
    fi
    echo "❌ $file:$((lineno + line_offset)): flagged: $token  (sink: $sink)"
    echo "   fix: wrap with escapeHtml(...) / escapeAttr(...) — or add a DOM-grep test in test*.js asserting the payload renders inert — or add 'PREFLIGHT-XSS-OPTOUT: $file:$((lineno + line_offset)) reason=\"...\"' to the PR body."
    fail=1
  done < "$file"
  return $fail
}

# Scan added lines from git diff (used by --diff mode).
scan_diff() {
  local base="$1"
  local files
  files=$(git diff "$base"...HEAD --name-only --diff-filter=AM \
            | grep -E '^public/.*\.(js|html)$' || true)
  [ -z "$files" ] && { echo "check-xss-sinks: no public/**/*.{js,html} changes to scan"; return 0; }
  local tmp; tmp=$(mktemp)
  local rc=0
  while IFS= read -r file; do
    while IFS=$'\t' read -r lineno content; do
      [ -z "$content" ] && continue
      sink=$(sink_match "$content") || continue
      token=$(allow_match "$content")
      [ -z "$token" ] && continue
      has_escape "$content" && continue
      local basename; basename=$(basename "$file")
      if test_covers "$basename"; then
        echo "ℹ️  $file:$lineno: flagged token '$token' in $sink — accepted via same-PR DOM-grep test"
        continue
      fi
      if body_optout "$file" "$lineno"; then
        echo "ℹ️  $file:$lineno: flagged token '$token' in $sink — author opt-out in PR body"
        continue
      fi
      echo "❌ $file:$lineno: flagged: $token  (sink: $sink)"
      echo "   fix: wrap with escapeHtml(...) / escapeAttr(...) — or add a DOM-grep test in test*.js asserting the payload renders inert — or add 'PREFLIGHT-XSS-OPTOUT: $file:$lineno reason=\"...\"' to the PR body."
      echo "1" >> "$tmp"
    done < <(awk '
      /^@@/ {
        match($0, /\+[0-9]+/)
        if (RSTART) { cur = substr($0, RSTART+1, RLENGTH-1) + 0 } else { cur = 0 }
        next
      }
      /^\+\+\+/ { next }
      /^\+/ { print cur "\t" substr($0, 2); cur++; next }
      /^-/  { next }
      /^ /  { cur++ }
    ' <(git diff --unified=0 "$base"...HEAD -- "$file"))
  done <<<"$files"
  [ -s "$tmp" ] && rc=1
  rm -f "$tmp"
  return $rc
}

mode="${1:-}"
shift || true
case "$mode" in
  --file)
    target="${1:-}"
    [ -z "$target" ] && { echo "usage: $0 --file <path>" >&2; exit 2; }
    [ -f "$target" ] || { echo "no such file: $target" >&2; exit 2; }
    scan_file_lines "$target" 0 || exit 1
    exit 0
    ;;
  --diff)
    base="${1:-${BASE:-origin/master}}"
    scan_diff "$base" || exit 1
    exit 0
    ;;
  *)
    echo "usage: $0 --file <path>   |   $0 --diff [BASE]" >&2
    exit 2
    ;;
esac
