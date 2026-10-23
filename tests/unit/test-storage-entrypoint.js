// Run the packaged startup guard against a fake migration/process boundary.
// No Docker daemon, host state, database, or credentials are used.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const root = path.resolve(__dirname, '../..');
const bash = require('../../scripts/bash-path')();
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-storage-entry-'));
const app = path.join(temp, 'app');
const posix = value => value.replace(/\\/g, '/').replace(/^([A-Za-z]):/, (_, drive) => '/' + drive.toLowerCase());
const shellQuote = value => "'" + value.replace(/'/g, "'\\''") + "'";
const calls = path.join(temp, 'calls');
fs.mkdirSync(path.join(app, 'data'), { recursive: true });
fs.mkdirSync(path.join(temp, 'bin'));
fs.writeFileSync(path.join(app, 'data/config.json'), '{}');
fs.writeFileSync(path.join(temp, 'mountinfo'), `1 0 8:1 / ${posix(app)}/data rw - ext4 /dev/owned rw\n`);
const tool = path.join(temp, 'tools.js');
fs.writeFileSync(tool, `
const fs=require('fs'),path=require('path');
const [name,...args]=process.argv.slice(2);
const calls=process.env.CALLS;
if(name!=='jq')fs.appendFileSync(calls,JSON.stringify([name,...args])+'\\n');
const field=flag=>args.find(v=>v.startsWith(flag+'='))?.slice(flag.length+1)||args[args.indexOf(flag)+1];
if(name==='corescope-migrate'){
 if(args.includes('-check-ready')){
  fs.appendFileSync(calls,JSON.stringify(['readiness-accounts',Boolean(process.env.CORESCOPE_USERS_DATABASE_URL)])+'\\n');
  const scenario=process.env.SCENARIO;
  const failure={
   'unsupported-schema':'unsupported telemetry schema version 99 (binary supports 3)',
   'missing-schema':'telemetry schema is not initialized',
   'corrupt-schema':'invalid telemetry schema layout'
  }[scenario];
  if(failure){process.stderr.write(failure+'\\n');process.exit(1)}
  if(['old-schema','failed-upgrade','failed-recheck'].includes(scenario)&&!fs.existsSync(calls+'.upgraded')){
   process.stderr.write('unsupported telemetry schema version 1 (binary supports 3)\\n');process.exit(1);
  }
  if(scenario==='failed-recheck'){process.stderr.write('telemetry schema verification failed after upgrade\\n');process.exit(1)}
  console.log('telemetry PostgreSQL schema is ready');
  process.exit(0);
 }
 const selection=field('-selection-file');const action=field('-storage-action');
 if(action==='status'){
  let report={version:1,state:'unrecorded',backend:'',generation:'',job_id:'',source_backend:'',target_backend:''};
  if(fs.existsSync(selection))report=JSON.parse(fs.readFileSync(selection,'utf8'));
  if(process.env.SCENARIO==='pending')report={...report,state:'pending',job_id:'0123456789abcdef0123456789abcdef'};
  if(process.env.SCENARIO==='corrupt'){console.log(JSON.stringify({...report,state:'corrupt'}));process.exit(1)};
  console.log(JSON.stringify(report));
 }else if(action==='setup'){
  fs.appendFileSync(calls,JSON.stringify(['setup-account',process.env.FIXTURE_RUN_AS||'root'])+'\\n');
  if(process.env.SCENARIO==='fail-setup')process.exit(1);
  const backend=field('-backend')||'sqlite';fs.mkdirSync(path.dirname(selection),{recursive:true});
  fs.writeFileSync(selection,JSON.stringify({version:1,state:'ready',backend,generation:'one',job_id:''}));console.log(JSON.stringify({verified:true}));
 }else if(action==='upgrade'){
  const report=JSON.parse(fs.readFileSync(selection,'utf8'));
  if(report.state!=='ready'||report.backend!=='postgres'||['unsupported-schema','missing-schema','corrupt-schema'].includes(process.env.SCENARIO)){
   process.stderr.write('recorded target cannot be upgraded\\n');process.exit(1);
  }
  process.stderr.write('[storage] PostgreSQL telemetry schema upgrade required: 1 -> 2\\n');
  if(process.env.SCENARIO==='failed-upgrade'){process.stderr.write('owner upgrade failed\\n');process.exit(1)}
  fs.appendFileSync(calls,JSON.stringify(['upgraded-stores',report.accounts?['telemetry','accounts']:['telemetry']])+'\\n');
  fs.writeFileSync(calls+'.upgraded','verified');
  process.stderr.write('[storage] upgraded recorded PostgreSQL telemetry schema 1 -> 2\\n');
 }else{process.stderr.write('unexpected migration action');process.exit(2)}
}else if(name==='su'){
 if(process.env.SCENARIO==='fail-user-switch')process.exit(1);
 const env={...process.env,FIXTURE_RUN_AS:args.at(-1)};
 // MSYS converts path-shaped environment values for the native Node stub.
 env.CORESCOPE_STATE_DIR=env.CORESCOPE_STATE_DIR.replace(/\\\\/g,'/').replace(/^([A-Za-z]):/,(_,drive)=>'/'+drive.toLowerCase());
 const result=require('child_process').spawnSync(process.env.FIXTURE_BASH,['--noprofile','--norc','-c',args[args.indexOf('-c')+1]],
  {env,stdio:'inherit'});
 process.exit(result.status??1);
}else if(name==='jq'){
 const value=JSON.parse(fs.readFileSync(0,'utf8'));const query=args.at(-1);
 if(query==='.accounts != null'){console.log(value.accounts!=null);process.exit(0)}
 if(query.includes('.telemetry.postgres ==')){
  const canonical=(target,database)=>target&&Object.keys(target).length===4&&target.host==='postgres'&&target.port===5432&&target.database===database&&target.schema==='public';
  const valid=value.state==='ready'&&value.backend==='postgres'&&canonical(value.telemetry?.postgres,'corescope_telemetry')&&
   ((query.includes('.accounts == null or')&&value.accounts===null)||canonical(value.accounts?.postgres,'corescope_accounts'));
  if(!valid)process.exit(1);console.log(true);process.exit(0);
 }
 const key=query.replace(/^\\./,'').replace(/ \\/\\/.*$/,'');
 if(!/^[a-z_.]+$/.test(key))throw Error('unhandled jq boundary query: '+query);
 let selected=key.split('.').reduce((obj,key)=>obj?.[key],value);
 if(selected==null||selected===''){process.exit(1)}else{console.log(selected)}
}else if(name==='psql'){
 fs.appendFileSync(calls,JSON.stringify(['grant-database',process.env.PGDATABASE])+'\\n');
}
`);
for (const name of ['corescope-migrate','jq','supervisord','psql','su']) {
  const file = path.join(name === 'corescope-migrate' ? app : path.join(temp, 'bin'), name);
  fs.writeFileSync(file, `#!/bin/sh\nexec node ${shellQuote(posix(tool))} ${name} "$@"\n`, { mode: 0o755 });
}
// Account resolution/.env trust has its own shell suite. Here the process
// boundary supplies the resolved identity and records who runs setup.
fs.writeFileSync(path.join(temp, 'select-service-user.sh'), 'prepare_service_user(){ CORESCOPE_SERVICE_USER=${TEST_SERVICE_USER:-root}; export CORESCOPE_SERVICE_USER; }\n');
fs.writeFileSync(path.join(temp, 'bin/id'), '#!/bin/sh\nprintf "1000\\n"\n', { mode: 0o755 });
function materialize() {
  let source = fs.readFileSync(path.join(root, 'docker/entrypoint-go.sh'), 'utf8');
  source = source.replaceAll('/app', posix(app)).replace('/usr/bin/supervisord', posix(path.join(temp, 'bin/supervisord')))
    .replaceAll('/select-service-user.sh', posix(path.join(temp, 'select-service-user.sh')));
  fs.writeFileSync(path.join(temp, 'entrypoint.sh'), source, { mode: 0o755 });
  const helper = path.join(root, 'docker/storage.sh');
  if (fs.existsSync(helper)) fs.writeFileSync(path.join(app, 'storage.sh'), fs.readFileSync(helper, 'utf8').replaceAll('/app', posix(app)).replace('/proc/self/mountinfo', posix(path.join(temp, 'mountinfo'))), { mode: 0o755 });
  fs.writeFileSync(path.join(app,'postgres-bootstrap.sh'),fs.readFileSync(path.join(root,'docker/postgres-bootstrap.sh'),'utf8').replaceAll('/app',posix(app)),{mode:0o755});
}
function run(extra = {}, action = [], bootstrap = false) {
  fs.writeFileSync(calls, '');
  fs.rmSync(calls + '.upgraded', { force: true });
  const env = { ...process.env, CALLS: calls, FIXTURE_BASH: bash, TEST_SERVICE_USER: 'root', CORESCOPE_STATE_DIR: posix(path.join(app, 'data/custom')), CORESCOPE_DB_BACKEND: 'sqlite', SCENARIO: '', ...extra };
  const inherited = env.PATH || env.Path || '';
  delete env.Path;
  env.PATH = path.join(temp, 'bin') + path.delimiter + inherited;
  // A run launches several native stub processes through the shell. Allow
  // Windows process-start overhead without changing any product deadline.
  const result = spawnSync(bash, ['--noprofile','--norc',posix(bootstrap ? path.join(app,'postgres-bootstrap.sh') : action.length ? path.join(app,'storage.sh') : path.join(temp,'entrypoint.sh')), ...action], { cwd: app, env, encoding: 'utf8', timeout: 30000 });
  result.calls = fs.readFileSync(calls,'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);
  return result;
}
try {
  materialize();
  const fresh = run();
  assert.equal(fresh.status,0,fresh.stderr || fresh.error?.message);
  assert(fresh.calls.some(call => call.includes('setup') || call.includes('-storage-action=setup')), 'default Docker startup did not invoke guarded fresh/legacy setup');
  const last = fresh.calls.at(-1); assert.equal(last?.[0], 'supervisord', 'supervisor started before setup completed');
  const setup = fresh.calls.find(call => call.includes('setup') || call.includes('-storage-action=setup'));
  assert(setup.includes('-offline'), 'guarded setup must be offline, before child processes');
  assert(setup.some(arg => arg.includes('/data/custom/storage-selection.json')), 'custom persistent selection path was ignored');
  assert.deepEqual(fresh.calls.find(call => call[0] === 'setup-account'), ['setup-account', 'root']);
  assert(!fresh.calls.some(call => call[0] === 'su'), 'default root startup switched users');
  const unprivileged = run({ TEST_SERVICE_USER: 'corescope', CORESCOPE_STATE_DIR: posix(path.join(app, 'data/unprivileged')) });
  assert.equal(unprivileged.status, 0, unprivileged.stderr || unprivileged.error?.message);
  assert.deepEqual(unprivileged.calls.find(call => call[0] === 'setup-account'), ['setup-account', 'corescope'],
    'fresh SQLite files must be created by the service account after ownership hand-over');
  assert.equal(unprivileged.calls.at(-1)?.[0], 'supervisord');
  const switchFailure = run({ TEST_SERVICE_USER: 'corescope', SCENARIO: 'fail-user-switch' });
  assert.notEqual(switchFailure.status, 0, 'failed service-account switch started the server');
  assert(!switchFailure.calls.some(call => call[0] === 'supervisord' || call[0] === 'corescope-migrate'));
  const recreated = run({ CORESCOPE_DB_BACKEND: 'postgres' });
  assert.equal(recreated.status,0,recreated.stderr);
  assert(!recreated.calls.some(call => call.includes('setup') || call.includes('-storage-action=setup')), 'recreate interpreted a stale bootstrap backend as new setup');
  assert.equal(recreated.calls.at(-1)?.[0],'supervisord');
  for (const scenario of ['pending','corrupt']) {
    const blocked = run({ SCENARIO: scenario });
    assert.notEqual(blocked.status,0,scenario+' selection started');
    assert(!blocked.calls.some(call => call[0]==='supervisord'),scenario+' selection launched application');
  }
  const corruptField = run({ SCENARIO: 'corrupt' }, ['field','backend']);
  assert.notEqual(corruptField.status,0,'field reader hid the CLI corrupt-status failure');
  const failed = run({ SCENARIO: 'fail-setup', CORESCOPE_STATE_DIR: posix(path.join(app,'data/new')) });
  assert.notEqual(failed.status,0,'failed setup started the server');
  assert(!failed.calls.some(call=>call[0]==='supervisord'));
  const ephemeral = run({ CORESCOPE_STATE_DIR: posix(path.join(temp,'ephemeral')) });
  assert.notEqual(ephemeral.status,0,'ephemeral selection directory accepted');
  assert.equal(ephemeral.calls.length,0,'unsupported path reached migration/runtime before rejection');
  for (const file of fs.readdirSync(path.join(root,'docker')).filter(name => /^supervisord-go.*[.]conf$/.test(name))) {
    const config = fs.readFileSync(path.join(root,'docker',file),'utf8');
    assert(!/corescope-server[^\n]*-state-dir/.test(config), file+' overrides shared selection directory');
  }
  const selectionFile=path.join(app,'data/custom/storage-selection.json');
  const target=database=>({postgres:{host:'postgres',port:5432,database,schema:'public'}});
  const pgReport={version:1,state:'ready',backend:'postgres',generation:'one',telemetry:target('corescope_telemetry'),accounts:null};
  for(const accounts of [null,target('corescope_accounts')]){
    fs.writeFileSync(selectionFile,JSON.stringify({...pgReport,accounts}));
    const guard=run({},['managed-postgres']);
    assert.equal(guard.status,0,'canonical PostgreSQL with optional accounts was rejected: '+guard.stderr);
    const started=run({CORESCOPE_OWNER_PASSWORD:'synthetic-owner',CORESCOPE_USERS_OWNER_DATABASE_URL:accounts?'synthetic-owner-url':'',CORESCOPE_USERS_DATABASE_URL:'runtime-account-url'},[],true);
    assert.equal(started.status,0,started.stderr||started.error?.message);
    assert.deepEqual(started.calls.filter(call=>call[0]==='grant-database').map(call=>call[1]),accounts?['corescope_telemetry','corescope_accounts']:['corescope_telemetry']);
    assert.deepEqual(started.calls.find(call=>call[0]==='readiness-accounts'),['readiness-accounts',accounts!==null]);
    assert(!started.calls.some(call=>call.includes('-storage-action=setup')),'ready PostgreSQL was implicitly initialized');
    assert(!started.calls.some(call=>call.includes('-storage-action=upgrade')),'current schema was unnecessarily upgraded');
    assert.match(started.stdout,/telemetry PostgreSQL schema is ready/,'current readiness output was hidden');
    const originalSelection=fs.readFileSync(selectionFile,'utf8');
    const upgraded=run({SCENARIO:'old-schema',CORESCOPE_OWNER_PASSWORD:'synthetic-owner',CORESCOPE_USERS_OWNER_DATABASE_URL:'synthetic-owner-url',CORESCOPE_USERS_DATABASE_URL:'runtime-account-url'},[],true);
    assert.equal(upgraded.status,0,upgraded.stderr||upgraded.error?.message);
    assert.match(upgraded.stderr,/upgrade required: 1 -> 2/,'supported upgrade did not report stored and current versions');
    assert(!upgraded.stderr.includes('unsupported telemetry'),'successful supported upgrade emitted a provisional failure');
    const upgrade=upgraded.calls.filter(call=>call.includes('-storage-action=upgrade'));
    assert.equal(upgrade.length,1,'old recorded schema did not invoke exactly one owner upgrade');
    assert(upgrade[0].includes('-offline'),'schema upgrade was not explicitly offline');
    assert(upgrade[0].includes('-selection-file')&&upgrade[0].some(arg=>arg.endsWith('/data/custom/storage-selection.json')),'upgrade lost its recorded selection');
    assert.deepEqual(upgraded.calls.find(call=>call[0]==='upgraded-stores'),['upgraded-stores',accounts?['telemetry','accounts']:['telemetry']]);
    assert.deepEqual(upgraded.calls.filter(call=>call[0]==='readiness-accounts'),[['readiness-accounts',accounts!==null],['readiness-accounts',accounts!==null]],'upgrade must recheck precisely the selected stores');
    const finalReady=upgraded.calls.map(call=>call[0]).lastIndexOf('readiness-accounts');
    assert(upgraded.calls.findIndex(call=>call[0]==='psql')>finalReady,'runtime grants preceded upgrade verification');
    assert(!upgraded.calls.some(call=>call.includes('-storage-action=setup')),'recorded upgrade could initialize a replacement or absent account target');
    assert.equal(fs.readFileSync(selectionFile,'utf8'),originalSelection,'upgrade changed the selection');
    for(const [scenario,diagnostic] of [
      ['unsupported-schema','unsupported telemetry schema version 99'],
      ['missing-schema','telemetry schema is not initialized'],
      ['corrupt-schema','invalid telemetry schema layout'],
      ['failed-upgrade','owner upgrade failed'],
      ['failed-recheck','telemetry schema verification failed after upgrade']
    ]){
      const unsupported=run({SCENARIO:scenario,CORESCOPE_OWNER_PASSWORD:'synthetic-owner',CORESCOPE_USERS_OWNER_DATABASE_URL:'synthetic-owner-url'},[],true);
      assert.notEqual(unsupported.status,0,scenario+' passed bootstrap');
      assert(!unsupported.calls.some(call=>call[0]==='psql'),scenario+' reached grants');
      assert.match(unsupported.stderr,new RegExp(diagnostic),scenario+' diagnostic was hidden');
      assert.equal(fs.readFileSync(selectionFile,'utf8'),originalSelection,'failed upgrade changed the selection');
    }
  }
  for(const report of [{...pgReport,telemetry:target('unselected_telemetry')},{...pgReport,accounts:target('unselected_accounts')},{...pgReport,accounts:{sqlite_path:'/app/data/users.db'}}]){
    fs.writeFileSync(selectionFile,JSON.stringify(report));
    const rejected=run({CORESCOPE_OWNER_PASSWORD:'synthetic-owner'},[],true);
    assert.notEqual(rejected.status,0,'custom/non-PostgreSQL target passed the managed guard');
    assert(!rejected.calls.some(call=>call[0]==='psql'||call.includes('-check-ready')),'rejected target reached database operations');
  }
  console.log('PASS packaged SQLite setup, recorded choice on recreate, blocked journals/errors and persistent custom state');
} finally {
  assert(path.resolve(temp).startsWith(path.resolve(os.tmpdir())+path.sep)&&path.basename(temp).startsWith('corescope-storage-entry-'));
  fs.rmSync(temp,{recursive:true});
}
