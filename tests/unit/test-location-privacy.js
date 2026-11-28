/* Location privacy (frontend): shared helper behaviour and the packets raw-bytes
 * section when the server withholds raw_hex. Loads the real public/*.js. */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
process.chdir(REPO_ROOT);
const { loadApiBase } = require('./api-base-sandbox');
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// Sandbox construction mirrors test-issue-1849-trace-hashbytes.js.
function makeSandbox() {
  const registeredPages = {};
  const ctx = {
    window: {
      addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
      innerWidth: 1200, PacketFilter: null,
    },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', className: '', style: {},
        appendChild: () => {}, setAttribute: () => {}, addEventListener: () => {},
        querySelectorAll: () => [], querySelector: () => null,
        classList: { add: () => {}, remove: () => {}, contains: () => false } }),
      head: { appendChild: () => {} }, getElementById: () => null,
      addEventListener: () => {}, removeEventListener: () => {},
      querySelectorAll: () => [], querySelector: () => null, body: { appendChild: () => {} },
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, RangeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => { const s = {}; return {
      getItem: k => s[k] || null, setItem: (k, v) => { s[k] = String(v); }, removeItem: k => { delete s[k]; },
    }; })(),
    location: { hash: '' }, history: { replaceState: () => {} },
    CustomEvent: class CustomEvent {}, Map, Set, Promise, URLSearchParams,
    addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
    requestAnimationFrame: (cb) => setTimeout(cb, 0),
    registerPage: (name, handler) => { registeredPages[name] = handler; },
  };
  vm.createContext(ctx);
  loadApiBase(ctx);
  return ctx;
}

function loadInCtx(ctx, file) {
  vm.runInContext(fs.readFileSync(file, 'utf8'), ctx, { filename: file });
  for (const k of Object.keys(ctx.window)) { ctx[k] = ctx.window[k]; }
}

function loadPacketsSandbox() {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  loadInCtx(ctx, 'public/packet-helpers.js');
  vm.runInContext(`
    window.HopDisplay = {
      renderHop: function(h, entry, opts) { return '<span>' + h + '</span>'; },
      _showFromBtn: function() {}
    };
  `, ctx);
  loadInCtx(ctx, 'public/packets.js');
  return ctx;
}


function loadLP() {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/location-privacy.js');
  return ctx.window.LocationPrivacy;
}

console.log('\n=== LocationPrivacy: client config ===');
test('missing config / missing key / disabled are all off', () => {
  const LP = loadLP();
  for (const cfg of [undefined, null, {}, { locationPrivacy: null }, { locationPrivacy: { enabled: false, radiusM: 1000 } }, { locationPrivacy: { enabled: 'yes', radiusM: 1000 } }]) {
    LP.fromClientConfig(cfg);
    assert.strictEqual(LP.enabled(), false);
    assert.strictEqual(LP.approxNoteHtml('positions'), '');
    assert.strictEqual(LP.approxLocationLabel(), '');
    assert.strictEqual(LP.legendText(), '');
  }
});
test('enabled config exposes radius and labels', () => {
  const LP = loadLP();
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  assert.strictEqual(LP.enabled(), true);
  assert.strictEqual(LP.radiusM(), 1000);
  assert.strictEqual(LP.radiusLabel(), 'within 1 km');
  assert.strictEqual(LP.approxLocationLabel(), 'Approximate location (within 1 km)');
  assert.strictEqual(LP.legendText(), 'Positions approximate (within 1 km)');
});
test('radius labels', () => {
  const LP = loadLP();
  const label = (r) => { LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: r } }); return LP.radiusLabel(); };
  assert.strictEqual(label(500), 'within 500 m');
  assert.strictEqual(label(999), 'within 999 m');
  assert.strictEqual(label(1500), 'within 1.5 km');
  assert.strictEqual(label(50000), 'within 50 km');
  assert.strictEqual(label('250'), 'within 250 m');
  assert.strictEqual(label(0), 'within a short distance');
  assert.strictEqual(label('junk'), 'within a short distance');
});
test('turning the flag off again clears the state', () => {
  const LP = loadLP();
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  LP.fromClientConfig({ locationPrivacy: { enabled: false, radiusM: 0 } });
  assert.strictEqual(LP.enabled(), false);
  assert.strictEqual(LP.radiusM(), 0);
});

test('a config without the locationPrivacy key (feature off) is off', () => {
  const LP = loadLP();
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  LP.fromClientConfig({ clientRxCoverage: true, map: {} });
  assert.strictEqual(LP.enabled(), false);
  assert.strictEqual(LP.radiusM(), 0);
  assert.strictEqual(LP.approxNoteHtml('positions'), '');
});

console.log('\n=== LocationPrivacy: notices ===');
test('positions notice carries the radius and the indicative wording', () => {
  const LP = loadLP();
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  const html = LP.approxNoteHtml('positions');
  assert(html.includes('Node positions on this instance are approximate (within 1 km). Distances, line of sight and planning results are indicative.'), html);
  assert(html.includes('role="note"'));
  assert(LP.approxNoteHtml('coverage').includes('coarsened for privacy'));
  assert(LP.approxNoteHtml('export').includes('exported positions'));
  assert(LP.approxNoteHtml('location').includes('Approximate location (within 1 km)'));
  assert(LP.approxNoteHtml('nonsense').includes('data-lp-kind="positions"'), 'unknown kind falls back');
});
test('notice HTML escapes its kind', () => {
  const LP = loadLP();
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  const html = LP.approxNoteHtml('"><script>x</script>');
  assert(!html.includes('<script>'), html);
  assert(!LP.slotHtml('"><img onerror=x>').includes('<img'));
});
test('rawHexHidden needs null raw_hex AND location_hidden', () => {
  const LP = loadLP();
  assert.strictEqual(LP.rawHexHidden({ raw_hex: null, location_hidden: true }), true);
  assert.strictEqual(LP.rawHexHidden({ location_hidden: true }), true);
  assert.strictEqual(LP.rawHexHidden({ raw_hex: null }), false);
  assert.strictEqual(LP.rawHexHidden({ raw_hex: 'aabb', location_hidden: true }), false);
  assert.strictEqual(LP.rawHexHidden(null), false);
  assert(LP.hiddenBytesHtml().includes('Raw bytes hidden \u2014 this advert carries a location, and this instance hides exact positions.'));
});
test('drawUncertainty is a no-op off and draws a radiusM circle on', () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/location-privacy.js');
  const LP = ctx.window.LocationPrivacy;
  const made = [];
  ctx.window.L = { circle: (ll, o) => { const c = { ll, o, addTo(m) { c.map = m; return c; } }; made.push(c); return c; } };
  ctx.window.getComputedStyle = () => ({ getPropertyValue: () => ' rgb(1, 2, 3) ' });
  ctx.document.documentElement = {};
  const map = {};
  assert.strictEqual(LP.drawUncertainty(map, [1, 2]), null);
  assert.strictEqual(made.length, 0);
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 750 } });
  const c = LP.drawUncertainty(map, [1, 2]);
  assert(c && c.map === map);
  assert.strictEqual(c.o.radius, 750);
  assert.strictEqual(c.o.color, 'rgb(1, 2, 3)');
  assert.strictEqual(LP.drawUncertainty(map, [1, 2]), c, 'one circle per map');
  assert.strictEqual(made.length, 1);
});
test('drawUncertainty skips a removed map and a failing addTo', () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/location-privacy.js');
  const LP = ctx.window.LocationPrivacy;
  ctx.document.documentElement = {};
  ctx.window.getComputedStyle = () => ({ getPropertyValue: () => 'rgb(1, 2, 3)' });
  let made = 0;
  ctx.window.L = { circle: () => { made++; return { setStyle() {}, addTo() { throw new Error('Map container is being reused'); } }; } };
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  assert.strictEqual(LP.drawUncertainty({ _loaded: false }, [1, 2]), null);
  assert.strictEqual(LP.drawUncertainty({ _loaded: true, _container: { isConnected: false } }, [1, 2]), null);
  assert.strictEqual(made, 0);
  assert.strictEqual(LP.drawUncertainty({ _loaded: true }, [1, 2]), null, 'addTo throwing is swallowed');
});
function focusSandbox() {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/location-privacy.js');
  const LP = ctx.window.LocationPrivacy;
  ctx.document.documentElement = {};
  ctx.window.getComputedStyle = () => ({ getPropertyValue: () => 'rgb(1, 2, 3)' });
  const made = [];
  const map = {
    _loaded: true,
    layers: new Set(),
    hasLayer(l) { return this.layers.has(l); },
    removeLayer(l) { this.layers.delete(l); }
  };
  ctx.window.L = focusSandbox.lastL = { circle: (ll, o) => {
    const c = { ll, o, setLatLng(x) { c.ll = x; return c; }, addTo(m) { m.layers.add(c); return c; }, setStyle() {} };
    made.push(c);
    return c;
  } };
  const marker = () => {
    const h = {};
    return { h, on(ev, fn) { h[ev] = fn; }, fire(ev) { const m = this; h[ev]({ target: m }); } };
  };
  return { LP, map, made, marker };
}
test('bindUncertainty: hover shows one moving circle, mouseout hides it', () => {
  const { LP, map, made, marker } = focusSandbox();
  const a = marker(), b = marker();
  const llA = [1, 2], llB = [3, 4];
  LP.bindUncertainty(map, a, llA);
  LP.bindUncertainty(map, b, llB);
  a.fire('mouseover');
  assert.strictEqual(made.length, 0, 'off: no circle');
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  a.fire('mouseover');
  assert.strictEqual(made.length, 1);
  const c = made[0];
  assert.strictEqual(c.ll, llA);
  assert.strictEqual(c.o.radius, 1000);
  assert(map.hasLayer(c));
  a.fire('mouseout');
  assert(!map.hasLayer(c), 'hidden after mouseout');
  b.fire('mouseover');
  assert.strictEqual(made.length, 1, 'the one circle is reused');
  assert.strictEqual(c.ll, llB);
  assert(map.hasLayer(c));
});
test('bindUncertainty: an open popup pins the circle through other hovers', () => {
  const { LP, map, made, marker } = focusSandbox();
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 500 } });
  const a = marker(), b = marker();
  const llA = [1, 2], llB = [3, 4];
  LP.bindUncertainty(map, a, llA);
  LP.bindUncertainty(map, b, llB);
  a.fire('popupopen');
  const c = made[0];
  assert.strictEqual(c.ll, llA);
  a.fire('mouseout');
  assert(map.hasLayer(c), 'pinned circle stays after mouseout');
  b.fire('mouseover');
  assert.strictEqual(c.ll, llB);
  b.fire('mouseout');
  assert.strictEqual(c.ll, llA, 'back to the pinned marker');
  assert(map.hasLayer(c));
  b.fire('popupclose');
  assert(map.hasLayer(c), 'closing another marker popup keeps the pin');
  a.fire('popupclose');
  assert(!map.hasLayer(c));
});
test('pinUncertainty pins and clears; no-op off or on a removed map', () => {
  const { LP, map, made } = focusSandbox();
  assert.strictEqual(LP.pinUncertainty(map, [1, 2]), null, 'off');
  assert.strictEqual(made.length, 0);
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  const c = LP.pinUncertainty(map, [1, 2]);
  assert(c && map.hasLayer(c));
  LP.pinUncertainty(map, null);
  assert(!map.hasLayer(c));
  assert.strictEqual(map._lpPinned, null);
  assert.strictEqual(LP.pinUncertainty({ _loaded: false }, [1, 2]), null);
  assert.strictEqual(LP.pinUncertainty(null, [1, 2]), null);
  LP.bindUncertainty(map, null, [1, 2]);
  LP.bindUncertainty(map, {}, [1, 2]);
});
test('circles share one no-pointer SVG pane under the overlay pane', () => {
  const { LP, map } = focusSandbox();
  const panes = {};
  map.getPane = (n) => panes[n];
  map.createPane = (n) => (panes[n] = { style: {} });
  const svgs = [];
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  const L = focusSandbox.lastL;
  L.svg = (o) => { const r = { o }; svgs.push(r); return r; };
  const fixed = LP.drawUncertainty(map, [1, 2]);
  const focus = LP.pinUncertainty(map, [3, 4]);
  assert.notStrictEqual(fixed, focus, 'detail circle and focus circle are separate');
  assert.strictEqual(svgs.length, 1, 'one renderer per map');
  assert.strictEqual(svgs[0].o.pane, 'lpUncertainty');
  assert.strictEqual(fixed.o.renderer, svgs[0]);
  assert.strictEqual(focus.o.pane, 'lpUncertainty');
  assert.strictEqual(panes.lpUncertainty.style.pointerEvents, 'none');
  assert.strictEqual(panes.lpUncertainty.style.zIndex, '390');
});
test('circle colour follows the theme', () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/location-privacy.js');
  const LP = ctx.window.LocationPrivacy;
  ctx.document.documentElement = {};
  let accent = 'rgb(1, 1, 1)';
  ctx.window.getComputedStyle = () => ({ getPropertyValue: () => accent });
  const styles = [];
  ctx.window.L = { circle: () => { const c = { setStyle: (o) => styles.push(o), addTo() { return c; } }; return c; } };
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  const map = { _loaded: true };
  assert(LP.drawUncertainty(map, [1, 2]));
  accent = 'rgb(9, 9, 9)';
  LP._restyle();
  assert.strictEqual(styles.length, 1);
  assert.strictEqual(styles[0].color, 'rgb(9, 9, 9)');
  map._loaded = false;
  LP._restyle();
  assert.strictEqual(styles.length, 1, 'a removed map is dropped, not restyled');
});
test('a new circle drops the circles of removed maps', () => {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/location-privacy.js');
  const LP = ctx.window.LocationPrivacy;
  ctx.document.documentElement = {};
  ctx.window.getComputedStyle = () => ({ getPropertyValue: () => 'rgb(1, 1, 1)' });
  ctx.window.L = { circle: () => { const c = { setStyle() {}, addTo() { return c; } }; return c; } };
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  // Node detail opened three times: each visit draws on a new map, the last stays.
  const maps = [{ _loaded: true }, { _loaded: true }, { _loaded: true }];
  maps.forEach((m, i) => {
    if (i > 0) maps[i - 1]._loaded = false;
    assert(LP.drawUncertainty(m, [1, 2]));
  });
  assert.strictEqual(LP._circleCount(), 1, 'only the open map keeps its circle reachable');
  maps[2]._container = { isConnected: false };
  assert(LP.drawUncertainty({ _loaded: true }, [3, 4]));
  assert.strictEqual(LP._circleCount(), 1, 'a detached container counts as removed');
});
test('addAttribution adds the legend line once', () => {
  const LP = loadLP();
  LP.fromClientConfig({ locationPrivacy: { enabled: true, radiusM: 1000 } });
  const added = [];
  const map = { attributionControl: { addAttribution: (h) => added.push(h) } };
  LP.addAttribution(map); LP.addAttribution(map);
  assert.strictEqual(added.length, 1);
  assert(added[0].includes('Positions approximate (within 1 km)'));
});

console.log('\n=== app.js: frame size and path byte from frame_bytes / frame_head ===');
{
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  // FLOOD ADVERT (0x11), path byte 0x42 = 2-byte hashes, 2 hops, then 4 path bytes.
  const hidden = { raw_hex: null, location_hidden: true, frame_bytes: 147, frame_head: '1142aabbccdd' };
  test('frameSize / frameHex fall back to the server fields', () => {
    assert.strictEqual(ctx.frameSize(hidden), 147);
    assert.strictEqual(ctx.frameHex(hidden), '1142aabbccdd');
    assert.strictEqual(ctx.frameSize({ raw_hex: 'aabbcc', frame_bytes: 9 }), 3, 'raw_hex wins');
    assert.strictEqual(ctx.frameSize({ raw_hex: null }), 0);
    assert.strictEqual(ctx.frameSize({ frame_bytes: -4 }), 0);
    assert.strictEqual(ctx.frameHex(null), '');
  });
  test('hash size badge works from frame_head', () => {
    assert.strictEqual(ctx.pathHashSize(ctx.frameHex(hidden)), 2);
  });
}

console.log('\n=== packets.js: raw bytes section without raw_hex ===');
function loadPacketsSandbox() {
  const ctx = makeSandbox();
  loadInCtx(ctx, 'public/payload-labels.js');
  loadInCtx(ctx, 'public/roles.js');
  loadInCtx(ctx, 'public/app.js');
  loadInCtx(ctx, 'public/packet-helpers.js');
  loadInCtx(ctx, 'public/location-privacy.js');
  vm.runInContext(`
    window.HopDisplay = {
      renderHop: function(h, entry, opts) { return '<span>' + h + '</span>'; },
      _showFromBtn: function() {}
    };
  `, ctx);
  loadInCtx(ctx, 'public/packets.js');
  return ctx;
}
{
  const ctx = loadPacketsSandbox();
  const raw = ctx.window.__meshcorePacketsInternals.rawBytesHtml;
  const LP = ctx.window.LocationPrivacy;
  const hiddenPkt = { raw_hex: null, location_hidden: true, route_type: 1, payload_type: 4 };
  const decoded = { name: 'Node A', flags: { raw: 0x91 } };
  test('hidden advert shows the message and never the hex view', () => {
    const html = raw(hiddenPkt, hiddenPkt, decoded, [], [], []);
    assert(html.includes('Raw bytes hidden'), html);
    assert(!html.includes('hex-dump'));
    assert(html.includes('Show decoded fields'));
    assert(html.includes('Node A'));
  });
  test('hidden advert with no decoded fields still shows the message', () => {
    const html = raw(hiddenPkt, hiddenPkt, {}, [], [], []);
    assert(html.includes('Raw bytes hidden'));
    assert(!html.includes('<details'));
  });
  test('observation hidden flag is honoured when the packet lacks it', () => {
    const pkt = { raw_hex: null, route_type: 1, payload_type: 4 };
    const obs = { raw_hex: null, location_hidden: true };
    assert(raw(Object.assign({}, pkt, obs), pkt, {}, [], [], []).includes('Raw bytes hidden'));
  });
  test('null raw_hex without location_hidden does not throw and shows no message', () => {
    const pkt = { raw_hex: null, route_type: 1, payload_type: 5 };
    const html = raw(pkt, pkt, { text: 'hi' }, [], [], []);
    assert(!html.includes('Raw bytes hidden'));
    assert(html.includes('Show raw bytes'));
    assert.strictEqual(raw(pkt, pkt, {}, [], [], []), '');
  });
  test('with raw_hex present the hex dump renders and no message', () => {
    const pkt = { raw_hex: '0100aabbcc', route_type: 1, payload_type: 5, location_hidden: true };
    const html = raw(pkt, pkt, { text: 'hi' }, [{ start: 0, end: 0, label: 'Header' }], [], []);
    assert(html.includes('hex-dump'));
    assert(!html.includes('Raw bytes hidden'));
  });
}

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed ? 1 : 0);
