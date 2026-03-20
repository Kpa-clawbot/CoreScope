#!/usr/bin/env node
'use strict';

/**
 * Benchmark suite for meshcore-analyzer API endpoints.
 * Tests with cache enabled (warm) and disabled (cold) to measure true compute cost.
 *
 * Usage: node benchmark.js [--base-url http://localhost:3000] [--runs 5] [--json]
 */

const http = require('http');
const https = require('https');

const args = process.argv.slice(2);
const BASE = args.find((a, i) => args[i - 1] === '--base-url') || 'http://127.0.0.1:3000';
const RUNS = Number(args.find((a, i) => args[i - 1] === '--runs') || 5);
const JSON_OUT = args.includes('--json');

const ENDPOINTS = [
  { name: 'Stats', path: '/api/stats' },
  { name: 'Packets (50)', path: '/api/packets?limit=50' },
  { name: 'Packets (100)', path: '/api/packets?limit=100' },
  { name: 'Packets grouped', path: '/api/packets?limit=100&groupByHash=true' },
  { name: 'Packets filtered (type=5)', path: '/api/packets?limit=50&type=5' },
  { name: 'Packets timestamps', path: '/api/packets/timestamps?since=2020-01-01' },
  { name: 'Nodes list', path: '/api/nodes?limit=50' },
  { name: 'Node detail', path: '/api/nodes/__FIRST_NODE__' },
  { name: 'Node health', path: '/api/nodes/__FIRST_NODE__/health' },
  { name: 'Bulk health', path: '/api/nodes/bulk-health?limit=50' },
  { name: 'Network status', path: '/api/nodes/network-status' },
  { name: 'Observers', path: '/api/observers' },
  { name: 'Channels', path: '/api/channels' },
  { name: 'Analytics: RF', path: '/api/analytics/rf' },
  { name: 'Analytics: Topology', path: '/api/analytics/topology' },
  { name: 'Analytics: Channels', path: '/api/analytics/channels' },
  { name: 'Analytics: Hash sizes', path: '/api/analytics/hash-sizes' },
  { name: 'Subpaths (2-hop)', path: '/api/analytics/subpaths?minLen=2&maxLen=2&limit=50' },
  { name: 'Subpaths (3-hop)', path: '/api/analytics/subpaths?minLen=3&maxLen=3&limit=30' },
  { name: 'Subpaths (4-hop)', path: '/api/analytics/subpaths?minLen=4&maxLen=4&limit=20' },
  { name: 'Subpaths (5-8 hop)', path: '/api/analytics/subpaths?minLen=5&maxLen=8&limit=15' },
];

function fetch(url) {
  return new Promise((resolve, reject) => {
    const mod = url.startsWith('https') ? https : http;
    const t0 = process.hrtime.bigint();
    const req = mod.get(url, (res) => {
      let body = '';
      res.on('data', c => body += c);
      res.on('end', () => {
        const ms = Number(process.hrtime.bigint() - t0) / 1e6;
        resolve({ ms, bytes: Buffer.byteLength(body), status: res.statusCode, body });
      });
    });
    req.on('error', reject);
    req.setTimeout(30000, () => { req.destroy(); reject(new Error('timeout')); });
  });
}

function stats(arr) {
  const sorted = [...arr].sort((a, b) => a - b);
  const sum = sorted.reduce((a, b) => a + b, 0);
  return {
    avg: Math.round(sum / sorted.length * 10) / 10,
    min: Math.round(sorted[0] * 10) / 10,
    max: Math.round(sorted[sorted.length - 1] * 10) / 10,
    p50: Math.round(sorted[Math.floor(sorted.length * 0.5)] * 10) / 10,
    p95: Math.round(sorted[Math.floor(sorted.length * 0.95)] * 10) / 10,
  };
}

async function run() {
  // Get first node pubkey for parameterized endpoints
  let firstNode = '';
  try {
    const r = await fetch(`${BASE}/api/nodes?limit=1`);
    const data = JSON.parse(r.body);
    firstNode = data.nodes?.[0]?.public_key || '';
  } catch {}

  const endpoints = ENDPOINTS.map(e => ({
    ...e,
    path: e.path.replace('__FIRST_NODE__', firstNode),
  }));

  const results = [];

  for (const mode of ['cached', 'nocache']) {
    if (!JSON_OUT) {
      console.log(`\n${'='.repeat(70)}`);
      console.log(`  ${mode === 'cached' ? '🟢 CACHE ENABLED (warm)' : '🔴 CACHE DISABLED (cold compute)'}`);
      console.log(`  ${RUNS} runs per endpoint`);
      console.log(`${'='.repeat(70)}`);
      console.log(`${'Endpoint'.padEnd(28)} ${'Avg'.padStart(8)} ${'P50'.padStart(8)} ${'P95'.padStart(8)} ${'Max'.padStart(8)} ${'Size'.padStart(9)}`);
      console.log(`${'-'.repeat(28)} ${'-'.repeat(8)} ${'-'.repeat(8)} ${'-'.repeat(8)} ${'-'.repeat(8)} ${'-'.repeat(9)}`);
    }

    for (const ep of endpoints) {
      const suffix = mode === 'nocache' ? (ep.path.includes('?') ? '&nocache=1' : '?nocache=1') : '';
      const url = `${BASE}${ep.path}${suffix}`;

      // Warm-up run (discard)
      try { await fetch(url); } catch {}

      const times = [];
      let bytes = 0;
      let failed = false;

      for (let i = 0; i < RUNS; i++) {
        try {
          const r = await fetch(url);
          if (r.status !== 200) { failed = true; break; }
          times.push(r.ms);
          bytes = r.bytes;
        } catch { failed = true; break; }
      }

      if (failed || !times.length) {
        if (!JSON_OUT) console.log(`${ep.name.padEnd(28)} FAILED`);
        results.push({ name: ep.name, mode, failed: true });
        continue;
      }

      const s = stats(times);
      const sizeStr = bytes > 1024 ? `${(bytes / 1024).toFixed(1)}KB` : `${bytes}B`;

      results.push({ name: ep.name, mode, ...s, bytes });

      if (!JSON_OUT) {
        console.log(
          `${ep.name.padEnd(28)} ${(s.avg + 'ms').padStart(8)} ${(s.p50 + 'ms').padStart(8)} ${(s.p95 + 'ms').padStart(8)} ${(s.max + 'ms').padStart(8)} ${sizeStr.padStart(9)}`
        );
      }
    }
  }

  if (!JSON_OUT) {
    // Summary comparison
    console.log(`\n${'='.repeat(70)}`);
    console.log('  📊 CACHE IMPACT (avg ms: cached → nocache)');
    console.log(`${'='.repeat(70)}`);
    console.log(`${'Endpoint'.padEnd(28)} ${'Cached'.padStart(8)} ${'No-cache'.padStart(8)} ${'Speedup'.padStart(8)}`);
    console.log(`${'-'.repeat(28)} ${'-'.repeat(8)} ${'-'.repeat(8)} ${'-'.repeat(8)}`);

    const cached = results.filter(r => r.mode === 'cached' && !r.failed);
    const nocache = results.filter(r => r.mode === 'nocache' && !r.failed);

    for (const c of cached) {
      const nc = nocache.find(n => n.name === c.name);
      if (!nc) continue;
      const speedup = nc.avg > 0 ? (nc.avg / c.avg).toFixed(1) + '×' : '—';
      console.log(
        `${c.name.padEnd(28)} ${(c.avg + 'ms').padStart(8)} ${(nc.avg + 'ms').padStart(8)} ${speedup.padStart(8)}`
      );
    }
  }

  if (JSON_OUT) {
    console.log(JSON.stringify(results, null, 2));
  }
}

run().catch(e => { console.error(e); process.exit(1); });
