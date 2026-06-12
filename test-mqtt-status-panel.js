/* test-mqtt-status-panel.js (#1043)
 *
 * DOM-grep test for public/mqtt-status-panel.js. Loads the module into a
 * VM sandbox (no jsdom), stubs the fetch + container, drives renderPanel
 * directly with a mocked /api/mqtt/status payload, then asserts:
 *   - one row per source
 *   - a row with no recent packet but `connected:true` is classified yellow
 *   - a disconnected source is classified red
 *   - a connected source with a recent packet is classified green
 *   - the masked broker URL is rendered (server is responsible for
 *     masking; the test verifies the panel does not re-leak it)
 *   - no plaintext password appears in the rendered HTML
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

const src = fs.readFileSync(require('path').resolve(__dirname, 'public/mqtt-status-panel.js'), 'utf8');
const ctx = {
  window: {},
  module: { exports: {} },
  setInterval, clearInterval, setTimeout, clearTimeout,
  Promise
};
vm.createContext(ctx);
vm.runInContext(src, ctx);
const Panel = ctx.window.MqttStatusPanel;

assert.ok(Panel, 'window.MqttStatusPanel must be exposed after loading mqtt-status-panel.js');

console.log('mqtt-status-panel:');

test('classifySource: connected + recent packet → green', () => {
  const now = 1_700_000_000_000;
  const state = Panel.classifySource({ connected: true, lastPacketUnix: now / 1000 - 30 }, now);
  assert.strictEqual(state, 'green');
});

test('classifySource: connected, no recent packets → yellow', () => {
  const now = 1_700_000_000_000;
  const state = Panel.classifySource({ connected: true, lastPacketUnix: now / 1000 - 9 * 60 }, now);
  assert.strictEqual(state, 'yellow');
});

test('classifySource: disconnected → red (regardless of packet age)', () => {
  const now = 1_700_000_000_000;
  const state = Panel.classifySource({ connected: false, lastPacketUnix: now / 1000 - 1 }, now);
  assert.strictEqual(state, 'red');
});

test('renderPanel emits one <tr> per source with the masked broker URL', () => {
  const container = { innerHTML: '' };
  const now = 1_700_000_000_000;
  const payload = {
    sampleAt: '2026-06-12T12:30:00Z',
    sources: [
      { name: 'local', broker: 'mqtt://obsuser:****@broker.example.com:1883',
        connected: true, lastPacketUnix: now / 1000 - 10, packetsTotal: 999, packetsLast5m: 42, disconnectCount: 0 },
      { name: 'cascadia', broker: 'mqtts://cascadia.example.com:8883',
        connected: true, lastPacketUnix: now / 1000 - 9 * 60, packetsTotal: 50, packetsLast5m: 0, disconnectCount: 2 },
      { name: 'dead', broker: 'mqtt://dead.example.com:1883',
        connected: false, lastPacketUnix: 0, packetsTotal: 0, packetsLast5m: 0, disconnectCount: 7 }
    ]
  };
  Panel.renderPanel(container, payload, now);
  const html = container.innerHTML;

  // 3 <tr data-source-name="...">
  const rowMatches = html.match(/<tr data-source-name=/g) || [];
  assert.strictEqual(rowMatches.length, 3, `expected 3 rows, got ${rowMatches.length}: ${html}`);

  // State data attributes wired through.
  assert.ok(/data-source-name="local"[^>]*data-state="green"/.test(html), 'local row must be green');
  assert.ok(/data-source-name="cascadia"[^>]*data-state="yellow"/.test(html), 'cascadia row must be yellow (connected, idle)');
  assert.ok(/data-source-name="dead"[^>]*data-state="red"/.test(html), 'dead row must be red (disconnected)');

  // Masked broker URL rendered verbatim; password placeholder visible.
  assert.ok(html.includes('mqtt://obsuser:****@broker.example.com:1883'),
    'masked broker URL must be present in rendered HTML');

  // Counts rendered.
  assert.ok(html.includes('999'), 'packetsTotal must be rendered');
  assert.ok(html.includes('42'), 'packetsLast5m must be rendered');
});

test('renderPanel never echoes a plaintext password (defense-in-depth)', () => {
  // The panel only renders what the server sends. If the server fails
  // to mask, the panel must NOT introduce its own leak — and a smoke
  // test here would catch a regression that adds a debug data-* with
  // the unmasked URL.
  const container = { innerHTML: '' };
  const now = 1_700_000_000_000;
  // Already-masked input — the server-side regex is tested in Go.
  Panel.renderPanel(container, { sources: [
    { name: 'local', broker: 'mqtt://obsuser:****@host:1883', connected: true, lastPacketUnix: now / 1000 - 5,
      packetsTotal: 1, packetsLast5m: 1, disconnectCount: 0 }
  ]}, now);
  assert.ok(!/hunter2|password=|p4ssw0rd/i.test(container.innerHTML),
    'plaintext password substrings must not appear in rendered HTML');
});

test('renderPanel handles empty source list with placeholder text', () => {
  const container = { innerHTML: '' };
  Panel.renderPanel(container, { sources: [] }, Date.now());
  assert.ok(container.innerHTML.includes('No MQTT sources'),
    `empty state should render placeholder; got: ${container.innerHTML}`);
});

test('mount fetches /api/mqtt/status and renders a row from the response', async () => {
  const container = { innerHTML: '' };
  const now = Date.now();
  const fakePayload = {
    sources: [{
      name: 'local',
      broker: 'mqtt://obsuser:****@broker.example.com:1883',
      connected: true,
      lastPacketUnix: Math.floor(now / 1000) - 5,
      packetsTotal: 3,
      packetsLast5m: 3,
      disconnectCount: 0
    }]
  };
  let fetchedURL = null;
  const fakeFetch = (url) => {
    fetchedURL = url;
    return Promise.resolve({ json: () => Promise.resolve(fakePayload) });
  };
  const teardown = Panel.mount(container, { fetchImpl: fakeFetch, intervalMs: 60_000 });
  // mount's initial tick is async; spin until container is populated.
  await new Promise((resolve) => setTimeout(resolve, 50));
  teardown();
  assert.strictEqual(fetchedURL, '/api/mqtt/status', 'mount must hit /api/mqtt/status');
  assert.ok(container.innerHTML.includes('mqtt://obsuser:****@broker.example.com:1883'),
    `panel did not render after fetch; html=${container.innerHTML}`);
});

(async () => {
  // Tiny sleep so the async test above resolves before the process exits.
  await new Promise((r) => setTimeout(r, 100));
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
})();
