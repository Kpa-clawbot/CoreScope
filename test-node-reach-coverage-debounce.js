'use strict';
// Unit test for #6: pan/zoom coverage redraws must be debounced so dragging the
// map fires at most one /api/...rx-coverage request per settle, not one per
// moveend. We load node-reach-coverage.js in a vm sandbox with controllable
// timers + the real debounce, bind the layer, fire a burst of map events, then
// advance the fake clock and assert exactly one extra fetch happened.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(__dirname, 'public', 'node-reach-coverage.js'), 'utf8');

// Controllable timer queue.
let now = 0;
let nextId = 1;
let timers = [];
function setTimeoutStub(fn, ms) { const id = nextId++; timers.push({ id: id, fn: fn, at: now + (ms || 0) }); return id; }
function clearTimeoutStub(id) { timers = timers.filter(function (t) { return t.id !== id; }); }
function advance(ms) {
  now += ms;
  const due = timers.filter(function (t) { return t.at <= now; });
  timers = timers.filter(function (t) { return t.at > now; });
  due.forEach(function (t) { t.fn(); });
}

let fetchCount = 0;
function fakeFetch() {
  fetchCount++;
  // Chainable stub whose then/catch never invoke callbacks (we only count calls).
  const chain = { then: function () { return chain; }, catch: function () { return chain; } };
  return chain;
}

const fakeGroup = { addTo: function () { return fakeGroup; }, clearLayers: function () {}, };
const map = {
  handlers: {},
  on: function (ev, fn) { this.handlers[ev] = fn; },
  off: function () {},
  getBounds: function () { return { getSouth: function () { return 0; }, getWest: function () { return 0; }, getNorth: function () { return 1; }, getEast: function () { return 1; } }; },
  getZoom: function () { return 10; },
  removeLayer: function () {},
};

const sandbox = {
  window: {},
  console: { warn: function () {} },
  setTimeout: setTimeoutStub,
  clearTimeout: clearTimeoutStub,
  fetch: fakeFetch,
  L: { layerGroup: function () { return fakeGroup; } },
  getComputedStyle: function () { return { getPropertyValue: function () { return ''; } }; },
  document: { documentElement: {} },
};
vm.createContext(sandbox);
// Define debounce IN the context so it uses the controllable timers above.
vm.runInContext('function debounce(fn, ms){var t; return function(){var a=arguments, c=this; clearTimeout(t); t=setTimeout(function(){fn.apply(c,a);}, ms);};}', sandbox);
vm.runInContext(code, sandbox);

const handle = sandbox.window.NodeReachCoverage.addLayer(map, 'aabbccddeeff');
assert.strictEqual(fetchCount, 1, 'addLayer should fetch once initially');

// Burst of pan/zoom events.
const fire = map.handlers['moveend zoomend'];
assert.strictEqual(typeof fire, 'function', 'moveend/zoomend handler must be bound');
for (let i = 0; i < 6; i++) fire();
assert.strictEqual(fetchCount, 1, 'burst must not fetch immediately (debounced)');

advance(200);
assert.strictEqual(fetchCount, 2, 'after settle, exactly one coalesced fetch (got ' + fetchCount + ')');

handle.off();
console.log('node-reach-coverage debounce OK');
