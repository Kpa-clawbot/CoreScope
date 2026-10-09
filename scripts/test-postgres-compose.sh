#!/usr/bin/env bash
# CI-only packaged startup/ingestion smoke. No image publication or host ports.
set +x
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && $(uname -s) == Linux ]] || { echo 'This smoke requires a disposable Linux GitHub Actions runner.' >&2; exit 1; }
[[ ${GITHUB_SHA:-} =~ ^[0-9a-f]{40}$ && ${1:-} == "corescope-pg-smoke:$GITHUB_SHA" ]] || { echo 'An exact locally built candidate image is required.' >&2; exit 1; }
repo=$(cd "$(dirname "$0")/.." && pwd -P)
temp_root=$(realpath -e "${RUNNER_TEMP:?RUNNER_TEMP is required}")
[[ -x "$repo/docker/postgres-init.sh" ]] || { echo 'PostgreSQL initialization must be executable, not sourced by the vendor entrypoint.' >&2; exit 1; }
image=$1
image_id=$(timeout 15s docker image inspect --format '{{.Id}}' "$image")
umask 077
work=$(mktemp -d "$temp_root/corescope-pg-smoke-XXXXXXXX")
project=$(basename "$work" | tr '[:upper:]' '[:lower:]')
owned_project=false
phase=configuration
dc=(docker compose --ansi never --project-name "$project" --env-file "$work/.env"
    -f "$repo/docker-compose.example.yml" -f "$work/override.yml")

project_resources() {
  timeout 10s docker container ls -aq --filter "label=com.docker.compose.project=$project" || return 1
  timeout 10s docker network ls -q --filter "label=com.docker.compose.project=$project" || return 1
  timeout 10s docker volume ls -q --filter "label=com.docker.compose.project=$project"
}

finish() {
  local result=$? cleanup_ok=true
  trap - EXIT INT TERM
  set +e
  if [ "$result" -ne 0 ] && $owned_project; then
    timeout 20s "${dc[@]}" logs --no-color --tail 100 > "$work/container.raw.log" 2>&1
  fi
  if $owned_project; then
    timeout 60s "${dc[@]}" down --volumes --remove-orphans --timeout 20 >> "$work/actions.raw.log" 2>&1 || cleanup_ok=false
    remaining=$(project_resources) || cleanup_ok=false
    [ -z "$remaining" ] || cleanup_ok=false
  fi
  if ! $cleanup_ok; then result=1; phase=cleanup; fi
  # Persist only bounded, redacted diagnostics; never print Compose's full config.
  python3 - "$work" <<'PY' || result=1
import os,re,sys
from pathlib import Path
root=Path(sys.argv[1])
text=""
for name in ("actions.raw.log","container.raw.log"):
    path=root/name
    if path.exists():
        with path.open("rb") as source:
            text += source.read(262144).decode("utf-8",errors="replace")+"\n"
for key,value in os.environ.items():
    if key.endswith("_PASSWORD") and value:
        text=text.replace(value,"[redacted]")
text=re.sub(r"postgres(?:ql)?://[^\s'\"<>]+","[redacted database URL]",text)
(root/"failure.log").write_text(text,encoding="utf-8")
PY
  # Remove only this mktemp directory's private data, after the project is gone.
  if [[ $(realpath -e "$work") == "$temp_root"/corescope-pg-smoke-* && ! -L "$work" ]]; then
    if $cleanup_ok; then
      sudo rm -rf -- "$work/state" "$work/postgres" || result=1
      rm -f -- "$work/.env" "$work/compose.json" "$work/override.yml" "$work/packet.json" "$work/stats.json" "$work/states.json" "$work/health.json" "$work/actions.raw.log" "$work/container.raw.log" || result=1
    fi
    if [ "$result" -eq 0 ]; then
      rm -f -- "$work/failure.log"
      rmdir -- "$work" || result=1
    else
      echo "Compose smoke failed during $phase; sanitized diagnostics: $work/failure.log" >&2
      tail -n 120 "$work/failure.log" >&2
    fi
  else
    echo 'Refusing cleanup outside the owned smoke directory.' >&2
    result=1
  fi
  exit "$result"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for key in POSTGRES_ADMIN_PASSWORD CORESCOPE_OWNER_PASSWORD CORESCOPE_READER_PASSWORD CORESCOPE_WRITER_PASSWORD CORESCOPE_ACCOUNTS_PASSWORD CORESCOPE_CHANNELS_PASSWORD; do
  password=$(openssl rand -hex 32)
  export "$key=$password"
  printf '%s=%s\n' "$key" "${!key}" >> "$work/.env"
done
unset password
export CORESCOPE_IMAGE="$image" DATA_DIR="$work/state" POSTGRES_DATA_DIR="$work/postgres"
export DISABLE_MOSQUITTO=false DISABLE_CADDY=true
mkdir "$work/state"
python3 - "$repo" "$work" <<'PY'
import json,re,sys
from pathlib import Path
repo,work=map(Path,sys.argv[1:])
config={"siteName":"CI packaged PostgreSQL smoke","port":3000,"stateDir":"/app/data",
        "mqttSources":[{"name":"ci-smoke","broker":"mqtt://localhost:1883","topics":["meshcore/SJC/ci-smoke/packets"]}],
        "userManagement":{"enabled":False},"autoRegionKeys":{"enabled":False}}
(work/"state/config.json").write_text(json.dumps(config),encoding="utf-8")
# Reuse the existing ingestor regression's packet, not invented wire bytes.
test=(repo/"cmd/ingestor/main_test.go").read_text(encoding="utf-8").split("func TestHandleMessageRawPacket(t *testing.T) {",1)[1].split("\n}",1)[0]
raw=re.search(r'rawHex := "([0-9A-Fa-f]+)"',test).group(1)
bytes.fromhex(raw)
(work/"packet.json").write_text(json.dumps({"raw":raw,"SNR":5.5,"RSSI":-100.0,"origin":"ci-smoke"})+"\n",encoding="utf-8")
PY
cat > "$work/override.yml" <<'YAML'
services:
  postgres:
    restart: "no"
    volumes:
      - postgres-smoke:/var/lib/postgresql
  bootstrap:
    pull_policy: never
  corescope:
    pull_policy: never
    restart: "no"
    ports: !reset []
networks:
  default:
    internal: true
volumes:
  postgres-smoke:
YAML

timeout 30s "${dc[@]}" config --format json > "$work/compose.json" 2>> "$work/actions.raw.log"
python3 - "$repo" "$work" <<'PY'
import json,sys
from pathlib import Path
repo,work=map(Path,sys.argv[1:])
config=json.loads((work/"compose.json").read_text())
assert config["name"]==work.name.lower()
assert set(config["services"])=={"postgres","bootstrap","corescope"}
assert set(config["networks"])=={"default"}
assert config["networks"]["default"]["internal"] is True
allowed={(work/"state").resolve(),(repo/"docker/postgres-init.sh").resolve()}
for service in config["services"].values():
    assert not service.get("ports"), "smoke must publish no ports"
    assert not service.get("network_mode") and set(service["networks"])=={"default"}
    for mount in service.get("volumes",[]):
        if mount["type"]=="bind":
            assert Path(mount["source"]).resolve() in allowed, "unexpected host mount"
for volume in config["volumes"].values():
    assert not volume.get("external") and volume["name"].startswith(config["name"]+"_")
PY
existing=$(project_resources)
[[ -z "$existing" ]] || { echo 'Smoke project already exists; refusing to touch it.' >&2; exit 1; }
owned_project=true
phase=startup
timeout 210s "${dc[@]}" up -d --wait --wait-timeout 180 >> "$work/actions.raw.log" 2>&1
bootstrap=$(timeout 10s "${dc[@]}" ps -a -q bootstrap)
application=$(timeout 10s "${dc[@]}" ps -q corescope)
phase=bootstrap-order
[[ $(timeout 10s docker inspect --format '{{.Image}}' "$application") == "$image_id" ]]
timeout 10s docker inspect --format '{{json .State}}' "$bootstrap" "$application" > "$work/states.json"
python3 - "$work/states.json" <<'PY'
import datetime,json,sys
bootstrap,app=map(json.loads,open(sys.argv[1],encoding="utf-8"))
assert bootstrap["Status"]=="exited" and bootstrap["ExitCode"]==0
assert app["Running"]
stamp=lambda value: datetime.datetime.fromisoformat(value.replace("Z","+00:00"))
assert stamp(app["StartedAt"]) >= stamp(bootstrap["FinishedAt"]), "application preceded bootstrap completion"
PY
timeout 10s "${dc[@]}" exec -T corescope pg_dump --version | grep -Eq '^pg_dump .* 18[.]'

phase=schema-readiness
owner_sql() {
  timeout 10s "${dc[@]}" exec -T postgres sh -eu -c '
    export PGHOST=127.0.0.1 PGUSER=corescope_owner PGPASSWORD="$CORESCOPE_OWNER_PASSWORD" PGDATABASE="$1"
    exec psql -X -A -t -v ON_ERROR_STOP=1
  ' sh "$1" 2>> "$work/actions.raw.log"
}
[[ $(owner_sql corescope_telemetry <<'SQL'
SELECT version=1 AND ready AND current_setting('server_version_num')='180006' FROM corescope_schema WHERE kind='telemetry';
SQL
) == t ]]
[[ $(owner_sql corescope_accounts <<'SQL'
SELECT version=1 AND ready AND (SELECT version=5 FROM schema_version) FROM corescope_schema WHERE kind='accounts';
SQL
) == t ]]

phase=mqtt-ingestion
timeout 10s "${dc[@]}" exec -T corescope mosquitto_pub -h 127.0.0.1 -q 1 -r -t meshcore/SJC/ci-smoke/packets -s < "$work/packet.json" >> "$work/actions.raw.log" 2>&1
for attempt in $(seq 1 30); do
  if timeout 5s "${dc[@]}" exec -T corescope wget -qO- http://127.0.0.1:3000/api/healthz > "$work/health.json" 2>> "$work/actions.raw.log" &&
     timeout 5s "${dc[@]}" exec -T corescope wget -qO- http://127.0.0.1:3000/api/stats > "$work/stats.json" 2>> "$work/actions.raw.log" &&
     python3 - "$work" "$GITHUB_SHA" <<'PY'
import json,sys
from pathlib import Path
root=Path(sys.argv[1])
health=json.loads((root/"health.json").read_text())
stats=json.loads((root/"stats.json").read_text())
sys.exit(0 if health.get("ready") is True and stats.get("totalTransmissions")==1 and stats.get("totalObservations")==1 and stats.get("commit")==sys.argv[2][:7] else 1)
PY
  then break; fi
  [ "$attempt" -lt 30 ] || { echo 'Published fixture did not become visible through the packaged API.' >&2; exit 1; }
  sleep 1
done
phase=runtime-roles
[[ $(owner_sql corescope_telemetry <<'SQL'
SELECT (SELECT count(*)=1 FROM transmissions)
 AND (SELECT count(*)=1 FROM observations)
 AND EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND usename='corescope_reader')
 AND EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND usename='corescope_writer')
 AND NOT has_table_privilege('corescope_reader','transmissions','INSERT,UPDATE,DELETE')
 AND NOT has_schema_privilege('corescope_reader','public','CREATE')
 AND NOT has_schema_privilege('corescope_writer','public','CREATE');
SQL
) == t ]]
echo 'Packaged Compose bootstrap, restricted PostgreSQL runtime, MQTT ingestion, API visibility and pg_dump availability passed.'
