/**
 * Unit tests for HopResolver.inferPath() — whole-path hop inference for one
 * observation. Every node here is synthetic.
 *
 * Firmware facts the model relies on (firmware/src/Mesh.cpp routeRecvPacket +
 * wasSeen): a flood relayer appends its own hash, so the last hop is the one
 * the observer heard directly; and a node forwards a packet once, so a node
 * appears at most once in a path.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const vm = require('vm');

const code = fs.readFileSync(REPO_ROOT + '/public/hop-resolver.js', 'utf8');
const sandbox = { window: {}, console: { debug() {}, log: console.log, warn: console.warn, error: console.error } };
vm.createContext(sandbox);
vm.runInContext(code, sandbox);
const HopResolver = sandbox.window.HopResolver;

let passed = 0;
let failed = 0;
function assert(condition, msg) {
  if (condition) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

// 0.25° of latitude ≈ 28 km, a comfortable single LoRa hop.
const STEP = 0.25;
function rpt(pubkey, name, lat, lon, role) {
  return { public_key: pubkey, name, role: role || 'repeater', lat, lon };
}
const at = (i) => 40 + i * STEP;
const pk = (entry) => entry && entry.pubkey;

console.log('\n=== HopResolver.inferPath ===\n');

console.log('Test 1: the same prefix at two positions resolves independently');
{
  const a1 = rpt('aa01' + '0'.repeat(60), 'A-near-start', at(0), -100);
  const a2 = rpt('aa02' + '0'.repeat(60), 'A-near-observer', at(3), -100);
  const b1 = rpt('b100' + '0'.repeat(60), 'B1', at(1), -100);
  const b2 = rpt('b200' + '0'.repeat(60), 'B2', at(2), -100);
  HopResolver.init([a1, a2, b1, b2]);
  HopResolver.setAffinity(null);
  const out = HopResolver.inferPath(['AA', 'B1', 'B2', 'AA'], { observer: { lat: at(4), lon: -100 } });
  assert(Array.isArray(out) && out.length === 4, 'returns one entry per hop');
  assert(pk(out[0]) === a1.public_key, 'hop 0 is the AA node near the start — got ' + (out[0] && out[0].name));
  assert(pk(out[3]) === a2.public_key, 'hop 3 is the AA node near the observer — got ' + (out[3] && out[3].name));
  assert(out[0].hop === 'AA' && out[3].hop === 'AA', 'each entry carries its hop prefix');
  const other = out[0].candidates.find(c => c.pubkey === a2.public_key);
  assert(other && other.usedAt === 3 && other.confidence === 0,
    'a node the path uses at another hop is no alternative, and says where it is used');
  assert(out[0].confidence > 0.7, 'so hop 0 is confident — ' + out[0].confidence.toFixed(2));
}

console.log('\nTest 2: the last hop is anchored to the observer');
{
  const near = rpt('cc01' + '0'.repeat(60), 'C-near', at(0), -100);
  const far = rpt('cc02' + '0'.repeat(60), 'C-far', at(6), -100);
  HopResolver.init([far, near]);
  const out = HopResolver.inferPath(['CC'], { observer: { lat: at(0) - STEP / 2, lon: -100 } });
  assert(pk(out[0]) === near.public_key, 'picks the candidate within range of the observer — got ' + out[0].name);
  assert(out[0].confidence > 0.7, 'and is confident about it — ' + out[0].confidence.toFixed(2));
  assert(out[0].ambiguous === true, 'still flagged as having more than one candidate');
}

console.log('\nTest 3: a lone candidate far from both neighbours is reported as unlisted');
{
  const d1 = rpt('d100' + '0'.repeat(60), 'D1', at(0), -100);
  const d2 = rpt('d200' + '0'.repeat(60), 'D2', at(2), -100);
  const farE = rpt('ee00' + '0'.repeat(60), 'E-far-away', at(0) + 9, -100); // ~1000 km
  HopResolver.init([d1, d2, farE]);
  const out = HopResolver.inferPath(['D1', 'EE', 'D2'], { observer: { lat: at(3), lon: -100 } });
  assert(out[1].unlisted === true, 'EE is unlisted');
  assert(out[1].pubkey == null && out[1].name == null, 'and carries no node identity');
  assert(out[1].unreliable === true, 'a located candidate exists but was rejected → unreliable');
  assert(out[1].candidates.length === 1 && out[1].candidates[0].pubkey === farE.public_key,
    'the rejected candidate is still listed');
  assert(out[1].candidates[0].confidence < 0.1, 'with a low confidence — ' + out[1].candidates[0].confidence.toFixed(3));
  assert(pk(out[0]) === d1.public_key && pk(out[2]) === d2.public_key, 'its neighbours still resolve');
}

console.log('\nTest 4: a node never appears twice in one path');
{
  const a1 = rpt('ab01' + '0'.repeat(60), 'AB-1', at(0), -100);
  const a2 = rpt('ab02' + '0'.repeat(60), 'AB-2', at(1), -100);
  HopResolver.init([a1, a2]);
  const out = HopResolver.inferPath(['AB', 'AB', 'AB'], { observer: { lat: at(1), lon: -100 } });
  const picked = out.map(pk).filter(Boolean);
  assert(new Set(picked).size === picked.length, 'no pubkey repeats — got ' + out.map(e => e.name).join(','));
  assert(pk(out[2]) === a2.public_key, 'the hop beside the observer keeps the nearer node');
  assert(out.some(e => e.unlisted), 'with two nodes for three hops, one hop is unlisted');
}

console.log('\nTest 5: a neighbour-graph link breaks a geographic tie');
{
  // Two F3 candidates mirror each other across the G0–G2 line: equal distances.
  const left = rpt('f300' + '0'.repeat(60), 'F3-left', at(1), -100.3);
  const right = rpt('f311' + '0'.repeat(60), 'F3-right', at(1), -99.7);
  const g0 = rpt('9000' + '0'.repeat(60), 'G0', at(0), -100);
  const g2 = rpt('9200' + '0'.repeat(60), 'G2', at(2), -100);
  const obs = { observer: { lat: at(3), lon: -100 } };
  HopResolver.init([left, right, g0, g2]);
  HopResolver.setAffinity(null);
  const noLink = HopResolver.inferPath(['90', 'F3', '92'], obs);
  assert(noLink[1].confidence < 0.7, 'without a link the tie is not confident — ' + noLink[1].confidence.toFixed(2));
  HopResolver.setAffinity({ edges: [{ source: g0.public_key, target: right.public_key, score: 1 }] });
  const linked = HopResolver.inferPath(['90', 'F3', '92'], obs);
  assert(pk(linked[1]) === right.public_key, 'the linked candidate wins — got ' + linked[1].name);
  assert(linked[1].confidence > noLink[1].confidence, 'and confidence rises');
  HopResolver.setAffinity(null);
  const unknownPrefix = HopResolver.inferPath(['90', 'E7', '92'], obs);
  assert(unknownPrefix[1].unlisted === true && unknownPrefix[1].candidates.length === 0,
    'a prefix with no candidates is unlisted');
}

console.log('\nTest 6: an observer without GPS (null or 0,0) falls back to its IATA region');
{
  const nearZero = rpt('7a01' + '0'.repeat(60), 'Near-0,0', 1, 1);
  const inRegion = rpt('7a02' + '0'.repeat(60), 'In-region', 30.2, -97.7);
  const opts = { observers: [{ id: 'OBS1', iata: 'XYZ' }], iataCoords: { XYZ: { lat: 30.3, lon: -97.7 } } };
  HopResolver.init([nearZero, inRegion], opts);
  const zero = HopResolver.inferPath(['7A'], { observer: { lat: 0, lon: 0 }, observerId: 'OBS1' });
  assert(pk(zero[0]) === inRegion.public_key, '0,0 is ignored, the region anchors — got ' + zero[0].name);
  const none = HopResolver.inferPath(['7A'], { observer: null, observerId: 'OBS1' });
  assert(pk(none[0]) === inRegion.public_key, 'null position → region anchors — got ' + none[0].name);
  const nothing = HopResolver.inferPath(['7A'], {});
  assert(!!pk(nothing[0]) && nothing[0].confidence <= 0.6, 'no anchor at all still picks a node, without confidence');
}

console.log('\nTest 7: a server-resolved pubkey restricts that hop');
{
  const h1 = rpt('4401' + '0'.repeat(60), 'H-near', at(0), -100);
  const h2 = rpt('4402' + '0'.repeat(60), 'H-server', at(1), -100);
  HopResolver.init([h1, h2]);
  const out = HopResolver.inferPath(['44'], { observer: { lat: at(0), lon: -100 }, resolvedPath: [h2.public_key] });
  assert(pk(out[0]) === h2.public_key, 'the server pick is kept over the geographically nearer node');
  const mismatch = HopResolver.inferPath(['44', '44'], { observer: { lat: at(0), lon: -100 }, resolvedPath: [h2.public_key] });
  assert(mismatch.length === 2, 'a resolvedPath of the wrong length is ignored, not fatal');
}

console.log('\nTest 8: the origin anchors the first hop');
{
  const n1 = rpt('5501' + '0'.repeat(60), 'N-north', at(8), -100);
  const n2 = rpt('5502' + '0'.repeat(60), 'N-south', at(0), -100);
  HopResolver.init([n1, n2]);
  const out = HopResolver.inferPath(['55'], { origin: { lat: at(0) - STEP, lon: -100 } });
  assert(pk(out[0]) === n2.public_key, 'picks the candidate near the sender — got ' + out[0].name);
}

console.log('\nTest 9: a candidate without GPS is named when it is the only one');
{
  const noGps = { public_key: '6600' + '0'.repeat(60), name: 'NoGPS', role: 'repeater', lat: null, lon: null };
  HopResolver.init([noGps]);
  const out = HopResolver.inferPath(['66'], { observer: { lat: 40, lon: -100 } });
  assert(out[0].name === 'NoGPS' && out[0].pubkey === noGps.public_key, 'named');
  assert(out[0].located === false && out[0].unlisted === true, 'but marked unplaced');
}

console.log('\nTest 10: edge cases and output shape');
{
  HopResolver.init([rpt('7700' + '0'.repeat(60), 'Only', 40, -100), rpt('7701' + '0'.repeat(60), 'Other', 40.1, -100)]);
  assert(HopResolver.inferPath([], {}).length === 0, 'empty path → []');
  assert(HopResolver.inferPath(null).length === 0, 'null path → []');
  const out = HopResolver.inferPath(['77'], { observer: { lat: 40, lon: -100 } });
  const sum = out[0].candidates.reduce((s, c) => s + c.confidence, 0);
  assert(sum <= 1 + 1e-9, 'candidate confidences sum to at most 1 — ' + sum.toFixed(3));
  assert(out[0].candidates[0].confidence >= out[0].candidates[1].confidence, 'candidates sorted by confidence');
  assert(out[0].conflicts === out[0].candidates, 'conflicts mirrors candidates for HopDisplay');
  const comp = rpt('7800' + '0'.repeat(60), 'Companion', 40, -100, 'companion');
  HopResolver.init([comp]);
  assert(HopResolver.inferPath(['78'], {})[0].unlisted, 'companions never appear as hops');
}

console.log('\nTest 11: performance — 64 hops × 16 candidates');
{
  // A vm sandbox routes every global lookup (Math, Infinity) through an
  // interceptor a browser does not have, which inflates timings ~10×. Load
  // a second copy into this context to time what a browser would run.
  // (init() reads window, so the global stays for the rest of this test.)
  global.window = {};
  vm.runInThisContext(code, { filename: 'hop-resolver.js' });
  const Fast = global.window.HopResolver;

  // Deterministic pseudo-random layout in a ~300 km box.
  let seed = 12345;
  const rnd = () => (seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff;
  const nodes = [];
  for (let b = 0; b < 64; b++) {
    for (let k = 0; k < 16; k++) {
      const hex = b.toString(16).padStart(2, '0') + k.toString(16).padStart(2, '0');
      nodes.push(rpt(hex + '0'.repeat(60), 'P' + hex, 30 + rnd() * 2.7, -98 + rnd() * 3));
    }
  }
  Fast.init(nodes);
  const hops = [];
  for (let i = 0; i < 64; i++) hops.push(((i * 7) % 64).toString(16).padStart(2, '0'));
  const opts = { observer: { lat: 31, lon: -97 } };
  for (let w = 0; w < 3; w++) Fast.inferPath(hops, opts); // let the JIT settle
  const times = [];
  for (let r = 0; r < 5; r++) {
    const t0 = process.hrtime.bigint();
    Fast.inferPath(hops, opts);
    times.push(Number(process.hrtime.bigint() - t0) / 1e6);
  }
  times.sort((a, b) => a - b);
  const median = times[2];
  console.log('    median ' + median.toFixed(1) + ' ms over 5 runs (target 20 ms)');
  // Headroom over the target for slower CI runners.
  assert(median < 40, 'median under 40 ms — ' + median.toFixed(1) + ' ms');
}

console.log('\n' + passed + ' passed, ' + failed + ' failed');
if (failed) process.exit(1);
