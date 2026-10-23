// Validate actual Compose models without starting a Docker daemon or service.
// Run: node scripts/test-storage-compose.js
// Optional portable executable: CORESCOPE_COMPOSE_BIN=/path/to/docker-compose
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawnSync } = require('node:child_process');
const root = path.resolve(__dirname, '..');
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-storage-config-'));
const candidates = process.env.CORESCOPE_COMPOSE_BIN
  ? [[process.env.CORESCOPE_COMPOSE_BIN, []]]
  : [['docker', ['compose']], ['docker-compose', []]];
const executable = candidates.find(([bin, args]) => spawnSync(bin, [...args, 'version'], { encoding: 'utf8', timeout: 10000 }).status === 0);
assert(executable, 'Docker Compose CLI required for model validation (no daemon needed); set CORESCOPE_COMPOSE_BIN for a portable executable');
const [bin, prefix] = executable;
const passwordKeys = ['POSTGRES_ADMIN_PASSWORD', 'CORESCOPE_OWNER_PASSWORD', 'CORESCOPE_READER_PASSWORD', 'CORESCOPE_WRITER_PASSWORD', 'CORESCOPE_ACCOUNTS_PASSWORD', 'CORESCOPE_CHANNELS_PASSWORD'];
const clean = { ...process.env };
for (const key of Object.keys(clean)) if (/^(PG|POSTGRES_|CORESCOPE_)/.test(key)) delete clean[key];
Object.assign(clean, {
  DATA_DIR: path.join(temp, 'example-state'),
  PROD_DATA_DIR: path.join(temp, 'prod-state'),
  STAGING_DATA_DIR: path.join(temp, 'staging-state'),
  POSTGRES_DATA_DIR: path.join(temp, 'example-postgres'),
  PROD_POSTGRES_DATA_DIR: path.join(temp, 'prod-postgres'),
  STAGING_POSTGRES_DATA_DIR: path.join(temp, 'staging-postgres'),
});
const credentials = Object.fromEntries(passwordKeys.map(key => [key, crypto.randomBytes(24).toString('hex')]));
const emptyEnv = path.join(temp, 'empty.env');
fs.writeFileSync(emptyEnv, '');
const cases = [
  ['docker-compose.yml', 'prod', 'docker-compose.postgres.yml', 'corescope', clean.PROD_POSTGRES_DATA_DIR],
  ['docker-compose.no-mosquitto.yml', 'prod', 'docker-compose.postgres.yml', 'corescope', clean.PROD_POSTGRES_DATA_DIR],
  ['docker-compose.example.yml', 'corescope', 'docker-compose.example.postgres.yml', 'corescope', clean.POSTGRES_DATA_DIR],
  ['docker-compose.staging.yml', 'staging-go', 'docker-compose.staging.postgres.yml', 'corescope-staging', clean.STAGING_POSTGRES_DATA_DIR],
  ['docker-compose.staging.no-mosquitto.yml', 'staging-go', 'docker-compose.staging.postgres.yml', 'corescope-staging', clean.STAGING_POSTGRES_DATA_DIR],
];
function model(files, env) {
  return spawnSync(bin, [...prefix, '--env-file', emptyEnv, ...files.flatMap(file => ['-f', path.join(root, file)]), 'config', '--format', 'json'], { cwd: root, env, encoding: 'utf8', timeout: 20000 });
}
function parsed(result, label) {
  // Deliberately never print a rendered model: it contains generated passwords.
  assert.equal(result.status, 0, `${label} failed Compose validation: ${result.stderr || result.error?.message}`);
  return JSON.parse(result.stdout);
}
try {
  for (const [base, appName, override, project, postgresPath] of cases) {
    const sqlite = parsed(model([base], clean), `${base} with all PostgreSQL variables unset`);
    assert.equal(sqlite.name, project);
    assert.deepEqual(Object.keys(sqlite.services), [appName], `${base} started an optional service by default`);
    const app = sqlite.services[appName];
    assert.equal(app.environment.CORESCOPE_DB_BACKEND, 'sqlite');
    assert.equal(app.environment.CORESCOPE_STATE_DIR, '/app/data');
    assert(!app.depends_on?.postgres && !app.depends_on?.bootstrap, 'SQLite app depends on PostgreSQL');
    assert(!Object.keys(app.environment).some(key => /DATABASE_URL|PASSWORD/.test(key)), 'default app needs PostgreSQL credentials');
    const missing = model([base, override], clean);
    assert.notEqual(missing.status, 0, `${override} accepted missing required credentials`);
    const pg = parsed(model([base, override], { ...clean, ...credentials }), `${base} + ${override}`);
    assert.equal(pg.name, project);
    assert.deepEqual(Object.keys(pg.services).sort(), [appName, 'bootstrap', 'postgres'].sort());
    assert(!pg.services.postgres.ports?.length, 'database port exposed on host');
    assert.equal(pg.services.postgres.image, 'postgres:18.6-alpine3.24');
    const data = pg.services.postgres.volumes.find(volume => volume.target === '/var/lib/postgresql');
    assert.equal(path.resolve(data.source), path.resolve(postgresPath), 'wrong environment persistent database directory');
    const init = pg.services.postgres.volumes.find(volume => volume.target === '/docker-entrypoint-initdb.d/10-corescope.sh');
    assert.equal(path.resolve(init.source), path.resolve(root, 'docker/postgres-init.sh'), 'extends resolved the init script outside this checkout');
    assert(init.read_only);
    const runtime = pg.services[appName];
    assert.equal(runtime.environment.CORESCOPE_DB_BACKEND, 'postgres');
    for (const key of passwordKeys) assert(!(key in runtime.environment), 'owner/admin credentials reached runtime');
    const roles = { CORESCOPE_READER_DATABASE_URL: ['corescope_reader','corescope_telemetry'], CORESCOPE_WRITER_DATABASE_URL: ['corescope_writer','corescope_telemetry'], CORESCOPE_USERS_DATABASE_URL: ['corescope_accounts','corescope_accounts'], CORESCOPE_APPROVED_CHANNELS_DATABASE_URL: ['corescope_channels','corescope_accounts'] };
    for (const [key, [user, db]] of Object.entries(roles)) {
      const target = new URL(runtime.environment[key]);
      assert.equal(target.username, user); assert.equal(target.hostname, 'postgres'); assert.equal(target.port, '5432', 'managed role endpoint must be explicitly pinned'); assert.equal(target.searchParams.get('search_path'), 'public', 'managed schema must be pinned'); assert.equal(target.pathname, '/' + db);
      assert(![credentials.POSTGRES_ADMIN_PASSWORD, credentials.CORESCOPE_OWNER_PASSWORD].includes(target.password), 'owner credential in runtime URL');
    }
    assert.equal(runtime.depends_on.bootstrap.condition, 'service_completed_successfully');
    assert.equal(pg.services.bootstrap.depends_on.postgres.condition, 'service_healthy');
    assert.equal(pg.services.bootstrap.environment.CORESCOPE_STATE_DIR, runtime.environment.CORESCOPE_STATE_DIR);
    const runtimeState = runtime.volumes.find(volume => volume.target === '/app/data');
    const setupState = pg.services.bootstrap.volumes.find(volume => volume.target === '/app/data');
    assert.equal(setupState.source, runtimeState.source); assert(!setupState.read_only, 'bootstrap cannot atomically commit shared selection');
    if (base.includes('no-mosquitto')) assert.equal(runtime.environment.DISABLE_MOSQUITTO, 'true');
    if (base === 'docker-compose.staging.yml') assert(pg.networks['meshcore-net'].external, 'staging lost its existing broker network');
    console.log(`PASS ${base}: SQLite default and explicit PostgreSQL override`);
  }
  const custom = parsed(model(['docker-compose.example.yml','docker-compose.example.postgres.yml'], { ...clean, ...credentials, CORESCOPE_STATE_DIR: '/app/data/custom-state' }), 'custom shared state directory');
  for (const service of ['corescope','bootstrap']) assert.equal(custom.services[service].environment.CORESCOPE_STATE_DIR, '/app/data/custom-state');
} finally {
  assert(path.resolve(temp).startsWith(path.resolve(os.tmpdir()) + path.sep) && path.basename(temp).startsWith('corescope-storage-config-'));
  fs.rmSync(temp, { recursive: true });
}
