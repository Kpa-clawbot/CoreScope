#!/usr/bin/env bash
# test-select-service-user.sh — unit tests for docker/select-service-user.sh,
# the uid selection the container entrypoint performs at start.
#
# Every branch is exercised without docker and without root: getent, adduser,
# addgroup, deluser, delgroup, id, chown, find and stat are replaced by stubs
# on PATH, and the mount table is a fixture file. The account stubs read and
# write a passwd/group pair of files seeded with the accounts the real image
# has (verified against alpine:3.24 with mosquitto, supervisor and caddy
# installed, plus the Dockerfile's corescope), so an account created mid-run is
# visible to the lookups that follow it. chown and find record their arguments
# rather than touching the filesystem, which is what lets the ownership
# hand-over be asserted exactly.
#
# The driver calls prepare_service_user, the one function the entrypoint
# (docker/entrypoint-go.sh) calls: resolve the account, hand over the data
# directory, read .env. A static check at the end pins that the entrypoint
# calls it and nothing else from the script, so what gets tested is the
# sequence the container runs: a non-zero return must never be read as root,
# and .env cannot choose the account.
#
# Run: bash docker/test-select-service-user.sh
# TEST_SHELL picks the shell the sequence runs in, default sh; the image runs
# busybox ash, so TEST_SHELL=/path/to/busybox-sh is worth a run. A busybox
# built to prefer its own applets over PATH (Ubuntu's package) bypasses the
# stubs and fails most cases; Debian's and Alpine's look up PATH first.
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
    printf '%s' "${FIX_MOUNTINFO-}" > "$FIX/mountinfo"
    if [ -n "${FIX_ENV-}" ]; then
        printf '%s\n' "$FIX_ENV" > "$FIX/env.real"
        if [ "${FIX_ENV_SYMLINK:-0}" = 1 ]; then
            ln -s "$FIX/env.real" "$FIX/env"
            [ -L "$FIX/env" ] || { echo "FAIL: fixture could not create the .env symlink" >&2; exit 1; }
        else
            mv "$FIX/env.real" "$FIX/env"
        fi
    fi
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
uid=""; grp=""; name=""; flags=""
while [ $# -gt 0 ]; do
  case "$1" in
    -u) uid="$2"; shift 2 ;;
    -G) grp="$2"; shift 2 ;;
    -h|-s) shift 2 ;;
    -S|-D|-H) flags="$flags $1"; shift ;;
    *) name="$1"; shift ;;
  esac
done
[ -n "$uid" ] && [ -n "$name" ] && [ -n "$grp" ] || exit 1
# busybox refuses a uid that is already taken
awk -F: -v u="$uid" '$2==u{found=1} END{exit !found}' "$FIX/passwd" && exit 1
gid=$(awk -F: -v g="$grp" '$1==g{print $2}' "$FIX/group")
[ -n "$gid" ] || exit 1
echo "$name:$uid:$gid" >> "$FIX/passwd"
echo "adduser $name uid=$uid gid=$gid flags=$flags" >> "$FIX/calls"
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

    # The hand-over walk is recorded; the verification walk (-print) answers
    # with STUB_LEFTOVER, the paths a failed chown would have left behind.
    cat > "$FIX/bin/find" <<'STUB'
#!/bin/sh
echo "find $*" >> "$FIX/calls"
case " $* " in
  *" -print "*) [ -n "${STUB_LEFTOVER-}" ] && printf '%s\n' "$STUB_LEFTOVER" ;;
esac
exit 0
STUB

    # Answers STUB_STAT ("uid mode"), by default a uid 1000 file: the uid
    # must never be inferred from that, and the .env check must refuse it.
    # Any call is recorded so the test can prove when none happens.
    cat > "$FIX/bin/stat" <<'STUB'
#!/bin/sh
echo "stat $*" >> "$FIX/calls"
echo "${STUB_STAT:-1000 -rw-r--r--}"
STUB

    chmod +x "$FIX"/bin/*
}

# drive [VAR=VAL ...] — run prepare_service_user as docker/entrypoint-go.sh
# does and exit non-zero if it fails. env -i keeps the host environment (a
# RUN_AS_UID of its own, a real PATH) out of the case. FIX_ENV (the .env
# contents), FIX_ENV_SYMLINK and FIX_MOUNTINFO shape the fixture; a .env that
# sets CS_ENV_READ shows up as "env-read" in CALLS. OUT is the account the
# services will run as (CORESCOPE_SERVICE_USER); FINAL_ENV is RUN_AS_UID/GID as
# the services would see them.
drive() {
    make_fixture
    OUT=$(env -i PATH="$FIX/bin:/usr/bin:/bin" FIX="$FIX" SCRIPT="$SCRIPT" "$@" "${TEST_SHELL:-sh}" -c '
        . "$SCRIPT"
        SERVICE_ENV_FILE="$FIX/env"
        SERVICE_MOUNTINFO="$FIX/mountinfo"
        prepare_service_user || exit 1
        [ -z "${CS_ENV_READ-}" ] || echo "env-read" >> "$FIX/calls"
        echo "RUN_AS_UID=${RUN_AS_UID-<unset>} RUN_AS_GID=${RUN_AS_GID-<unset>}" > "$FIX/final-env"
        echo "$CORESCOPE_SERVICE_USER"
    ' 2>"$FIX/log")
    RC=$?
    LOG=$(cat "$FIX/log")
    CALLS=$(cat "$FIX/calls")
    FINAL_ENV=$(cat "$FIX/final-env" 2>/dev/null)
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
assert_lacks "/app stays root's" "$CALLS" "chown 1000:1000 /app"
assert_has "data dir handed over, .env left out, symlinks not followed" "$CALLS" \
    "find /app/data ( -path $FIX/env ) -prune -o ! -user 1000 -exec chown -h 1000:1000 {} +"
assert_has "hand-over checked afterwards" "$CALLS" \
    "find /app/data ( -path $FIX/env ) -prune -o ! -user 1000 -print"
assert_lacks "nothing reported left over" "$LOG" "WARNING"
assert_lacks "no account created" "$CALLS" "adduser"
assert_lacks "baked-in account kept" "$CALLS" "deluser"

# ----- 4. RUN_AS_GID defaults to RUN_AS_UID ---------------------------------
CASE="RUN_AS_UID=1000, RUN_AS_GID unset"
drive RUN_AS_UID=1000
assert_eq "account" "corescope" "$OUT"
assert_has "gid defaulted to the uid" "$CALLS" "chown -h 1000:1000"
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
assert_has "home directory not created or re-owned" "$CALLS" "flags= -S -H"
assert_has "data dir handed over" "$CALLS" "chown -h 5000:5000"

# ----- 6. a separate RUN_AS_GID ---------------------------------------------
CASE="RUN_AS_UID=5000, RUN_AS_GID=6000"
drive RUN_AS_UID=5000 RUN_AS_GID=6000
assert_eq "account" "corescope" "$OUT"
assert_has "group created at the gid" "$CALLS" "addgroup corescope gid=6000"
assert_has "account created with that group" "$CALLS" "adduser corescope uid=5000 gid=6000"
assert_has "data dir handed over" "$CALLS" "chown -h 5000:6000"

# ----- 7. a uid an image account already holds ------------------------------
# caddy is uid 100 but gid 101, so the requested gid cannot be honoured; the
# account is used as it is and the mismatch is reported rather than hidden.
CASE="RUN_AS_UID=100 (caddy)"
drive RUN_AS_UID=100
assert_eq "account" "caddy" "$OUT"
assert_eq "exit status" 0 "$RC"
assert_lacks "existing account untouched" "$CALLS" "adduser"
assert_has "mismatch reported" "$LOG" "primary group is 101 and not 100"
assert_has "account's own gid used" "$CALLS" "chown -h 100:101"

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

# ----- 11. RUN_AS_GID is honoured for the baked-in account -----------------
# uid 1000 is the image's own corescope account, gid 1000. Asked for gid 1001,
# it is rebuilt in that group rather than used with the wrong one.
CASE="RUN_AS_UID=1000, RUN_AS_GID=1001"
drive RUN_AS_UID=1000 RUN_AS_GID=1001
assert_eq "account" "corescope" "$OUT"
assert_has "baked-in account rebuilt" "$CALLS" "deluser corescope"
assert_has "group created at the gid" "$CALLS" "addgroup corescope gid=1001"
assert_has "account recreated in it" "$CALLS" "adduser corescope uid=1000 gid=1001"
assert_has "data dir handed to that gid" "$CALLS" "chown -h 1000:1001"
assert_lacks "no group mismatch left to report" "$LOG" "note:"

# ----- 12. .env as root: read as before, whoever owns it -------------------
CASE=".env, RUN_AS_UID unset"
FIX_ENV='CS_ENV_READ=1' drive
assert_eq "account" "root" "$OUT"
assert_has ".env read" "$CALLS" "env-read"
assert_lacks "owner not checked as root" "$CALLS" "stat"

# ----- 13. .env, unprivileged: only a root-owned, root-only-writable file ----
CASE=".env root-owned 0644, RUN_AS_UID=1000"
FIX_ENV='CS_ENV_READ=1' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_has ".env read" "$CALLS" "env-read"

CASE=".env root-owned 0600, RUN_AS_UID=1000"
FIX_ENV='CS_ENV_READ=1' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-------'
assert_has ".env read" "$CALLS" "env-read"

CASE=".env owned by the service uid, RUN_AS_UID=1000"
FIX_ENV='CS_ENV_READ=1' drive RUN_AS_UID=1000 STUB_STAT='1000 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_lacks ".env not read" "$CALLS" "env-read"
assert_has "refusal explained" "$LOG" "not reading $FIX/env"

CASE=".env root-owned but group-writable, RUN_AS_UID=1000"
FIX_ENV='CS_ENV_READ=1' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-rw-r--'
assert_lacks ".env not read" "$CALLS" "env-read"

CASE=".env root-owned but world-writable, RUN_AS_UID=1000"
FIX_ENV='CS_ENV_READ=1' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-r--rw-'
assert_lacks ".env not read" "$CALLS" "env-read"

CASE=".env a symlink, RUN_AS_UID=1000"
FIX_ENV='CS_ENV_READ=1' FIX_ENV_SYMLINK=1 drive RUN_AS_UID=1000 STUB_STAT='0 -rw-r--r--'
assert_lacks ".env not read" "$CALLS" "env-read"
assert_lacks "refused before its owner is asked" "$CALLS" "stat"

# ----- 14. .env cannot choose the account -----------------------------------
# A service that can delete .env could otherwise drop its own RUN_AS_UID, and
# one that can write it could set RUN_AS_UID= and be root after a restart.
CASE="RUN_AS_UID in .env, unset on the container"
FIX_ENV='RUN_AS_UID=1000' drive
assert_eq "account" "root" "$OUT"
assert_eq "nothing was chowned" "" "$CALLS"
assert_has "ignored, and said so" "$LOG" "ignoring RUN_AS_UID/RUN_AS_GID"
assert_eq "and not left exported to the services" "RUN_AS_UID=<unset> RUN_AS_GID=<unset>" "$FINAL_ENV"

CASE="RUN_AS_UID= in a trusted .env, 1000 on the container"
FIX_ENV='RUN_AS_UID=' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_has "ignored, and said so" "$LOG" "ignoring RUN_AS_UID/RUN_AS_GID"
assert_eq "the container's value is put back" "RUN_AS_UID=1000 RUN_AS_GID=<unset>" "$FINAL_ENV"

CASE="RUN_AS_UID=0 and RUN_AS_GID in a trusted .env, 1000 on the container"
FIX_ENV='RUN_AS_UID=0
RUN_AS_GID=0' drive RUN_AS_UID=1000 RUN_AS_GID=1000 STUB_STAT='0 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_eq "both put back" "RUN_AS_UID=1000 RUN_AS_GID=1000" "$FINAL_ENV"

# The account is resolved before .env is read. If it were read first, a
# RUN_AS_UID there would make the resolver create an account at that uid.
CASE="order: .env read after the account is resolved"
FIX_ENV='RUN_AS_UID=5000' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_lacks "no account was created at the uid from .env" "$CALLS" "adduser"
assert_lacks "none was freed for it either" "$CALLS" "deluser"
assert_has "the data dir went to the container's uid" "$CALLS" "chown -h 1000:1000"

# A trusted .env that assigns the names the entrypoint and these functions use
# for the account must not change who the services run as, or who is handed
# the data directory.
CASE=".env assigns the account's variable names"
FIX_ENV='account=root
service_account=root
_account=root
CORESCOPE_SERVICE_USER=root' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_has "the data dir was still handed over to it" "$CALLS" "chown -h 1000:1000"

# The file is sourced inside load_env_file, which has positional parameters of
# its own, so a `set --` in it cannot reach the account prepare_service_user
# keeps in $1. Sourcing it in prepare_service_user instead would make this fail.
CASE=".env runs set -- root"
FIX_ENV='set -- root' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_has "the data dir was still handed over to it" "$CALLS" "chown -h 1000:1000"

# A sourced file shares every variable of the shell, the locals of the
# functions on the call stack included. The container's RUN_AS_UID/RUN_AS_GID
# are therefore put back from positional parameters, not from variables a line
# in .env could pre-empt. The uid and gid differ so that a mixed-up parameter
# shows.
CASE=".env assigns the names the restore could have used"
FIX_ENV='_uid_before=0
_uid_was=set
_gid_before=0
_gid_was=set
_env_file=/nowhere
_env_account=root
SERVICE_ENV_FILE=/nowhere
RUN_AS_UID=0
RUN_AS_GID=0' drive RUN_AS_UID=1000 RUN_AS_GID=1001 STUB_STAT='0 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_eq "both put back" "RUN_AS_UID=1000 RUN_AS_GID=1001" "$FINAL_ENV"
assert_has "ignored, and the log names the real file" "$LOG" "ignoring RUN_AS_UID/RUN_AS_GID from $FIX/env"

# Each of the two is compared on its own: a .env that changes only the gid is
# noticed too.
CASE="RUN_AS_GID alone in a trusted .env, 1000:1000 on the container"
FIX_ENV='RUN_AS_GID=0' drive RUN_AS_UID=1000 RUN_AS_GID=1000 STUB_STAT='0 -rw-r--r--'
assert_eq "gid put back" "RUN_AS_UID=1000 RUN_AS_GID=1000" "$FINAL_ENV"
assert_has "ignored, and said so" "$LOG" "ignoring RUN_AS_UID/RUN_AS_GID"

# The hand-over reads the SERVICE_* paths, so it happens before .env is read:
# a .env that points them elsewhere must find the data directory already dealt
# with, and .env itself already left out of it.
CASE=".env points the SERVICE_* paths elsewhere"
FIX_ENV='SERVICE_DATA_DIR=/
SERVICE_ENV_FILE=/nowhere
SERVICE_MOUNTINFO=/dev/null' drive RUN_AS_UID=1000 STUB_STAT='0 -rw-r--r--'
assert_eq "account" "corescope" "$OUT"
assert_has "the data dir was handed over, .env left out" "$CALLS" \
    "find /app/data ( -path $FIX/env ) -prune -o ! -user 1000 -exec chown -h 1000:1000 {} +"
assert_lacks "nothing else was walked" "$CALLS" "find / ("
assert_lacks "and the real .env was the one excluded" "$CALLS" "/nowhere"

# ----- 15. a file mounted into the data directory keeps its host owner -----
# The documented single-file config mount, plus one with a space in its path,
# as the kernel escapes it. The data directory's own mount is not excluded.
CASE="single-file mounts in the data directory"
FIX_MOUNTINFO='600 500 0:50 /srv/data /app/data rw,relatime - ext4 /dev/sda1 rw
601 600 0:50 /srv/config.json /app/data/config.json ro,relatime - ext4 /dev/sda1 rw
602 600 0:50 /srv/my\040keys /app/data/my\040keys rw,relatime - ext4 /dev/sda1 rw
603 500 0:51 / /data/caddy rw,relatime - ext4 /dev/sda1 rw
' drive RUN_AS_UID=1000
assert_has "mounts pruned along with .env" "$CALLS" \
    "find /app/data ( -path $FIX/env -o -path /app/data/config.json -o -path /app/data/my keys ) -prune -o ! -user 1000 -exec chown -h 1000:1000 {} +"
assert_has "config.json reported as left alone" "$LOG" "left alone: /app/data/config.json"
assert_lacks "data dir itself still handed over" "$CALLS" "-path /app/data )"
assert_lacks "other mounts ignored" "$CALLS" "/data/caddy"

# Names the kernel escapes (tab) or that find would read as patterns ([ * ?)
# must exclude exactly that path.
CASE="mount points with a tab and glob characters"
FIX_MOUNTINFO='601 600 0:50 /srv/a /app/data/tab\011name rw,relatime - ext4 /dev/sda1 rw
602 600 0:50 /srv/b /app/data/b[1]*? rw,relatime - ext4 /dev/sda1 rw
' drive RUN_AS_UID=1000
TAB=$(printf '\t')
assert_has "tab decoded" "$CALLS" "-path /app/data/tab${TAB}name"
assert_has "glob characters escaped" "$CALLS" '-path /app/data/b\[1\]\*\?'

# A newline in a mount point cannot be listed one per line. Failing closed
# means nothing is handed over, not that the file is handed over by mistake.
CASE="mount point with a newline"
FIX_MOUNTINFO='601 600 0:50 /srv/c /app/data/x\012y rw,relatime - ext4 /dev/sda1 rw
' drive RUN_AS_UID=1000
assert_eq "account" "corescope" "$OUT"
assert_eq "startup continues" 0 "$RC"
assert_lacks "nothing handed over" "$CALLS" "chown"
assert_has "and said why" "$LOG" "has a newline in its name"

# ----- 16. a hand-over that could not complete is logged -------------------
CASE="chown refused (root_squash NFS)"
drive RUN_AS_UID=1000 STUB_LEFTOVER='/app/data
/app/data/meshcore.db'
assert_eq "account" "corescope" "$OUT"
assert_eq "startup continues" 0 "$RC"
assert_has "warning names the count" "$LOG" "WARNING: 2 path(s) in /app/data could not be given to uid 1000"
assert_has "and the paths" "$LOG" "  /app/data/meshcore.db"

# A logged path is printed as it is: dash's echo would turn the \012 into a
# newline and cut the path in two.
CASE="chown refused on a path with a backslash escape"
drive RUN_AS_UID=1000 STUB_LEFTOVER='/app/data/x\012y'
assert_has "path logged intact" "$LOG" '  /app/data/x\012y'

# ----- 17. the entrypoint runs the sequence through prepare_service_user ----
# The driver above tests the function; this pins that the entrypoint uses it
# and does not rebuild the sequence from the steps, in an order of its own.
CASE="entrypoint-go.sh"
ENTRYPOINT="$(dirname "$SCRIPT")/entrypoint-go.sh"
EP=$(cat "$ENTRYPOINT")
assert_has "calls prepare_service_user" "$EP" "prepare_service_user || exit 1"
assert_lacks "does not resolve the account itself" "$EP" "resolve_service_account"
assert_lacks "does not read .env itself" "$EP" "load_env_file"
assert_lacks "does not hand over paths itself" "$EP" "claim_service_paths"
assert_lacks "does not set the account itself" "$EP" "CORESCOPE_SERVICE_USER="

# ----- report ---------------------------------------------------------------
echo "select-service-user: $PASS assertions passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
