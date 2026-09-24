#!/usr/bin/env bash
# test-blacklist-sql.sh — unit tests for the §10.2 SQL construction in
# qa/scripts/blacklist-test.sh (issue #1977). Sources the script and exercises
# its pure helpers, plus a real local sqlite3 against throwaway fixture DBs.
#
# Run: bash qa/scripts/test-blacklist-sql.sh
# Exits non-zero if any case fails.
#
# The point of the sqlite3 group is that BOTH directions are asserted. A test
# that only checks "the injection payload returns 0" passes just as happily when
# the query is silently broken and returns 0 for everything, so the legitimate
# pubkey must be shown to still return the row it should.
#
# The fixture schema is NOT hand-written: it is the transmissions CREATE TABLE
# extracted from cmd/ingestor/db.go, and the query is also run against the
# committed staging-captured test-fixtures/e2e-fixture.db. An earlier version
# invented a `from_node` column that no CoreScope database has, and so passed
# while the real probe could never succeed.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
INGESTOR_DB_GO="$REPO_ROOT/cmd/ingestor/db.go"
REAL_FIXTURE="$REPO_ROOT/test-fixtures/e2e-fixture.db"
# shellcheck source=blacklist-test.sh
. "$SCRIPT_DIR/blacklist-test.sh"

PASS=0
FAIL=0

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [ "$expected" = "$actual" ]; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        echo "FAIL: $label — expected '$expected' got '$actual'" >&2
    fi
}

assert_match() {
    local label="$1" pattern="$2" actual="$3"
    if [[ "$actual" =~ $pattern ]]; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        echo "FAIL: $label — '$actual' does not match /$pattern/" >&2
    fi
}

assert_true() {
    local label="$1"; shift
    if "$@"; then PASS=$((PASS + 1)); else
        FAIL=$((FAIL + 1)); echo "FAIL: $label" >&2
    fi
}

contains() { [[ "$1" == *"$2"* ]]; }
lacks()    { [[ "$1" != *"$2"* ]]; }

# ----- sql_hex_literal ------------------------------------------------------
# The security property: whatever goes in, the SQL text it produces is drawn
# from [0-9a-f] only. No caller-supplied byte can close a string literal or add
# a dot-command argument. Needs no sqlite3, so this group always runs.
assert_eq "hex of deadbeef" "x'6465616462656566'" "$(sql_hex_literal deadbeef)"
assert_eq "hex of empty"    "x''"                 "$(sql_hex_literal "")"

HEX_ONLY="^x'[0-9a-f]*'\$"
assert_match "alphabet: sql quote payload" "$HEX_ONLY" "$(sql_hex_literal "' OR 1=1 --")"
assert_match "alphabet: drop table"        "$HEX_ONLY" "$(sql_hex_literal '"; DROP TABLE transmissions; --')"
assert_match "alphabet: backslash"         "$HEX_ONLY" "$(sql_hex_literal 'a\b')"
assert_match "alphabet: dollar and backtick" "$HEX_ONLY" "$(sql_hex_literal '$(id) `id`')"
assert_match "alphabet: embedded newline"  "$HEX_ONLY" "$(sql_hex_literal "$(printf 'a\nb')")"
assert_match "alphabet: multibyte"         "$HEX_ONLY" "$(sql_hex_literal 'héllo')"

# `od` without -v collapses runs of identical lines to '*'. A long repetitive
# value is the case that catches losing the flag.
LONG=$(printf 'x%.0s' $(seq 1 4096))
LONG_HEX=$(sql_hex_literal "$LONG")
assert_match "alphabet: 4096 repeated bytes" "$HEX_ONLY" "$LONG_HEX"
# 4096 bytes → 8192 hex digits, plus the 3 chars of x''. A collapsed run would
# be far shorter and would also fail the alphabet check on '*'.
assert_eq "no od line-collapse in 4096-byte value" "8192" "$(( ${#LONG_HEX} - 3 ))"

# ----- query text -------------------------------------------------------------
# Synthetic 32-byte pubkeys in the ingestor's form (hex.EncodeToString → lowercase).
PK_A="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
PK_B="fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
PK_ABSENT="00000000000000000000000000000000000000000000000000000000000000ff"
PK_A_UPPER=$(printf '%s' "$PK_A" | tr 'a-f' 'A-F')

COUNT_SQL=$(transmission_count_sql "$PK_A")
assert_true "query targets transmissions.from_pubkey" contains "$COUNT_SQL" "WHERE from_pubkey = lower(:pubkey)"
assert_true "query does not reference from_node"     lacks "$COUNT_SQL" "from_node"
assert_true "query text does not carry the raw pubkey" lacks "$COUNT_SQL" "$PK_A"

# ----- schema taken from the ingestor ------------------------------------------
# Pull the transmissions DDL out of cmd/ingestor/db.go instead of restating it.
transmissions_ddl() {
    awk '/CREATE TABLE IF NOT EXISTS transmissions \(/{p=1} p{print} p&&/^[[:space:]]*\);/{exit}' "$INGESTOR_DB_GO"
}
DDL=$(transmissions_ddl)
assert_match "ingestor DDL extracted" "CREATE TABLE IF NOT EXISTS transmissions \\(" "$DDL"
assert_match "ingestor DDL has from_pubkey" "from_pubkey[[:space:]]+TEXT" "$DDL"
assert_true  "ingestor DDL has no from_node" lacks "$DDL" "from_node"

# ----- against a real sqlite3 ----------------------------------------------
if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "SKIP: sqlite3 not on PATH — skipping the ${#SQLITE_ARGS[@]}-flag query group" >&2
    echo "      (the alphabet, query-text and schema assertions above still ran)" >&2
else
    FIXTURE_DIR=$(mktemp -d)
    trap 'rm -rf "$FIXTURE_DIR"' EXIT
    DB="$FIXTURE_DIR/fixture.db"
    LEGACY_DB="$FIXTURE_DIR/legacy-from-node.db"
    EMPTY_DB="$FIXTURE_DIR/no-table.db"

    # Schema-realistic fixture: the ingestor's own DDL. Three ADVERTs from A, one
    # from B, and two non-ADVERT rows whose from_pubkey is NULL (the ingestor only
    # attributes ADVERTs), so a structural injection has 6 rows to leak.
    printf '%s\n' "$DDL" | sqlite3 "$DB"
    sqlite3 "$DB" <<SQL
INSERT INTO transmissions(raw_hex,hash,first_seen,payload_type,from_pubkey) VALUES
  ('00','h1','2026-01-01T00:00:00Z',4,'$PK_A'),
  ('00','h2','2026-01-01T00:00:01Z',4,'$PK_A'),
  ('00','h3','2026-01-01T00:00:02Z',4,'$PK_A'),
  ('00','h4','2026-01-01T00:00:03Z',4,'$PK_B'),
  ('00','h5','2026-01-01T00:00:04Z',5,NULL),
  ('00','h6','2026-01-01T00:00:05Z',2,NULL);
SQL
    # The fixture the previous version of this test used: an invented column.
    sqlite3 "$LEGACY_DB" "CREATE TABLE transmissions(from_node TEXT); INSERT INTO transmissions VALUES('$PK_A');"
    sqlite3 "$EMPTY_DB" "CREATE TABLE unrelated(x);"

    run_local() { sqlite3 "${SQLITE_ARGS[@]}" "$1"; }
    count() { transmission_count_sql "$1" | run_local "$2"; }

    # The capability probe must round-trip on this machine, or the assertions
    # below would be testing nothing.
    assert_eq "probe round-trips" "$SQLITE_PROBE_TOKEN" "$(sqlite_probe_sql | run_local :memory:)"
    assert_eq "fixture really holds 6 rows" "6" \
        "$(run_local "$DB" <<<'SELECT COUNT(*) FROM transmissions;')"

    # POSITIVE CONTROL: a legitimate pubkey still returns its rows. Without this,
    # a silently broken query looks like a passing security fix.
    out=$(count "$PK_A" "$DB"); rc=$?
    assert_eq "legit pubkey → its 3 rows"  "3" "$out"
    assert_eq "legit pubkey → exit 0"      "0" "$rc"
    assert_eq "other legit pubkey → 1 row" "1" "$(count "$PK_B" "$DB")"
    assert_eq "upper-case pubkey matches (blacklist is case-insensitive)" "3" "$(count "$PK_A_UPPER" "$DB")"
    assert_eq "absent pubkey → 0"          "0" "$(count "$PK_ABSENT" "$DB")"
    assert_eq "prefix of a pubkey → 0 (exact match only)" "0" "$(count "${PK_A:0:16}" "$DB")"

    # NEGATIVE: payloads bind as literals that match nothing. A structural
    # injection would return 6 (or 4, the non-NULL rows), not 0.
    for payload in "' OR 1=1 --" "') OR 1=1 --" '" OR 1=1 --' \
                   "x' OR from_pubkey IS NOT NULL --" "$PK_A' OR '1'='1" \
                   '"); .shell id; --' "$(printf "a\n.shell id\nSELECT 99;")"; do
        out=$(count "$payload" "$DB" 2>&1); rc=$?
        assert_eq "payload binds literally: $(printf %q "$payload")" "0" "$out"
        assert_eq "payload exit 0: $(printf %q "$payload")" "0" "$rc"
    done

    # Interpolating the same payload the old way returns the whole table. This is
    # the behaviour the change removes; asserting it keeps the test honest about
    # what "0" above is worth.
    legacy="SELECT COUNT(*) FROM transmissions WHERE from_pubkey = '' OR 1=1 --';"
    assert_eq "interpolated form leaks the table" "6" "$(run_local "$DB" <<<"$legacy")"

    # Multibyte, whitespace, empty and long values bind as themselves.
    LONG_PK=$(printf 'ab%.0s' $(seq 1 5000))
    sqlite3 "$DB" "INSERT INTO transmissions(raw_hex,hash,first_seen,from_pubkey) VALUES
      ('00','m1','t','héllo wörld'),('00','m2','t',''),('00','m3','t','  '),('00','m4','t','$LONG_PK');"
    assert_eq "multibyte value with a space binds" "1" "$(count 'héllo wörld' "$DB")"
    assert_eq "empty value binds as ''"            "1" "$(count '' "$DB")"
    assert_eq "whitespace value binds as itself"   "1" "$(count '  ' "$DB")"
    assert_eq "10000-byte value binds"             "1" "$(count "$LONG_PK" "$DB")"

    # REAL DATA: the committed staging-captured fixture. Its most-attributed
    # pubkey, counted by the script's query, must equal a direct count.
    if [ -f "$REAL_FIXTURE" ]; then
        cp "$REAL_FIXTURE" "$FIXTURE_DIR/real.db"
        real_pk=$(run_local "$FIXTURE_DIR/real.db" <<<"SELECT from_pubkey FROM transmissions WHERE from_pubkey IS NOT NULL GROUP BY from_pubkey ORDER BY COUNT(*) DESC, from_pubkey LIMIT 1;")
        real_n=$(run_local "$FIXTURE_DIR/real.db" <<<"SELECT COUNT(*) FROM transmissions WHERE from_pubkey = '$real_pk';")
        assert_match "real fixture has an attributed pubkey" '^[0-9a-f]{64}$' "$real_pk"
        assert_match "real fixture count > 0" '^[1-9][0-9]*$' "$real_n"
        assert_eq "script query on real fixture" "$real_n" "$(count "$real_pk" "$FIXTURE_DIR/real.db")"
    else
        FAIL=$((FAIL + 1)); echo "FAIL: $REAL_FIXTURE missing" >&2
    fi

    # ERROR SURFACING: a broken query must be distinguishable from an empty
    # result — non-zero exit and something on stderr, not a silent "" or "0".
    for bad in "$LEGACY_DB:no such column: from_pubkey" "$EMPTY_DB:no such table: transmissions"; do
        bad_db=${bad%%:*}; want=${bad#*:}
        err_file="$FIXTURE_DIR/err"
        out=$(count "$PK_A" "$bad_db" 2>"$err_file"); rc=$?
        assert_true "$want → non-zero exit (got $rc)" test "$rc" -ne 0
        assert_true "$want → named on stderr" contains "$(cat "$err_file")" "$want"
        assert_eq   "$want → no count on stdout" "" "$out"
    done

    # ----- runner selection and transport, with ssh stubbed ---------------------
    # ssh_t is replaced by a stub that logs its argv, captures stdin, and runs the
    # remote command string through a real `bash -c`, so the script's own quoting
    # is what gets parsed. "docker exec -i NAME" is peeled off when the stub
    # container is said to have sqlite3; otherwise it fails like the app image.
    STUB_ARGV="$FIXTURE_DIR/ssh.argv"; STUB_STDIN="$FIXTURE_DIR/ssh.stdin"
    STUB_CONTAINER_SQLITE=0
    STUB_PATH="$PATH"
    ssh_t() {
        local cmd="$1" in
        printf '%s\n' "$*" >>"$STUB_ARGV"
        in=$(cat); printf '%s\n' "$in" >>"$STUB_STDIN"
        case "$cmd" in
            "docker exec -i "*)
                if [ "$STUB_CONTAINER_SQLITE" != 1 ]; then
                    echo 'exec: "sqlite3": executable file not found in $PATH' >&2; return 126
                fi
                cmd=${cmd#docker exec -i }; cmd=${cmd#* } ;;
        esac
        printf '%s\n' "$in" | PATH="$STUB_PATH" bash -c "$cmd"
    }
    reset_stub() { : >"$STUB_ARGV"; : >"$STUB_STDIN"; }
    TMP="$FIXTURE_DIR"; ADMIN_API_TOKEN=""; TARGET_CONTAINER="corescope-stub"
    TARGET_SSH_HOST="stub-host"; TARGET_DB_PATH="$DB"; TEST_PUBKEY="$PK_A"

    # Container has no sqlite3 → host runner, correct count.
    reset_stub
    read_retain_count >/dev/null 2>&1; rc=$?
    assert_eq "fallback: rc"           "0"    "$rc"
    assert_eq "fallback: runner"       "host" "$SQLITE_RUNNER"
    assert_eq "fallback: count"        "3"    "$RETAIN_COUNT"
    argv=$(cat "$STUB_ARGV"); stdin=$(cat "$STUB_STDIN")
    assert_true "transport: container tried first" contains "$(head -1 "$STUB_ARGV")" "docker exec -i corescope-stub sqlite3"
    assert_true "transport: pubkey not in remote argv" lacks "$argv" "$PK_A"
    assert_true "transport: SQL not in remote argv"    lacks "$argv" "SELECT"
    assert_true "transport: column not in remote argv" lacks "$argv" "from_pubkey"
    assert_true "transport: SQL arrives on stdin"      contains "$stdin" "SELECT COUNT(*) FROM transmissions WHERE from_pubkey = lower(:pubkey);"
    assert_true "transport: pubkey arrives hex-encoded on stdin" contains "$stdin" "$(sql_hex_literal "$PK_A")"
    assert_true "transport: raw pubkey never on stdin" lacks "$stdin" "$PK_A"

    # Container has sqlite3 → container runner.
    reset_stub; STUB_CONTAINER_SQLITE=1
    read_retain_count >/dev/null 2>&1; rc=$?
    assert_eq "container: rc"     "0"         "$rc"
    assert_eq "container: runner" "container" "$SQLITE_RUNNER"
    assert_eq "container: count"  "3"         "$RETAIN_COUNT"
    STUB_CONTAINER_SQLITE=0

    # A sqlite3 that cannot bind (.parameter prints help to stdout, exit 0,
    # parameter left NULL) must be rejected by the probe, not trusted.
    FAKE_BIN="$FIXTURE_DIR/fakebin"; mkdir -p "$FAKE_BIN"
    cat >"$FAKE_BIN/sqlite3" <<'FAKE'
#!/usr/bin/env bash
cat >/dev/null
echo ".parameter CMD ...       Manage SQL parameter bindings"
echo "0"
FAKE
    chmod +x "$FAKE_BIN/sqlite3"
    reset_stub; STUB_PATH="$FAKE_BIN:$PATH"
    read_retain_count >/dev/null 2>&1; rc=$?
    assert_eq "unbindable sqlite3: rejected" "1" "$rc"
    assert_eq "unbindable sqlite3: no runner" "" "$SQLITE_RUNNER"
    assert_eq "unbindable sqlite3: no count"  "" "$RETAIN_COUNT"
    STUB_PATH="$PATH"

    # A query error on the target is a failure with no count, never "0".
    reset_stub; TARGET_DB_PATH="$LEGACY_DB"
    read_retain_count >/dev/null 2>&1; rc=$?
    assert_eq "remote schema error: rc"    "1" "$rc"
    assert_eq "remote schema error: count" ""  "$RETAIN_COUNT"
    assert_true "remote schema error: stderr kept" contains "$(cat "$TMP/sqlite.err")" "no such column: from_pubkey"
    TARGET_DB_PATH="$DB"

    # run_sqlite with no resolved runner must refuse rather than guess.
    SQLITE_RUNNER=""
    if run_sqlite </dev/null >/dev/null 2>&1; then
        FAIL=$((FAIL + 1)); echo "FAIL: run_sqlite with no runner — expected non-zero exit" >&2
    else
        PASS=$((PASS + 1))
    fi
fi

echo "test-blacklist-sql.sh: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
