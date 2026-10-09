// Exercise the actual management functions with a fake container boundary.
// No running Docker service or real database is touched.
const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const root = path.resolve(__dirname, '../..');
const source = fs.readFileSync(path.join(root, 'manage.sh'), 'utf8').replace(/\r/g, '');
const bash = require('../../scripts/bash-path')();
const functions = ['pg_exec', 'pg_empty', 'pg_dump_file', 'pg_restore_file', 'cmd_backup', 'cmd_restore', 'prepare_staging_db', 'prepare_staging_config', 'write_env_managed_values', 'is_true', 'cmd_start']
  .map(name => source.match(new RegExp(`^${name}\\(\\)\\s*\\{[\\s\\S]*?^}`, 'm'))?.[0] || '').join('\n');
const quote = value => "'" + value.replace(/'/g, "'\\''") + "'";
const posix = value => value.replace(/\\/g, '/');
// The example's published HTTP port must reach a listener in the default
// supervisor selection. A healthy private port 3000 alone does not prove this.
const example = fs.readFileSync(path.join(root, 'docker-compose.example.yml'), 'utf8');
const disableCaddy = example.match(/DISABLE_CADDY=\$\{DISABLE_CADDY:-(true|false)\}/)?.[1];
assert(disableCaddy, 'example must declare its Caddy default');
const supervisor = fs.readFileSync(path.join(root, 'docker', disableCaddy === 'true' ? 'supervisord-go-no-caddy.conf' : 'supervisord-go.conf'), 'utf8');
const listeners = new Set([Number(supervisor.match(/corescope-server[^\n]* -port (\d+)/)?.[1])]);
if (supervisor.includes('[program:caddy]')) {
  const caddy = fs.readFileSync(path.join(root, 'docker/Caddyfile'), 'utf8');
  listeners.add(Number(caddy.match(/^:(\d+)\s*\{/m)?.[1]));
  assert(listeners.has(Number(caddy.match(/^\s*reverse_proxy localhost:(\d+)/m)?.[1])), 'Caddy upstream does not reach the application');
}
const publishedPort = Number(example.match(/HTTP_PORT:-\d+\}:(\d+)/)?.[1]);
assert(listeners.has(publishedPort), 'default example publishes HTTP to a port with no running listener');
const smoke = fs.readFileSync(path.join(root, 'scripts/test-postgres-compose.sh'), 'utf8');
assert(!/\bDISABLE_CADDY=true\b/.test(smoke), 'packaged smoke bypasses the example default HTTP listener');
for (const endpoint of ['http://127.0.0.1:3000/api/healthz', 'http://127.0.0.1:80/api/healthz', 'http://127.0.0.1:80/api/stats']) {
  assert(smoke.includes(endpoint), `packaged smoke does not exercise ${endpoint}`);
}
assert(smoke.includes('proxy_health.get("ready") is True'), 'packaged smoke does not validate readiness through Caddy');
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-pg-ops-'));
function run(command, occupied = '0', failRestore = '0', prodReady = '1') {
  const setup = `
set -e
PROD_DATA=${quote(posix(path.join(temp, 'state')))}
STAGING_DATA=${quote(posix(path.join(temp, 'staging')))}
OCCUPIED=${quote(occupied)}
FAIL_RESTORE=${quote(failRestore)}
PROD_READY=${quote(prodReady)}
CALLS=${quote(posix(path.join(temp, 'calls')))}
CORESCOPE_OWNER_PASSWORD='must-not-appear-in-argv'
log(){ :; }; info(){ :; }; warn(){ :; }; err(){ printf '%s\\n' "$*" >&2; }
confirm(){ return 0; }; container_running(){ return 1; }
docker(){ return 0; }; require_database_credentials(){ :; }
preflight_validate_prod_ports(){ return 0; }; migrate_config(){ :; }; ensure_config(){ :; }
database_reply(){
 case "$*" in
  *pg_dump*) [ "$PROD_READY" = 1 ] || { err 'Production PostgreSQL is not running'; return 1; }; printf 'PGDMP-native-test-archive';;
  *pg_restore*) cat >/dev/null; [ "$FAIL_RESTORE" != 1 ];;
  *SELECT*COUNT*) printf '%s\\n' "$OCCUPIED";;
  *) :;;
 esac
}
dc_prod(){
 printf 'prod %s\\n' "$*" >> "$CALLS"
 if [ "$*" = 'up -d prod' ]; then
  [ "$PROD_READY" != fail ] || return 1
  PROD_READY=1
 fi
 database_reply "$@"
}
dc_staging(){ printf 'staging %s\\n' "$*" >> "$CALLS"; database_reply "$@"; }
${functions}
${command}
`;
  // Keep the extracted shell source off Windows' bounded command line.
  return spawnSync(bash, ['--noprofile', '--norc'], { input: setup, cwd: temp, encoding: 'utf8', timeout: 20000 });
}
try {
  fs.mkdirSync(path.join(temp, 'state'));
  fs.mkdirSync(path.join(temp, 'docker'));
  fs.writeFileSync(path.join(temp, 'docker/postgres-grants.sql'), 'SELECT 1;');
  fs.writeFileSync(path.join(temp, 'state/meshcore.db'), 'SQLite format 3\0legacy recovery');
  fs.writeFileSync(path.join(temp, 'state/config.json'), '{"siteName":"test"}');
  const backup = path.join(temp, 'backup');
  const made = run(`cmd_backup ${quote(posix(backup))}`);
  assert.strictEqual(made.status, 0, made.stderr || made.error?.message);
  for (const name of ['telemetry.dump', 'accounts.dump']) {
    assert(fs.existsSync(path.join(backup, name)), `native archive missing: ${name}`);
    assert(fs.readFileSync(path.join(backup, name), 'utf8').startsWith('PGDMP'));
  }
  assert(fs.existsSync(path.join(backup, 'config.json')), 'configuration omitted');
  assert(!fs.existsSync(path.join(backup, 'meshcore.db')), 'backup copied a legacy live SQLite file');
  const blocked = run(`cmd_restore ${quote(posix(backup))}`, '1');
  assert.notStrictEqual(blocked.status, 0, 'restore overwrote a nonempty destination');
  assert(!fs.readFileSync(path.join(temp, 'calls'), 'utf8').includes('pg_restore'), 'restore ran before emptiness check');
  const failedRestore = run(`cmd_restore ${quote(posix(backup))}`, '0', '1');
  assert.notStrictEqual(failedRestore.status, 0, 'failed native restore was reported as success');
  const restored = run(`cmd_restore ${quote(posix(backup))}`);
  assert.strictEqual(restored.status, 0, restored.stderr || restored.error?.message);
  const calls = fs.readFileSync(path.join(temp, 'calls'), 'utf8');
  assert(calls.includes('pg_restore'), 'native restore was not invoked');
  assert(!calls.includes('must-not-appear-in-argv'), 'database password appeared in command arguments');
  const legacy = run(`cmd_restore ${quote(posix(path.join(temp, 'state/meshcore.db')))}`);
  assert.notStrictEqual(legacy.status, 0, 'SQLite restore accepted without offline import');
  fs.writeFileSync(path.join(temp, 'calls'), '');
  const staged = run('prepare_staging_db');
  assert.strictEqual(staged.status, 0, staged.stderr || staged.error?.message);
  assert(!fs.readFileSync(path.join(temp, 'calls'), 'utf8').includes('corescope_accounts'), 'staging copied production accounts');
  fs.writeFileSync(path.join(temp, 'calls'), '');
  const productionOnly = run("cmd_start ''", '0', '0', '0');
  assert.strictEqual(productionOnly.status, 0, productionOnly.stderr || productionOnly.error?.message);
  assert.deepStrictEqual(fs.readFileSync(path.join(temp, 'calls'), 'utf8').trim().split('\n'), ['prod up -d prod'], 'ordinary cold start changed');
  fs.writeFileSync(path.join(temp, 'calls'), '');
  const coldStaging = run('cmd_start --with-staging', '0', '0', '0');
  assert.strictEqual(coldStaging.status, 0, coldStaging.stderr || coldStaging.error?.message);
  const startCalls = fs.readFileSync(path.join(temp, 'calls'), 'utf8').trim().split('\n');
  assert.strictEqual(startCalls.filter(call => call === 'prod up -d prod').length, 1, 'production must start once through normal Compose dependencies');
  assert(startCalls.indexOf('prod up -d prod') < startCalls.findIndex(call => call.includes('pg_dump')), 'staging dump preceded production bootstrap');
  const restoreIndex = startCalls.findIndex(call => call.includes('pg_restore'));
  assert(restoreIndex >= 0 && restoreIndex < startCalls.indexOf('staging up -d staging-go'), 'staging application started before restore');
  assert(!startCalls.some(call => call.includes('corescope_accounts')), 'cold staging start copied production accounts');
  fs.writeFileSync(path.join(temp, 'calls'), '');
  const retainedStaging = run('cmd_start --with-staging', '1', '0', '0');
  assert.strictEqual(retainedStaging.status, 0, retainedStaging.stderr || retainedStaging.error?.message);
  assert(!/pg_dump|pg_restore/.test(fs.readFileSync(path.join(temp, 'calls'), 'utf8')), 'cold start replaced existing staging data');
  fs.writeFileSync(path.join(temp, 'calls'), '');
  const failedStart = run('cmd_start --with-staging', '0', '0', 'fail');
  assert.notStrictEqual(failedStart.status, 0, 'failed production startup reported success');
  assert.deepStrictEqual(fs.readFileSync(path.join(temp, 'calls'), 'utf8').trim().split('\n'), ['prod up -d prod'], 'staging started after production bootstrap failed');
  for (const [executable, script] of [
    [process.execPath, 'scripts/migrate-dedup.js'],
    [process.platform === 'win32' ? 'python' : 'python3', 'scripts/prune-nodes-outside-geo-filter.py']
  ]) {
    const retired = spawnSync(executable, [path.join(root, script)], { cwd: temp, encoding: 'utf8', timeout: 10000 });
    assert.notStrictEqual(retired.status, 0, 'legacy tool executed without explicit offline opt-in');
    assert(/offline legacy SQLite/i.test(retired.stderr), retired.stderr);
  }
  fs.writeFileSync(path.join(temp, '.env'), 'CORESCOPE_OWNER_PASSWORD=private-test-value\nPROD_HTTP_PORT=80\n', { mode: 0o600 });
  const configured = run("write_env_managed_values 8080 8443 1883 ./state false");
  assert.strictEqual(configured.status, 0, configured.stderr);
  const config = fs.readFileSync(path.join(temp, '.env'), 'utf8');
  assert(config.includes('CORESCOPE_OWNER_PASSWORD=private-test-value'), 'setup lost database credentials');
  assert(config.includes('PROD_HTTP_PORT=8080'), 'setup did not update the requested port');
  if (process.platform !== 'win32') assert.strictEqual(fs.statSync(path.join(temp, '.env')).mode & 0o777, 0o600, 'setup exposed private credentials');
  console.log('PostgreSQL management backup/restore safeguards passed');
} finally {
  const base = path.resolve(os.tmpdir()) + path.sep;
  assert(path.resolve(temp).startsWith(base) && path.basename(temp).startsWith('corescope-pg-ops-'));
  fs.rmSync(temp, { recursive: true });
}
