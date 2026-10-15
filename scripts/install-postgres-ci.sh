#!/usr/bin/env bash
# Exact PostgreSQL client/server packages for disposable Ubuntu CI runners.
set -euo pipefail
mode=${1:-client}
[[ ${GITHUB_ACTIONS:-} == true ]] || { echo 'This installer is for disposable GitHub Actions runners.' >&2; exit 1; }
[[ $mode == client || $mode == server ]] || { echo 'Usage: install-postgres-ci.sh client|server' >&2; exit 1; }
sudo install -d /usr/share/postgresql-common/pgdg
curl --fail --show-error --silent https://www.postgresql.org/media/keys/ACCC4CF8.asc |
  sudo tee /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc >/dev/null
. /etc/os-release
printf 'deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt-archive.postgresql.org/pub/repos/apt %s-pgdg-archive main\n' "$VERSION_CODENAME" |
  sudo tee /etc/apt/sources.list.d/corescope-pgdg.list >/dev/null
sudo apt-get update -qq
client_version=$(apt-cache madison postgresql-client-18 | awk '$3 ~ /^18[.]6-/ && !found {print $3; found=1} END {if(!found) exit 1}')
packages=("postgresql-client-18=$client_version" sqlite3)
if [[ $mode == server ]]; then
  # The benchmark owns its own cluster. Package installation must not launch
  # an extra unmeasured PostgreSQL service on the runner.
  sudo install -d /etc/postgresql-common
  printf 'create_main_cluster = false\n' | sudo tee /etc/postgresql-common/createcluster.conf >/dev/null
  server_version=$(apt-cache madison postgresql-18 | awk '$3 ~ /^18[.]6-/ && !found {print $3; found=1} END {if(!found) exit 1}')
  packages+=("postgresql-18=$server_version")
fi
sudo apt-get install -y --no-install-recommends "${packages[@]}"
echo /usr/lib/postgresql/18/bin >> "$GITHUB_PATH"
/usr/lib/postgresql/18/bin/pg_dump --version | grep -E '18[.]6([ (]|$)'
if [[ $mode == server ]]; then /usr/lib/postgresql/18/bin/postgres --version | grep -E '18[.]6([ (]|$)'; fi
