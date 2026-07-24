/* === CoreScope — node-analytics.js === */
'use strict';
(function () {
  const PAYLOAD_LABELS = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
  const CHART_COLORS = ['#4a9eff', '#ff6b6b', '#51cf66', '#fcc419', '#cc5de8', '#20c997', '#ff922b', '#845ef7', '#f06595', '#339af0'];
  const GRADE_COLORS = { A: '#51cf66', 'A-': '#51cf66', 'B+': '#339af0', B: '#339af0', C: '#fcc419', D: '#ff6b6b' };
  const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

  let charts = [];
  let currentDays = 7;
  let currentPubkey = null;

  function destroyCharts() {
    charts.forEach(c => { try { c.destroy(); } catch {} });
    charts = [];
  }

  function chartDefaults() {
    const style = getComputedStyle(document.documentElement);
    Chart.defaults.color = style.getPropertyValue('--text-muted').trim() || '#6b7280';
    Chart.defaults.borderColor = style.getPropertyValue('--border').trim() || '#e2e5ea';
  }

  function formatSilence(ms) {
    if (!ms) return '—';
    const h = Math.floor(ms / 3600000);
    const m = Math.floor((ms % 3600000) / 60000);
    if (h > 24) return Math.floor(h / 24) + 'd ' + (h % 24) + 'h';
    if (h > 0) return h + 'h ' + m + 'm';
    return m + 'm';
  }

  async function loadAnalytics(container, pubkey, days) {
    currentPubkey = pubkey;
    currentDays = days;
    destroyCharts();
    chartDefaults();

    container.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading analytics…</div>';

    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/analytics?days=' + days, { ttl: CLIENT_TTL.nodeAnalytics });
    } catch (e) {
      container.innerHTML = '<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load analytics: ' + escapeHtml(e.message) + '</div>';
      return;
    }

    const n = data.node;
    const s = data.computedStats;
    const nodeName = escapeHtml(n.name || n.public_key.slice(0, 12));

    container.innerHTML = `
      <div style="max-width:1000px;margin:0 auto;padding:12px 16px">
        <div style="margin-bottom:12px">
          <a href="#/nodes/${encodeURIComponent(n.public_key)}" style="color:var(--link-color);text-decoration:none;font-size:12px">← Back to ${nodeName}</a>
          <h2 style="margin:4px 0 2px;font-size:18px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> ${nodeName} — Analytics</h2>
          <div style="color:var(--text-muted);font-size:11px">${n.role || 'Unknown role'} · ${s.totalTransmissions || s.totalPackets} packets in ${days}d window</div>
        </div>

        <div class="analytics-time-range" id="timeRangeBtns">
          <button data-days="1" ${days===1?'class="active"':''}>24h</button>
          <button data-days="7" ${days===7?'class="active"':''}>7d</button>
          <button data-days="30" ${days===30?'class="active"':''}>30d</button>
          <button data-days="365" ${days===365?'class="active"':''}>All</button>
        </div>

        <div class="analytics-stats">
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Availability</div>
            <div class="analytics-stat-value">${s.availabilityPct}%</div>
            <div class="analytics-stat-desc">% of time windows with at least one packet</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Signal Grade</div>
            <div class="analytics-stat-value" style="color:${GRADE_COLORS[s.signalGrade]||'var(--text)'}">${s.signalGrade}</div>
            <div class="analytics-stat-desc">A–F based on average SNR across all observers</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Packets / Day</div>
            <div class="analytics-stat-value">${s.avgPacketsPerDay}</div>
            <div class="analytics-stat-desc">Average daily packet volume in this window</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Observers</div>
            <div class="analytics-stat-value">${s.uniqueObservers}</div>
            <div class="analytics-stat-desc">Distinct stations that heard this node</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Relay %</div>
            <div class="analytics-stat-value">${s.relayPct}%</div>
            <div class="analytics-stat-desc">Packets forwarded through repeaters vs direct</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Longest Silence</div>
            <div class="analytics-stat-value" style="font-size:18px">${formatSilence(s.longestSilenceMs)}</div>
            <div class="analytics-stat-desc">Longest gap between consecutive packets</div>
          </div>
        </div>

        <div class="analytics-charts">
          <div class="analytics-chart-card full">
            <h4>Activity Timeline</h4>
            <div class="analytics-chart-desc">Packet count per time bucket — shows when this node is most active</div>
            <canvas id="activityChart" role="img" aria-label="Activity timeline chart"></canvas>
          </div>
          <div class="analytics-chart-card">
            <h4>SNR Trend</h4>
            <div class="analytics-chart-desc">Signal-to-noise ratio over time — higher is better reception</div>
            <canvas id="snrChart" role="img" aria-label="SNR trend chart"></canvas>
          </div>
          <div class="analytics-chart-card">
            <h4>Packet Types</h4>
            <div class="analytics-chart-desc">Breakdown of advert, position, text, and other packet types</div>
            <canvas id="packetTypeChart" role="img" aria-label="Packet types chart"></canvas>
          </div>
          <div class="analytics-chart-card">
            <h4>Observer Coverage</h4>
            <div class="analytics-chart-desc">Which stations hear this node and how often</div>
            <canvas id="observerChart" role="img" aria-label="Observer coverage chart"></canvas>
          </div>
          <div class="analytics-chart-card">
            <h4>Hop Distribution</h4>
            <div class="analytics-chart-desc">How many repeater hops packets take — 0 means direct</div>
            <canvas id="hopChart" role="img" aria-label="Hop distribution chart"></canvas>
          </div>
          <div class="analytics-chart-card full">
            <h4>Relay Hop-Count <span style="font-weight:normal;font-size:11px;color:var(--text-muted)">(tuning flood.max)</span></h4>
            <div class="analytics-chart-desc">Hop count <em>at this node</em> when it relayed each packet — the value MeshCore firmware compares against <code>flood.max</code>/<code>flood.max.advert</code>/<code>flood.max.unscoped</code> before forwarding. Not the same as Hop Distribution above, which measures distance to the observer instead.</div>
            <div id="hopAnalyticsChips" class="analytics-time-range" style="margin-bottom:8px"></div>
            <div id="hopAnalyticsSummary" style="font-size:11px;color:var(--text-muted);margin-bottom:6px"></div>
            <canvas id="hopAnalyticsBoxplot" role="img" aria-label="Hop count boxplot" style="max-height:60px;margin-bottom:4px"></canvas>
            <canvas id="hopAnalyticsHistogram" role="img" aria-label="Hop count histogram"></canvas>
            <div id="hopAnalyticsEmpty" style="display:none;padding:20px;text-align:center;color:var(--text-muted);font-size:12px">No relayed traffic with a resolvable path through this node in this window.</div>
          </div>
          <div class="analytics-chart-card full">
            <h4>Battery Voltage <span id="batteryStatusBadge" style="font-size:11px;font-weight:normal;margin-left:8px"></span></h4>
            <div class="analytics-chart-desc">Battery voltage over time from observer status reports — flat line means full, downward slope means draining</div>
            <canvas id="batteryChart" role="img" aria-label="Battery voltage trend chart"></canvas>
            <div id="batteryEmpty" style="display:none;padding:20px;text-align:center;color:var(--text-muted);font-size:12px">No battery telemetry recorded for this node in this window.</div>
          </div>
          <div class="analytics-chart-card full">
            <h4>Uptime Heatmap</h4>
            <div class="analytics-chart-desc">Hour-by-hour activity grid — darker = more packets in that slot</div>
            <div id="heatmapGrid" class="analytics-heatmap"></div>
          </div>
          ${data.peerInteractions.length ? `<div class="analytics-chart-card full">
            <h4>Peer Interactions</h4>
            <div class="analytics-chart-desc">Nodes this device has exchanged messages with</div>
            <div class="analytics-table-scroll"><table class="analytics-peer-table">
              <thead><tr><th scope="col">Peer</th><th scope="col">Messages</th><th scope="col">Last Contact</th></tr></thead>
              <tbody>${data.peerInteractions.map(p => `<tr>
                <td><a href="#/nodes/${encodeURIComponent(p.peer_key)}" style="color:var(--link-color)">${escapeHtml(p.peer_name)}</a></td>
                <td>${p.messageCount}</td>
                <td>${timeAgo(p.lastContact)}</td>
              </tr>`).join('')}</tbody>
            </table></div>
          </div>` : ''}
        </div>
      </div>`;

    // Time range buttons
    container.querySelectorAll('#timeRangeBtns button').forEach(btn => {
      btn.addEventListener('click', () => {
        const d = Number(btn.dataset.days);
        loadAnalytics(container, pubkey, d);
      });
    });

    // Build charts
    buildActivityChart(data);
    buildSnrChart(data);
    buildPacketTypeChart(data);
    buildObserverChart(data);
    buildHopChart(data);
    buildHeatmap(data);
    loadBatteryChart(pubkey, currentDays);
    loadHopAnalyticsChart(pubkey, currentDays);
  }

  function buildActivityChart(data) {
    const ctx = document.getElementById('activityChart');
    if (!ctx) return;
    const tl = data.activityTimeline;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: tl.map(b => {
          const d = new Date(b.bucket);
          return (typeof formatChartAxisLabel === 'function') ? formatChartAxisLabel(d, currentDays <= 3) : (currentDays <= 3 ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleDateString([], { month: 'short', day: 'numeric' }));
        }),
        datasets: [{ label: 'Packets', data: tl.map(b => b.count), backgroundColor: 'rgba(74,158,255,0.5)', borderColor: '#4a9eff', borderWidth: 1 }]
      },
      options: { responsive: true, plugins: { legend: { display: false } }, scales: { x: { ticks: { maxTicksAutoSkip: true, maxRotation: 45 } }, y: { beginAtZero: true } } }
    });
    charts.push(c);
  }

  function buildSnrChart(data) {
    const ctx = document.getElementById('snrChart');
    if (!ctx) return;
    // Group by observer
    const byObs = {};
    data.snrTrend.forEach(p => {
      const key = p.observer_id || 'unknown';
      if (!byObs[key]) byObs[key] = { name: p.observer_name || key, points: [] };
      byObs[key].points.push({ x: new Date(p.timestamp), y: p.snr });
    });
    const datasets = Object.values(byObs).map((obs, i) => ({
      label: obs.name, data: obs.points.map(p => p.y), borderColor: CHART_COLORS[i % CHART_COLORS.length],
      backgroundColor: 'transparent', pointRadius: 1, borderWidth: 1.5, tension: 0.3
    }));
    // Use labels from the observer with most points
    const longestObs = Object.values(byObs).sort((a, b) => b.points.length - a.points.length)[0];
    const labels = longestObs ? longestObs.points.map(p => {
      const d = p.x;
      return (typeof formatChartAxisLabel === 'function') ? formatChartAxisLabel(d, false) : d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    }) : [];
    const c = new Chart(ctx, {
      type: 'line',
      data: { labels, datasets },
      options: {
        responsive: true,
        scales: { x: { display: false }, y: { title: { display: true, text: 'SNR (dB)' } } },
        plugins: { legend: { position: 'bottom', labels: { boxWidth: 12, font: { size: 10 } } } }
      }
    });
    charts.push(c);
  }

  function buildPacketTypeChart(data) {
    const ctx = document.getElementById('packetTypeChart');
    if (!ctx) return;
    const items = data.packetTypeBreakdown;
    const c = new Chart(ctx, {
      type: 'doughnut',
      data: {
        labels: items.map(i => PAYLOAD_LABELS[i.payload_type] || 'Type ' + i.payload_type),
        datasets: [{ data: items.map(i => i.count), backgroundColor: items.map((_, i) => CHART_COLORS[i % CHART_COLORS.length]) }]
      },
      options: { responsive: true, plugins: { legend: { position: 'bottom', labels: { boxWidth: 12, font: { size: 10 } } } } }
    });
    charts.push(c);
  }

  function buildObserverChart(data) {
    const ctx = document.getElementById('observerChart');
    if (!ctx) return;
    const obs = data.observerCoverage;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: obs.map(o => (o.observer_name || o.observer_id || '?').slice(0, 20)),
        datasets: [{ label: 'Packets', data: obs.map(o => o.packetCount), backgroundColor: obs.map(o => {
          const snr = o.avgSnr || 0;
          const alpha = Math.min(1, Math.max(0.3, snr / 20));
          return `rgba(74,158,255,${alpha})`;
        }) }]
      },
      options: { indexAxis: 'y', responsive: true, plugins: { legend: { display: false } }, scales: { x: { beginAtZero: true } } }
    });
    charts.push(c);
  }

  function buildHopChart(data) {
    const ctx = document.getElementById('hopChart');
    if (!ctx) return;
    const hops = data.hopDistribution;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: hops.map(h => h.hops + ' hop' + (h.hops !== '1' ? 's' : '')),
        datasets: [{ label: 'Packets', data: hops.map(h => h.count), backgroundColor: 'rgba(81,207,102,0.6)', borderColor: '#51cf66', borderWidth: 1 }]
      },
      options: { responsive: true, plugins: { legend: { display: false } }, scales: { y: { beginAtZero: true } } }
    });
    charts.push(c);
  }

  function buildHeatmap(data) {
    const grid = document.getElementById('heatmapGrid');
    if (!grid) return;
    // Build lookup
    const lookup = {};
    let maxCount = 1;
    data.uptimeHeatmap.forEach(h => {
      const key = h.dayOfWeek + '-' + h.hour;
      lookup[key] = h.count;
      if (h.count > maxCount) maxCount = h.count;
    });

    // Header row
    grid.innerHTML = '<div class="analytics-heatmap-label"></div>';
    for (let h = 0; h < 24; h++) {
      grid.innerHTML += `<div class="analytics-heatmap-label" style="justify-content:center;font-size:9px">${h}</div>`;
    }
    // Day rows
    for (let d = 0; d < 7; d++) {
      grid.innerHTML += `<div class="analytics-heatmap-label">${DAY_NAMES[d]}</div>`;
      for (let h = 0; h < 24; h++) {
        const count = lookup[d + '-' + h] || 0;
        const intensity = count / maxCount;
        const bg = count === 0 ? 'var(--card-bg)' : `rgba(74,158,255,${0.15 + intensity * 0.85})`;
        grid.innerHTML += `<div class="analytics-heatmap-cell" style="background:${bg}" title="${DAY_NAMES[d]} ${h}:00 — ${count} packets"></div>`;
      }
    }
  }

  async function loadBatteryChart(pubkey, days) {
    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/battery?days=' + days);
    } catch (e) {
      const empty = document.getElementById('batteryEmpty');
      if (empty) { empty.style.display = 'block'; empty.textContent = 'Battery data unavailable: ' + e.message; }
      return;
    }
    const ctx = document.getElementById('batteryChart');
    const empty = document.getElementById('batteryEmpty');
    const badge = document.getElementById('batteryStatusBadge');
    const samples = (data && data.samples) || [];
    const thr = (data && data.thresholds) || { low_mv: 3300, critical_mv: 3000 };

    if (badge) {
      const STATUS_COLOR = { ok: '#51cf66', low: '#fcc419', critical: '#ff6b6b', unknown: 'var(--text-muted)' };
      const label = data && data.status === 'ok' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-battery-high"/></svg> OK'
        : data && data.status === 'low' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Low'
        : data && data.status === 'critical' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-battery-low"/></svg> Critical'
        : 'No data';
      const mv = data && data.latest_mv ? ' · ' + data.latest_mv + ' mV' : '';
      badge.textContent = label + mv;
      badge.style.color = STATUS_COLOR[(data && data.status) || 'unknown'];
    }

    if (!ctx || samples.length === 0) {
      if (ctx) ctx.style.display = 'none';
      if (empty) empty.style.display = 'block';
      return;
    }
    if (empty) empty.style.display = 'none';
    ctx.style.display = '';

    const labels = samples.map(p => {
      const d = new Date(p.timestamp);
      return (typeof formatChartAxisLabel === 'function')
        ? formatChartAxisLabel(d, days <= 3)
        : (days <= 3 ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
                     : d.toLocaleDateString([], { month: 'short', day: 'numeric' }));
    });
    const values = samples.map(p => p.battery_mv);

    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: labels,
        datasets: [
          { label: 'Battery (mV)', data: values, borderColor: '#4a9eff', backgroundColor: 'rgba(74,158,255,0.15)', tension: 0.25, pointRadius: 2, fill: true },
          { label: 'Low threshold', data: values.map(() => thr.low_mv), borderColor: '#fcc419', borderDash: [6, 4], pointRadius: 0, fill: false },
          { label: 'Critical', data: values.map(() => thr.critical_mv), borderColor: '#ff6b6b', borderDash: [6, 4], pointRadius: 0, fill: false }
        ]
      },
      options: {
        responsive: true,
        plugins: { legend: { display: true, position: 'bottom' } },
        scales: { x: { ticks: { maxTicksAutoSkip: true, maxRotation: 45 } }, y: { title: { display: true, text: 'mV' } } }
      }
    });
    charts.push(c);
  }

  // ─── Relay Hop-Count (issue #1812) ────────────────────────────────────────
  // Hop count HERE is the node's own 0-based index within a packet's
  // resolved relay path (set server-side, GetNodeHopAnalytics) -- the value
  // MeshCore firmware compares against flood_max/flood_max_advert/
  // flood_max_unscoped before forwarding. Distinct from the Hop
  // Distribution chart above, which is path length to the reporting
  // observer, a different and unrelated number.
  const HOP_TRANSPORT_CHIPS = [
    { key: 'flood', label: 'Flood' },
    { key: 'flood_advert', label: 'Flood Adverts' },
    { key: 'flood_unscoped', label: 'Flood Unscoped' },
    { key: 'direct', label: 'Direct' },
  ];
  let hopAnalyticsPackets = [];
  let hopAnalyticsFilter = 'flood';
  let hopAnalyticsHistChart = null;

  async function loadHopAnalyticsChart(pubkey, days) {
    hopAnalyticsFilter = 'flood';
    try {
      const data = await api('/nodes/' + encodeURIComponent(pubkey) + '/hop_analytics?days=' + days);
      hopAnalyticsPackets = (data && data.packets) || [];
    } catch (e) {
      hopAnalyticsPackets = [];
      const empty = document.getElementById('hopAnalyticsEmpty');
      if (empty) { empty.style.display = 'block'; empty.textContent = 'Hop-count data unavailable: ' + e.message; }
      return;
    }
    renderHopAnalyticsChips();
    renderHopAnalyticsChart();
  }

  function renderHopAnalyticsChips() {
    const container = document.getElementById('hopAnalyticsChips');
    if (!container) return;
    container.innerHTML = HOP_TRANSPORT_CHIPS.map(c =>
      '<button data-hop-transport="' + c.key + '"' + (hopAnalyticsFilter === c.key ? ' class="active"' : '') + '>' + c.label + '</button>'
    ).join('');
    container.querySelectorAll('[data-hop-transport]').forEach(btn => {
      btn.addEventListener('click', () => {
        hopAnalyticsFilter = btn.dataset.hopTransport;
        renderHopAnalyticsChips();
        renderHopAnalyticsChart();
      });
    });
  }

  // Linear-interpolation percentile (same convention as numpy's default) —
  // `sorted` must already be ascending.
  function hopQuantile(sortedVals, p) {
    const idx = p * (sortedVals.length - 1);
    const lo = Math.floor(idx), hi = Math.ceil(idx);
    if (lo === hi) return sortedVals[lo];
    return sortedVals[lo] + (sortedVals[hi] - sortedVals[lo]) * (idx - lo);
  }

  function renderHopAnalyticsChart() {
    const empty = document.getElementById('hopAnalyticsEmpty');
    const boxCanvas = document.getElementById('hopAnalyticsBoxplot');
    const histCanvas = document.getElementById('hopAnalyticsHistogram');
    const summary = document.getElementById('hopAnalyticsSummary');
    if (hopAnalyticsHistChart) { try { hopAnalyticsHistChart.destroy(); } catch (e) {} hopAnalyticsHistChart = null; }

    const filtered = hopAnalyticsPackets.filter(p => p.transport === hopAnalyticsFilter);
    if (filtered.length === 0) {
      if (boxCanvas) boxCanvas.style.display = 'none';
      if (histCanvas) histCanvas.style.display = 'none';
      if (summary) summary.textContent = '';
      if (empty) empty.style.display = 'block';
      return;
    }
    if (empty) empty.style.display = 'none';
    if (boxCanvas) boxCanvas.style.display = '';
    if (histCanvas) histCanvas.style.display = '';

    const hopsSorted = filtered.map(p => p.hops).sort((a, b) => a - b);
    const q = {
      min: hopsSorted[0],
      q1: hopQuantile(hopsSorted, 0.25),
      median: hopQuantile(hopsSorted, 0.5),
      q3: hopQuantile(hopsSorted, 0.75),
      max: hopsSorted[hopsSorted.length - 1],
    };

    if (summary) {
      summary.textContent = filtered.length.toLocaleString() + ' packets — min ' + q.min +
        ', Q1 ' + q.q1.toFixed(1) + ', median ' + q.median.toFixed(1) +
        ', Q3 ' + q.q3.toFixed(1) + ', max ' + q.max;
    }

    drawHopBoxplot(boxCanvas, q, q.max);

    const buckets = new Array(q.max + 1).fill(0);
    hopsSorted.forEach(h => buckets[h]++);
    hopAnalyticsHistChart = new Chart(histCanvas, {
      type: 'bar',
      data: {
        labels: buckets.map((_, i) => String(i)),
        datasets: [{ label: 'Packets', data: buckets, backgroundColor: 'rgba(74,158,255,0.5)', borderColor: '#4a9eff', borderWidth: 1 }]
      },
      options: {
        responsive: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { title: { display: true, text: 'Hop count at this node' } },
          y: { beginAtZero: true, title: { display: true, text: 'Packets' } }
        }
      }
    });
    charts.push(hopAnalyticsHistChart);
  }

  // Hand-drawn boxplot — Chart.js has no built-in boxplot type and this
  // project doesn't load a boxplot plugin. Draws horizontal box+whiskers
  // over [0, maxHops], positioned directly above the histogram with a
  // best-effort matching left padding so the two roughly share an x-axis
  // (exact pixel alignment with Chart.js's own computed padding isn't
  // attempted — close enough to read visually).
  function drawHopBoxplot(canvas, q, maxHops) {
    if (!canvas) return;
    const dpr = window.devicePixelRatio || 1;
    const cssWidth = (canvas.parentElement && canvas.parentElement.clientWidth) || 300;
    const cssHeight = 50;
    canvas.style.width = cssWidth + 'px';
    canvas.style.height = cssHeight + 'px';
    canvas.width = cssWidth * dpr;
    canvas.height = cssHeight * dpr;
    const ctx = canvas.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, cssWidth, cssHeight);

    const style = getComputedStyle(document.documentElement);
    const lineColor = style.getPropertyValue('--text').trim() || '#333';

    const padLeft = 36, padRight = 12;
    const plotW = Math.max(10, cssWidth - padLeft - padRight);
    const domainMax = Math.max(1, maxHops);
    const xFor = v => padLeft + (v / domainMax) * plotW;

    const midY = cssHeight / 2;
    const boxH = 20;

    ctx.strokeStyle = lineColor;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(xFor(q.min), midY);
    ctx.lineTo(xFor(q.q1), midY);
    ctx.moveTo(xFor(q.q3), midY);
    ctx.lineTo(xFor(q.max), midY);
    ctx.stroke();
    ctx.beginPath();
    ctx.moveTo(xFor(q.min), midY - boxH / 4); ctx.lineTo(xFor(q.min), midY + boxH / 4);
    ctx.moveTo(xFor(q.max), midY - boxH / 4); ctx.lineTo(xFor(q.max), midY + boxH / 4);
    ctx.stroke();

    ctx.fillStyle = 'rgba(74,158,255,0.35)';
    ctx.strokeStyle = '#4a9eff';
    ctx.lineWidth = 1.5;
    const boxX = xFor(q.q1), boxW = Math.max(1, xFor(q.q3) - xFor(q.q1));
    ctx.fillRect(boxX, midY - boxH / 2, boxW, boxH);
    ctx.strokeRect(boxX, midY - boxH / 2, boxW, boxH);

    ctx.beginPath();
    ctx.moveTo(xFor(q.median), midY - boxH / 2);
    ctx.lineTo(xFor(q.median), midY + boxH / 2);
    ctx.strokeStyle = '#4a9eff';
    ctx.lineWidth = 2;
    ctx.stroke();
  }

  function init(container, routeParam) {
    // routeParam is "PUBKEY/analytics"
    if (!routeParam || !routeParam.endsWith('/analytics')) {
      container.innerHTML = '<div style="padding:40px;text-align:center">Invalid analytics URL</div>';
      return;
    }
    const pubkey = routeParam.slice(0, -'/analytics'.length);
    loadAnalytics(container, pubkey, 7);
  }

  function destroy() {
    destroyCharts();
    currentPubkey = null;
  }

  // Expose for testing
  if (typeof window !== 'undefined') {
    window._nodeAnalyticsHopQuantile = hopQuantile;
    window._nodeAnalyticsLoadHopChart = loadHopAnalyticsChart;
    window._nodeAnalyticsGetHopFilter = function() { return hopAnalyticsFilter; };
    window._nodeAnalyticsSetHopFilter = function(v) { hopAnalyticsFilter = v; };
    window._nodeAnalyticsRenderHopChart = renderHopAnalyticsChart;
    window._nodeAnalyticsGetHopPackets = function() { return hopAnalyticsPackets; };
  }

  registerPage('node-analytics', { init, destroy });
})();
