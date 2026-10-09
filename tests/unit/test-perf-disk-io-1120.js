/* Tests for perf.js Disk I/O + Write Sources + PostgreSQL sections (#1120) */
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

function stubFetch(sb, perfData, healthData, ioData, postgresData, sourcesData) {
  sb.ctx.fetch = (url) => {
    if (url === '/api/perf') return Promise.resolve({ json: () => Promise.resolve(perfData) });
    if (url === '/api/health') return Promise.resolve({ json: () => Promise.resolve(healthData) });
    if (url === '/api/perf/io') return Promise.resolve({ json: () => Promise.resolve(ioData) });
    if (url === '/api/perf/postgres') return Promise.resolve({ json: () => Promise.resolve(postgresData) });
    if (url === '/api/perf/write-sources') return Promise.resolve({ json: () => Promise.resolve(sourcesData) });
    return Promise.resolve({ json: () => Promise.resolve({}) });
  };
}

const basePerf = {
  totalRequests: 100, avgMs: 5, uptime: 3600,
  slowQueries: [], endpoints: {}, cache: null, packetStore: null, postgres: null
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
const postgresData = {
  engine: 'postgresql', databaseBytes: 12900000, openConnections: 4, inUseConnections: 2,
  idleConnections: 2, connectionWaitCount: 3, connectionWaitMs: 1.25, cacheHitRate: 0.987
};
const sourcesData = {
  sources: { tx_inserted: 25, obs_inserted: 1787, backfill_path_json: 0, node_upserts: 329, observer_upserts: 1823, walCommits: 100 },
  sampleAt: '2026-01-01T00:00:00Z'
};

console.log('\n🧪 perf.js — Disk I/O + Write Sources (#1120)\n');

(async () => {
await test('Renders Disk I/O section', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, postgresData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(html.includes('Disk I/O'), 'should show Disk I/O heading');
  assert.ok(/2\.0\s*KB/.test(html), 'should render write rate value (2048 B/s formatted as 2.0 KB/s)');
});

await test('Renders Write Sources section with non-zero rates', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, postgresData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(html.includes('Write Sources'), 'should show Write Sources heading');
  assert.ok(html.includes('tx_inserted'), 'should list tx_inserted source');
  assert.ok(html.includes('obs_inserted'), 'should list obs_inserted source');
});

await test('Renders PostgreSQL connection and cache statistics', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, postgresData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(html.includes('PostgreSQL'), 'should name the active database engine');
  assert.ok(html.includes('Connections in use'), 'should show PostgreSQL pool utilization');
  assert.ok(!html.includes('WAL Size') && !html.includes('Page Count'), 'must not invent SQLite metrics');
  assert.ok(/Cache Hit/i.test(html) || /cacheHitRate/i.test(html), 'should show cache hit rate');
});

// === #1120 follow-up: cancelled writes + ingestor row + threshold UX ===

await test('Renders cancelledWriteBytesPerSec for server process', async () => {
  const sb = loadPerf();
  const io = { ...ioData, cancelledWriteBytesPerSec: 4096 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, io, postgresData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/Cancel(led)?/i.test(html), 'should show a Cancelled write label');
  assert.ok(/4\.0\s*KB/.test(html), 'should render cancelled write rate (4096 B/s → 4.0 KB/s)');
});

await test('Renders ingestor row alongside server row in Disk I/O', async () => {
  const sb = loadPerf();
  const io = {
    ...ioData,
    cancelledWriteBytesPerSec: 0,
    ingestor: {
      readBytesPerSec: 0,
      writeBytesPerSec: 1048576,
      cancelledWriteBytesPerSec: 0,
      syscallsRead: 0,
      syscallsWrite: 0,
    },
  };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, io, postgresData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/Ingestor/i.test(html), 'should label ingestor row');
  assert.ok(/1\.0\s*MB/.test(html), 'should render ingestor write 1 MB/s');
});

await test('Unavailable PostgreSQL cache statistics render as unknown', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, { ...postgresData, cacheHitRate: null }, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  assert.ok(sb.getHtml().includes('—</div><div class="perf-label">Database Block Cache Hit Rate'), 'missing metrics must not become zero');
});

await test('PostgreSQL numeric string metrics render without an exception', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, { ...postgresData, cacheHitRate: '0.987', connectionWaitMs: '1.25' }, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  assert.ok(sb.getHtml().includes('98.7%') && sb.getHtml().includes('1.3ms'), 'string metrics must format numerically');
});

await test('Cache hit <90% fires ⚠️ flag', async () => {
  const sb = loadPerf();
  const sql = { ...postgresData, cacheHitRate: 0.85 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/85\.0%\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected warning icon next to 85.0% cache hit value');
});

await test('Cache hit ≥90% does NOT fire ⚠️ flag', async () => {
  const sb = loadPerf();
  const sql = { ...postgresData, cacheHitRate: 0.987 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(!/98\.7%\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected NO warning icon next to 98.7% cache hit value');
});

// === #1167 must-fix #7: threshold boundary cases ===

await test('Unavailable database diagnostics do not invent a PostgreSQL panel', async () => {
  const sb = loadPerf();
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, null, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  assert.ok(!sb.getHtml().includes('PostgreSQL connections'), 'unavailable statistics should stay unavailable');
});

await test('PostgreSQL data panel reports counts without SQLite storage fields', async () => {
  const sb = loadPerf();
  const postgres = { engine: 'postgresql', dbSizeMB: 42, rows: { transmissions: 30000, observations: 90000, nodes: 2000, observers: 32 } };
  stubFetch(sb, { ...basePerf, goRuntime, postgres }, goHealth, ioData, postgresData, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(html.includes('PostgreSQL data') && html.includes('42MB') && html.includes((30000).toLocaleString()), 'native storage figures missing');
  assert.ok(!html.includes('Freelist') && !html.includes('WAL Size'), 'SQLite-only metrics must not survive the conversion');
});

await test('Cache hit exactly 90% does NOT fire ⚠️ (boundary, strict <)', async () => {
  const sb = loadPerf();
  const sql = { ...postgresData, cacheHitRate: 0.90 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(!/90\.0%\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected NO warning icon at exactly 90.0% cache hit (boundary)');
});

await test('Cache hit infinitesimally below 90% DOES fire ⚠️', async () => {
  const sb = loadPerf();
  const sql = { ...postgresData, cacheHitRate: 0.8999 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, sql, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  const html = sb.getHtml();
  assert.ok(/90\.0%\s*<svg\b[^>]*><use\b[^>]*#ph-warning"/.test(html), 'expected warning icon next to 90.0% cache hit value (just under threshold)');
});

await test('Stale PostgreSQL sample is labelled and retains verified counts', async () => {
  const sb = loadPerf();
  const postgres = { engine: 'postgresql', dbSizeMB: 42, rows: { transmissions: 30000, observations: 90000, nodes: 2000, observers: 32 }, sampledAt: '2026-01-01T12:00:00Z', sampleIntervalSeconds: 30, stale: true, error: 'unavailable' };
  stubFetch(sb, { ...basePerf, goRuntime, postgres }, goHealth, ioData, { ...postgresData, sampledAt: postgres.sampledAt, sampleIntervalSeconds: 30, stale: true, cacheHitRate: null }, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  assert.ok(sb.getHtml().includes('42MB') && sb.getHtml().includes('Last verified PostgreSQL sample'), 'stale data must remain labelled as a prior verified sample');
});

await test('Initial PostgreSQL sampling failure does not invent zero storage', async () => {
  const sb = loadPerf();
  const postgres = { engine: 'postgresql', dbSizeMB: null, rows: null, stale: true, error: 'unavailable' };
  stubFetch(sb, { ...basePerf, goRuntime, postgres }, goHealth, ioData, { ...postgresData, databaseBytes: null, cacheHitRate: null, stale: true }, sourcesData);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 100));
  assert.ok(sb.getHtml().includes('PostgreSQL storage unavailable'), 'failed first sample must be explicit');
  assert.ok(!sb.getHtml().includes('0MB</div><div class="perf-label">DB Size'), 'unknown storage must not become zero');
});
await test('Backfill anomaly: rate >10× its stable rolling baseline shows a warning', async () => {
  // The current detector compares a source with its own rolling baseline.
  // Prime a 60-second baseline at 1/s, then jump to 1000/s for the next tick.
  const sb = loadPerf();
  const samples = [
    { sources: { tx_inserted: 100, backfill_path_json: 0 }, sampleAt: '2026-01-01T00:00:00Z' },
    { sources: { tx_inserted: 400, backfill_path_json: 60 }, sampleAt: '2026-01-01T00:01:00Z' },
    { sources: { tx_inserted: 405, backfill_path_json: 1060 }, sampleAt: '2026-01-01T00:01:01Z' },
  ];
  for (const sample of samples) {
    stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, postgresData, sample);
    await sb.pages.perf.init({ set innerHTML(v) {} });
    await new Promise(r => setTimeout(r, 50));
  }
  const html = sb.getHtml();
  const idx = html.indexOf('backfill_path_json');
  assert.ok(idx >= 0, 'backfill_path_json row missing');
  const row = html.slice(idx, html.indexOf('</tr>', idx));
  assert.ok(row.includes('1000.00') && row.includes('1.00'), 'renders current and baseline rates');
  assert.ok(row.includes('#ph-warning"'), 'expected warning on backfill row when rate ratio >10×, row=' + row);
});

await test('Backfill anomaly: insufficient history suppresses the warning', async () => {
  // Two snapshots cannot provide the required stable historical baseline,
  // even if the current backfill rate is much larger than the tx rate.
  const sb = loadPerf();
  const t0 = '2026-01-01T00:00:00Z';
  const t1 = '2026-01-01T00:00:01Z';
  const phase1 = { sources: { tx_inserted: 5, backfill_path_json: 0 }, sampleAt: t0 };
  const phase2 = { sources: { tx_inserted: 6, backfill_path_json: 1000 }, sampleAt: t1 };
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, postgresData, phase1);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  stubFetch(sb, { ...basePerf, goRuntime }, goHealth, ioData, postgresData, phase2);
  await sb.pages.perf.init({ set innerHTML(v) {} });
  await new Promise(r => setTimeout(r, 50));
  const html = sb.getHtml();
  const idx = html.indexOf('backfill_path_json');
  assert.ok(idx >= 0, 'backfill_path_json row missing');
  const row = html.slice(idx, html.indexOf('</tr>', idx));
  assert.ok(!row.includes('#ph-warning"'), 'expected NO warning without a stable baseline, row=' + row);
});

console.log(`\n${passed} passed, ${failed} failed\n`);
process.exit(failed ? 1 : 0);
})();
