/* scripts/static/build.sh: packages public/ for a static host. The output is
 * public/ byte for byte except index.html (cache buster) and deployment.js
 * (where the API is), plus _headers; every input is validated because it
 * lands inside a JS string literal. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const vm = require('vm');
const { spawnSync } = require('child_process');

const ROOT = path.join(__dirname, '..', '..');
const SCRIPT = path.join(ROOT, 'scripts', 'static', 'build.sh');
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'cs-static-build-'));
let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); } catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

let n = 0;
function run(env) {
  const out = path.join(TMP, 'out' + (n++));
  const base = { PATH: process.env.PATH, HOME: process.env.HOME, OUT: out };
  const r = spawnSync('bash', [SCRIPT], { cwd: ROOT, env: Object.assign(base, env), encoding: 'utf8' });
  return { status: r.status, stderr: r.stderr, stdout: r.stdout, out };
}
function descriptor(out) {
  const ctx = {};
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(out, 'deployment.js'), 'utf8'), ctx);
  return JSON.parse(JSON.stringify(ctx.CS_DEPLOYMENT));
}
const OK = { API_BASE: 'https://analyzer.example', AUTH: 'false', CHANNEL: 'beta', COMMIT: 'abc1234' };

console.log('static build.sh');

test('writes the descriptor from the environment', () => {
  const r = run(Object.assign({}, OK, { CLASSIC_URL: 'https://analyzer.example/' }));
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(descriptor(r.out), { channel: 'beta', commit: 'abc1234', version: null,
    apiBase: 'https://analyzer.example', auth: false, classicUrl: 'https://analyzer.example' });
});

test('replaces every __BUST__ in index.html with the commit', () => {
  const r = run(OK);
  const html = fs.readFileSync(path.join(r.out, 'index.html'), 'utf8');
  assert.ok(!html.includes('__BUST__'));
  const want = (fs.readFileSync(path.join(ROOT, 'public', 'index.html'), 'utf8').match(/__BUST__/g) || []).length;
  assert.strictEqual((html.match(/\?v=abc1234/g) || []).length, want);
});

test('is public/ byte for byte plus _headers, apart from index.html and deployment.js', () => {
  const r = run(OK);
  const list = (dir) => {
    const acc = [];
    (function walk(d) { for (const e of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, e.name); if (e.isDirectory()) walk(p); else acc.push(path.relative(dir, p)); } })(dir);
    return acc.sort();
  };
  const pub = list(path.join(ROOT, 'public'));
  assert.deepStrictEqual(list(r.out), pub.concat('_headers').sort());
  for (const f of pub) {
    if (f === 'index.html' || f === 'deployment.js') continue;
    assert.ok(fs.readFileSync(path.join(ROOT, 'public', f)).equals(fs.readFileSync(path.join(r.out, f))), f + ' changed');
  }
  assert.strictEqual(fs.readFileSync(path.join(r.out, '_headers'), 'utf8'),
    fs.readFileSync(path.join(ROOT, 'deploy', 'static', '_headers'), 'utf8'));
});

test('the same commit builds the same bytes', () => {
  const a = run(OK), b = run(OK);
  for (const f of ['index.html', 'deployment.js']) {
    assert.ok(fs.readFileSync(path.join(a.out, f)).equals(fs.readFileSync(path.join(b.out, f))), f);
  }
});

test('the commit defaults to the first 7 characters of CF_PAGES_COMMIT_SHA', () => {
  const env = Object.assign({}, OK, { CF_PAGES_COMMIT_SHA: '0123456789abcdef0123456789abcdef01234567' });
  delete env.COMMIT;
  const r = run(env);
  assert.strictEqual(r.status, 0, r.stderr);
  assert.strictEqual(descriptor(r.out).commit, '0123456');
});

test('http is accepted for localhost only, and the channel defaults to static', () => {
  for (const base of ['http://localhost:3000', 'http://127.0.0.1:13581']) {
    const r = run({ API_BASE: base, AUTH: 'true', COMMIT: 'dev' });
    assert.strictEqual(r.status, 0, base + ': ' + r.stderr);
    const d = descriptor(r.out);
    assert.strictEqual(d.apiBase, base);
    assert.strictEqual(d.auth, true);
    assert.strictEqual(d.channel, 'static');
  }
});

test('same-origin is for a host that forwards /api and /ws to the server', () => {
  const r = run({ API_BASE: 'same-origin', AUTH: 'true', COMMIT: 'dev' });
  assert.strictEqual(r.status, 0, r.stderr);
  const d = descriptor(r.out);
  assert.strictEqual(d.apiBase, '');
  assert.strictEqual(d.auth, true);
});

const refusals = [
  ['same-origin without logins', { API_BASE: 'same-origin', AUTH: 'false' }, /same-origin needs AUTH=true/],
  ['no API_BASE', { API_BASE: '' }, /API_BASE is required/],
  ['http on a real host', { API_BASE: 'http://analyzer.example' }, /API_BASE must be/],
  ['a path', { API_BASE: 'https://analyzer.example/corescope' }, /API_BASE must be/],
  ['upper case', { API_BASE: 'https://Analyzer.example' }, /API_BASE must be/],
  ['a quote that would break out of the string', { API_BASE: "https://a.example';alert(1)//" }, /API_BASE must be/],
  ['no AUTH (no default)', { AUTH: '' }, /AUTH must be true or false/],
  ['AUTH yes', { AUTH: 'yes' }, /AUTH must be true or false/],
  ['an unknown channel', { CHANNEL: 'prod' }, /CHANNEL must be/],
  ['a commit with a quote', { COMMIT: "abc'1" }, /COMMIT must be/],
  ['a classic URL with a path', { CLASSIC_URL: 'https://analyzer.example/x' }, /CLASSIC_URL must be/],
];
for (const [what, env, re] of refusals) {
  test('refuses ' + what, () => {
    const r = run(Object.assign({}, OK, env));
    assert.notStrictEqual(r.status, 0, 'should fail');
    assert.ok(re.test(r.stderr), r.stderr);
  });
}

fs.rmSync(TMP, { recursive: true, force: true });
console.log(`\nstatic build: ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
