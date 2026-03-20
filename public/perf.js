/* === MeshCore Analyzer — perf.js === */
'use strict';

(function () {
  let interval = null;

  async function render(app) {
    app.innerHTML = '<div style="height:100%;overflow-y:auto;padding:16px 24px;"><h2>⚡ Performance Dashboard</h2><div id="perfContent">Loading...</div></div>';
    await refresh();
  }

  async function refresh() {
    const el = document.getElementById('perfContent');
    if (!el) return;
    try {
      const [server, client] = await Promise.all([
        fetch('/api/perf').then(r => r.json()),
        Promise.resolve(window.apiPerf ? window.apiPerf() : null)
      ]);

      let html = '';

      // Server overview
      html += `<div style="display:flex;gap:16px;flex-wrap:wrap;margin:16px 0;">
        <div class="perf-card"><div class="perf-num">${server.totalRequests}</div><div class="perf-label">Total Requests</div></div>
        <div class="perf-card"><div class="perf-num">${server.avgMs}ms</div><div class="perf-label">Avg Response</div></div>
        <div class="perf-card"><div class="perf-num">${Math.round(server.uptime / 60)}m</div><div class="perf-label">Uptime</div></div>
        <div class="perf-card"><div class="perf-num">${server.slowQueries.length}</div><div class="perf-label">Slow (&gt;100ms)</div></div>
      </div>`;

      // Server endpoints table
      const eps = Object.entries(server.endpoints);
      if (eps.length) {
        html += '<h3>Server Endpoints (sorted by total time)</h3>';
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th>Endpoint</th><th>Count</th><th>Avg</th><th>P50</th><th>P95</th><th>Max</th><th>Total</th></tr></thead><tbody>';
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
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th>Endpoint</th><th>Count</th><th>Avg</th><th>Max</th><th>Total</th></tr></thead><tbody>';
        for (const s of client.endpoints) {
          const cls = s.maxMs > 500 ? ' class="perf-slow"' : s.avgMs > 200 ? ' class="perf-warn"' : '';
          html += `<tr${cls}><td><code>${s.path}</code></td><td>${s.count}</td><td>${s.avgMs}ms</td><td>${s.maxMs}ms</td><td>${s.totalMs}ms</td></tr>`;
        }
        html += '</tbody></table></div>';
      }

      // Slow queries
      if (server.slowQueries.length) {
        html += '<h3>Recent Slow Queries (&gt;100ms)</h3>';
        html += '<div style="overflow-x:auto"><table class="perf-table"><thead><tr><th>Time</th><th>Path</th><th>Duration</th><th>Status</th></tr></thead><tbody>';
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
