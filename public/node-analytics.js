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
  // A time-range click is a new load on the same visit: only the newest may
  // render (#2218).
  let loadSeq = 0;

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

  // #748: the server only holds its in-memory window. When that starts
  // after the requested range, say from when the data runs, why, and what
  // the rates cover; '' otherwise. stats.availabilityPct is null when the
  // window is too short for rates.
  function coverageNotice(tr, stats) {
    if (!tr || !tr.partial || !tr.dataFrom || !tr.to) return '';
    const from = Date.parse(tr.dataFrom);
    const to = Date.parse(tr.to);
    if (isNaN(from) || isNaN(to)) return '';
    const spanDays = Math.max(1, Math.round((to - from) / 86400000));
    let why;
    if (tr.dataFromReason === 'database') why = 'the database holds no older packets';
    else if (tr.dataFromReason === 'loading') why = 'the server is still loading older packets';
    else why = 'the server keeps about ' + spanDays + ' day' + (spanDays === 1 ? '' : 's') + ' of packets in memory';
    const rates = stats && stats.availabilityPct == null
      ? 'That is under a day, too short for availability and packets/day.'
      : 'Availability and packets/day cover that period, not the full ' + tr.days + ' days.';
    return '<div class="analytics-coverage-notice" data-testid="analytics-coverage-notice" role="note">' +
      'Data from ' + new Date(from).toISOString().slice(0, 10) + ': ' + why + '. ' + rates + '</div>';
  }

  // A rate the server left out (window too short, #748) reads as a dash.
  function rateText(v, suffix) {
    return v == null ? '—' : v + (suffix || '');
  }
  window.NodeAnalyticsCoverage = { notice: coverageNotice, rateText: rateText };

  // #2260: split each timeline bucket into packets that can only be this
  // node (certain) and packets attributed to it through a hop prefix it
  // shares with other nodes (uncertain). A server without the certain field
  // counts everything as certain, as before.
  function timelineSeries(tl) {
    const certain = [], uncertain = [];
    (tl || []).forEach(b => {
      const count = Number(b.count) || 0;
      const c = b.certain == null ? count : Math.min(Number(b.certain) || 0, count);
      certain.push(c);
      uncertain.push(count - c);
    });
    return { certain, uncertain };
  }
  window.NodeAnalyticsTimeline = { series: timelineSeries };

  async function loadAnalytics(container, pubkey, days) {
    const gen = viewGeneration();
    const seq = ++loadSeq;
    const isCurrent = () => gen === viewGeneration() && seq === loadSeq;
    currentPubkey = pubkey;
    currentDays = days;
    destroyCharts();
    chartDefaults();

    container.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading analytics…</div>';

    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/analytics?days=' + days, { ttl: CLIENT_TTL.nodeAnalytics });
    } catch (e) {
      if (!isCurrent()) return;
      container.innerHTML = '<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load analytics: ' + escapeHtml(e.message) + '</div>';
      return;
    }
    if (!isCurrent()) return;

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
        ${coverageNotice(data.timeRange, s)}

        <div class="analytics-stats">
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Availability</div>
            <div class="analytics-stat-value">${rateText(s.availabilityPct, '%')}</div>
            <div class="analytics-stat-desc">% of hours in which this node was certainly heard</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Signal Grade</div>
            <div class="analytics-stat-value" style="color:${GRADE_COLORS[s.signalGrade]||'var(--text)'}">${s.signalGrade}</div>
            <div class="analytics-stat-desc">A–F based on average SNR across all observers</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Packets / Day</div>
            <div class="analytics-stat-value">${rateText(s.avgPacketsPerDay)}</div>
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
            <div class="analytics-stat-desc">Longest gap without a packet certainly from this node, including one still running</div>
          </div>
        </div>

        <div class="analytics-charts">
          <div class="analytics-chart-card full">
            <h4>Activity Timeline</h4>
            <div class="analytics-chart-desc">Packet count per time bucket. Certain: can only be this node. Uncertain: a hop prefix this node shares with others, so possibly another node.</div>
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
            <h4>Hop Count at This Node</h4>
            <div class="analytics-chart-desc">For each flood packet this node forwarded: how many hops it already had, the number the flood.max, flood.max.unscoped and flood.max.advert limits are checked against. Bars count packets per hop count; the box above spans the middle half of the packets with a line at the median, and the whiskers reach the furthest packet within 1.5 box widths of the box.</div>
            <div id="hopCountSection"><div style="padding:20px;text-align:center;color:var(--text-muted);font-size:12px">Loading hop counts...</div></div>
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
    loadBatteryChart(pubkey, currentDays, isCurrent);
    NodeHopAnalytics.load(document.getElementById('hopCountSection'), pubkey, currentDays);
  }

  function buildActivityChart(data) {
    const ctx = document.getElementById('activityChart');
    if (!ctx) return;
    const tl = data.activityTimeline;
    const series = timelineSeries(tl);
    const style = getComputedStyle(document.documentElement);
    const certainColor = style.getPropertyValue('--accent').trim();
    const uncertainColor = style.getPropertyValue('--text-muted').trim();
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: tl.map(b => {
          const d = new Date(b.bucket);
          return (typeof formatChartAxisLabel === 'function') ? formatChartAxisLabel(d, currentDays <= 3) : (currentDays <= 3 ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleDateString([], { month: 'short', day: 'numeric' }));
        }),
        datasets: [
          { label: 'Certain', data: series.certain, backgroundColor: certainColor, borderWidth: 0, stack: 'p' },
          { label: 'Uncertain', data: series.uncertain, backgroundColor: uncertainColor, borderWidth: 0, stack: 'p' }
        ]
      },
      options: { responsive: true, plugins: { legend: { display: true, position: 'bottom', labels: { boxWidth: 12 } } }, scales: { x: { stacked: true, ticks: { maxTicksAutoSkip: true, maxRotation: 45 } }, y: { stacked: true, beginAtZero: true } } }
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

  async function loadBatteryChart(pubkey, days, isCurrent) {
    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/battery?days=' + days);
    } catch (e) {
      if (!isCurrent()) return;
      const empty = document.getElementById('batteryEmpty');
      if (empty) { empty.style.display = 'block'; empty.textContent = 'Battery data unavailable: ' + e.message; }
      return;
    }
    if (!isCurrent()) return;
    const ctx = document.getElementById('batteryChart');
    const empty = document.getElementById('batteryEmpty');
    const badge = document.getElementById('batteryStatusBadge');
    const samples = (data && data.samples) || [];
    const thr = (data && data.thresholds) || { low_mv: 3300, critical_mv: 3000 };

    if (badge) {
      const STATUS_COLOR = { ok: 'var(--success)', low: 'var(--warning)', critical: 'var(--danger)', unknown: 'var(--text-muted)' };
      const label = data && data.status === 'ok' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-battery-high"/></svg> OK'
        : data && data.status === 'low' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Low'
        : data && data.status === 'critical' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-battery-low"/></svg> Critical'
        : 'No data';
      const mv = data && data.latest_mv != null ? ' · ' + data.latest_mv + ' mV' : '';
      badge.innerHTML = label; // Only fixed status markup is parsed as HTML.
      badge.insertAdjacentText('beforeend', mv);
      badge.style.color = Object.prototype.hasOwnProperty.call(STATUS_COLOR, data && data.status)
        ? STATUS_COLOR[data.status] : STATUS_COLOR.unknown;
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
    NodeHopAnalytics.destroy();
    currentPubkey = null;
  }

  registerPage('node-analytics', { init, destroy });
})();
