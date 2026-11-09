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
// The top-level pages of public/: each is served on its own and gets the cache buster.
const PAGES = fs.readdirSync(path.join(ROOT, 'public')).filter((f) => /\.html$/i.test(f)).sort();
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

test('is public/ byte for byte plus _headers, apart from the top-level pages and deployment.js', () => {
  const r = run(OK);
  const list = (dir) => {
    const acc = [];
    (function walk(d) { for (const e of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, e.name); if (e.isDirectory()) walk(p); else acc.push(path.relative(dir, p)); } })(dir);
    return acc.sort();
  };
  const pub = list(path.join(ROOT, 'public'));
  assert.deepStrictEqual(list(r.out), pub.concat('_headers').sort());
  for (const f of pub) {
    if (PAGES.includes(f) || f === 'deployment.js') continue;
    assert.ok(fs.readFileSync(path.join(ROOT, 'public', f)).equals(fs.readFileSync(path.join(r.out, f))), f + ' changed');
  }
  assert.strictEqual(fs.readFileSync(path.join(r.out, '_headers'), 'utf8'),
    fs.readFileSync(path.join(ROOT, 'deploy', 'static', '_headers'), 'utf8'));
});

// Every top-level page, not just index.html, loads its own scripts with the
// cache buster, so a page served fresh never runs a script cached from the
// previous deploy. vendor/ files carry their version in the path instead.
test('every local script and stylesheet in a top-level page carries ?v=__BUST__', () => {
  const missing = [];
  for (const page of PAGES) {
    const html = fs.readFileSync(path.join(ROOT, 'public', page), 'utf8');
    for (const tag of html.match(/<(script|link)\b[^>]*>/gi) || []) {
      if (/^<link/i.test(tag) && !/\brel=["']stylesheet["']/i.test(tag)) continue;
      const m = /\b(?:src|href)=["']([^"']+)["']/i.exec(tag);
      if (!m || /^(https?:)?\/\//i.test(m[1]) || /^data:/i.test(m[1]) || /^\/?vendor\//.test(m[1])) continue;
      if (!/\?v=__BUST__$/.test(m[1])) missing.push(page + ': ' + m[1]);
    }
  }
  assert.deepStrictEqual(missing, []);
});

test('the build replaces the cache buster in every top-level page and changes nothing else', () => {
  const r = run(OK);
  for (const page of PAGES) {
    const src = fs.readFileSync(path.join(ROOT, 'public', page), 'utf8');
    assert.strictEqual(fs.readFileSync(path.join(r.out, page), 'utf8'), src.split('__BUST__').join('abc1234'), page);
  }
  assert.ok(fs.readFileSync(path.join(r.out, 'area-map.html'), 'utf8').includes('api-base.js?v=abc1234'));
});

test('_headers makes every top-level page and deployment.js revalidate', () => {
  const paths = fs.readFileSync(path.join(ROOT, 'deploy', 'static', '_headers'), 'utf8')
    .split('\n').filter((l) => l.startsWith('/')).map((l) => l.trim()).sort();
  assert.deepStrictEqual(paths, ['/', '/deployment.js'].concat(PAGES.map((p) => '/' + p)).sort());
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
    const r = run({ API_BASE: base, AUTH: 'false', COMMIT: 'dev' });
    assert.strictEqual(r.status, 0, base + ': ' + r.stderr);
    const d = descriptor(r.out);
    assert.strictEqual(d.apiBase, base);
    assert.strictEqual(d.auth, false);
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

// OUT is replaced wholesale, so a typo must not wipe an unrelated folder.
test('refuses to replace a folder that is neither empty nor a previous build', () => {
  const dir = path.join(TMP, 'precious');
  fs.mkdirSync(dir);
  fs.writeFileSync(path.join(dir, 'notes.txt'), 'keep');
  const r = run(Object.assign({}, OK, { OUT: dir }));
  assert.notStrictEqual(r.status, 0, 'should fail');
  assert.ok(/refusing to replace/.test(r.stderr), r.stderr);
  assert.strictEqual(fs.readFileSync(path.join(dir, 'notes.txt'), 'utf8'), 'keep');
});

test('refuses a file as OUT', () => {
  const file = path.join(TMP, 'a-file');
  fs.writeFileSync(file, 'keep');
  const r = run(Object.assign({}, OK, { OUT: file }));
  assert.notStrictEqual(r.status, 0, 'should fail');
  assert.ok(/not a directory/.test(r.stderr), r.stderr);
  assert.strictEqual(fs.readFileSync(file, 'utf8'), 'keep');
});

test('replaces its own previous build, and builds into an empty folder', () => {
  const first = run(OK);
  assert.strictEqual(first.status, 0, first.stderr);
  const again = run(Object.assign({}, OK, { OUT: first.out, CHANNEL: 'next' }));
  assert.strictEqual(again.status, 0, again.stderr);
  assert.strictEqual(descriptor(first.out).channel, 'next');
  const empty = path.join(TMP, 'empty'); // the web image builds into the empty /srv
  fs.mkdirSync(empty);
  const r = run(Object.assign({}, OK, { OUT: empty }));
  assert.strictEqual(r.status, 0, r.stderr);
});

const refusals = [
  ['same-origin without logins', { API_BASE: 'same-origin', AUTH: 'false' }, /same-origin needs AUTH=true/],
  // The server allows no credentialed CORS, so every request of such a build
  // would be blocked: logins on another origin are refused, not built broken.
  ['logins on another origin', { AUTH: 'true' }, /AUTH=true needs API_BASE=same-origin/],
  ['logins on another origin, localhost', { API_BASE: 'http://localhost:3000', AUTH: 'true' }, /AUTH=true needs API_BASE=same-origin/],
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
