'use strict';
// Checks public/los.js against the vectors the Go port (cmd/server/planner_los.go)
// is also tested with, so server and browser verdicts cannot drift.
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ctx = { window: {} };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(__dirname, '../../public/los.js'), 'utf8'), ctx);
const L = ctx.window.MeshLOS;
const vectors = JSON.parse(fs.readFileSync(path.join(__dirname, '../fixtures/planner-los-vectors.json'), 'utf8'));
assert(vectors.length >= 6, 'need >= 6 vectors');

const near = (a, b) => Math.abs(a - b) <= 1e-9;
let failed = 0;
for (const v of vectors) {
  try {
    const r = L.analyze(v.input);
    const e = v.expect;
    assert(r, 'analyze returned null');
    if (e.clearance === null) assert.strictEqual(r.clearance, Infinity);
    else assert(near(r.clearance, e.clearance), 'clearance ' + r.clearance);
    if (e.obstructionM === null) assert.strictEqual(r.obstructionM, -Infinity);
    else assert(near(r.obstructionM, e.obstructionM), 'obstructionM ' + r.obstructionM);
    if (e.worstKm !== null) assert(near(r.worstKm, e.worstKm), 'worstKm ' + r.worstKm);
    assert.strictEqual(r.worstIdx, e.worstIdx);
    assert.strictEqual(r.verdict, e.verdict);
    assert(near(r.distanceKm, e.distanceKm));
    console.log('  ok  ' + v.name);
  } catch (err) {
    failed++;
    console.error('  FAIL ' + v.name + ': ' + err.message);
  }
}
console.log(failed ? failed + ' failed' : 'all ' + vectors.length + ' vectors passed');
process.exit(failed ? 1 : 0);
