#!/usr/bin/env bash
# test-select-service-user.sh — unit tests for docker/select-service-user.sh,
# the uid selection the container entrypoint performs at start.
#
# Every branch is exercised without docker and without root: getent, adduser,
# addgroup, deluser, delgroup, id, chown, find and stat are replaced by stubs
# on PATH. The account stubs read and write a passwd/group pair of files seeded
# with the accounts the real image has — verified against alpine:3.20 with
# mosquitto, supervisor and caddy installed, plus the Dockerfile's corescope —
# so an account created mid-run is visible to the lookups that follow it. chown
# and find record their arguments rather than touching the filesystem, which is
# what lets the ownership hand-over be asserted exactly.
#
# The driver runs the same two steps in the same order as
# docker/entrypoint-go.sh, so what gets tested is the contract the entrypoint
# depends on: stdout carries only the account name, and a non-zero return must
# never be read as root.
#
# Run: bash docker/test-select-service-user.sh
# Exits non-zero if any case fails.
set -u

SCRIPT="$(cd "$(dirname "$0")" && pwd)/select-service-user.sh"
[ -f "$SCRIPT" ] || { echo "FAIL: $SCRIPT missing" >&2; exit 1; }

# Accounts the image actually has. uid:gid differ for caddy and mosquitto,
# which is what makes the "uid already belongs to an account" branch testable.
IMAGE_PASSWD='root:0:0
caddy:100:101
mosquitto:101:102
corescope:1000:1000'
IMAGE_GROUP='root:0
caddy:101
mosquitto:102
corescope:1000'

PASS=0
FAIL=0
CASE=""

fail() {
    FAIL=$((FAIL + 1))
    echo "FAIL: $CASE — $1" >&2
}

assert_eq() {
    if [ "$2" = "$3" ]; then PASS=$((PASS + 1)); else
        fail "$1 — expected '$2', got '$3'"
    fi
}

assert_has() {
    case "$2" in
        *"$3"*) PASS=$((PASS + 1)) ;;
        *) fail "$1 — expected to find '$3' in: $2" ;;
    esac
}

assert_lacks() {
    case "$2" in
        *"$3"*) fail "$1 — did not expect '$3' in: $2" ;;
        *) PASS=$((PASS + 1)) ;;
    esac
}

# ----- stub environment ------------------------------------------------------

make_fixture() {
    FIX=$(mktemp -d)
    printf '%s\n' "$IMAGE_PASSWD" > "$FIX/passwd"
    printf '%s\n' "$IMAGE_GROUP" > "$FIX/group"
    : > "$FIX/calls"
    : > "$FIX/log"
    mkdir -p "$FIX/bin"

    cat > "$FIX/bin/getent" <<'STUB'
#!/bin/sh
case "$1" in
  passwd)
    while IFS=: read -r name uid gid; do
      [ -n "$name" ] || continue
      if [ "$2" = "$name" ] || [ "$2" = "$uid" ]; then
        echo "$name:x:$uid:$gid::/:/sbin/nologin"; exit 0
      fi
    done < "$FIX/passwd" ;;
  group)
    while IFS=: read -r name gid; do
      [ -n "$name" ] || continue
      if [ "$2" = "$name" ] || [ "$2" = "$gid" ]; then
        echo "$name:x:$gid:"; exit 0
      fi
    done < "$FIX/group" ;;
esac
exit 2
STUB

    cat > "$FIX/bin/adduser" <<'STUB'
#!/bin/sh
[ "${STUB_ADDUSER_FAIL:-0}" = 1 ] && exit 1
uid=""; grp=""; name=""
while [ $# -gt 0 ]; do
  case "$1" in
    -u) uid="$2"; shift 2 ;;
    -G) grp="$2"; shift 2 ;;
    -h|-s) shift 2 ;;
    -S|-D|-H) shift ;;
    *) name="$1"; shift ;;
  esac
done
[ -n "$uid" ] && [ -n "$name" ] && [ -n "$grp" ] || exit 1
# busybox refuses a uid that is already taken
awk -F: -v u="$uid" '$2==u{found=1} END{exit !found}' "$FIX/passwd" && exit 1
gid=$(awk -F: -v g="$grp" '$1==g{print $2}' "$FIX/group")
[ -n "$gid" ] || exit 1
echo "$name:$uid:$gid" >> "$FIX/passwd"
echo "adduser $name uid=$uid gid=$gid" >> "$FIX/calls"
STUB

    cat > "$FIX/bin/addgroup" <<'STUB'
#!/bin/sh
[ "${STUB_ADDGROUP_FAIL:-0}" = 1 ] && exit 1
gid=""; name=""
while [ $# -gt 0 ]; do
  case "$1" in
    -g) gid="$2"; shift 2 ;;
    -S|-D) shift ;;
    *) name="$1"; shift ;;
  esac
done
[ -n "$gid" ] && [ -n "$name" ] || exit 1
awk -F: -v g="$gid" '$2==g{found=1} END{exit !found}' "$FIX/group" && exit 1
echo "$name:$gid" >> "$FIX/group"
echo "addgroup $name gid=$gid" >> "$FIX/calls"
STUB

    cat > "$FIX/bin/deluser" <<'STUB'
#!/bin/sh
grep -v "^$1:" "$FIX/passwd" > "$FIX/passwd.new" || true
mv "$FIX/passwd.new" "$FIX/passwd"
echo "deluser $1" >> "$FIX/calls"
STUB

    cat > "$FIX/bin/delgroup" <<'STUB'
#!/bin/sh
grep -v "^$1:" "$FIX/group" > "$FIX/group.new" || true
mv "$FIX/group.new" "$FIX/group"
echo "delgroup $1" >> "$FIX/calls"
STUB

    cat > "$FIX/bin/id" <<'STUB'
#!/bin/sh
while IFS=: read -r name uid gid; do
  if [ "$2" = "$name" ]; then
    case "$1" in
      -u) echo "$uid"; exit 0 ;;
      -g) echo "$gid"; exit 0 ;;
    esac
    exit 2
  fi
done < "$FIX/passwd"
exit 1
STUB

    cat > "$FIX/bin/chown" <<'STUB'
#!/bin/sh
echo "chown $*" >> "$FIX/calls"
STUB

    cat > "$FIX/bin/find" <<'STUB'
#!/bin/sh
echo "find $*" >> "$FIX/calls"
STUB

    # Reporting a non-root owner for /app/data: the uid must still not be
    # inferred from it. Any call is recorded so the test can prove none happens.
    cat > "$FIX/bin/stat" <<'STUB'
#!/bin/sh
echo "stat $*" >> "$FIX/calls"
echo 1000
STUB

    chmod +x "$FIX"/bin/*
}

# drive [VAR=VAL ...] — resolve the account and claim the paths, in the order
# and with the error handling docker/entrypoint-go.sh uses. env -i keeps the
# host environment (a RUN_AS_UID of its own, a real PATH) out of the case.
drive() {
    make_fixture
    OUT=$(env -i PATH="$FIX/bin:/usr/bin:/bin" FIX="$FIX" SCRIPT="$SCRIPT" "$@" sh -c '
        . "$SCRIPT"
        account=$(resolve_service_account) || exit 1
        [ "$account" = root ] || claim_service_paths "$account"
        echo "$account"
    ' 2>"$FIX/log")
    RC=$?
    LOG=$(cat "$FIX/log")
    CALLS=$(cat "$FIX/calls")
}

# ----- 1. RUN_AS_UID unset: root, exactly as earlier releases ----------------
CASE="RUN_AS_UID unset"
drive
assert_eq "account" "root" "$OUT"
assert_eq "exit status" 0 "$RC"
assert_eq "nothing was chowned" "" "$CALLS"
assert_has "log explains the default" "$LOG" "RUN_AS_UID is unset"

# ----- 2. the dropped inferred branch must not come back ---------------------
# The data directory is reported as owned by uid 1000 (`mkdir data` + bind
# mount, the ordinary setup). Earlier revisions of this change ran the services
# as that uid, which broke every upgrade whose data directory held root-owned
# database files. Staying root here is the point of the whole arrangement.
CASE="non-root data dir, RUN_AS_UID unset"
drive
assert_eq "account" "root" "$OUT"
assert_lacks "data dir owner never consulted" "$CALLS" "stat"
assert_eq "nothing was chowned" "" "$CALLS"

# ----- 3. RUN_AS_UID=1000 reuses the account baked into the image ------------
CASE="RUN_AS_UID=1000"
drive RUN_AS_UID=1000 RUN_AS_GID=1000
assert_eq "account" "corescope" "$OUT"
assert_eq "exit status" 0 "$RC"
assert_has "app dir handed over" "$CALLS" "chown 1000:1000 /app"
assert_has "data dir handed over" "$CALLS" \
    "find /app/data ! -user 1000 -exec chown 1000:1000 {} +"
assert_lacks "no account created" "$CALLS" "adduser"
assert_lacks "baked-in account kept" "$CALLS" "deluser"

# ----- 4. RUN_AS_GID defaults to RUN_AS_UID ---------------------------------
CASE="RUN_AS_UID=1000, RUN_AS_GID unset"
drive RUN_AS_UID=1000
assert_eq "account" "corescope" "$OUT"
assert_has "gid defaulted to the uid" "$CALLS" "chown 1000:1000 /app"
assert_lacks "no group mismatch reported" "$LOG" "note:"

# ----- 5. a uid nothing holds: the account is recreated at it ----------------
CASE="RUN_AS_UID=5000"
drive RUN_AS_UID=5000
assert_eq "account" "corescope" "$OUT"
assert_eq "exit status" 0 "$RC"
assert_has "baked-in account freed" "$CALLS" "deluser corescope"
assert_has "baked-in group freed" "$CALLS" "delgroup corescope"
assert_has "group created at the gid" "$CALLS" "addgroup corescope gid=5000"
assert_has "account created at the uid" "$CALLS" "adduser corescope uid=5000 gid=5000"
assert_has "app dir handed over" "$CALLS" "chown 5000:5000 /app"

# ----- 6. a separate RUN_AS_GID ---------------------------------------------
CASE="RUN_AS_UID=5000, RUN_AS_GID=6000"
drive RUN_AS_UID=5000 RUN_AS_GID=6000
assert_eq "account" "corescope" "$OUT"
assert_has "group created at the gid" "$CALLS" "addgroup corescope gid=6000"
assert_has "account created with that group" "$CALLS" "adduser corescope uid=5000 gid=6000"
assert_has "app dir handed over" "$CALLS" "chown 5000:6000 /app"

# ----- 7. a uid an image account already holds ------------------------------
# caddy is uid 100 but gid 101, so the requested gid cannot be honoured; the
# account is used as it is and the mismatch is reported rather than hidden.
CASE="RUN_AS_UID=100 (caddy)"
drive RUN_AS_UID=100
assert_eq "account" "caddy" "$OUT"
assert_eq "exit status" 0 "$RC"
assert_lacks "existing account untouched" "$CALLS" "adduser"
assert_has "mismatch reported" "$LOG" "primary group is 101 and not 100"
assert_has "account's own gid used" "$CALLS" "chown 100:101 /app"

# ----- 8. RUN_AS_UID=0 is a request for the old behaviour -------------------
CASE="RUN_AS_UID=0"
drive RUN_AS_UID=0
assert_eq "account" "root" "$OUT"
assert_eq "exit status" 0 "$RC"
assert_eq "nothing was chowned" "" "$CALLS"

# ----- 9. non-numeric input is rejected -------------------------------------
CASE="RUN_AS_UID=nobody"
drive RUN_AS_UID=nobody
assert_eq "exit status" 1 "$RC"
assert_eq "no account on stdout" "" "$OUT"
assert_has "reason given" "$LOG" "RUN_AS_UID must be a number"

CASE="RUN_AS_GID=wheel"
drive RUN_AS_UID=5000 RUN_AS_GID=wheel
assert_eq "exit status" 1 "$RC"
assert_eq "no account on stdout" "" "$OUT"
assert_has "reason given" "$LOG" "RUN_AS_GID must be a number"

# ----- 10. an explicit request that cannot be met must not become root ------
CASE="RUN_AS_UID=5000, adduser fails"
drive RUN_AS_UID=5000 STUB_ADDUSER_FAIL=1
assert_eq "exit status" 1 "$RC"
assert_eq "no account on stdout" "" "$OUT"
assert_has "refusal is explicit" "$LOG" "refusing to fall back to root"
assert_lacks "nothing handed over" "$CALLS" "chown"

CASE="RUN_AS_UID=5000, addgroup fails"
drive RUN_AS_UID=5000 STUB_ADDGROUP_FAIL=1
assert_eq "exit status" 1 "$RC"
assert_eq "no account on stdout" "" "$OUT"
assert_has "refusal is explicit" "$LOG" "refusing to fall back to root"

# ----- report ---------------------------------------------------------------
echo "select-service-user: $PASS assertions passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
