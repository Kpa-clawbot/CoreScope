/* Unit tests for filter UX helpers: PacketFilter metadata + autocomplete +
 * SavedFilters store (issue #966). Pure-logic only — DOM exercised by E2E.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');

function loadInCtx(files, ctx) {
  for (const f of files) vm.runInContext(fs.readFileSync(f, 'utf8'), ctx);
}

// Fake DOM-less window with a localStorage shim so filter-ux.js can be loaded
// in Node without touching the document object model.
function makeCtx() {
  const store = {};
  const ls = {
    getItem: k => Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null,
    setItem: (k, v) => { store[k] = String(v); },
    removeItem: k => { delete store[k]; },
    clear: () => { for (const k of Object.keys(store)) delete store[k]; },
  };
  // Minimal document stub — filter-ux.js init() must early-exit when DOM missing.
  const doc = { getElementById: () => null, addEventListener: () => {}, body: null };
  const win = { localStorage: ls, document: doc, addEventListener: () => {} };
  const ctx = { window: win, document: doc, localStorage: ls, console };
  vm.createContext(ctx);
  return ctx;
}

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ✓ ' + name); }
  catch (e) { console.log('  ✗ ' + name + ' — ' + e.message); fail++; }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const ctx = makeCtx();
loadInCtx(['public/packet-filter.js', 'public/filter-ux.js'], ctx);
const PF = ctx.window.PacketFilter;
const UX = ctx.window.FilterUX;

console.log('\n=== #966 filter-UX unit tests ===');

// ── Metadata exposed by PacketFilter ──────────────────────────────────────
test('PacketFilter.FIELDS exposes top-level fields', () => {
  assert(Array.isArray(PF.FIELDS), 'FIELDS is array');
  const names = PF.FIELDS.map(f => f.name);
  for (const want of ['type', 'route', 'snr', 'rssi', 'hops', 'observer', 'hash', 'size', 'age']) {
    assert(names.includes(want), 'FIELDS missing ' + want);
  }
  for (const f of PF.FIELDS) { assert(typeof f.desc === 'string' && f.desc.length, 'desc required for ' + f.name); }
});

test('PacketFilter.OPERATORS lists comparison operators with examples', () => {
  assert(Array.isArray(PF.OPERATORS), 'OPERATORS array');
  const ops = PF.OPERATORS.map(o => o.op);
  for (const want of ['==', '!=', '>', '<', '>=', '<=', 'contains', 'starts_with']) {
    assert(ops.includes(want), 'OPERATORS missing ' + want);
  }
  for (const o of PF.OPERATORS) { assert(typeof o.example === 'string' && o.example.length, 'example required for ' + o.op); }
});

test('PacketFilter.TYPE_VALUES exposes canonical type names', () => {
  assert(Array.isArray(PF.TYPE_VALUES));
  for (const want of ['ADVERT', 'GRP_TXT', 'GRP_DATA', 'TXT_MSG', 'ACK']) {
    assert(PF.TYPE_VALUES.includes(want), 'TYPE_VALUES missing ' + want);
  }
});

test('PacketFilter.ROUTE_VALUES exposes route names', () => {
  assert(Array.isArray(PF.ROUTE_VALUES));
  for (const want of ['FLOOD', 'DIRECT', 'TRANSPORT_FLOOD', 'TRANSPORT_DIRECT']) {
    assert(PF.ROUTE_VALUES.includes(want), 'ROUTE_VALUES missing ' + want);
  }
});

// ── Autocomplete suggestions ──────────────────────────────────────────────
test('suggest() on empty input returns top-level fields', () => {
  const r = PF.suggest('', 0);
  assert(r && Array.isArray(r.suggestions), 'returns object with suggestions');
  const vals = r.suggestions.map(s => s.value);
  assert(vals.includes('type'), 'suggests type');
  assert(vals.includes('snr'), 'suggests snr');
});

test('suggest() prefix-matches field names', () => {
  const r = PF.suggest('pay', 3);
  const vals = r.suggestions.map(s => s.value);
  // payload.* aliases or payload_bytes/payload_hex should surface
  assert(vals.some(v => v.startsWith('payload')), 'no payload* suggestion: ' + vals.join(','));
  assert(r.replaceStart === 0 && r.replaceEnd === 3, 'replace range covers prefix');
});

test('suggest() after `type ==` lists type values', () => {
  const r = PF.suggest('type == ', 8);
  const vals = r.suggestions.map(s => s.value);
  assert(vals.includes('ADVERT'), 'ADVERT in type values');
  assert(vals.includes('GRP_TXT'), 'GRP_TXT in type values');
});

test('suggest() after `type == AD` filters type values', () => {
  const r = PF.suggest('type == AD', 10);
  const vals = r.suggestions.map(s => s.value);
  assert(vals.includes('ADVERT'), 'ADVERT matches AD prefix');
  assert(!vals.includes('GRP_TXT'), 'GRP_TXT filtered out');
});

test('suggest() after `route ==` lists route values', () => {
  const r = PF.suggest('route == ', 9);
  const vals = r.suggestions.map(s => s.value);
  assert(vals.includes('FLOOD'), 'FLOOD route');
  assert(vals.includes('DIRECT'), 'DIRECT route');
});

test('suggest() after operator suggests operators when no field given yet (no crash)', () => {
  const r = PF.suggest('snr ', 4);
  const vals = r.suggestions.map(s => s.value);
  assert(vals.includes('>') || vals.includes('==') || vals.includes('<'), 'op suggested: ' + vals.join(','));
});

test('suggest() includes payload.* keys from dynamic discovery', () => {
  const r = PF.suggest('payload.', 8, { payloadKeys: ['name', 'lat', 'channelHash'] });
  const vals = r.suggestions.map(s => s.value);
  assert(vals.includes('payload.name'), 'payload.name from dynamic keys');
  assert(vals.includes('payload.lat'), 'payload.lat from dynamic keys');
});

// ── Improved parse-error positioning ──────────────────────────────────────
test('error message cites position for unknown character', () => {
  const r = PF.parse('snr @ 5');
  assert(r.error && /position/i.test(r.error), 'error should cite position: ' + r.error);
});

// ── Saved filters store ───────────────────────────────────────────────────
test('SavedFilters.defaults() returns at least 5 starter filters', () => {
  const d = UX.SavedFilters.defaults();
  assert(Array.isArray(d) && d.length >= 5, 'defaults length ≥ 5: ' + (d && d.length));
  for (const f of d) { assert(f.name && f.expr, 'each default has name + expr'); }
});

test('SavedFilters.list() includes defaults when nothing saved', () => {
  ctx.window.localStorage.clear();
  const list = UX.SavedFilters.list();
  assert(list.length >= 5, 'list seeded with defaults');
});

test('SavedFilters.save() persists to localStorage and survives list()', () => {
  ctx.window.localStorage.clear();
  UX.SavedFilters.save('my filter', 'snr > 10');
  const list = UX.SavedFilters.list();
  const found = list.find(f => f.name === 'my filter' && f.expr === 'snr > 10');
  assert(found, 'saved filter present in list');
  // Must persist to LS, not memory
  const raw = ctx.window.localStorage.getItem('corescope_saved_filters_v1');
  assert(raw && raw.includes('snr > 10'), 'persisted to LS: ' + raw);
});

test('SavedFilters.delete() removes user filter but keeps defaults', () => {
  ctx.window.localStorage.clear();
  UX.SavedFilters.save('temp', 'hops > 0');
  UX.SavedFilters.delete('temp');
  const list = UX.SavedFilters.list();
  assert(!list.find(f => f.name === 'temp'), 'temp removed');
  assert(list.length >= 5, 'defaults still present');
});

test('SavedFilters.save() overwrites existing user filter with same name', () => {
  ctx.window.localStorage.clear();
  UX.SavedFilters.save('x', 'snr > 1');
  UX.SavedFilters.save('x', 'snr > 99');
  const list = UX.SavedFilters.list();
  const matches = list.filter(f => f.name === 'x');
  assert(matches.length === 1, 'no duplicate name');
  assert(matches[0].expr === 'snr > 99', 'overwritten to latest');
});

// ── Filter-by-cell helper (for right-click) ───────────────────────────────
test('buildCellFilterClause() emits field == "value" with quoting for strings', () => {
  assert(UX.buildCellFilterClause('observer', 'Dorrington', '==') === 'observer == "Dorrington"');
  assert(UX.buildCellFilterClause('snr', '8.5', '==') === 'snr == 8.5');
  assert(UX.buildCellFilterClause('type', 'ADVERT', '==') === 'type == ADVERT');
});

test('appendClauseToExpr() appends with && when expr present', () => {
  assert(UX.appendClauseToExpr('', 'snr > 5') === 'snr > 5');
  assert(UX.appendClauseToExpr('type == ADVERT', 'snr > 5') === 'type == ADVERT && snr > 5');
});

console.log(`\n=== Results: ${pass} passed, ${fail} failed ===`);
process.exit(fail > 0 ? 1 : 0);
