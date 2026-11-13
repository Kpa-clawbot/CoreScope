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
# Taken, like RUN_AS_UID, from the container's environment and not from .env;
# used on PostgreSQL only (see below).
_server_run_as_uid=${SERVER_RUN_AS_UID:-1001}
. /select-service-user.sh
prepare_service_user || exit 1

# ---------------------------------------------------------------------------
# Database credentials and, on PostgreSQL, a separate account for the server.
#
# Both Go services run in this container under one environment, but on
# PostgreSQL they use different database roles: the ingestor connects as the
# writer, the server as a read-only role (it refuses to start otherwise). Each
# gets its own DSN: supervisord hands DB_DSN_INGESTOR to the ingestor and
# DB_DSN_SERVER to the server, each as DB_DSN, and the server's command drops
# DB_DSN_INGESTOR from its environment. Both default to DB_DSN, so a
# deployment that sets only DB_DSN, or nothing (SQLite), runs as before.
#
# Keeping the writer DSN out of the server's environment is not enough on its
# own: a process can read /proc/<pid>/environ of any process with its uid, and
# root can read all of them, supervisord's (PID 1, which holds both DSNs)
# included. So on PostgreSQL the server runs as its own account,
# corescope-server, with a uid (SERVER_RUN_AS_UID, default 1001) that is
# neither 0 nor the ingestor's; the kernel then refuses it the environ of
# PID 1 and of the ingestor. The entrypoint refuses to start rather than run
# the server under the ingestor's uid or as root.
#
# What the server still writes is moved into one directory, /app/data/shared:
# users.db (with its -wal/-shm and the backups/ directory) and the two file
# queues it shares with the ingestor, prune-requests/ and mbcap-snapshot/. Both
# services are pointed at it (the server's -db and the ingestor's DB_PATH,
# which on PostgreSQL only anchor these paths). The directories are owned by
# root, group corescope-share (gid = the server's uid), mode 2770: the server
# account has that as its primary group, a non-root ingestor is added to it,
# and nobody else gets in. users.db is the server's, mode 0660, so whichever
# process creates its -shm gives the other read and write.
#
# config.json is the one file outside the shared directory the server may
# write: the geo-filter editor saves into it. Only the file is opened up —
# group corescope-share, mode 0660 — never /app/data, so the server cannot
# create, rename or remove anything there; in particular it cannot create
# .env, which this script sources as root. Its owner is kept (an operator's
# bind-mounted file stays theirs), except that one owned by the server is
# given to root so the server cannot change its mode. When the save cannot
# replace the file atomically it rewrites it in place, after a backup in the
# shared directory. Write access to config.json gives the server no way to
# the writer credential: the ingestor takes its DSN from DB_DSN_INGESTOR,
# which overrides anything in config.json; config.json may not carry a dsn
# at all (refused below); and the server's uid comes from SERVER_RUN_AS_UID
# in the container's environment, never from a file it can write. A
# config.json that resolves outside /app/data, or is not a regular file, is
# refused. The rest of /app/data stays as it was, read-only to the server.
# A users.db left in /app/data by an earlier SQLite run is moved over
# once; userManagement.dbPath in config.json, when set, must point into
# /app/data/shared too (a warning says so when it does not).
#
# Checked before supervisord starts, with the server's own credentials:
# config.json must be readable by it and contain no "dsn" (the server reads
# it), and /app/data/.env must NOT be readable by it.
#
# SQLite keeps today's arrangement: one account for both services, -db
# /app/data/meshcore.db for the server, and the ingestor's DB_PATH as the
# container had it.
# ---------------------------------------------------------------------------
: "${DB_DSN_INGESTOR:=${DB_DSN:-}}"
: "${DB_DSN_SERVER:=${DB_DSN:-}}"
_nl='
'
for _v in "$DB_DSN_INGESTOR" "$DB_DSN_SERVER" "${DB_PATH:-}"; do
  case "$_v" in
    *'"'* | *\\* | *"$_nl"*)
      # Never print the value: a DSN carries a password.
      echo "[entrypoint] DB_DSN / DB_DSN_INGESTOR / DB_DSN_SERVER / DB_PATH must not contain a double quote, backslash or newline;" >&2
      echo "[entrypoint] use a postgres:// URL and percent-encode the password." >&2
      exit 1
      ;;
  esac
done
unset _v _nl
export DB_DSN_INGESTOR DB_DSN_SERVER

SERVER_ACCOUNT=corescope-server
SHARE_GROUP=corescope-share
SHARED_DIR=/app/data/shared

pg_log() {
  printf '%s\n' "[entrypoint] postgres: $*" >&2
}

# is_postgres_mode: DB_DRIVER, or db.driver in config.json, names PostgreSQL.
is_postgres_mode() {
  case "$(printf '%s' "${DB_DRIVER:-}" | tr -d ' \t' | tr '[:upper:]' '[:lower:]')" in
    postgres | postgresql | pg) return 0 ;;
  esac
  [ -f /app/config.json ] &&
    grep -Eiq '"driver"[[:space:]]*:[[:space:]]*"[[:space:]]*(postgres|postgresql|pg)[[:space:]]*"' /app/config.json
}

as_server() {
  su -s /bin/sh "$SERVER_ACCOUNT" -c "$1"
}

# shared_dir <path>: create a directory of the shared tree, refusing a symlink
# (the services can write in its parent), owned root:corescope-share 2770.
shared_dir() {
  if [ -L "$1" ] || { [ -e "$1" ] && [ ! -d "$1" ]; }; then
    pg_log "$1 is not a directory; refusing to start"
    return 1
  fi
  mkdir -p "$1" && chown root:"$SHARE_GROUP" "$1" && chmod 2770 "$1"
}

# users_db_path_setting <file>: prints userManagement.dbPath from a JSON
# config, or nothing. A small scanner rather than a grep, so the top-level
# dbPath (the measurement database) is never mistaken for it.
users_db_path_setting() {
  awk '
    BEGIN { RS = "\001" }
    {
      n = length($0); depth = 0; top = ""; key = ""; want = 0
      for (i = 1; i <= n; i++) {
        c = substr($0, i, 1)
        if (c == "\"") {
          s = ""
          for (i++; i <= n; i++) {
            c = substr($0, i, 1)
            if (c == "\\") { s = s c substr($0, i + 1, 1); i++; continue }
            if (c == "\"") break
            s = s c
          }
          if (want) { print s; exit }
          key = s
          continue
        }
        if (c == ":") {
          if (depth == 1) top = key
          else if (depth == 2 && top == "userManagement" && key == "dbPath") want = 1
          continue
        }
        if (c == "{" || c == "[") depth++
        else if (c == "}" || c == "]") depth--
        if (c != " " && c != "\t" && c != "\n" && c != "\r") { key = ""; want = 0 }
      }
    }' "$1"
}

# setup_server_account <uid>: everything described above. Returns non-zero,
# with the reason on stderr, when the separation cannot be arranged.
setup_server_account() {
  local uid="$1" ingest_uid name f

  case "$uid" in
    '' | *[!0-9]*) pg_log "SERVER_RUN_AS_UID must be a number, got \"$uid\""; return 1 ;;
  esac
  ingest_uid=$(id -u "$CORESCOPE_SERVICE_USER") || return 1
  if [ "$uid" -eq 0 ] || [ "$uid" = "$ingest_uid" ]; then
    pg_log "the server must run as a uid other than 0 and the ingestor's ($ingest_uid); SERVER_RUN_AS_UID=$uid"
    return 1
  fi

  name=$(getent group "$uid" 2>/dev/null | cut -d: -f1)
  if [ -z "$name" ]; then
    addgroup -S -g "$uid" "$SHARE_GROUP" || { pg_log "cannot create group $SHARE_GROUP (gid $uid)"; return 1; }
  elif [ "$name" != "$SHARE_GROUP" ]; then
    pg_log "gid $uid already belongs to group \"$name\"; choose another SERVER_RUN_AS_UID"
    return 1
  fi
  name=$(getent passwd "$uid" 2>/dev/null | cut -d: -f1)
  if [ -z "$name" ]; then
    adduser -S -u "$uid" -G "$SHARE_GROUP" -H -h /nonexistent -s /sbin/nologin "$SERVER_ACCOUNT" ||
      { pg_log "cannot create account $SERVER_ACCOUNT (uid $uid)"; return 1; }
  elif [ "$name" != "$SERVER_ACCOUNT" ]; then
    pg_log "uid $uid already belongs to account \"$name\"; choose another SERVER_RUN_AS_UID"
    return 1
  fi
  if [ "$(id -g "$SERVER_ACCOUNT")" != "$uid" ] || [ "$(id -G "$SERVER_ACCOUNT")" != "$uid" ]; then
    pg_log "$SERVER_ACCOUNT must have group $SHARE_GROUP ($uid) and no other"
    return 1
  fi
  if [ "$ingest_uid" -ne 0 ] && ! id -nG "$CORESCOPE_SERVICE_USER" | tr ' ' '\n' | grep -qx "$SHARE_GROUP"; then
    addgroup "$CORESCOPE_SERVICE_USER" "$SHARE_GROUP" ||
      { pg_log "cannot add $CORESCOPE_SERVICE_USER to $SHARE_GROUP"; return 1; }
  fi

  # A users.db from an earlier SQLite run moves over once, with its backups.
  if [ ! -e "$SHARED_DIR/users.db" ] && [ -f /app/data/users.db ] && [ ! -L /app/data/users.db ]; then
    shared_dir "$SHARED_DIR" || return 1
    for f in users.db users.db-wal users.db-shm; do
      if [ -f "/app/data/$f" ] && [ ! -L "/app/data/$f" ]; then mv "/app/data/$f" "$SHARED_DIR/$f" || return 1; fi
    done
    if [ -d /app/data/backups ] && [ ! -L /app/data/backups ]; then
      shared_dir "$SHARED_DIR/backups" || return 1
      find /app/data/backups -maxdepth 1 -type f -name 'users-*.db' -exec mv {} "$SHARED_DIR/backups/" \;
    fi
    pg_log "moved users.db (and its backups) from /app/data to $SHARED_DIR"
  fi

  for f in "$SHARED_DIR" "$SHARED_DIR/prune-requests" "$SHARED_DIR/mbcap-snapshot" "$SHARED_DIR/backups"; do
    shared_dir "$f" || return 1
  done
  # backups/ is the server's alone, like the 0700 directory it would create.
  chown "$SERVER_ACCOUNT:$SHARE_GROUP" "$SHARED_DIR/backups" && chmod 0700 "$SHARED_DIR/backups" || return 1
  find "$SHARED_DIR/backups" -mindepth 1 ! -type l -exec chown "$SERVER_ACCOUNT:$SHARE_GROUP" {} + || return 1
  # An empty file is an empty SQLite database; creating it here gives it the
  # 0660 mode its -wal/-shm copy, whoever opens it first.
  if [ -L "$SHARED_DIR/users.db" ]; then
    pg_log "$SHARED_DIR/users.db is a symlink; refusing to start"
    return 1
  fi
  [ -e "$SHARED_DIR/users.db" ] || : > "$SHARED_DIR/users.db" || return 1
  for f in users.db users.db-wal users.db-shm; do
    if [ -f "$SHARED_DIR/$f" ] && [ ! -L "$SHARED_DIR/$f" ]; then
      chown "$SERVER_ACCOUNT:$SHARE_GROUP" "$SHARED_DIR/$f" && chmod 0660 "$SHARED_DIR/$f" || return 1
    fi
  done

  # config.json (through the /app/config.json link) becomes writable by the
  # share group; see the comment at the top.
  if [ -e /app/config.json ] || [ -L /app/config.json ]; then
    f=$(readlink -f /app/config.json) || f=
    case "$f" in
      /app/data/*/../* | /app/data/../*) f= ;;
      /app/data/* | /app/config.json) ;;
      *) f= ;;
    esac
    if [ -z "$f" ] || [ -L "$f" ] || [ ! -f "$f" ]; then
      pg_log "/app/config.json must be a regular file in /app/data (or /app/config.json itself); refusing to start"
      return 1
    fi
    if [ "$(stat -c %u "$f")" = "$uid" ]; then
      chown root "$f" || return 1
    fi
    chgrp "$SHARE_GROUP" "$f" && chmod 0660 "$f" || return 1
  fi

  # What the server can and cannot reach, checked as the server.
  if ! as_server "test -x /app/data && test -w $SHARED_DIR/prune-requests && test -w $SHARED_DIR/users.db"; then
    pg_log "$SERVER_ACCOUNT cannot reach $SHARED_DIR; /app/data must be searchable (o+x) by it"
    return 1
  fi
  if [ -e /app/config.json ]; then
    if ! as_server 'test -r /app/config.json'; then
      pg_log "config.json is not readable by $SERVER_ACCOUNT (uid $uid); make it readable by group $SHARE_GROUP (gid $uid) or by others"
      return 1
    fi
    if grep -q '"dsn"' /app/config.json; then
      pg_log "config.json sets db.dsn, which the server can read; pass DB_DSN_INGESTOR and DB_DSN_SERVER in the environment instead"
      return 1
    fi
  fi
  # Accepted but flagged: a users.db outside the shared directory is one the
  # server (and a non-root ingestor) may not be able to write or read.
  f=$( [ -f /app/config.json ] && users_db_path_setting /app/config.json )
  if [ -n "$f" ]; then
    case "$f" in /*) ;; *) f=/app/$f ;; esac
    case "$f" in
      "$SHARED_DIR"/*/../* | "$SHARED_DIR"/../* | *'/../'*) f_ok=false ;;
      "$SHARED_DIR"/*) f_ok=true ;;
      *) f_ok=false ;;
    esac
    if [ "$f_ok" = false ]; then
      pg_log "WARNING: userManagement.dbPath in config.json is $f, outside $SHARED_DIR;"
      pg_log "WARNING: $SERVER_ACCOUNT can write only there. Move users.db into $SHARED_DIR and point dbPath at it."
    fi
    unset f_ok
  fi
  if [ -e /app/data/.env ] && as_server 'test -r /app/data/.env'; then
    pg_log "/app/data/.env is readable by $SERVER_ACCOUNT; chmod 600 it (it may hold the writer DSN)"
    return 1
  fi

  CORESCOPE_SERVER_USER=$SERVER_ACCOUNT
  CORESCOPE_SERVER_DB_PATH=$SHARED_DIR/meshcore.db
  CORESCOPE_INGESTOR_DB_PATH=$SHARED_DIR/meshcore.db
  pg_log "server runs as $SERVER_ACCOUNT (uid $uid), ingestor as $CORESCOPE_SERVICE_USER (uid $ingest_uid); shared files in $SHARED_DIR"
}

if is_postgres_mode; then
  setup_server_account "$_server_run_as_uid" || exit 1
else
  CORESCOPE_SERVER_USER=$CORESCOPE_SERVICE_USER
  CORESCOPE_SERVER_DB_PATH=/app/data/meshcore.db
  CORESCOPE_INGESTOR_DB_PATH=${DB_PATH:-}
fi
unset _server_run_as_uid
export CORESCOPE_SERVER_USER CORESCOPE_SERVER_DB_PATH CORESCOPE_INGESTOR_DB_PATH

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
