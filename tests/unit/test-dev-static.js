/* scripts/dev-static.mjs: the zero-dependency static server used to run the
 * UI on one origin against the API on another (make dev-split, CI). It must
 * never serve outside its root, and its 404 must be HTML, as a real static
 * host's is, so api() reports a wrong API base as not_json. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');

let passed = 0, failed = 0;
let chain = Promise.resolve();
function test(name, fn) {
  chain = chain.then(fn).then(
    () => { passed++; console.log('  ✅ ' + name); },
    (e) => { failed++; console.log('  ❌ ' + name + ': ' + (e && e.stack || e)); });
}

function get(port, p, method) {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, path: p, method: method || 'GET' }, (res) => {
      let body = '';
      res.on('data', (c) => { body += c; });
      res.on('end', () => resolve({ status: res.statusCode, type: res.headers['content-type'], cache: res.headers['cache-control'], body }));
    });
    req.on('error', reject);
    req.end();
  });
}

(async () => {
  const mod = await import(path.join(__dirname, '..', '..', 'scripts', 'dev-static.mjs'));
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'cs-static-'));
  fs.writeFileSync(path.join(root, 'index.html'), '<!doctype html><p>ui</p>');
  fs.mkdirSync(path.join(root, 'icons'));
  fs.writeFileSync(path.join(root, 'icons', 'a.svg'), '<svg/>');
  fs.writeFileSync(path.join(path.dirname(root), 'cs-static-secret.txt'), 'secret');
  const server = mod.createServer(root);
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const port = server.address().port;

  console.log('dev-static.mjs');

  test('serves index.html at / and files with their type', async () => {
    const a = await get(port, '/');
    assert.strictEqual(a.status, 200);
    assert.strictEqual(a.type, 'text/html; charset=utf-8');
    assert.strictEqual(a.cache, 'no-cache');
    const b = await get(port, '/icons/a.svg?v=abc');
    assert.strictEqual(b.status, 200);
    assert.strictEqual(b.type, 'image/svg+xml');
  });

  test('a missing file is an HTML 404, like a static host', async () => {
    const r = await get(port, '/api/stats');
    assert.strictEqual(r.status, 404);
    assert.match(r.type, /^text\/html/);
  });

  test('never serves outside the root', async () => {
    for (const p of ['/../cs-static-secret.txt', '/%2e%2e/cs-static-secret.txt', '/icons/..%2f..%2fcs-static-secret.txt', '/%00']) {
      const r = await get(port, p);
      assert.strictEqual(r.status, 404, p);
      assert.ok(!r.body.includes('secret'), p);
    }
    assert.strictEqual(mod.resolveFile(root, '/../../etc/passwd'), null);
  });

  test('only GET and HEAD', async () => {
    assert.strictEqual((await get(port, '/', 'POST')).status, 405);
    const h = await get(port, '/', 'HEAD');
    assert.strictEqual(h.status, 200);
    assert.strictEqual(h.body, '');
  });

  test('parses its arguments and rejects unknown ones', () => {
    assert.deepStrictEqual(mod.parseArgs(['--root', 'x', '--port', '9000']), { root: 'x', port: 9000, host: '127.0.0.1' });
    assert.throws(() => mod.parseArgs(['--proxy', 'y']), /unknown argument/);
    assert.throws(() => mod.parseArgs(['--port', 'abc']), /bad --port/);
  });

  await chain;
  server.close();
  fs.rmSync(root, { recursive: true, force: true });
  fs.rmSync(path.join(path.dirname(root), 'cs-static-secret.txt'), { force: true });
  console.log(`\ndev-static: ${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();
