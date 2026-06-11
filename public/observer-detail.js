/* === CoreScope — observer-detail.js === */
'use strict';

// Issue #1478 — naive-clock banner for observer detail page.
// Exposed as a window global so a jsdom-style test can call it directly.
// Returns the banner HTML when clock_naive is true, or "" when clean.
window.ObserverDetailNaiveBanner = {
  render: function (obs) {
    if (!obs || obs.clock_naive !== true) return '';
    var sec = Number(obs.clock_skew_seconds || 0);
    var absSec = Math.abs(sec);
    var magnitude;
    if (absSec >= 3600) {
      magnitude = (absSec / 3600).toFixed(absSec >= 36000 ? 0 : 1) + 'h';
    } else if (absSec >= 60) {
      magnitude = Math.round(absSec / 60) + 'm';
    } else {
      magnitude = absSec + 's';
    }
    var dir = sec < 0 ? 'behind' : 'ahead of';
    var count = Number(obs.clock_skew_count_24h || 0);
    var lastAt = obs.clock_last_naive_at ? new Date(obs.clock_last_naive_at).toLocaleString() : '';
    // Bright warning card. Plain HTML (no framework). Inline styles so it
    // shows even on pages without the latest CSS bust.
    return '<div class="obs-clock-naive-banner" role="alert" style="'
      + 'background:rgba(255,193,7,0.12);border:1px solid #ffc107;border-left-width:4px;'
      + 'padding:12px 16px;margin-bottom:16px;border-radius:6px;font-size:14px;line-height:1.45">'
      + '<div style="font-weight:600;font-size:15px;margin-bottom:6px">'
      + '\u26A0\uFE0F Naive observer clock — timing is being clamped'
      + '</div>'
      + '<div>This observer is emitting <strong>zone-less local-time</strong> timestamps '
      + 'and its clock is currently <strong>' + magnitude + ' ' + dir + ' UTC</strong>.'
      + (count > 0 ? ' We have recorded <strong>' + count + '</strong> clamp event'
          + (count === 1 ? '' : 's') + ' in the last 24 hours' : '')
      + (lastAt ? ' (most recent: ' + lastAt + ')' : '') + '.'
      + ' Per-packet rxTime for this observer is being collapsed to ingest time, '
      + 'which muddies propagation-delay analytics.'
      + '</div>'
      + '<div style="margin-top:8px"><strong>Fix:</strong> set the observer host clock to <strong>UTC</strong>, '
      + 'OR have the observer script emit <strong>Z-suffixed</strong> '
      + '(<code>datetime.now(timezone.utc).isoformat()</code>) or offset-aware timestamps. '
      + 'The chip and this banner clear automatically once 24 hours pass without a new clamp event.'
      + '</div>'
      + '</div>';
  },
};

(function () {
  const PAYLOAD_LABELS = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
  const CHART_COLORS = ['#4a9eff', '#ff6b6b', '#51cf66', '#fcc419', '#cc5de8', '#20c997', '#ff922b', '#845ef7', '#f06595', '#339af0'];

  let charts = [];
  let currentDays = 7;
  let currentId = null;

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
          <div style="margin-left:auto;display:flex;gap:8px;align-items:center;flex-wrap:wrap">
            <span class="compare-with-group">
              <label class="sr-only" for="obsCompareWithPicker">Compare with another observer</label>
              <select id="obsCompareWithPicker" data-action="compare-with-picker"
                      aria-label="Compare with another observer"
                      title="Pick another observer to compare against"
                      class="time-range-select">
                <option value="">Compare with…</option>
              </select>
              <button type="button" data-action="compare-with-go" class="btn-secondary" disabled aria-disabled="true"
                      title="Open side-by-side comparison">
                <svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"></use></svg><span>Compare</span>
              </button>
            </span>
            <select id="obsDaysSelect" class="time-range-select" aria-label="Time range">
              <option value="1">24 Hours</option>
              <option value="3">3 Days</option>
              <option value="7" selected>7 Days</option>
              <option value="30">30 Days</option>
            </select>
          </div>
        </div>
        <div id="obsDetailContent"><div class="text-center text-muted" style="padding:40px">Loading…</div></div>
      </div>`;

    document.getElementById('obsDaysSelect').addEventListener('change', function (e) {
      currentDays = parseInt(e.target.value);
      loadDetail();
    });

    // #1640 — "Compare with…" picker. Fetches the observer list once,
    // populates options excluding the current observer, enables the
    // Compare button only when a target is selected.
    populateCompareWithPicker(currentId);
    var picker = document.getElementById('obsCompareWithPicker');
    var goBtn = document.querySelector('[data-action="compare-with-go"]');
    if (picker && goBtn) {
      picker.addEventListener('change', function () {
        var enabled = !!picker.value;
        goBtn.disabled = !enabled;
        goBtn.setAttribute('aria-disabled', enabled ? 'false' : 'true');
      });
      goBtn.addEventListener('click', function () {
        if (!picker.value || !currentId) return;
        location.hash = '#/compare?a=' + encodeURIComponent(currentId) +
                        '&b=' + encodeURIComponent(picker.value);
      });
    }

    loadDetail();
  }

  function destroy() {
    destroyCharts();
    currentId = null;
  }

  async function loadDetail() {
    try {
      destroyCharts();
      chartDefaults();
      const [obs, analytics, obsSkewArr] = await Promise.all([
        api('/observers/' + encodeURIComponent(currentId)),
        api('/observers/' + encodeURIComponent(currentId) + '/analytics?days=' + currentDays),
        api('/observers/clock-skew', { ttl: 30000 }).catch(function() { return []; }),
      ]);
      // Find this observer's calibration data.
      var obsSkew = null;
      (Array.isArray(obsSkewArr) ? obsSkewArr : []).forEach(function(s) {
        if (s && s.observerID === currentId) obsSkew = s;
      });
      renderDetail(obs, analytics, obsSkew);
    } catch (e) {
      // SECURITY (OBS-2, PR #1539): use textContent for error messages.
      // Error.message is JS-controlled and shouldn't normally carry attacker
      // strings, but textContent is a one-line defense against an upstream
      // bug that surfaces user-supplied input via thrown errors.
      const errEl = document.getElementById('obsDetailContent');
      if (errEl) {
        errEl.innerHTML = '<div class="text-muted" style="padding:40px"></div>';
        errEl.firstChild.textContent = 'Error: ' + e.message;
      }
    }
  }

  function renderDetail(obs, analytics, obsSkew) {
    const el = document.getElementById('obsDetailContent');
    if (!el) return;

    const title = document.getElementById('obsTitle');
    if (title) title.textContent = obs.name || obs.id.substring(0, 16) + '…';

    // SECURITY (OBS-1, post-#1537 sweep): every MQTT-controlled string from
    // the observer's `status` topic (extractObserverMeta) must be escaped at
    // the render sink. Use the global 5-char OWASP escapeHtml from app.js.
    // Hard-fail if the helper is missing — never identity-passthrough
    // (see #1537: map.js safeEsc identity-fallback bug).
    if (typeof escapeHtml !== 'function') {
      throw new Error('observer-detail.js: global escapeHtml missing — refusing to render unescaped MQTT-controlled input');
    }

    // Parse radio string. obs.radio is observer-published — split parts must
    // be escaped before being injected into HTML.
    let radioHtml = '—';
    if (obs.radio) {
      const rp = String(obs.radio).split(',');
      radioHtml = escapeHtml(rp[0] || '?') + ' MHz · SF' + escapeHtml(rp[2] || '?')
        + ' · BW' + escapeHtml(rp[1] || '?') + ' · CR' + escapeHtml(rp[3] || '?');
    }

    // Health status — Issue #1552: thresholds are operator-configurable via
    // window.HEALTH_THRESHOLDS.observerOnlineMs / observerStaleMs (defaults
    // 60 min / 1440 min (24h), matching node thresholds — #1552).
    const ago = obs.last_seen ? Date.now() - new Date(obs.last_seen).getTime() : Infinity;
    const _obsOnlineMs = (HEALTH_THRESHOLDS && HEALTH_THRESHOLDS.observerOnlineMs) || 3600000;
    const _obsStaleMs = (HEALTH_THRESHOLDS && HEALTH_THRESHOLDS.observerStaleMs) || 86400000;
    const statusCls = ago < _obsOnlineMs ? 'health-green' : ago < _obsStaleMs ? 'health-yellow' : 'health-red';
    const statusLabel = ago < _obsOnlineMs ? 'Online' : ago < _obsStaleMs ? 'Stale' : 'Offline';

    el.innerHTML = `
      ${window.ObserverDetailNaiveBanner.render(obs)}
      <div class="obs-info-grid" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin-bottom:20px">
        <div class="stat-card">
          <div class="stat-label">Status</div>
          <div class="stat-value"><span class="health-dot ${statusCls}"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"></use></svg></span> ${statusLabel}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Relay</div>
          <div class="stat-value">${obs.can_relay === false ? '<span class="badge-listener" title="Firmware reported repeat:off — excluded from path-hop disambiguator (#1290)">listener</span>' : (obs.can_relay === true ? '<span class="badge-repeater" title="Firmware reported repeat:on — eligible as a path hop">repeater</span>' : '<span class="text-muted" title="No repeat field received yet — unknown until firmware publishes a /status">—</span>')}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Region</div>
          <div class="stat-value">${obs.iata ? '<span class="badge-region">' + escapeHtml(obs.iata) + '</span>' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Model</div>
          <div class="stat-value">${escapeHtml(obs.model || '—')}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Firmware</div>
          <div class="stat-value" style="font-size:0.8em;word-break:break-all">${escapeHtml(obs.firmware || '—')}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Client</div>
          <div class="stat-value" style="font-size:0.8em;word-break:break-all">${escapeHtml(obs.client_version || '—')}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Radio</div>
          <div class="stat-value" style="font-size:0.85em">${radioHtml}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Battery</div>
          <!-- SECURITY (OBS-2, PR #1539): Number() coercion is defense-in-depth.
               Backend extractObserverMeta types this *int (cmd/ingestor), so a
               malicious string SHOULD never reach here. If the API contract
               loosens in the future (e.g. interface{}), Number() strips any
               XSS payload to NaN, which renders as '—'. Same for uptime_secs
               and noise_floor below. Do NOT remove without auditing the
               backend type contract. -->
          <div class="stat-value">${Number.isFinite(Number(obs.battery_mv)) && Number(obs.battery_mv) ? Number(obs.battery_mv) + ' mV' : '—'}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Uptime</div>
          <div class="stat-value">${formatDuration(Number.isFinite(Number(obs.uptime_secs)) ? Number(obs.uptime_secs) : 0)}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Noise Floor</div>
          <div class="stat-value">${obs.noise_floor != null && Number.isFinite(Number(obs.noise_floor)) ? Number(obs.noise_floor) + ' dBm' : '—'}</div>
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
      </div>
      <div class="mono" style="font-size:0.75em;color:var(--text-muted);margin-bottom:20px;word-break:break-all">
        ID: ${escapeHtml(obs.id)}
      </div>
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
    if (analytics.recentPackets) {
      renderRecentPackets(analytics.recentPackets);
    }
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

  function renderRecentPackets(packets) {
    const el = document.getElementById('obsRecentPackets');
    if (!el || !packets.length) { if (el) el.innerHTML = '<div class="text-muted">No recent packets.</div>'; return; }
    el.innerHTML = `<table class="data-table" style="font-size:0.85em">
      <thead><tr><th scope="col">Time</th><th scope="col">Type</th><th scope="col">Hash</th><th scope="col">SNR</th><th scope="col">RSSI</th><th scope="col">Hops</th></tr></thead>
      <tbody>${packets.map(p => {
        const decoded = typeof p.decoded_json === 'string' ? JSON.parse(p.decoded_json) : (p.decoded_json || {});
        const hops = typeof p.path_json === 'string' ? JSON.parse(p.path_json) : (p.path_json || []);
        const typeName = PAYLOAD_LABELS[p.payload_type] || 'Type ' + p.payload_type;
        return `<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/packets/${p.hash || p.id}">
          <td>${timeAgo(p.timestamp)}</td>
          <td>${typeName}</td>
          <td class="mono" style="font-size:0.85em">${(p.hash || '').substring(0, 10)}</td>
          <td>${p.snr != null ? Number(p.snr).toFixed(1) : '—'}</td>
          <td>${p.rssi != null ? p.rssi : '—'}</td>
          <td>${hops.length}</td>
        </tr>`;
      }).join('')}</tbody>
    </table>`;

    // SECURITY (PR #1539, djb finding): inline onclick= is a CSP blocker and
    // an XSS-amplification path if data ever sneaks in. Replaced with a
    // single delegated click listener that reads data-value. The keydown
    // listener below already followed this pattern for #209.
    el.addEventListener('click', function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      location.hash = row.dataset.value;
    });

    // #209 — Keyboard accessibility for recent packet rows
    el.addEventListener('keydown', function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      location.hash = row.dataset.value;
    });
  }

  // #1640 — populate the "Compare with…" dropdown with all other observers.
  // Uses the same /observers list endpoint the observers page already caches,
  // so this should hit the in-memory cache in the common case.
  async function populateCompareWithPicker(thisId) {
    var picker = document.getElementById('obsCompareWithPicker');
    if (!picker) return;
    try {
      var data = await api('/observers', { ttl: (window.CLIENT_TTL && window.CLIENT_TTL.observers) || 120000 });
      var list = (data && data.observers ? data.observers : [])
        .filter(function (o) { return String(o.id) !== String(thisId); })
        .sort(function (a, b) { return (a.name || a.id).localeCompare(b.name || b.id); });
      var opts = ['<option value="">Compare with\u2026</option>'];
      for (var i = 0; i < list.length; i++) {
        var o = list[i];
        var label = (o.name || o.id) + (o.iata ? ' (' + o.iata + ')' : '');
        opts.push('<option value="' + escapeHtml(o.id) + '">' + escapeHtml(label) + '</option>');
      }
      picker.innerHTML = opts.join('');
    } catch (e) {
      // Leave the placeholder option in place; user can still navigate via
      // the observers page Compare button.
    }
  }

  registerPage('observer-detail', { init, destroy });
})();
