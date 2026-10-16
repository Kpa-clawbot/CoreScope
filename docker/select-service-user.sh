#!/bin/sh
# select-service-user.sh — decides which user the two Go services run as, and
# hands over the paths they have to write.
#
# Sourced by docker/entrypoint-go.sh, which calls prepare_service_user and
# nothing else from here: the order of the steps (resolve the account, hand
# over the data directory, read .env) is part of what keeps .env from choosing
# the account, so it lives in one function and the test drives that function.
# docker/test-select-service-user.sh replaces getent/adduser/addgroup/deluser/
# delgroup/id/chown/find/stat with stubs: keep every external command here
# stubbable, keep account resolution free of filesystem writes, and send
# human-readable output to stderr so stdout carries only the account name.
#
# Which user the services run as:
#
#   1. RUN_AS_UID / RUN_AS_GID, when set in the container's environment
#   2. root — what every earlier release did
#
# Both are read before /app/data/.env and never from it: the data directory is
# writable by the service account, and a setting there could be removed by the
# very process it restricts.
#
# There is deliberately no third rule inferring the uid from the mounted data
# directory. Such a rule switches user on upgrade for any deployment whose
# data directory is not root-owned — the ordinary `mkdir data` bind mount —
# while meshcore.db and its -wal/-shm, users.db and the backups inside it stay
# root-owned from the releases that ran as root, leaving the services unable to
# write them. Unprivileged operation is opt-in for that reason.

# The account baked into the image, at uid 1000.
SERVICE_ACCOUNT=corescope
SERVICE_APP_DIR=/app
SERVICE_DATA_DIR=/app/data
SERVICE_ENV_FILE=/app/data/.env
SERVICE_MOUNTINFO=/proc/self/mountinfo

# printf, not echo: dash's echo turns a \012 in a logged path into a newline.
svc_log() {
  printf '%s\n' "[entrypoint] $*" >&2
}

# prepare_service_user
#
# The start-up sequence:
#
#   1. resolve the account, from RUN_AS_UID/RUN_AS_GID in the container's
#      environment;
#   2. hand the data directory to the account, when it is not root;
#   3. read /app/data/.env (see load_env_file for when it is trusted), then
#      put RUN_AS_UID/RUN_AS_GID back to what the container had.
#
# Returns non-zero, with the reason on stderr, when RUN_AS_UID was set and no
# account could be provided. On success exports CORESCOPE_SERVICE_USER.
#
# .env comes last, and what runs after it reads only this function's
# positional parameters. A sourced file shares the shell's variables: every
# global, the locals of load_env_file, which sources it, and, by dynamic
# scoping, the locals of this function too, so `_account=root` or
# `SERVICE_DATA_DIR=/` in .env would reach any step that still read them. It
# also gets load_env_file's positional parameters, so `set -- root` there
# lands in that function's $1 and not in this one's. Hence the account, the
# container's RUN_AS_UID/RUN_AS_GID and the file's path are parked in $1..$6
# before .env is read, the hand-over, which reads the SERVICE_* paths, is done
# before it, and nothing else follows.
#
# A .env that passes the trust check is root's own code, and this does not
# try to defend against it: it can redefine these functions or set PATH. What
# it cannot do by assigning variables is change the account, where the
# hand-over goes, or RUN_AS_UID/RUN_AS_GID as the services see them.
prepare_service_user() {
  local _account
  _account=$(resolve_service_account) || return 1
  set -- "$_account" "${RUN_AS_UID+set}" "${RUN_AS_UID-}" \
         "${RUN_AS_GID+set}" "${RUN_AS_GID-}" "$SERVICE_ENV_FILE"
  unset _account

  if [ "$1" != root ]; then
    claim_service_paths "$1"
  fi

  load_env_file "$6" "$1"

  # The account was resolved before .env was read, so a RUN_AS_UID or
  # RUN_AS_GID in it must not reach supervisord and the services either: put
  # back what the container had, set or unset.
  if [ "${RUN_AS_UID+set}" != "$2" ] || [ "${RUN_AS_UID-}" != "$3" ] ||
     [ "${RUN_AS_GID+set}" != "$4" ] || [ "${RUN_AS_GID-}" != "$5" ]; then
    svc_log "ignoring RUN_AS_UID/RUN_AS_GID from $6: set them on the container"
    svc_log "(docker run -e, or environment: in compose), where the services cannot remove them."
    if [ -n "$2" ]; then RUN_AS_UID=$3; export RUN_AS_UID; else unset RUN_AS_UID; fi
    if [ -n "$4" ]; then RUN_AS_GID=$5; export RUN_AS_GID; else unset RUN_AS_GID; fi
  fi

  CORESCOPE_SERVICE_USER=$1
  export CORESCOPE_SERVICE_USER
}

# resolve_service_account
#
# Echoes the name of the account the Go services should run as: `root`, or an
# account with uid RUN_AS_UID, created when the image has none at that uid.
# Returns non-zero when RUN_AS_UID was set and no such account could be
# provided — a deployment that asked for unprivileged services must not
# quietly get root ones instead.
resolve_service_account() {
  local run_as_uid="${RUN_AS_UID:-}"
  local run_as_gid="${RUN_AS_GID:-}"
  local account group account_gid

  if [ -z "$run_as_uid" ]; then
    svc_log "RUN_AS_UID is unset — the Go services run as root, as in earlier releases."
    svc_log "To run them unprivileged, see \"Running the Go services unprivileged\" in docs/deployment.md."
    echo root
    return 0
  fi

  case "$run_as_uid" in
    *[!0-9]*)
      svc_log "RUN_AS_UID must be a number, got \"$run_as_uid\""
      return 1
      ;;
  esac
  : "${run_as_gid:=$run_as_uid}"
  case "$run_as_gid" in
    *[!0-9]*)
      svc_log "RUN_AS_GID must be a number, got \"$run_as_gid\""
      return 1
      ;;
  esac

  # Asking for uid 0 is asking for the old behaviour; say so rather than
  # treating root as an account to hand files over to.
  if [ "$run_as_uid" -eq 0 ]; then
    svc_log "RUN_AS_UID=0 — the Go services run as root, as in earlier releases."
    echo root
    return 0
  fi

  account=$(getent passwd "$run_as_uid" 2>/dev/null | cut -d: -f1)
  # The baked-in account in another group is rebuilt below rather than used
  # as it is, so RUN_AS_UID=1000 RUN_AS_GID=1001 means gid 1001. An account the
  # image needs for something else (caddy, mosquitto) keeps its own group.
  if [ "$account" = "$SERVICE_ACCOUNT" ] && [ "$(id -g "$account")" != "$run_as_gid" ]; then
    account=""
  fi
  if [ -z "$account" ]; then
    # Nothing usable holds that uid. The baked-in account is not what was
    # asked for: free its name, but only now that it is known to be in the
    # way.
    if getent passwd "$SERVICE_ACCOUNT" >/dev/null 2>&1; then
      deluser "$SERVICE_ACCOUNT" >/dev/null 2>&1 || true
    fi
    if getent group "$SERVICE_ACCOUNT" >/dev/null 2>&1; then
      delgroup "$SERVICE_ACCOUNT" >/dev/null 2>&1 || true
    fi
    group=$(getent group "$run_as_gid" 2>/dev/null | cut -d: -f1)
    if [ -z "$group" ] && addgroup -S -g "$run_as_gid" "$SERVICE_ACCOUNT" 2>/dev/null; then
      group="$SERVICE_ACCOUNT"
    fi
    # -H as in the Dockerfile: without it busybox adduser gives the existing
    # /app to the account, mode 2755.
    if [ -n "$group" ] && adduser -S -u "$run_as_uid" -G "$group" -H \
         -h "$SERVICE_APP_DIR" -s /sbin/nologin "$SERVICE_ACCOUNT" 2>/dev/null; then
      account="$SERVICE_ACCOUNT"
    fi
  fi

  if [ -z "$account" ]; then
    svc_log "no account available for uid=$run_as_uid gid=$run_as_gid"
    svc_log "RUN_AS_UID was set explicitly — refusing to fall back to root"
    return 1
  fi

  account_gid=$(id -g "$account")
  if [ "$account_gid" != "$run_as_gid" ]; then
    svc_log "note: uid $run_as_uid belongs to the existing account \"$account\"," \
            "whose primary group is $account_gid and not $run_as_gid"
  fi

  echo "$account"
}

# load_env_file <path> <account>
#
# Sources the data directory's .env with every variable exported, as the
# entrypoint always has. When the services run unprivileged that directory is
# theirs, so a .env they could have written or put there is not run as root: it
# is read only when it is a regular file, not a symlink, owned by root and
# writable by no one else. Running as root changes nothing — whoever could
# write the file could already run code as root.
#
# Nothing runs here after the file: it shares this function's variables and
# positional parameters, so anything read afterwards would be whatever it
# chose to leave. prepare_service_user does the rest from its own parameters.
load_env_file() {
  local _env_file="$1" _env_account="$2"

  [ -f "$_env_file" ] || return 0
  if [ "$_env_account" != root ] && ! env_file_trusted "$_env_file"; then
    svc_log "not reading $_env_file: the Go services run as $_env_account and can"
    svc_log "write $SERVICE_DATA_DIR, and this file is run as root. Make it a regular"
    svc_log "file owned by root and writable only by root to have it read."
    return 0
  fi

  set -a
  . "$_env_file"
  set +a
}

# env_file_trusted <path>
#
# True for a regular file, not a symlink, owned by root and writable by no one
# else. stat without -L describes a symlink itself, so that test is belt and
# braces.
env_file_trusted() {
  local _st
  [ -L "$1" ] && return 1
  _st=$(stat -c '%u %A' "$1" 2>/dev/null) || return 1
  case "$_st" in
    0\ -????w????|0\ -???????w?) return 1 ;;
    0\ -*) return 0 ;;
  esac
  return 1
}

# service_data_mounts
#
# Mount points inside the data directory, not counting the data directory
# itself, one per line, with the kernel's escapes undone (space, tab, backslash;
# \134 last, so that an escaped backslash followed by digits stays literal). A
# config.json bind-mounted on its own shows up here.
#
# Returns non-zero when a mount point contains a newline: it cannot be listed
# one per line, and the caller must then not hand anything over.
service_data_mounts() {
  local _paths _tab
  _tab=$(printf '\t')
  [ -r "$SERVICE_MOUNTINFO" ] || return 0
  _paths=$(awk -v d="$SERVICE_DATA_DIR/" 'index($5, d) == 1 { print $5 }' "$SERVICE_MOUNTINFO")
  case "$_paths" in
    *'\012'*) return 1 ;;
  esac
  printf '%s\n' "$_paths" | sed "s/\\\\040/ /g; s/\\\\011/$_tab/g; s/\\\\134/\\\\/g"
}

# glob_escape <path>
#
# find -path treats [ ] * ? and \ in its argument as pattern characters; a mount
# point named with one of them must match itself and nothing else.
glob_escape() {
  printf '%s\n' "$1" | sed 's/[][*?\\]/\\&/g'
}

# claim_service_paths <account>
#
# Gives the service account the data directory and what an earlier root-run
# container left in it. /app stays root's: nothing the services write lives
# there, and an operator may run its binaries as root with `docker exec`.
#
# Reached only when RUN_AS_UID named an unprivileged account, and that is what
# makes the hand-over consented to rather than a surprise. It runs on every
# start, so a file later created there as root, by `docker exec` say, is handed
# over at the next one.
#
# Left alone, with what is below them:
#   - anything mounted into the data directory on its own, such as a
#     config.json bind-mounted as a single file: that is the operator's file on
#     the host, and its owner is theirs to choose;
#   - .env, which the entrypoint reads as root and so must stay root's.
#
# chown -h re-owns a symlink itself rather than what it points at. A path the
# hand-over could not change — on NFS with root_squash, say — is logged: the
# services would otherwise fail to write it with nothing in the log saying why.
claim_service_paths() {
  local account="$1"
  local uid gid excluded p left count

  uid=$(id -u "$account")
  gid=$(id -g "$account")

  if ! excluded=$(service_data_mounts); then
    svc_log "WARNING: a mount point in $SERVICE_DATA_DIR has a newline in its name, so which"
    svc_log "files are the operator's cannot be told; not handing $SERVICE_DATA_DIR over."
    return 0
  fi
  if [ -n "$excluded" ]; then
    printf '%s\n' "$excluded" | while IFS= read -r p; do
      svc_log "left alone: $p is mounted on its own, so it keeps the owner it has on the host"
    done
  fi
  excluded="$SERVICE_ENV_FILE${excluded:+
$excluded}"

  # find's prune expression, built in the positional parameters so that
  # paths with spaces survive.
  set --
  set -f
  local IFS='
'
  for p in $excluded; do
    [ $# -eq 0 ] || set -- "$@" -o
    set -- "$@" -path "$(glob_escape "$p")"
  done
  unset IFS
  set +f

  find "$SERVICE_DATA_DIR" \( "$@" \) -prune -o ! -user "$uid" \
    -exec chown -h "$uid:$gid" {} + 2>/dev/null

  left=$(find "$SERVICE_DATA_DIR" \( "$@" \) -prune -o ! -user "$uid" -print 2>/dev/null)
  if [ -n "$left" ]; then
    count=$(printf '%s\n' "$left" | wc -l)
    svc_log "WARNING: $count path(s) in $SERVICE_DATA_DIR could not be given to uid $uid;"
    svc_log "the Go services will fail to write them. First ones:"
    printf '%s\n' "$left" | head -n 5 | while IFS= read -r p; do svc_log "  $p"; done
  fi
}
