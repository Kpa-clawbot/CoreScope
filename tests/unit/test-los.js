/** Radio LOS maths + profile SVG: loads the real public/los.js. */
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
const near = (a, b, tol) => Math.abs(a - b) <= tol;

const ctx = { window: {} };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public', 'los.js'), 'utf8'), ctx);
const L = ctx.window.MeshLOS;

console.log('\n=== geometry ===');
assert(near(L.haversineKm({ lat: 0, lon: 0 }, { lat: 0, lon: 1 }), 111.195, 0.01), 'haversine 1 deg of longitude at equator ~111.195 km');
assert(near(L.bearingDeg({ lat: 0, lon: 0 }, { lat: 1, lon: 0 }), 0, 1e-9), 'bearing north = 0');
assert(near(L.bearingDeg({ lat: 0, lon: 0 }, { lat: 0, lon: 1 }), 90, 1e-9), 'bearing east = 90');
const mid = L.intermediatePoint({ lat: 0, lon: 0 }, { lat: 0, lon: 90 }, 0.5);
assert(near(mid.lat, 0, 1e-9) && near(mid.lon, 45, 1e-9), 'intermediate midpoint (0,0)->(0,90) = (0,45)');
const p0 = L.intermediatePoint({ lat: 10, lon: 20 }, { lat: 30, lon: 40 }, 0);
const p1 = L.intermediatePoint({ lat: 10, lon: 20 }, { lat: 30, lon: 40 }, 1);
assert(near(p0.lat, 10, 1e-9) && near(p0.lon, 20, 1e-9), 'intermediate t=0 is A');
assert(near(p1.lat, 30, 1e-9) && near(p1.lon, 40, 1e-9), 'intermediate t=1 is B');
const same = L.intermediatePoint({ lat: 5, lon: 6 }, { lat: 5, lon: 6 }, 0.5);
assert(same.lat === 5 && same.lon === 6, 'intermediate of identical points returns a copy');

console.log('\n=== radio formulas ===');
assert(near(L.earthBulgeM(10, 20, 4 / 3), 5.886, 0.001), 'earthBulgeM(10,20,4/3) ~ 5.886');
assert(near(L.fresnelRadiusM(5, 10, 910.525), 28.70, 0.01), 'fresnelRadiusM(5,10,910.525) ~ 28.70');
assert(near(L.fsplDb(10, 910.525), 111.64, 0.01), 'fsplDb(10,910.525) ~ 111.64');

console.log('\n=== verdicts ===');
assert(L.verdictFor(1) === 'clear', '1 -> clear');
assert(L.verdictFor(0.999) === 'clear60', '0.999 -> clear60');
assert(L.verdictFor(0.6) === 'clear60', '0.6 -> clear60');
assert(L.verdictFor(0.599) === 'marginal', '0.599 -> marginal');
assert(L.verdictFor(0) === 'marginal', '0 -> marginal');
assert(L.verdictFor(-0.01) === 'blocked', '-0.01 -> blocked');
assert(L.verdictFor(Infinity) === 'clear', 'Infinity -> clear');

console.log('\n=== analyze ===');
const base = { distanceKm: 10, antennaAM: 30, antennaBM: 30, freqMHz: 910.525 };
const flat = L.analyze(Object.assign({ elevM: new Array(21).fill(100) }, base, { distanceKm: 5 }));
assert(flat && flat.verdict === 'clear', 'flat terrain with 30 m masts is clear');
assert(flat.n === 21 && flat.terrainM.length === 21 && flat.losM.length === 21 && flat.fresnelM.length === 21, 'arrays sized N');
assert(near(flat.kFactor, 4 / 3, 1e-12), 'default k = 4/3');
assert(near(flat.fsplDb, L.fsplDb(5, 910.525), 1e-12), 'analysis fsplDb matches helper');
const hill = new Array(21).fill(100); hill[10] = 400;
const hl = L.analyze(Object.assign({ elevM: hill }, base));
assert(hl.verdict === 'blocked', 'hill in the middle is blocked');
assert(hl.worstIdx === 10, 'worstIdx at the hill');
assert(near(hl.worstT, 0.5, 1e-12) && near(hl.worstKm, 5, 1e-9), 'worstT / worstKm at the hill');
assert(hl.obstructionM > 0, 'obstructionM positive for a hill');
assert(flat.obstructionM < 0, 'obstructionM negative when clear');
const two = L.analyze(Object.assign({ elevM: [100, 120] }, base));
assert(two.clearance === Infinity && two.worstIdx === -1 && two.verdict === 'clear', 'N=2 -> clearance Infinity, worstIdx -1');
assert(L.analyze(Object.assign({ elevM: [100] }, base)) === null, 'one sample -> null');
assert(L.analyze(Object.assign({}, base, { elevM: 'abc' })) === null, 'non-array elevM -> null');
assert(L.analyze(Object.assign({ elevM: [1, NaN, 3] }, base)) === null, 'non-finite sample -> null');
assert(L.analyze(Object.assign({ elevM: [1, 2, 3] }, base, { freqMHz: 0 })) === null, 'freq 0 -> null');
assert(L.analyze(Object.assign({ elevM: [1, 2, 3] }, base, { kFactor: -1 })) === null, 'k <= 0 -> null');
assert(L.analyze(Object.assign({ elevM: [1, 2, 3] }, base, { distanceKm: 0 })).distanceKm === 0.001, 'distance clamped to 0.001');
const nanAnt = L.analyze(Object.assign({ elevM: [1, 2, 3] }, base, { antennaAM: NaN, antennaBM: 'x' }));
assert(nanAnt.antennaAM === 0 && nanAnt.antennaBM === 0, 'NaN antennas -> 0');
const str = L.analyze({ distanceKm: '10', elevM: ['100', '100', '100'], antennaAM: '30', antennaBM: '30', freqMHz: '910.525', kFactor: '1.5' });
assert(str && str.distanceKm === 10 && str.antennaAM === 30 && str.kFactor === 1.5 && typeof str.elevM[0] === 'number', 'string inputs are cast');

console.log('\n=== parity with reference formulas ===');
{
  const D = 23.7, f = 868.1, k = 4 / 3, R = 6371, aA = 12, aB = 25;
  const elev = [];
  for (let i = 0; i < 40; i++) elev.push(150 + 60 * Math.sin(i * 0.7) + (i * 3) % 17);
  const N = elev.length;
  const terr = elev.map((e, i) => { const t = i / (N - 1); return e + (t * D) * (D - t * D) / (2 * k * R) * 1000; });
  const yA = terr[0] + aA, yB = terr[N - 1] + aB;
  let min = Infinity;
  for (let i = 1; i <= N - 2; i++) {
    const t = i / (N - 1);
    const los = yA + (yB - yA) * t;
    const fr = 17.32 * Math.sqrt(Math.max((t * D) * (D - t * D), 1e-9) / (D * f / 1000));
    min = Math.min(min, (los - terr[i]) / fr);
  }
  const an = L.analyze({ distanceKm: D, elevM: elev, antennaAM: aA, antennaBM: aB, freqMHz: f, kFactor: k });
  assert(Math.abs(an.clearance - min) < 1e-9, 'clearance matches reference to 1e-9');
}

console.log('\n=== renderProfileSvg ===');
const svg = L.renderProfileSvg(hl, { nameA: '<script>alert(1)</script>', nameB: 'Bob & "Co" \'x\'' });
assert(svg.indexOf('viewBox="0 0 720 260"') >= 0, 'viewBox present');
assert(svg.indexOf('role="img"') >= 0 && /aria-label="[^"]*km[^"]*Blocked[^"]*%/.test(svg), 'role=img and aria-label with distance, verdict, %');
['mc-los-grid', 'mc-los-tick', 'mc-los-f1', 'mc-los-f60', 'mc-los-ground', 'mc-los-line',
  'mc-los-ant mc-los-ant-a', 'mc-los-ant mc-los-ant-b', 'mc-los-worst mc-los-tone-bad',
  'mc-los-cursor', 'mc-los-hit', 'mc-los-endlabel'].forEach(c => assert(svg.indexOf('class="' + c + '"') >= 0, 'has class ' + c));
const order = ['mc-los-grid', 'mc-los-tick', 'mc-los-f1', 'mc-los-f60', 'mc-los-ground', 'mc-los-line', 'mc-los-ant-a', 'mc-los-worst', 'mc-los-cursor', 'mc-los-hit'].map(c => svg.indexOf(c));
assert(order.every((v, i) => i === 0 || v > order[i - 1]), 'layers in the specified order');
assert(/>\d+ m</.test(svg) && /\d+ km</.test(svg), 'y ticks in m and x ticks in km');
assert(!/#[0-9a-fA-F]{3,6}\b/.test(svg) && svg.indexOf('rgb(') < 0, 'no colour values in svg');
assert(svg.indexOf('<script>') < 0 && svg.indexOf('&lt;script&gt;') >= 0, 'names escaped');
assert(svg.indexOf('visibility="hidden"') >= 0 && svg.indexOf('fill="transparent"') >= 0, 'cursor hidden, hit rect transparent');
assert(L.renderProfileSvg(two, {}).indexOf('mc-los-worst') < 0, 'no worst marker when worstIdx < 0');
assert(L.renderProfileSvg(null) === '', 'null analysis -> empty string');

console.log('\n=== view mapping ===');
assert(near(L.tAtViewX(L.viewXAtT(0.37)), 0.37, 1e-12), 'tAtViewX(viewXAtT(0.37)) ~ 0.37');
assert(L.tAtViewX(-100) === 0 && L.tAtViewX(10000) === 1, 'tAtViewX clamps to [0,1]');
assert(L.viewXAtT(0) === L.PLOT.ML && L.viewXAtT(1) === L.PLOT.W - L.PLOT.MR, 'viewXAtT endpoints');

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed ? 1 : 0);
