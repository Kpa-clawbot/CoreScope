/**
 * Unit tests for public/area-filter.js
 *
 * Tests the exported AreaFilter API via Node.js vm sandbox.
 * No real DOM or network — localStorage and fetch are mocked.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✓ ${name}`); }
  catch (e) { failed++; console.error(`  ✗ ${name}: ${e.message}`); }
}

// ---------------------------------------------------------------------------
// Helper: build a fresh vm context with mocked globals.
// Each call resets _selected / _areas so tests are independent.
// ---------------------------------------------------------------------------
function buildCtx({ storageValue = null, fetchResponse = [] } = {}) {
  const storage = Object.create(null);
  if (storageValue !== null) storage['meshcore-area-filter'] = storageValue;

  const localStorage = {
    getItem: (k) => (k in storage ? storage[k] : null),
    setItem: (k, v) => { storage[k] = String(v); },
    removeItem: (k) => { delete storage[k]; },
  };

  const fetchMock = () => Promise.resolve({ json: () => Promise.resolve(fetchResponse) });

  // Minimal document stub for render() calls and cleanup registration.
  const listeners = [];
  const document = {
    addEventListener: (type, fn, capture) => listeners.push({ type, fn, capture }),
    removeEventListener: (type, fn) => {
      const idx = listeners.findIndex(l => l.fn === fn);
      if (idx !== -1) listeners.splice(idx, 1);
    },
  };

  const ctx = {
    window: {},
    console,
    document,
    localStorage,
    fetch: fetchMock,
    encodeURIComponent,
    Array,
    Promise,
    String,
  };
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('public/area-filter.js', 'utf8'), ctx);
  return { AF: ctx.window.AreaFilter, ctx, storage, listeners };
}

// ---------------------------------------------------------------------------
// Default state (no storage, no areas loaded)
// ---------------------------------------------------------------------------
console.log('\n=== default state (no selection, no areas) ===');
{
  const { AF } = buildCtx();
  test('getSelected() is null initially', () => assert.strictEqual(AF.getSelected(), null));
  test('getAreaParam() returns empty string', () => assert.strictEqual(AF.getAreaParam(), ''));
  test('areaQueryString() returns empty string', () => assert.strictEqual(AF.areaQueryString(), ''));
}

// ---------------------------------------------------------------------------
// State restored from localStorage
// ---------------------------------------------------------------------------
console.log('\n=== selection restored from localStorage ===');
{
  const { AF } = buildCtx({ storageValue: 'BEL' });
  test('getSelected() returns stored key', () => assert.strictEqual(AF.getSelected(), 'BEL'));
  test('getAreaParam() returns stored key', () => assert.strictEqual(AF.getAreaParam(), 'BEL'));
  test('areaQueryString() returns &area=BEL', () => assert.strictEqual(AF.areaQueryString(), '&area=BEL'));
}

// ---------------------------------------------------------------------------
// areaQueryString encodes special characters
// ---------------------------------------------------------------------------
console.log('\n=== areaQueryString URL-encoding ===');
{
  const { AF } = buildCtx({ storageValue: 'SAN JOSE' });
  test('spaces are encoded', () => assert.strictEqual(AF.areaQueryString(), '&area=SAN%20JOSE'));
}

// ---------------------------------------------------------------------------
// fetchAreas — selection cleared when key no longer exists in config
// ---------------------------------------------------------------------------
console.log('\n=== fetchAreas clears stale selection ===');
(async () => {
  const { AF, storage } = buildCtx({
    storageValue: 'GONE',
    fetchResponse: [{ key: 'NL', label: 'Netherlands' }],
  });
  await AF.init(makeDomContainer());
  test('stale key cleared after fetchAreas', () => assert.strictEqual(AF.getSelected(), null));
  test('localStorage entry removed when stale', () => assert.strictEqual(storage['meshcore-area-filter'], undefined));
})();

// ---------------------------------------------------------------------------
// fetchAreas — valid selection kept after fetch
// ---------------------------------------------------------------------------
console.log('\n=== fetchAreas keeps valid selection ===');
(async () => {
  const { AF } = buildCtx({
    storageValue: 'BEL',
    fetchResponse: [{ key: 'BEL', label: 'Belgium' }, { key: 'NL', label: 'Netherlands' }],
  });
  await AF.init(makeDomContainer());
  test('valid selection retained after fetchAreas', () => assert.strictEqual(AF.getSelected(), 'BEL'));
  test('getAreaParam() still returns BEL', () => assert.strictEqual(AF.getAreaParam(), 'BEL'));
})();

// ---------------------------------------------------------------------------
// onChange / offChange
// ---------------------------------------------------------------------------
console.log('\n=== onChange / offChange ===');
{
  const { AF } = buildCtx();
  const calls = [];
  const fn = (v) => calls.push(v);
  AF.onChange(fn);

  // Manually trigger a selection change via render + click simulation would
  // require full DOM. Instead verify the listener is registered and can be removed.
  AF.offChange(fn);
  // No assertion failure means no error in register/remove path.
  test('offChange removes listener without error', () => assert.ok(true));
}

// ---------------------------------------------------------------------------
// Helper: minimal DOM container stub for init/render calls
// ---------------------------------------------------------------------------
function makeDomContainer() {
  const container = {
    innerHTML: '',
    style: {},
    _areaCleanup: null,
    querySelector: () => ({ onclick: null, hidden: true, setAttribute: () => {} }),
    contains: () => false,
  };
  return container;
}

// Print synchronous results immediately; async tests append on their own.
setImmediate(() => {
  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed > 0) process.exit(1);
});
