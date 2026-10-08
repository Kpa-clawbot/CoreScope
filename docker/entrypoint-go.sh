#!/bin/sh

# Config lives in the data directory (bind-mounted from host)
# The Go server already searches /app/data/config.json via LoadConfig
# but the ingestor expects a direct path — symlink for compatibility
if [ -f /app/data/config.json ]; then
  ln -sf /app/data/config.json /app/config.json
elif [ ! -f /app/config.json ]; then
  echo "[entrypoint] No config.json found in /app/data/ — using built-in defaults"
fi

# theme.json: check data/ volume (admin-editable on host)
if [ -f /app/data/theme.json ]; then
  ln -sf /app/data/theme.json /app/theme.json
fi

# Source .env from data volume if present (works with any launch method)
if [ -f /app/data/.env ]; then
  set -a
  . /app/data/.env
  set +a
fi

SUPERVISORD_CONF="/etc/supervisor/conf.d/supervisord.conf"
if [ "${DISABLE_MOSQUITTO:-false}" = "true" ] && [ "${DISABLE_CADDY:-false}" = "true" ]; then
  echo "[config] internal MQTT broker disabled (DISABLE_MOSQUITTO=true)"
  echo "[config] Caddy reverse proxy disabled (DISABLE_CADDY=true)"
  SUPERVISORD_CONF="/etc/supervisor/conf.d/supervisord-no-mosquitto-no-caddy.conf"
elif [ "${DISABLE_MOSQUITTO:-false}" = "true" ]; then
  echo "[config] internal MQTT broker disabled (DISABLE_MOSQUITTO=true)"
  SUPERVISORD_CONF="/etc/supervisor/conf.d/supervisord-no-mosquitto.conf"
elif [ "${DISABLE_CADDY:-false}" = "true" ]; then
  echo "[config] Caddy reverse proxy disabled (DISABLE_CADDY=true)"
  SUPERVISORD_CONF="/etc/supervisor/conf.d/supervisord-no-caddy.conf"
fi

# Decide which user runs the two Go services. supervisord itself stays root so
# that mosquitto and caddy can still bind their ports. The uid is taken from,
# in order:
#
#   1. RUN_AS_UID / RUN_AS_GID, when set
#   2. the owner of the mounted /app/data, when that is not root
#   3. root — what every earlier release did
#
# Nothing on the host is given a new owner unless RUN_AS_UID was set
# explicitly: only that case takes over files an earlier root-run container
# left in the data directory. Otherwise we adapt to the data directory as we
# find it and say so in the log.
CORESCOPE_SERVICE_USER=root
RUN_AS_UID="${RUN_AS_UID:-}"
RUN_AS_GID="${RUN_AS_GID:-}"
UID_SOURCE=""

if [ -n "$RUN_AS_UID" ]; then
  UID_SOURCE=explicit
  RUN_AS_GID="${RUN_AS_GID:-$RUN_AS_UID}"
else
  DATA_UID=$(stat -c %u /app/data 2>/dev/null || echo 0)
  DATA_GID=$(stat -c %g /app/data 2>/dev/null || echo 0)
  if [ "$DATA_UID" != "0" ]; then
    UID_SOURCE=data-dir
    RUN_AS_UID="$DATA_UID"
    RUN_AS_GID="$DATA_GID"
  fi
fi

if [ -n "$UID_SOURCE" ]; then
  # Re-use whichever account already holds that uid; only create one if none
  # does, and never delete the baked-in account before that is known.
  ACCOUNT=$(getent passwd "$RUN_AS_UID" | cut -d: -f1)
  if [ -z "$ACCOUNT" ]; then
    getent passwd corescope >/dev/null 2>&1 && deluser corescope >/dev/null 2>&1
    getent group corescope >/dev/null 2>&1 && delgroup corescope >/dev/null 2>&1
    GRP=$(getent group "$RUN_AS_GID" | cut -d: -f1)
    if [ -z "$GRP" ] && addgroup -S -g "$RUN_AS_GID" corescope 2>/dev/null; then
      GRP=corescope
    fi
    if [ -n "$GRP" ] && adduser -S -u "$RUN_AS_UID" -G "$GRP" -h /app \
         -s /sbin/nologin corescope 2>/dev/null; then
      ACCOUNT=corescope
    fi
  fi

  if [ -z "$ACCOUNT" ]; then
    echo "[entrypoint] no account available for uid=$RUN_AS_UID gid=$RUN_AS_GID"
    if [ "$UID_SOURCE" = explicit ]; then
      echo "[entrypoint] RUN_AS_UID was set explicitly — not falling back to root"
      exit 1
    fi
    echo "[entrypoint] Go services stay root"
  else
    CORESCOPE_SERVICE_USER="$ACCOUNT"
    if [ "$(id -g "$ACCOUNT")" != "$RUN_AS_GID" ]; then
      echo "[entrypoint] note: uid $RUN_AS_UID belongs to the existing account" \
           "'$ACCOUNT', whose primary group is $(id -g "$ACCOUNT") and not $RUN_AS_GID"
    fi
    # /app belongs to the image, not to the mount: the server writes
    # config.json.tmp there when the geo filter is saved.
    chown "$RUN_AS_UID:$(id -g "$ACCOUNT")" /app
    if [ "$UID_SOURCE" = explicit ]; then
      # The operator asked for this uid, so hand over the files an earlier
      # root-run container left behind.
      find /app/data ! -user "$RUN_AS_UID" \
        -exec chown "$RUN_AS_UID:$RUN_AS_GID" {} + 2>/dev/null || true
    elif find /app/data ! -user "$RUN_AS_UID" 2>/dev/null | head -1 | grep -q .; then
      echo "[entrypoint] WARNING: /app/data holds files not owned by uid $RUN_AS_UID;"
      echo "[entrypoint]          CoreScope cannot write those. On the host, run:"
      echo "[entrypoint]            chown -R $RUN_AS_UID:$RUN_AS_GID <data dir>"
      echo "[entrypoint]          or start with RUN_AS_UID=$RUN_AS_UID to hand them over."
    fi
  fi
else
  echo "[entrypoint] /app/data is root-owned — Go services stay root, as before."
  echo "[entrypoint] To run them unprivileged, chown the data directory on the host,"
  echo "[entrypoint] or set RUN_AS_UID (see docs/deployment.md)."
fi

export CORESCOPE_SERVICE_USER
echo "[entrypoint] Go services run as $CORESCOPE_SERVICE_USER (uid=$(id -u "$CORESCOPE_SERVICE_USER") gid=$(id -g "$CORESCOPE_SERVICE_USER"))"

exec /usr/bin/supervisord -c "$SUPERVISORD_CONF"
