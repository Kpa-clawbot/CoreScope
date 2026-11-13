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

# Which user runs the two Go services, and what they are handed. supervisord
# itself stays root so that mosquitto and caddy can still bind their ports.
# Unprivileged operation is opt-in via RUN_AS_UID, taken from the container's
# environment before .env is read; the data directory is handed over, and .env
# is read last (with the services unprivileged, only if root owns it). The
# order matters and lives in one function: see docker/select-service-user.sh,
# and "Running the Go services unprivileged" in docs/deployment.md.
. /select-service-user.sh
prepare_service_user || exit 1

# One container, one environment, two database roles. On PostgreSQL the
# ingestor connects as the writer and the server as a read-only role (it
# refuses to start otherwise), so each gets its own DSN: supervisord hands
# DB_DSN_INGESTOR to the ingestor and DB_DSN_SERVER to the server, each as
# DB_DSN, and the server's command drops DB_DSN_INGESTOR from its environment.
# Both default to DB_DSN, so a deployment that sets only DB_DSN, or nothing
# (SQLite), runs as before. Set here, after .env has been read, because
# supervisord refuses a config naming an unset variable.
: "${DB_DSN_INGESTOR:=${DB_DSN:-}}"
: "${DB_DSN_SERVER:=${DB_DSN:-}}"
_nl='
'
for _dsn in "$DB_DSN_INGESTOR" "$DB_DSN_SERVER"; do
  case "$_dsn" in
    *'"'* | *\\* | *"$_nl"*)
      # Never print the DSN: it carries a password.
      echo "[entrypoint] DB_DSN / DB_DSN_INGESTOR / DB_DSN_SERVER must not contain a double quote, backslash or newline;" >&2
      echo "[entrypoint] use a postgres:// URL and percent-encode the password." >&2
      exit 1
      ;;
  esac
done
unset _dsn _nl
export DB_DSN_INGESTOR DB_DSN_SERVER

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

echo "[entrypoint] Go services run as $CORESCOPE_SERVICE_USER (uid=$(id -u "$CORESCOPE_SERVICE_USER") gid=$(id -g "$CORESCOPE_SERVICE_USER"))"

exec /usr/bin/supervisord -c "$SUPERVISORD_CONF"
