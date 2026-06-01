'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

// Simple DOM/Browser Sandbox
const ctx = {
  window: { addEventListener: () => {}, dispatchEvent: () => {}, devicePixelRatio: 1 },
  document: {
    readyState: 'complete',
    createElement: (tag) => ({
      tagName: tag, id: '', textContent: '', innerHTML: '', style: {},
      classList: { add() {}, remove() {}, contains() { return false; } },
      setAttribute() {}, getAttribute() { return null; },
      addEventListener() {}, focus() {},
      getContext: () => ({
        clearRect() {}, fillRect() {}, beginPath() {}, arc() {}, fill() {},
        scale() {}, fillStyle: '', font: '', fillText() {}, stroke() {},
      }),
      offsetWidth: 200, offsetHeight: 40, width: 0, height: 0,
    }),
    head: { appendChild: () => {} },
    getElementById: () => null,
    addEventListener: () => {},
    querySelectorAll: () => [],
    querySelector: () => null,
    createElementNS: () => ({
      tagName: 'svg', id: '', textContent: '', innerHTML: '', style: {},
      setAttribute() {}, getAttribute() { return null; },
    }),
    documentElement: { getAttribute: () => null, setAttribute: () => {} },
    body: { appendChild: () => {}, removeChild: () => {}, contains: () => false },
    hidden: false,
  },
  console,
  Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
  Error, TypeError, Map, Set, Promise, URLSearchParams,
  parseInt, parseFloat, isNaN, isFinite,
  encodeURIComponent, decodeURIComponent,
  setTimeout: () => 0, clearTimeout: () => {},
  setInterval: () => 0, clearInterval: () => {},
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  performance: { now: () => Date.now() },
  requestAnimationFrame: (cb) => {},
  cancelAnimationFrame: () => {},
  getComputedStyle: () => ({ getPropertyValue: () => '' }),
  matchMedia: () => ({ matches: false, addEventListener: () => {} }),
  navigator: {},
  localStorage: { getItem: () => null, setItem: () => {}, removeItem: () => {} },
  sessionStorage: { getItem: () => null, setItem: () => {}, removeItem: () => {} },
  registerPage: () => {},
  visualViewport: null,
  MutationObserver: function() { this.observe = () => {}; this.disconnect = () => {}; },
  WebSocket: function() { this.close = () => {}; },
  IATA_COORDS_GEO: {},
  L: {
    layerGroup: () => ({ addTo: () => {}, removeLayer: () => {}, clearLayers: () => {} }),
    map: () => ({ on: () => {}, remove: () => {} }),
    circleMarker: () => ({ addTo: () => {}, on: () => {}, setRadius: () => {}, setStyle: () => {} })
  }
};
vm.createContext(ctx);

const src = fs.readFileSync('public/live.js', 'utf8');
vm.runInContext(src, ctx);

try {
  console.log("Running dt-cap test...");

  const stepPulse = ctx.window._liveStepPulse;
  assert.ok(stepPulse, "_liveStepPulse must be exported");

  const pulse = {
    lastPulse: null,
    r: 10,
    op: 1.0,
    hl_r: 15,
    hl_op: 1.0,
    hl_weight: 2,
    color: '#ff0000',
    latlng: { lat: 0, lng: 0 }
  };

  // 1. Initial tick to set lastPulse
  stepPulse(pulse, 1000, false, 'LIVE', 1);
  assert.strictEqual(pulse.lastPulse, 1000, "lastPulse should be initialized");

  // 2. Simulate a massive time gap (e.g., 5 seconds)
  const expectedMaxDtSec = 32 / 1000;
  const expectedRIncrease = 58 * expectedMaxDtSec;

  const initialR = pulse.r;
  stepPulse(pulse, 6000, false, 'LIVE', 1); // 5000ms later

  const actualRIncrease = pulse.r - initialR;

  console.log(`Time elapsed: 5000ms. Expected radius increase: ~${expectedRIncrease.toFixed(4)}. Actual: ${actualRIncrease.toFixed(4)}`);

  assert.ok(
    Math.abs(actualRIncrease - expectedRIncrease) < 0.001,
    `Regression! Pulse grew by ${actualRIncrease} instead of capped ${expectedRIncrease}`
  );

  console.log("Test passed: dt is correctly capped to 32ms!");
} catch (err) {
  console.error("Test failed!");
  console.error(err);
  process.exit(1);
}
