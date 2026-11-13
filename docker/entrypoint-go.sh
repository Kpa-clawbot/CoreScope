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
_share_gid=${CORESCOPE_SHARE_GID:-61001}
. /select-service-user.sh
prepare_service_user || exit 1

# ---------------------------------------------------------------------------
# Database mode, credentials and, on PostgreSQL, the server's own account.
#
# Which database: decided from the environment the services are given —
# DB_DRIVER, DB_DSN, DB_DSN_INGESTOR, DB_DSN_SERVER, as set on the container
# (and by /app/data/.env, read above) — never from config.json. A config.json
# that selects PostgreSQL while the environment does not is refused: the
# server prefers config.json over the environment, so it would run on
# PostgreSQL with none of the arrangements below. A DSN with DB_DRIVER unset
# means PostgreSQL; a DSN with DB_DRIVER=sqlite is refused as ambiguous.
#
# The writer credential. The security boundary for it is a SEPARATE
# CONTAINER: on PostgreSQL run the ingestor as its own container (see
# docker-compose.postgres.yml), and start this one with DISABLE_INGESTOR=true
# and only the reader DSN (DB_DSN_SERVER, or DB_DSN). This container then has
# no ingestor program and refuses to start if DB_DSN_INGESTOR is set. The
# writer DSN is not in this container's environment or files at all, which no
# arrangement of processes inside one container can match: they share
# supervisord, and caddy runs as root with an admin endpoint.
#
# Running both services in this one container on PostgreSQL still works, with
# a warning. supervisord hands DB_DSN_INGESTOR to the ingestor and
# DB_DSN_SERVER to the server, each as DB_DSN (both default to DB_DSN).
#
# Defence in depth, either way: on PostgreSQL the server runs as its own
# account, corescope-server, uid SERVER_RUN_AS_UID (default 1001, never 0; in
# the single-container layout also never the ingestor's), so the kernel
# refuses it the /proc/<pid>/environ of supervisord and of the ingestor. Its
# environment is a whitelist (docker/server-exec.sh): no PG*, POSTGRES_* or
# other secrets reach it. SERVER_RUN_AS_UID and CORESCOPE_SHARE_GID are taken
# from the container's environment, never from .env or config.json.
#
# What the server writes lives in /app/data/shared: users.db (-wal, -shm,
# backups/) and the two file queues it shares with the ingestor,
# prune-requests/ and mbcap-snapshot/. Both services point there (the
# server's -db and the ingestor's DB_PATH only anchor these paths on
# PostgreSQL). Ownership:
#   - group corescope-share, gid CORESCOPE_SHARE_GID (default 61001). It sits
#     above the range given to ordinary users (GID_MAX 60000), so on the host
#     a bind-mounted config.json or shared directory is not opened to some
#     real user's group. The server has it as its only group; the ingestor
#     (its own container: group_add; this container: added here) is in it.
#   - /app/data/shared root:corescope-share 3770: group-writable for users.db
#     and its -wal/-shm, sticky so the server cannot rename or remove what is
#     not its own (the root-owned queue directories). Its subdirectories
#     prune-requests/ and mbcap-snapshot/ are 2770, not sticky, as each side
#     removes files the other wrote. backups/ is the server's, 0700.
#   - users.db and its -shm are created here, the server's, 0660. With
#     fs.protected_regular=2 a sticky group-writable directory refuses to
#     open another user's file with O_CREAT, so the server must own -shm
#     before the read-only ingestor can create one.
#
# config.json is the one other file the server may write (the geo-filter
# editor): group corescope-share, 0660, never /app/data, so the server cannot
# create .env (sourced here as root) or anything else there. Its owner is
# kept, except that one owned by the server goes to root. Writing it gives
# the server no way to the writer credential: the ingestor takes its DSN from
# the environment (DB_DSN overrides config.json there), a dsn in config.json
# is refused here, and the server's identity comes from the container's
# environment. A config.json that resolves outside /app/data, or is not a
# regular file, is refused.
#
# Checked as the server before supervisord starts: it can read config.json
# and reach /app/data/shared, and cannot read /app/data/.env.
#
# SQLite keeps today's arrangement: one account for both services, -db
# /app/data/meshcore.db, the ingestor's DB_PATH as the container had it, and
# the environment passed through.
# ---------------------------------------------------------------------------
_nl='
'
for _v in "${DB_DSN:-}" "${DB_DSN_INGESTOR:-}" "${DB_DSN_SERVER:-}" "${DB_PATH:-}"; do
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

SERVER_ACCOUNT=corescope-server
SHARE_GROUP=corescope-share
SHARED_DIR=/app/data/shared

pg_log() {
  printf '%s\n' "[entrypoint] postgres: $*" >&2
}

# json_key_re <key>: an ERE (for grep -Ei) matching "<key>": in JSON as Go's
# decoder reads it: any letter case, any letter written as a \u00XX escape.
json_key_re() {
  local key="$1" re='"' c lo up
  while [ -n "$key" ]; do
    c=${key%"${key#?}"}
    key=${key#?}
    lo=$(printf '%s' "$c" | tr '[:upper:]' '[:lower:]')
    up=$(printf '%s' "$c" | tr '[:lower:]' '[:upper:]')
    re="$re($lo|\\\\u00$(printf '%02x' "'$lo")|\\\\u00$(printf '%02x' "'$up"))"
  done
  printf '%s' "$re\"[[:space:]]*:"
}

# config_selects_postgres: config.json sets db.driver (any key spelling Go
# accepts) to a PostgreSQL name.
config_selects_postgres() {
  [ -f /app/config.json ] &&
    grep -Eiq "$(json_key_re driver)[[:space:]]*\"[[:space:]]*(postgres|postgresql|pg)[[:space:]]*\"" /app/config.json
}

config_has_dsn() {
  [ -f /app/config.json ] && grep -Eiq "$(json_key_re dsn)" /app/config.json
}

# db_env_mode: postgres, sqlite, or a reason to refuse (on stderr, returns 1).
db_env_mode() {
  local drv any_dsn=
  drv=$(printf '%s' "${DB_DRIVER:-}" | tr -d ' \t' | tr '[:upper:]' '[:lower:]')
  [ -n "${DB_DSN:-}${DB_DSN_INGESTOR:-}${DB_DSN_SERVER:-}" ] && any_dsn=1
  case "$drv" in
    postgres | postgresql | pg) echo postgres ;;
    '') if [ -n "$any_dsn" ]; then echo postgres; else echo sqlite; fi ;;
    sqlite | sqlite3)
      if [ -n "$any_dsn" ]; then
        echo "[entrypoint] DB_DRIVER=$DB_DRIVER but a DB_DSN* is set; unset one of them" >&2
        return 1
      fi
      echo sqlite
      ;;
    *) echo "[entrypoint] DB_DRIVER \"$DB_DRIVER\" is not recognised; use sqlite or postgres" >&2; return 1 ;;
  esac
}

as_server() {
  su -s /bin/sh "$SERVER_ACCOUNT" -c "$1"
}

# shared_dir <path> <mode>: create a directory of the shared tree, refusing a
# symlink (the services can write in its parent), root:corescope-share.
shared_dir() {
  if [ -L "$1" ] || { [ -e "$1" ] && [ ! -d "$1" ]; }; then
    pg_log "$1 is not a directory; refusing to start"
    return 1
  fi
  mkdir -p "$1" && chown root:"$SHARE_GROUP" "$1" && chmod "$2" "$1"
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

# setup_server_account <uid> <share gid> <ingestor here: true|false>:
# everything described above. Returns non-zero, with the reason on stderr,
# when it cannot be arranged.
setup_server_account() {
  local uid="$1" gid="$2" with_ingestor="$3" ingest_uid='' name f

  case "$uid" in
    '' | *[!0-9]*) pg_log "SERVER_RUN_AS_UID must be a number, got \"$uid\""; return 1 ;;
  esac
  case "$gid" in
    '' | *[!0-9]*) pg_log "CORESCOPE_SHARE_GID must be a number, got \"$gid\""; return 1 ;;
  esac
  if [ "$uid" -eq 0 ] || [ "$gid" -eq 0 ]; then
    pg_log "the server must not run as root: SERVER_RUN_AS_UID=$uid CORESCOPE_SHARE_GID=$gid"
    return 1
  fi
  if [ "$with_ingestor" = true ]; then
    ingest_uid=$(id -u "$CORESCOPE_SERVICE_USER") || return 1
    if [ "$uid" = "$ingest_uid" ]; then
      pg_log "the server must run as a uid other than the ingestor's ($ingest_uid); SERVER_RUN_AS_UID=$uid"
      return 1
    fi
  fi

  name=$(getent group "$gid" 2>/dev/null | cut -d: -f1)
  if [ -z "$name" ]; then
    addgroup -S -g "$gid" "$SHARE_GROUP" || { pg_log "cannot create group $SHARE_GROUP (gid $gid)"; return 1; }
  elif [ "$name" != "$SHARE_GROUP" ]; then
    pg_log "gid $gid already belongs to group \"$name\"; choose another CORESCOPE_SHARE_GID"
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
  if [ "$(id -g "$SERVER_ACCOUNT")" != "$gid" ] || [ "$(id -G "$SERVER_ACCOUNT")" != "$gid" ]; then
    pg_log "$SERVER_ACCOUNT must have group $SHARE_GROUP ($gid) and no other"
    return 1
  fi
  if [ "$with_ingestor" = true ] && [ "$ingest_uid" -ne 0 ] &&
     ! id -nG "$CORESCOPE_SERVICE_USER" | tr ' ' '\n' | grep -qx "$SHARE_GROUP"; then
    addgroup "$CORESCOPE_SERVICE_USER" "$SHARE_GROUP" ||
      { pg_log "cannot add $CORESCOPE_SERVICE_USER to $SHARE_GROUP"; return 1; }
  fi

  # A users.db from an earlier SQLite run moves over once, with its backups.
  if [ ! -e "$SHARED_DIR/users.db" ] && [ -f /app/data/users.db ] && [ ! -L /app/data/users.db ]; then
    shared_dir "$SHARED_DIR" 3770 || return 1
    for f in users.db users.db-wal users.db-shm; do
      if [ -f "/app/data/$f" ] && [ ! -L "/app/data/$f" ]; then mv "/app/data/$f" "$SHARED_DIR/$f" || return 1; fi
    done
    if [ -d /app/data/backups ] && [ ! -L /app/data/backups ]; then
      shared_dir "$SHARED_DIR/backups" 0700 || return 1
      find /app/data/backups -maxdepth 1 -type f -name 'users-*.db' -exec mv {} "$SHARED_DIR/backups/" \;
    fi
    pg_log "moved users.db (and its backups) from /app/data to $SHARED_DIR"
  fi

  shared_dir "$SHARED_DIR" 3770 || return 1
  shared_dir "$SHARED_DIR/prune-requests" 2770 || return 1
  shared_dir "$SHARED_DIR/mbcap-snapshot" 2770 || return 1
  shared_dir "$SHARED_DIR/backups" 0700 || return 1
  chown "$SERVER_ACCOUNT" "$SHARED_DIR/backups" || return 1
  find "$SHARED_DIR/backups" -mindepth 1 ! -type l -exec chown "$SERVER_ACCOUNT:$SHARE_GROUP" {} + || return 1
  # An empty file is an empty SQLite database, and an empty -shm one SQLite
  # initialises; created here they are the server's (see above).
  for f in users.db users.db-wal users.db-shm; do
    if [ -L "$SHARED_DIR/$f" ] || { [ -e "$SHARED_DIR/$f" ] && [ ! -f "$SHARED_DIR/$f" ]; }; then
      pg_log "$SHARED_DIR/$f is not a regular file; refusing to start"
      return 1
    fi
  done
  [ -e "$SHARED_DIR/users.db" ] || : > "$SHARED_DIR/users.db" || return 1
  [ -e "$SHARED_DIR/users.db-shm" ] || : > "$SHARED_DIR/users.db-shm" || return 1
  for f in users.db users.db-wal users.db-shm; do
    if [ -f "$SHARED_DIR/$f" ]; then
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
  # The invariant behind .env: the server cannot create files in /app/data.
  # (RUN_AS_UID hands /app/data to its account, so it must not be the server's.)
  if as_server 'test -w /app/data'; then
    pg_log "/app/data is writable by $SERVER_ACCOUNT (uid $uid); it could create .env there."
    pg_log "Give the data directory to another uid (RUN_AS_UID must differ from SERVER_RUN_AS_UID)."
    return 1
  fi
  if [ -e /app/config.json ] && ! as_server 'test -r /app/config.json'; then
    pg_log "config.json is not readable by $SERVER_ACCOUNT (uid $uid)"
    return 1
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
    pg_log "/app/data/.env is readable by $SERVER_ACCOUNT; chmod 600 it (it may hold a DSN)"
    return 1
  fi

  CORESCOPE_SERVER_USER=$SERVER_ACCOUNT
  CORESCOPE_SERVER_DB_PATH=$SHARED_DIR/meshcore.db
  if [ "$with_ingestor" = true ]; then
    CORESCOPE_INGESTOR_DB_PATH=$SHARED_DIR/meshcore.db
    # The ingestor's DB_PATH now only anchors the shared file queues, so its
    # empty-database guard would no longer see the operator's SQLite data.
    # Point the guard at that file explicitly: the ingestor refuses to start
    # on an empty PostgreSQL while it still holds transmissions.
    CORESCOPE_SQLITE_GUARD_PATH=${DB_PATH:-/app/data/meshcore.db}
    export CORESCOPE_SQLITE_GUARD_PATH
    pg_log "server runs as $SERVER_ACCOUNT (uid $uid), ingestor as $CORESCOPE_SERVICE_USER (uid $ingest_uid); shared files in $SHARED_DIR"
  else
    pg_log "server runs as $SERVER_ACCOUNT (uid $uid); no ingestor in this container; shared files in $SHARED_DIR"
  fi
}

if ! DB_MODE=$(db_env_mode); then
  exit 1
fi
case "${DISABLE_INGESTOR:-false}" in
  true) WITH_INGESTOR=false ;;
  false) WITH_INGESTOR=true ;;
  *) echo "[entrypoint] DISABLE_INGESTOR must be true or false, got \"$DISABLE_INGESTOR\"" >&2; exit 1 ;;
esac

if [ "$DB_MODE" = postgres ]; then
  if config_has_dsn; then
    pg_log "config.json sets db.dsn, which the server can read; pass DB_DSN_SERVER (and the ingestor's DSN to its own container) in the environment instead"
    exit 1
  fi
  if [ "$WITH_INGESTOR" = true ]; then
    : "${DB_DSN_INGESTOR:=${DB_DSN:-}}"
    export DB_DSN_INGESTOR
    pg_log "WARNING: the ingestor runs in this container, so its writer DSN is in this container too."
    pg_log "WARNING: run it as its own container (docker-compose.postgres.yml) and set DISABLE_INGESTOR=true here."
  elif [ -n "${DB_DSN_INGESTOR:-}" ]; then
    pg_log "DISABLE_INGESTOR=true but DB_DSN_INGESTOR is set: the writer DSN must not be given to this container"
    exit 1
  else
    unset DB_DSN_INGESTOR
  fi
  : "${DB_DSN_SERVER:=${DB_DSN:-}}"
  export DB_DSN_SERVER
  # The server reads its DSN as DB_DSN (supervisord sets it from
  # DB_DSN_SERVER); a different DB_DSN here would only be passed along.
  unset DB_DSN
  setup_server_account "$_server_run_as_uid" "$_share_gid" "$WITH_INGESTOR" || exit 1
  CORESCOPE_SERVER_ENV_WHITELIST=1
else
  if config_selects_postgres; then
    echo "[entrypoint] config.json selects PostgreSQL (db.driver), but the environment does not." >&2
    echo "[entrypoint] Set DB_DRIVER=postgres and the DSN(s) on the container; the database is chosen from the environment only." >&2
    exit 1
  fi
  : "${DB_DSN_INGESTOR:=${DB_DSN:-}}"
  : "${DB_DSN_SERVER:=${DB_DSN:-}}"
  export DB_DSN_INGESTOR DB_DSN_SERVER
  CORESCOPE_SERVER_USER=$CORESCOPE_SERVICE_USER
  CORESCOPE_SERVER_DB_PATH=/app/data/meshcore.db
  CORESCOPE_INGESTOR_DB_PATH=${DB_PATH:-}
  CORESCOPE_SERVER_ENV_WHITELIST=0
fi
unset _server_run_as_uid _share_gid
export CORESCOPE_SERVER_USER CORESCOPE_SERVER_DB_PATH CORESCOPE_INGESTOR_DB_PATH CORESCOPE_SERVER_ENV_WHITELIST

# Caddy's admin API. The image's Caddyfile turns it off (`admin off`). For a
# Caddyfile that does not, CADDY_ADMIN moves it from localhost:2019 — where
# any process here, the server included, could rewrite the proxy — to a
# socket in a directory only root can enter; and the operator is told.
CORESCOPE_CADDY_ADMIN=unix//run/caddy-admin/admin.sock
export CORESCOPE_CADDY_ADMIN
if [ "${DISABLE_CADDY:-false}" != "true" ]; then
  mkdir -p /run/caddy-admin && chown root:root /run/caddy-admin && chmod 0700 /run/caddy-admin
  if ! grep -Eq '^[[:space:]]*admin[[:space:]]+off([[:space:]]|$)' /etc/caddy/Caddyfile 2>/dev/null; then
    echo "[entrypoint] ********************************************************************" >&2
    echo "[entrypoint] WARNING: /etc/caddy/Caddyfile does not disable Caddy's admin API." >&2
    echo "[entrypoint] It is moved to a root-only socket (/run/caddy-admin/admin.sock), but" >&2
    echo "[entrypoint] add a global options block at the top of your Caddyfile:" >&2
    echo "[entrypoint]     {" >&2
    echo "[entrypoint]         admin off" >&2
    echo "[entrypoint]     }" >&2
    echo "[entrypoint] ********************************************************************" >&2
  fi
fi

# supervisord config: one per combination of the three switches.
_suffix=
if [ "$WITH_INGESTOR" = false ]; then
  echo "[config] ingestor disabled in this container (DISABLE_INGESTOR=true)"
  _suffix="$_suffix-no-ingestor"
fi
if [ "${DISABLE_MOSQUITTO:-false}" = "true" ]; then
  echo "[config] internal MQTT broker disabled (DISABLE_MOSQUITTO=true)"
  _suffix="$_suffix-no-mosquitto"
fi
if [ "${DISABLE_CADDY:-false}" = "true" ]; then
  echo "[config] Caddy reverse proxy disabled (DISABLE_CADDY=true)"
  _suffix="$_suffix-no-caddy"
fi
SUPERVISORD_CONF="/etc/supervisor/conf.d/supervisord$_suffix.conf"
unset _suffix

echo "[entrypoint] Go services run as $CORESCOPE_SERVICE_USER (uid=$(id -u "$CORESCOPE_SERVICE_USER") gid=$(id -g "$CORESCOPE_SERVICE_USER"))"

exec /usr/bin/supervisord -c "$SUPERVISORD_CONF"
