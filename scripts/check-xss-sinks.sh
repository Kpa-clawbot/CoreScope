#!/usr/bin/env bash
# check-xss-sinks.sh — local mirror of the canonical pr-preflight gate at
# ~/.openclaw/skills/pr-preflight/scripts/check-xss-sinks.sh.
#
# THREAT MODEL: This gate targets HONEST AUTHORS who forget to escape a
# node-controlled field, NOT hostile authors trying to evade the gate.
# Known coverage gaps that an actively-malicious author could exploit
# (intentionally — out of scope, callable from a human review pass):
#   - bracket notation:        el['innerHTML'] = nodeName
#   - aliased writes:          const sink = el.innerHTML.bind(el); sink(...)
#   - deferred sink assignment via a helper indirection
#   - DOMPurify-bypass payloads inside an otherwise-escaped expression
# We accept these as residual risk in exchange for a regex-only gate that
# runs in <5s on every PR and surfaces actionable file:line evidence.
#
# Two modes:
#   $0 --file <path>       Scan a single file. Exit 1 if any flagged sink
#                          interpolates a node-controlled identifier
#                          without escapeHtml/escapeAttr/safeEsc/esc and is
#                          not covered by a same-PR DOM-grep test (passed
#                          via $PREFLIGHT_TEST_FILES, colon-separated)
#                          or a PR-body opt-out matching:
#                            PREFLIGHT-XSS-OPTOUT: <file>:<line> reason="<≥40ch>"
#                          AND the PR carries the `xss-optout` label (passed
#                          via $PREFLIGHT_PR_LABELS, space/comma-separated).
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

# Default allowlist — kept in lockstep with the skill-side
# data/xss-node-controlled-fields.txt. Add new node/MQTT-controlled
# fields to BOTH files.
DEFAULT_ALLOW='adv_name name observer observer_name sender from_node channel channel_name model firmware firmware_version client_version clientVersion radio iata hopNames nodeLabel obsName n.name o.name obs.name public_key pubkey area_key region_name text body message preview hash urlHash payload target origin topic display_name displayName nodeName alias nickname'

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

ALLOW_FILE="$(resolve_allowlist || true)"
if [ -n "$ALLOW_FILE" ]; then
  ALLOW_TOKENS=$(grep -vE '^\s*(#|$)' "$ALLOW_FILE" | tr '\n' ' ')
else
  ALLOW_TOKENS="$DEFAULT_ALLOW"
fi

# Python core — does the per-line sink detection, comment/string strip
# (preserving short identifier-like string contents and template-literal
# ${...} interpolations), per-interpolation escape-helper audit, and
# exception-property strip. Called ONCE per file for performance.
#
# Inputs (env):
#   XSS_ALLOW_TOKENS          space-separated identifier allowlist
#   XSS_FILE                  path of file being scanned (for output)
#   XSS_LINE_OFFSET           added to lineno in output (for --diff mode)
#   XSS_TEST_FILES            colon-separated list of same-PR test files
#                              that may carry coverage markers
#   XSS_PR_BODY               path to PR body file (for opt-out)
#   XSS_PR_LABELS             space/comma-separated PR labels (must include
#                              xss-optout for opt-out to apply)
#   XSS_INPUT_LINES           '1' = read tab-separated (lineno\tcontent)
#                              from stdin (diff mode); else read whole file
#
# Exit 0 = no findings; exit 1 = one or more findings.
PY_CORE=$(cat <<'PYEOF'
import os, re, sys

ALLOW_TOKENS = os.environ.get("XSS_ALLOW_TOKENS", "").split()
FILE = os.environ.get("XSS_FILE", "<stdin>")
LINE_OFFSET = int(os.environ.get("XSS_LINE_OFFSET", "0"))
TEST_FILES = [t for t in os.environ.get("XSS_TEST_FILES", "").split(":") if t]
PR_BODY = os.environ.get("XSS_PR_BODY", "")
PR_LABELS = re.split(r"[\s,]+", os.environ.get("XSS_PR_LABELS", "").strip())
INPUT_LINES_MODE = os.environ.get("XSS_INPUT_LINES", "") == "1"

# Build allowlist word-boundary regex.
allow_alt = "|".join(re.escape(t) for t in ALLOW_TOKENS if t)
ALLOW_RE = re.compile(rf"(?:^|[^A-Za-z0-9_$])({allow_alt})(?:[^A-Za-z0-9_]|$)") if allow_alt else None

# Sink patterns — each detects a sink and returns (label, rhs).
# For call-form sinks we use a paren-balanced extractor so the RHS
# captures the full argument list, including helper calls with nested
# parens like escapeHtml(n.name || x.slice(0, 12)).
ASSIGN_SINK_RE = re.compile(r"\.(innerHTML|outerHTML|srcdoc)\s*\+?=\s*([^;]*\S)")

def extract_call_args(line, call_re):
    """Find call_re match, then balance parens from the '(' to capture
    the full argument list. Returns (label_match, args_string) or None."""
    m = call_re.search(line)
    if not m:
        return None
    # The match must end at or just before the '(' — locate the next '('.
    paren_start = line.find("(", m.end() - 1)
    if paren_start < 0:
        return None
    depth = 0
    end = paren_start
    while end < len(line):
        c = line[end]
        if c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
            if depth == 0:
                break
        end += 1
    args = line[paren_start+1:end]
    return (m, args)

# Patterns that fire after a paren-balanced extraction.
CALL_SINK_PATTERNS = [
    # (regex matching up to the '(', label-builder taking (m, args))
    (re.compile(r"insertAdjacentHTML\s*\("),
     lambda m, args: ("insertAdjacentHTML(<rhs>)", _drop_first_arg(args))),
    (re.compile(r"\.(bindPopup|bindTooltip)\s*\("),
     lambda m, args: (f".{m.group(1)}(<rhs>)", args)),
    (re.compile(r"document\.(write|writeln)\s*\("),
     lambda m, args: (f"document.{m.group(1)}(<rhs>)", args)),
    (re.compile(r"\.setHTMLUnsafe\s*\("),
     lambda m, args: (".setHTMLUnsafe(<rhs>)", args)),
    (re.compile(r"\.createContextualFragment\s*\("),
     lambda m, args: (".createContextualFragment(<rhs>)", args)),
    (re.compile(r"\.setAttribute\s*\("),
     lambda m, args: _setattr_sink(args)),
]

def _drop_first_arg(args):
    # Find top-level comma, return tail.
    depth = 0
    for i, c in enumerate(args):
        if c == "(": depth += 1
        elif c == ")": depth -= 1
        elif c == "," and depth == 0:
            return args[i+1:]
    return ""

def _setattr_sink(args):
    # 1st arg = attribute name (quoted). 2nd onwards = RHS.
    am = re.match(r"\s*['\"]([A-Za-z][A-Za-z0-9_-]*)['\"]\s*,", args)
    if not am:
        return None
    attr = am.group(1)
    rest = args[am.end():]
    if re.fullmatch(r"on[a-z]+", attr):
        return (f"setAttribute('{attr}', <rhs>)", rest)
    if attr in ("href", "src", "action", "formaction"):
        return (f"setAttribute('{attr}', <rhs>)", rest)
    return None

def sink_match(line):
    # Assignment-form sinks first.
    am = ASSIGN_SINK_RE.search(line)
    if am:
        return (f".{am.group(1)}=<rhs>", am.group(2))
    # Call-form sinks with paren balancing.
    for call_re, build in CALL_SINK_PATTERNS:
        res = extract_call_args(line, call_re)
        if not res:
            continue
        m, args = res
        built = build(m, args)
        if built:
            return built
    return None

# Comment/string strip — preserves short identifier-like string contents
# (so setAttribute('href', ...) keeps its 'href' marker) and template
# literal ${...} interpolations (so the RHS audit can still see node IDs).
def strip(line):
    line = re.sub(r"/\*.*?\*/", "", line)
    line = re.sub(r"//[^\n]*", "", line)
    def short_str(m, q):
        body = m.group(1)
        if len(body) <= 32 and re.fullmatch(r"[A-Za-z0-9_:\-/.]+", body):
            return q + body + q
        return q + q
    line = re.sub(r"\"((?:[^\"\\]|\\.)*)\"", lambda m: short_str(m, '"'), line)
    line = re.sub(r"'((?:[^'\\]|\\.)*)'", lambda m: short_str(m, "'"), line)
    def tpl(m):
        body = m.group(1)
        parts = re.findall(r"\$\{[^}]*\}", body)
        return "`" + "".join(parts) + "`"
    line = re.sub(r"`((?:[^`\\]|\\.)*)`", tpl, line)
    return line

# Strip exception-property accesses (e.message, error.cause.stack,
# caughtError.message, myErr.cause.message etc.) — NOT node-controlled.
EXC_RE = re.compile(
    r"\b(?:"
    r"(?:e|err|ex|exc|exception|error)"
    r"|(?:[A-Za-z_][A-Za-z0-9_]*[Ee]rr(?:or)?)"
    r")(?:\.cause)?\.(?:message|stack|name|code|cause)\b"
)

# Peel escape-helper wrappers from a candidate. We balance parens so
# `escapeHtml(n.name || x.slice(0, 12))` is fully consumed (not truncated
# at the inner `(` like a naïve [^()]* would do).
HELPER_NAMES = ("escapeHtml", "escapeAttr", "safeEsc", "esc")
def peel_helpers(s):
    for _ in range(4):
        new_parts = []
        i = 0
        changed = False
        while i < len(s):
            matched = False
            for name in HELPER_NAMES:
                if s.startswith(name + "(", i) and (i == 0 or not (s[i-1].isalnum() or s[i-1] == "_" or s[i-1] == "$")):
                    # Balance parens from i+len(name).
                    j = i + len(name)
                    depth = 0
                    while j < len(s):
                        c = s[j]
                        if c == "(":
                            depth += 1
                        elif c == ")":
                            depth -= 1
                            if depth == 0:
                                j += 1
                                break
                        j += 1
                    if depth == 0:
                        # Consumed wrapper — emit nothing.
                        i = j
                        matched = True
                        changed = True
                        break
            if not matched:
                new_parts.append(s[i])
                i += 1
        s = "".join(new_parts)
        if not changed:
            break
    return s

# Extract candidates from a sink's RHS:
#   1. Each ${...} interpolation (audited INDEPENDENTLY)
#   2. Each `+ IDENT[.prop]*` concat fragment
#   3. If no interp/concat, the entire RHS (bare-ident form)
def audit_rhs(rhs):
    interps = re.findall(r"\$\{([^}]*)\}", rhs)
    concats = re.findall(r"\+\s*([A-Za-z_$][A-Za-z0-9_$.]*)", rhs)
    candidates = list(interps) + list(concats)
    if not candidates:
        candidates = [rhs]
    for cand in candidates:
        stripped = EXC_RE.sub("", cand)
        stripped = peel_helpers(stripped)
        if not ALLOW_RE:
            continue
        m = ALLOW_RE.search(stripped)
        if m:
            return m.group(1)
    return None

# Sham-test defense: a test file counts as coverage only if it contains
# the sink-file basename AND at least one audit marker OUTSIDE comments.
# (String literals OK — real DOM-grep tests put the payload in a string.)
TEST_MARKER_RE = re.compile(r"(' onfocus=|onerror=alert)")
def strip_test_comments(src):
    src = re.sub(r"/\*.*?\*/", "", src, flags=re.S)
    src = re.sub(r"//[^\n]*", "", src)
    return src

def test_covers(basename):
    for tf in TEST_FILES:
        if not os.path.isfile(tf):
            continue
        try:
            with open(tf, encoding="utf-8", errors="replace") as f:
                src = f.read()
        except Exception:
            continue
        if basename not in src:
            continue
        if TEST_MARKER_RE.search(strip_test_comments(src)):
            return True
    return False

def body_optout(file, lineno):
    if not PR_BODY or not os.path.isfile(PR_BODY):
        return False
    if "xss-optout" not in PR_LABELS:
        return False
    try:
        with open(PR_BODY, encoding="utf-8", errors="replace") as f:
            body = f.read()
    except Exception:
        return False
    pat = rf'PREFLIGHT-XSS-OPTOUT:\s*{re.escape(file)}:{lineno}\s+reason="([^"]*)"'
    m = re.search(pat, body)
    if not m:
        return False
    reason = m.group(1)
    if len(reason) < 40:
        sys.stderr.write(f"::warning::PREFLIGHT-XSS-OPTOUT at {file}:{lineno} "
                         f"rejected — reason has {len(reason)} chars, need ≥40\n")
        return False
    sys.stderr.write(f"::warning::PREFLIGHT-XSS-OPTOUT accepted at {file}:{lineno} "
                     f"(reason: {reason[:80]}…)\n")
    return True

def emit_finding(file, lineno, token, sink):
    if test_covers(os.path.basename(file)):
        print(f"ℹ️  {file}:{lineno}: flagged token '{token}' in {sink} — accepted via same-PR DOM-grep test")
        return False
    if body_optout(file, lineno):
        print(f"ℹ️  {file}:{lineno}: flagged token '{token}' in {sink} — author opt-out in PR body (xss-optout label + ≥40ch reason)")
        return False
    print(f"❌ {file}:{lineno}: flagged: {token}  (sink: {sink})")
    print(f"   fix: wrap with escapeHtml(...) / escapeAttr(...) — or add a DOM-grep test in test*.js asserting the payload renders inert — or add 'PREFLIGHT-XSS-OPTOUT: {file}:{lineno} reason=\"...(≥40 chars)...\"' to the PR body AND apply the xss-optout label.")
    return True

def scan_lines(lines_with_no):
    fail = False
    for lineno, content in lines_with_no:
        if not content.strip():
            continue
        stripped = strip(content)
        sm = sink_match(stripped)
        if not sm:
            continue
        sink, rhs = sm
        token = audit_rhs(rhs)
        if not token:
            continue
        if emit_finding(FILE, lineno + LINE_OFFSET, token, sink):
            fail = True
    return 1 if fail else 0

if INPUT_LINES_MODE:
    pairs = []
    for raw in sys.stdin:
        raw = raw.rstrip("\n")
        if "\t" not in raw:
            continue
        ln, content = raw.split("\t", 1)
        try:
            pairs.append((int(ln), content))
        except ValueError:
            continue
    sys.exit(scan_lines(pairs))
else:
    # Whole-file mode.
    if not os.path.isfile(FILE):
        sys.stderr.write(f"no such file: {FILE}\n")
        sys.exit(2)
    with open(FILE, encoding="utf-8", errors="replace") as f:
        text = f.read()
    pairs = [(i + 1, line) for i, line in enumerate(text.split("\n"))]
    sys.exit(scan_lines(pairs))
PYEOF
)

scan_file() {
  local target="$1" offset="${2:-0}"
  XSS_ALLOW_TOKENS="$ALLOW_TOKENS" \
  XSS_FILE="$target" \
  XSS_LINE_OFFSET="$offset" \
  XSS_TEST_FILES="${PREFLIGHT_TEST_FILES:-}" \
  XSS_PR_BODY="${PREFLIGHT_PR_BODY:-}" \
  XSS_PR_LABELS="${PREFLIGHT_PR_LABELS:-}" \
  XSS_INPUT_LINES="" \
  python3 -c "$PY_CORE"
}

scan_diff() {
  local base="$1"
  local files
  files=$(git diff "$base"...HEAD --name-only --diff-filter=AM \
            | grep -E '^public/.*\.(js|html)$' || true)
  [ -z "$files" ] && { echo "check-xss-sinks: no public/**/*.{js,html} changes to scan"; return 0; }
  local rc=0
  local file
  while IFS= read -r file; do
    [ -z "$file" ] && continue
    local diff_lines
    diff_lines=$(git diff --unified=0 "$base"...HEAD -- "$file" | awk '
      /^@@/ {
        match($0, /\+[0-9]+/)
        if (RSTART) { cur = substr($0, RSTART+1, RLENGTH-1) + 0 } else { cur = 0 }
        next
      }
      /^\+\+\+/ { next }
      /^\+/ { print cur "\t" substr($0, 2); cur++; next }
      /^-/  { next }
      /^ /  { cur++ }
    ')
    [ -z "$diff_lines" ] && continue
    local sub_rc=0
    printf '%s\n' "$diff_lines" | \
      XSS_ALLOW_TOKENS="$ALLOW_TOKENS" \
      XSS_FILE="$file" \
      XSS_LINE_OFFSET=0 \
      XSS_TEST_FILES="${PREFLIGHT_TEST_FILES:-}" \
      XSS_PR_BODY="${PREFLIGHT_PR_BODY:-}" \
      XSS_PR_LABELS="${PREFLIGHT_PR_LABELS:-}" \
      XSS_INPUT_LINES=1 \
      python3 -c "$PY_CORE" || sub_rc=$?
    [ "$sub_rc" -ne 0 ] && rc=1
  done <<<"$files"
  return $rc
}

mode="${1:-}"
shift || true
case "$mode" in
  --file)
    target="${1:-}"
    [ -z "$target" ] && { echo "usage: $0 --file <path>" >&2; exit 2; }
    [ -f "$target" ] || { echo "no such file: $target" >&2; exit 2; }
    scan_file "$target" 0
    exit $?
    ;;
  --diff)
    base="${1:-${BASE:-origin/master}}"
    scan_diff "$base"
    exit $?
    ;;
  *)
    echo "usage: $0 --file <path>   |   $0 --diff [BASE]" >&2
    exit 2
    ;;
esac
