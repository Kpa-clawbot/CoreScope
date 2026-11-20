/* test-map-tile-pin.js — #2428 saved base-map pick (mc-tile-pinned) and the
 * opt-in default of the new provider groups.
 *
 * Runs the real public/map-tile-providers.js in a vm sandbox:
 *   - eox, osmfr, openfreemap, versatiles, hillshade are off unless
 *     map.tiles.providers.<group>.enabled === true (strictly boolean true);
 *     carto / osm / opentopomap / stamen / usgs / esri keep their rules
 *   - MC_getPinnedTileProvider / MC_setPinnedTileProvider: round trip, an
 *     unregistered or junk stored value reads as nothing, set refuses an
 *     unregistered id, an in-memory copy when localStorage throws, and the
 *     mc-tile-provider-changed event carries detail.pinned
 *   - MC_resolveTile (maps without a picker) follows a pin that suits the
 *     theme ('any' suits both) and ignores one that does not or whose group
 *     is off; MC_applyTileFilter uses the same rule
 *
 * Runs via: node tests/unit/test-map-tile-pin.js
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

const SRC = fs.readFileSync(path.join(REPO_ROOT, 'public', 'map-tile-providers.js'), 'utf8');

function makeStorage(opts) {
  const store = {};
  const o = opts || {};
  return {
    store,
    getItem(k)    { if (o.throwGet) throw new Error('blocked'); return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
    setItem(k, v) { if (o.throwSet) throw new Error('blocked'); store[k] = String(v); },
    removeItem(k) { if (o.throwSet) throw new Error('blocked'); delete store[k]; },
  };
}

const ALL_ON = () => ({ eox: { enabled: true }, osmfr: { enabled: true }, openfreemap: { enabled: true },
  versatiles: { enabled: true }, hillshade: { enabled: true } });

function makeSandbox(mapCfg, storageOpts) {
  const listeners = {};
  const events = [];
  const storage = makeStorage(storageOpts);
  const pane = { style: { filter: '' }, setAttribute() {}, getAttribute() { return null; }, removeAttribute() {} };
  const state = { theme: 'light' };
  const ctx = {
    console: { log: console.log, error: console.error, warn() {} },
    JSON, Date, Math, Object, Array, String, Number, Boolean, isFinite,
    document: {
      documentElement: { getAttribute: (k) => (k === 'data-theme' ? state.theme : null) },
      querySelector: (sel) => (sel === '.leaflet-tile-pane' ? pane : null),
      createElement: () => ({ style: {}, appendChild() {}, setAttribute() {} }),
    },
    window: {
      addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
      removeEventListener() {},
      dispatchEvent(ev) { events.push(ev); (listeners[ev.type] || []).slice().forEach(fn => fn(ev)); return true; },
      matchMedia: () => ({ matches: false }),
      localStorage: storage,
    },
    CustomEvent: function (type, init) { this.type = type; this.detail = (init && init.detail) || null; },
  };
  ctx.localStorage = storage;
  ctx.L = ctx.window.L = {};
  vm.createContext(ctx);
  ctx.window.document = ctx.document;
  ctx.window.MC_MAP_CFG = mapCfg !== undefined ? mapCfg : { tiles: { providers: ALL_ON() } };
  vm.runInContext(SRC, ctx);
  for (const k of Object.keys(ctx.window)) if (!(k in ctx)) ctx[k] = ctx.window[k];
  const w = ctx.window;
  return {
    w, storage, events, pane, state,
    reg: () => w.MC_TILE_PROVIDERS,
    load(cfg) { w.MC_MAP_CFG = cfg; w.MC_initTileRegistry(false); return w.MC_TILE_PROVIDERS; },
    // The pane filter MC_applyTileFilter sets for the current theme.
    filter() { w.MC_applyTileFilter(); return pane.style.filter; },
  };
}

const GROUP_IDS = {
  eox: ['eox-s2-2025', 'eox-s2-2016', 'eox-terrain-light'],
  osmfr: ['osmfr-hot', 'osmfr-cyclosm', 'osmfr-gray', 'osmfr-gray-dark'],
  openfreemap: ['openfreemap-liberty', 'openfreemap-bright', 'openfreemap-positron', 'openfreemap-dark', 'openfreemap-fiord'],
  versatiles: ['versatiles-colorful', 'versatiles-muted', 'versatiles-gray', 'versatiles-colorful-dark', 'versatiles-gray-dark'],
};
const OVERLAY_IDS = { eox: ['eox-labels', 'eox-labels-dark'] };
const overlays = (s) => Object.keys(s.w.MC_TILE_OVERLAYS || {});

console.log('── #2428 opt-in groups ──');

for (const group of Object.keys(GROUP_IDS)) {
  test(group + ': absent, {}, enabled:false and enabled:"true" are off (no base entry, no overlay)', () => {
    const s = makeSandbox(null);
    const cases = [null, { tiles: {} }, { tiles: { providers: {} } }, { tiles: { providers: { [group]: {} } } },
      { tiles: { providers: { [group]: { enabled: false } } } }, { tiles: { providers: { [group]: { enabled: 'true' } } } },
      { tiles: { providers: { [group]: { enabled: 1 } } } }];
    for (const cfg of cases) {
      const reg = s.load(cfg);
      for (const id of GROUP_IDS[group]) assert.ok(!reg[id], id + ' off for ' + JSON.stringify(cfg));
      for (const id of (OVERLAY_IDS[group] || [])) assert.ok(overlays(s).indexOf(id) < 0, id + ' off for ' + JSON.stringify(cfg));
    }
  });
  test(group + ': enabled:true registers it, and nothing from the other groups', () => {
    const s = makeSandbox(null);
    const reg = s.load({ tiles: { providers: { [group]: { enabled: true } } } });
    for (const id of GROUP_IDS[group]) assert.ok(reg[id], id + ' on');
    for (const id of (OVERLAY_IDS[group] || [])) assert.ok(overlays(s).indexOf(id) >= 0, id + ' overlay on');
    for (const other of Object.keys(GROUP_IDS)) {
      if (other !== group) for (const id of GROUP_IDS[other]) assert.ok(!reg[id], id + ' still off');
    }
  });
}

test('hillshade: no overlay unless providers.hillshade.enabled === true (the factory is loaded)', () => {
  const s = makeSandbox(null);
  s.w.MC_createHillshadeLayer = () => ({ on() { return this; } });
  for (const v of [undefined, {}, { enabled: false }, { enabled: 'true' }]) {
    s.load({ tiles: { providers: v === undefined ? {} : { hillshade: v } } });
    assert.ok(overlays(s).indexOf('hillshade') < 0, 'off for ' + JSON.stringify(v));
  }
  s.load({ tiles: { providers: { hillshade: { enabled: true } } } });
  assert.ok(overlays(s).indexOf('hillshade') >= 0, 'on for enabled:true');
});

test('existing groups are unchanged: carto on by default, esri always, osm/usgs/opentopomap opt in', () => {
  const s = makeSandbox(null);
  let reg = s.reg();
  assert.ok(reg['carto-dark'] && reg['carto-light'] && reg['esri-darkgray-labels']);
  for (const id of ['osm-standard', 'usgs-topo', 'usgs-imagery', 'opentopomap']) assert.ok(!reg[id], id + ' off by default');
  reg = s.load({ tiles: { providers: { osm: { enabled: true }, usgs: { enabled: true }, opentopomap: { enabled: true },
    carto: { enabled: false } } } });
  assert.ok(reg['osm-standard'] && reg['usgs-topo'] && reg['usgs-imagery'] && reg.opentopomap, 'opt-in groups on');
  assert.ok(!reg['carto-dark'] && reg['esri-darkgray-labels'], 'carto opt-out, esri stays');
});

console.log('── #2428 saved pick (mc-tile-pinned) ──');

test('pin: get/set round trip, stored under mc-tile-pinned; null clears it', () => {
  const s = makeSandbox();
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), null, 'absent = Auto');
  assert.strictEqual(s.w.MC_setPinnedTileProvider('osmfr-hot'), true);
  assert.strictEqual(s.storage.store['mc-tile-pinned'], 'osmfr-hot');
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), 'osmfr-hot');
  assert.strictEqual(s.w.MC_setPinnedTileProvider(null), true);
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), null);
  assert.ok(!('mc-tile-pinned' in s.storage.store), 'key removed');
});

test('pin: an unregistered or junk stored value reads as nothing', () => {
  const s = makeSandbox();
  const junk = ['nope', '', '<img src=x onerror=1>', '__proto__', 'constructor', 'toString', 'hasOwnProperty',
    'osm-standard' /* registered only when its group is on */, '["osmfr-hot"]', ' osmfr-hot', 'OSMFR-HOT', '{"a":1}', '0', 'null'];
  for (const v of junk) {
    s.storage.store['mc-tile-pinned'] = v;
    assert.strictEqual(s.w.MC_getPinnedTileProvider(), null, JSON.stringify(v));
  }
  s.storage.store['mc-tile-pinned'] = 'osmfr-hot';
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), 'osmfr-hot');
});

test('pin: a stored pin of a group that is switched off reads as nothing, and comes back with the group', () => {
  const s = makeSandbox();
  s.w.MC_setPinnedTileProvider('osmfr-hot');
  s.load({ tiles: { providers: {} } });
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), null, 'group off');
  assert.strictEqual(s.storage.store['mc-tile-pinned'], 'osmfr-hot', 'the saved value is kept');
  s.load({ tiles: { providers: ALL_ON() } });
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), 'osmfr-hot', 'group back on');
});

test('pin: set refuses an unregistered id (false, nothing stored, no event)', () => {
  const s = makeSandbox();
  for (const bad of ['nope', '', 'osm-standard', '__proto__', 'toString', undefined, 42, {}, 'custom-x']) {
    assert.strictEqual(s.w.MC_setPinnedTileProvider(bad), false, String(bad));
  }
  assert.ok(!('mc-tile-pinned' in s.storage.store));
  assert.strictEqual(s.events.length, 0, 'no event');
  // A group that is off cannot be pinned either.
  s.load({ tiles: { providers: {} } });
  assert.strictEqual(s.w.MC_setPinnedTileProvider('eox-s2-2025'), false);
});

test('pin: a throwing localStorage keeps an in-memory copy', () => {
  for (const opts of [{ throwGet: true, throwSet: true }, { throwSet: true }]) {
    const s = makeSandbox(undefined, opts);
    assert.strictEqual(s.w.MC_getPinnedTileProvider(), null);
    assert.strictEqual(s.w.MC_setPinnedTileProvider('osmfr-hot'), true, 'the set still succeeds');
    if (opts.throwGet) {
      assert.strictEqual(s.w.MC_getPinnedTileProvider(), 'osmfr-hot', 'memo wins when reads throw');
      assert.strictEqual(s.w.MC_resolveTile(false).id, 'osmfr-hot', 'and MC_resolveTile follows it');
      assert.strictEqual(s.w.MC_setPinnedTileProvider(null), true);
      assert.strictEqual(s.w.MC_getPinnedTileProvider(), null, 'cleared in memory');
    } else {
      // Reads work but writes are blocked: nothing was stored, nothing breaks.
      assert.doesNotThrow(() => s.w.MC_getPinnedTileProvider());
    }
  }
});

test('pin: every set dispatches mc-tile-provider-changed with detail.pinned', () => {
  const s = makeSandbox();
  const seen = [];
  s.w.addEventListener('mc-tile-provider-changed', (e) => seen.push(e.detail));
  s.w.MC_setPinnedTileProvider('osmfr-hot');
  s.w.MC_setPinnedTileProvider(null);
  assert.deepStrictEqual(seen.map(d => d.pinned), ['osmfr-hot', null]);
  assert.deepStrictEqual(s.events.map(e => e.type), ['mc-tile-provider-changed', 'mc-tile-provider-changed']);
});

console.log('── #2428 MC_resolveTile / MC_applyTileFilter follow the pin ──');

test('resolveTile: a pin that suits the theme is used, in both themes', () => {
  const s = makeSandbox();
  s.w.MC_setPinnedTileProvider('osmfr-hot'); // light
  const light = s.w.MC_resolveTile(false);
  assert.strictEqual(light.id, 'osmfr-hot');
  assert.ok(light.url.indexOf('openstreetmap.fr/hot') >= 0, light.url);
  s.w.MC_setPinnedTileProvider('carto-dark');
  s.w.MC_setDarkTileProvider('esri-darkgray-labels'); // the theme's own dark choice
  assert.strictEqual(s.w.MC_resolveTile(true).id, 'carto-dark', 'the pin beats the theme choice');
});

test('resolveTile: a pin that does not suit the theme is ignored (light pin in dark, dark pin in light)', () => {
  const s = makeSandbox();
  const baseLight = s.w.MC_resolveTile(false);
  const baseDark = s.w.MC_resolveTile(true);
  s.w.MC_setPinnedTileProvider('osmfr-hot');
  assert.strictEqual(s.w.MC_resolveTile(true).id, baseDark.id, 'light pin in dark');
  assert.strictEqual(s.w.MC_resolveTile(true).url, baseDark.url);
  s.w.MC_setPinnedTileProvider('carto-dark');
  assert.strictEqual(s.w.MC_resolveTile(false).id, baseLight.id, 'dark pin in light');
  assert.strictEqual(s.w.MC_resolveTile(false).url, baseLight.url);
});

test("resolveTile: an 'any' pin (satellite) fits both themes", () => {
  const s = makeSandbox();
  s.w.MC_setPinnedTileProvider('eox-s2-2025');
  for (const dark of [false, true]) {
    const d = s.w.MC_resolveTile(dark);
    assert.strictEqual(d.id, 'eox-s2-2025', 'dark=' + dark);
    assert.strictEqual(d.maxNativeZoom, 15);
    assert.ok(d.url.indexOf('tiles.maps.eox.at') >= 0);
  }
});

test('resolveTile: no pin, junk pin, or a pin of a disabled group gives the theme choice as before', () => {
  const s = makeSandbox();
  const base = [s.w.MC_resolveTile(false).id, s.w.MC_resolveTile(true).id];
  assert.deepStrictEqual(base, ['carto-light', 'carto-dark']);
  s.storage.store['mc-tile-pinned'] = 'nope';
  assert.deepStrictEqual([s.w.MC_resolveTile(false).id, s.w.MC_resolveTile(true).id], base, 'junk');
  s.w.MC_setPinnedTileProvider('eox-s2-2025');
  s.load({ tiles: { providers: {} } }); // eox now off
  assert.deepStrictEqual([s.w.MC_resolveTile(false).id, s.w.MC_resolveTile(true).id], base, 'disabled group');
  s.w.MC_setPinnedTileProvider(null);
  s.load({ tiles: { providers: ALL_ON() } });
  assert.deepStrictEqual([s.w.MC_resolveTile(false).id, s.w.MC_resolveTile(true).id], base, 'cleared');
});

test('applyTileFilter: the pane filter follows a fitting pin and ignores a non-fitting one', () => {
  const s = makeSandbox();
  s.state.theme = 'dark';
  const plain = s.filter(); // carto-dark: its own (empty or inverting) filter
  s.w.MC_setPinnedTileProvider('osmfr-gray-dark');
  assert.ok(/grayscale\(/.test(s.filter()), 'dark gray pin applies its filter: ' + s.filter());
  s.w.MC_setPinnedTileProvider('osmfr-gray'); // light gray pin in dark: ignored
  assert.strictEqual(s.filter(), plain, 'light pin in dark leaves the dark choice');
  s.w.MC_setPinnedTileProvider('eox-s2-2025'); // any: never inverted
  assert.strictEqual(s.filter(), '', 'satellite pin has no filter');
  s.state.theme = 'light';
  s.w.MC_setPinnedTileProvider('osmfr-gray');
  assert.ok(/grayscale\(/.test(s.filter()), 'light gray pin in light');
  s.w.MC_setPinnedTileProvider(null);
  assert.strictEqual(s.filter(), '', 'Auto: carto-light has no filter');
});

test('resolveTile { ignorePin }: the theme map even with a fitting pin (the Auto layer of a picker map)', () => {
  const s = makeSandbox();
  s.w.MC_setPinnedTileProvider('eox-s2-2025');
  assert.strictEqual(s.w.MC_resolveTile(false).id, 'eox-s2-2025', 'pin used by default');
  assert.strictEqual(s.w.MC_resolveTile(false, { ignorePin: true }).id, 'carto-light', 'ignored on request');
  assert.strictEqual(s.w.MC_resolveTile(true, { ignorePin: true }).id, 'carto-dark');
});

test('applyTileFilter: a picker map pane (data-mc-picker) ignores the pin', () => {
  const s = makeSandbox();
  s.state.theme = 'dark';
  const plain = s.filter();
  s.w.MC_setPinnedTileProvider('osmfr-gray-dark');
  const pickerPane = { style: { filter: 'x' }, getAttribute: (k) => (k === 'data-mc-picker' ? 'true' : null) };
  s.w.MC_applyTileFilter(pickerPane);
  assert.strictEqual(pickerPane.style.filter, plain, 'picker pane keeps the theme filter: ' + pickerPane.style.filter);
  assert.ok(/grayscale\(/.test(s.filter()), 'a pane without a picker follows the pin');
});

test('a failed write (quota full) never brings back the older stored pin', () => {
  const s = makeSandbox();
  s.storage.store['mc-tile-pinned'] = 'osmfr-hot'; // an older pin, still stored
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), 'osmfr-hot');
  s.storage.setItem = () => { throw new Error('QuotaExceededError'); };
  s.storage.removeItem = () => { throw new Error('QuotaExceededError'); };
  assert.strictEqual(s.w.MC_setPinnedTileProvider('eox-s2-2025'), true);
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), 'eox-s2-2025', 'the new pick, not the stored older one');
  s.w.MC_setPinnedTileProvider(null);
  assert.strictEqual(s.w.MC_getPinnedTileProvider(), null, 'Auto, not the stored older one');
});

console.log('map tile pin: ' + passed + ' passed, ' + failed + ' failed');
process.exit(failed ? 1 : 0);
