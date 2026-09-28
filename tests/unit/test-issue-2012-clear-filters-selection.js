/**
 * Regression test for #2012: Clear Filters must empty the observer and type
 * selections, not only filters.observer / filters.type.
 *
 * Before the fix the Clear handler reset the checkboxes by hand but left the
 * closure Sets `selectedObservers` and `selectedTypes` populated, so
 * "select A, Clear, select B" produced "A,B". It also unchecked the "All" rows.
 * The type change handler also never called updatePacketsUrl(), so with only a
 * type selected the Clear button stayed hidden.
 *
 * The test runs the real packets.js source: the observer/type multi-select
 * section and the Clear handler are sliced out and evaluated in one function
 * scope (the same scope they share in packets.js), together with the real
 * updatePacketsUrl(), against a minimal DOM that parses the menu markup into
 * checkbox objects.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const assert = require('assert');

console.log('--- test-issue-2012-clear-filters-selection.js ---');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

const src = fs.readFileSync(REPO_ROOT + '/public/packets.js', 'utf-8');
function slice(startMarker, endMarker) {
  const start = src.indexOf(startMarker);
  assert(start !== -1, 'marker not found in packets.js: ' + startMarker);
  const end = src.indexOf(endMarker, start);
  assert(end !== -1, 'marker not found in packets.js: ' + endMarker);
  return src.substring(start, end);
}
const multiSelectSrc = slice('// --- Observer multi-select ---', '// --- Channel filter (#812) ---');
const clearSrc = slice('// --- Clear filters button ---', '// Show clear button if page loaded');
// The real updatePacketsUrl(), which shows or hides the Clear button.
const urlSrc = slice('function buildPacketsQuery(', 'let filtersBuilt = false;');
const appSrc = fs.readFileSync(REPO_ROOT + '/public/app.js', 'utf-8');
const hashParamsSrc = appSrc.match(/function getHashParams\(\) \{[\s\S]*?\n\}/)[0];
const initParamsSrc = slice('// Parse ?obs=OBSERVER_ID from routeParam', 'app.innerHTML =');

function makeEl(id) {
  const listeners = {};
  const el = {
    id, value: '', textContent: '', title: '', style: {},
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener(ev, fn) { listeners[ev] = fn; },
    fire(ev, arg) { listeners[ev](arg); },
    _checkboxes: [],
    querySelectorAll(sel) { return sel === 'input[type=checkbox]' ? el._checkboxes : []; },
  };
  Object.defineProperty(el, 'innerHTML', {
    set(html) {
      el._checkboxes = [];
      const re = /<input type="checkbox"([^>]*)>/g;
      let m;
      while ((m = re.exec(html))) {
        const attrs = m[1];
        const dataset = {};
        const obs = /data-obs-id="([^"]*)"/.exec(attrs);
        const typ = /data-type-id="([^"]*)"/.exec(attrs);
        if (obs) dataset.obsId = obs[1];
        if (typ) dataset.typeId = typ[1];
        el._checkboxes.push({ dataset, checked: /\schecked(\s|$)/.test(attrs) });
      }
    },
  });
  return el;
}

function setup(hash = '#/packets', initialFilters = {}) {
  const elements = {};
  // #observerList and #observerSearchInput are not in packets.js yet. The
  // observer search PR (#1884) renders the observer rows into #observerList
  // and wires #observerSearchInput; providing both keeps this test valid
  // whichever of the two lands first.
  for (const id of [
    'observerMenu', 'observerList', 'observerSearchInput', 'observerTrigger',
    'typeMenu', 'typeTrigger', 'clearFiltersBtn',
    'fHash', 'fNode', 'fChannel', 'fTimeWindow', 'fMyNodes',
    'packetFilterInput', 'packetFilterError', 'packetFilterCount',
  ]) elements[id] = makeEl(id);
  elements.clearFiltersBtn.style.display = 'none';

  const storage = {};
  const localStorage = {
    getItem: (k) => (k in storage ? storage[k] : null),
    setItem: (k, v) => { storage[k] = String(v); },
    removeItem: (k) => { delete storage[k]; },
  };
  const document = { getElementById: (id) => elements[id] || null };
  const observers = [{ id: 'obsA', name: 'Observer A' }, { id: 'obsB', name: 'Observer B' }];
  const observerMap = new Map(observers.map((o) => [o.id, o]));
  const SHORT_BY_ID = { 4: 'ADVERT', 5: 'GRP_TXT' };
  const filters = { myNodes: false, ...initialFilters };
  const escapeHtml = (s) => String(s);
  const RegionFilter = { setSelected() {}, getRegionParam: () => '' };
  const location = { hash };
  const history = { replaceState(_state, _title, url) { location.hash = url; } };
  const noop = () => {};

  const run = new Function(
    'filters', 'observers', 'observerMap', 'SHORT_BY_ID', 'escapeHtml', 'document', 'localStorage',
    'RegionFilter', 'renderTableRows', 'loadPackets',
    '_rebuildObserverMenu', '_observerFilterSet', 'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW',
    'location', 'history', 'window', '_packetSortColumn', '_packetSortDirection', 'showFullNames',
    hashParamsSrc + '\n' + urlSrc + '\n' + multiSelectSrc + '\n' + clearSrc + '\n' +
    'updatePacketsUrl(); return { updatePacketsUrl, buildPacketsQuery };'
  );
  const actions = run(filters, observers, observerMap, SHORT_BY_ID, escapeHtml, document, localStorage,
    RegionFilter, noop, noop, null, null, 15, 15,
    location, history, {}, null, null, false);

  // Observer rows live in #observerMenu today and in #observerList after
  // #1884; the change listener stays on #observerMenu in both layouts.
  function boxes(menuId) {
    return menuId === 'observerMenu'
      ? elements.observerMenu._checkboxes.concat(elements.observerList._checkboxes)
      : elements[menuId]._checkboxes;
  }
  function toggle(menuId, attr, id, checked) {
    const cb = boxes(menuId).find((c) => c.dataset[attr] === id);
    assert(cb, `checkbox ${id} not found in #${menuId}`);
    cb.checked = checked;
    elements[menuId].fire('change', { target: cb });
  }
  function allRow(menuId, attr) {
    return boxes(menuId).find((c) => c.dataset[attr] === '__all__');
  }
  return {
    filters, elements, storage, boxes, location, ...actions,
    clear: () => elements.clearFiltersBtn.fire('click'),
    pickObserver: (id) => toggle('observerMenu', 'obsId', id, true),
    pickType: (id) => toggle('typeMenu', 'typeId', id, true),
    observerAllRow: () => allRow('observerMenu', 'obsId'),
    typeAllRow: () => allRow('typeMenu', 'typeId'),
  };
}

test('observer: select A, Clear, select B leaves only B selected', () => {
  const s = setup();
  s.pickObserver('obsA');
  assert.strictEqual(s.filters.observer, 'obsA');
  s.clear();
  assert.strictEqual(s.filters.observer, undefined);
  s.pickObserver('obsB');
  assert.strictEqual(s.filters.observer, 'obsB', 'stale selection survived Clear');
  assert.strictEqual(s.storage['meshcore-observer-filter'], 'obsB');
  assert.strictEqual(s.elements.observerTrigger.textContent, 'Observer B ▾');
  assert.strictEqual(s.observerAllRow().checked, false, 'All Observers row checked while B is selected');
});

test('observer: All Observers row is checked after Clear', () => {
  const s = setup();
  s.pickObserver('obsA');
  assert.strictEqual(s.observerAllRow().checked, false);
  s.clear();
  assert.strictEqual(s.observerAllRow().checked, true, 'All Observers row unchecked after Clear');
  const others = s.boxes('observerMenu').filter((c) => c.dataset.obsId !== '__all__');
  assert(others.length === 2 && others.every((c) => !c.checked), 'an observer row is still checked after Clear');
  assert.strictEqual(s.elements.observerTrigger.textContent, 'All Observers ▾');
});

test('type: select A, Clear, select B leaves only B selected', () => {
  const s = setup();
  s.pickType('4');
  assert.strictEqual(s.filters.type, '4');
  s.clear();
  assert.strictEqual(s.filters.type, undefined);
  s.pickType('5');
  assert.strictEqual(s.filters.type, '5', 'stale selection survived Clear');
  assert.strictEqual(s.storage['meshcore-type-filter'], '5');
  assert.strictEqual(s.elements.typeTrigger.textContent, 'GRP_TXT ▾');
  assert.strictEqual(s.typeAllRow().checked, false, 'All Types row checked while B is selected');
});

test('type: All Types row is checked after Clear', () => {
  const s = setup();
  s.pickType('4');
  assert.strictEqual(s.typeAllRow().checked, false);
  s.clear();
  assert.strictEqual(s.typeAllRow().checked, true, 'All Types row unchecked after Clear');
  const others = s.elements.typeMenu._checkboxes.filter((c) => c.dataset.typeId !== '__all__');
  assert(others.length === 2 && others.every((c) => !c.checked), 'a type row is still checked after Clear');
  assert.strictEqual(s.elements.typeTrigger.textContent, 'All Types ▾');
});

test('type only: Clear button is shown once a type is picked, hidden after Clear', () => {
  const s = setup();
  assert.strictEqual(s.elements.clearFiltersBtn.style.display, 'none');
  s.pickType('4');
  assert.strictEqual(s.filters.type, '4');
  assert.strictEqual(s.elements.clearFiltersBtn.style.display, '', 'Clear button hidden while only a type filter is active');
  s.clear();
  assert.strictEqual(s.elements.clearFiltersBtn.style.display, 'none', 'Clear button still shown after Clear');
});

test('observer only: Clear button is shown once an observer is picked, hidden after Clear', () => {
  const s = setup();
  assert.strictEqual(s.elements.clearFiltersBtn.style.display, 'none');
  s.pickObserver('obsA');
  assert.strictEqual(s.elements.clearFiltersBtn.style.display, '', 'Clear button hidden while only an observer filter is active');
  s.clear();
  assert.strictEqual(s.elements.clearFiltersBtn.style.display, 'none', 'Clear button still shown after Clear');
});

for (const route of ['#/packets/aabbccddeeff0011', '#/packets/id/42']) {
  test('#2091: initial URL rewrite preserves observation on ' + route, () => {
    const s = setup(route + '?obs=123');
    assert.strictEqual(s.location.hash, route + '?obs=123');
    assert.strictEqual(s.elements.clearFiltersBtn.style.display, 'none', 'observation is not a filter');
  });
}

test('#2091: type and observer changes preserve detail selection and canonical hash', () => {
  const route = '#/packets/aabbccddeeff0011';
  const s = setup(route + '?obs=123', { hash: 'aabbccddeeff0011' });
  s.pickType('4');
  s.pickObserver('obsA');
  s.pickObserver('obsB');
  const params = new URLSearchParams(s.location.hash.split('?')[1]);
  assert.strictEqual(params.get('obs'), '123');
  assert.strictEqual(params.getAll('obs').length, 1);
  assert.strictEqual(params.get('observer'), 'obsA,obsB');
  assert.strictEqual(params.has('hash'), false, 'path hash must not be duplicated as a filter');
  assert.strictEqual(s.location.hash.split('?')[0], route);
});

test('#2091: Clear removes filters but keeps the current observation detail', () => {
  const route = '#/packets/aabbccddeeff0011';
  const s = setup(route + '?obs=123', { hash: 'aabbccddeeff0011' });
  s.pickObserver('obsA');
  s.pickType('4');
  s.clear();
  assert.strictEqual(s.location.hash, route + '?obs=123');
  assert.strictEqual(s.elements.clearFiltersBtn.style.display, 'none');
  assert.strictEqual(s.observerAllRow().checked, true);
  assert.strictEqual(s.typeAllRow().checked, true);
});

test('#2091: encoded values survive repeated filter rewrites without duplicate obs', () => {
  const s = setup('#/packets/id/42?obs=row%2B%26%3D%20%3F&obs=discarded', {
    node: 'node +&=', channel: 'channel +&=', _filterExpr: 'name == "A & B"',
  });
  s.pickObserver('obsA');
  s.updatePacketsUrl();
  const params = new URLSearchParams(s.location.hash.split('?')[1]);
  assert.deepStrictEqual(params.getAll('obs'), ['row+&= ?']);
  assert.strictEqual(params.get('node'), s.filters.node);
  assert.strictEqual(params.get('channel'), s.filters.channel);
  assert.strictEqual(params.get('filter'), s.filters._filterExpr);
  assert.strictEqual(params.get('observer'), 'obsA');
});

test('#2091: returning to list or selecting another packet does not resurrect stale obs', () => {
  const s = setup('#/packets/aabbccddeeff0011?obs=123');
  for (const route of ['#/packets', '#/packets?obs=123', '#/packets/1122334455667788']) {
    s.location.hash = route;
    s.pickType('4');
    assert.strictEqual(new URLSearchParams(s.location.hash.split('?')[1]).has('obs'), false, route);
  }
});

test('#2091: list query builder never inherits detail observation', () => {
  const s = setup('#/packets/aabbccddeeff0011?obs=123');
  assert.strictEqual(s.buildPacketsQuery(60, 'region +&=', false), '?timeWindow=60&region=region%20%2B%26%3D');
});

test('#2091: init restores obs from the hash after router strips the query', () => {
  const readInitialObservation = new Function('location', 'routeParam',
    'let directObsId = "stale", directPacketId = null, directPacketHash = null; ' +
    'let savedTimeWindowMin = 15, _pendingUrlRegion = null; const filters = {}, window = {}; ' +
    hashParamsSrc + '\n' + initParamsSrc + '\nreturn directObsId;');
  for (const route of ['aabbccddeeff0011', 'id/42']) {
    assert.strictEqual(readInitialObservation({ hash: '#/packets/' + route + '?obs=123' }, route), '123');
    assert.strictEqual(readInitialObservation({ hash: '#/packets/' + route }, route), null);
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
console.log('All tests passed ✅');
