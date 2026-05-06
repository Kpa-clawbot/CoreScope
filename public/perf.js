/* === CoreScope — perf.js === */
'use strict';

(function () {
  let interval = null;

  async function render(app) {
    app.innerHTML = '<div id="perfWrapper" style="padding:16px 24px;"><h2>⚡ Performance Dashboard</h2><div id="perfContent">Loading...</div></div>';
    await refresh();
  }

  async function refresh() {
    const el = document.getElementById('perfContent');
    if (!el) return;
    try {
      const [server, client, ioStats, sqliteStats, writeSources] = await Promise.all([
        fetch('/api/perf').then(r => r.json()),
        Promise.resolve(window.apiPerf ? window.apiPerf() : null),
        fetch('/api/perf/io').then(r => r.json()).catch(() => null),
        fetch('/api/perf/sqlite').then(r => r.json()).catch(() => null),
        fetch('/api/perf/write-sources').then(r => r.json()).catch(() => null)
      ]);

      // Also fetch health telemetry
      const health = await fetch('/api/health').then(r => r.json()).catch(() => null);

      let html = '';

      // Server overview
      html += `<div style="display:flex;gap:16px;flex-wrap:wrap;margin:16px 0;">
        <div class="perf-card"><div class="perf-num">${server.totalRequests}</div><div class="perf-label">Total Requests</div></div>
        <div class="perf-card"><div class="perf-num">${server.avgMs}ms</div><div class="perf-label">Avg Response</div></div>
        <div class="perf-card"><div class="perf-num">${health ? health.uptimeHuman : Math.round(server.uptime / 60) + 'm'}</div><div class="perf-label">Uptime</div></div>
        <div class="perf-card"><div class="perf-num">${server.slowQueries.length}</div><div class="perf-label">Slow (&gt;100ms)</div></div>
      </div>`;

      // System health (memory, event loop / go runtime, WS)
      if (health) {
        const isGo = health.engine === 'go';
        if (isGo && server.goRuntime) {
          const gr = server.goRuntime;
          const gcColor = gr.lastPauseMs > 5 ? 'var(--status-red)' : gr.lastPauseMs > 1 ? 'var(--status-yellow)' : 'var(--status-green)';
          html += `<h3>🔧 Go Runtime</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
            <div class="perf-card"><div class="perf-num">${gr.goroutines}</div><div class="perf-label">Goroutines</div></div>
            <div class="perf-card"><div class="perf-num">${gr.numGC}</div><div class="perf-label">GC Collections</div></div>
            <div class="perf-card"><div class="perf-num" style="color:${gcColor}">${(+gr.pauseTotalMs).toFixed(1)}ms</div><div class="perf-label">GC Pause Total</div></div>
            <div class="perf-card"><div class="perf-num">${(+gr.lastPauseMs).toFixed(1)}ms</div><div class="perf-label">Last GC Pause</div></div>
            <div class="perf-card"><div class="perf-num">${(+gr.heapAllocMB).toFixed(1)}MB</div><div class="perf-label">Heap Alloc</div></div>
            <div class="perf-card"><div class="perf-num">${(+gr.heapSysMB).toFixed(1)}MB</div><div class="perf-label">Heap Sys</div></div>
            <div class="perf-card"><div class="perf-num">${(+gr.heapInuseMB).toFixed(1)}MB</div><div class="perf-label">Heap Inuse</div></div>
            <div class="perf-card"><div class="perf-num">${(+gr.heapIdleMB).toFixed(1)}MB</div><div class="perf-label">Heap Idle</div></div>
            <div class="perf-card"><div class="perf-num">${gr.numCPU}</div><div class="perf-label">CPUs</div></div>
            <div class="perf-card"><div class="perf-num">${health.websocket.clients}</div><div class="perf-label">WS Clients</div></div>
          </div>`;
        } else {
          const m = health.memory, el = health.eventLoop;
          const elColor = el.p95Ms > 500 ? 'var(--status-red)' : el.p95Ms > 100 ? 'var(--status-yellow)' : 'var(--status-green)';
          const memColor = m.heapUsed > m.heapTotal * 0.85 ? 'var(--status-red)' : m.heapUsed > m.heapTotal * 0.7 ? 'var(--status-yellow)' : 'var(--status-green)';
          html += `<h3>System Health</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
            <div class="perf-card"><div class="perf-num" style="color:${memColor}">${m.heapUsed}MB</div><div class="perf-label">Heap Used / ${m.heapTotal}MB</div></div>
            <div class="perf-card"><div class="perf-num">${m.rss}MB</div><div class="perf-label">RSS</div></div>
            <div class="perf-card"><div class="perf-num" style="color:${elColor}">${el.p95Ms}ms</div><div class="perf-label">Event Loop p95</div></div>
            <div class="perf-card"><div class="perf-num">${el.maxLagMs}ms</div><div class="perf-label">EL Max Lag</div></div>
            <div class="perf-card"><div class="perf-num">${el.currentLagMs}ms</div><div class="perf-label">EL Current</div></div>
            <div class="perf-card"><div class="perf-num">${health.websocket.clients}</div><div class="perf-label">WS Clients</div></div>
          </div>`;
        }
      }

      // Disk I/O (#1120)
      if (ioStats) {
        const fmtRate = (bps) => {
          if (bps >= 1048576) return (bps / 1048576).toFixed(1) + ' MB/s';
          if (bps >= 1024) return (bps / 1024).toFixed(1) + ' KB/s';
          return Math.round(bps) + ' B/s';
        };
        const writeWarn = ioStats.writeBytesPerSec > 10 * 1048576 ? ' ⚠️' : '';
        html += `<h3>Disk I/O (server process)</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num">${fmtRate(ioStats.readBytesPerSec || 0)}</div><div class="perf-label">Read</div></div>
          <div class="perf-card"><div class="perf-num">${fmtRate(ioStats.writeBytesPerSec || 0)}${writeWarn}</div><div class="perf-label">Write</div></div>
          <div class="perf-card"><div class="perf-num">${Math.round(ioStats.syscallsRead || 0)}/s</div><div class="perf-label">Syscalls Read</div></div>
          <div class="perf-card"><div class="perf-num">${Math.round(ioStats.syscallsWrite || 0)}/s</div><div class="perf-label">Syscalls Write</div></div>
        </div>`;
      }

      // Write Sources (#1120) — per-component counters from ingestor
      if (writeSources && writeSources.sources) {
        const src = writeSources.sources;
        const keys = Object.keys(src).sort((a, b) => (src[b] || 0) - (src[a] || 0));
        html += '<h3>Write Sources</h3>';
        if (keys.length === 0) {
          html += '<p style="color:var(--text-muted)">No ingestor stats yet (waiting for /tmp/corescope-ingestor-stats.json)</p>';
        } else {
          // Anomaly detection (#1123 polish):
          //   Compare PER-SECOND DELTA RATES, not cumulative counts.
          //   Cumulative-vs-cumulative was a tautology that fired ⚠️ at startup
          //   (any backfill_* > 10 when tx_inserted=0 → baseline collapses to 1)
          //   and false-cleared once tx grew past a one-shot backfill burst.
          //   Now we cache the previous snapshot + sampleAt and only fire when:
          //     1) we have a real interval (≥ 0.5s) to compute deltas against
          //     2) tx_inserted has crossed MIN_SAMPLE so the baseline is meaningful
          //     3) the per-second backfill rate exceeds 10× the per-second tx rate
          const MIN_SAMPLE = 100;
          const prev = window._perfWriteSourcesPrev;
          let prevSrc = null, dtSec = 0;
          if (prev && prev.sampleAt && writeSources.sampleAt) {
            dtSec = (Date.parse(writeSources.sampleAt) - Date.parse(prev.sampleAt)) / 1000;
            if (dtSec >= 0.5) prevSrc = prev.sources;
          }
          const txTotal = src.tx_inserted || 0;
          const txDelta = prevSrc ? (txTotal - (prevSrc.tx_inserted || 0)) : 0;
          const txRate = (prevSrc && dtSec > 0) ? (txDelta / dtSec) : 0;
          html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th scope="col">Source</th><th scope="col">Total</th><th scope="col">Rate/s</th><th scope="col">Anomaly</th></tr></thead><tbody>';
          for (const k of keys) {
            const v = src[k] || 0;
            const isBackfill = k.startsWith('backfill_');
            let rate = 0;
            let flag = '';
            if (prevSrc && dtSec > 0) {
              const delta = v - (prevSrc[k] || 0);
              rate = delta / dtSec;
              // Only flag when tx baseline is statistically meaningful AND
              // backfill is actively running faster than 10× the live tx rate.
              if (isBackfill && txTotal >= MIN_SAMPLE && rate > 10 * Math.max(txRate, 1)) {
                flag = ' ⚠️';
              }
            }
            const rateStr = (prevSrc && dtSec > 0) ? rate.toFixed(1) : '—';
            html += `<tr><td><code>${k}</code></td><td>${v.toLocaleString()}</td><td>${rateStr}</td><td>${flag}</td></tr>`;
          }
          html += '</tbody></table></div>';
          // Stash for next tick's delta computation.
          window._perfWriteSourcesPrev = { sources: { ...src }, sampleAt: writeSources.sampleAt };
          if (writeSources.sampleAt) {
            html += `<div style="font-size:11px;color:var(--text-muted);margin-top:4px">Sampled: ${writeSources.sampleAt}</div>`;
          }
        }
      }

      // SQLite perf (separate from existing SQLite block — focused on WAL + cache hit) (#1120)
      if (sqliteStats) {
        const walMB = sqliteStats.walSizeMB || 0;
        const walFlag = walMB > 100 ? ' ⚠️' : '';
        const hitRate = (sqliteStats.cacheHitRate || 0) * 100;
        const hitFlag = hitRate > 0 && hitRate < 90 ? ' ⚠️' : '';
        html += `<h3>SQLite (WAL + Cache Hit)</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num">${walMB.toFixed(1)}MB${walFlag}</div><div class="perf-label">WAL Size</div></div>
          <div class="perf-card"><div class="perf-num">${(sqliteStats.pageCount || 0).toLocaleString()}</div><div class="perf-label">Page Count</div></div>
          <div class="perf-card"><div class="perf-num">${sqliteStats.pageSize || 0}</div><div class="perf-label">Page Size</div></div>
          <div class="perf-card"><div class="perf-num">${hitRate.toFixed(1)}%${hitFlag}</div><div class="perf-label">Cache Hit Rate</div></div>
        </div>`;
      }

      // Cache stats
      if (server.cache) {
        const c = server.cache;
        const clientCache = _apiCache ? _apiCache.size : 0;
        html += `<h3>Cache</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num">${c.size}</div><div class="perf-label">Server Entries</div></div>
          <div class="perf-card"><div class="perf-num">${c.hits}</div><div class="perf-label">Server Hits</div></div>
          <div class="perf-card"><div class="perf-num">${c.misses}</div><div class="perf-label">Server Misses</div></div>
          <div class="perf-card"><div class="perf-num" style="color:${c.hitRate > 50 ? 'var(--status-green)' : c.hitRate > 20 ? 'var(--status-yellow)' : 'var(--status-red)'}">${c.hitRate}%</div><div class="perf-label">Server Hit Rate</div></div>
          <div class="perf-card"><div class="perf-num">${c.staleHits || 0}</div><div class="perf-label">Stale Hits (SWR)</div></div>
          <div class="perf-card"><div class="perf-num">${c.recomputes || 0}</div><div class="perf-label">Recomputes</div></div>
          <div class="perf-card"><div class="perf-num">${clientCache}</div><div class="perf-label">Client Entries</div></div>
        </div>`;
        if (client) {
          html += `<div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
            <div class="perf-card"><div class="perf-num">${client.cacheHits || 0}</div><div class="perf-label">Client Hits</div></div>
            <div class="perf-card"><div class="perf-num">${client.cacheMisses || 0}</div><div class="perf-label">Client Misses</div></div>
            <div class="perf-card"><div class="perf-num" style="color:${(client.cacheHitRate||0) > 50 ? 'var(--status-green)' : 'var(--status-yellow)'}">${client.cacheHitRate || 0}%</div><div class="perf-label">Client Hit Rate</div></div>
          </div>`;
        }
      }

      // Packet Store stats
      if (server.packetStore) {
        const ps = server.packetStore;
        html += `<h3>In-Memory Packet Store</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num">${ps.inMemory.toLocaleString()}</div><div class="perf-label">Packets in RAM</div></div>
          <div class="perf-card"><div class="perf-num">${ps.trackedMB}MB</div><div class="perf-label">Tracked Memory</div></div>
          <div class="perf-card"><div class="perf-num">${ps.maxMB}MB</div><div class="perf-label">Memory Limit</div></div>
          <div class="perf-card"><div class="perf-num">${ps.estimatedMB}MB</div><div class="perf-label">Heap (debug)</div></div>
          <div class="perf-card"><div class="perf-num">${ps.queries.toLocaleString()}</div><div class="perf-label">Queries Served</div></div>
          <div class="perf-card"><div class="perf-num">${ps.inserts.toLocaleString()}</div><div class="perf-label">Live Inserts</div></div>
          <div class="perf-card"><div class="perf-num">${ps.evicted.toLocaleString()}</div><div class="perf-label">Evicted</div></div>
          <div class="perf-card"><div class="perf-num">${ps.indexes.byHash.toLocaleString()}</div><div class="perf-label">Unique Hashes</div></div>
          <div class="perf-card"><div class="perf-num">${ps.indexes.byObserver}</div><div class="perf-label">Observers</div></div>
          <div class="perf-card"><div class="perf-num">${ps.indexes.byNode.toLocaleString()}</div><div class="perf-label">Indexed Nodes</div></div>
        </div>`;
      }

      // SQLite stats
      if (server.sqlite && !server.sqlite.error) {
        const sq = server.sqlite;
        const walColor = sq.walSizeMB > 50 ? 'var(--status-red)' : sq.walSizeMB > 10 ? 'var(--status-yellow)' : 'var(--status-green)';
        const freelistColor = sq.freelistMB > 10 ? 'var(--status-yellow)' : 'var(--status-green)';
        html += `<h3>SQLite</h3><div style="display:flex;gap:16px;flex-wrap:wrap;margin:8px 0;">
          <div class="perf-card"><div class="perf-num">${sq.dbSizeMB}MB</div><div class="perf-label">DB Size</div></div>
          <div class="perf-card"><div class="perf-num" style="color:${walColor}">${sq.walSizeMB}MB</div><div class="perf-label">WAL Size</div></div>
          <div class="perf-card"><div class="perf-num" style="color:${freelistColor}">${sq.freelistMB}MB</div><div class="perf-label">Freelist</div></div>
          <div class="perf-card"><div class="perf-num">${(sq.rows.transmissions || 0).toLocaleString()}</div><div class="perf-label">Transmissions</div></div>
          <div class="perf-card"><div class="perf-num">${(sq.rows.observations || 0).toLocaleString()}</div><div class="perf-label">Observations</div></div>
          <div class="perf-card"><div class="perf-num">${sq.rows.nodes || 0}</div><div class="perf-label">Nodes</div></div>
          <div class="perf-card"><div class="perf-num">${sq.rows.observers || 0}</div><div class="perf-label">Observers</div></div>`;
        if (sq.walPages) {
          html += `<div class="perf-card"><div class="perf-num">${sq.walPages.busy}</div><div class="perf-label">WAL Busy Pages</div></div>`;
        }
        html += `</div>`;
      }

      // Server endpoints table
      const eps = Object.entries(server.endpoints);
      if (eps.length) {
        html += '<h3>Server Endpoints (sorted by total time)</h3>';
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th scope="col">Endpoint</th><th scope="col">Count</th><th scope="col">Avg</th><th scope="col">P50</th><th scope="col">P95</th><th scope="col">Max</th><th scope="col">Total</th></tr></thead><tbody>';
        for (const [path, s] of eps) {
          const total = Math.round(s.count * s.avgMs);
          const cls = s.p95Ms > 200 ? ' class="perf-slow"' : s.p95Ms > 50 ? ' class="perf-warn"' : '';
          html += `<tr${cls}><td><code>${path}</code></td><td>${s.count}</td><td>${s.avgMs}ms</td><td>${s.p50Ms}ms</td><td>${s.p95Ms}ms</td><td>${s.maxMs}ms</td><td>${total}ms</td></tr>`;
        }
        html += '</tbody></table></div>';
      }

      // Client API calls
      if (client && client.endpoints.length) {
        html += '<h3>Client API Calls (this session)</h3>';
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th scope="col">Endpoint</th><th scope="col">Count</th><th scope="col">Avg</th><th scope="col">Max</th><th scope="col">Total</th></tr></thead><tbody>';
        for (const s of client.endpoints) {
          const cls = s.maxMs > 500 ? ' class="perf-slow"' : s.avgMs > 200 ? ' class="perf-warn"' : '';
          html += `<tr${cls}><td><code>${s.path}</code></td><td>${s.count}</td><td>${s.avgMs}ms</td><td>${s.maxMs}ms</td><td>${s.totalMs}ms</td></tr>`;
        }
        html += '</tbody></table></div>';
      }

      // Slow queries
      if (server.slowQueries.length) {
        html += '<h3>Recent Slow Queries (&gt;100ms)</h3>';
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th scope="col">Time</th><th scope="col">Path</th><th scope="col">Duration</th><th scope="col">Status</th></tr></thead><tbody>';
        for (const q of server.slowQueries.slice().reverse()) {
          html += `<tr class="perf-slow"><td>${new Date(q.time).toLocaleTimeString()}</td><td><code>${q.path}</code></td><td>${q.ms}ms</td><td>${q.status}</td></tr>`;
        }
        html += '</tbody></table></div>';
      }

      html += `<div style="margin-top:16px"><button id="perfReset" style="padding:8px 16px;cursor:pointer">Reset Stats</button> <button id="perfRefresh" style="padding:8px 16px;cursor:pointer">Refresh</button></div>`;
      el.innerHTML = html;

      document.getElementById('perfReset')?.addEventListener('click', async () => {
        await fetch('/api/perf/reset', { method: 'POST' });
        if (window._apiPerf) { window._apiPerf = { calls: 0, totalMs: 0, log: [] }; }
        refresh();
      });
      document.getElementById('perfRefresh')?.addEventListener('click', refresh);
    } catch (err) {
      el.innerHTML = `<p style="color:red">Error: ${err.message}</p>`;
    }
  }

  registerPage('perf', {
    init(app) {
      render(app);
      interval = setInterval(refresh, 5000);
    },
    destroy() {
      if (interval) { clearInterval(interval); interval = null; }
    }
  });
})();
