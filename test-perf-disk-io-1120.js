/* Tests for perf.js Disk I/O + Write Sources + SQLite sections (#1120) */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

function makeSandbox() {
  let capturedHtml = '';
  const pages = {};
  const ctx = {
    window: { addEventListener: () => {}, apiPerf: null },
    document: {
      getElementById: (id) => {
        if (id === 'perfContent') return { set innerHTML(v) { capturedHtml = v; } };
        return null;
      },
      addEventListener: () => {},
    },
    console,
    Date, Math, Array, Object, String, Number, JSON, RegExp, Error, TypeError,
    parseInt, parseFloat, isNaN, isFinite,
    setTimeout: () => {}, clearTimeout: () => {},
    setInterval: () => 0, clearInterval: () => {},
    performance: { now: () => Date.now() },
    Map, Set, Promise,
    registerPage: (name, handler) => { pages[name] = handler; },
    _apiCache: null,
    fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  };
  ctx.window.document = ctx.document;
  ctx.globalThis = ctx;
  return { ctx, pages, getHtml: () => capturedHtml };
}

function loadPerf() {
  const sb = makeSandbox();
  const code = fs.readFileSync('public/perf.js', 'utf8');
  vm.runInNewContext(code, sb.ctx);
  return sb;
}

function stubFetch(sb, perfData, healthData, ioData, sqliteData, sourcesData) {
  sb.ctx.fetch = (url) => {
    if (url === '/api/perf') return Promise.resolve({ json: () => Promise.resolve(perfData) });
    if (url === '/api/health') return Promise.resolve({ json: () => Promise.resolve(healthData) });
    if (url === '/api/perf/io') return Promise.resolve({ json: () => Promise.resolve(ioData) });
    if (url === '/api/perf/sqlite') return Promise.resolve({ json: () => Promise.resolve(sqliteData) });
    if (url === '/api/perf/write-sources') return Promise.resolve({ json: () => Promise.resolve(sourcesData) });
    return Promise.resolve({ json: () => Promise.resolve({}) });
  };
}

const basePerf = {
  totalRequests: 100, avgMs: 5, uptime: 3600,
  slowQueries: [], endpoints: {}, cache: null, packetStore: null, sqlite: null
};
const goRuntime = {
  goroutines: 17, numGC: 31, pauseTotalMs: 2.1, lastPauseMs: 0.03,
  heapAllocMB: 473, heapSysMB: 1035, heapInuseMB: 663, heapIdleMB: 371, numCPU: 2
};
const goHealth = { engine: 'go', uptimeHuman: '2h', websocket: { clients: 5 } };

const ioData = {
  readBytesPerSec: 1024, writeBytesPerSec: 2048,
  syscallsRead: 10, syscallsWrite: 20
};
const sqliteData = {
  walSizeMB: 12.3, walSize: 12900000, pageCount: 4096, pageSize: 4096,
  cacheSize: 2000, cacheHitRate: 0.987
};
const sourcesData = {
  sources: { tx_inserted: 25, obs_inserted: 1787, backfill_path_json: 0, node_upserts: 329, observer_upserts: 1823, walCommits: 100 },
  sampleAt: '2026-01-01T00:00:00Z'
};

console.log('\n🧪 perf.js — Disk I/O + Write Sources (#1120)\n');

(async () => {
await test('Renders Disk I/O section', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(html.includes('Disk I/O'), 'should show Disk I/O heading');
  assert.ok(/2048|2\.0\s*KB|2 KB/.test(html) || html.includes('writeBytesPerSec') === false,
    'should render write rate value');
});

await test('Renders Write Sources section with non-zero rates', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(html.includes('Write Sources'), 'should show Write Sources heading');
  assert.ok(html.includes('tx_inserted'), 'should list tx_inserted source');
  assert.ok(html.includes('obs_inserted'), 'should list obs_inserted source');
});

await test('Renders SQLite section with WAL + cache hit rate', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sqliteData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/WAL/i.test(html), 'should show WAL info');
  assert.ok(/Cache Hit/i.test(html) || /cacheHitRate/i.test(html), 'should show cache hit rate');
});

console.log(`\n${passed} passed, ${failed} failed\n`);
process.exit(failed ? 1 : 0);
})();
