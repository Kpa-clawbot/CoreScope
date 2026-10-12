#!/bin/sh
# select-service-user.sh — decides which user the two Go services run as, and
# hands over the paths they have to write.
#
# Sourced by docker/entrypoint-go.sh. The same functions are sourced by
# docker/test-select-service-user.sh, which replaces getent/adduser/addgroup/
# deluser/delgroup/id/chown/find with stubs: keep every external command here
# stubbable, keep account resolution free of filesystem writes, and send
# human-readable output to stderr so stdout carries only the account name.
#
# Which user the services run as:
#
#   1. RUN_AS_UID / RUN_AS_GID, when set
#   2. root — what every earlier release did
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

svc_log() {
  echo "[entrypoint] $*" >&2
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
  if [ -z "$account" ]; then
    # Nothing holds that uid. The baked-in account sits at 1000 and so is not
    # what was asked for: free its name, but only now that it is known to be
    # in the way.
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
    if [ -n "$group" ] && adduser -S -u "$run_as_uid" -G "$group" \
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

# claim_service_paths <account>
#
# Gives the service account the paths it has to be able to write. Reached only
# when RUN_AS_UID named an unprivileged account, and that is what makes the
# hand-over of the data directory consented to rather than a surprise.
claim_service_paths() {
  local account="$1"
  local uid gid

  uid=$(id -u "$account")
  gid=$(id -g "$account")

  # /app belongs to the image rather than to a mount: the server writes
  # config.json.tmp next to config.json when the geo filter is saved. Only the
  # directory changes hands — the files in it, a bind-mounted config.json
  # among them, keep the owner they have on the host.
  chown "$uid:$gid" "$SERVICE_APP_DIR"

  # Setting RUN_AS_UID is taken as permission to hand over what an earlier
  # root-run container left in the data directory; without this the services
  # could not write an existing meshcore.db.
  find "$SERVICE_DATA_DIR" ! -user "$uid" -exec chown "$uid:$gid" {} + 2>/dev/null || true
}
