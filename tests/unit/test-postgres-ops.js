// Exercise the actual management functions with a fake container boundary.
// No running Docker service or real database is touched.
const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const root = path.resolve(__dirname, '../..');
const workflow=fs.readFileSync(path.join(root,'.github/workflows/deploy.yml'),'utf8').replace(/\r/g,'');
const e2eJob=workflow.split('\n  e2e-shard:\n')[1]?.split(/^  [a-z][a-z0-9-]*:\s*$/m)[0];
const userStep=workflow.split('- name: Start user-management E2E server (fake mailer)')[1]?.split('\n      - name:')[0];
const sqliteSetup=userStep?.split('if [ "$CORESCOPE_TEST_BACKEND" = sqlite ]; then')[1]?.split(/^\s*else\s*$/m)[0];
const fixtureCommands=sqliteSetup?.replace(/\\\n\s*/g,' ').match(/\.\/corescope-migrate[^\n]+/g)||[];
assert.deepStrictEqual({actions:fixtureCommands.map(command=>command.match(/-storage-action=(\w+)/)?.[1]),failFastDisabled:/strategy:\n\s+fail-fast: false\n/.test(e2eJob||'')},
  {actions:['adopt','setup'],failFastDisabled:true},'UM fixture must adopt legacy telemetry then initialize accounts; all independent E2E shards must run');
const fixtureArgs='-backend=sqlite -offline -config-dir /tmp/cs-um -selection-file /tmp/cs-um/storage-selection.json -sqlite-path "$E2E_SQLITE" -users-sqlite-path /tmp/cs-um/users.db';
for(const command of fixtureCommands){
  assert.strictEqual(command.replace(/^\.\/corescope-migrate -storage-action=\w+\s+/,'').replace(/\s+/g,' '),fixtureArgs,'account setup changed the adopted selection, SQLite paths or config directory');
}
const source = fs.readFileSync(path.join(root, 'manage.sh'), 'utf8').replace(/\r/g, '');
const bash = require('../../scripts/bash-path')();
const functions = ['pg_container_exec', 'pg_exec', 'pg_empty', 'pg_dump_file', 'pg_restore_file', 'sqlite_source_exists', 'sqlite_dump_file', 'backup_state', 'stage_backup_state', 'restore_sqlite_bundle', 'cmd_backup', 'cmd_restore', 'prepare_staging_db', 'prepare_staging_config', 'write_env_managed_values', 'is_true', 'cmd_start']
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
const nilPreparation=smoke.slice(smoke.indexOf('phase=nil-account-adoption'),smoke.indexOf('phase=startup'));
assert(nilPreparation.includes('-storage-action=adopt')&&nilPreparation.includes('-users-sqlite-path /app/data/uninitialized-users.db'), 'packaged smoke lacks native adoption of telemetry without accounts');
assert(nilPreparation.includes('bootstrap switch -backend=postgres'),'packaged nil-account fixture bypasses the verified switch');
const firstAccount=smoke.slice(smoke.indexOf('phase=first-account-setup'),smoke.indexOf('phase=recreation'));
assert(firstAccount.indexOf('stop corescope')>=0&&firstAccount.indexOf('stop corescope')<firstAccount.indexOf('bootstrap setup -backend=postgres'),'first-account setup must stop the owned writer/selection lease before setup');
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-pg-ops-'));
function run(command, occupied = '0', failRestore = '0', prodReady = '1', backend = 'postgres', hasAccounts = 'true', backupAccounts = hasAccounts) {
  const setup = `
set -e
PROD_DATA=${quote(posix(path.join(temp, 'state')))}
STAGING_DATA=${quote(posix(path.join(temp, 'staging')))}
OCCUPIED=${quote(occupied)}
FAIL_RESTORE=${quote(failRestore)}
PROD_READY=${quote(prodReady)}
BACKEND=${quote(backend)}
HAS_ACCOUNTS=${quote(hasAccounts)}
BACKUP_HAS_ACCOUNTS=${quote(backupAccounts)}
CALLS=${quote(posix(path.join(temp, 'calls')))}
CORESCOPE_OWNER_PASSWORD='must-not-appear-in-argv'
log(){ :; }; info(){ :; }; warn(){ :; }; err(){ printf '%s\\n' "$*" >&2; }
confirm(){ return 0; }; container_running(){ return 1; }
docker(){ return 0; }; require_database_credentials(){ :; }
selected_backend(){ printf '%s\\n' "$BACKEND"; }
require_managed_postgres(){ [ "$BACKEND" = postgres ]; }
storage_field(){ case "$2" in has_accounts) echo "$HAS_ACCOUNTS";;state) if [ "$1" = staging ]; then echo unrecorded; else echo ready; fi;;backend) echo "$BACKEND";;telemetry.sqlite_path) echo '/app/data/custom telemetry.db';;accounts.sqlite_path) echo '/app/data/users.db';;state_dir) echo '/app/data';;esac; }
storage_action(){ :; }
storage_at(){ shift 2; if [ "$1" = field ]; then if [ "$2" = has_accounts ]; then echo "$BACKUP_HAS_ACCOUNTS"; else storage_field prod "$2"; fi; elif [ "$1" = managed-postgres ]; then [ "$BACKEND" = postgres ]; fi; }
dc_staging_base(){ dc_prod_base "$@"; }
dc_prod_base(){
 printf 'base %s\\n' "$*" >> "$CALLS"
 case "$*" in
  *'--entrypoint sqlite3'*)
   case "$*" in *'PRAGMA quick_check'*) printf 'ok\\n'; return;;esac
   local destination='' previous='' part
   for part in "$@"; do [ "$previous" != -v ] || destination="\${part%:/backup}"; previous="$part"; done
   case "$*" in *telemetry.db.partial*) printf 'SQLite format 3\\0native WAL snapshot' >| "$destination/telemetry.db.partial";;*accounts.db.partial*) printf 'SQLite format 3\\0native accounts snapshot' >| "$destination/accounts.db.partial";;esac
   ;;
  *) :;;
 esac
}
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
dc_postgres(){ local environment="$1"; shift; "dc_$environment" "$@"; }
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
  const sqliteBackup = path.join(temp, 'sqlite-backup');
  const sqlite = run(`cmd_backup ${quote(posix(sqliteBackup))}`, '0', '0', '1', 'sqlite');
  assert.strictEqual(sqlite.status, 0, sqlite.stderr || sqlite.error?.message);
  for (const name of ['telemetry.db','accounts.db']) assert(fs.existsSync(path.join(sqliteBackup,name)), 'SQLite native snapshot missing: '+name);
  assert(fs.readFileSync(path.join(sqliteBackup,'telemetry.db')).includes(Buffer.from('native WAL snapshot')), 'backup copied the live main file instead of taking a native snapshot');
  const sqliteCalls = fs.readFileSync(path.join(temp,'calls'),'utf8');
  assert(sqliteCalls.includes('--entrypoint sqlite3') && sqliteCalls.includes('-readonly') && sqliteCalls.includes('/app/data/custom telemetry.db'), 'backup ignored the selected path or read-only boundary');
  assert(!sqliteCalls.includes('pg_dump'), 'SQLite backup contacted PostgreSQL');
  assert(fs.existsSync(path.join(sqliteBackup,'config.json')), 'SQLite backup omitted configuration');
  assert(fs.existsSync(path.join(sqliteBackup,'state.tar')), 'SQLite backup omitted persistent selection/queue/state recovery archive');
  const missingRecorded = run(`
    storage_field(){ case "$2" in has_accounts) echo true;;state) echo ready;;backend) echo sqlite;;*) printf '%s/missing-recorded.db\\n' "$PROD_DATA";;esac; }
    sqlite_dump_file(){ :; }
    dc_prod_base(){ while [ "$1" != -c ]; do shift; done; shift; bash -eu -c "$@"; }
    cmd_backup ${quote(posix(path.join(temp,'missing-recorded-backup')))}
  `, '0','0','1','sqlite');
  assert.notStrictEqual(missingRecorded.status,0,'backup labelled a missing recorded account database as intentionally absent');
  const badAccounts = path.join(temp,'bad-account-backup');
  fs.cpSync(sqliteBackup,badAccounts,{recursive:true});
  fs.unlinkSync(path.join(badAccounts,'accounts.db'));
  fs.writeFileSync(path.join(badAccounts,'accounts.absent'),'');
  const badRestore = run(`PROD_DATA=${quote(posix(path.join(temp,'bad-restored-state')))}; cmd_restore ${quote(posix(badAccounts))}`,'0','0','1','sqlite');
  assert.notStrictEqual(badRestore.status,0,'restore skipped initialized accounts based on an absence marker');
  const restoredSQLite = path.join(temp,'restored-sqlite');
  const sqliteRestore = run(`PROD_DATA=${quote(posix(restoredSQLite))}; cmd_restore ${quote(posix(sqliteBackup))}`, '0', '0', '1', 'sqlite');
  assert.strictEqual(sqliteRestore.status,0,sqliteRestore.stderr || sqliteRestore.error?.message);
  assert(fs.readFileSync(path.join(restoredSQLite,'custom telemetry.db')).includes(Buffer.from('native WAL snapshot')), 'SQLite restore ignored the recorded target');
  assert(fs.existsSync(path.join(restoredSQLite,'users.db')), 'SQLite restore lost accounts');
  assert(fs.existsSync(path.join(restoredSQLite,'config.json')), 'SQLite restore lost persistent state');
  const repeatedSQLite = run(`PROD_DATA=${quote(posix(restoredSQLite))}; cmd_restore ${quote(posix(sqliteBackup))}`, '0', '0', '1', 'sqlite');
  assert.notStrictEqual(repeatedSQLite.status,0,'SQLite restore overwrote existing state');
  fs.writeFileSync(path.join(temp,'calls'),'');
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
  const nilBackup=path.join(temp,'nil-account-backup');
  fs.writeFileSync(path.join(temp,'calls'),'');
  const nilMade=run(`cmd_backup ${quote(posix(nilBackup))}`,'0','0','1','postgres','false');
  assert.strictEqual(nilMade.status,0,nilMade.stderr||nilMade.error?.message);
  assert(fs.existsSync(path.join(nilBackup,'accounts.absent')),'nullable account target was not recorded as absent');
  assert(!fs.existsSync(path.join(nilBackup,'accounts.dump')),'backup dumped an unselected account database');
  assert(!fs.readFileSync(path.join(temp,'calls'),'utf8').includes('corescope_accounts'),'nil-account backup contacted the account database');
  fs.writeFileSync(path.join(temp,'calls'),'');
  const nilRestore=run(`cmd_restore ${quote(posix(nilBackup))}`,'0','0','1','postgres','false');
  assert.strictEqual(nilRestore.status,0,nilRestore.stderr||nilRestore.error?.message);
  const nilCalls=fs.readFileSync(path.join(temp,'calls'),'utf8');
  assert(nilCalls.includes('pg_restore'),'telemetry-only restore did not restore telemetry');
  assert(!nilCalls.includes('corescope_accounts'),'telemetry-only restore read or wrote the unselected account database');
  for(const [bundle,selected,recorded] of [[nilBackup,'true','false'],[nilBackup,'false','true'],[backup,'false','true']]){
    fs.writeFileSync(path.join(temp,'calls'),'');
    const mismatch=run(`cmd_restore ${quote(posix(bundle))}`,'0','0','1','postgres',selected,recorded);
    assert.notStrictEqual(mismatch.status,0,'restore accepted account snapshot/selection disagreement');
    assert(!fs.readFileSync(path.join(temp,'calls'),'utf8').includes('pg_restore'),'mismatched selection reached restore writes');
  }
  const legacyBundle=path.join(temp,'legacy-two-dump-backup');
  fs.cpSync(backup,legacyBundle,{recursive:true});fs.unlinkSync(path.join(legacyBundle,'state.tar'));
  const legacyBundleRestore=run(`cmd_restore ${quote(posix(legacyBundle))}`);
  assert.strictEqual(legacyBundleRestore.status,0,'existing two-archive PG backup support regressed: '+legacyBundleRestore.stderr);
  const absentWithoutSelection=path.join(temp,'absence-without-selection');
  fs.cpSync(nilBackup,absentWithoutSelection,{recursive:true});fs.unlinkSync(path.join(absentWithoutSelection,'state.tar'));
  fs.writeFileSync(path.join(temp,'calls'),'');
  const unprovedAbsence=run(`cmd_restore ${quote(posix(absentWithoutSelection))}`,'0','0','1','postgres','false');
  assert.notStrictEqual(unprovedAbsence.status,0,'restore trusted a bare absence marker without its retained selection');
  assert(!fs.readFileSync(path.join(temp,'calls'),'utf8').includes('pg_restore'),'unproved account absence reached restore writes');
  fs.writeFileSync(path.join(temp,'calls'),'');
  const unselectedSQL=run('pg_exec prod corescope_accounts psql -c SELECT','0','0','1','postgres','false');
  assert.notStrictEqual(unselectedSQL.status,0,'generic managed executor contacted an unselected account target');
  assert(!fs.readFileSync(path.join(temp,'calls'),'utf8').includes('corescope_accounts'),'unselected database reached the container boundary');
  const legacy = run(`cmd_restore ${quote(posix(path.join(temp, 'state/meshcore.db')))}`);
  assert.notStrictEqual(legacy.status, 0, 'SQLite restore accepted without offline import');
  fs.writeFileSync(path.join(temp, 'calls'), '');
  const sqliteStage = run('require_managed_postgres(){ :; }; prepare_staging_db', '0', '0', '1', 'sqlite');
  assert.strictEqual(sqliteStage.status,0,sqliteStage.stderr || sqliteStage.error?.message);
  assert(fs.existsSync(path.join(temp,'staging/custom telemetry.db')), 'SQLite staging did not clone native telemetry');
  assert(!fs.existsSync(path.join(temp,'staging/users.db')), 'staging copied production accounts');
  assert(!fs.readFileSync(path.join(temp,'calls'),'utf8').includes('pg_dump'), 'SQLite staging required PostgreSQL');
  // The independent PG cases below use another fresh staging directory.
  fs.renameSync(path.join(temp,'staging'),path.join(temp,'staging-sqlite-retained'));
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
