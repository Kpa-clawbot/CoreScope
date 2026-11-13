/* docker/entrypoint-go.sh must keep DB_DSN_ACCOUNTS out of a container that
 * also runs the ingestor (supervisord children share its environment).
 *
 * The real script runs in a temp dir with its two external hooks replaced:
 * /select-service-user.sh by a stub, and supervisord by a stub that records
 * the environment it was given. SQLite mode keeps it clear of accounts, chown
 * and the like.
 */
'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const assert = require('assert');
const { spawnSync } = require('child_process');

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ✅ ' + name); }
  catch (e) { fail++; console.log('  ❌ ' + name + ': ' + e.message); }
}

const root = path.join(__dirname, '..', '..');
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'entrypoint-'));
const SECRET = 'postgres://acct:s3cret-pw@db.example.org/corescope';

let src = fs.readFileSync(path.join(root, 'docker', 'entrypoint-go.sh'), 'utf8');
fs.writeFileSync(path.join(tmp, 'select.sh'),
  'prepare_service_user() { CORESCOPE_SERVICE_USER=$(id -un); export CORESCOPE_SERVICE_USER; }\n');
fs.writeFileSync(path.join(tmp, 'supervisord'),
  '#!/bin/sh\necho "SUPERVISORD $*"\necho "ACCOUNTS=${DB_DSN_ACCOUNTS:-<unset>}"\n', { mode: 0o755 });
src = src.replace('. /select-service-user.sh', '. ' + path.join(tmp, 'select.sh'))
  .replace('/usr/bin/supervisord', path.join(tmp, 'supervisord'));
fs.writeFileSync(path.join(tmp, 'entrypoint.sh'), src);

function run(env) {
  const base = { PATH: process.env.PATH, DISABLE_CADDY: 'true' };
  return spawnSync('sh', [path.join(tmp, 'entrypoint.sh')],
    { env: Object.assign(base, env), encoding: 'utf8' });
}

test('refused with the ingestor in the container, value not echoed', () => {
  const r = run({ DB_DSN_ACCOUNTS: SECRET });
  assert.strictEqual(r.status, 1);
  assert.ok(/DISABLE_INGESTOR=true/.test(r.stderr), r.stderr);
  assert.ok(/docs\/postgresql\.md/.test(r.stderr), r.stderr);
  assert.ok(!(r.stdout + r.stderr).includes('s3cret-pw'), 'value leaked');
  assert.ok(!/SUPERVISORD/.test(r.stdout), 'supervisord was started');
});

test('accepted in a server-only container (DISABLE_INGESTOR=true)', () => {
  const r = run({ DB_DSN_ACCOUNTS: SECRET, DISABLE_INGESTOR: 'true' });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.ok(/supervisord-no-ingestor/.test(r.stdout), r.stdout);
  assert.ok(r.stdout.includes('ACCOUNTS=' + SECRET), 'server container lost the DSN');
});

test('an empty value is dropped and accepted with the ingestor', () => {
  const r = run({ DB_DSN_ACCOUNTS: '' });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.ok(/ACCOUNTS=<unset>/.test(r.stdout), r.stdout);
});

fs.rmSync(tmp, { recursive: true, force: true });
console.log(`\n${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);
