/* === CoreScope — node-analytics.js === */
'use strict';
(function () {
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

  // The server sends only the hours that hold packets. Put every hour of the
  // covered window (dataFrom when partial, else from) on the axis, silent
  // ones as 0, or an outage collapses into two neighbouring bars. Above
  // DAY_BUCKETS_ABOVE_HOURS the hours are summed per UTC day: 337 hourly bars
  // (14 days in memory) are about 1.5 px wide and a quiet node looks empty.
  const DAY_BUCKETS_ABOVE_HOURS = 8 * 24;
  function fillTimeline(tl, tr) {
    if (!tr || !tr.to) return tl;
    const start = Date.parse(tr.dataFrom || tr.from);
    const end = Date.parse(tr.to);
    if (isNaN(start) || isNaN(end) || end < start) return tl;
    const HOUR = 3600000;
    const daily = (end - start) / HOUR > DAY_BUCKETS_ABOVE_HOURS;
    // Rows from older ingestors carry "2006-01-02 15:04:05", so their bucket
    // has a space where the ISO key has a T.
    const key = iso => daily ? iso.slice(0, 10) + 'T00:00:00Z' : iso.slice(0, 13) + ':00:00Z';
    const byKey = {};
    tl.forEach(b => {
      const k = key(String(b.bucket).replace(' ', 'T'));
      const c = b.certain == null ? b.count : b.certain;
      const cur = byKey[k];
      byKey[k] = cur ? { bucket: k, count: cur.count + b.count, certain: cur.certain + c }
        : { bucket: k, count: b.count, certain: c };
      if (daily) byKey[k].day = true;
    });
    for (let ms = Math.floor(start / HOUR) * HOUR; ms <= end; ms += HOUR) {
      const k = key(new Date(ms).toISOString());
      if (!byKey[k]) byKey[k] = daily ? { bucket: k, count: 0, certain: 0, day: true } : { bucket: k, count: 0, certain: 0 };
    }
    return Object.keys(byKey).sort().map(k => byKey[k]);
  }

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
  window.NodeAnalyticsTimeline = { series: timelineSeries, fill: fillTimeline };

  // "Heard by observers": one row per fixed observer that heard this node
  // itself in the window (heardBy from the analytics API).
  const HEARD_ROWS = 10;
  const HEARD_CHANGE_DB = 3;
  const HEARD_SCALE_DB = 20;

  function stripPct(v) {
    return Math.max(0, Math.min(100, (v + HEARD_SCALE_DB) / (2 * HEARD_SCALE_DB) * 100));
  }

  function changeCell(recent, earlier) {
    if (recent == null || earlier == null) return { text: '', cls: '' };
    const d = recent - earlier;
    if (Math.abs(d) < HEARD_CHANGE_DB) return { text: '=', cls: '' };
    return { text: (d > 0 ? '+' : '') + d.toFixed(1), cls: d > 0 ? 'status-ok' : 'status-warn' };
  }

  function dotClass(median) {
    if (median == null) return '';
    if (median >= 0) return 'na-heard-good';
    if (median >= -10) return 'na-heard-fair';
    return 'na-heard-weak';
  }

  function dbText(v) {
    return v == null ? '' : Number(v).toFixed(1) + ' dB';
  }

  function heardStrip(r) {
    if (r.medianSnr == null) return '';
    const lo = stripPct(r.p10Snr), hi = stripPct(r.p90Snr);
    return '<div class="na-heard-strip" title="' + escapeHtml('p10 ' + dbText(r.p10Snr) + ', median ' + dbText(r.medianSnr) + ', p90 ' + dbText(r.p90Snr)) + '">' +
      '<span class="na-heard-zero"></span>' +
      '<span class="na-heard-range" style="left:' + lo.toFixed(1) + '%;width:' + (hi - lo).toFixed(1) + '%"></span>' +
      '<span class="na-heard-dot ' + dotClass(r.medianSnr) + '" style="left:' + stripPct(r.medianSnr).toFixed(1) + '%"></span>' +
      '</div>';
  }

  function mobileGroup(m, pubkey) {
    if (!m) return '';
    const parts = [m.receptions + ' reception' + (m.receptions === 1 ? '' : 's'), m.receivers + ' receiver' + (m.receivers === 1 ? '' : 's')];
    if (m.medianSnr != null) parts.push('median ' + dbText(m.medianSnr));
    if (m.maxDistanceKm != null) parts.push('furthest ' + Number(m.maxDistanceKm).toFixed(1) + ' km');
    return '<details class="na-heard-mobile"><summary>Mobile receptions (' + m.receptions + ')</summary>' +
      '<div class="na-heard-mobile-row">' + escapeHtml(parts.join(' · ')) +
      ' · <a href="#/nodes/' + encodeURIComponent(pubkey) + '/reach">Reach</a></div>' +
      '<div class="analytics-chart-desc">Phones with a companion radio that heard this node directly. Not part of the stats above.</div>' +
      '</details>';
  }

  function heardByHtml(data, opts) {
    const pubkey = opts.pubkey;
    const rows = data.heardBy || [];
    const nodeLink = '<a href="#/nodes/' + encodeURIComponent(pubkey) + '?section=node-observers">Heard By, direct</a>';
    let body;
    if (!rows.length) {
      body = '<div class="na-heard-empty">No observer heard this node directly in this window. ' +
        'Only a zero-hop advert or a flood packet whose last hop names this node with 2 or more bytes, shared with no other node, proves who was heard. ' +
        'An empty list is missing evidence, not proof of missing coverage.</div>';
    } else {
      const shown = opts.showAll ? rows : rows.slice(0, HEARD_ROWS);
      const withChange = rows.some(r => r.recentMedianSnr != null);
      body = '<div class="analytics-table-scroll"><table class="na-heard-table">' +
        '<thead><tr><th scope="col">Observer</th><th scope="col" class="num">Receptions</th>' +
        '<th scope="col">SNR, -20 to +20 dB (bar p10 to p90, dot median)</th><th scope="col" class="num">Median</th>' +
        (withChange ? '<th scope="col" class="num" title="Median of the last 24 h minus the median of the rest of the window">24 h change</th>' : '') +
        '</tr></thead><tbody>' +
        shown.map(r => {
          const c = changeCell(r.recentMedianSnr, r.earlierMedianSnr);
          return '<tr data-testid="heard-by-row">' +
            '<td><a href="#/observers/' + encodeURIComponent(r.observer_id) + '">' + escapeHtml(r.observer_name || r.observer_id) + '</a></td>' +
            '<td class="num">' + r.count + '</td>' +
            '<td class="na-heard-strip-cell">' + heardStrip(r) + '</td>' +
            '<td class="num">' + dbText(r.medianSnr) + '</td>' +
            (withChange ? '<td class="num ' + c.cls + '">' + c.text + '</td>' : '') +
            '</tr>';
        }).join('') +
        '</tbody></table></div>' +
        (rows.length > shown.length ? '<button type="button" class="na-heard-showall" data-action="heard-show-all">Show all ' + rows.length + '</button>' : '');
    }
    return body +
      '<div class="analytics-chart-desc">Same evidence as the node page’s ' + nodeLink + ' table.</div>' +
      mobileGroup(data.mobileReceptions, pubkey);
  }

  // Signal Grade comes from the direct observers' medians; without one the
  // server's "D" means no evidence, not a bad signal. color null = grade colour.
  function gradeView(data) {
    const has = (data.heardBy || []).some(r => r.medianSnr != null);
    if (!has) return { text: 'n/a', color: 'var(--text-muted)' };
    return { text: data.computedStats.signalGrade, color: null };
  }

  window.NodeAnalyticsHeardBy = { html: heardByHtml, stripPct, changeCell, dotClass, gradeView };

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
    const grade = gradeView(data);
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
            <div class="analytics-stat-value" style="color:${grade.color || GRADE_COLORS[grade.text] || 'var(--text)'}">${grade.text}</div>
            <div class="analytics-stat-desc">From the median SNR of each observer that heard this node directly; each observer counts once</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Packets / Day</div>
            <div class="analytics-stat-value">${rateText(s.avgPacketsPerDay)}</div>
            <div class="analytics-stat-desc">Average daily packet volume in this window</div>
          </div>
          <div class="analytics-stat-card">
            <div class="analytics-stat-label">Observers</div>
            <div class="analytics-stat-value">${s.uniqueObservers}</div>
            <div class="analytics-stat-desc">Fixed observers that heard this node directly</div>
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
          <div class="analytics-chart-card full" id="heardByCard">
            <h4>Heard by observers</h4>
            <div class="analytics-chart-desc">Fixed observers that received this node's own transmissions in this window, with the spread of their SNR. Relayed copies are left out: they measure the relay, not this node.</div>
            <div id="heardByBody">${heardByHtml(data, { pubkey: n.public_key })}</div>
          </div>
          <div class="analytics-chart-card">
            <h4>Packet Types</h4>
            <div class="analytics-chart-desc">Breakdown of advert, position, text, and other packet types</div>
            <canvas id="packetTypeChart" role="img" aria-label="Packet types chart"></canvas>
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
                <td>${renderTimestampHtml(p.lastContact)}</td>
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
    buildPacketTypeChart(data);
    const heardBody = document.getElementById('heardByBody');
    if (heardBody) heardBody.addEventListener('click', e => {
      if (!e.target.closest('[data-action="heard-show-all"]')) return;
      heardBody.innerHTML = heardByHtml(data, { pubkey: n.public_key, showAll: true });
    });
    buildHopChart(data);
    buildHeatmap(data);
    loadBatteryChart(pubkey, currentDays, isCurrent);
    NodeHopAnalytics.load(document.getElementById('hopCountSection'), pubkey, currentDays);
  }

  function buildActivityChart(data) {
    const ctx = document.getElementById('activityChart');
    if (!ctx) return;
    const tl = fillTimeline(data.activityTimeline, data.timeRange);
    const series = timelineSeries(tl);
    const style = getComputedStyle(document.documentElement);
    const certainColor = style.getPropertyValue('--accent').trim();
    const uncertainColor = style.getPropertyValue('--text-muted').trim();
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: tl.map(b => {
          // A day bucket is a UTC day: label it by its date, a local time
          // of its midnight would read as one hour.
          if (b.day) return b.bucket.slice(0, 10) + ' UTC';
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

  function buildPacketTypeChart(data) {
    const ctx = document.getElementById('packetTypeChart');
    if (!ctx) return;
    const items = data.packetTypeBreakdown;
    const c = new Chart(ctx, {
      type: 'doughnut',
      data: {
        labels: items.map(i => window.PayloadLabels.api.displayName(i.payload_type)),
        datasets: [{ data: items.map(i => i.count), backgroundColor: items.map((_, i) => CHART_COLORS[i % CHART_COLORS.length]) }]
      },
      options: { responsive: true, plugins: { legend: { position: 'bottom', labels: { boxWidth: 12, font: { size: 10 } } } } }
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
