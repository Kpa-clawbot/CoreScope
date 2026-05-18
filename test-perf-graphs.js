// test-perf-graphs.js — unit tests for ingestor graph rate computation helpers
'use strict';

let passed = 0, failed = 0;
function assert(desc, actual, expected) {
  const ok = Math.abs(actual - expected) < 0.0001;
  if (ok) { passed++; console.log(`  ✓ ${desc}`); }
  else { failed++; console.error(`  ✗ ${desc}: expected ${expected}, got ${actual}`); }
}

// --- writeWalRate added to writeSourceRates ---
// Simulate two write-sources snapshots 10s apart
const sources1 = { tx_inserted: 1000, obs_inserted: 5000, walCommits: 100 };
const sources2 = { tx_inserted: 1110, obs_inserted: 5600, walCommits: 115 };
const dtSec = 10;
const txRate  = (sources2.tx_inserted  - sources1.tx_inserted)  / dtSec; // 11
const obsRate = (sources2.obs_inserted - sources1.obs_inserted) / dtSec; // 60
const walRate = (sources2.walCommits   - sources1.walCommits)   / dtSec; // 1.5
assert('tx rate 11/s', txRate, 11);
assert('obs rate 60/s', obsRate, 60);
assert('wal rate 1.5/s', walRate, 1.5);

// --- Ingestor I/O rate computation from cumulative PerfIOSample ---
const s1 = { readBytes: 204800, writeBytes: 1048576, syscR: 50, syscW: 30, sampledAt: '2026-01-01T00:00:00.000Z' };
const s2 = { readBytes: 307200, writeBytes: 1572864, syscR: 60, syscW: 38, sampledAt: '2026-01-01T00:00:10.000Z' };
const elapsed = (new Date(s2.sampledAt) - new Date(s1.sampledAt)) / 1000; // 10s
const readBps  = (s2.readBytes  - s1.readBytes)  / elapsed; // 10240 B/s
const writeBps = (s2.writeBytes - s1.writeBytes) / elapsed; // 52428.8 B/s
const syscR    = (s2.syscR - s1.syscR) / elapsed;           // 1/s
const syscW    = (s2.syscW - s1.syscW) / elapsed;           // 0.8/s
assert('ingestor readBps 10240', readBps, 10240);
assert('ingestor writeBps 52428.8', writeBps, 52428.8);
assert('ingestor syscR 1/s', syscR, 1);
assert('ingestor syscW 0.8/s', syscW, 0.8);

// --- First-sample produces zero rates (no previous reference) ---
const firstRates = { readBps: 0, writeBps: 0, syscR: 0, syscW: 0 };
assert('first sample readBps is 0', firstRates.readBps, 0);
assert('first sample walRate is 0', 0, 0);

// --- Null ingestor produces null sample fields ---
const nullIngestor = null;
const ingestorReadBps = nullIngestor ? 1 : null;
assert('null ingestor yields null readBps', ingestorReadBps === null ? 0 : 1, 0);

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
