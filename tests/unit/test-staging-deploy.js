// Execute the workflow's actual deployment shell with only Docker/sleep replaced.
// All state is synthetic; no daemon, application or database is contacted.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.resolve(__dirname, '../..');
const workflow = fs.readFileSync(path.join(root, '.github/workflows/deploy.yml'), 'utf8').replace(/\r/g, '');
const step = workflow.match(/^      - name: Deploy staging\n        run: \|\n([\s\S]*?)(?=^      - name:)/m);
assert(step, 'staging deployment shell is missing');
const deploy = step[1].replace(/^          /gm, '');
const base = fs.readFileSync(path.join(root, 'docker-compose.staging.yml'), 'utf8');
const override = fs.readFileSync(path.join(root, 'docker-compose.staging.postgres.yml'), 'utf8');
const defaultBackend = base.match(/CORESCOPE_DB_BACKEND=(\w+)/)?.[1];
const overrideBackend = override.match(/CORESCOPE_DB_BACKEND: (\w+)/)?.[1];
assert.equal(defaultBackend, 'sqlite');
assert.equal(overrideBackend, 'postgres');
assert.match(override, /bootstrap:\s*\n\s+condition: service_completed_successfully/);
const runtimeURLs = [...override.matchAll(/^\s+(CORESCOPE_\w+_DATABASE_URL): postgresql:/gm)].map(match => match[1]);
assert.equal(runtimeURLs.length, 4, 'documented override no longer supplies all runtime roles');
const bash = require('../../scripts/bash-path')();
const posix = value => value.replace(/\\/g, '/').replace(/^([A-Za-z]):/, (_, drive) => '/' + drive.toLowerCase());
const quote = value => "'" + value.replace(/'/g, "'\\''") + "'";
const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-staging-deploy-'));
const boundary = path.join(temp, 'docker.js');
fs.writeFileSync(boundary, `
const fs = require('node:fs');
const args = process.argv.slice(2);
const model = JSON.parse(fs.readFileSync(process.env.MODEL, 'utf8'));
fs.appendFileSync(process.env.CALLS, JSON.stringify(args) + '\\n');
function save() { fs.writeFileSync(process.env.MODEL, JSON.stringify(model)); }
function field(selection, name) {
  if (selection.unreadable || !selection[name]) process.exit(1);
  process.stdout.write(selection[name] + '\\n');
  if (selection.fieldFails === name) process.exit(1);
}
if (args[0] === 'ps') {
  if (model.listFails) process.exit(1);
  const filter = args.indexOf('--filter');
  if (filter >= 0 && args[filter + 1] !== 'name=^/corescope-staging-go$') throw Error('wrong container filter');
  if (model.exists) console.log('corescope-staging-go');
} else if (args[0] === 'exec') {
  if (args[1] !== 'corescope-staging-go' || !model.exists || !model.running) process.exit(1);
  if (args[2] !== '/app/storage.sh' || args[3] !== 'field') throw Error('unexpected container command');
  field(model.current, args[4]);
} else if (args[0] === 'stop' || args[0] === 'rm') {
  if (!args.includes('corescope-staging-go')) throw Error('wrong application target');
  model.running = false;
  if (args[0] === 'rm') model.exists = false;
  save();
} else if (args[0] === 'compose') {
  if (args[1] !== '-f' || args[2] !== 'docker-compose.staging.yml' || args[3] !== '-p' || args[4] !== 'corescope-staging') throw Error('wrong Compose scope');
  const command = args[5];
  if (command === 'run') {
    if (args.slice(6, 12).join(' ') !== '--rm --no-deps --entrypoint /app/storage.sh staging-go field') throw Error('discovery started application or dependencies');
    field(model.candidate, args[12]);
  } else if (command === 'down') {
    model.exists = model.running = false; save();
  } else if (command === 'up') {
    if (args.slice(6).join(' ') !== '-d staging-go') throw Error('wrong startup target');
    model.exists = true;
    // The base model omits PostgreSQL URLs/bootstrap. Recorded PG must fail,
    // even though its stale bootstrap hint now says SQLite.
    model.runtimeURLs = [];
    model.running = model.candidate.state === 'ready' && model.candidate.backend === model.baseBackend;
    save();
    if (!model.running) process.exit(1);
  } else throw Error('unexpected Compose operation');
} else throw Error('unexpected Docker operation');
`);

const selected = (backend = 'sqlite', changes = {}) => ({
  state: 'ready', backend, generation: '0123456789abcdef0123456789abcdef', ...changes,
});
function run(label, changes = {}) {
  const dir = fs.mkdtempSync(path.join(temp, 'case-'));
  const modelPath = path.join(dir, 'model.json');
  const callsPath = path.join(dir, 'calls.jsonl');
  const data = path.join(dir, 'data');
  fs.mkdirSync(data);
  fs.writeFileSync(path.join(data, 'config.json'), '{"fixture":true}\n');
  const model = { exists: true, running: true, current: selected(), candidate: selected(),
    baseBackend: defaultBackend, runtimeURLs: [], ...changes };
  fs.writeFileSync(modelPath, JSON.stringify(model));
  fs.writeFileSync(callsPath, '');
  const result = spawnSync(bash, ['--noprofile', '--norc'], {
    input: `set -e\ndocker(){ MSYS_NO_PATHCONV=1 node ${quote(boundary.replace(/\\/g, '/'))} "$@"; }\nsleep(){ :; }\n${deploy}`,
    cwd: dir, encoding: 'utf8', timeout: 20000,
    env: { ...process.env, MODEL: modelPath, CALLS: callsPath, STAGING_DATA_DIR: posix(data),
      STAGING_COMPOSE_FILE: 'docker-compose.staging.yml', STAGING_CONTAINER: 'corescope-staging-go', STAGING_SERVICE: 'staging-go' },
  });
  assert(!result.error, `${label}: shell did not finish: ${result.error?.message}`);
  assert.equal(fs.readFileSync(path.join(data, 'config.json'), 'utf8'), '{"fixture":true}\n', `${label}: configuration changed`);
  return { ...result, model: JSON.parse(fs.readFileSync(modelPath, 'utf8')),
    calls: fs.readFileSync(callsPath, 'utf8').trim().split('\n').filter(Boolean).map(line => JSON.parse(line)) };
}

try {
  for (const exists of [true, false]) {
    const sqlite = run('recorded default SQLite', { exists, running: exists });
    assert.equal(sqlite.status, 0, sqlite.stderr);
    assert(sqlite.model.running, 'recorded SQLite application did not start');
    assert(sqlite.calls.some(args => args[0] === 'compose' && args[5] === 'up'), 'SQLite never reached deployment');
    assert.deepEqual(sqlite.model.candidate, selected(), 'SQLite selection changed during update');
  }
  const cases = [
    ['documented PostgreSQL override with environment runtime roles', {
      current: selected(overrideBackend), candidate: selected(overrideBackend), runtimeURLs,
    }],
    ['recorded PostgreSQL despite a stale SQLite bootstrap hint', { candidate: selected('postgres') }],
    ['active PostgreSQL custom state mount while base points to SQLite', {
      current: selected(overrideBackend, { generation: 'fedcba9876543210fedcba9876543210', state_dir: '/app/data/custom-state' }), runtimeURLs,
    }],
    ['different active SQLite selection', { current: selected('sqlite', { generation: 'fedcba9876543210fedcba9876543210' }) }],
    ['unreadable active selection', { current: selected('sqlite', { unreadable: true }) }],
    ['unreadable candidate selection', { candidate: selected('sqlite', { unreadable: true }) }],
    ['failed candidate read after partial output', { candidate: selected('sqlite', { fieldFails: 'state' }) }],
    ['malformed candidate state', { candidate: selected('sqlite', { state: 'corrupt' }) }],
    ['pending candidate conversion', { candidate: selected('sqlite', { state: 'pending' }) }],
    ['unknown candidate backend', { candidate: selected('unknown') }],
    ['unrecorded first setup', { exists: false, running: false, candidate: selected('', { state: 'unrecorded' }) }],
    ['legacy unrecorded active installation', { current: selected('', { state: 'unrecorded' }) }],
    ['Docker inspection failure', { listFails: true }],
    ['uninspectable stopped application', { running: false }],
  ];
  for (const [label, changes] of cases) {
    const result = run(label, changes);
    const mutations = result.calls.filter(args => ['stop', 'rm'].includes(args[0]) ||
      (args[0] === 'compose' && ['down', 'up'].includes(args[5])));
    assert.deepEqual(mutations, [], `${label}: refused deployment must leave the application intact`);
    assert.notEqual(result.status, 0, `${label}: unsafe deployment succeeded`);
    assert.equal(result.model.running, changes.running ?? true, `${label}: application availability changed`);
    assert.deepEqual(result.model.runtimeURLs, changes.runtimeURLs ?? [], `${label}: runtime configuration was lost`);
  }
  console.log('PASS staging workflow: recorded SQLite deploys; PostgreSQL and unverified/mismatched state preserve the application');
} finally {
  assert(path.resolve(temp).startsWith(path.resolve(os.tmpdir()) + path.sep) && path.basename(temp).startsWith('corescope-staging-deploy-'));
  fs.rmSync(temp, { recursive: true });
}
