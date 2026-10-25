'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.resolve(__dirname, '../..');

// Run the real side-pane renderer. Stop at its DOM write: later map/QR/API
// effects are unrelated to the links, and no browser dependency is needed.
const ctx = vm.createContext({
  console, URLSearchParams, clearTimeout() {},
  localStorage: { getItem() { return null; } },
  location: { origin: 'http://localhost' },
  registerPage() {},
  ROLE_COLORS: {}, HEALTH_THRESHOLDS: { infraSilentMs: 3600000, nodeSilentMs: 3600000 },
  getNodeStatus() { return 'active'; }, getEffectiveHeardMs() { return NaN; },
  groupRecentAdverts() { return []; },
  api() { throw new Error('Rendering quick actions must not request data'); },
});
ctx.window = ctx;
const appSource = fs.readFileSync(path.join(root, 'public/app.js'), 'utf8');
for (const name of ['escapeHtml', 'parseViewportHash']) {
  const source = appSource.match(new RegExp('function ' + name + '\\([^]*?\\n}'));
  assert.ok(source, name + ' must exist in app.js');
  vm.runInContext(source[0], ctx);
}
const nodesSource = fs.readFileSync(path.join(root, 'public/nodes.js'), 'utf8');
vm.runInContext(nodesSource.replace(/\}\)\(\);\s*$/, 'window.renderDetailForTest = renderDetail; window.selectNodeForTest = selectNode; window.destroyNodeForTest = destroy; })();'), ctx);

function render(node) {
  let html;
  const done = new Error('DOM captured');
  const panel = { set innerHTML(value) { html = value; throw done; } };
  assert.throws(() => ctx.renderDetailForTest(panel, {
    node: { public_key: 'ab'.repeat(32), name: 'Fixture node', role: 'companion', ...node },
  }), error => error === done);
  return html;
}
function action(html, label) {
  const links = [...html.matchAll(/<a\b[^>]*href="([^"]*)"[^>]*>([^]*?)<\/a>/g)];
  const found = links.filter(match => match[2].replace(/<[^>]*>/g, '').trim() === label);
  assert.ok(found.length <= 1, 'duplicate action: ' + label);
  if (!found.length) return null;
  const href = found[0][1].replace(/&amp;/g, '&').replace(/&#39;/g, "'").replace(/&quot;/g, '"');
  return { markup: found[0][0], href, params: new URLSearchParams(href.split('?')[1]) };
}

let passed = 0, failed = 0;
function test(name, run) {
  try { run(); passed++; console.log('PASS ' + name); }
  catch (error) { failed++; console.error('FAIL ' + name + ': ' + error.message); }
}

test('both actions use existing routes without overriding packet preferences', () => {
  const html = render({ lat: 12.5, lon: 34.5 });
  const map = action(html, 'Open in Map');
  const packets = action(html, 'View packets');
  assert.ok(map, 'Open in Map is missing');
  assert.ok(packets, 'View packets is missing');
  assert.equal(map.href.split('?')[0], '#/map');
  assert.deepEqual(Object.fromEntries(map.params), { node: 'ab'.repeat(32), lat: '12.5', lon: '34.5', zoom: '14' });
  assert.equal(packets.href.split('?')[0], '#/packets');
  assert.deepEqual(Object.fromEntries(packets.params), { node: 'ab'.repeat(32) });
  assert.ok(action(html, 'Analytics') && action(html, 'Reach'), 'existing actions remain');
  assert.ok(html.includes('node-detail-btn'), 'Details remains');
});

for (const [lat, lon] of [[0, 0], [0, -45], [45, 0], ['0', '0'], ['12.5', '-34.5'], [-90, -180], [90, 180]]) {
  test('valid coordinates ' + JSON.stringify([lat, lon]), () => {
    const map = action(render({ lat, lon }), 'Open in Map');
    assert.ok(map, 'valid coordinates must offer Map');
    assert.equal(Number(map.params.get('lat')), Number(lat));
    assert.equal(Number(map.params.get('lon')), Number(lon));
  });
}
for (const value of [null, undefined, '', ' ', '12north', 'NaN', NaN, Infinity, -Infinity, true, [], {}]) {
  for (const coordinate of ['lat', 'lon']) {
    test('invalid ' + coordinate + ': ' + String(value), () => {
      const html = render({ lat: 1, lon: 2, [coordinate]: value });
      assert.equal(action(html, 'Open in Map'), null);
      assert.ok(action(html, 'View packets'), 'packets remain available without coordinates');
    });
  }
}
for (const [lat, lon] of [[90.01, 0], [-90.01, 0], [0, 180.01], [0, -180.01]]) {
  test('out-of-range coordinates ' + JSON.stringify([lat, lon]), () => {
    assert.equal(action(render({ lat, lon }), 'Open in Map'), null);
  });
}
test('hostile identifiers remain one encoded node parameter', () => {
  const key = 'node" onfocus="bad &/?#%\'<value>';
  const html = render({ public_key: key, lat: 1, lon: 2 });
  for (const label of ['Open in Map', 'View packets']) {
    const link = action(html, label);
    assert.ok(link, label + ' is missing');
    assert.equal(link.params.get('node'), key);
    assert.ok(!link.markup.includes('onfocus='), 'identifier escaped into an attribute');
    assert.ok(!link.markup.includes('<value>'), 'identifier escaped into markup');
  }
});
(async () => {
  const key = 'ab'.repeat(32);
  let close;
  const routeTitleCalls = [];
  ctx.setRouteTitle = (...args) => routeTitleCalls.push(args);
  ctx.innerWidth = 820;
  ctx.document = { getElementById() { return null; }, querySelector() { return null; } };
  ctx.history = { replaceState(_state, _title, hash) { ctx.location.hash = hash; } };
  ctx.CLIENT_TTL = {};
  ctx.api = async () => ({ node: { public_key: key, role: 'companion' } });
  ctx.SlideOver = {
    shouldUse() { return true; },
    open(options) { close = options.onClose; return {}; },
  };
  for (const destination of ['#/nodes/' + key, '#/map?node=' + key, '#/packets?node=' + key,
    '#/nodes/' + key + '/analytics', '#/nodes/' + key + '/reach', '#/nodes/other']) {
    await ctx.selectNodeForTest(key);
    routeTitleCalls.length = 0;
    ctx.location.hash = destination;
    close();
    test('closing the preview preserves the route title for ' + destination.split('?')[0], () => {
      assert.deepEqual(routeTitleCalls, destination === '#/nodes/' + key ? [['nodes']] : []);
    });
  }
  await ctx.selectNodeForTest(key);
  ctx.destroyNodeForTest();
  routeTitleCalls.length = 0;
  close();
  test('closing after full-detail navigation does not reset its title', () => {
    assert.deepEqual(routeTitleCalls, []);
  });
  console.log(`${passed} passed, ${failed} failed`);
  process.exitCode = failed ? 1 : 0;
})().catch(error => { console.error(error); process.exitCode = 1; });
