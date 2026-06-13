/* test-issue-1697-mqtt-mobile-e2e.js (#1697)
 *
 * Asserts that public/mqtt-status-panel.js renders a *card* layout at
 * mobile viewports (≤640px) instead of the 7-column desktop table that
 * overflows 375px screens with cells running into each other.
 *
 * VM-sandbox unit test (matches the style of test-mqtt-status-panel.js)
 * — no jsdom, no playwright. window.innerWidth is stubbed and the
 * panel's renderPanel() is driven directly with a fixed payload.
 *
 * Failure on current master (before #1697 fix) is the RED commit gate.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  \u2705 ${name}`); }
  catch (e) { failed++; console.log(`  \u274C ${name}: ${e.message}`); }
}

function loadPanel(innerWidth) {
  // Build a minimal container that supports `.innerHTML = '...'`
  // assignment AND simple `.querySelector` / `.querySelectorAll` so the
  // test can inspect the rendered DOM without jsdom.
  const container = {
    _html: '',
    get innerHTML() { return this._html; },
    set innerHTML(v) { this._html = String(v); }
  };
  const win = { innerWidth: innerWidth, addEventListener: function () {}, removeEventListener: function () {} };
  const ctx = {
    window: win,
    document: { createElement: function () { return {}; } },
    module: { exports: {} },
    setInterval, clearInterval, setTimeout, clearTimeout,
    Promise
  };
  vm.createContext(ctx);
  const src = fs.readFileSync(path.resolve(__dirname, 'public/mqtt-status-panel.js'), 'utf8');
  vm.runInContext(src, ctx);
  return { Panel: ctx.window.MqttStatusPanel, container: container, win: win };
}

const NOW = 1_700_000_000_000;
const PAYLOAD = {
  sources: [
    { name: 'gomesh',  broker: 'wss://mqtt.gomesh.dev',     connected: true,  lastPacketUnix: NOW / 1000 - 27,  packetsLast5m: 27, packetsTotal: 1247, disconnectCount: 0 },
    { name: 'flood',   broker: 'wss://mqtt.flood.example',  connected: false, lastPacketUnix: 0,                packetsLast5m: 0,  packetsTotal: 88,   disconnectCount: 4 }
  ]
};

console.log('issue-1697 — MQTT panel mobile cards:');

test('mobile 375px: renders cards (no desktop table)', () => {
  const { Panel, container } = loadPanel(375);
  assert.ok(Panel, 'MqttStatusPanel must be exposed');
  Panel.renderPanel(container, PAYLOAD, NOW);
  const html = container.innerHTML;
  assert.ok(!/<table[^>]*class="mqtt-status-table"/.test(html),
    'must NOT render <table class="mqtt-status-table"> at 375px; got: ' + html.slice(0, 400));
  assert.ok(/class="mqtt-status-card"/.test(html),
    'must render .mqtt-status-card elements at 375px');
});

test('desktop 1200px: renders the existing 7-column table', () => {
  const { Panel, container } = loadPanel(1200);
  Panel.renderPanel(container, PAYLOAD, NOW);
  const html = container.innerHTML;
  assert.ok(/<table[^>]*class="mqtt-status-table"/.test(html),
    'must render <table class="mqtt-status-table"> at 1200px');
  // 7 columns: Source / Broker / State / Last packet / 5m / Total / Disc.
  const headerCount = (html.match(/<th[\s>]/g) || []).length;
  assert.strictEqual(headerCount, 7, 'expected 7 <th> headers, got ' + headerCount);
});

test('mobile 375px: every card surfaces all 7 data points', () => {
  const { Panel, container } = loadPanel(375);
  Panel.renderPanel(container, PAYLOAD, NOW);
  const html = container.innerHTML;
  // Two sources × 7 data points must all appear in the cards.
  // Per source: name, broker, state, last-relative, 5m count, total count, disc count.
  const cardCount = (html.match(/class="mqtt-status-card"/g) || []).length;
  assert.strictEqual(cardCount, 2, 'expected 2 cards (one per source), got ' + cardCount);

  // Source 1 — gomesh
  assert.ok(html.includes('gomesh'),               'gomesh name missing');
  assert.ok(html.includes('wss://mqtt.gomesh.dev'),'gomesh broker missing');
  assert.ok(html.includes('connected'),            'gomesh state missing');
  assert.ok(/27s ago/.test(html),                  'gomesh last-packet relative missing');
  // numeric counters (5m=27 / Total=1247 / Disc=0) — make sure each is present.
  assert.ok(/1247/.test(html),                     'gomesh packetsTotal=1247 missing');
  assert.ok(/Total/.test(html),                    'Total label missing');
  assert.ok(/5m/.test(html),                       '5m label missing');
  assert.ok(/Disc/.test(html),                     'Disc label missing');

  // Source 2 — flood (disconnected, 4 disconnects, 88 total)
  assert.ok(html.includes('flood'),                'flood name missing');
  assert.ok(html.includes('disconnected'),         'flood disconnected state missing');
  assert.ok(/\b88\b/.test(html),                   'flood packetsTotal=88 missing');
  assert.ok(/\b4\b/.test(html),                    'flood disconnectCount=4 missing');
});

test('mobile 375px: no fixed-width / min-width styles that would force horizontal overflow', () => {
  const { Panel, container } = loadPanel(375);
  Panel.renderPanel(container, PAYLOAD, NOW);
  const html = container.innerHTML;
  // The card markup must not declare any width >= 400px (would overflow 375).
  // Catch literal `width:NNNpx` / `min-width:NNNpx` ≥ 400.
  const widthHits = (html.match(/(?:min-)?width:\s*(\d{3,})px/g) || [])
    .map(function (s) { return parseInt(s.match(/(\d{3,})/)[1], 10); })
    .filter(function (n) { return n >= 400; });
  assert.deepStrictEqual(widthHits, [], 'card layout must not declare widths >=400px; got: ' + widthHits.join(','));
});

test('renderPanel reads window.innerWidth (regression: layout must be viewport-aware)', () => {
  // Same Panel instance, two viewports, two different outputs proves
  // the renderer is viewport-aware (not statically table-only).
  const m = loadPanel(375);
  const d = loadPanel(1200);
  m.Panel.renderPanel(m.container, PAYLOAD, NOW);
  d.Panel.renderPanel(d.container, PAYLOAD, NOW);
  assert.notStrictEqual(m.container.innerHTML, d.container.innerHTML,
    'renderPanel output at 375px and 1200px must differ');
});

console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);
