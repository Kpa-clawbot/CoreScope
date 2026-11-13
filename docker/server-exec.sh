#!/bin/sh
# server-exec.sh — supervisord's command for corescope-server: execs
# /app/corescope-server with the given arguments.
#
# On PostgreSQL (the entrypoint exports CORESCOPE_SERVER_ENV_WHITELIST=1) the
# server is started with `env -i` and only the variables named below, so
# nothing else in the container's environment — PG*, POSTGRES_*, a writer DSN,
# a broker password — reaches it. The list is what cmd/server reads (grep
# os.Getenv / LookupEnv / envOrValue there) plus the Go runtime, TLS and proxy
# variables. DB_DSN is the server's own: supervisord sets it from
# DB_DSN_SERVER. A variable the server starts reading must be added here.
#
# On SQLite the environment is passed on as before, less DB_DSN_INGESTOR.

if [ "${CORESCOPE_SERVER_ENV_WHITELIST:-}" != 1 ]; then
  unset DB_DSN_INGESTOR
  exec /app/corescope-server "$@"
fi

set -- /app/corescope-server "$@"
_set='' _value=''
for _name in \
  PATH HOME TZ LANG LC_ALL \
  GOMEMLIMIT GOGC GODEBUG GOMAXPROCS GOTRACEBACK \
  SSL_CERT_FILE SSL_CERT_DIR \
  HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy \
  DB_DRIVER DB_DSN \
  CORS_ALLOWED_ORIGINS ENABLE_PPROF PPROF_PORT \
  CORESCOPE_VERSION CORESCOPE_INGESTOR_STATS \
  CORESCOPE_OBSERVER_HOOK_SECRET CORESCOPE_OBSERVER_KEY \
  CORESCOPE_BREVO_API_KEY CORESCOPE_POSTAL_API_KEY \
  CORESCOPE_BREVO_WEBHOOK_SECRET CORESCOPE_POSTAL_WEBHOOK_SECRET; do
  eval "_set=\${$_name+set}"
  if [ "$_set" = set ]; then
    eval "_value=\$$_name"
    set -- "$_name=$_value" "$@"
  fi
done
unset _name _set _value
exec /usr/bin/env -i "$@"
