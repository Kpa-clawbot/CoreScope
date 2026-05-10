/* === CoreScope — observer-detail.js === */
'use strict';
(function () {
  const PAYLOAD_LABELS = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
  const CHART_COLORS = ['#4a9eff', '#ff6b6b', '#51cf66', '#fcc419', '#cc5de8', '#20c997', '#ff922b', '#845ef7', '#f06595', '#339af0'];

  let charts = [];
  let currentMinutes = parseInt(localStorage.getItem('obs-detail-minutes') || '') || 7 * 1440;
  let currentId = null;
  let wsHandler = null;
  let brokerTickTimer = null;
  let lastObs = null;

  function destroyCharts() {
    charts.forEach(c => { try { c.destroy(); } catch {} });
    charts = [];
  }

  function chartDefaults() {
    const style = getComputedStyle(document.documentElement);
    Chart.defaults.color = style.getPropertyValue('--text-muted').trim() || '#6b7280';
    Chart.defaults.borderColor = style.getPropertyValue('--border').trim() || '#e2e5ea';
  }

  function formatDuration(secs) {
    if (!secs) return '—';
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    const m = Math.floor((secs % 3600) / 60);
    if (d > 0) return d + 'd ' + h + 'h';
    if (h > 0) return h + 'h ' + m + 'm';
    return m + 'm';
  }

  function init(app, routeParam) {
    currentId = routeParam;
    if (!currentId) {
      app.innerHTML = '<div class="text-center text-muted" style="padding:40px">No observer ID specified.</div>';
      return;
    }

    app.innerHTML = `
      <div class="observer-detail-page" style="padding:16px">
        <div class="page-header" style="display:flex;align-items:center;gap:12px;margin-bottom:16px">
          <a href="#/observers" class="btn-icon" title="Back to Observers" aria-label="Back">←</a>
          <h2 style="margin:0" id="obsTitle">Observer Detail</h2>
          <div style="margin-left:auto;display:flex;gap:8px">
            <select id="obsDaysSelect" class="time-range-select" aria-label="Time range">
              <option value="20">20 Minutes</option>
              <option value="60">1 Hour</option>
              <option value="180">3 Hours</option>
              <option value="1440">24 Hours</option>
              <option value="4320">3 Days</option>
              <option value="10080" selected>7 Days</option>
              <option value="43200">30 Days</option>
            </select>
          </div>
        </div>
        <div id="obsDetailContent"><div class="text-center text-muted" style="padding:40px">Loading…</div></div>
      </div>`;

    var sel = document.getElementById('obsDaysSelect');
    sel.value = String(currentMinutes);
    sel.addEventListener('change', function (e) {
      currentMinutes = parseInt(e.target.value);
      localStorage.setItem('obs-detail-minutes', String(currentMinutes));
      loadDetail();
    });

    loadDetail();

    // Re-fetch broker sources every 30s so status-packet last_seen updates
    // are reflected even when no raw radio packets arrive (status packets
    // update observer_sources but don't emit a WS broadcast event).
    brokerTickTimer = setInterval(function () {
      if (!currentId) return;
      api('/observers/' + encodeURIComponent(currentId)).then(function (fresh) {
        lastObs = fresh;
        renderBrokerSources(fresh);
      }).catch(function () {});
    }, 30000);

    // Re-fetch observer when a packet arrives from this observer so broker
    // last-seen and packet counts stay current without a full page reload.
    wsHandler = debouncedOnWS(function (msgs) {
      if (!currentId) return;
      var relevant = msgs.some(function (m) {
        return m.type === 'packet' && m.data && m.data.observer_id === currentId;
      });
      if (!relevant) return;
      api('/observers/' + encodeURIComponent(currentId), { ttl: 0 }).then(function (obs) {
        lastObs = obs;
        renderBrokerSources(obs);
      }).catch(function () {});
    });
  }

  function destroy() {
    destroyCharts();
    if (wsHandler) { offWS(wsHandler); wsHandler = null; }
    if (brokerTickTimer) { clearInterval(brokerTickTimer); brokerTickTimer = null; }
    currentId = null;
    lastObs = null;
  }

  async function loadDetail() {
    try {
      destroyCharts();
      chartDefaults();
      const since = new Date(Date.now() - currentMinutes * 60000).toISOString();
      const metricsRes = currentMinutes <= 1440 ? '5m' : currentMinutes <= 10080 ? '1h' : '1d';
      const [obs, analytics, obsSkewArr, metrics] = await Promise.all([
        api('/observers/' + encodeURIComponent(currentId)),
        api('/observers/' + encodeURIComponent(currentId) + '/analytics?minutes=' + currentMinutes),
        api('/observers/clock-skew', { ttl: 30000 }).catch(function() { return []; }),
        api('/observers/' + encodeURIComponent(currentId) + '/metrics?since=' + encodeURIComponent(since) + '&resolution=' + metricsRes).catch(function() { return null; }),
      ]);
      // Find this observer's calibration data.
      var obsSkew = null;
      (Array.isArray(obsSkewArr) ? obsSkewArr : []).forEach(function(s) {
        if (s && s.observerID === currentId) obsSkew = s;
      });
      renderDetail(obs, analytics, obsSkew, metrics);
    } catch (e) {
      document.getElementById('obsDetailContent').innerHTML =
        '<div class="text-muted" style="padding:40px">Error: ' + e.message + '</div>';
    }
  }

  function renderBrokerSources(obs) {
    var el = document.getElementById('obsBrokerSources');
    if (!el) return;
    if (!obs.ingestSources || !obs.ingestSources.length) {
      el.innerHTML = '';
      return;
    }
    el.innerHTML = `
      <div class="node-full-card" style="margin-bottom:20px;padding:12px">
        <h4 style="margin:0 0 8px">Broker Sources</h4>
        <table style="width:100%;border-collapse:collapse;font-size:0.85em">
          <thead>
            <tr style="text-align:left;color:var(--text-muted)">
              <th style="padding:4px 8px 4px 0;font-weight:600">Broker</th>
              <th style="padding:4px 8px 4px 0;font-weight:600">Packets</th>
              <th style="padding:4px 8px 4px 0;font-weight:600">Status</th>
              <th style="padding:4px 0;font-weight:600">Last Seen</th>
            </tr>
          </thead>
          <tbody>
            ${obs.ingestSources.map(function(s) {
              return '<tr>' +
                '<td style="padding:4px 8px 4px 0">' + (s.name || s.host) + '<br><span style="color:var(--text-muted);font-size:0.82em;font-family:var(--mono)">' + s.host + '</span></td>' +
                '<td style="padding:4px 8px 4px 0;font-family:var(--mono)">' + (s.packetCount || 0).toLocaleString() + '</td>' +
                '<td style="padding:4px 8px 4px 0;font-family:var(--mono)">' + (s.statusCount || 0).toLocaleString() + '</td>' +
                '<td style="padding:4px 0">' + timeAgo(s.last_seen) + '<span style="color:var(--text-muted);font-size:0.85em;margin-left:6px">' + new Date(s.last_seen).toLocaleString() + '</span></td>' +
                '</tr>';
            }).join('')}
          </tbody>
        </table>
      </div>`;
  }

  function renderDetail(obs, analytics, obsSkew, metrics) {
    lastObs = obs;
    const el = document.getElementById('obsDetailContent');
    if (!el) return;

    const title = document.getElementById('obsTitle');
    if (title) title.textContent = obs.name || obs.id.substring(0, 16) + '…';

    // Parse radio string
    let radioHtml = '—';
    if (obs.radio) {
      const rp = obs.radio.split(',');
      radioHtml = rp[0] + ' MHz · SF' + (rp[2] || '?') + ' · BW' + (rp[1] || '?') + ' · CR' + (rp[3] || '?');
    }

    // Health status
    const ago = obs.last_seen ? Date.now() - new Date(obs.last_seen).getTime() : Infinity;
    const statusCls = ago < 600000 ? 'health-green' : ago < HEALTH_THRESHOLDS.nodeDegradedMs ? 'health-yellow' : 'health-red';
    const statusLabel = ago < 600000 ? 'Online' : ago < HEALTH_THRESHOLDS.nodeDegradedMs ? 'Stale' : 'Offline';

    el.innerHTML = `
      <div class="obs-info-grid" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin-bottom:20px">
        <div class="stat-card">
          <div class="stat-label">Status</div>
          <div class="stat-value"><span class="health-dot ${statusCls}">●</span> ${statusLabel}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Region</div>
          <div class="stat-value">${obs.iata ? '<span class="badge-region">' + obs.iata + '</span>' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Model</div>
          <div class="stat-value">${obs.model || '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Firmware</div>
          <div class="stat-value" style="font-size:0.8em;word-break:break-all">${obs.firmware || '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Client</div>
          <div class="stat-value" style="font-size:0.8em;word-break:break-all">${obs.client_version || '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Radio</div>
          <div class="stat-value" style="font-size:0.85em">${radioHtml}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Battery</div>
          <div class="stat-value">${obs.battery_mv ? obs.battery_mv + ' mV' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Uptime</div>
          <div class="stat-value">${formatDuration(obs.uptime_secs)}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Noise Floor</div>
          <div class="stat-value">${obs.noise_floor != null ? obs.noise_floor + ' dBm' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Total Packets</div>
          <div class="stat-value">${(obs.packet_count || 0).toLocaleString()}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Packets/Hour</div>
          <div class="stat-value">${(obs.packetsLastHour || 0).toLocaleString()}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">First Seen</div>
          <div class="stat-value" style="font-size:0.85em">${obs.first_seen ? new Date(obs.first_seen).toLocaleDateString() : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Last Status Update</div>
          <div class="stat-value" style="font-size:0.85em">${obs.last_seen ? timeAgo(obs.last_seen) + '<br><span style="font-size:0.8em;color:var(--text-muted)">' + new Date(obs.last_seen).toLocaleString() + '</span>' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Last Packet Observation</div>
          <div class="stat-value" style="font-size:0.85em">${obs.last_packet_at ? timeAgo(obs.last_packet_at) + '<br><span style="font-size:0.8em;color:var(--text-muted)">' + new Date(obs.last_packet_at).toLocaleString() + '</span>' : '<span style="color:var(--text-muted)">never</span>'}</div>
        </div>
        ${obs.ingestSources && obs.ingestSources.length > 0 ? `
        <div class="stat-card">
          <div class="stat-label">Brokers</div>
          <div class="stat-value">${obs.ingestSources.length}</div>
        </div>` : ''}
      </div>
      <div class="mono" style="font-size:0.75em;color:var(--text-muted);margin-bottom:20px;word-break:break-all">
        ID: ${obs.id}
      </div>
      <div id="obsBrokerSources"></div>
      ${obsSkew && obsSkew.samples > 0 ? `
      <div class="node-full-card skew-detail-section" style="margin-bottom:20px;padding:12px">
        <h4 style="margin:0 0 6px">⏰ Clock Offset</h4>
        <div style="display:flex;align-items:center;gap:12px;flex-wrap:wrap">
          <span style="font-size:18px;font-weight:700;font-family:var(--mono)">${formatSkew(obsSkew.offsetSec)}</span>
          ${renderSkewBadge(observerSkewSeverity(obsSkew.offsetSec), obsSkew.offsetSec)}
          <span class="text-muted" style="font-size:12px">${obsSkew.samples} sample${obsSkew.samples !== 1 ? 's' : ''}</span>
        </div>
        <div style="font-size:12px;color:var(--text-muted);margin-top:8px;max-width:600px">
          <strong>How this is computed:</strong> when this observer and another observer see the same packet, we compare their receive timestamps. The median deviation across all multi-observer packets is this observer's offset.
        </div>
      </div>` : ''}
      <div class="obs-charts" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(400px,1fr));gap:16px">
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Packets Over Time</h3>
          <canvas id="obsTimeChart" role="img" aria-label="Packets over time chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Packet Types</h3>
          <div style="max-width:280px;margin:0 auto"><canvas id="obsTypeChart" role="img" aria-label="Packet types chart"></canvas></div>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Unique Nodes Heard</h3>
          <canvas id="obsNodesChart" role="img" aria-label="Unique nodes heard chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">SNR Distribution</h3>
          <canvas id="obsSnrChart" role="img" aria-label="SNR distribution chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Uptime</h3>
          <canvas id="obsUptimeChart" role="img" aria-label="Uptime chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Battery</h3>
          <canvas id="obsBatteryChart" role="img" aria-label="Battery voltage chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Noise Floor</h3>
          <canvas id="obsNoiseFloorChart" role="img" aria-label="Noise floor chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">RSSI (avg per period)</h3>
          <canvas id="obsRssiChart" role="img" aria-label="RSSI chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em" id="obsAirtimeTitle">Airtime Utilization (%)</h3>
          <canvas id="obsAirtimeChart" role="img" aria-label="Airtime utilization chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">Receive Errors</h3>
          <canvas id="obsRecvErrorsChart" role="img" aria-label="Receive errors chart"></canvas>
        </div>
        <div class="chart-card" style="padding:12px">
          <h3 style="margin:0 0 8px;font-size:0.95em">TX Queue Length</h3>
          <canvas id="obsQueueLenChart" role="img" aria-label="TX queue length chart"></canvas>
        </div>
      </div>
      <div style="margin-top:20px">
        <h3 style="font-size:0.95em">Recent Packets</h3>
        <div id="obsRecentPackets"><div class="text-muted">Loading…</div></div>
      </div>`;

    // Render charts
    if (analytics.timeline && analytics.timeline.length > 0) {
      renderTimelineChart(analytics.timeline);
    }
    if (analytics.packetTypes) {
      renderTypeChart(analytics.packetTypes);
    }
    if (analytics.nodesTimeline && analytics.nodesTimeline.length > 0) {
      renderNodesChart(analytics.nodesTimeline);
    }
    if (analytics.snrDistribution && analytics.snrDistribution.length > 0) {
      renderSnrChart(analytics.snrDistribution);
    }
    var uptimePoints = metrics && metrics.metrics ? metrics.metrics.filter(function(m) { return m.uptime_secs != null; }) : [];
    if (uptimePoints.length > 0) {
      renderUptimeChart(uptimePoints);
    }
    var batteryPoints = metrics && metrics.metrics ? metrics.metrics.filter(function(m) { return m.battery_mv != null; }) : [];
    if (batteryPoints.length > 0) {
      renderBatteryChart(batteryPoints);
    }
    var noiseFloorPoints = metrics && metrics.metrics ? metrics.metrics.filter(function(m) { return m.noise_floor != null; }) : [];
    if (noiseFloorPoints.length > 0) {
      renderNoiseFloorChart(noiseFloorPoints);
    }
    if (analytics.rssiTimeline && analytics.rssiTimeline.length > 0) {
      renderRssiChart(analytics.rssiTimeline);
    }
    var airtimePoints = metrics && metrics.metrics ? metrics.metrics.filter(function(m) { return m.tx_airtime_pct != null || m.rx_airtime_pct != null; }) : [];
    if (airtimePoints.length > 0) {
      renderAirtimeChart(airtimePoints);
    }
    var recvErrorPoints = metrics && metrics.metrics ? metrics.metrics.filter(function(m) { return m.recv_errors != null; }) : [];
    if (recvErrorPoints.length > 0) {
      renderRecvErrorsChart(recvErrorPoints);
    }
    var queueLenPoints = metrics && metrics.metrics ? metrics.metrics.filter(function(m) { return m.queue_len != null; }) : [];
    if (queueLenPoints.length > 0) {
      renderQueueLenChart(queueLenPoints);
    }
    if (analytics.recentPackets) {
      renderRecentPackets(analytics.recentPackets);
    }
    renderBrokerSources(obs);
  }

  function renderTimelineChart(timeline) {
    const ctx = document.getElementById('obsTimeChart');
    if (!ctx) return;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: timeline.map(t => t.label),
        datasets: [{
          label: 'Packets',
          data: timeline.map(t => t.count),
          backgroundColor: CHART_COLORS[0] + '80',
          borderColor: CHART_COLORS[0],
          borderWidth: 1,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { beginAtZero: true, ticks: { precision: 0 } }
        }
      }
    });
    charts.push(c);
  }

  function renderTypeChart(types) {
    const ctx = document.getElementById('obsTypeChart');
    if (!ctx) return;
    const labels = Object.keys(types).map(k => PAYLOAD_LABELS[k] || 'Type ' + k);
    const values = Object.values(types);
    const c = new Chart(ctx, {
      type: 'doughnut',
      data: {
        labels: labels,
        datasets: [{ data: values, backgroundColor: CHART_COLORS.slice(0, labels.length) }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { position: 'bottom', labels: { boxWidth: 12 } } }
      }
    });
    charts.push(c);
  }

  function renderNodesChart(timeline) {
    const ctx = document.getElementById('obsNodesChart');
    if (!ctx) return;
    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: timeline.map(t => t.label),
        datasets: [{
          label: 'Unique Nodes',
          data: timeline.map(t => t.count),
          borderColor: CHART_COLORS[2],
          backgroundColor: CHART_COLORS[2] + '20',
          fill: true, tension: 0.3, pointRadius: 2,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { beginAtZero: true, ticks: { precision: 0 } }
        }
      }
    });
    charts.push(c);
  }

  function renderSnrChart(distribution) {
    const ctx = document.getElementById('obsSnrChart');
    if (!ctx) return;
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: distribution.map(d => d.range),
        datasets: [{
          label: 'Packets',
          data: distribution.map(d => d.count),
          backgroundColor: CHART_COLORS[3] + '80',
          borderColor: CHART_COLORS[3],
          borderWidth: 1,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { title: { display: true, text: 'SNR (dB)' } },
          y: { beginAtZero: true, ticks: { precision: 0 } }
        }
      }
    });
    charts.push(c);
  }

  function renderUptimeChart(samples) {
    const ctx = document.getElementById('obsUptimeChart');
    if (!ctx) return;
    // For each reboot, push a 0 at the same timestamp first so the line drops
    // vertically to the baseline before rising again — clean sharkfin shape.
    const labels = [];
    const data = [];
    samples.forEach(function(s) {
      const lbl = new Date(s.timestamp).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
      if (s.is_reboot_sample) {
        labels.push(lbl);
        data.push(0);
      }
      labels.push(lbl);
      data.push(s.uptime_secs);
    });
    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: labels,
        datasets: [{
          label: 'Uptime',
          data: data,
          borderColor: CHART_COLORS[2],
          backgroundColor: CHART_COLORS[2] + '20',
          fill: true, tension: 0, pointRadius: 0, spanGaps: true,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: {
          legend: { display: false },
          tooltip: { callbacks: { label: function(ctx) { return formatDuration(ctx.raw); } } }
        },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { beginAtZero: true, ticks: { callback: function(v) { return formatDuration(v); } } }
        }
      }
    });
    charts.push(c);
  }

  function renderBatteryChart(samples) {
    const ctx = document.getElementById('obsBatteryChart');
    if (!ctx) return;
    const labels = samples.map(function(s) {
      const d = new Date(s.timestamp);
      return d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    });
    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: labels,
        datasets: [{
          label: 'Battery (mV)',
          data: samples.map(function(s) { return s.battery_mv; }),
          borderColor: CHART_COLORS[3],
          backgroundColor: CHART_COLORS[3] + '20',
          fill: true, tension: 0.3, pointRadius: 2,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { ticks: { callback: function(v) { return v + ' mV'; } } }
        }
      }
    });
    charts.push(c);
  }

  function renderNoiseFloorChart(samples) {
    const ctx = document.getElementById('obsNoiseFloorChart');
    if (!ctx) return;
    const labels = samples.map(function(s) {
      const d = new Date(s.timestamp);
      return d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    });
    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: labels,
        datasets: [{
          label: 'Noise Floor (dBm)',
          data: samples.map(function(s) { return s.noise_floor; }),
          borderColor: CHART_COLORS[4],
          backgroundColor: CHART_COLORS[4] + '20',
          fill: true, tension: 0.3, pointRadius: 2,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { ticks: { callback: function(v) { return v + ' dBm'; } } }
        }
      }
    });
    charts.push(c);
  }

  function renderRssiChart(timeline) {
    const ctx = document.getElementById('obsRssiChart');
    if (!ctx) return;
    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: timeline.map(function(t) { return t.label; }),
        datasets: [{
          label: 'Avg RSSI (dBm)',
          data: timeline.map(function(t) { return t.avg; }),
          borderColor: CHART_COLORS[5],
          backgroundColor: CHART_COLORS[5] + '20',
          fill: true, tension: 0.3, pointRadius: 2,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { ticks: { callback: function(v) { return v + ' dBm'; } } }
        }
      }
    });
    charts.push(c);
  }

  function renderAirtimeChart(samples) {
    const ctx = document.getElementById('obsAirtimeChart');
    if (!ctx) return;
    const labels = samples.map(function(s) {
      return new Date(s.timestamp).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    });
    const txVals = samples.map(function(s) { return s.tx_airtime_pct; });
    const rxVals = samples.map(function(s) { return s.rx_airtime_pct; });
    const txAvg = (function() {
      const v = txVals.filter(function(x) { return x != null; });
      return v.length ? Math.round(v.reduce(function(a, b) { return a + b; }, 0) / v.length * 100) / 100 : null;
    })();
    const rxAvg = (function() {
      const v = rxVals.filter(function(x) { return x != null; });
      return v.length ? Math.round(v.reduce(function(a, b) { return a + b; }, 0) / v.length * 100) / 100 : null;
    })();
    const titleEl = document.getElementById('obsAirtimeTitle');
    if (titleEl) {
      var parts = ['Airtime Utilization (%)'];
      if (txAvg != null) parts.push('TX: ' + txAvg + '% avg');
      if (rxAvg != null) parts.push('RX: ' + rxAvg + '% avg');
      titleEl.textContent = parts.join(' — ');
    }
    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: labels,
        datasets: [
          {
            label: 'TX Airtime %',
            data: txVals,
            borderColor: CHART_COLORS[0],
            backgroundColor: CHART_COLORS[0] + '20',
            fill: false, tension: 0.3, pointRadius: 2, spanGaps: true,
          },
          {
            label: 'RX Airtime %',
            data: rxVals,
            borderColor: CHART_COLORS[1],
            backgroundColor: CHART_COLORS[1] + '20',
            fill: false, tension: 0.3, pointRadius: 2, spanGaps: true,
          },
        ]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: true, position: 'top', labels: { boxWidth: 12 } } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { beginAtZero: true, ticks: { callback: function(v) { return v + '%'; } } }
        }
      }
    });
    charts.push(c);
  }

  function renderRecvErrorsChart(samples) {
    const ctx = document.getElementById('obsRecvErrorsChart');
    if (!ctx) return;
    const labels = samples.map(function(s) {
      return new Date(s.timestamp).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    });
    const c = new Chart(ctx, {
      type: 'line',
      data: {
        labels: labels,
        datasets: [{
          label: 'Recv Errors',
          data: samples.map(function(s) { return s.recv_errors; }),
          borderColor: CHART_COLORS[1],
          backgroundColor: CHART_COLORS[1] + '20',
          fill: true, tension: 0.3, pointRadius: 2, spanGaps: true,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { beginAtZero: true, ticks: { precision: 0 } }
        }
      }
    });
    charts.push(c);
  }

  function renderQueueLenChart(samples) {
    const ctx = document.getElementById('obsQueueLenChart');
    if (!ctx) return;
    const labels = samples.map(function(s) {
      return new Date(s.timestamp).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    });
    const c = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: labels,
        datasets: [{
          label: 'Queue Length',
          data: samples.map(function(s) { return s.queue_len; }),
          backgroundColor: CHART_COLORS[6] + '80',
          borderColor: CHART_COLORS[6],
          borderWidth: 1,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: true,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { maxRotation: 45, autoSkip: true, maxTicksLimit: 12 } },
          y: { beginAtZero: true, ticks: { precision: 0 } }
        }
      }
    });
    charts.push(c);
  }

  function renderRecentPackets(packets) {
    const el = document.getElementById('obsRecentPackets');
    if (!el || !packets.length) { if (el) el.innerHTML = '<div class="text-muted">No recent packets.</div>'; return; }
    el.innerHTML = `<table class="data-table" style="font-size:0.85em">
      <thead><tr><th scope="col">Time</th><th scope="col">Type</th><th scope="col">Hash</th><th scope="col">SNR</th><th scope="col">RSSI</th><th scope="col">Hops</th></tr></thead>
      <tbody>${packets.map(p => {
        const decoded = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : (p.decoded_json || {});
        const hops = typeof p.path_json === 'string' ? JSON.parse(p.path_json) : (p.path_json || []);
        const typeName = PAYLOAD_LABELS[p.payload_type] || 'Type ' + p.payload_type;
        return `<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/packets/${p.hash || p.id}" onclick="location.hash='#/packets/${p.hash || p.id}'">
          <td>${timeAgo(p.timestamp)}</td>
          <td>${typeName}</td>
          <td class="mono" style="font-size:0.85em">${(p.hash || '').substring(0, 10)}</td>
          <td>${p.snr != null ? Number(p.snr).toFixed(1) : '—'}</td>
          <td>${p.rssi != null ? p.rssi : '—'}</td>
          <td>${hops.length}</td>
        </tr>`;
      }).join('')}</tbody>
    </table>`;

    // #209 — Keyboard accessibility for recent packet rows
    el.addEventListener('keydown', function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      location.hash = row.dataset.value;
    });
  }

  registerPage('observer-detail', { init, destroy });
})();
