/* scripts/e2e-coverage.js (#2203): every E2E page's __coverage__ is read before
 * its document is replaced or closed, summed, and never counted twice. Driven
 * with fake pages, so no browser is needed. */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');

const assert = require('assert');
const fs = require('fs');
const path = require('path');
const os = require('os');
const { readAndReset, mergeInto, createCollector, writeCoverage } = require('../../scripts/e2e-coverage');

// Playwright calls that replace a page's document. Listed here, not taken from
// the module, so dropping one there fails this test.
const NEW_DOCUMENT = ['goto', 'reload', 'setContent', 'goBack', 'goForward'];

// One instrumented file as istanbul shapes it: statement, function and branch
// counters plus the maps that only need to travel once.
function freshFile() {
  return {
    path: 'public/app.js',
    statementMap: { 0: {}, 1: {} }, fnMap: { 0: {} }, branchMap: { 0: {} },
    s: { 0: 0, 1: 0 }, f: { 0: 0 }, b: { 0: [0, 0] },
  };
}

// A fake Playwright page: evaluate runs the function against this page's
// window, and goto/reload/setContent/goBack/goForward load a new document
// (fresh counters), as a real full load does.
function fakePage(context) {
  const page = {
    win: { __coverage__: { 'public/app.js': freshFile() } },
    closed: false,
    isClosed() { return this.closed; },
    async evaluate(fn, arg) {
      const saved = global.window;
      global.window = this.win;
      try { return fn(arg); } finally { global.window = saved; }
    },
    context() { return context; },
    hit(stmt, times) { this.win.__coverage__['public/app.js'].s[stmt] += times; },
  };
  for (const name of NEW_DOCUMENT) {
    page[name] = async function () {
      this.win = { __coverage__: { 'public/app.js': freshFile() } };
    };
  }
  page.close = async function () { this.closed = true; };
  return page;
}

function fakeContext() {
  const listeners = [];
  const ctx = {
    list: [],
    pages() { return this.list.filter(p => !p.closed); },
    on(event, fn) { if (event === 'page') listeners.push(fn); },
    // A page the context opens by itself (window.open): only the 'page'
    // event announces it.
    open() {
      const p = fakePage(ctx);
      this.list.push(p);
      listeners.forEach(fn => fn(p));
      return p;
    },
    async newPage() { return this.open(); },
    async close() { this.list.forEach(p => { p.closed = true; }); },
  };
  return ctx;
}

function fakeBrowser() {
  const browser = {
    ctxs: [],
    contexts() { return this.ctxs; },
    async newContext() { const c = fakeContext(); this.ctxs.push(c); return c; },
    async newPage() { const c = await this.newContext(); return c.newPage(); },
    async close() { this.ctxs.forEach(c => c.list.forEach(p => { p.closed = true; })); },
  };
  return browser;
}

async function main() {
  // readAndReset: a new file comes back whole, a known one as counts only,
  // and the page's counters are zero afterwards.
  {
    global.window = { __coverage__: { 'public/app.js': freshFile() } };
    window.__coverage__['public/app.js'].s[0] = 3;
    window.__coverage__['public/app.js'].b[0][1] = 2;
    const first = JSON.parse(readAndReset([]));
    assert.ok(first['public/app.js'], 'an instrumented file comes back');
    assert.strictEqual(first['public/app.js'].s[0], 3);
    assert.deepStrictEqual(first['public/app.js'].b[0], [0, 2]);
    assert.ok(first['public/app.js'].statementMap, 'an unseen file must carry its maps');
    assert.deepStrictEqual(window.__coverage__['public/app.js'].s, { 0: 0, 1: 0 }, 'statement counters are zeroed');
    assert.deepStrictEqual(window.__coverage__['public/app.js'].b[0], [0, 0], 'branch counters are zeroed');
    const again = JSON.parse(readAndReset(['public/app.js']));
    assert.strictEqual(again['public/app.js'].statementMap, undefined, 'a known file travels as counts only');
    assert.strictEqual(again['public/app.js'].s[0], 0, 'a second read of the same document adds nothing');
    global.window = {};
    assert.strictEqual(readAndReset([]), null, 'an uninstrumented page gives nothing');
    delete global.window;
  }

  // mergeInto sums every counter and copies files it has not seen.
  {
    const store = {};
    const a = freshFile(); a.s[0] = 1; a.b[0] = [1, 0];
    mergeInto(store, { 'public/app.js': a });
    mergeInto(store, { 'public/app.js': { s: { 0: 2, 1: 5 }, f: { 0: 1 }, b: { 0: [0, 4] } } });
    assert.deepStrictEqual(store['public/app.js'].s, { 0: 3, 1: 5 });
    assert.deepStrictEqual(store['public/app.js'].f, { 0: 1 });
    assert.deepStrictEqual(store['public/app.js'].b, { 0: [1, 4] });
  }

  // Every document a page shows is counted once: before each call that
  // replaces it, and before close.
  for (const name of NEW_DOCUMENT) {
    const c = createCollector();
    const browser = fakeBrowser();
    c.trackBrowser(browser);
    const page = await browser.newPage();
    page.hit(0, 1);
    await page[name]();
    page.hit(0, 2);
    page.hit(1, 1);
    await page.close();
    assert.deepStrictEqual(c.store['public/app.js'].s, { 0: 3, 1: 1 }, name + ': both documents counted once');
    await browser.close();
    assert.deepStrictEqual(c.store['public/app.js'].s, { 0: 3, 1: 1 }, name + ': a closed page is not read again');
  }

  // Pages left open are read when their context or browser closes, including
  // pages from browser.newContext() and ones the page itself opened.
  {
    const c = createCollector();
    const browser = fakeBrowser();
    c.trackBrowser(browser);
    const ctx = await browser.newContext();
    const p1 = await ctx.newPage();
    p1.hit(0, 1);
    await ctx.close();
    assert.strictEqual(c.store['public/app.js'].s[0], 1, 'context.close reads its pages');

    const ctx2 = await browser.newContext();
    const popup = ctx2.open();
    popup.hit(1, 2);
    await popup.goto();
    assert.strictEqual(c.store['public/app.js'].s[1], 2, 'a page opened by the page itself is tracked too');
    const p2 = await ctx2.newPage();
    p2.hit(1, 4);
    await browser.close();
    assert.strictEqual(c.store['public/app.js'].s[1], 6, 'browser.close reads the pages still open');
  }

  // wrapLaunch tracks what launch returns.
  {
    const c = createCollector();
    const browserType = { async launch() { return fakeBrowser(); } };
    c.wrapLaunch(browserType);
    const browser = await browserType.launch();
    const page = await browser.newPage();
    page.hit(0, 7);
    await browser.close();
    assert.strictEqual(c.store['public/app.js'].s[0], 7);
  }

  // A read that fails or hangs is given up: the call it runs before still
  // happens, promptly, and nothing is added.
  {
    const c = createCollector({ readTimeoutMs: 50 });
    const browser = fakeBrowser();
    c.trackBrowser(browser);
    const broken = await browser.newPage();
    broken.evaluate = async () => { throw new Error('Target crashed'); };
    await broken.close();
    assert.ok(broken.closed, 'close still runs after a failed read');

    const busy = await browser.newPage();
    busy.evaluate = () => new Promise(() => {}); // a page stuck in a busy loop
    const started = Date.now();
    await busy.close();
    assert.ok(busy.closed, 'close still runs after a read that never answers');
    assert.ok(Date.now() - started < 2000, 'the read is given up after the timeout, took ' + (Date.now() - started) + ' ms');
    assert.deepStrictEqual(c.store, {}, 'a failed or hung read adds nothing');
  }

  // writeCoverage: one file when there is coverage, none without, and never
  // a throw (it runs in an exit handler).
  {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-cov-'));
    const out = path.join(dir, 'nyc');
    assert.strictEqual(writeCoverage(fs, path, {}, out, 'suite', 1), null, 'nothing to write');
    assert.ok(!fs.existsSync(out), 'no directory without coverage');
    const store = { 'public/app.js': freshFile() };
    const file = writeCoverage(fs, path, store, out, 'test-x-e2e', 42);
    assert.strictEqual(path.basename(file), 'e2e-test-x-e2e-42.json');
    assert.deepStrictEqual(JSON.parse(fs.readFileSync(file, 'utf8')), store);
    const fullDisk = { mkdirSync() {}, writeFileSync() { throw new Error('ENOSPC'); } };
    const savedError = console.error;
    console.error = () => {};
    try {
      assert.strictEqual(writeCoverage(fullDisk, path, store, out, 'suite', 1), null, 'a failed write returns null');
    } finally {
      console.error = savedError;
    }
  }

  finished = true;
  console.log('test-e2e-coverage: ok');
}

// A read that waits forever leaves nothing for node to wait on, so the
// process would exit 0 half-way through the test without this check.
let finished = false;
process.on('exit', code => {
  if (code === 0 && !finished) {
    console.error('test-e2e-coverage: stopped before the end (a read never returned)');
    process.exitCode = 1;
  }
});

main().catch(e => { console.error(e); process.exit(1); });
