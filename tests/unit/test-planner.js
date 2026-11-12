/**
 * Repeater planner (#/tools/planner): loads the REAL public/los.js and
 * public/planner.js in a vm context with a stub window, location/history,
 * fake timers and a stub fetch.
 *
 * Covers the window.MeshPlanner surface the area module builds on (fetchJob's
 * job protocol, params/setParams, fmt*, esc) and the pure helpers in
 * _internals: point (de)serialisation, hash parsing per mode, legsFromPoints,
 * chipFor, and that every HTML builder keeps hostile node names inert. The
 * page itself (map, panels, profile panel) is covered in the browser.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const REPO_ROOT = path.resolve(__dirname, '..', '..');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const read = f => fs.readFileSync(path.join(REPO_ROOT, 'public', f), 'utf8');
const LOS_SRC = read('los.js');
const PLANNER_SRC = read('planner.js');
const flush = () => new Promise(r => setImmediate(r));
const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// ─── Fake timers: setTimeout only queues; the test runs them ─────────────

function fakeTimers() {
  let seq = 0;
  const q = [];
  return {
    setTimeout(fn, ms) { const t = { id: ++seq, fn, ms }; q.push(t); return t.id; },
    clearTimeout(id) { const i = q.findIndex(t => t.id === id); if (i >= 0) q.splice(i, 1); },
    pending: () => q.slice(),
    runNext() { const t = q.shift(); if (t) t.fn(); return t; }
  };
}

// ─── Stub responses ───────────────────────────────────────────────────────

function res(status, body, headers) {
  const h = headers || {};
  return {
    status, ok: status >= 200 && status < 300,
    json: () => (body === undefined ? Promise.reject(new Error('no body')) : Promise.resolve(body)),
    headers: { get: k => (Object.prototype.hasOwnProperty.call(h, k) ? h[k] : null) }
  };
}

function makeFetch(responses) {
  const calls = [];
  const f = (url, opts) => {
    calls.push({ url, opts });
    const next = responses.shift();
    if (typeof next === 'function') return next(url, opts);
    return next instanceof Error ? Promise.reject(next) : Promise.resolve(next);
  };
  f.calls = calls;
  return f;
}

// ─── Context loader ───────────────────────────────────────────────────────

function load(opts) {
  opts = opts || {};
  const timers = opts.timers || fakeTimers();
  const replaced = [];
  const ctx = {
    console: { log() {}, error() {}, warn() {} },
    setTimeout: timers.setTimeout, clearTimeout: timers.clearTimeout,
    location: { hash: opts.hash || '#/tools/planner' },
    history: { state: null, replaceState(s, t, u) { replaced.push(u); ctx.location.hash = u; } },
    fetch: opts.fetch || makeFetch([]),
    AbortController, Map, Promise, Date
  };
  Object.assign(ctx, opts.globals || {});
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(LOS_SRC, ctx);
  vm.runInContext(PLANNER_SRC, ctx);
  return { ctx, MP: ctx.MeshPlanner, I: ctx.MeshPlanner._internals, LOS: ctx.MeshLOS, timers, replaced };
}

const N = 100;
const flat = h => Array.from({ length: N }, () => h);
const ridge = (base, peak) => Array.from({ length: N }, (_, i) => base + peak * Math.exp(-Math.pow((i - 50) / 6, 2)));

async function main() {
  const { MP, I, LOS } = load();

  console.log('\n=== MeshPlanner surface ===');
  ['fetchJob', 'showProfiles', 'esc', 'fmtKm', 'fmtPct', 'params', 'setParams'].forEach(k => {
    assert(typeof MP[k] === 'function', 'MeshPlanner.' + k + ' is a function');
  });
  ['parsePts', 'serializePts', 'parseParams', 'legsFromPoints', 'chipFor'].forEach(k => {
    assert(typeof I[k] === 'function', '_internals.' + k + ' is a function');
  });
  assert(MP.esc('<a href="x">\'&') === '&lt;a href=&quot;x&quot;&gt;&#39;&amp;', 'esc escapes & < > " \'');
  assert(MP.fmtKm(3.14159) === '3.14 km' && MP.fmtKm(42.26) === '42.3 km' && MP.fmtKm(123.4) === '123 km', 'fmtKm: 2 / 1 / 0 decimals by size');
  assert(MP.fmtKm(null) === '—' && MP.fmtKm('x') === '—', 'fmtKm: unknown → —');
  assert(MP.fmtPct(62.4) === '62%' && MP.fmtPct(-45.2) === '−45%' && MP.fmtPct(5000) === '>999%', 'fmtPct: rounds, true minus, caps');
  assert(MP.fmtPct(null) === '—' && MP.fmtPct(NaN) === '—', 'fmtPct: unknown → —');

  console.log('\n=== fmtClearance ===');
  {
    const f = MP.fmtClearance;
    assert(f(999) === 'full clearance' && f(1500) === 'full clearance', 'fmtClearance: >= 999 → full clearance');
    assert(f(998.9) === '999%', 'fmtClearance: 998.9 → 999%');
    assert(f(0) === '0%' && f(62.4) === '62%', 'fmtClearance: ordinary values');
    assert(f(-99.9) === '−100%', 'fmtClearance: -99.9 → −100%');
    assert(f(-100) === 'terrain blocks the path' && f(-999) === 'terrain blocks the path', 'fmtClearance: <= -100 → terrain blocks the path');
    assert(f(null) === '—' && f('') === '—' && f(NaN) === '—', 'fmtClearance: unknown → —');
    assert(MP.fmtPct(-999) === '−999%' && MP.fmtPct(5000) === '>999%', 'fmtPct unchanged');
    const area = {};
    area.window = area;
    vm.createContext(area);
    vm.runInContext(read('planner-area.js'), area);
    const fb = area.MeshPlannerArea._internals.fmtClearance;
    const samples = [999, 1500, 998.9, 62.4, 0, -0.4, -45.2, -99.9, -100, -999, null, '', NaN, 'x', '12'];
    assert(samples.every(x => fb(x) === f(x)), 'planner-area fallback fmtClearance matches the page helper');
    const dh = I.directHtml({ distance_km: 41.3, clearance_pct: -999, verdict: 'blocked' }, 'Point A', 'Point B');
    assert(/Point A → Point B · 41\.3 km · terrain blocks the path/.test(dh), 'direct link line wording');
    assert(!/999/.test(dh), 'no sentinel number in the direct link');
    assert(I.directHtml({ distance_km: 5, clearance_pct: 62, verdict: 'clear' }, 'A', 'B').includes('F1 clearance 62%'), 'ordinary direct clearance keeps its label');
    const row = I.optionRowHtml({ kind: 'open', lat: 1, lon: 2, leg_a: { verdict: 'clear', clearance_pct: 999 }, leg_b: { verdict: 'clear', clearance_pct: 999 }, bottleneck_pct: 999 }, 'open', 0, false);
    assert(row.includes('full clearance') && !row.includes('&gt;999'), 'option score wording');
    assert(I.chipFor(I.linkAnalysis({ verdict: 'blocked', clearance_pct: -999 })).title.indexOf('-999') < 0 && I.chipFor(I.linkAnalysis({ verdict: 'blocked', clearance_pct: -999 })).title.indexOf('999') < 0, 'chip title has no sentinel');
    const prof = I.profileSectionHtml({ a: { name: 'A' }, b: { name: 'B' }, error: 'no_terrain', distanceKm: 3 }, 0);
    assert(prof.includes('No terrain data for this leg'), 'no_terrain leg wording');
    const w = I.warningsHtml(['terrain missing for 1 of 4 tiles (read as sea level)', '<b>x</b>']);
    assert(w.includes('mc-pl-warnings') && w.includes('terrain missing for 1 of 4 tiles') && w.includes('&lt;b&gt;'), 'relay warnings render, escaped');
    assert(I.warningsHtml([]) === '' && I.warningsHtml(undefined) === '', 'no warnings → nothing');
    assert(/el\.innerHTML = html \+ warningsHtml\(res\.warnings\)/.test(PLANNER_SRC), 'relay panel renders warnings');
  }

  console.log('\n=== parsePts / serializePts ===');
  {
    const pts = [{ lat: 37.77493, lon: -122.41942 }, { lat: -33.8688, lon: 151.2093 }, { lat: 0, lon: 0 }];
    const s = I.serializePts(pts);
    assert(s === '37.77493,-122.41942,-33.8688,151.2093,0,0', 'serialize: lat,lon pairs, trailing zeros trimmed (got ' + s + ')');
    assert(eq(I.parsePts(s), pts), 'round trip parse(serialize(pts)) === pts');
    assert(I.serializePts(I.parsePts(s)) === s, 'round trip serialize(parse(s)) === s');
    const r = I.parsePts('37.1234567,-122.0000049');
    assert(r.length === 1 && r[0].lat === 37.12346 && r[0].lon === -122, 'parse rounds to 5 decimals (~1 m)');
    assert(I.serializePts([{ lat: -0.000001, lon: 5 }]) === '0,5', 'no "-0" in the URL');
    assert(I.parsePts('').length === 0 && I.parsePts(null).length === 0 && I.parsePts(undefined).length === 0, 'empty / null → []');
    assert(I.parsePts('abc').length === 0 && I.parsePts('a,b,c,d').length === 0, 'non-numeric → []');
    assert(eq(I.parsePts('1,2,3'), [{ lat: 1, lon: 2 }]), 'trailing odd value ignored');
    assert(eq(I.parsePts('91,0,10,20'), [{ lat: 10, lon: 20 }]), 'out-of-range latitude pair dropped');
    assert(I.parsePts('10,181').length === 0 && I.parsePts('-90.5,0').length === 0, 'out-of-range longitude / latitude dropped');
    assert(I.parsePts('0x10,5').length === 0 && I.parsePts('1e1,5').length === 0 && I.parsePts('Infinity,5').length === 0, 'hex, exponent and Infinity are not decimal degrees');
    assert(eq(I.parsePts(' 10 , 20 '), [{ lat: 10, lon: 20 }]), 'whitespace around numbers tolerated');
    assert(I.parsePts('1,1,2,2,3,3', 2).length === 2, 'max caps the count');
    assert(I.serializePts([{ lat: 'x', lon: 1 }, null, { lat: 1, lon: 2 }, { lat: 95, lon: 0 }]) === '1,2', 'serialize skips invalid points');
    assert(I.serializePts([]) === '' && I.serializePts(null) === '', 'serialize empty → ""');
    assert(I.parsePoint('37.5,-122.25').lat === 37.5 && I.parsePoint('1,2,3,4') === null && I.parsePoint('x,y') === null, 'parsePoint wants exactly one pair');
  }

  console.log('\n=== parseParams ===');
  {
    const c = I.parseParams('#/tools/planner?mode=chain&pts=37.1,-122.1,37.2,-122.2,37.3,-122.3&h=10,5,x');
    assert(c.mode === 'chain', 'chain: mode');
    assert(c.chain.pts.length === 3 && c.chain.pts[2].lon === -122.3, 'chain: pts');
    assert(eq(c.chain.h, [10, 5, null]), 'chain: h per point, bad entry → null (default applies)');

    const r = I.parseParams('#/tools/planner?mode=relay&a=37.1,-122.1&b=37.2,-122.2&ha=12.5&hb=5&hr=20&range=30&sel=e2');
    assert(r.mode === 'relay', 'relay: mode');
    assert(r.relay.a.lat === 37.1 && r.relay.b.lon === -122.2, 'relay: a and b');
    assert(r.relay.ha === 12.5 && r.relay.hb === 5 && r.relay.hr === 20 && r.relay.range === 30, 'relay: heights and range');
    assert(eq(r.relay.sel, { list: 'existing', idx: 1 }), 'relay: sel=e2 → existing #2');
    assert(eq(I.parseParams('?mode=relay&sel=o1').relay.sel, { list: 'open', idx: 0 }), 'relay: sel=o1 → open #1');

    const bad = I.parseParams('#/tools/planner?mode=relay&a=1,2,3&b=x,y&ha=500&hb=-1&hr=abc&range=nope&sel=x9');
    assert(bad.relay.a === null && bad.relay.b === null, 'relay: malformed a/b → null');
    assert(bad.relay.ha === null && bad.relay.hb === null && bad.relay.hr === null, 'relay: heights outside 0–100 m or non-numeric → null');
    assert(bad.relay.range === null && bad.relay.sel === null, 'relay: bad range / sel → null');

    const a = I.parseParams('#/tools/planner?mode=area&bbox=37,-123,38,-122&sites=3');
    assert(a.mode === 'area' && a.raw.bbox === '37,-123,38,-122' && a.raw.sites === '3', 'area: mode, its params left raw for the area module');

    assert(I.parseParams('#/tools/planner').mode === 'chain', 'no mode → chain');
    assert(I.parseParams('#/tools/planner?mode=bogus').mode === 'chain', 'unknown mode → chain');
    assert(I.parseParams('').mode === 'chain' && I.parseParams(null).chain.pts.length === 0, 'empty / null hash');
    assert(I.parseParams('#/tools/planner?pts=1%2C2%2C3%2C4').chain.pts.length === 2, 'percent-encoded commas decode');
    assert(I.parseParams('mode=relay&a=1,2').relay.a.lon === 2, 'a bare query string works too');
    const proto = I.queryOf('?__proto__=x&constructor=y&mode=relay&mode=chain');
    assert(!Object.prototype.hasOwnProperty.call(proto, '__proto__') && proto.mode === 'relay', 'queryOf: prototype keys ignored, first occurrence wins');
    assert(I.buildQuery({ pts: '1,2', mode: 'chain', h: '', x: null }) === '?mode=chain&pts=1,2', 'buildQuery: mode first, empty dropped, commas readable');
  }

  console.log('\n=== params / setParams (history.replaceState) ===');
  {
    const L2 = load({ hash: '#/tools/planner?mode=chain&pts=1,2,3,4&h=10,5' });
    assert(L2.MP.params().pts === '1,2,3,4', 'params() reads the hash query');
    L2.MP.setParams({ h: '7,8', pts: null, extra: 'a b' });
    assert(L2.ctx.location.hash === '#/tools/planner?mode=chain&h=7,8&extra=a%20b', 'setParams merges, removes null, encodes (got ' + L2.ctx.location.hash + ')');
    assert(L2.replaced.length === 1, 'setParams uses replaceState (no hashchange, no new history entry)');
    L2.MP.setParams({ h: '7,8' });
    assert(L2.replaced.length === 1, 'unchanged params → no replaceState');
    const L3 = load({ hash: '#/map' });
    assert(L3.MP.setParams({ mode: 'relay' }) === false && L3.replaced.length === 0, 'setParams is a no-op off the planner route');
  }

  console.log('\n=== legsFromPoints ===');
  {
    const pts = [
      { lat: 37.1, lon: -122.1, h: 10, node: null },
      { lat: 37.2, lon: -122.2, h: 5, node: { name: 'Hilltop', pubkey: 'abcd' } },
      { lat: 37.3, lon: -122.3, h: 20, node: null }
    ];
    const legs = I.legsFromPoints(pts);
    assert(legs.length === 2, '3 points → 2 legs');
    assert(legs[0].ha === 10 && legs[0].hb === 5, 'leg 1 heights: point 1 → point 2');
    assert(legs[1].ha === 5 && legs[1].hb === 20, 'leg 2 heights: the middle mast serves both legs');
    assert(legs[0].a.name === 'Point 1' && legs[0].b.name === 'Hilltop' && legs[1].b.name === 'Point 3', 'names: node name when snapped, else "Point N"');
    assert(legs[1].a.lat === 37.2 && legs[1].b.lon === -122.3 && legs[0].i === 0 && legs[1].i === 1, 'leg i joins points i → i+1');
    assert(legs[0].key === '37.1,-122.1>37.2,-122.2', 'leg key from rounded coordinates, direction-aware');
    const many = Array.from({ length: 16 }, (_, i) => ({ lat: 37 + i / 100, lon: -122, h: i }));
    const ml = I.legsFromPoints(many);
    assert(ml.length === 15 && ml.every((l, k) => l.ha === k && l.hb === k + 1), '16 points → 15 legs, heights in order');
    assert(I.legsFromPoints([pts[0]]).length === 0 && I.legsFromPoints([]).length === 0 && I.legsFromPoints(null).length === 0, 'fewer than 2 points → no legs');
    assert(I.legsFromPoints([{ lat: 1, lon: 1 }, { lat: 2, lon: 2, h: 'x' }])[0].hb === 0, 'missing / bad height → 0');
  }

  console.log('\n=== chipFor ===');
  {
    // 5 km at 910 MHz: the first Fresnel zone is ~20 m at mid-path.
    const base = { distanceKm: 5, antennaAM: 30, antennaBM: 30, freqMHz: 910.525, kFactor: 4 / 3 };
    const clear = LOS.analyze(Object.assign({ elevM: flat(10) }, base));
    const blocked = LOS.analyze(Object.assign({ elevM: ridge(10, 200) }, base));
    const marginal = LOS.analyze(Object.assign({ elevM: ridge(10, 25) }, base));
    assert(clear.verdict === 'clear' && blocked.verdict === 'blocked' && marginal.verdict === 'marginal', 'fixtures: clear / blocked / marginal profiles');
    const cc = I.chipFor(clear), cb = I.chipFor(blocked), cm = I.chipFor(marginal);
    assert(cc.tone === 'good' && cc.text === 'Clear' && !cc.loading, 'clear → good "Clear"');
    assert(cb.tone === 'bad' && cb.text === 'Blocked' && /terrain \d+ m above/.test(cb.title), 'blocked → bad "Blocked", title says how high the terrain is');
    assert(cm.tone === 'warn' && cm.text === 'Marginal' && /Fresnel/.test(cm.title), 'marginal → warn "Marginal"');
    assert(I.chipFor({ verdict: 'clear60', clearance: 0.7 }).tone === 'good', 'clear60 → good');
    const load0 = I.chipFor(null);
    assert(load0.tone === 'muted' && load0.loading === true && load0.text === 'Checking…', 'null (loading) → muted, loading');
    const err = I.chipFor({ error: 'too_long' });
    assert(err.tone === 'muted' && err.text === 'n/a' && /Too long/.test(err.title) && !err.loading, 'leg error → muted n/a with the reason');
    assert(/failed/.test(I.chipFor({ error: 'whatever' }).title), 'unknown error code → generic reason');
    assert(I.chipFor({ verdict: 'bogus' }).tone === 'muted', 'unknown verdict → muted');
    const link = I.chipFor(I.linkAnalysis({ verdict: 'blocked', clearance_pct: -45, obstruction_m: 30.4, distance_km: 12 }));
    assert(link.tone === 'bad' && /30 m/.test(link.title), 'server Link → chip via linkAnalysis');
    assert(I.chipFor(I.linkAnalysis({ verdict: 'marginal', clearance_pct: 22 })).title.indexOf('22%') >= 0, 'Link clearance_pct shows in the title');
  }

  console.log('\n=== legResultFrom / resolveQuery / normCfg / parseRetryAfter ===');
  {
    assert(eq(I.legResultFrom({ distance_km: 3, elev_m: [1, 2, 3] }), { distanceKm: 3, elevM: [1, 2, 3] }), 'leg with terrain');
    assert(I.legResultFrom({ distance_km: 300, error: 'too_long' }).error === 'too_long', 'too_long kept');
    assert(I.legResultFrom({ error: 'out_of_area' }).error === 'out_of_area' && I.legResultFrom({ error: 'no_terrain' }).error === 'no_terrain', 'out_of_area / no_terrain kept');
    assert(I.legResultFrom({ error: '<b>x' }).error === 'request', 'unknown error code → request');
    assert(I.legResultFrom({ elev_m: [1, 'x'] }).error === 'bad_data' && I.legResultFrom({ elev_m: [1] }).error === 'bad_data', 'non-numeric / too-short profile → bad_data');
    assert(I.legResultFrom(null).error === 'request', 'missing leg → request');

    const nodes = I.normNodes([
      { public_key: 'aa', name: 'Hilltop Repeater', role: 'repeater', lat: 37.5, lon: -122.5 },
      { public_key: 'bb', name: 'Hilltop', role: 'companion', lat: 37.6, lon: -122.6 },
      { public_key: 'cc', name: 'Zero', role: 'repeater', lat: 0, lon: 0 },
      { public_key: 'dd', name: 'NoGps', role: 'room', lat: null, lon: null }
    ]);
    assert(nodes.length === 2 && nodes[0].major && !nodes[1].major, 'normNodes drops (0,0) and missing GPS; repeaters/rooms are major');
    assert(I.resolveQuery('hilltop', nodes).node.pubkey === 'bb', 'resolveQuery: exact name beats prefix');
    assert(I.resolveQuery('hill', nodes).node.pubkey === 'aa', 'resolveQuery: prefix tie → repeater wins');
    assert(I.resolveQuery('37.7749, -122.4194', nodes).node === null && I.resolveQuery('37.7749 -122.4194').lat === 37.7749, 'resolveQuery: coordinates → free point');
    assert(I.resolveQuery('nothing', nodes) === null && I.resolveQuery('', nodes) === null && I.resolveQuery('95,0', nodes) === null, 'resolveQuery: no match / empty / out of range → null');

    const cfg = I.normCfg({ defaultRangeKm: 45, rangesKm: [10, 30], newAntennaM: 'x', maxPoints: 16 });
    assert(cfg.defaultRangeKm === 10 && eq(cfg.rangesKm, [10, 30]) && cfg.newAntennaM === 10, 'normCfg: default range must be offered, bad numbers fall back');
    assert(I.parseRetryAfter('30') === 30 && I.parseRetryAfter(null) === null && I.parseRetryAfter('soon') === null, 'parseRetryAfter: seconds, missing, junk');
    assert(I.roleClass('repeater') === 'repeater' && I.roleClass('x" onload="y') === 'other', 'roleClass: only known roles become class names');
  }

  console.log('\n=== fetchJob: 202, 202, 200 ===');
  {
    const T = fakeTimers();
    const f = makeFetch([
      res(202, { state: 'loading_terrain', progress: 0.25, retry_after_s: 0.2 }),
      res(202, { state: 'computing', progress: 0.8, retry_after_s: 30 }),
      res(200, { source: 'SRTM', legs: [] })
    ]);
    const { MP: P } = load({ timers: T, fetch: f });
    const progress = [];
    let out = null, error = null;
    const url = '/api/planner/relay?a=1,2&b=3,4';
    P.fetchJob(url, { onProgress: (s, p) => progress.push([s, p]) }).then(d => { out = d; }, e => { error = e; });
    await flush();
    assert(f.calls.length === 1, 'first GET sent at once');
    assert(T.pending().length === 1 && T.pending()[0].ms === 1000, 'retry_after_s 0.2 → clamped up to 1 s (got ' + (T.pending()[0] || {}).ms + ')');
    assert(eq(progress[0], ['loading_terrain', 0.25]), 'onProgress(state, progress) on the first 202');
    T.runNext();
    await flush();
    assert(f.calls.length === 2 && T.pending().length === 1 && T.pending()[0].ms === 5000, 'retry_after_s 30 → clamped down to 5 s');
    assert(eq(progress[1], ['computing', 0.8]), 'onProgress on the second 202');
    T.runNext();
    await flush();
    assert(f.calls.length === 3, 'polled exactly three times');
    assert(f.calls.every(c => c.url === url), 'every poll re-requests the SAME url');
    assert(out && out.source === 'SRTM' && !error, 'resolves with the 200 body');
    assert(T.pending().length === 0, 'no timer left behind');
  }

  console.log('\n=== fetchJob: 202 without retry_after_s, odd progress ===');
  {
    const T = fakeTimers();
    const f = makeFetch([res(202, { progress: 7 }), res(200, { ok: 1 })]);
    const { MP: P } = load({ timers: T, fetch: f });
    const progress = [];
    const p = P.fetchJob('/x', { onProgress: (s, v) => progress.push([s, v]) });
    await flush();
    assert(T.pending()[0].ms === 1000, 'missing retry_after_s → 1 s');
    assert(eq(progress[0], ['queued', 1]), 'missing state → "queued", progress clamped to 0..1');
    T.runNext();
    assert((await p).ok === 1, 'then resolves');
  }

  console.log('\n=== fetchJob: errors ===');
  {
    const T = fakeTimers();
    const f = makeFetch([res(429, { error: 'rate limited' }, { 'Retry-After': '30' })]);
    const { MP: P } = load({ timers: T, fetch: f });
    let e = null;
    await P.fetchJob('/x').catch(x => { e = x; });
    assert(e && e.status === 429 && e.retryAfter === 30 && e.message === 'rate limited', '429 rejects with {status, retryAfter, message}');
    assert(T.pending().length === 0 && f.calls.length === 1, '429 is not retried by fetchJob');
  }
  {
    const f = makeFetch([res(503, { error: 'busy' }, { 'Retry-After': '5' })]);
    const { MP: P } = load({ fetch: f });
    let e = null;
    await P.fetchJob('/x').catch(x => { e = x; });
    assert(e && e.status === 503 && e.retryAfter === 5, '503 rejects with retryAfter');
  }
  {
    const f = makeFetch([res(400, { error: 'pts: need 2 points' })]);
    const { MP: P, I: I2 } = load({ fetch: f });
    let e = null;
    await P.fetchJob('/x').catch(x => { e = x; });
    assert(e && e.status === 400 && e.retryAfter === null && e.message === 'pts: need 2 points', '400 rejects with the server message, no retryAfter');
    assert(/need 2 points/.test(I2.jobErrorText(e)), 'jobErrorText shows a 400 message');
    assert(/Try again in 30 s/.test(I2.jobErrorText({ status: 429, retryAfter: 30 })), 'jobErrorText: 429 says when to retry');
    assert(/connection/.test(I2.jobErrorText({ status: 0 })), 'jobErrorText: network failure');
    assert(/more terrain tiles/.test(I2.jobErrorText({ status: 400, message: 'too_many_tiles' })), 'jobErrorText: too_many_tiles has its own message');
  }
  {
    const f = makeFetch([res(404, undefined)]);
    const { MP: P } = load({ fetch: f });
    let e = null;
    await P.fetchJob('/x').catch(x => { e = x; });
    assert(e && e.status === 404 && /404/.test(e.message), '404 with no JSON body still rejects with status');
  }
  {
    const f = makeFetch([new TypeError('Failed to fetch')]);
    const { MP: P } = load({ fetch: f });
    let e = null;
    await P.fetchJob('/x').catch(x => { e = x; });
    assert(e && e.status === 0 && e.name !== 'AbortError', 'network failure rejects with status 0');
  }
  {
    const f = makeFetch([res(200, undefined)]);
    const { MP: P } = load({ fetch: f });
    let e = null;
    await P.fetchJob('/x').catch(x => { e = x; });
    assert(e && e.status === 200, '200 without a JSON body rejects');
  }

  console.log('\n=== fetchJob: abort ===');
  {
    const T = fakeTimers();
    const f = makeFetch([res(202, { state: 'computing', retry_after_s: 2 }), res(200, { never: true })]);
    const { MP: P } = load({ timers: T, fetch: f });
    const ac = new AbortController();
    let e = null, out = null;
    P.fetchJob('/x', { signal: ac.signal }).then(d => { out = d; }, x => { e = x; });
    await flush();
    assert(f.calls.length === 1 && T.pending().length === 1, 'waiting for the next poll');
    assert(f.calls[0].opts.signal === ac.signal, 'the signal is passed to fetch');
    ac.abort();
    await flush();
    assert(e && e.name === 'AbortError' && !out, 'abort rejects with AbortError');
    assert(T.pending().length === 0, 'abort clears the pending poll');
    T.runNext();
    await flush();
    assert(f.calls.length === 1, 'no poll after abort');
  }
  {
    // Abort while a GET is in flight: the late response is ignored.
    const T = fakeTimers();
    let release;
    const f = makeFetch([() => new Promise(r => { release = r; })]);
    const { MP: P } = load({ timers: T, fetch: f });
    const ac = new AbortController();
    let e = null;
    P.fetchJob('/x', { signal: ac.signal }).catch(x => { e = x; });
    await flush();
    ac.abort();
    release(res(202, { retry_after_s: 1 }));
    await flush();
    assert(e && e.name === 'AbortError', 'abort mid-request rejects with AbortError');
    assert(T.pending().length === 0 && f.calls.length === 1, 'a 202 arriving after abort schedules nothing');
  }
  {
    const f = makeFetch([res(200, {})]);
    const { MP: P } = load({ fetch: f });
    const ac = new AbortController();
    ac.abort();
    let e = null;
    await P.fetchJob('/x', { signal: ac.signal }).catch(x => { e = x; });
    assert(e && e.name === 'AbortError' && f.calls.length === 0, 'an already-aborted signal never fetches');
  }

  // Node names come off the mesh (anyone can name a node). Every HTML
  // builder in planner.js must render them as inert text: list rows, the
  // relay rows, the direct-link card, tooltips and the profile panel titles.
  console.log('\n=== planner.js HTML builders keep hostile node names inert ===');
  {
    const EVIL_A = '"><img src=x onerror=alert(1)>';
    const EVIL_B = "' onfocus=alert(2) x='<script>alert(3)</script>";
    const nodeA = { name: EVIL_A, role: 'repeater', pubkey: 'aa' };
    const nodeB = { name: EVIL_B, role: EVIL_A, pubkey: 'bb' };
    const pts = [
      { lat: 37.78406, lon: -122.42401, h: 5, node: nodeA },
      { lat: 37.76047, lon: -122.41755, h: 10, node: nodeB }
    ];
    const leg = I.legsFromPoints(pts)[0];
    const elev = flat(10); elev[50] = 900; // a ridge, so the obstruction sentence renders
    const an = LOS.analyze({ distanceKm: 2.7, elevM: elev, antennaAM: 5, antennaBM: 10, freqMHz: 910.525, kFactor: 4 / 3 });
    const link = { distance_km: 4.2, clearance_pct: -30, verdict: 'blocked', worst_km: 1.1, obstruction_m: 40, elev_m: flat(5) };
    const opt = { kind: 'existing', pubkey: 'cc', name: EVIL_A, lat: 37.77, lon: -122.42, elev_m: 120, antenna_m: 5, leg_a: link, leg_b: link, bottleneck_pct: -30 };
    const profLeg = { a: leg.a, b: leg.b, ha: 5, hb: 10, an: an, distanceKm: 2.7 };
    const outputs = {
      pointRow: I.pointRowHtml(pts[0], 0, I.legRowHtml(leg, an, false)),
      pointRow2: I.pointRowHtml(pts[1], 1, ''),
      legRowLoading: I.legRowHtml(leg, null, true),
      legRowError: I.legRowHtml(leg, { error: 'too_long' }, false),
      endRow: I.endRowHtml('a', { lat: 37.1, lon: -122.1, node: nodeB }, true),
      optionRow: I.optionRowHtml(opt, 'existing', 0, true),
      direct: I.directHtml(link, EVIL_A, EVIL_B),
      tooltip: I.nodeTooltipHtml(nodeB),
      profile: I.profileSectionHtml(profLeg, 0),
      profileLoading: I.profileSectionHtml({ a: leg.a, b: leg.b, an: null, distanceKm: 2.7 }, 1),
      profileError: I.profileSectionHtml({ a: leg.a, b: leg.b, an: null, error: 'no_terrain', distanceKm: 2.7 }, 2),
      readout: I.readoutHtml(profLeg, an, 50),
      serverError: I.errorHtml(I.jobErrorText({ status: 400, message: EVIL_A }), 'chain')
    };
    // Escaped text still contains " onerror=" as characters, so check structure:
    // every real tag must be well formed (double-quoted attribute values only)
    // and carry no on* attribute. A name that broke out of a text node or an
    // attribute value would leave a malformed tag, an extra tag, or an on* attr.
    const TAG_RE = /<[^>]*>/g;
    const WELL_FORMED = /^<\/?[a-zA-Z][\w:-]*(\s+[\w:-]+="[^"<]*")*\s*\/?>$/;
    Object.keys(outputs).forEach(k => {
      const html = outputs[k];
      const tags = html.match(TAG_RE) || [];
      const bad = tags.filter(t => !WELL_FORMED.test(t) || /\son[a-z]+="/i.test(t) || /^<\/?(img|script)\b/i.test(t));
      assert(bad.length === 0, k + ': hostile name produces no live tag or handler' + (bad.length ? ' (got ' + bad[0] + ')' : ''));
    });
    assert(outputs.pointRow.includes('&quot;&gt;&lt;img src=x onerror=alert(1)&gt;'), 'point row shows the name as escaped text');
    assert(outputs.optionRow.includes('&lt;img src=x onerror=alert(1)&gt;'), 'relay option row shows the name as escaped text');
    assert(/<h3 class="mc-pl-prof-title"[^>]*>&quot;&gt;&lt;img/.test(outputs.profile), 'profile panel title "A → B" escapes the name');
    assert(outputs.profile.includes('Terrain rises') && outputs.profile.includes('&lt;img'), 'obstruction sentence and SVG labels escape the name');
    assert(outputs.direct.includes('&lt;script&gt;'), 'direct-link card escapes both names');
    assert(outputs.serverError.includes('&lt;img'), 'a server error message is escaped');
    // The panel heading (showProfiles title) is set with textContent, never innerHTML.
    assert(/title\.textContent = P\.title;/.test(PLANNER_SRC) && !/\.mc-los-title'\)\.innerHTML/.test(PLANNER_SRC), 'panel heading is written with textContent');
  }

  console.log('\n=== loadNodes: fetchAllNodes pages through every node ===');
  {
    // Any property/call on the stub returns another stub, so mount() can run without a DOM or Leaflet.
    const stub = () => new Proxy(function () {}, {
      get: (t, k) => (k === Symbol.toPrimitive ? () => 0 : k === 'then' ? undefined
        : k === 'length' ? 0 : /^(parent|next|previous|first|last)/.test(String(k)) ? null
        : (k in t ? t[k] : stub())),
      set: () => true,
      apply: () => stub(),
      construct: () => stub()
    });
    const repeaters = Array.from({ length: 2500 }, (_, i) => ({
      public_key: 'pk' + String(i).padStart(4, '0'), name: 'R' + i, role: 'repeater',
      lat: 40 + (i % 100) / 1000, lon: -120 + (i % 100) / 1000
    }));
    const mountWith = (globals, fetchImpl) => {
      const pages = {};
      const timers = fakeTimers();
      const { I: II, ctx: cx } = load({
        timers, fetch: fetchImpl,
        globals: Object.assign({
          L: stub(), document: stub(), MC_PLANNER_CFG: {},
          addEventListener() {}, removeEventListener() {},
          registerPage: (name, obj) => { pages[name] = obj; }
        }, globals)
      });
      pages.planner.init(stub());
      return { II, cx, pages };
    };

    const calls = [];
    const fakeFetchAll = (q, o) => { calls.push({ q, o }); return Promise.resolve({ nodes: repeaters, counts: {}, total: repeaters.length }); };
    const r1 = mountWith({ fetchAllNodes: fakeFetchAll, api: undefined }, makeFetch([]));
    await flush(); await flush();
    const st1 = r1.II.state();
    assert(calls.length === 1, 'loadNodes calls fetchAllNodes once');
    assert(calls[0] && calls[0].q === '' && calls[0].o && calls[0].o.ttl === 60000, 'fetchAllNodes gets empty extra query and the nodeList ttl');
    assert(st1 && st1.nodesState === 'ready', 'planner reaches nodesState ready');
    assert(st1 && st1.rawNodes.length === 2500, 'planner node list holds all 2500 repeaters');
    assert(r1.cx.fetch.calls.filter(c => c.url.indexOf('/api/nodes') === 0).length === 0, 'fetchAllNodes path makes no direct /api/nodes fetch');

    // Route by URL: mount() also fetches /api/config/map, which must not consume the nodes response.
    const routed = (url) => url.indexOf('/api/nodes') === 0
      ? Promise.resolve(res(200, { nodes: repeaters.slice(0, 3) }))
      : Promise.reject(new Error('not routed: ' + url));
    const fb = mountWith({ fetchAllNodes: undefined }, routed);
    await flush(); await flush();
    const st2 = fb.II.state();
    assert(st2 && st2.nodesState === 'ready' && st2.rawNodes.length === 3, 'fallback: without fetchAllNodes, the single /api/nodes fetch still loads');
  }

  console.log('\n=== source checks ===');
  {
    assert(/registerPage\('planner', \{ init: init, destroy: destroy \}\)/.test(PLANNER_SRC), 'registers page "planner" with init/destroy');
    const code = PLANNER_SRC.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '');
    assert(!/location\.hash\s*=[^=]/.test(code), 'never assigns location.hash (app.js re-inits on hashchange)');
    assert((PLANNER_SRC.match(/\/api\/nodes\?limit=2000/g) || []).length >= 1 && !/\/api\/nodes\/'/.test(PLANNER_SRC), 'one bulk /api/nodes fetch, no per-node requests');
    const css = read('planner.css');
    const hexes = (css.replace(/var\(--[\w-]+,\s*#[0-9a-fA-F]{3,8}\)/g, '').match(/#[0-9a-fA-F]{3,6}\b/g) || []);
    assert(hexes.length === 0, 'planner.css: no raw hex colours outside var(--x, #fallback)');
  }

  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error(e); process.exit(1); });
