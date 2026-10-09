#!/usr/bin/env bash
# Native PostgreSQL regressions for the blacklist retention probe. No external
# endpoints are contacted: SSH is stubbed and all schemas/roles are disposable.
set -uo pipefail
set +x
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=blacklist-test.sh
. "$SCRIPT_DIR/blacklist-test.sh"
PYTHON="${CORESCOPE_QA_PYTHON:-python3}"
CONNECT="$SCRIPT_DIR/postgres-connect.py"
export QA_CONNECT="$CONNECT"
PASS=0; FAIL=0
assert_eq() {
  if [[ "$2" == "$3" ]]; then PASS=$((PASS+1)); else
    FAIL=$((FAIL+1)); printf 'FAIL: %s — expected %q got %q\n' "$1" "$2" "$3" >&2
  fi
}
assert_true() { local label="$1"; shift; if "$@"; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); echo "FAIL: $label" >&2; fi; }
contains() { [[ "$1" == *"$2"* ]]; }
lacks() { [[ "$1" != *"$2"* ]]; }
hex_only() { [[ "$1" =~ ^\'[0-9a-f]*\'$ ]]; }
assert_eq 'hex of deadbeef' "'6465616462656566'" "$(sql_hex_literal deadbeef)"
assert_eq 'hex of empty value' "''" "$(sql_hex_literal '')"
for payload in "' OR 1=1 --" '"; DROP TABLE transmissions; --' 'a\b' '$(id) `id`' "$(printf 'a\nb')" 'héllo'; do
  assert_true 'bound parameter alphabet is only hexadecimal' hex_only "$(sql_hex_literal "$payload")"
done
LONG=$(printf 'x%.0s' $(seq 1 4096)); LONG_HEX=$(sql_hex_literal "$LONG")
assert_true 'long bound parameter alphabet is hexadecimal' hex_only "$LONG_HEX"
assert_eq 'repeated lines are not collapsed by od' 8192 "$(( ${#LONG_HEX} - 2 ))"

command -v "$PYTHON" >/dev/null || { echo 'FAIL: Python 3 is required' >&2; exit 1; }
"$PYTHON" "$SCRIPT_DIR/test-postgres-connect.py" || exit 1
command -v psql >/dev/null || { echo 'FAIL: PostgreSQL 18 psql is required' >&2; exit 1; }
[[ -n "${CORESCOPE_TEST_POSTGRES_URL:-}" ]] || { echo 'FAIL: set CORESCOPE_TEST_POSTGRES_URL to a disposable PostgreSQL administrator URL' >&2; exit 1; }
FIXTURE_ROOT="$(cd "${TMPDIR:-/tmp}" && pwd -P)"
FIXTURE_DIR=$(mktemp -d "$FIXTURE_ROOT/corescope-qa-XXXXXX")
FIXTURE_DIR="$(cd "$FIXTURE_DIR" && pwd -P)"
SCHEMA="corescope_qa_${BASHPID}_${RANDOM}"
OWNER="${SCHEMA}_owner"; READER="${SCHEMA}_reader"
FIXTURE_PASSWORD=$(od -An -N24 -v -tx1 /dev/urandom | tr -d ' \n')
CREATED=0
admin_sql() { CORESCOPE_READER_DATABASE_URL="$CORESCOPE_TEST_POSTGRES_URL" "$PYTHON" "$CONNECT" | tr -d '\r'; }
cleanup() {
  local rc=$?
  if [[ "$CREATED" == 1 && "$SCHEMA" =~ ^corescope_qa_[0-9]+_[0-9]+$ ]]; then
    if ! admin_sql >/dev/null <<SQL
DROP SCHEMA $SCHEMA,${SCHEMA}_bad,${SCHEMA}_empty,${SCHEMA}_import CASCADE;
DROP OWNED BY $READER,$OWNER;
DROP ROLE $READER,$OWNER;
SQL
    then echo 'FAIL: disposable PostgreSQL cleanup failed' >&2; rc=1; fi
  fi
  case "$FIXTURE_DIR" in "$FIXTURE_ROOT"/corescope-qa-*) rm -rf -- "$FIXTURE_DIR" ;; *) echo 'FAIL: refusing unsafe temporary cleanup' >&2; rc=1 ;; esac
  exit "$rc"
}
trap cleanup EXIT
# Build private role URLs without putting the original URL/password in argv or
# printing either. The connection launcher validates the URL before psql runs.
set_role_url() {
  local base="$CORESCOPE_TEST_POSTGRES_URL" authority tail query part rest=""
  authority=${base#*://}; tail=${authority#*/}; authority=${authority%%/*}
  QA_ROLE_URL="${base%%://*}://$1:$FIXTURE_PASSWORD@${authority##*@}/${tail%%\?*}"
  if [[ "$tail" == *\?* ]]; then
    query=${tail#*\?}
    while [[ -n "$query" ]]; do
      part=${query%%&*}; if [[ "$query" == *'&'* ]]; then query=${query#*&}; else query=""; fi
      [[ "${part%%=*}" == search_path ]] || rest+="${part}&"
    done
  fi
  QA_ROLE_URL+="?${rest}search_path=$2"
}
if ! admin_sql >/dev/null <<SQL
BEGIN;
CREATE ROLE $OWNER LOGIN PASSWORD '$FIXTURE_PASSWORD';
CREATE ROLE $READER LOGIN PASSWORD '$FIXTURE_PASSWORD';
CREATE SCHEMA $SCHEMA AUTHORIZATION $OWNER;
CREATE SCHEMA ${SCHEMA}_bad AUTHORIZATION $OWNER;
CREATE SCHEMA ${SCHEMA}_empty AUTHORIZATION $OWNER;
CREATE SCHEMA ${SCHEMA}_import AUTHORIZATION $OWNER;
COMMIT;
SQL
then echo 'FAIL: cannot provision disposable PostgreSQL schemas/roles' >&2; exit 1; fi
CREATED=1
set_role_url "$OWNER" "$SCHEMA"; OWNER_URL="$QA_ROLE_URL"
set_role_url "$READER" "$SCHEMA"; READER_URL="$QA_ROLE_URL"
set_role_url "$READER" "${SCHEMA}_bad"; BAD_URL="$QA_ROLE_URL"
set_role_url "$READER" "${SCHEMA}_empty"; EMPTY_URL="$QA_ROLE_URL"
set_role_url "$OWNER" "${SCHEMA}_import"; IMPORT_OWNER_URL="$QA_ROLE_URL"
set_role_url "$READER" "${SCHEMA}_import"; IMPORT_READER_URL="$QA_ROLE_URL"
MIGRATOR="${CORESCOPE_QA_MIGRATOR:-$FIXTURE_DIR/migrate}"
PREPARER="${CORESCOPE_QA_PREPARER:-$FIXTURE_DIR/prepare-fixture}"
if [[ -z "${CORESCOPE_QA_MIGRATOR:-}" ]]; then
  (cd "$REPO_ROOT/cmd/migrate" && CGO_ENABLED=1 go build -o "$MIGRATOR" .) || exit 1
fi
if [[ -z "${CORESCOPE_QA_PREPARER:-}" ]]; then
  (cd "$REPO_ROOT/cmd/migrate" && CGO_ENABLED=1 go build -o "$PREPARER" ../../scripts/prepare-postgres-fixture.go) || exit 1
fi
if ! CORESCOPE_DATABASE_URL="$OWNER_URL" CORESCOPE_USERS_DATABASE_URL="" "$MIGRATOR" >"$FIXTURE_DIR/bootstrap.out" 2>"$FIXTURE_DIR/bootstrap.err"; then
  echo 'FAIL: shipping migration tool could not bootstrap the owner schema' >&2; exit 1
fi
PK_A="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
PK_B="fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
PK_ABSENT="00000000000000000000000000000000000000000000000000000000000000ff"
PK_A_UPPER=$(printf '%s' "$PK_A" | tr 'a-f' 'A-F')
LONG_PK=$(printf 'ab%.0s' $(seq 1 5000))
if ! admin_sql >/dev/null <<SQL
SET ROLE $OWNER;
SET search_path TO $SCHEMA;
INSERT INTO transmissions(raw_hex,hash,first_seen,payload_type,from_pubkey) VALUES
 ('00','h1','2026-01-01T00:00:00Z',4,'$PK_A'),('00','h2','2026-01-01T00:00:01Z',4,'$PK_A'),
 ('00','h3','2026-01-01T00:00:02Z',4,'$PK_A'),('00','h4','2026-01-01T00:00:03Z',4,'$PK_B'),
 ('00','h5','2026-01-01T00:00:04Z',5,NULL),('00','h6','2026-01-01T00:00:05Z',2,NULL);
CREATE TABLE ${SCHEMA}_bad.transmissions(from_node TEXT);
CREATE TABLE ${SCHEMA}_bad.corescope_schema AS TABLE corescope_schema;
CREATE TABLE ${SCHEMA}_empty.corescope_schema AS TABLE corescope_schema;
GRANT USAGE ON SCHEMA $SCHEMA,${SCHEMA}_bad,${SCHEMA}_empty,${SCHEMA}_import TO $READER;
GRANT SELECT ON ALL TABLES IN SCHEMA $SCHEMA,${SCHEMA}_bad,${SCHEMA}_empty TO $READER;
SQL
then echo 'FAIL: native fixture seeding failed' >&2; exit 1; fi
run_local() { CORESCOPE_READER_DATABASE_URL="$1" "$PYTHON" "$CONNECT" | tr -d '\r'; }
count() { transmission_count_sql "$1" | run_local "$2"; }

# Positive controls must succeed, otherwise zero-valued injection results prove
# nothing. These exercise the actual canonical schema and restricted LOGIN role.
assert_eq 'fixture holds six rows' 6 "$(run_local "$READER_URL" <<<'SELECT count(*) FROM transmissions;')"
out=$(count "$PK_A" "$READER_URL"); rc=$?
assert_eq 'legitimate pubkey has three rows' 3 "$out"
assert_eq 'legitimate pubkey returns success' 0 "$rc"
assert_eq 'native parameter capability probe round-trips' "$POSTGRES_PROBE_TOKEN" "$(postgres_probe_sql | run_local "$READER_URL")"
COUNT_SQL=$(transmission_count_sql "$PK_A")
assert_true 'query uses the real attribution column' contains "$COUNT_SQL" 'WHERE from_pubkey = lower(convert_from(decode($1'
assert_true 'query has no invented from_node column' lacks "$COUNT_SQL" from_node
assert_true 'query contains no raw pubkey' lacks "$COUNT_SQL" "$PK_A"
assert_eq 'other legitimate pubkey has one row' 1 "$(count "$PK_B" "$READER_URL")"
assert_eq 'upper-case key matches' 3 "$(count "$PK_A_UPPER" "$READER_URL")"
assert_eq 'absent key has no rows' 0 "$(count "$PK_ABSENT" "$READER_URL")"
assert_eq 'prefix is not an exact key' 0 "$(count "${PK_A:0:16}" "$READER_URL")"
for payload in "' OR 1=1 --" "') OR 1=1 --" '" OR 1=1 --' "x' OR from_pubkey IS NOT NULL --" "$PK_A' OR '1'='1" \
               '"); .shell id; --' "$(printf 'a\n.shell id\nSELECT 99;')" "$(printf 'a\n\\! id\nSELECT 99;')" '$(id) `id`' 'a\b'; do
  out=$(count "$payload" "$READER_URL" 2>"$FIXTURE_DIR/payload.err"); rc=$?
  assert_eq 'hostile pubkey is a literal, not SQL/meta-command/shell code' 0 "$out"
  assert_eq 'hostile literal query succeeds' 0 "$rc"
done
assert_eq 'unsafe interpolated control would leak all rows' 6 "$(run_local "$READER_URL" <<<"SELECT count(*) FROM transmissions WHERE from_pubkey = '' OR 1=1 --';")"
admin_sql >/dev/null <<SQL
INSERT INTO $SCHEMA.transmissions(raw_hex,hash,first_seen,from_pubkey) VALUES
 ('00','m1','t','héllo wörld'),('00','m2','t',''),('00','m3','t','  '),('00','m4','t','$LONG_PK');
SQL
assert_eq 'multibyte and space round-trip' 1 "$(count 'héllo wörld' "$READER_URL")"
assert_eq 'empty value round-trips' 1 "$(count '' "$READER_URL")"
assert_eq 'whitespace round-trips' 1 "$(count '  ' "$READER_URL")"
assert_eq '10000-byte repetitive value is not collapsed' 1 "$(count "$LONG_PK" "$READER_URL")"

# A broken schema is an error, never a false successful zero count.
for spec in "$BAD_URL|42703" "$EMPTY_URL|42P01"; do
  out=$(count "$PK_A" "${spec%|*}" 2>"$FIXTURE_DIR/schema.err"); rc=$?
  assert_true 'bad schema returns nonzero' test "$rc" -ne 0
  assert_true 'bad schema exposes its PostgreSQL error class' contains "$(cat "$FIXTURE_DIR/schema.err")" "${spec##*|}"
  assert_eq 'bad schema produces no count' '' "$out"
done
# Prove actual grants, independently of the read-only transaction setting.
out=$(run_local "$READER_URL" <<<'BEGIN READ WRITE; DELETE FROM transmissions;' 2>"$FIXTURE_DIR/write.err"); rc=$?
assert_true 'restricted reader cannot delete even in read-write transaction' test "$rc" -ne 0
assert_true 'delete fails for privilege, not transaction mode' contains "$(cat "$FIXTURE_DIR/write.err")" '42501'
out=$(count "$PK_A" "$OWNER_URL" 2>"$FIXTURE_DIR/owner.err"); rc=$?
assert_true 'owner credential refused by runtime probe' test "$rc" -ne 0
assert_eq 'owner refusal has no count' '' "$out"
admin_sql >/dev/null <<<"UPDATE $SCHEMA.corescope_schema SET ready=false;"
out=$(count "$PK_A" "$READER_URL" 2>"$FIXTURE_DIR/ready.err"); rc=$?
assert_true 'unfinished import refused' test "$rc" -ne 0
assert_eq 'unfinished import produces no count' '' "$out"
admin_sql >/dev/null <<<"UPDATE $SCHEMA.corescope_schema SET ready=true;"

# Preserve the committed fixture comparison through its explicit offline repair
# and shipping importer. The original SQLite file is never modified or queried
# as a runtime backend.
REAL_FIXTURE="$REPO_ROOT/test-fixtures/e2e-fixture.db"
if ! "$PREPARER" -source "$REAL_FIXTURE" -destination "$FIXTURE_DIR/prepared.sqlite" >"$FIXTURE_DIR/prepare.out" 2>"$FIXTURE_DIR/prepare.err"; then
  echo 'FAIL: offline historical fixture preparation failed' >&2; exit 1
fi
if ! CORESCOPE_DATABASE_URL="$IMPORT_OWNER_URL" CORESCOPE_USERS_DATABASE_URL="" "$MIGRATOR" -offline -from-sqlite "$FIXTURE_DIR/prepared.sqlite" -state-dir "$FIXTURE_DIR/import-state" >"$FIXTURE_DIR/import.out" 2>"$FIXTURE_DIR/import.err"; then
  echo 'FAIL: shipping importer failed for the historical fixture' >&2; exit 1
fi
admin_sql >/dev/null <<<"GRANT SELECT ON ALL TABLES IN SCHEMA ${SCHEMA}_import TO $READER;"
real_row=$(run_local "$IMPORT_READER_URL" <<<'SELECT from_pubkey,count(*) FROM transmissions WHERE from_pubkey IS NOT NULL GROUP BY from_pubkey ORDER BY count(*) DESC,from_pubkey LIMIT 1;')
real_pk=${real_row%|*}; real_n=${real_row##*|}
assert_true 'imported historical fixture has attributed pubkey' bash -c '[[ "$1" =~ ^[0-9a-f]{64}$ ]]' _ "$real_pk"
assert_true 'imported fixture has positive retained count' bash -c '[[ "$1" =~ ^[1-9][0-9]*$ ]]' _ "$real_n"
assert_eq 'bound query matches direct count on imported fixture' "$real_n" "$(count "$real_pk" "$IMPORT_READER_URL")"

# Transport uses the real query/client but stubs all SSH/container activity.
STUB_ARGV="$FIXTURE_DIR/ssh.argv"; STUB_STDIN="$FIXTURE_DIR/ssh.stdin"
STUB_CONTAINER=0
ssh_t() {
  local cmd="$1" input
  printf '%s\n' "$cmd" >>"$STUB_ARGV"
  input=$(cat); printf '%s\n' "$input" >>"$STUB_STDIN"
  case "$cmd" in
    'docker exec -i '*)
      if [[ "$STUB_CONTAINER" != 1 ]]; then echo 'client unavailable in stub container' >&2; return 127; fi
      cmd=${cmd#docker exec -i }; cmd=${cmd#* } ;;
  esac
  printf '%s\n' "$input" | CORESCOPE_READER_DATABASE_URL="$READER_URL" bash -c "$cmd"
}
reset_stub() { : >"$STUB_ARGV"; : >"$STUB_STDIN"; }
TMP="$FIXTURE_DIR"; ADMIN_API_TOKEN=''; TEST_PUBKEY="$PK_A"
TARGET_CONTAINER='corescope-stub'; TARGET_SSH_HOST='stub-host'
unset CORESCOPE_READER_DATABASE_URL PGSERVICE PGDATABASE
for runner in host container; do
  reset_stub; [[ "$runner" == container ]] && STUB_CONTAINER=1
  read_retain_count >/dev/null 2>"$FIXTURE_DIR/transport.err"; rc=$?
  assert_eq 'remote reader count succeeds' 0 "$rc"
  assert_eq 'resolved runner is expected target' "$runner" "${POSTGRES_RUNNER:-}"
  assert_eq 'transport preserves retained count' 3 "$RETAIN_COUNT"
  argv=$(cat "$STUB_ARGV"); input=$(cat "$STUB_STDIN")
  assert_true 'pubkey is absent from remote argv' lacks "$argv" "$PK_A"
  assert_true 'credentials are absent from remote argv' lacks "$argv" "$FIXTURE_PASSWORD"
  assert_true 'SQL is absent from remote argv' lacks "$argv" 'SELECT'
  assert_true 'native parameter query arrives on stdin' contains "$input" 'decode($1'
  assert_true 'raw pubkey is absent from stdin' lacks "$input" "$PK_A"
done
reset_stub
CORESCOPE_READER_DATABASE_URL="$READER_URL" read_retain_count >/dev/null 2>"$FIXTURE_DIR/local.err"; rc=$?
assert_eq 'explicit local reader URL succeeds' 0 "$rc"
assert_eq 'explicit URL selects local client' local "${POSTGRES_RUNNER:-}"
assert_eq 'explicit URL does not contact SSH' '' "$(cat "$STUB_ARGV")"
# Missing/unusable binding capability and query errors must fail closed.
ssh_t() { cat >/dev/null; printf '\\bind unsupported\n0\n'; }
read_retain_count >/dev/null 2>&1; rc=$?
assert_eq 'client that does not bind is rejected' 1 "$rc"
assert_eq 'failed capability leaves no count' '' "$RETAIN_COUNT"
POSTGRES_RUNNER=''
if run_postgres </dev/null >/dev/null 2>&1; then FAIL=$((FAIL+1)); echo 'FAIL: unresolved runner accepted' >&2; else PASS=$((PASS+1)); fi
for malformed in 'postgresql://test:secret-sentinel@/db' 'postgresql://test:secret-sentinel@localhost/db?unsupported=yes' 'postgresql://test:secret-sentinel%xx@localhost/db' "${READER_URL}&target_session_attrs=secret-sentinel"; do
  out=$(CORESCOPE_READER_DATABASE_URL="$malformed" "$PYTHON" "$CONNECT" </dev/null 2>&1); rc=$?
  assert_true 'malformed reader URL fails' test "$rc" -ne 0
  assert_true 'malformed URL never reveals credentials' lacks "$out" 'secret-sentinel'
done
# Native libpq configuration is also accepted without putting values in argv.
out=$(CORESCOPE_READER_DATABASE_URL="$READER_URL" "$PYTHON" -c 'import os,runpy,subprocess,sys; m=runpy.run_path(os.environ["QA_CONNECT"]); e=m["connection_env"](os.environ); e.pop("CORESCOPE_READER_DATABASE_URL",None); sys.exit(subprocess.run(["psql","-X","-qAt","-w","-v","ON_ERROR_STOP=1"],env=e).returncode)' <<<'SELECT 1977;' 2>"$FIXTURE_DIR/libpq.err" | tr -d '\r')
assert_eq 'private libpq fields reach the real database' 1977 "$out"
space_url=${READER_URL/search_path=$SCHEMA/search_path=$SCHEMA%2C%20pg_catalog}
assert_eq 'search_path option whitespace is escaped on the real client' "$SCHEMA, pg_catalog" "$(run_local "$space_url" <<<"SELECT current_setting('search_path');")"
echo "test-blacklist-sql.sh: $PASS passed, $FAIL failed"
[[ "$FAIL" == 0 ]]
