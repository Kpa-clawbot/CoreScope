'use strict';
// Collects window.__coverage__ from every page a Playwright E2E suite opens
// (#2203). Loaded through scripts/e2e-coverage-hook.js; kept free of
// Playwright and fs so tests/unit/test-e2e-coverage.js can drive it with fakes.
//
// A page's counters live until its document is replaced, so they are read
// (and zeroed) before every call that can replace or end the document. A
// full load the suite does not start through one of these calls, such as a
// click on a link that leaves the SPA, is not counted.

const PAGE_CALLS = ['goto', 'reload', 'setContent', 'goBack', 'goForward', 'close'];

// A read must never hold up the call it runs before: a page stuck in a busy
// loop would otherwise keep browser.close() in a suite's cleanup waiting for
// as long as the loop runs. Past this, the read is given up.
const READ_TIMEOUT_MS = 5000;

// Runs inside the page. Returns the hit counts since the last read and zeroes
// them, so a document read twice is not counted twice. A file the caller has
// not seen yet comes back whole (with its statement, function and branch maps).
// The result is a JSON string: Playwright serialises a returned object node by
// node, which took about 10 s for the roughly 6 MB of maps of the first read.
function readAndReset(known) {
  const cov = window.__coverage__;
  if (!cov) return null;
  const seen = new Set(known);
  const out = {};
  for (const file of Object.keys(cov)) {
    const fc = cov[file];
    out[file] = seen.has(file) ? { s: fc.s, f: fc.f, b: fc.b } : fc;
  }
  const json = JSON.stringify(out);
  for (const file of Object.keys(cov)) {
    const fc = cov[file];
    for (const k in fc.s) fc.s[k] = 0;
    for (const k in fc.f) fc.f[k] = 0;
    for (const k in fc.b) fc.b[k].fill(0);
  }
  return json;
}

// Adds a snapshot from readAndReset to store, summing the hit counts.
function mergeInto(store, snapshot) {
  for (const file of Object.keys(snapshot)) {
    const add = snapshot[file];
    const into = store[file];
    if (!into) {
      store[file] = add;
      continue;
    }
    for (const k in add.s) into.s[k] = (into.s[k] || 0) + add.s[k];
    for (const k in add.f) into.f[k] = (into.f[k] || 0) + add.f[k];
    for (const k in add.b) {
      const sum = into.b[k] || add.b[k].map(() => 0);
      add.b[k].forEach((n, i) => { sum[i] = (sum[i] || 0) + n; });
      into.b[k] = sum;
    }
  }
}

function createCollector({ readTimeoutMs = READ_TIMEOUT_MS } = {}) {
  const store = {};
  const pages = new WeakSet();
  const contexts = new WeakSet();

  async function collect(page) {
    if (page.isClosed()) return;
    let timer;
    const timeout = new Promise(resolve => { timer = setTimeout(resolve, readTimeoutMs, null); });
    let snapshot;
    try {
      snapshot = await Promise.race([page.evaluate(readAndReset, Object.keys(store)), timeout]);
    } catch {
      return; // page crashed or navigated away mid-read: nothing to add
    } finally {
      clearTimeout(timer);
    }
    if (snapshot) mergeInto(store, JSON.parse(snapshot));
  }

  function readFirst(obj, names, read) {
    for (const name of names) {
      const orig = obj[name];
      if (typeof orig !== 'function') continue;
      obj[name] = async function (...args) {
        await read();
        return orig.apply(this, args);
      };
    }
  }

  function trackPage(page) {
    if (pages.has(page)) return page;
    pages.add(page);
    readFirst(page, PAGE_CALLS, () => collect(page));
    return page;
  }

  function trackContext(context) {
    if (contexts.has(context)) return context;
    contexts.add(context);
    context.pages().forEach(trackPage);
    context.on('page', trackPage);
    readFirst(context, ['close'], () => Promise.all(context.pages().map(collect)));
    const newPage = context.newPage;
    context.newPage = async function (...args) {
      return trackPage(await newPage.apply(this, args));
    };
    return context;
  }

  function trackBrowser(browser) {
    const newContext = browser.newContext;
    browser.newContext = async function (...args) {
      return trackContext(await newContext.apply(this, args));
    };
    const newPage = browser.newPage;
    browser.newPage = async function (...args) {
      const page = await newPage.apply(this, args);
      trackContext(page.context());
      return trackPage(page);
    };
    readFirst(browser, ['close'], () =>
      Promise.all(browser.contexts().flatMap(c => c.pages()).map(collect)));
    return browser;
  }

  function wrapLaunch(browserType) {
    const launch = browserType.launch;
    browserType.launch = async function (...args) {
      return trackBrowser(await launch.apply(this, args));
    };
  }

  return { store, collect, trackPage, trackContext, trackBrowser, wrapLaunch };
}

// Writes store as one nyc input file, or nothing when it is empty. Never
// throws: it runs in an exit handler, where a throw would change the suite's
// exit code. Returns the file written, or null.
function writeCoverage(fs, path, store, outDir, suite, pid) {
  if (Object.keys(store).length === 0) return null;
  try {
    fs.mkdirSync(outDir, { recursive: true });
    const file = path.join(outDir, `e2e-${suite}-${pid}.json`);
    fs.writeFileSync(file, JSON.stringify(store));
    return file;
  } catch (e) {
    console.error(`e2e-coverage: not written: ${e.message}`);
    return null;
  }
}

module.exports = { readAndReset, mergeInto, createCollector, writeCoverage };
