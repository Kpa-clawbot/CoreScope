// test-network-digest-tool.js — vm.createContext sandbox tests for
// public/network-digest.js (Tools > Network Digest page).
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try {
    await fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

function makeDigest(overrides) {
  return Object.assign({
    window: '7d',
    since: '2026-07-22T10:00:00Z',
    newNodes: 3,
    roleChanges: 1,
    nameChanges: 0,
    positionMoves: 2,
    resurrections: 1,
    topArea: { label: 'Area A', count: 2 },
  }, overrides);
}

function createSandbox(digestFixture) {
  const docStore = {};
  const apiCalls = [];
  function fakeEl(id) {
    if (!docStore[id]) {
      docStore[id] = {
        id: id,
        innerHTML: '',
        textContent: '',
        value: '',
        dataset: {},
        _listeners: {},
        addEventListener: function (evt, fn) { this._listeners[evt] = fn; },
        querySelectorAll: function () { return []; },
        querySelector: function () { return null; },
      };
    }
    return docStore[id];
  }

  const sandbox = {
    window: {},
    document: {
      getElementById: (id) => fakeEl(id),
      querySelectorAll: () => [],
      querySelector: () => null,
    },
    location: { hash: '#/tools/network-digest' },
    api: (path) => {
      apiCalls.push(path);
      return Promise.resolve(typeof digestFixture === 'function' ? digestFixture(path) : digestFixture);
    },
    URLSearchParams: URLSearchParams,
    registerPage: function () {},
    timeAgo: (iso) => 'TIME_AGO(' + iso + ')',
    encodeURIComponent: encodeURIComponent,
    console: console,
    __docStore: docStore,
    __apiCalls: apiCalls,
  };
  sandbox.self = sandbox;
  sandbox.globalThis = sandbox;
  const ctx = vm.createContext(sandbox);
  const src = fs.readFileSync(__dirname + '/public/network-digest.js', 'utf8');
  vm.runInContext(src, ctx);
  return sandbox;
}

function initWith(digestFixture) {
  const sb = createSandbox(digestFixture);
  const container = { innerHTML: '' };
  sb.window.NetworkDigestTool.init(container);
  sb.__container = container;
  return sb;
}

function waitForLoad() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

(async () => {
  console.log('\n=== network-digest.js: Network Digest tool page ===');

  await test('window.NetworkDigestTool exists with init/destroy', () => {
    const sb = createSandbox(makeDigest());
    assert.strictEqual(typeof sb.window.NetworkDigestTool.init, 'function');
    assert.strictEqual(typeof sb.window.NetworkDigestTool.destroy, 'function');
  });

  await test('init renders window tabs with 7d active by default and requests the 7d window', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const html = sb.__container.innerHTML;
    assert.ok(html.includes('data-window="7d"'), `expected a 7d tab, got: ${html}`);
    assert.ok(/class="tab-btn active"[^>]*data-window="7d"|data-window="7d"[^>]*class="tab-btn active"/.test(html),
      `expected the 7d tab to be active by default, got: ${html}`);
    assert.ok(sb.__apiCalls[0].includes('window=7d'), `expected the initial load to request window=7d, got: ${sb.__apiCalls[0]}`);
  });

  await test('clicking a different window tab reloads data for that window', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const tabs = sb.__docStore['network-digest-window-tabs'];
    tabs._listeners.click({ target: { closest: () => ({ dataset: { window: '30d' } }) } });
    await waitForLoad();
    assert.ok(sb.__apiCalls[sb.__apiCalls.length - 1].includes('window=30d'), `expected a reload for window=30d, got: ${sb.__apiCalls}`);
    assert.ok(tabs.innerHTML.includes('data-window="30d"'), 'expected the tabs to re-render');
    assert.ok(/class="tab-btn active"[^>]*data-window="30d"|data-window="30d"[^>]*class="tab-btn active"/.test(tabs.innerHTML),
      `expected the 30d tab to become active, got: ${tabs.innerHTML}`);
  });

  await test('clicking the already-active tab does not trigger a reload', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const callsBefore = sb.__apiCalls.length;
    const tabs = sb.__docStore['network-digest-window-tabs'];
    tabs._listeners.click({ target: { closest: () => ({ dataset: { window: '7d' } }) } });
    await waitForLoad();
    assert.strictEqual(sb.__apiCalls.length, callsBefore, 'expected no additional api call for a no-op tab click');
  });

  await test('renders a stat tile for each of the five counts, linking to the right drill-down tool', async () => {
    const sb = initWith(makeDigest());
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('href="#/tools/new-nodes"'), 'expected a link to New Nodes');
    assert.ok(content.innerHTML.includes('href="#/tools/node-changes"'), 'expected links to Node Changes');
    assert.ok(content.innerHTML.includes('New Nodes'), 'expected the New Nodes label');
    assert.ok(content.innerHTML.includes('Role Changes'), 'expected the Role Changes label');
    assert.ok(content.innerHTML.includes('Name Changes'), 'expected the Name Changes label');
    assert.ok(content.innerHTML.includes('Position Moves'), 'expected the Position Moves label');
    assert.ok(content.innerHTML.includes('Returned'), 'expected the Returned (resurrections) label');
    assert.ok(content.innerHTML.includes('>3<'), `expected the newNodes value 3 to render, got: ${content.innerHTML}`);
  });

  await test('renders the topArea card when present', async () => {
    const sb = initWith(makeDigest({ topArea: { label: 'Area A', count: 2 } }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('Area A'), `expected the top area label, got: ${content.innerHTML}`);
    assert.ok(content.innerHTML.includes('2 new nodes'), `expected the pluralized count, got: ${content.innerHTML}`);
  });

  await test('singular "new node" wording when topArea count is 1', async () => {
    const sb = initWith(makeDigest({ topArea: { label: 'Area B', count: 1 } }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('1 new node') && !content.innerHTML.includes('1 new nodes'),
      `expected singular wording, got: ${content.innerHTML}`);
  });

  await test('omits the topArea card entirely when absent', async () => {
    const sb = initWith(makeDigest({ topArea: null }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(!content.innerHTML.includes('Most Growth'), `expected no top-area card, got: ${content.innerHTML}`);
  });

  await test('shows a neutral "nothing to report" message when every count is zero', async () => {
    const sb = initWith(makeDigest({ newNodes: 0, roleChanges: 0, nameChanges: 0, positionMoves: 0, resurrections: 0, topArea: null }));
    await waitForLoad();
    const content = sb.__docStore['network-digest-content'];
    assert.ok(content.innerHTML.includes('Nothing to report'), `expected the all-zero message, got: ${content.innerHTML}`);
    assert.ok(!content.innerHTML.includes('stats-grid'), 'should not render the stat tiles when everything is zero');
  });

  await test('status line reports the since timestamp', async () => {
    const sb = initWith(makeDigest({ since: '2026-07-22T10:00:00Z' }));
    await waitForLoad();
    const status = sb.__docStore['network-digest-status'];
    assert.ok(status.textContent.includes('2026-07-22T10:00:00Z'), `expected the since timestamp, got: ${status.textContent}`);
    assert.ok(status.textContent.includes('TIME_AGO(2026-07-22T10:00:00Z)'), `expected timeAgo formatting, got: ${status.textContent}`);
  });

  await test('a failed load shows an error message, not a stuck loading state', async () => {
    const sb = initWith(() => Promise.reject(new Error('boom')));
    await waitForLoad();
    const status = sb.__docStore['network-digest-status'];
    const content = sb.__docStore['network-digest-content'];
    assert.ok(status.textContent.includes('Failed to load'), `expected an error message, got: ${status.textContent}`);
    assert.ok(!content.innerHTML.includes('Loading'), 'should not still show the loading placeholder after failure');
  });

  console.log('\n════════════════════════════════════════');
  console.log(`  Network Digest tool: ${passed} passed, ${failed} failed`);
  console.log('════════════════════════════════════════');
  process.exit(failed === 0 ? 0 : 1);
})();
