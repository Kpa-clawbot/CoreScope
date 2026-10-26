// #2229: run the complete management script with only external commands
// replaced. No Docker daemon, network access or real checkout is involved.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.resolve(__dirname, '../..');
const bash = process.env.BASH_PATH || 'bash';
const quote = value => "'" + value.replace(/'/g, "'\\''") + "'";
const posix = value => value.replace(/\\/g, '/');
let passed = 0, failed = 0;
function test(name, fn) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-manage-test-'));
  const run = (before, after) => spawnSync(bash, ['--noprofile', '--norc'], {
    input: `set -e\n${before}\nsource ${quote(posix(path.join(root, 'manage.sh')))} help >/dev/null\n${after}\n`,
    cwd: dir,
    env: { ...process.env, PROD_DATA_DIR: posix(path.join(dir, 'data')), STAGING_DATA_DIR: posix(path.join(dir, 'staging')), PROD_HTTP_PORT: '', DISABLE_MOSQUITTO: '' },
    encoding: 'utf8', timeout: 20000,
  });
  try { fn(dir, run); passed++; console.log('PASS ' + name); }
  catch (e) { failed++; console.error('FAIL ' + name + ': ' + e.message); }
  finally {
    assert.equal(path.dirname(path.resolve(dir)), path.resolve(os.tmpdir()));
    assert.ok(path.basename(dir).startsWith('corescope-manage-test-'));
    fs.rmSync(dir, { recursive: true });
  }
}
const ok = result => assert.equal(result.status, 0, result.error?.message || result.stderr || result.stdout);

for (const endings of ['\r\n', '\n', null]) {
  test('bootstrap with ' + (endings === null ? 'no .env' : JSON.stringify(endings)), (dir, run) => {
    if (endings !== null) fs.writeFileSync(path.join(dir, '.env'), ['# Test configuration', 'PROD_HTTP_PORT=18080', 'DISABLE_MOSQUITTO=true', ''].join(endings));
    // -U makes Git Bash grep preserve CR bytes, as GNU grep does on Linux.
    const before = `docker(){ [ "$*" = 'compose version' ]; }
git(){ printf 'fixture-revision\\n'; }
grep(){ command grep -U "$@"; }`;
    for (let i = 0; i < 2; i++) {
      const result = run(before, 'printf "port=%s mqtt=%s\\n" "${PROD_HTTP_PORT:-unset}" "${DISABLE_MOSQUITTO:-unset}"');
      ok(result);
      assert.match(result.stdout, endings === null ? /port=unset mqtt=unset/ : /port=18080 mqtt=true/);
      if (endings !== null) assert.ok(!fs.readFileSync(path.join(dir, '.env'), 'utf8').includes('\r'), 'CRLF remains after startup');
    }
  });
}

for (const target of ['', 'latest', 'v2.0.0']) {
  test('update metadata follows checkout: ' + (target || 'latest release'), (dir, run) => {
    const result = run(`
task_version=v1.0.0
task_commit=old-sha
task_time=2026-01-01T00:00:00Z
git(){
  case "$1" in
    describe) printf '%s\\n' "$task_version" ;;
    rev-parse) printf '%s\\n' "$task_commit" ;;
    checkout)
      printf '%s\\n' "$2" > checked-out
      task_version=v2.0.0; task_commit=new-sha; task_time=2026-01-02T00:00:00Z ;;
    tag) printf 'v2.0.0\\n' ;;
    fetch) : ;;
    *) return 1 ;;
  esac
}
date(){ printf '%s\\n' "$task_time"; }
docker(){
  case "$*" in
    'compose version') : ;;
    'compose build prod') printf 'version=%s commit=%s built=%s\\n' "$APP_VERSION" "$GIT_COMMIT" "$BUILD_TIME" > build-metadata ;;
    'compose up -d --force-recreate prod') : ;;
    *) return 1 ;;
  esac
}`, 'cmd_update ' + quote(target));
    ok(result);
    assert.equal(fs.readFileSync(path.join(dir, 'checked-out'), 'utf8').trim(), target === 'latest' ? 'origin/master' : 'v2.0.0');
    assert.equal(fs.readFileSync(path.join(dir, 'build-metadata'), 'utf8').trim(),
      'version=v2.0.0 commit=new-sha built=2026-01-02T00:00:00Z', 'build received identity from the previous checkout');
  });
}

test('failed checkout never builds or restarts', (dir, run) => {
  const result = run(`
git(){ [ "$1" != checkout ]; }
docker(){ [ "$*" = 'compose version' ] || printf '%s\\n' "$*" >> mutations; }
`, "cmd_update latest");
  assert.equal(result.status, 1);
  assert.ok(!fs.existsSync(path.join(dir, 'mutations')));
});

for (const ready of [true, false]) {
  test('verify_health ' + (ready ? 'waits until ready' : 'fails when loading never finishes'), (dir, run) => {
    fs.mkdirSync(path.join(dir, 'caddy-config'));
    fs.writeFileSync(path.join(dir, 'caddy-config/Caddyfile'), 'scope.example.org {\n}\n');
    const result = run(`
git(){ printf 'fixture-revision\\n'; }
docker(){
  case "$*" in
    'compose version') : ;;
    *'/api/stats') printf 'stats\\n' >> probes; return 0 ;;
    *'/api/healthz')
      printf 'ready\\n' >> probes
      [ ${ready} = true ] && [ "$(wc -l < probes)" -ge 3 ] ;;
    logs*) : ;;
    *) return 1 ;;
  esac
}
sleep(){ :; }
curl(){ printf '%s\\n' "$*" > https-probe; }
`, 'verify_health');
    assert.equal(result.status, ready ? 0 : 1, result.error?.message || result.stderr || result.stdout);
    const probes = fs.readFileSync(path.join(dir, 'probes'), 'utf8').trim().split('\n');
    assert.equal(probes.length, ready ? 3 : 45, 'did not poll until readiness or timeout');
    assert.ok(probes.every(p => p === 'ready'), 'statistics accepted as readiness');
    if (ready) assert.match(fs.readFileSync(path.join(dir, 'https-probe'), 'utf8'), /https:\/\/scope\.example\.org\/api\/healthz/);
    else assert.ok(!fs.existsSync(path.join(dir, 'https-probe')), 'must not check proxy health before local readiness');
  });
}

test('status still displays packet and node statistics', (dir, run) => {
  const result = run(`
git(){ printf 'fixture-revision\\n'; }
docker(){
  case "$*" in
    'compose version') : ;;
    'ps --format'*) printf 'corescope-prod\\n' ;;
    'inspect '*) printf 'healthy\\n' ;;
    'ps --filter '*) : ;;
    *'/api/stats') printf 'stats\\n' >> probes; printf '{"totalPackets":123,"totalNodes":7}\\n' ;;
    *) return 1 ;;
  esac
}
`, 'show_container_status corescope-prod Production');
  ok(result);
  assert.match(result.stdout, /123 packets, 7 nodes/);
  assert.equal(fs.readFileSync(path.join(dir, 'probes'), 'utf8'), 'stats\nstats\n');
});

for (const file of ['docker-compose.yml', 'docker-compose.no-mosquitto.yml', 'docker-compose.example.yml',
  'docker-compose.staging.yml', 'docker-compose.staging.no-mosquitto.yml']) {
  test(file + ' requires readiness', () => {
    const source = fs.readFileSync(path.join(root, file), 'utf8');
    const command = source.match(/healthcheck:\s*\n\s*test:\s*(\[[^\n]+\])/);
    assert.ok(command, 'healthcheck command missing');
    assert.deepEqual(JSON.parse(command[1]), ['CMD', 'wget', '-qO-', 'http://localhost:3000/api/healthz']);
  });
}
console.log(`${passed} passed, ${failed} failed`);
process.exitCode = failed ? 1 : 0;
