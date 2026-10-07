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

# Run the Go services as the unprivileged "corescope" user (supervisord
# itself stays root so it can start mosquitto and caddy). Match the user's
# uid/gid to the owner of the mounted data directory so the services can
# write there and files they create carry the host owner's ids. Files that a
# previous root-run container left behind are handed to that owner too.
DATA_UID=$(stat -c %u /app/data 2>/dev/null || echo 0)
DATA_GID=$(stat -c %g /app/data 2>/dev/null || echo 0)
if [ "$DATA_UID" != "0" ]; then
  if [ "$(id -u corescope)" != "$DATA_UID" ] || [ "$(id -g corescope)" != "$DATA_GID" ]; then
    deluser corescope 2>/dev/null || true
    delgroup corescope 2>/dev/null || true
    GRP=$(getent group "$DATA_GID" | cut -d: -f1)
    if [ -z "$GRP" ]; then
      addgroup -S -g "$DATA_GID" corescope
      GRP=corescope
    fi
    adduser -S -u "$DATA_UID" -G "$GRP" -h /app -s /sbin/nologin corescope
  fi
  find /app/data -not -user "$DATA_UID" -exec chown "$DATA_UID:$DATA_GID" {} + 2>/dev/null || true
else
  chown -R corescope:corescope /app/data
fi
# The geo-filter save writes config.json.tmp next to /app/config.json.
chown corescope /app
chown -h corescope /app/config.json /app/theme.json 2>/dev/null || true
echo "[entrypoint] Go services run as uid=$(id -u corescope) gid=$(id -g corescope)"

exec /usr/bin/supervisord -c "$SUPERVISORD_CONF"
