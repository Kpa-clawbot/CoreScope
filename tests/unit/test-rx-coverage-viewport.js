'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// #2032: exercise the registered RX page, with the real shared URL parser.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const source = fs.readFileSync(path.join(REPO_ROOT, 'public', 'rx-coverage.js'), 'utf8');
const app = fs.readFileSync(path.join(REPO_ROOT, 'public', 'app.js'), 'utf8');
const parser = app.slice(app.indexOf('function parseViewportHash('), app.indexOf("if (typeof window !== 'undefined') { window.parseViewportHash"));
const flush = () => new Promise(resolve => setImmediate(resolve));
function harness(options = {}) {
  let page;
  const maps = [], requests = [], timers = [];
  const storage = { 'rx-coverage-view': options.saved };
  const location = { hash: options.hash || '#/rx-coverage' };
  const context = {
    URLSearchParams, location, Promise, console,
    window: { MC_CLIENT_RX_COVERAGE: true, MeshConfigReady: options.ready },
    document: { getElementById: () => null },
    registerPage: (_, value) => { page = value; },
    getHashParams: () => new URLSearchParams(location.hash.split('?')[1]),
    localStorage: {
      getItem: key => { if (options.blockStorage) throw Error('blocked'); return storage[key]; },
      setItem: (key, value) => { if (options.blockStorage) throw Error('blocked'); storage[key] = value; }
    },
    history: { replaceState: (_, __, hash) => { location.hash = hash; } },
    debounce: fn => fn,
    setTimeout: fn => { timers.push(fn); }, clearTimeout: () => {},
    fetch: url => {
      requests.push(url);
      if (url === '/api/config/map') {
        if (options.configPromise) return options.configPromise;
        if (options.failConfig) return Promise.reject(Error('offline'));
        return Promise.resolve({ json: () => options.config || { center: [12, 34], zoom: 6 } });
      }
      return Promise.resolve({ json: () => ({ features: [], observers: [] }) });
    },
    L: {
      map: () => {
        const m = { events: {}, setView(center, zoom) { this.center = Array.from(center); this.zoom = zoom; return this; },
          on(events, fn) { events.split(' ').forEach(event => { this.events[event] = fn; }); },
          getCenter() { return { lat: this.center[0], lng: this.center[1] }; }, getZoom() { return this.zoom; },
          getBounds() { return { getSouth: () => 0, getWest: () => 0, getNorth: () => 1, getEast: () => 1 }; },
          invalidateSize() {}, remove() { this.removed = true; } };
        maps.push(m); return m;
      },
      tileLayer: () => ({ addTo() {} }), layerGroup: () => ({ addTo() { return this; }, clearLayers() {} })
    }
  };
  vm.runInNewContext(parser + '\n' + source, context);
  return { page, maps, requests, timers, storage, location, init: async () => { page.init({ innerHTML: '' }); await flush(); } };
}
(async () => {
  let h = harness(); await h.init(); assert.deepStrictEqual(h.maps[0].center, [12, 34]); assert.equal(h.maps[0].zoom, 6); assert.equal(h.requests.filter(url => url === '/api/config/map').length, 1);
  h = harness({ saved: JSON.stringify({ lat: 22, lng: 44, zoom: 11 }) }); await h.init(); assert.deepStrictEqual(h.maps[0].center, [22, 44]); assert.equal(h.maps[0].zoom, 11); assert(!h.requests.includes('/api/config/map'));
  h = harness({ hash: '#/rx-coverage?days=14&rx=abcd&lat=0&lon=0&zoom=5', saved: JSON.stringify({ lat: 22, lng: 44, zoom: 11 }) });
  await h.init(); assert.deepStrictEqual(h.maps[0].center, [0, 0]); assert(!h.requests.includes('/api/config/map')); h.timers.forEach(fn => fn()); await flush();
  assert(!h.requests.some(url => url.includes('bbox=-90,-180')), 'explicit URL viewport must not be fitted to observer');
  h.maps[0].setView([40, 50], 10); h.maps[0].events.moveend();
  assert.deepStrictEqual(JSON.parse(h.storage['rx-coverage-view']), { lat: 40, lng: 50, zoom: 10 });
  assert.equal(h.storage['map-view'], undefined, 'coverage must not write the main map\'s saved viewport');
  const params = new URLSearchParams(h.location.hash.split('?')[1]);
  for (const [key, value] of Object.entries({ days: '14', rx: 'abcd', lat: '40.00000', lon: '50.00000', zoom: '10' })) assert.equal(params.get(key), value);
  for (const saved of ['broken', 'null', '{}', '{"lat":12,"lng":34,"zoom":"invalid"}', '{"lat":null,"lng":4,"zoom":8}', '{"lat":91,"lng":4,"zoom":8}', '{"lat":"nope","lng":4,"zoom":8}']) {
    h = harness({ saved }); await h.init(); assert.deepStrictEqual(h.maps[0].center, [12, 34], saved);
  }
  h = harness({ saved: JSON.stringify({ lat: 0, lng: 0, zoom: 4 }) }); await h.init(); assert.deepStrictEqual(h.maps[0].center, [0, 0]);
  h = harness({ hash: '#/rx-coverage?lat=bad&lon=34' }); await h.init(); assert.deepStrictEqual(h.maps[0].center, [12, 34]);
  for (const center of [[999, 34], null, [null, 34], ['bad', 34], []]) {
    h = harness({ config: { center, zoom: 6 } }); await h.init(); assert.deepStrictEqual(h.maps[0].center, [37.6, -122.1]);
  }
  h = harness({ blockStorage: true }); await h.init(); h.maps[0].events.moveend();
  h = harness({ failConfig: true }); await h.init(); assert.deepStrictEqual(h.maps[0].center, [37.6, -122.1]); assert.equal(h.maps[0].zoom, 9);
  h = harness({ hash: '#/rx-coverage?rx=abcd' }); await h.init(); h.timers.forEach(fn => fn()); await flush(); assert(h.requests.some(url => url.includes('bbox=-90,-180')));
  let resolve;
  h = harness({ configPromise: new Promise(r => { resolve = r; }) });
  await h.init(); h.page.destroy(); resolve({ json: () => ({ center: [12, 34], zoom: 6 }) }); await flush(); assert.equal(h.maps.length, 0, 'late config must not recreate a destroyed map');
  let delayed;
  h = harness({ configPromise: new Promise(r => { delayed = r; }) });
  await h.init(); h.page.destroy(); h.page.init({ innerHTML: '' }); await flush();
  delayed({ json: () => ({ center: [12, 34], zoom: 6 }) }); await flush();
  assert.equal(h.maps.length, 1, 'old config response must not create a map after re-entry');
  // A stale 150 ms layout timer must not fetch for the next visit.
  const oldMove = h.maps[0].events.moveend;
  const oldTimer = h.timers[0]; h.page.destroy(); h.page.init({ innerHTML: '' }); await flush();
  const before = h.requests.length; oldTimer(); assert.equal(h.requests.length, before);
  h.maps[1].setView([30, 60], 8);
  const oldHash = h.location.hash, oldSaved = h.storage['rx-coverage-view'];
  oldMove(); assert.equal(h.location.hash, oldHash); assert.equal(h.storage['rx-coverage-view'], oldSaved); assert.equal(h.requests.length, before);
  let ready;
  h = harness({ ready: new Promise(r => { ready = r; }) });
  h.page.init({ innerHTML: '' }); h.page.destroy(); h.page.init({ innerHTML: '' }); ready(); await flush(); assert.equal(h.maps.length, 1, 'old initialization must not survive destroy/re-entry');
  console.log('RX coverage viewport regressions OK');
})().catch(error => { console.error(error); process.exit(1); });
