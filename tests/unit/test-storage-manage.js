// Exercise the actual management shell against a fake Compose/process boundary.
const assert=require('node:assert/strict');
const fs=require('node:fs'),os=require('node:os'),path=require('node:path');
const {spawnSync}=require('node:child_process');
const root=path.resolve(__dirname,'../..');
const source=fs.readFileSync(path.join(root,'manage.sh'),'utf8').replace(/\r/g,'');
const bash=require('../../scripts/bash-path')();
const temp=fs.mkdtempSync(path.join(os.tmpdir(),'corescope-storage-manage-'));
const quote=value=>"'"+value.replace(/'/g,"'\\''")+"'";
const posix=value=>value.replace(/\\/g,'/');
const names=['is_true','dc_prod_base','dc_staging_base','dc_postgres','storage_at','storage_action','storage_field','selected_backend','require_managed_postgres','choose_storage','dc_prod','dc_staging','cmd_start','cmd_update','cmd_stop','cmd_storage'];
const functions=names.map(name=>source.match(new RegExp(`^${name}\\(\\)\\s*\\{[\\s\\S]*?^}`,'m'))?.[0]||'').join('\n');
function run(command,backend='sqlite',state='ready',input=''){
 const log=path.join(temp,'calls');fs.writeFileSync(log,'');
 const script=`
set -e
DC=fake_compose
PROD_DATA=${quote(posix(path.join(temp,'prod')))}
STAGING_DATA=${quote(posix(path.join(temp,'staging')))}
STAGING_COMPOSE_FILE=docker-compose.staging.yml
STAGING_CONTAINER=corescope-staging-go
RECORD_BACKEND=${quote(backend)}
RECORD_STATE=${quote(state)}
CORESCOPE_DB_BACKEND=sqlite
CALLS=${quote(posix(log))}
log(){ :; }; info(){ :; }; warn(){ :; }; err(){ printf '%s\\n' "$*" >&2; }
confirm(){ return 0; }
migrate_config(){ :; }; ensure_config(){ :; }; preflight_validate_prod_ports(){ :; }
write_private_env(){ export "$1=$2"; };
prepare_database_credentials(){ printf 'credentials prepared\\n' >> "$CALLS"; };
require_database_credentials(){ [ "$RECORD_BACKEND" = postgres ] || [ "\${STORAGE_OPERATION:-}" = switch ] || { err 'PostgreSQL credentials are not configured'; return 1; }; }
container_running(){ return 1; }
prepare_staging_db(){ :; };prepare_staging_config(){ :; }
docker(){ if [ "$1" = ps ];then return 0;fi;printf 'docker %s\\n' "$*" >> "$CALLS"; }
git(){ case "$1" in describe) echo v1.0.0;;rev-parse) echo fixture;;*) printf 'git %s\\n' "$*" >> "$CALLS";;esac; }
fake_compose(){
 case "$*" in
  *'/app/storage.sh prod field state'|*'/app/storage.sh staging-go field state') printf '%s\\n' "$RECORD_STATE";;
  *'/app/storage.sh prod field backend'|*'/app/storage.sh staging-go field backend') printf '%s\\n' "$RECORD_BACKEND";;
  *'/app/storage.sh prod field has_accounts') printf 'false\\n';;
  *'/app/storage.sh prod field state_dir') printf '/app/data\\n';;
  *'/app/storage.sh prod field source_backend'|*'/app/storage.sh prod field target_backend') printf '%s\\n' "$RECORD_BACKEND";;
  *'/app/storage.sh prod field job_id'|*'/app/storage.sh staging-go field job_id') printf '0123456789abcdef0123456789abcdef\\n';;
  *) printf 'compose %s\\n' "$*" >> "$CALLS";;
 esac
}
${functions}
${input ? `printf %s ${quote(input)} | ${command}` : command}
`;
 const result=spawnSync(bash,['--noprofile','--norc'],{input:script,cwd:temp,encoding:'utf8',timeout:20000});
 result.calls=fs.readFileSync(log,'utf8');return result;
}
try{
 const sqlite=run("cmd_start ''");
 assert.equal(sqlite.status,0,sqlite.stderr||sqlite.error?.message);
 assert(sqlite.calls.includes('up -d prod'),'SQLite app never started');
 assert(!sqlite.calls.includes('.postgres.yml'),'SQLite default selected a PostgreSQL overlay');
 const pg=run("cmd_start ''",'postgres');
 assert.equal(pg.status,0,pg.stderr);
 assert(pg.calls.includes('-f docker-compose.postgres.yml'),'recorded PostgreSQL was replaced by stale SQLite bootstrap hint');
 const pending=run("cmd_start ''",'postgres','pending');
 assert.notEqual(pending.status,0,'pending conversion started');
 assert(!pending.calls.includes('up -d'),'pending conversion reached application startup');
 const update=run('cmd_update latest','postgres');
 assert.equal(update.status,0,update.stderr);
 assert(update.calls.includes('build prod'),'update did not build its source');
 assert(update.calls.includes('docker-compose.postgres.yml'),'update discarded recorded backend');
 assert(!/storage.sh (switch|init)/.test(update.calls),'ordinary update changed backend');
 const defaultChoice=run('choose_storage','sqlite','unrecorded','\n');
 assert.equal(defaultChoice.status,0,defaultChoice.stderr);
 assert(!defaultChoice.calls.includes('credentials prepared'),'default setup prepared PostgreSQL credentials');
 const postgresChoice=run('choose_storage','postgres','unrecorded','2\n');
 assert.equal(postgresChoice.status,0,postgresChoice.stderr);
 assert(postgresChoice.calls.includes('credentials prepared'),'explicit PostgreSQL setup did not prepare its credentials');
 const keep=run('choose_storage','postgres','ready','1\n');
 assert.equal(keep.status,0,keep.stderr);
 assert(!keep.calls.includes('credentials prepared'),'setup reconfigured credentials for an existing recorded backend');
 const switched=run('cmd_storage switch postgres','sqlite');
 assert.equal(switched.status,0,switched.stderr);
 assert(switched.calls.includes('stop prod'),'conversion did not stop owned app processes');
 assert(switched.calls.includes('up -d --wait postgres'),'conversion did not prepare only the database service');
 assert(switched.calls.includes('bootstrap switch -backend=postgres'),'conversion bypassed verified CLI action');
 assert(!switched.calls.includes('up -d bootstrap'),'conversion pre-initialized its target schema');
 assert(!switched.calls.includes('up -d prod'),'conversion automatically restarted writers before operator validation');
 const reversed=run('cmd_storage switch sqlite','postgres');
 assert.equal(reversed.status,0,reversed.stderr);
 assert(reversed.calls.includes('prod managed-postgres'),'reverse conversion skipped the recorded target guard');
 assert(reversed.calls.includes('bootstrap switch -backend=sqlite'),'reverse conversion bypassed the verified CLI');
 assert(!reversed.calls.includes('corescope_accounts'),'reverse routing touched the unselected account database');
 assert(reversed.calls.includes('stop postgres'),'completed reverse conversion left the managed PG service running');
 const sqliteResume=run('cmd_storage resume 0123456789abcdef0123456789abcdef','sqlite','pending');
 assert.equal(sqliteResume.status,0,sqliteResume.stderr);
 assert(sqliteResume.calls.includes('prod resume -job-id='),'same-backend SQLite recovery bypassed the CLI');
 assert(!sqliteResume.calls.includes('.postgres.yml')&&!sqliteResume.calls.includes('up -d --wait postgres'),'SQLite-only recovery required PostgreSQL');
 const pgAbort=run('require_database_credentials(){ return 1; }; cmd_storage abort 0123456789abcdef0123456789abcdef','postgres','pending');
 assert.equal(pgAbort.status,0,pgAbort.stderr);
 assert(pgAbort.calls.includes('prod abort -job-id='),'abort bypassed metadata-only CLI');
 assert(!pgAbort.calls.includes('.postgres.yml')&&!pgAbort.calls.includes('up -d --wait postgres'),'metadata abort started PostgreSQL');
 const stop=run('cmd_stop prod','postgres','pending');
 assert.equal(stop.status,0,stop.stderr);
 assert(stop.calls.includes('stop prod'),'operator cannot stop app while recovery journal exists');
 console.log('PASS SQLite default start, authoritative PG routing/update, pending start refusal and stop during recovery');
}finally{assert(path.resolve(temp).startsWith(path.resolve(os.tmpdir())+path.sep)&&path.basename(temp).startsWith('corescope-storage-manage-'));fs.rmSync(temp,{recursive:true});}
