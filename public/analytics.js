/* === CoreScope — analytics.js (v2 — full nerd mode) === */
'use strict';

(function () {
  let _analyticsData = {};
  const sf = (v, d) => (v != null ? v.toFixed(d) : '–'); // safe toFixed
  function esc(s) { return s ? String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;') : ''; }

  // #1085 — Roles tab helpers (hoisted from renderRolesTab so they're not
  // re-allocated per render).
  function _rolesEmoji(role) {
    if (window.ROLE_EMOJI && window.ROLE_EMOJI[role]) return window.ROLE_EMOJI[role];
    return '•';
  }
  function _rolesFmtSec(v) {
    if (!v && v !== 0) return '—';
    var abs = Math.abs(v);
    if (abs < 1) return v.toFixed(2) + 's';
    if (abs < 60) return v.toFixed(1) + 's';
    if (abs < 3600) return (v / 60).toFixed(1) + 'm';
    if (abs < 86400) return (v / 3600).toFixed(1) + 'h';
    return (v / 86400).toFixed(1) + 'd';
  }
  // #1085 — auto-refresh timer for the Roles tab. Started when the Roles
  // tab is rendered, cleared on tab switch and destroy.
  var _rolesRefreshTimer = null;
  function _stopRolesRefresh() {
    if (_rolesRefreshTimer) { clearInterval(_rolesRefreshTimer); _rolesRefreshTimer = null; }
  }
  var _scopesRefreshTimer = null;
  function _stopScopesRefresh() {
    if (_scopesRefreshTimer) { clearInterval(_scopesRefreshTimer); _scopesRefreshTimer = null; }
  }
  var _foreignTrafficRefreshTimer = null;
  function _stopForeignTrafficRefresh() {
    if (_foreignTrafficRefreshTimer) { clearInterval(_foreignTrafficRefreshTimer); _foreignTrafficRefreshTimer = null; }
  }
  var _wardrivingRefreshTimer = null;
  function _stopWardrivingRefresh() {
    if (_wardrivingRefreshTimer) { clearInterval(_wardrivingRefreshTimer); _wardrivingRefreshTimer = null; }
  }

  // --- Status color helpers (read from CSS variables for theme support) ---
  function cssVar(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
  function statusGreen() { return cssVar('--status-green') || '#22c55e'; }
  function statusYellow() { return cssVar('--status-yellow') || '#eab308'; }
  function statusRed() { return cssVar('--status-red') || '#ef4444'; }
  function accentColor() { return cssVar('--accent') || '#4a9eff'; }
  function snrColor(snr) { return snr > 6 ? statusGreen() : snr > 0 ? statusYellow() : statusRed(); }

  // --- SVG helpers ---
  function sparkSvg(data, color, w = 120, h = 32) {
    if (!data.length) return '';
    const max = Math.max(...data, 1);
    const pts = data.map((v, i) => {
      const x = i * (w / Math.max(data.length - 1, 1));
      const y = h - 2 - (v / max) * (h - 4);
      return `${x},${y}`;
    }).join(' ');
    return `<svg viewBox="0 0 ${w} ${h}" style="width:${w}px;height:${h}px" role="img" aria-label="Sparkline showing trend of ${data.length} data points"><title>Sparkline showing trend of ${data.length} data points</title><polyline points="${pts}" fill="none" stroke="${color}" stroke-width="1.5"/></svg>`;
  }

  function barChart(data, labels, colors, w = 800, h = 220, pad = 40) {
    const max = Math.max(...data, 1);
    const barW = Math.max(1, Math.min((w - pad * 2) / data.length - 2, 30));
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Bar chart showing data distribution"><title>Bar chart showing data distribution</title>`;
    // Grid
    for (let i = 0; i <= 4; i++) {
      const y = pad + (h - pad * 2) * i / 4;
      const val = Math.round(max * (4 - i) / 4);
      svg += `<line x1="${pad}" y1="${y}" x2="${w-pad}" y2="${y}" stroke="var(--border)" stroke-dasharray="2"/>`;
      svg += `<text x="${pad-4}" y="${y+4}" text-anchor="end" font-size="10" fill="var(--text-muted)">${val}</text>`;
    }
    data.forEach((v, i) => {
      const x = pad + i * ((w - pad * 2) / data.length) + barW / 2;
      const bh = (v / max) * (h - pad * 2);
      const y = h - pad - bh;
      const c = typeof colors === 'string' ? colors : colors[i % colors.length];
      svg += `<rect x="${x}" y="${y}" width="${barW}" height="${bh}" fill="${c}" rx="2"/>`;
      if (labels[i]) svg += `<text x="${x + barW/2}" y="${h - pad + 14}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${labels[i]}</text>`;
    });
    svg += '</svg>';
    return svg;
  }

  function histogram(data, bins, color, w = 800, h = 180) {
    // Support pre-computed histogram from server { bins: [{x, w, count}], min, max }
    if (data && data.bins && Array.isArray(data.bins)) {
      const buckets = data.bins.map(b => b.count);
      const labels = data.bins.map(b => b.x.toFixed(1));
      return { svg: barChart(buckets, labels, color, w, h), buckets, labels };
    }
    // Legacy: raw values array
    const values = data;
    const min = Math.min(...values), max = Math.max(...values);
    const step = (max - min) / bins;
    const buckets = Array(bins).fill(0);
    const labels = [];
    for (let i = 0; i < bins; i++) labels.push((min + step * i).toFixed(1));
    values.forEach(v => { const b = Math.min(Math.floor((v - min) / step), bins - 1); buckets[b]++; });
    return { svg: barChart(buckets, labels, color, w, h), buckets, labels };
  }

  // --- Main ---
  async function init(app) {
    app.innerHTML = `
      <div class="analytics-page">
        <div class="analytics-header">
          <h2><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Mesh Analytics</h2>
          <p class="text-muted">Deep dive into your mesh network data</p>
          <div class="analytics-filters">
            <div id="analyticsRegionFilter" class="region-filter-container"></div>
            <div id="analyticsAreaFilter" style="display:none"></div>
            <select id="analyticsTimeWindow" class="analytics-time-window-select" data-testid="analytics-time-window" aria-label="Time window">
              <option value="">All data</option>
              <option value="1h">Last 1 hour</option>
              <option value="24h">Last 24 hours</option>
              <option value="7d">Last 7 days</option>
              <option value="30d">Last 30 days</option>
            </select>
          </div>
          <div class="analytics-tabs" id="analyticsTabs" role="tablist" aria-label="Analytics tabs">
            <button class="tab-btn active" data-tab="overview">Overview</button>
            <button class="tab-btn" data-tab="rf">RF / Signal</button>
            <button class="tab-btn" data-tab="topology">Topology</button>
            <button class="tab-btn" data-tab="channels">Channels</button>
            <button class="tab-btn" data-tab="hashsizes">Hash Stats</button>
            <button class="tab-btn" data-tab="collisions">Hash Issues</button>
            <button class="tab-btn" data-tab="subpaths">Route Patterns</button>
            <button class="tab-btn" data-tab="nodes">Nodes</button>
            <button class="tab-btn" data-tab="my-repeaters">My Repeaters</button>
            <button class="tab-btn" data-tab="distance">Distance</button>
            <button class="tab-btn" data-tab="neighbor-graph">Neighbor Graph</button>
            <button class="tab-btn" data-tab="rf-health">RF Health</button>
            <button class="tab-btn" data-tab="clock-health">Clock Health</button>
            <!-- #1085 — Roles tab folded in from former /#/roles standalone page.
                 Placed after Clock Health (clock-skew posture is shown per-role
                 inside this tab) and before Prefix Tool (utility tabs trail). -->
            <button class="tab-btn" data-tab="roles">Roles</button>
            <button class="tab-btn" data-tab="scopes">Scopes</button>
            <button class="tab-btn" data-tab="foreign-traffic">Foreign Traffic</button>
            <button class="tab-btn" data-tab="wardriving">Wardriving</button>
            <button class="tab-btn" data-tab="prefix-tool">Prefix Tool</button>
          </div>
        </div>
        <div id="analyticsContent" class="analytics-content" aria-live="polite">
          <div class="text-center text-muted" style="padding:40px">Loading analytics…</div>
        </div>
      </div>`;

    // Tabs where the area filter is meaningful (transmitter GPS attribution)
    const AREA_FILTER_TABS = new Set(['overview', 'rf', 'topology', 'hashsizes', 'collisions', 'nodes', 'my-repeaters', 'clock-health']);

    function setAreaFilterVisibility(tab) {
      const el = document.getElementById('analyticsAreaFilter');
      if (el) el.style.display = AREA_FILTER_TABS.has(tab) ? '' : 'none';
    }

    // Tab handling
    const analyticsTabs = document.getElementById('analyticsTabs');
    initTabBar(analyticsTabs);
    // #749 — keep analytics tab + window in URL for deep-linking.
    function _updateAnalyticsUrl() {
      if (!window.URLState) return;
      var twElNow = document.getElementById('analyticsTimeWindow');
      var updates = {
        tab: _currentTab && _currentTab !== 'overview' ? _currentTab : '',
        window: twElNow && twElNow.value ? twElNow.value : ''
      };
      // Drop any subview-specific keys that don't belong to the active tab
      // so switching tabs gives a clean URL. (rf-health uses 'range', 'observer', 'from', 'to')
      if (_currentTab !== 'rf-health') {
        var cleared = ['range', 'observer', 'from', 'to'];
        for (var i = 0; i < cleared.length; i++) updates[cleared[i]] = '';
      }
      var newHash = URLState.updateHashParams(updates, location.hash);
      if (newHash !== location.hash) history.replaceState(null, '', newHash);
    }

    analyticsTabs.addEventListener('click', e => {
      const btn = e.target.closest('.tab-btn');
      if (!btn) return;
      document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      _currentTab = btn.dataset.tab;
      setAreaFilterVisibility(_currentTab);
      // #1085 — Roles tab owns its own 60s auto-refresh; stop it on switch.
      if (_currentTab !== 'roles') _stopRolesRefresh();
      if (_currentTab !== 'scopes') _stopScopesRefresh();
      if (_currentTab !== 'foreign-traffic') _stopForeignTrafficRefresh();
      if (_currentTab !== 'wardriving') _stopWardrivingRefresh();
      _updateAnalyticsUrl();
      renderTab(_currentTab);
    });

    // Deep-link: #/analytics?tab=collisions&window=7d
    const hashParams = location.hash.split('?')[1] || '';
    const _ap = new URLSearchParams(hashParams);
    const urlTab = _ap.get('tab');
    if (urlTab) {
      const tabBtn = analyticsTabs.querySelector(`[data-tab="${urlTab}"]`);
      if (tabBtn) {
        analyticsTabs.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
        tabBtn.classList.add('active');
        _currentTab = urlTab;
      }
    }
    // #749 — restore time window from URL.
    const urlWindow = _ap.get('window');
    if (urlWindow) {
      const twInit = document.getElementById('analyticsTimeWindow');
      if (twInit) twInit.value = urlWindow;
    }

    RegionFilter.init(document.getElementById('analyticsRegionFilter'));
    AreaFilter.init(document.getElementById('analyticsAreaFilter'));
    setAreaFilterVisibility(_currentTab);
    RegionFilter.onChange(function () { loadAnalytics(); });
    AreaFilter.onChange(function () { loadAnalytics(); });

    // Time-window picker (#842) — refresh analytics on change.
    const tw = document.getElementById('analyticsTimeWindow');
    if (tw) {
      tw.addEventListener('change', function () { _updateAnalyticsUrl(); loadAnalytics(); });
    }

    // Delegated click/keyboard handler for clickable table rows
    const analyticsContent = document.getElementById('analyticsContent');
    if (analyticsContent) {
      const handler = (e) => {
        const row = e.target.closest('tr[data-action="navigate"]');
        if (!row) return;
        if (e.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return;
        if (e.type === 'keydown') e.preventDefault();
        location.hash = row.dataset.value;
      };
      analyticsContent.addEventListener('click', handler);
      analyticsContent.addEventListener('keydown', handler);
    }

    // Re-render when distance unit or theme changes
    _themeRefreshHandler = function () { renderTab(_currentTab); };
    window.addEventListener('theme-refresh', _themeRefreshHandler);

    loadAnalytics();
  }

  var _themeRefreshHandler = null;
  let _currentTab = 'overview';

  async function loadAnalytics() {
    try {
      _analyticsData = {};
      const rqs = RegionFilter.regionQueryString(); // "&region=..." or ""
      const aqs = AreaFilter.areaQueryString();     // "&area=..." or ""
      // Time window picker (#842) — append &window=… when set.
      // NOTE: only the three window-aware endpoints (rf/topology/channels)
      // receive ?window=…; hash-sizes and hash-collisions are about node
      // identity / hash-byte distribution and intentionally span all data.
      const twEl = document.getElementById('analyticsTimeWindow');
      const twVal = twEl ? twEl.value : '';
      const tws = twVal ? '&window=' + encodeURIComponent(twVal) : '';
      // hash-sizes / hash-collisions: region + area, no window
      const baseQS = (rqs + aqs).slice(1);
      const sepBase = baseQS ? '?' + baseQS : '';
      // rf / topology: region + area + window
      const windowedQS = (rqs + aqs + tws).slice(1);
      const sepWin = windowedQS ? '?' + windowedQS : '';
      // channels: region + window (no area per original PR intent)
      const chanQS = (rqs + tws).slice(1);
      const sepChan = chanQS ? '?' + chanQS : '';
      const [hashData, rfData, topoData, chanData, collisionData, airtimeData] = await Promise.all([
        api('/analytics/hash-sizes' + sepBase, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/rf' + sepWin, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/topology' + sepWin, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/channels' + sepChan, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/hash-collisions' + sepBase, { ttl: CLIENT_TTL.analyticsRF }),
        api('/analytics/relay-airtime-share' + sepWin, { ttl: CLIENT_TTL.analyticsRF }).catch(() => ({ rows: [] })),
      ]);
      _analyticsData = { hashData, rfData, topoData, chanData, collisionData, airtimeData };
      renderTab(_currentTab);
    } catch (e) {
      document.getElementById('analyticsContent').innerHTML =
        `<div class="text-muted" role="alert" aria-live="polite" style="padding:40px">Failed to load: ${e.message}</div>`;
    }
  }

  async function renderTab(tab) {
    const el = document.getElementById('analyticsContent');
    const d = _analyticsData;
    switch (tab) {
      case 'overview': renderOverview(el, d); break;
      case 'rf': renderRF(el, d.rfData); break;
      case 'topology': renderTopology(el, d.topoData); break;
      case 'channels': renderChannels(el, d.chanData); break;
      case 'hashsizes': renderHashSizes(el, d.hashData); break;
      case 'collisions': await renderCollisionTab(el, d.hashData, d.collisionData); break;
      case 'subpaths': await renderSubpaths(el); break;
      case 'nodes': await renderNodesTab(el); break;
      case 'my-repeaters': await renderMyRepeatersTab(el); break;
      case 'distance': await renderDistanceTab(el); break;
      case 'neighbor-graph': await renderNeighborGraphTab(el); break;
      case 'rf-health': await renderRFHealthTab(el); break;
      case 'clock-health': await renderClockHealthTab(el); break;
      case 'roles': await renderRolesTab(el); break;
      case 'prefix-tool': await renderPrefixTool(el); break;
      case 'scopes': await renderScopesTab(el); break;
      case 'foreign-traffic': await renderForeignTrafficTab(el); break;
      case 'wardriving': await renderWardrivingTab(el); break;
    }
    // Auto-apply column resizing to all analytics tables
    requestAnimationFrame(() => {
      el.querySelectorAll('.analytics-table').forEach((tbl, i) => {
        tbl.id = tbl.id || `analytics-tbl-${tab}-${i}`;
        if (typeof makeColumnsResizable === 'function') makeColumnsResizable('#' + tbl.id, `meshcore-analytics-${tab}-${i}-col-widths`);
      });
      // #206 — Wrap analytics tables in scroll containers on mobile
      el.querySelectorAll('.analytics-table').forEach(tbl => {
        if (!tbl.parentElement.classList.contains('analytics-table-scroll')) {
          const wrapper = document.createElement('div');
          wrapper.className = 'analytics-table-scroll';
          tbl.parentElement.insertBefore(wrapper, tbl);
          wrapper.appendChild(tbl);
        }
      });
    });
    // Deep-link scroll to section within tab
    const sectionId = new URLSearchParams((location.hash.split('?')[1] || '')).get('section');
    if (sectionId) {
      setTimeout(() => {
        const target = document.getElementById(sectionId);
        if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }, 400);
    }
  }

  // ===================== OVERVIEW =====================
  function renderOverview(el, d) {
    const rf = d.rfData, topo = d.topoData, ch = d.chanData, hs = d.hashData;
    el.innerHTML = `
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-value">${(rf.totalTransmissions || rf.totalAllPackets || rf.totalPackets).toLocaleString()}</div>
          <div class="stat-label">Total Transmissions</div>
          <div class="stat-spark">${sparkSvg(rf.packetsPerHour.map(h=>h.count), 'var(--accent)')}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${rf.totalPackets.toLocaleString()}</div>
          <div class="stat-label">Observations with Signal</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${topo.uniqueNodes}</div>
          <div class="stat-label">Unique Nodes</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${sf(rf.snr.avg, 1)} dB</div>
          <div class="stat-label">Avg SNR</div>
          <div class="stat-detail">${sf(rf.snr.min, 1)} to ${sf(rf.snr.max, 1)}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${sf(rf.rssi.avg, 0)} dBm</div>
          <div class="stat-label">Avg RSSI</div>
          <div class="stat-detail">${rf.rssi.min} to ${rf.rssi.max}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${sf(topo.avgHops, 1)}</div>
          <div class="stat-label">Avg Hops</div>
          <div class="stat-detail">max ${topo.maxHops}</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${ch.activeChannels}</div>
          <div class="stat-label">Active Channels</div>
          <div class="stat-detail">${ch.decryptable} decryptable</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${rf.avgPacketSize} B</div>
          <div class="stat-label">Avg Packet Size</div>
          <div class="stat-detail">${rf.minPacketSize}–${rf.maxPacketSize} B</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${((rf.timeSpanHours || 1)).toFixed(1)}h</div>
          <div class="stat-label">Data Span</div>
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trend-up"/></svg> Packets / Hour</h3>
          ${(() => {
            const pph = rf.packetsPerHour;
            const counts = pph.map(h => h.count);
            // Decimate x-axis labels to avoid overlap
            const totalHours = pph.length;
            // Pick label interval: <=24h show every 6h, <=72h every 12h, else every 24h
            const labelInterval = totalHours <= 24 ? 6 : totalHours <= 72 ? 12 : 24;
            const labels = pph.map((h, i) => {
              const hh = h.hour.slice(11, 13); // "HH"
              const hourNum = parseInt(hh, 10);
              if (hourNum % labelInterval === 0) {
                // For multi-day ranges, show date on 00h boundaries
                if (totalHours > 48 && hourNum === 0) return h.hour.slice(5, 10);
                return hh + 'h';
              }
              return ''; // skip label
            });
            return barChart(counts, labels, 'var(--accent)');
          })()}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-package"/></svg> Payload Type Mix</h3>
          ${renderPayloadPie(rf.payloadTypes)}
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-link"/></svg> Hop Count Distribution</h3>
          ${barChart(topo.hopDistribution.map(h=>h.count), topo.hopDistribution.map(h=>h.hops), ['#3b82f6'])}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> Relay Airtime Share</h3>
          <p class="text-muted" style="margin-top:-4px;font-size:12px">Score = LoRa Time-on-Air × distinct repeaters that forwarded the packet (issue #1768). Counts relay re-transmissions; originator TX excluded. Not comparable across meshes — see preset caption below.</p>
          ${renderRelayAirtimeDumbbell(d.airtimeData)}
        </div>
      </div>
    `;

    // Affinity stats widget — fetch and append if debugAffinity enabled
    var showDebug = (window.CLIENT_CONFIG && window.CLIENT_CONFIG.debugAffinity) || localStorage.getItem('meshcore-affinity-debug') === 'true';
    if (showDebug) {
      var apiKey = localStorage.getItem('meshcore-api-key') || '';
      fetch('/api/debug/affinity', { headers: { 'X-API-Key': apiKey } })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (data) {
          if (!data || !data.stats) return;
          var s = data.stats;
          var total = s.resolvedCount + s.ambiguousCount + s.unresolvedCount;
          var resolvedPct = total > 0 ? (s.resolvedCount / total * 100).toFixed(1) : '0.0';
          var ambiguousPct = total > 0 ? (s.ambiguousCount / total * 100).toFixed(1) : '0.0';
          var widget = document.createElement('div');
          widget.className = 'analytics-row';
          widget.innerHTML = '<div class="analytics-card flex-1">' +
            '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Neighbor Affinity Graph</h3>' +
            '<div class="stats-grid">' +
            '<div class="stat-card"><div class="stat-value">' + s.totalEdges + '</div><div class="stat-label">Total Edges</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + s.totalNodes + '</div><div class="stat-label">Total Nodes</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + s.resolvedCount + ' <span style="font-size:12px;color:var(--text-muted)">(' + resolvedPct + '%)</span></div><div class="stat-label">Resolved Prefixes</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + s.ambiguousCount + ' <span style="font-size:12px;color:var(--text-muted)">(' + ambiguousPct + '%)</span></div><div class="stat-label">Ambiguous Prefixes</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (s.avgConfidence || 0).toFixed(3) + '</div><div class="stat-label">Avg Confidence</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (s.coldStartCoverage || 0).toFixed(1) + '%</div><div class="stat-label">Cold-Start Coverage</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (s.cacheAge || 'N/A') + '</div><div class="stat-label">Cache Age</div></div>' +
            '<div class="stat-card"><div class="stat-value">' + (s.lastRebuild ? s.lastRebuild.substring(0, 19) : 'N/A') + '</div><div class="stat-label">Last Rebuild</div></div>' +
            '</div></div>';
          el.appendChild(widget);
        })
        .catch(function () {});
    }
  }

  function renderPayloadPie(types) {
    const total = types.reduce((s, t) => s + t.count, 0);
    const colors = ['#ef4444','#f59e0b','#22c55e','#3b82f6','#8b5cf6','#ec4899','#14b8a6','#64748b','#f97316','#06b6d4','#84cc16'];
    let html = '<div class="payload-bars">';
    types.forEach((t, i) => {
      const pct = (t.count / total * 100).toFixed(1);
      const w = Math.max(t.count / total * 100, 1);
      html += `<div class="payload-bar-row">
        <div class="payload-bar-label"><span class="legend-dot" style="background:${colors[i]}"></span>${escapeHtml(t.name)}</div>
        <div class="hash-bar-track"><div class="hash-bar-fill" style="width:${w}%;background:${colors[i]}"></div></div>
        <div class="payload-bar-value">${t.count} <span class="text-muted">(${pct}%)</span></div>
      </div>`;
    });
    return html + '</div>';
  }

  // === Relay Airtime Share — dumbbell chart (#1359) ===
  // Two dots per payload_type row: gray = count %, colored = airtime %.
  // Connector line between them = the divergence. Shared 0–100% axis.
  // Sorted desc by airtime (server-side) so count dots visibly scatter
  // out of rank order — that visible disorder IS the headline.
  function renderRelayAirtimeDumbbell(data) {
    var rows = (data && Array.isArray(data.rows)) ? data.rows : [];
    if (!rows.length) {
      return '<div class="text-muted" style="padding:20px">No relay-airtime data in this window.</div>';
    }
    var totalScore = (data && typeof data.total_score === 'number') ? data.total_score : 0;
    if (totalScore <= 0) {
      return '<div class="text-muted" style="padding:20px">No relay activity observed in this window (all packets direct).</div>';
    }
    // Issue #1768 — surface the LoRa preset baked into the ToA score. Share
    // numbers are only meaningful relative to one PHY preset; operators must
    // know what was assumed. All fields are run through esc() defensively
    // (preset.sf/cr/preamble/bw_khz/freq_hz arrive from the server JSON,
    // which the operator can configure — never inject untrusted text raw).
    // BW formatted consistent with home.js / customize-v2.js presets
    // (e.g. `62.5 kHz`, `125 kHz`) — strip trailing `.0` for integer kHz.
    var preset = data && data.preset;
    var presetCaption = '';
    if (preset && typeof preset === 'object') {
      var freqMHz = Number(preset.freq_hz || 0) / 1e6;
      var bwKhz = Number(preset.bw_khz || 0);
      var bwStr = bwKhz ? bwKhz.toFixed(1).replace(/\.0$/, '') : '0';
      presetCaption =
        '<div class="dumbbell-preset text-muted" style="font-size:11px;padding:0 4px 6px 4px">' +
        'Assumed LoRa preset: ' +
        (freqMHz ? esc(freqMHz.toFixed(3)) + ' MHz / ' : '') +
        'BW ' + esc(bwStr) + ' kHz / ' +
        'SF ' + esc(String(Number(preset.sf || 0))) + ' / ' +
        'CR 4/' + esc(String(Number(preset.cr || 0))) +
        ' (preamble ' + esc(String(Number(preset.preamble || 0))) + ' sym)' +
        '</div>';
    }
    // Layout: per row → label | track 0..100% | values
    var palette = ['#ef4444','#f59e0b','#22c55e','#3b82f6','#8b5cf6','#ec4899','#14b8a6','#64748b','#f97316','#06b6d4','#84cc16'];
    var html = '<div class="dumbbell-chart" style="display:flex;flex-direction:column;gap:8px;padding:8px 4px">';
    if (presetCaption) html += presetCaption;
    rows.forEach(function (r, i) {
      var name = r.payload_type || 'UNK';
      var cnt = Number(r.count || 0);
      var cpct = Number(r.count_pct || 0);
      var apct = Number(r.airtime_pct || 0);
      var score = Number(r.score || 0);
      var color = palette[i % palette.length];
      var loPct = Math.min(cpct, apct);
      var hiPct = Math.max(cpct, apct);
      // Tooltip per row — payload_type, count %, count N, airtime %, raw score, caveat.
      // Score is summed (ToA in ns) × distinct repeaters across packets of
      // this type; converted to ms for human-readable display per #1768
      // round-1 review.
      var scoreMs = score / 1e6;
      var scoreStr = scoreMs >= 1000
        ? (scoreMs / 1000).toFixed(2) + ' s'
        : scoreMs.toFixed(2) + ' ms';
      var tip =
        name + '\n' +
        'Count: ' + cnt.toLocaleString() + ' (' + cpct.toFixed(2) + '%)\n' +
        'Airtime: ' + apct.toFixed(2) + '% (score ' + scoreStr + ' · airtime × repeaters)\n' +
        'Score = LoRa Time-on-Air × distinct repeaters. Within-mesh only.';
      html += '<div class="dumbbell-row" title="' + esc(tip) + '" style="display:grid;grid-template-columns:80px 1fr 180px;align-items:center;gap:10px;font-size:12px">' +
        '<div class="dumbbell-label" style="font-weight:600;color:var(--text)">' + esc(name) + '</div>' +
        '<div class="dumbbell-track" style="position:relative;height:18px;background:var(--bg-elev,rgba(127,127,127,0.12));border-radius:9px">' +
          '<div class="dumbbell-connector" style="position:absolute;top:50%;left:' + loPct.toFixed(3) + '%;width:' + (hiPct - loPct).toFixed(3) + '%;height:2px;background:var(--text-muted,#888);transform:translateY(-50%);opacity:0.5"></div>' +
          '<div class="dumbbell-dot dumbbell-dot-count" style="position:absolute;top:50%;left:' + cpct.toFixed(3) + '%;width:10px;height:10px;border-radius:50%;background:var(--text-muted,#888);transform:translate(-50%,-50%);border:1px solid var(--bg,#fff)" aria-label="count share"></div>' +
          '<div class="dumbbell-dot dumbbell-dot-airtime" style="position:absolute;top:50%;left:' + apct.toFixed(3) + '%;width:12px;height:12px;border-radius:50%;background:' + color + ';transform:translate(-50%,-50%);border:1px solid var(--bg,#fff)" aria-label="airtime share"></div>' +
        '</div>' +
        '<div class="dumbbell-values" style="text-align:right;color:var(--text-muted);font-variant-numeric:tabular-nums">' +
          '<span style="color:var(--text-muted)">cnt ' + cpct.toFixed(1) + '%</span>' +
          ' &nbsp;·&nbsp; ' +
          '<span style="display:inline-flex;align-items:center;gap:4px;color:var(--text);font-weight:600"><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:' + color + '" aria-hidden="true"></span>air ' + apct.toFixed(1) + '%</span>' +
        '</div>' +
      '</div>';
    });
    // Axis legend.
    html += '<div class="dumbbell-axis" style="display:grid;grid-template-columns:80px 1fr 180px;gap:10px;color:var(--text-muted);font-size:11px;margin-top:4px">' +
      '<div></div>' +
      '<div style="display:flex;justify-content:space-between"><span>0%</span><span>50%</span><span>100%</span></div>' +
      '<div></div>' +
    '</div>';
    html += '<div class="dumbbell-legend" style="display:flex;gap:14px;font-size:11px;color:var(--text-muted);padding:4px 0 0 84px"><span><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--text-muted,#888);vertical-align:middle"></span> count %</span><span><span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:var(--accent,#3b82f6);vertical-align:middle"></span> airtime %</span></div>';
    html += '</div>';
    return html;
  }

  // ===================== RF / SIGNAL =====================
  function renderRF(el, rf) {
    const snrHist = histogram(rf.snrValues, 20, statusGreen());
    const rssiHist = histogram(rf.rssiValues, 20, accentColor());

    el.innerHTML = `
      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-cell-signal-high"/></svg> SNR Distribution</h3>
          <p class="text-muted">Signal-to-Noise Ratio (higher = cleaner signal)</p>
          ${snrHist.svg}
          <div class="rf-stats">
            <span>Min: <strong>${sf(rf.snr.min, 1)} dB</strong></span>
            <span>Mean: <strong>${sf(rf.snr.avg, 1)} dB</strong></span>
            <span>Median: <strong>${sf(rf.snr.median, 1)} dB</strong></span>
            <span>Max: <strong>${sf(rf.snr.max, 1)} dB</strong></span>
            <span>σ: <strong>${sf(rf.snr.stddev, 1)} dB</strong></span>
          </div>
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> RSSI Distribution</h3>
          <p class="text-muted">Received Signal Strength (closer to 0 = stronger)</p>
          ${rssiHist.svg}
          <div class="rf-stats">
            <span>Min: <strong>${rf.rssi.min} dBm</strong></span>
            <span>Mean: <strong>${sf(rf.rssi.avg, 0)} dBm</strong></span>
            <span>Median: <strong>${rf.rssi.median} dBm</strong></span>
            <span>Max: <strong>${rf.rssi.max} dBm</strong></span>
            <span>σ: <strong>${sf(rf.rssi.stddev, 1)} dBm</strong></span>
          </div>
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-target"/></svg> SNR vs RSSI Scatter</h3>
          <p class="text-muted">Each dot = one packet. Cluster position reveals link quality.</p>
          ${renderScatter(rf.scatterData)}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> SNR by Payload Type</h3>
          ${renderSNRByType(rf.snrByType)}
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trend-up"/></svg> Signal Quality Over Time</h3>
          ${renderSignalTimeline(rf.signalOverTime)}
        </div>
      </div>

      <div class="analytics-card">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-ruler"/></svg> Packet Size Distribution</h3>
        <p class="text-muted">Raw packet length in bytes</p>
        ${histogram(rf.packetSizes, 25, '#8b5cf6').svg}
        <div class="rf-stats">
          <span>Min: <strong>${rf.minPacketSize} B</strong></span>
          <span>Avg: <strong>${rf.avgPacketSize} B</strong></span>
          <span>Max: <strong>${rf.maxPacketSize} B</strong></span>
        </div>
      </div>
    `;
  }

  function renderScatter(data) {
    const w = 600, h = 300, pad = 40;
    const snrMin = -12, snrMax = 15, rssiMin = -130, rssiMax = -5;
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:300px" role="img" aria-label="SNR vs RSSI scatter plot showing signal quality distribution"><title>SNR vs RSSI scatter plot showing signal quality distribution</title>`;
    // Axes
    svg += `<line x1="${pad}" y1="${h-pad}" x2="${w-pad}" y2="${h-pad}" stroke="var(--text-muted)" stroke-width="0.5"/>`;
    svg += `<line x1="${pad}" y1="${pad}" x2="${pad}" y2="${h-pad}" stroke="var(--text-muted)" stroke-width="0.5"/>`;
    svg += `<text x="${w/2}" y="${h-5}" text-anchor="middle" font-size="11" fill="var(--text-muted)">SNR (dB)</text>`;
    svg += `<text x="12" y="${h/2}" text-anchor="middle" font-size="11" fill="var(--text-muted)" transform="rotate(-90,12,${h/2})">RSSI (dBm)</text>`;
    // Grid labels
    for (let snr = -10; snr <= 14; snr += 4) {
      const x = pad + (snr - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      svg += `<text x="${x}" y="${h-pad+14}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${snr}</text>`;
    }
    for (let rssi = -120; rssi <= -20; rssi += 20) {
      const y = h - pad - (rssi - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      svg += `<text x="${pad-4}" y="${y+3}" text-anchor="end" font-size="9" fill="var(--text-muted)">${rssi}</text>`;
    }
    // Quality zones
    const _sg = statusGreen(), _sy = statusYellow(), _sr = statusRed();
    const zones = [
      { label: 'Excellent', snr: [6, 15], rssi: [-80, -5], color: _sg + '20' },
      { label: 'Good', snr: [0, 6], rssi: [-100, -80], color: _sy + '15' },
      { label: 'Weak', snr: [-12, 0], rssi: [-130, -100], color: _sr + '10' },
    ];
    // Define patterns for color-blind accessibility
    svg += `<defs>`;
    svg += `<pattern id="pat-excellent" patternUnits="userSpaceOnUse" width="8" height="8"><line x1="0" y1="8" x2="8" y2="0" stroke="${_sg}" stroke-width="0.5" opacity="0.4"/></pattern>`;
    svg += `<pattern id="pat-good" patternUnits="userSpaceOnUse" width="6" height="6"><circle cx="3" cy="3" r="1" fill="${_sy}" opacity="0.4"/></pattern>`;
    svg += `<pattern id="pat-weak" patternUnits="userSpaceOnUse" width="8" height="8"><line x1="0" y1="0" x2="8" y2="8" stroke="${_sr}" stroke-width="0.5" opacity="0.4"/><line x1="0" y1="8" x2="8" y2="0" stroke="${_sr}" stroke-width="0.5" opacity="0.4"/></pattern>`;
    svg += `</defs>`;
    const zonePatterns = { 'Excellent': 'pat-excellent', 'Good': 'pat-good', 'Weak': 'pat-weak' };
    const zoneDash = { 'Excellent': '4,2', 'Good': '6,3', 'Weak': '2,2' };
    const zoneBorder = { 'Excellent': _sg, 'Good': _sy, 'Weak': _sr };
    zones.forEach(z => {
      const x1 = pad + (z.snr[0] - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      const x2 = pad + (z.snr[1] - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      const y1 = h - pad - (z.rssi[1] - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      const y2 = h - pad - (z.rssi[0] - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      svg += `<rect x="${x1}" y="${y1}" width="${x2-x1}" height="${y2-y1}" fill="${z.color}"/>`;
      svg += `<rect x="${x1}" y="${y1}" width="${x2-x1}" height="${y2-y1}" fill="url(#${zonePatterns[z.label]})"/>`;
      svg += `<rect x="${x1}" y="${y1}" width="${x2-x1}" height="${y2-y1}" fill="none" stroke="${zoneBorder[z.label]}" stroke-width="1" stroke-dasharray="${zoneDash[z.label]}" opacity="0.6"/>`;
      svg += `<text x="${x1+4}" y="${y1+12}" font-size="9" fill="var(--text-muted)" opacity="0.7">${z.label}</text>`;
    });
    // Dots (sample if too many)
    const sample = data.length > 500 ? data.filter((_, i) => i % Math.ceil(data.length / 500) === 0) : data;
    sample.forEach(d => {
      const x = pad + (d.snr - snrMin) / (snrMax - snrMin) * (w - pad * 2);
      const y = h - pad - (d.rssi - rssiMin) / (rssiMax - rssiMin) * (h - pad * 2);
      svg += `<circle cx="${x}" cy="${y}" r="2" fill="var(--accent)" opacity="0.5"/>`;
    });
    svg += '</svg>';
    return svg;
  }

  function renderSNRByType(snrByType) {
    if (!snrByType.length) return '<div class="text-muted">No data</div>';
    let html = '<table class="analytics-table"><thead><tr><th scope="col">Type</th><th scope="col">Packets</th><th scope="col">Avg SNR</th><th scope="col">Min</th><th scope="col">Max</th><th scope="col">Distribution</th></tr></thead><tbody>';
    snrByType.forEach(t => {
      const barPct = Math.max(((t.avg - (-12)) / 27) * 100, 2);
      const color = t.avg > 6 ? statusGreen() : t.avg > 0 ? statusYellow() : statusRed();
      html += `<tr>
        <td><strong>${escapeHtml(t.name)}</strong></td>
        <td>${t.count}</td>
        <td><strong>${sf(t.avg, 1)} dB</strong></td>
        <td>${sf(t.min, 1)}</td>
        <td>${sf(t.max, 1)}</td>
        <td><div class="hash-bar-track" style="height:14px"><div class="hash-bar-fill" style="width:${barPct}%;background:${color};height:100%"></div></div></td>
      </tr>`;
    });
    return html + '</tbody></table>';
  }

  function renderSignalTimeline(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    const w = 400, h = 160, pad = 35;
    const maxPkts = Math.max(...data.map(d => d.count), 1);
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:160px" role="img" aria-label="Signal quality over time showing SNR trend and packet volume"><title>Signal quality over time showing SNR trend and packet volume</title>`;
    const snrPts = data.map((d, i) => {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      const y = h - pad - ((d.avgSnr + 12) / 27) * (h - pad * 2);
      return `${x},${y}`;
    }).join(' ');
    svg += `<polyline points="${snrPts}" fill="none" stroke="${statusGreen()}" stroke-width="2"/>`;
    // Packet count as area
    const areaPts = data.map((d, i) => {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      const y = h - pad - (d.count / maxPkts) * (h - pad * 2) * 0.4;
      return `${x},${y}`;
    });
    const baseline = data.map((_, i) => {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      return `${x},${h - pad}`;
    });
    svg += `<polygon points="${areaPts.join(' ')} ${baseline.reverse().join(' ')}" fill="var(--accent)" opacity="0.15"/>`;
    // Labels
    const step = Math.max(1, Math.floor(data.length / 6));
    for (let i = 0; i < data.length; i += step) {
      const x = pad + i * ((w - pad * 2) / Math.max(data.length - 1, 1));
      svg += `<text x="${x}" y="${h-pad+14}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${data[i].hour.slice(11)}h</text>`;
    }
    svg += '</svg>';
    svg += `<div class="timeline-legend"><span><span class="legend-dot" style="background:${statusGreen()}"></span>Avg SNR</span><span><span class="legend-dot" style="background:var(--accent);opacity:0.3"></span>Volume</span></div>`;
    return svg;
  }

  // ===================== TOPOLOGY =====================
  function renderTopology(el, topo) {
    el.innerHTML = `
      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-link"/></svg> Hop Count Distribution</h3>
          <p class="text-muted">Number of repeater hops per packet</p>
          ${barChart(topo.hopDistribution.map(h=>h.count), topo.hopDistribution.map(h=>h.hops), ['#3b82f6'])}
          <div class="rf-stats">
            <span>Avg: <strong>${sf(topo.avgHops, 1)} hops</strong></span>
            <span>Median: <strong>${topo.medianHops}</strong></span>
            <span>Max: <strong>${topo.maxHops}</strong></span>
            <span>1-hop direct: <strong>${topo.hopDistribution[0]?.count || 0}</strong></span>
          </div>
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-graph"/></svg> Top Repeaters</h3>
          <p class="text-muted">Nodes appearing most in packet paths</p>
          ${renderRepeaterTable(topo.topRepeaters)}
        </div>
      </div>

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-handshake"/></svg> Repeater Pair Heatmap</h3>
          <p class="text-muted">Which repeaters frequently appear together in paths</p>
          ${renderPairTable(topo.topPairs)}
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Hops vs SNR</h3>
          <p class="text-muted">Does more hops = worse signal?</p>
          ${renderHopsSNR(topo.hopsVsSnr)}
        </div>
      </div>

      <div class="analytics-card">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trophy"/></svg> Best Path to Each Node</h3>
        <p class="text-muted">Shortest hop distance seen across all observers</p>
        ${renderBestPath(topo.bestPathList)}
      </div>

      <div class="analytics-card">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-globe"/></svg> Per-Observer Reachability</h3>
        <p class="text-muted">Nodes at each hop distance, from each observer's perspective</p>
        ${topo.observers.length > 1 ? `<div class="observer-selector" id="obsSelector">
          ${topo.observers.map((o, i) => `<button class="tab-btn ${i === 0 ? 'active' : ''}" data-obs="${o.id}">${esc(o.name)}</button>`).join('')}
          <button class="tab-btn" data-obs="__all">All Observers</button>
        </div>` : ''}
        <div id="reachContent">${renderPerObserverReach(topo.perObserverReach, topo.observers[0]?.id)}</div>
      </div>

      ${topo.multiObsNodes.length ? `<div class="analytics-card">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-shuffle"/></svg> Cross-Observer Comparison</h3>
        <p class="text-muted">Nodes seen by multiple observers — hop distance varies by vantage point</p>
        ${renderCrossObserver(topo.multiObsNodes)}
      </div>` : ''}
    `;

    // Observer selector event handling
    const selector = document.getElementById('obsSelector');
    if (selector) {
      initTabBar(selector);
      selector.addEventListener('click', e => {
        const btn = e.target.closest('.tab-btn');
        if (!btn) return;
        selector.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        const obsId = btn.dataset.obs;
        document.getElementById('reachContent').innerHTML =
          obsId === '__all' ? renderAllObserversReach(topo.perObserverReach) : renderPerObserverReach(topo.perObserverReach, obsId);
      });
    }
  }

  function renderRepeaterTable(repeaters) {
    if (!repeaters.length) return '<div class="text-muted">No data</div>';
    const max = repeaters[0].count;
    let html = '<div class="repeater-list">';
    repeaters.slice(0, 15).forEach(r => {
      const pct = (r.count / max * 100).toFixed(0);
      html += `<div class="repeater-row ${r.pubkey ? 'clickable-row' : ''}" ${r.pubkey ? `onclick="location.hash='#/nodes/${encodeURIComponent(r.pubkey)}'"` : ''}>
        <div class="repeater-name">${r.name ? '<strong>' + esc(r.name) + '</strong>' : '<span class="mono">' + r.hop + '</span>'}</div>
        <div class="repeater-bar"><div class="hash-bar-track"><div class="hash-bar-fill" style="width:${pct}%;background:var(--accent)"></div></div></div>
        <div class="repeater-count">${r.count.toLocaleString()}</div>
      </div>`;
    });
    return html + '</div>';
  }

  function renderPairTable(pairs) {
    if (!pairs.length) return '<div class="text-muted">Not enough multi-hop data</div>';
    let html = '<table class="analytics-table"><thead><tr><th scope="col">Node A</th><th scope="col">Node B</th><th scope="col">Co-appearances</th></tr></thead><tbody>';
    pairs.slice(0, 12).forEach(p => {
      html += `<tr>
        <td>${p.nameA ? `<a href="#/nodes/${encodeURIComponent(p.pubkeyA)}" class="analytics-link">${esc(p.nameA)}</a>` : `<span class="mono">${p.hopA}</span>`}</td>
        <td>${p.nameB ? `<a href="#/nodes/${encodeURIComponent(p.pubkeyB)}" class="analytics-link">${esc(p.nameB)}</a>` : `<span class="mono">${p.hopB}</span>`}</td>
        <td>${p.count}</td>
      </tr>`;
    });
    return html + '</tbody></table>';
  }

  function renderHopsSNR(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    const w = 380, h = 160, pad = 40;
    const maxHop = Math.max(...data.map(d => d.hops));
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:160px" role="img" aria-label="Hops vs SNR bubble chart showing signal degradation over distance"><title>Hops vs SNR bubble chart showing signal degradation over distance</title>`;
    data.forEach(d => {
      const x = pad + (d.hops / maxHop) * (w - pad * 2);
      const y = h - pad - ((d.avgSnr + 12) / 27) * (h - pad * 2);
      const r = Math.min(Math.sqrt(d.count) * 1.5, 12);
      const color = d.avgSnr > 6 ? statusGreen() : d.avgSnr > 0 ? statusYellow() : statusRed();
      svg += `<circle cx="${x}" cy="${y}" r="${r}" fill="${color}" opacity="0.6"/>`;
      svg += `<text x="${x}" y="${y-r-3}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${d.hops}h</text>`;
    });
    svg += `<text x="${w/2}" y="${h-5}" text-anchor="middle" font-size="10" fill="var(--text-muted)">Hops</text>`;
    svg += `<text x="10" y="${h/2}" text-anchor="middle" font-size="10" fill="var(--text-muted)" transform="rotate(-90,10,${h/2})">Avg SNR</text>`;
    svg += '</svg>';
    return svg;
  }

  function renderPerObserverReach(perObserverReach, obsId) {
    const data = perObserverReach[obsId];
    if (!data || !data.rings.length) return '<div class="text-muted">No path data for this observer</div>';
    let html = `<div class="reach-rings">`;
    data.rings.forEach(ring => {
      const nodeLinks = ring.nodes.slice(0, 8).map(n => {
        const label = n.name ? `<a href="#/nodes/${encodeURIComponent(n.pubkey)}" class="analytics-link">${esc(n.name)}</a>` : `<span class="mono">${n.hop}</span>`;
        const detail = n.distRange ? ` <span class="text-muted">(${n.distRange})</span>` : '';
        return label + detail;
      }).join(', ');
      const extra = ring.nodes.length > 8 ? ` <span class="text-muted">+${ring.nodes.length - 8} more</span>` : '';
      html += `<div class="reach-ring">
        <div class="reach-hop">${ring.hops} hop${ring.hops > 1 ? 's' : ''}</div>
        <div class="reach-nodes">${nodeLinks}${extra}</div>
        <div class="reach-count">${ring.nodes.length} node${ring.nodes.length > 1 ? 's' : ''}</div>
      </div>`;
    });
    return html + '</div>';
  }

  function renderAllObserversReach(perObserverReach) {
    let html = '';
    for (const [obsId, data] of Object.entries(perObserverReach)) {
      html += `<h4 style="margin:12px 0 6px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> ${esc(data.observer_name)}</h4>`;
      html += renderPerObserverReach(perObserverReach, obsId);
    }
    return html || '<div class="text-muted">No data</div>';
  }

  function renderCrossObserver(nodes) {
    if (!nodes.length) return '<div class="text-muted">No nodes seen by multiple observers</div>';
    let html = `<table class="analytics-table">
      <thead><tr><th scope="col">Node</th><th scope="col">Observers</th><th scope="col">Hop Distances</th></tr></thead><tbody>`;
    nodes.forEach(n => {
      const name = n.name
        ? `<a href="#/nodes/${encodeURIComponent(n.pubkey)}" class="analytics-link">${esc(n.name)}</a>`
        : `<span class="mono">${n.hop}</span>`;
      const obsInfo = n.observers.map(o =>
        `${esc(o.observer_name)}: <strong>${o.minDist} hop${o.minDist > 1 ? 's' : ''}</strong> <span class="text-muted">(${o.count} pkts)</span>`
      ).join('<br>');
      html += `<tr><td>${name}</td><td>${n.observers.length}</td><td>${obsInfo}</td></tr>`;
    });
    return html + '</tbody></table>';
  }

  function renderBestPath(nodes) {
    if (!nodes.length) return '<div class="text-muted">No data</div>';
    // Group by distance for a cleaner view
    const byDist = {};
    nodes.forEach(n => {
      if (!byDist[n.minDist]) byDist[n.minDist] = [];
      byDist[n.minDist].push(n);
    });
    let html = '<div class="reach-rings">';
    Object.entries(byDist).sort((a, b) => +a[0] - +b[0]).forEach(([dist, nodes]) => {
      const nodeLinks = nodes.slice(0, 10).map(n => {
        const label = n.name
          ? `<a href="#/nodes/${encodeURIComponent(n.pubkey)}" class="analytics-link">${esc(n.name)}</a>`
          : `<span class="mono">${n.hop}</span>`;
        return label + ` <span class="text-muted">via ${esc(n.observer_name)}</span>`;
      }).join(', ');
      const extra = nodes.length > 10 ? ` <span class="text-muted">+${nodes.length - 10} more</span>` : '';
      html += `<div class="reach-ring">
        <div class="reach-hop">${dist} hop${+dist > 1 ? 's' : ''}</div>
        <div class="reach-nodes">${nodeLinks}${extra}</div>
        <div class="reach-count">${nodes.length} node${nodes.length > 1 ? 's' : ''}</div>
      </div>`;
    });
    return html + '</div>';
  }

  // ===================== CHANNELS =====================
  var _channelSortState = null;
  var _channelData = null;
  var _channelRenderGen = 0;
  var CHANNEL_SORT_KEY = 'meshcore-channel-sort';

  function loadChannelSort() {
    try {
      var s = localStorage.getItem(CHANNEL_SORT_KEY);
      if (s) { var p = JSON.parse(s); if (p.col && p.dir) return p; }
    } catch (e) {}
    return { col: 'lastActivity', dir: 'desc' };
  }

  // True when the user has explicitly chosen a sort (saved in localStorage).
  // Used by the grouped analytics view to decide whether to apply its own
  // default ("messages desc") instead of the global flat-list default.
  function hasSavedChannelSort() {
    try {
      var s = localStorage.getItem(CHANNEL_SORT_KEY);
      if (!s) return false;
      var p = JSON.parse(s);
      return !!(p && p.col && p.dir);
    } catch (e) { return false; }
  }

  function saveChannelSort(state) {
    try { localStorage.setItem(CHANNEL_SORT_KEY, JSON.stringify(state)); } catch (e) {}
  }

  function sortChannels(channels, col, dir) {
    var sorted = channels.slice();
    var mult = dir === 'asc' ? 1 : -1;
    sorted.sort(function (a, b) {
      var av, bv;
      switch (col) {
        case 'name':
          av = (a.name || '').toLowerCase(); bv = (b.name || '').toLowerCase();
          return av < bv ? -1 * mult : av > bv ? 1 * mult : 0;
        case 'hash':
          av = typeof a.hash === 'number' ? a.hash : String(a.hash);
          bv = typeof b.hash === 'number' ? b.hash : String(b.hash);
          if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * mult;
          av = String(av).toLowerCase(); bv = String(bv).toLowerCase();
          return av < bv ? -1 * mult : av > bv ? 1 * mult : 0;
        case 'messages': return (a.messages - b.messages) * mult;
        case 'senders': return (a.senders - b.senders) * mult;
        case 'lastActivity':
          av = a.lastActivity || ''; bv = b.lastActivity || '';
          return av < bv ? -1 * mult : av > bv ? 1 * mult : 0;
        case 'encrypted':
          av = a.encrypted ? 1 : 0; bv = b.encrypted ? 1 : 0;
          return (av - bv) * mult;
        default: return 0;
      }
    });
    return sorted;
  }

  function channelRowHtml(c) {
    // displayNameHtml: app-controlled raw HTML (e.g. the encrypted
    // placeholder which carries a Phosphor sprite — set by
    // decorateAnalyticsChannels for unknown encrypted rows). Same pattern
    // as the section-header row: sprite-bearing labels must NOT be
    // HTML-escaped (#1657 class of bug). For any other input we still
    // go through esc() — never trust c.name / c.displayName as HTML.
    var nameHtml = c.displayNameHtml
      ? c.displayNameHtml
      : esc(c.displayName || c.name || 'Unknown');
    return '<tr class="clickable-row" data-action="navigate" data-value="#/channels?ch=' + c.hash + '" tabindex="0" role="row">' +
      '<td><strong>' + nameHtml + '</strong></td>' +
      '<td class="mono">' + (typeof c.hash === 'number' ? '0x' + c.hash.toString(16).toUpperCase().padStart(2, '0') : c.hash) + '</td>' +
      '<td>' + c.messages + '</td>' +
      '<td>' + c.senders + '</td>' +
      '<td>' + timeAgo(c.lastActivity) + '</td>' +
      '<td>' + (c.encrypted ? (c.group === 'mine' ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-key"/></svg>' : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg>') : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg>') + '</td>' +
    '</tr>';
  }

  // ── PSK-aware decoration ──────────────────────────────────────────────────
  // Server returns raw "chNNN" placeholder names for encrypted channels it
  // doesn't know. Decorate so the UI shows a useful display name and a
  // group bucket: mine / network / encrypted. Pure function for testability.
  function decorateAnalyticsChannels(channels, hashByteToKeyName, labels) {
    var keyMap = hashByteToKeyName || {};
    var lab = labels || {};
    var out = [];
    for (var i = 0; i < (channels || []).length; i++) {
      var c = channels[i];
      var copy = Object.assign({}, c);
      var hashNum = typeof c.hash === 'number' ? c.hash : parseInt(c.hash, 10);
      var rawName = String(c.name || '');
      var isPlaceholder = /^ch(\d+|\?)$/.test(rawName);
      if (c.encrypted) {
        var keyName = !isNaN(hashNum) ? keyMap[hashNum] : null;
        if (keyName) {
          copy.displayName = lab[keyName] || keyName;
          copy.group = 'mine';
        } else if (isPlaceholder || !rawName) {
          // Placeholder ("chNNN") or empty name → render as opaque encrypted.
          // Empty-name encrypted rows would otherwise leak through with an
          // empty <strong> in the row; force the placeholder rendering.
          //
          // displayName carries a clean text label (no emoji, no markup) for
          // screen readers / sort comparison / tests. displayNameHtml carries
          // the rendered HTML with a Phosphor ph-lock sprite — consumed by
          // channelRowHtml(). Two fields, one source of truth, no escaping
          // surprises (v384-12.18 follow-up to #1648/#1657).
          var hex = !isNaN(hashNum)
            ? ' (0x' + hashNum.toString(16).toUpperCase().padStart(2, '0') + ')'
            : '';
          copy.displayName = 'Encrypted' + hex;
          copy.displayNameHtml =
            '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg> Encrypted' + hex;
          copy.group = 'encrypted';
        } else {
          // Server gave us a real name (rainbow table hit) for an encrypted ch.
          copy.displayName = rawName;
          copy.group = 'network';
        }
      } else {
        copy.displayName = rawName || 'Unknown';
        copy.group = 'network';
      }
      out.push(copy);
    }
    return out;
  }

  // Build the (hash byte → key name) map from ChannelDecrypt's stored keys.
  // Async because computeChannelHash uses subtle.digest. Returns {} if the
  // module or its keys are unavailable (graceful fallback).
  async function buildHashKeyMap() {
    if (typeof ChannelDecrypt === 'undefined' || !ChannelDecrypt.getStoredKeys) return {};
    var keys = ChannelDecrypt.getStoredKeys();
    var map = {};
    var names = Object.keys(keys || {});
    for (var ni = 0; ni < names.length; ni++) {
      var name = names[ni];
      try {
        var bytes = ChannelDecrypt.hexToBytes(keys[name]);
        var hb = await ChannelDecrypt.computeChannelHash(bytes);
        if (typeof hb === 'number') map[hb] = name;
      } catch (e) { /* skip bad key */ }
    }
    return map;
  }

  function channelTbodyHtml(channels, col, dir, opts) {
    var sorted = sortChannels(channels, col, dir);
    var parts = [];
    if (opts && opts.grouped) {
      // Group by .group: mine → network → encrypted. Inside each group keep
      // the active sort (caller passes col/dir; for the integration we sort
      // by messages desc by default).
      var groups = { mine: [], network: [], encrypted: [] };
      for (var gi = 0; gi < sorted.length; gi++) {
        var g = sorted[gi].group || (sorted[gi].encrypted ? 'encrypted' : 'network');
        (groups[g] || (groups[g] = [])).push(sorted[gi]);
      }
      var sections = [
        { key: 'mine', label: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-key"/></svg> My Channels' },
        { key: 'network', label: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-radio"/></svg> Network' },
        { key: 'encrypted', label: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg> Encrypted' },
      ];
      for (var si = 0; si < sections.length; si++) {
        var rows = groups[sections[si].key] || [];
        if (!rows.length) continue;
        parts.push(
          '<tr class="ch-section-row"><td colspan="6" class="ch-section-header">' +
          // sections[].label is a hardcoded sprite-bearing string (no user
          // input) — must be inserted raw so the <svg> renders. esc() here
          // HTML-escaped the angle brackets and surfaced literal "<svg…>"
          // text in the table (#1657).
          sections[si].label + ' <span class="text-muted">(' + rows.length + ')</span>' +
          '</td></tr>'
        );
        for (var ri = 0; ri < rows.length; ri++) parts.push(channelRowHtml(rows[ri]));
      }
    } else {
      for (var i = 0; i < sorted.length; i++) parts.push(channelRowHtml(sorted[i]));
    }
    return parts.join('');
  }

  function channelSortArrow(col, activeCol, dir) {
    if (col !== activeCol) return '<span class="sort-arrow">⇅</span>';
    return '<span class="sort-arrow">' + (dir === 'asc' ? '↑' : '↓') + '</span>';
  }

  function channelTheadHtml(activeCol, dir) {
    var cols = [
      { key: 'name', label: 'Channel' },
      { key: 'hash', label: 'Hash' },
      { key: 'messages', label: 'Messages' },
      { key: 'senders', label: 'Unique Senders' },
      { key: 'lastActivity', label: 'Last Activity' },
      { key: 'encrypted', label: 'Decrypted' },
    ];
    var ths = '';
    for (var i = 0; i < cols.length; i++) {
      var c = cols[i];
      ths += '<th scope="col" class="sortable' + (c.key === activeCol ? ' sort-active' : '') + '" data-sort-col="' + c.key + '">' +
        c.label + channelSortArrow(c.key, activeCol, dir) + '</th>';
    }
    return '<thead><tr>' + ths + '</tr></thead>';
  }

  function updateChannelTable() {
    var tbody = document.getElementById('channelsTbody');
    var thead = document.querySelector('#channelsTable thead');
    if (!tbody || !_channelData) return;
    tbody.innerHTML = channelTbodyHtml(_channelData, _channelSortState.col, _channelSortState.dir, { grouped: true });
    if (thead) thead.outerHTML = channelTheadHtml(_channelSortState.col, _channelSortState.dir);
  }

  function renderChannels(el, ch) {
    // Decorate first so grouping/display name reflect locally-stored PSK keys.
    // buildHashKeyMap is async; render once with a sync best-effort empty map,
    // then upgrade once keys resolve. That keeps first paint fast and avoids
    // blocking on subtle.digest in environments where it's slow.
    var rawChannels = ch.channels || [];
    // Resolve the persisted sort first so the default-fallback below doesn't
    // shadow what the user previously chose. Default for the grouped view is
    // messages desc (matches the PR description); only used when nothing saved.
    if (!_channelSortState) {
      _channelSortState = hasSavedChannelSort()
        ? loadChannelSort()
        : { col: 'messages', dir: 'desc' };
    }
    var ranOnce = false;
    // Generation token: if renderChannels is called again before
    // buildHashKeyMap() resolves, the older promise must not clobber the
    // newer rawChannels / decoration with stale-key data.
    var myGen = ++_channelRenderGen;
    function applyDecorate(map) {
      if (myGen !== _channelRenderGen) return; // superseded
      var labels = (typeof ChannelDecrypt !== 'undefined' && ChannelDecrypt.getLabels)
        ? ChannelDecrypt.getLabels() : {};
      _channelData = decorateAnalyticsChannels(rawChannels, map, labels);
      if (ranOnce) updateChannelTable();
    }
    applyDecorate({});
    ranOnce = true;
    buildHashKeyMap().then(applyDecorate).catch(function () { /* graceful */ });

    var timelineHtml = renderChannelTimeline(ch.channelTimeline);
    var topSendersHtml = renderTopSenders(ch.topSenders);
    var histoHtml = ch.msgLengths.length ? histogram(ch.msgLengths, 20, '#8b5cf6').svg : '<div class="text-muted">No decrypted messages</div>';

    el.innerHTML =
      '<div class="analytics-card">' +
        '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-radio"/></svg> Channel Activity</h3>' +
        '<p class="text-muted">' + ch.activeChannels + ' active channels, ' + ch.decryptable + ' decryptable</p>' +
        '<table class="analytics-table" id="channelsTable">' +
          channelTheadHtml(_channelSortState.col, _channelSortState.dir) +
          '<tbody id="channelsTbody">' +
            channelTbodyHtml(_channelData, _channelSortState.col, _channelSortState.dir, { grouped: true }) +
          '</tbody>' +
        '</table>' +
      '</div>' +
      '<div class="analytics-row">' +
        '<div class="analytics-card flex-1">' +
          '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chat-circle"/></svg> Messages / Hour by Channel</h3>' +
          timelineHtml +
        '</div>' +
        '<div class="analytics-card flex-1">' +
          '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-megaphone"/></svg> Top Senders</h3>' +
          topSendersHtml +
        '</div>' +
      '</div>' +
      '<div class="analytics-card">' +
        '<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Message Length Distribution</h3>' +
        histoHtml +
      '</div>';

    // Attach sort handler via delegation on the table
    var table = document.getElementById('channelsTable');
    if (table) {
      table.addEventListener('click', function (e) {
        var th = e.target.closest('th[data-sort-col]');
        if (!th) return;
        var col = th.dataset.sortCol;
        if (_channelSortState.col === col) {
          _channelSortState.dir = _channelSortState.dir === 'asc' ? 'desc' : 'asc';
        } else {
          _channelSortState.col = col;
          _channelSortState.dir = col === 'name' || col === 'hash' ? 'asc' : 'desc';
        }
        saveChannelSort(_channelSortState);
        updateChannelTable();
      });
    }
  }

  var CHANNEL_TIMELINE_MAX_SERIES = 8;

  function renderChannelTimeline(data) {
    if (!data.length) return '<div class="text-muted">No data</div>';
    var hours = []; var hourSet = {};
    var channelList = []; var channelSet = {};
    var lookup = {};
    var channelVolume = {};
    for (var i = 0; i < data.length; i++) {
      var d = data[i];
      if (!hourSet[d.hour]) { hourSet[d.hour] = 1; hours.push(d.hour); }
      if (!channelSet[d.channel]) { channelSet[d.channel] = 1; channelList.push(d.channel); }
      lookup[d.hour + '|' + d.channel] = d.count;
      channelVolume[d.channel] = (channelVolume[d.channel] || 0) + d.count;
    }
    hours.sort();
    // Sort channels by total volume descending, cap to top N
    channelList.sort(function(a, b) { return channelVolume[b] - channelVolume[a]; });
    var hiddenCount = Math.max(0, channelList.length - CHANNEL_TIMELINE_MAX_SERIES);
    var visibleChannels = channelList.slice(0, CHANNEL_TIMELINE_MAX_SERIES);

    var maxCount = 1;
    for (var vi = 0; vi < visibleChannels.length; vi++) {
      for (var hi2 = 0; hi2 < hours.length; hi2++) {
        var c = lookup[hours[hi2] + '|' + visibleChannels[vi]] || 0;
        if (c > maxCount) maxCount = c;
      }
    }

    var colors = ['#ef4444','#22c55e','#3b82f6','#f59e0b','#8b5cf6','#ec4899','#14b8a6','#64748b'];
    var w = 600, h = 180, pad = 35;
    var xScale = (w - pad * 2) / Math.max(hours.length - 1, 1);
    var yScale = (h - pad * 2) / maxCount;
    var svg = '<svg viewBox="0 0 ' + w + ' ' + h + '" style="width:100%;max-height:180px" role="img" aria-label="Channel message activity over time"><title>Channel message activity over time</title>';
    for (var ci = 0; ci < visibleChannels.length; ci++) {
      var pts = [];
      for (var hi = 0; hi < hours.length; hi++) {
        var count = lookup[hours[hi] + '|' + visibleChannels[ci]] || 0;
        var x = pad + hi * xScale;
        var y = h - pad - count * yScale;
        pts.push(x + ',' + y);
      }
      svg += '<polyline points="' + pts.join(' ') + '" fill="none" stroke="' + colors[ci % colors.length] + '" stroke-width="1.5" opacity="0.8"/>';
    }
    var step = Math.max(1, Math.floor(hours.length / 6));
    for (var li = 0; li < hours.length; li += step) {
      var lx = pad + li * xScale;
      svg += '<text x="' + lx + '" y="' + (h - pad + 14) + '" text-anchor="middle" font-size="9" fill="var(--text-muted)">' + hours[li].slice(11) + 'h</text>';
    }
    svg += '</svg>';
    var legendParts = [];
    for (var lci = 0; lci < visibleChannels.length; lci++) {
      legendParts.push('<span><span class="legend-dot" style="background:' + colors[lci % colors.length] + '"></span>' + esc(visibleChannels[lci]) + '</span>');
    }
    if (hiddenCount > 0) {
      legendParts.push('<span class="text-muted">+' + hiddenCount + ' more</span>');
    }
    svg += '<div class="timeline-legend">' + legendParts.join('') + '</div>';
    return svg;
  }

  function renderTopSenders(senders) {
    if (!senders.length) return '<div class="text-muted">No decrypted messages</div>';
    const max = senders[0].count;
    let html = '<div class="repeater-list">';
    senders.slice(0, 10).forEach(s => {
      html += `<div class="repeater-row">
        <div class="repeater-name"><strong>${esc(s.name)}</strong></div>
        <div class="repeater-bar"><div class="hash-bar-track"><div class="hash-bar-fill" style="width:${(s.count/max*100).toFixed(0)}%;background:#8b5cf6"></div></div></div>
        <div class="repeater-count">${s.count} msgs</div>
      </div>`;
    });
    return html + '</div>';
  }

  // ===================== HASH SIZES (original) =====================
  function renderHashSizes(el, data) {
    const d = data.distribution;
    const total = data.total;
    const pct = (n) => total ? (n / total * 100).toFixed(1) : '0';
    const maxCount = Math.max(d[1] || 0, d[2] || 0, d[3] || 0, 1);

    el.innerHTML = `
      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>Hash Size Distribution</h3>
          <p class="text-muted">${total.toLocaleString()} packets with path hops</p>
          <div class="hash-bars">
            ${[1, 2, 3].map(size => {
              const count = d[size] || 0;
              const width = Math.max((count / maxCount) * 100, count ? 2 : 0);
              const colors = { 1: '#ef4444', 2: '#22c55e', 3: '#3b82f6' };
              return `<div class="hash-bar-row">
              <div class="hash-bar-label"><strong>${size}-byte</strong> <span class="text-muted">(${size * 8}-bit, ${Math.pow(256, size).toLocaleString()} IDs)</span></div>
              <div class="hash-bar-track"><div class="hash-bar-fill" style="width:${width}%;background:${colors[size]}"></div></div>
              <div class="hash-bar-value">${count.toLocaleString()} <span class="text-muted">(${pct(count)}%)</span></div>
            </div>`;
            }).join('')}
          </div>
          ${data.distributionByRepeaters ? (() => {
            const dr = data.distributionByRepeaters;
            const totalRepeaters = (dr[1] || 0) + (dr[2] || 0) + (dr[3] || 0);
            const rpct = (n) => totalRepeaters ? (n / totalRepeaters * 100).toFixed(1) : '0';
            const maxRepeaters = Math.max(dr[1] || 0, dr[2] || 0, dr[3] || 0, 1);
            const colors = { 1: '#ef4444', 2: '#22c55e', 3: '#3b82f6' };
            return `<h4 style="margin:16px 0 4px">By Repeaters</h4>
              <p class="text-muted">${totalRepeaters.toLocaleString()} unique repeaters</p>
              <div class="hash-bars">
                ${[1, 2, 3].map(size => {
                  const count = dr[size] || 0;
                  const width = Math.max((count / maxRepeaters) * 100, count ? 2 : 0);
                  return `<div class="hash-bar-row">
                  <div class="hash-bar-label"><strong>${size}-byte</strong></div>
                  <div class="hash-bar-track"><div class="hash-bar-fill" style="width:${width}%;background:${colors[size]};opacity:0.7"></div></div>
                  <div class="hash-bar-value">${count.toLocaleString()} <span class="text-muted">(${rpct(count)}%)</span></div>
                </div>`;
                }).join('')}
              </div>`;
          })() : ''}
        </div>
        <div class="analytics-card flex-1">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trend-up"/></svg> Hash Size Over Time</h3>
          ${renderHashTimeline(data.hourly)}
        </div>
      </div>

      ${renderMultiByteAdopters(data.multiByteNodes, data.multiByteCapability || [])}

      <div class="analytics-row">
        <div class="analytics-card flex-1">
          <h3>Top Path Hops</h3>
        <table class="analytics-table">
          <thead><tr><th scope="col">Hop</th><th scope="col">Node</th><th scope="col">Bytes</th><th scope="col">Appearances</th></tr></thead>
          <tbody>
            ${data.topHops.map(h => {
              const link = h.pubkey ? `#/nodes/${encodeURIComponent(h.pubkey)}` : `#/packets?search=${h.hex}`;
              return `<tr class="clickable-row" data-action="navigate" data-value="${link}" tabindex="0" role="row">
              <td class="mono">${h.hex}</td>
              <td>${h.name ? `<strong>${esc(h.name)}</strong>` : '<span class="text-muted">unknown</span>'}</td>
              <td><span class="badge badge-hash-${h.size}">${h.size}-byte</span></td>
              <td>${h.count.toLocaleString()}</td>
            </tr>`;
            }).join('')}
          </tbody>
        </table>
        </div>
      </div>
    `;
  }

  function renderMultiByteAdopters(nodes, caps) {
    // Merge capability status into adopter nodes
    var capByPubkey = {};
    (caps || []).forEach(function(c) { capByPubkey[c.pubkey] = c; });

    var statusIcon = { confirmed: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg>', suspected: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>', unknown: '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg>' };
    var statusLabel = { confirmed: 'Confirmed', suspected: 'Suspected', unknown: 'Unknown' };
    var statusColor = { confirmed: 'var(--success, #22c55e)', suspected: 'var(--warning, #eab308)', unknown: 'var(--text-muted, #888)' };

    // Build merged rows: each adopter node gets a capability status
    var rows = (nodes || []).map(function(n) {
      var cap = capByPubkey[n.pubkey] || {};
      return {
        name: n.name, pubkey: n.pubkey || '', role: n.role || '',
        hashSize: n.hashSize, packets: n.packets, lastSeen: n.lastSeen,
        status: cap.status || 'unknown', evidence: cap.evidence || ''
      };
    });

    // Count statuses
    var counts = { confirmed: 0, suspected: 0, unknown: 0 };
    rows.forEach(function(r) { counts[r.status] = (counts[r.status] || 0) + 1; });

    function buildTableContent(rows, filter) {
      var filtered = filter === 'all' ? rows : rows.filter(function(r) { return r.status === filter; });
      return (filtered.length ? '<table class="analytics-table" id="mbAdoptersTable" style="margin-top:12px">' +
          '<thead><tr>' +
            '<th scope="col" data-sort="name">Node</th>' +
            '<th scope="col" data-sort="role">Role</th>' +
            '<th scope="col" data-sort="status">Status</th>' +
            '<th scope="col" data-sort="hashSize">Hash Size</th>' +
            '<th scope="col" data-sort="packets">Adverts</th>' +
            '<th scope="col" data-sort="lastSeen">Last Seen</th>' +
          '</tr></thead>' +
          '<tbody>' +
            filtered.map(function(r) {
              var roleColor = (window.ROLE_COLORS || {})[r.role] || '#6b7280';
              return '<tr class="clickable-row" data-action="navigate" data-value="#/nodes/' + encodeURIComponent(r.pubkey) + '" tabindex="0" role="row">' +
                '<td><strong>' + esc(r.name) + '</strong></td>' +
                '<td><span class="badge" style="' + (window.aaBadgeStyle ? window.aaBadgeStyle(roleColor) : 'background:' + roleColor + ';color:#fff') + '">' + esc(r.role || 'unknown') + '</span></td>' +
                '<td><span style="color:' + (statusColor[r.status] || statusColor.unknown) + '">' +
                  (statusIcon[r.status] || '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg>') + ' ' + (statusLabel[r.status] || 'Unknown') + '</span></td>' +
                '<td><span class="badge badge-hash-' + r.hashSize + '">' + r.hashSize + '-byte</span></td>' +
                '<td>' + r.packets + '</td>' +
                '<td>' + (r.lastSeen ? timeAgo(r.lastSeen) : '—') + '</td>' +
              '</tr>';
            }).join('') +
          '</tbody>' +
        '</table>' : '<div class="text-muted" style="padding:16px">No adopters match this filter.</div>');
    }

    if (!rows.length) return '<div class="analytics-row"><div class="analytics-card flex-1">' +
      '<h3>Multi-Byte Hash Adopters</h3>' +
      '<div class="text-muted" style="padding:16px">No multi-byte adopters found</div></div></div>';

    var html = '<div class="analytics-row"><div class="analytics-card flex-1" id="mbAdoptersSection">' +
      '<div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px">' +
        '<div>' +
          '<h3 style="margin:0">Multi-Byte Hash Adopters</h3>' +
          '<p class="text-muted" style="margin:4px 0 0;font-size:0.8em">Nodes advertising with 2+ byte hash paths. ' +
          '<strong>Confirmed</strong> = seen advertising with multi-byte hash. ' +
          '<strong>Suspected</strong> = prefix appeared in a multi-byte path. ' +
          '<strong>Unknown</strong> = no multi-byte evidence yet.</p>' +
        '</div>' +
        '<div style="display:flex;gap:4px;flex-wrap:wrap" id="mbCapFilters">' +
          '<button class="tab-btn active" data-mb-filter="all">All (' + rows.length + ')</button>' +
          '<button class="tab-btn" data-mb-filter="confirmed" style="--filter-color:var(--success, #22c55e)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> Confirmed (' + counts.confirmed + ')</button>' +
          '<button class="tab-btn" data-mb-filter="suspected" style="--filter-color:var(--warning, #eab308)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Suspected (' + counts.suspected + ')</button>' +
          '<button class="tab-btn" data-mb-filter="unknown" style="--filter-color:var(--text-muted, #888)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg> Unknown (' + counts.unknown + ')</button>' +
        '</div>' +
      '</div>' +
      '<div id="mbAdoptersTableWrap">' + buildTableContent(rows, 'all') + '</div>' +
    '</div></div>';

    // Use setTimeout for event delegation on the stable section container
    setTimeout(function() {
      var section = document.getElementById('mbAdoptersSection');
      if (!section) return;
      var currentFilter = 'all';

      section.addEventListener('click', function handler(e) {
        var btn = e.target.closest('[data-mb-filter]');
        if (btn) {
          currentFilter = btn.dataset.mbFilter;
          // Update active state on buttons (no DOM replacement needed)
          var buttons = section.querySelectorAll('[data-mb-filter]');
          buttons.forEach(function(b) { b.classList.toggle('active', b.dataset.mbFilter === currentFilter); });
          // Replace only the table content, not the whole section
          var wrap = section.querySelector('#mbAdoptersTableWrap');
          if (wrap) wrap.innerHTML = buildTableContent(rows, currentFilter);
          return;
        }
        var th = e.target.closest('[data-sort]');
        if (th) {
          var tbody = section.querySelector('tbody');
          if (!tbody) return;
          var sortRows = Array.from(tbody.querySelectorAll('tr'));
          var col = th.dataset.sort;
          var colIdx = { name: 0, status: 1, hashSize: 2, packets: 3, lastSeen: 4 };
          var statusWeight = { 'confirmed': 0, 'suspected': 1, 'unknown': 2 };
          sortRows.sort(function(a, b) {
            var va = a.children[colIdx[col]] ? a.children[colIdx[col]].textContent.trim() : '';
            var vb = b.children[colIdx[col]] ? b.children[colIdx[col]].textContent.trim() : '';
            if (col === 'status') {
              va = statusWeight[va.toLowerCase().split(' ').pop()] !== undefined ? statusWeight[va.toLowerCase().split(' ').pop()] : 2;
              vb = statusWeight[vb.toLowerCase().split(' ').pop()] !== undefined ? statusWeight[vb.toLowerCase().split(' ').pop()] : 2;
            }
            if (col === 'hashSize' || col === 'packets') { va = parseInt(va) || 0; vb = parseInt(vb) || 0; }
            if (va < vb) return -1;
            if (va > vb) return 1;
            return 0;
          });
          sortRows.forEach(function(r) { tbody.appendChild(r); });
        }
      });
    }, 100);

    return html;
  }

  // Legacy alias for tests — delegates to renderMultiByteAdopters with empty nodes
  function renderMultiByteCapability(caps) {
    if (!caps.length) return '';
    // Convert caps to adopter-style rows for backward compat
    var fakeNodes = caps.map(function(c) {
      return { name: c.name, pubkey: c.pubkey, role: c.role, hashSize: c.maxHashSize, packets: 0, lastSeen: c.lastSeen };
    });
    return renderMultiByteAdopters(fakeNodes, caps);
  }

  async function renderCollisionTab(el, data, collisionData) {
    el.innerHTML = `
      <nav id="hashIssuesToc" style="display:flex;gap:12px;margin-bottom:12px;font-size:13px;flex-wrap:wrap">
        <a href="#/analytics?tab=collisions&section=inconsistentHashSection" style="color:var(--link-color)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Inconsistent Sizes</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=collisions&section=hashMatrixSection" style="color:var(--link-color)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-list-numbers"/></svg> Hash Matrix</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=collisions&section=collisionRiskSection" style="color:var(--link-color)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-bomb"/></svg> Collision Risk</a>
        <span style="color:var(--border)">|</span>
        <a href="#/analytics?tab=prefix-tool" style="color:var(--link-color)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Check a prefix →</a>
      </nav>
      <p class="text-muted" style="margin:0 0 12px;font-size:0.78em">Collisions <strong>actually observed in packet traffic</strong> — among <strong>repeaters</strong> grouped by their configured hash size. For <em>theoretical</em> address conflicts that <em>would</em> occur if all repeaters used a given hash size, see the <a href="#/analytics?tab=prefix-tool" style="color:var(--link-color)">Prefix Tool</a> tab.</p>

      <div class="analytics-card" id="inconsistentHashSection">
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Inconsistent Hash Sizes</h3><a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <p class="text-muted" style="margin:4px 0 8px;font-size:0.8em">Repeaters and room servers sending adverts with varying hash sizes in the last 7 days. Originally caused by a <a href="https://github.com/meshcore-dev/MeshCore/commit/fcfdc5f" target="_blank" style="color:var(--link-color)">firmware bug</a> where automatic adverts ignored the configured multibyte path setting, fixed in <a href="https://github.com/meshcore-dev/MeshCore/releases/tag/repeater-v1.14.1" target="_blank" style="color:var(--link-color)">repeater v1.14.1</a>. Companion nodes are excluded.</p>
        <div id="inconsistentHashList"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading…</div></div>
      </div>

      <div class="analytics-card" id="hashMatrixSection">
        <div style="display:flex;justify-content:space-between;align-items:center">
          <h3 style="margin:0" id="hashMatrixTitle"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-list-numbers"/></svg> Hash Usage Matrix</h3>
          <a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a>
        </div>
        <div style="display:flex;align-items:center;gap:16px;margin:8px 0">
          <div class="hash-byte-selector" id="hashByteSelector" style="display:flex;gap:4px">
            <button class="hash-byte-btn active" data-bytes="1">1-Byte</button>
            <button class="hash-byte-btn" data-bytes="2">2-Byte</button>
            <button class="hash-byte-btn" data-bytes="3">3-Byte</button>
          </div>
          <p class="text-muted" id="hashMatrixDesc" style="margin:0;font-size:0.8em">Click a cell to see which nodes share that prefix.</p>
        </div>
        <div id="hashMatrix"></div>
      </div>

      <div class="analytics-card" id="collisionRiskSection">
        <div style="display:flex;justify-content:space-between;align-items:center"><h3 style="margin:0" id="collisionRiskTitle"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-bomb"/></svg> Collision Risk</h3><a href="#/analytics?tab=collisions" style="font-size:11px;color:var(--text-muted)">↑ top</a></div>
        <div id="collisionList"><div class="text-muted" style="padding:8px">Loading…</div></div>
      </div>
    `;
    // Use pre-computed collision data from server (no more /nodes?limit=2000 fetch)
    const cData = collisionData || { inconsistent_nodes: [], by_size: {} };
    const inconsistent = cData.inconsistent_nodes || [];
    const ihEl = document.getElementById('inconsistentHashList');
    if (ihEl) {
      if (!inconsistent.length) {
        ihEl.innerHTML = '<div class="text-muted" style="padding:4px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No inconsistencies detected — all nodes are reporting consistent hash sizes.</div>';
      } else {
        ihEl.innerHTML = `<table class="analytics-table" style="background:var(--card-bg);border:1px solid var(--border);border-radius:8px;overflow:hidden">
          <thead><tr><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Current Hash</th><th scope="col">Sizes Seen</th></tr></thead>
          <tbody>${inconsistent.map((n, i) => {
            const roleColor = window.ROLE_COLORS?.[n.role] || '#6b7280';
            const prefix = n.hash_size ? n.public_key.slice(0, n.hash_size * 2).toUpperCase() : '?';
            const sizeBadges = (Array.isArray(n.hash_sizes_seen) ? n.hash_sizes_seen : []).map(s => {
              const c = s >= 3 ? '#16a34a' : s === 2 ? '#86efac' : '#f97316';
              const fg = s === 2 ? '#064e3b' : '#fff';
              return '<span class="badge" style="background:' + c + ';color:' + fg + ';font-size:10px;font-family:var(--mono)">' + s + 'B</span>';
            }).join(' ');
            const stripe = i % 2 === 1 ? 'background:var(--row-stripe)' : '';
            return `<tr style="${stripe}">
              <td><a href="#/nodes/${encodeURIComponent(n.public_key)}?section=node-packets" style="font-weight:600;color:var(--link-color)">${esc(n.name || n.public_key.slice(0, 12))}</a></td>
              <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(roleColor)) || ('background:'+roleColor+';color:#fff')}">${n.role}</span></td>
              <td><code style="font-family:var(--mono);font-weight:700">${prefix}</code> <span class="text-muted">(${n.hash_size || '?'}B)</span></td>
              <td>${sizeBadges}</td>
            </tr>`;
          }).join('')}</tbody>
        </table>
        <p class="text-muted" style="margin:8px 0 0;font-size:0.8em">${inconsistent.length} node${inconsistent.length > 1 ? 's' : ''} affected. Click a node name to see which adverts have different hash sizes.</p>`;
      }
    }

    // Repeaters and routing nodes no longer needed — collision data is server-computed

    let currentBytes = 1;
    function refreshHashViews(bytes) {
      currentBytes = bytes;
      hideMatrixTip();
      // Update selector button states
      document.querySelectorAll('.hash-byte-btn').forEach(b => {
        b.classList.toggle('active', Number(b.dataset.bytes) === bytes);
      });
      // Update titles and description
      const matrixTitle = document.getElementById('hashMatrixTitle');
      const matrixDesc = document.getElementById('hashMatrixDesc');
      const riskTitle = document.getElementById('collisionRiskTitle');
      if (matrixTitle) matrixTitle.innerHTML = bytes === 3 ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-list-numbers"/></svg> Hash Usage Matrix' : `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-list-numbers"/></svg> ${bytes}-Byte Hash Usage Matrix`;
      if (riskTitle) riskTitle.innerHTML = `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-bomb"/></svg> ${bytes}-Byte Collision Risk`;
      if (matrixDesc) {
        if (bytes === 1) matrixDesc.textContent = 'Cells include the first byte of all repeaters — including those using 2- or 3-byte prefixes — so this reflects real conflicts in the 1-byte hash space. Click a cell to see the nodes.';
        else if (bytes === 2) matrixDesc.textContent = 'Each cell = first-byte group. Color shows worst 2-byte collision within. Click a cell to see the breakdown.';
        else matrixDesc.textContent = '3-byte prefix space is too large to visualize as a matrix — collision table is shown below.';
      }
      renderHashMatrixFromServer(cData.by_size[String(bytes)], bytes);
      // Show collision risk section for all byte sizes
      const riskCard = document.getElementById('collisionRiskSection');
      if (riskCard) riskCard.style.display = '';
      renderCollisionsFromServer(cData.by_size[String(bytes)], bytes);
    }

    // Wire up selector
    document.getElementById('hashByteSelector')?.querySelectorAll('.hash-byte-btn').forEach(btn => {
      btn.addEventListener('click', () => refreshHashViews(Number(btn.dataset.bytes)));
    });

    refreshHashViews(1);
  }

  function renderHashTimeline(hourly) {
    if (!hourly.length) return '<div class="text-muted">Not enough data</div>';
    const w = 800, h = 180, pad = 35;
    const maxVal = Math.max(...hourly.map(h => Math.max(h[1] || 0, h[2] || 0, h[3] || 0)), 1);
    const colors = { 1: '#ef4444', 2: '#22c55e', 3: '#3b82f6' };
    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:180px" role="img" aria-label="Hash size distribution over time showing 1-byte, 2-byte, and 3-byte hash trends"><title>Hash size distribution over time showing 1-byte, 2-byte, and 3-byte hash trends</title>`;
    for (const size of [1, 2, 3]) {
      const pts = hourly.map((d, i) => {
        const x = pad + i * ((w - pad * 2) / Math.max(hourly.length - 1, 1));
        const y = h - pad - ((d[size] || 0) / maxVal) * (h - pad * 2);
        return `${x},${y}`;
      }).join(' ');
      if (hourly.some(d => d[size] > 0)) svg += `<polyline points="${pts}" fill="none" stroke="${colors[size]}" stroke-width="2"/>`;
    }
    const step = Math.max(1, Math.floor(hourly.length / 8));
    for (let i = 0; i < hourly.length; i += step) {
      const x = pad + i * ((w - pad * 2) / Math.max(hourly.length - 1, 1));
      svg += `<text x="${x}" y="${h-5}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${hourly[i].hour.slice(11)}h</text>`;
    }
    svg += '</svg>';
    svg += `<div class="timeline-legend"><span><span class="legend-dot" style="background:#ef4444"></span>1-byte</span><span><span class="legend-dot" style="background:#22c55e"></span>2-byte</span><span><span class="legend-dot" style="background:#3b82f6"></span>3-byte</span></div>`;
    return svg;
  }

  // Shared hover tooltip for hash matrix cells.
  // Called once per container — reads content from data-tip on each <td>.
  // Single shared tooltip element for the entire hash matrix — avoids DOM accumulation on mode switch
  let _matrixTip = null;
  function getMatrixTip() {
    if (!_matrixTip) {
      _matrixTip = document.createElement('div');
      _matrixTip.className = 'hash-matrix-tooltip';
      _matrixTip.style.display = 'none';
      document.body.appendChild(_matrixTip);
    }
    return _matrixTip;
  }
  function hideMatrixTip() { if (_matrixTip) _matrixTip.style.display = 'none'; }

  // SECURITY (ANL-1, PR #1539 round 2): rebuild tooltip body via DOM APIs.
  // The previous round-1 fix used `tip.textContent = td.dataset.tip`, which
  // defeated the mutation-XSS but also broke the rendered tooltip — the
  // payload had been a structured HTML string from hashTooltipHtml(), so
  // users saw literal `<div class="...">…</div>` text. The correct fix is
  // to stop carrying HTML through the dataset round-trip entirely: each
  // tooltip field rides as its own data-tip-* attribute (entity-decoded by
  // the browser on read = plain text), and we materialize the three styled
  // child <div>s via createElement + textContent. No innerHTML on any
  // node-controlled field path.
  function buildMatrixTipChildren(tip, td) {
    while (tip.firstChild) tip.removeChild(tip.firstChild);
    const ds = td.dataset || {};
    if (ds.tipHex) {
      const h = document.createElement('div');
      h.className = 'hash-matrix-tooltip-hex';
      h.textContent = ds.tipHex;
      tip.appendChild(h);
    }
    if (ds.tipStatus) {
      const s = document.createElement('div');
      s.className = 'hash-matrix-tooltip-status';
      s.textContent = ds.tipStatus;
      tip.appendChild(s);
    }
    if (ds.tipLines) {
      const wrap = document.createElement('div');
      wrap.className = 'hash-matrix-tooltip-nodes';
      const lines = ds.tipLines.split('\u001f');
      for (let i = 0; i < lines.length; i++) {
        const row = document.createElement('div');
        row.style.fontSize = '11px';
        row.textContent = lines[i];
        wrap.appendChild(row);
      }
      tip.appendChild(wrap);
    }
  }

  function initMatrixTooltip(el) {
    if (el._matrixTipInit) return;
    el._matrixTipInit = true;
    el.addEventListener('mouseover', e => {
      const td = e.target.closest('td[data-tip-hex]');
      if (!td) return;
      const tip = getMatrixTip();
      buildMatrixTipChildren(tip, td);
      tip.style.display = 'block';
    });
    el.addEventListener('mousemove', e => {
      if (!_matrixTip || _matrixTip.style.display === 'none') return;
      const x = e.clientX + 14, y = e.clientY + 14;
      _matrixTip.style.left = Math.min(x, window.innerWidth - _matrixTip.offsetWidth - 8) + 'px';
      _matrixTip.style.top = Math.min(y, window.innerHeight - _matrixTip.offsetHeight - 8) + 'px';
    });
    el.addEventListener('mouseout', e => {
      if (e.target.closest('td[data-tip-hex]') && !e.relatedTarget?.closest('td[data-tip-hex]')) hideMatrixTip();
    });
    el.addEventListener('mouseleave', hideMatrixTip);
  }

  // --- Shared helpers for hash matrix rendering ---

  function hashStatCardsHtml(totalNodes, usingCount, sizeLabel, spaceSize, usedCount, collisionCount) {
    const pct = spaceSize > 0 && usedCount > 0 ? ((usedCount / spaceSize) * 100) : 0;
    const pctStr = spaceSize > 65536 ? pct.toFixed(6) : spaceSize > 256 ? pct.toFixed(3) : pct.toFixed(1);
    const spaceLabel = spaceSize >= 1e6 ? (spaceSize / 1e6).toFixed(1) + 'M' : spaceSize.toLocaleString();
    return `<div style="display:flex;gap:12px;flex-wrap:wrap;margin-bottom:12px">
      <div class="analytics-stat-card" style="flex:1;min-width:110px">
        <div class="analytics-stat-label">Nodes tracked</div>
        <div class="analytics-stat-value">${totalNodes.toLocaleString()}</div>
      </div>
      <div class="analytics-stat-card" style="flex:1;min-width:110px">
        <div class="analytics-stat-label">Using ${sizeLabel} ID</div>
        <div class="analytics-stat-value">${usingCount.toLocaleString()}</div>
      </div>
      <div class="analytics-stat-card" style="flex:1;min-width:110px">
        <div class="analytics-stat-label">Prefix space used</div>
        <div class="analytics-stat-value" style="font-size:16px">${pctStr}%</div>
        <div style="font-size:10px;color:var(--text-muted);margin-top:2px">${usedCount > 256 ? usedCount + ' of ' : 'of '}${spaceLabel} possible</div>
      </div>
      <div class="analytics-stat-card" style="flex:1;min-width:110px;border-color:${collisionCount > 0 ? 'var(--status-red)' : 'var(--border)'}${collisionCount > 0 ? ';cursor:pointer' : ''}" ${collisionCount > 0 ? 'onclick="document.getElementById(\'collisionRiskSection\')?.scrollIntoView({behavior:\'smooth\',block:\'start\'})"' : ''} ${collisionCount > 0 ? 'title="Click to see collision details"' : ''}>
        <div class="analytics-stat-label">Prefix collisions</div>
        <div class="analytics-stat-value" style="color:${collisionCount > 0 ? 'var(--status-red)' : 'var(--status-green)'}">${collisionCount}${collisionCount > 0 ? ' <span style="font-size:11px;opacity:0.7"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-down"/></svg></span>' : ''}</div>
      </div>
    </div>`;
  }

  function hashMatrixGridHtml(nibbles, cellSize, headerSize, cellDataFn) {
    let html = `<div style="display:flex;gap:16px;flex-wrap:wrap"><div class="hash-matrix-scroll"><table class="hash-matrix-table" style="border-collapse:collapse;font-size:12px;font-family:monospace">`;
    html += `<tr><td style="width:${headerSize}px"></td>`;
    for (const n of nibbles) html += `<td style="width:${cellSize}px;text-align:center;padding:2px 0;font-weight:bold;color:var(--text-muted)">${n}</td>`;
    html += '</tr>';
    for (let hi = 0; hi < 16; hi++) {
      html += `<tr><td style="text-align:right;padding-right:4px;font-weight:bold;color:var(--text-muted)">${nibbles[hi]}</td>`;
      for (let lo = 0; lo < 16; lo++) {
        html += cellDataFn(nibbles[hi] + nibbles[lo], cellSize);
      }
      html += '</tr>';
    }
    html += '</table></div>';
    return html;
  }

  function hashMatrixLegendHtml(labels) {
    return `<div style="margin-top:8px;font-size:0.8em;display:flex;gap:16px;align-items:center;flex-wrap:wrap">
      ${labels.map(l => `<span><span class="legend-swatch ${l.cls}"${l.style ? ' style="'+l.style+'"' : ''}></span> ${l.text}</span>`).join('\n')}
    </div>`;
  }

  // --- Shared cell classification for hash matrix ---

  function classifyHashCell(count, isConfirmedCollision, isPossibleConflict) {
    if (count === 0) return { cls: 'hash-cell-empty', bg: '' };
    if (!isConfirmedCollision && !isPossibleConflict) return { cls: 'hash-cell-taken', bg: '' };
    if (isPossibleConflict) return { cls: 'hash-cell-possible', bg: '' };
    const t = Math.min((count - 2) / 4, 1);
    // #1668-M4 r1: gradient endpoints darkened from orange-500→red-500 (3.11:1/3.96:1 vs #fff)
    // to orange-800→red-800 (7.31:1→8.31:1 vs #fff) — passes WCAG AA across the whole range.
    // Encoding stays distinguishable from green-700 (taken) and yellow-800 (possible).
    const cr = Math.round(154 + (-1) * t);   // 154 → 153
    const cg = Math.round(52  + (-25) * t);  // 52  → 27
    const cb = Math.round(18  + 9 * t);      // 18  → 27
    return { cls: 'hash-cell-collision', bg: `background:rgb(${cr},${cg},${cb});` };
  }

  // hashCellTd — emits a hash-matrix <td>. Tooltip data rides as separate
  // data-tip-* attributes (plain text per field); buildMatrixTipChildren()
  // materializes the styled DOM on mouseover. NEVER carry HTML through
  // data-tip-* — see ANL-1 (PR #1539).
  function hashCellTd(hex, cellSize, cls, bg, count, tipSpec, fontWeight) {
    // tipSpec: { hex: string, status?: string, lines?: string[] }
    const spec = tipSpec || {};
    const hexAttr = ' data-tip-hex="' + esc(spec.hex || '') + '"';
    const statusAttr = spec.status
      ? ' data-tip-status="' + esc(spec.status) + '"'
      : '';
    // Join row lines with U+001F (Unit Separator) — a control char that
    // never appears in node names, so we can split safely on the read side.
    const linesAttr = (spec.lines && spec.lines.length)
      ? ' data-tip-lines="' + esc(spec.lines.join('\u001f')) + '"'
      : '';
    return `<td class="hash-cell ${cls}${count ? ' hash-active' : ''}" data-hex="${hex}"${hexAttr}${statusAttr}${linesAttr} style="width:${cellSize}px;height:${cellSize}px;text-align:center;${bg}border:1px solid var(--border);cursor:${count ? 'pointer' : 'default'};font-size:12px;font-weight:${fontWeight}">${hex}</td>`;
  }

  // hashTooltipSpec — returns a plain-data tooltip descriptor consumed by
  // hashCellTd and rendered by buildMatrixTipChildren. Plain text only;
  // no HTML, no markup. `lines` is an array of pre-formatted row strings.
  function hashTooltipSpec(hexLabel, statusText, lines) {
    const spec = { hex: hexLabel, status: statusText };
    if (lines && lines.length) spec.lines = lines;
    return spec;
  }

  function renderHashMatrixPanel(el, statCardsHtml, cellRendererFn, detailMaxWidth, legendLabels, clickHandlerFn) {
    const nibbles = '0123456789ABCDEF'.split('');
    const cellSize = 36;
    const headerSize = 24;
    let html = statCardsHtml;
    html += hashMatrixGridHtml(nibbles, cellSize, headerSize, cellRendererFn);
    html += `<div id="hashDetail" style="flex:1;min-width:200px;max-width:${detailMaxWidth}px;font-size:0.85em"></div></div>`;
    html += hashMatrixLegendHtml(legendLabels);
    el.innerHTML = html;
    initMatrixTooltip(el);
    // #1473 — Grey out cells whose first byte the MeshCore firmware keygen
    // routine avoids (pub_key[0] in {0x00, 0xFF}). This is a keygen
    // CONVENTION, not a protocol-level rejection — see firmware
    // examples/simple_repeater/main.cpp:83 (HEAD 8ede7641). Must run BEFORE
    // we wire click handlers so .hash-active is stripped first.
    if (typeof PrefixReserved !== 'undefined' && PrefixReserved && typeof PrefixReserved.markReservedCells === 'function') {
      PrefixReserved.markReservedCells(el);
    }
    el.querySelectorAll('.hash-active').forEach(td => {
      td.addEventListener('click', () => {
        clickHandlerFn(td);
        el.querySelectorAll('.hash-selected').forEach(c => c.classList.remove('hash-selected'));
        td.classList.add('hash-selected');
      });
    });
  }

  function renderHashMatrixFromServer(sizeData, bytes) {
    const el = document.getElementById('hashMatrix');
    if (!sizeData) { el.innerHTML = '<div class="text-muted">No data</div>'; return; }
    const stats = sizeData.stats || {};
    const totalNodes = stats.total_nodes || 0;

    // 3-byte: show a summary panel instead of a matrix
    if (bytes === 3) {
      el.innerHTML = hashStatCardsHtml(totalNodes, stats.using_this_size || 0, '3-byte', 16777216, stats.unique_prefixes || 0, stats.collision_count || 0) +
        `<p class="text-muted" style="margin:0;font-size:0.8em">The 3-byte prefix space (16.7M values) is too large to visualize as a grid.${(stats.collision_count || 0) > 0 ? ' See collision details below.' : ''}</p>` +
        `<p class="text-muted" style="margin:8px 0 0;font-size:0.8em">ℹ️ This tab only counts collisions among repeaters configured for this hash size. The <a href="#/analytics?tab=prefix-tool" style="color:var(--link-color)">Prefix Tool</a> checks all repeaters regardless of configured hash size.</p>`;
      return;
    }

    if (bytes === 1) {
      const oneByteCells = sizeData.one_byte_cells || {};
      const oneByteCount = stats.using_this_size || 0;
      const oneUsed = Object.values(oneByteCells).filter(v => v.length > 0).length;
      const oneCollisions = Object.values(oneByteCells).filter(v => v.length > 1).length;

      renderHashMatrixPanel(el,
        hashStatCardsHtml(totalNodes, oneByteCount, '1-byte', 256, oneUsed, oneCollisions),
        (hex, cs) => {
          const nodes = oneByteCells[hex] || [];
          const count = nodes.length;
          const repeaterCount = nodes.filter(n => n.role === 'repeater').length;
          const isCollision = count >= 2 && repeaterCount >= 2;
          const isPossible = count >= 2 && !isCollision;
          const { cls, bg } = classifyHashCell(count, isCollision, isPossible);
          const nodeLabel = m => (m.name || m.public_key.slice(0,12)) + (!m.role ? ' (unknown role)' : '');
          const previewLines = nodes.slice(0,5).map(nodeLabel);
          if (nodes.length > 5) previewLines.push(`+${nodes.length-5} more`);
          const tip = count === 0 ? hashTooltipSpec(`0x${hex}`, 'Available')
            : count === 1 ? hashTooltipSpec(`0x${hex}`, 'One node — no collision', [nodeLabel(nodes[0])])
            : isPossible ? hashTooltipSpec(`0x${hex}`, `${count} nodes — POSSIBLE CONFLICT`, previewLines)
            : hashTooltipSpec(`0x${hex}`, `${count} nodes — COLLISION`, previewLines);
          return hashCellTd(hex, cs, cls, bg, count, tip, count >= 2 ? '700' : '400');
        },
        400,
        [
          {cls: 'hash-cell-empty', style: 'border:1px solid var(--border)', text: 'Available'},
          {cls: 'hash-cell-taken', text: 'One node'},
          {cls: 'hash-cell-possible', text: 'Possible conflict'},
          {cls: 'hash-cell-collision', style: 'background:rgb(154,40,22)', text: 'Collision'}
        ],
        (td) => {
          const hex = td.dataset.hex.toUpperCase();
          const matches = oneByteCells[hex] || [];
          const detail = document.getElementById('hashDetail');
          if (!matches.length) { detail.innerHTML = `<strong class="mono">0x${hex}</strong><br><span class="text-muted">No known nodes</span>`; return; }
          detail.innerHTML = `<strong class="mono" style="font-size:1.1em">0x${hex}</strong> — ${matches.length} node${matches.length !== 1 ? 's' : ''}` +
            `<div style="margin-top:8px">${matches.map(m => {
              const coords = (m.lat && m.lon && !(m.lat === 0 && m.lon === 0)) ? `<span class="text-muted" style="font-size:0.8em">(${m.lat.toFixed(2)}, ${m.lon.toFixed(2)})</span>` : '<span class="text-muted" style="font-size:0.8em">(no coords)</span>';
              const role = m.role ? `<span class="badge" style="font-size:0.7em;padding:1px 4px;background:var(--border)">${esc(m.role)}</span> ` : '';
              return `<div style="padding:3px 0">${role}<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a> ${coords}</div>`;
            }).join('')}</div>`;
        }
      );

    } else if (bytes === 2) {
      const twoByteCells = sizeData.two_byte_cells || {};
      const twoByteCount = stats.using_this_size || 0;
      const uniqueTwoBytePrefixes = stats.unique_prefixes || 0;
      const twoCollisions = Object.values(twoByteCells).filter(v => v.collision_count > 0).length;

      renderHashMatrixPanel(el,
        hashStatCardsHtml(totalNodes, twoByteCount, '2-byte', 65536, uniqueTwoBytePrefixes, twoCollisions),
        (hex, cs) => {
          const info = twoByteCells[hex] || { group_nodes: [], max_collision: 0, collision_count: 0, two_byte_map: {} };
          const nodeCount = (info.group_nodes || []).length;
          const maxCol = info.max_collision || 0;
          const overlapping = Object.values(info.two_byte_map || {}).filter(v => v.length > 1);
          const hasConfirmed = overlapping.some(ns => ns.filter(n => n.role === 'repeater').length >= 2);
          const hasPossible = !hasConfirmed && overlapping.some(ns => ns.length >= 2);
          const { cls, bg } = classifyHashCell(maxCol > 0 ? maxCol : nodeCount === 0 ? 0 : 1, hasConfirmed, hasPossible);
          const nodeLabel2 = m => (m.name || m.public_key.slice(0,8)) + (!m.role ? ' (?)' : '');
          const tip = nodeCount === 0
            ? hashTooltipSpec(`0x${hex}__`, 'No nodes in this group')
            : (info.collision_count || 0) === 0
              ? hashTooltipSpec(`0x${hex}__`, `${nodeCount} node${nodeCount>1?'s':''} — no 2-byte collisions`)
              : hashTooltipSpec(`0x${hex}__`,
                  hasConfirmed ? (info.collision_count||0) + ' collision' + ((info.collision_count||0)>1?'s':'') : 'Possible conflict',
                  Object.entries(info.two_byte_map||{}).filter(([,v])=>v.length>1).slice(0,4).map(([p,ns])=>`${p} — ${ns.map(nodeLabel2).join(', ')}`));
          return hashCellTd(hex, cs, cls, bg, nodeCount, tip, maxCol > 0 ? '700' : '400');
        },
        420,
        [
          {cls: 'hash-cell-empty', style: 'border:1px solid var(--border)', text: 'No nodes in group'},
          {cls: 'hash-cell-taken', text: 'Nodes present, no collision'},
          {cls: 'hash-cell-possible', text: 'Possible conflict'},
          {cls: 'hash-cell-collision', style: 'background:rgb(154,40,22)', text: 'Collision'}
        ],
        (td) => {
          const hex = td.dataset.hex.toUpperCase();
          const info = twoByteCells[hex];
          const detail = document.getElementById('hashDetail');
          if (!info || !(info.group_nodes || []).length) { detail.innerHTML = ''; return; }
          const groupNodes = info.group_nodes || [];
          let dhtml = `<strong class="mono" style="font-size:1.1em">0x${hex}__</strong> — ${groupNodes.length} node${groupNodes.length !== 1 ? 's' : ''} in group`;
          if ((info.collision_count || 0) === 0) {
            dhtml += `<div class="text-muted" style="margin-top:6px;font-size:0.85em"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No 2-byte collisions in this group</div>`;
            dhtml += `<div style="margin-top:8px">${groupNodes.map(m => {
              const prefix = m.public_key.slice(0,4).toUpperCase();
              return `<div style="padding:2px 0"><code class="mono" style="font-size:0.85em">${prefix}</code> <a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a></div>`;
            }).join('')}</div>`;
          } else {
            dhtml += `<div style="margin-top:8px">`;
            for (const [twoHex, nodes] of Object.entries(info.two_byte_map || {}).sort()) {
              const isCollision = nodes.length > 1;
              dhtml += `<div style="margin-bottom:6px;padding:4px 6px;border-radius:4px;background:${isCollision ? 'rgba(220,50,30,0.1)' : 'transparent'};border:1px solid ${isCollision ? 'rgba(220,50,30,0.3)' : 'transparent'}">`;
              dhtml += `<code class="mono" style="font-size:0.9em;font-weight:${isCollision?'700':'400'}">${twoHex}</code>${isCollision ? ' <span style="color:var(--danger);font-size:0.75em;font-weight:700">COLLISION</span>' : ''} `;
              dhtml += nodes.map(m => `<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link" style="font-size:0.85em">${esc(m.name || m.public_key.slice(0,12))}</a>`).join(', ');
              dhtml += `</div>`;
            }
            dhtml += '</div>';
          }
          detail.innerHTML = dhtml;
        }
      );
    }
  }

  function renderCollisionsFromServer(sizeData, bytes) {
    const el = document.getElementById('collisionList');
    if (!sizeData) { el.innerHTML = '<div class="text-muted">No data</div>'; return; }
    const collisions = sizeData.collisions || [];

    if (!collisions.length) {
      const cleanMsg = bytes === 3
        ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No 3-byte prefix collisions detected — all repeaters have unique 3-byte prefixes.'
        : `<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No ${bytes}-byte collisions detected`;
      el.innerHTML = `<div class="text-muted" style="padding:8px">${cleanMsg}</div>`;
      return;
    }

    const showAppearances = bytes < 3;
    const t50 = formatDistanceRound(50);
    const t200 = formatDistanceRound(200);
    el.innerHTML = `<table class="analytics-table">
      <thead><tr>
        <th scope="col">Prefix</th>
        ${showAppearances ? '<th scope="col">Appearances</th>' : ''}
        <th scope="col">Max Distance</th>
        <th scope="col">Assessment</th>
        <th scope="col">Colliding Nodes</th>
      </tr></thead>
      <tbody>${collisions.map(c => {
        let badge, tooltip;
        if (c.classification === 'local') {
          badge = `<span class="badge" style="background:var(--status-green);color:#fff" title="All nodes within ${t50} — likely true collision, same RF neighborhood"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-buildings"/></svg> Local</span>`;
          tooltip = 'Nodes close enough for direct RF — probably genuine prefix collision';
        } else if (c.classification === 'regional') {
          badge = `<span class="badge" style="background:var(--status-yellow);color:#fff" title="Nodes ${t50}–${t200} apart — edge of LoRa range, could be atmospheric"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lightning"/></svg> Regional</span>`;
          tooltip = 'At edge of 915MHz range — could indicate atmospheric ducting or hilltop-to-hilltop links';
        } else if (c.classification === 'distant') {
          badge = `<span class="badge" style="background:var(--status-red);color:#fff" title="Nodes >${t200} apart — beyond typical 915MHz range"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-globe"/></svg> Distant</span>`;
          tooltip = 'Beyond typical LoRa range — likely internet bridging, MQTT gateway, or separate mesh networks sharing prefix';
        } else {
          badge = '<span class="badge" style="background:#6b7280;color:#fff"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-question"/></svg> Unknown</span>';
          tooltip = 'Not enough coordinate data to classify';
        }
        const nodes = c.nodes || [];
        const distStr = c.with_coords >= 2 ? formatDistanceRound(c.max_dist_km) : '<span class="text-muted">—</span>';
        return `<tr>
          <td class="mono">${c.prefix}</td>
          ${showAppearances ? `<td>${(c.appearances || 0).toLocaleString()}</td>` : ''}
          <td>${distStr}</td>
          <td title="${tooltip}">${badge}</td>
          <td>${nodes.map(m => {
            const loc = (m.lat && m.lon && !(m.lat === 0 && m.lon === 0))
              ? ` <span class="text-muted" style="font-size:0.75em">(${m.lat.toFixed(2)}, ${m.lon.toFixed(2)})</span>`
              : ' <span class="text-muted" style="font-size:0.75em">(no coords)</span>';
            return `<a href="#/nodes/${encodeURIComponent(m.public_key)}" class="analytics-link">${esc(m.name || m.public_key.slice(0,12))}</a>${loc}`;
          }).join('<br>')}</td>
        </tr>`;
      }).join('')}</tbody>
    </table>
    <div class="text-muted" style="padding:8px;font-size:0.8em">
      <strong><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-buildings"/></svg> Local</strong> &lt;${t50}: true prefix collision, same mesh area &nbsp;
      <strong><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lightning"/></svg> Regional</strong> ${t50}–${t200}: edge of LoRa range, possible atmospheric propagation &nbsp;
      <strong><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-globe"/></svg> Distant</strong> &gt;${t200}: beyond 915MHz range — internet bridge, MQTT gateway, or separate networks
    </div>`;
  }
    async function renderSubpaths(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Analyzing route patterns…</div>';
    try {
      const rq = RegionFilter.regionQueryString();
      // Issue #1217: thread the Time window picker into the Route Patterns
      // request so the chart actually reflects the user's selection.
      const twEl = document.getElementById('analyticsTimeWindow');
      const twVal = twEl ? twEl.value : '';
      const tws = twVal ? '&window=' + encodeURIComponent(twVal) : '';
      const bulk = await api('/analytics/subpaths-bulk?groups=2-2:50,3-3:30,4-4:20,5-8:15' + rq + tws, { ttl: CLIENT_TTL.analyticsRF });
      const [d2, d3, d4, d5] = bulk.results;

      function renderTable(data, title) {
        if (!data.subpaths.length) return `<h4>${title}</h4><div class="text-muted">No data</div>`;
        // #1633 — when "Hide 1-byte path hops" is ON, filter route patterns
        // whose underlying rawHops contain any 1-byte hex token. We filter
        // INPUT (not just CSS-hide) so the displayed % and ordering reflect
        // the surviving population.
        const _hide1 = !!(typeof window !== 'undefined' && window.MC_getHide1ByteHops && window.MC_getHide1ByteHops());
        const _hop1 = function (h) { return String(h || '').length === 2; };
        const subpaths = _hide1
          ? data.subpaths.filter(function (s) {
              var rh = s.rawHops || [];
              for (var k = 0; k < rh.length; k++) if (_hop1(rh[k])) return false;
              return true;
            })
          : data.subpaths;
        if (!subpaths.length) return `<h4>${title}</h4><div class="text-muted">No data (all matching routes contained 1-byte hops — toggle off in customizer to see)</div>`;
        const maxCount = subpaths[0]?.count || 1;
        return `<h4>${title}</h4>
          <p class="text-muted" style="margin:4px 0 8px">From ${data.totalPaths.toLocaleString()} paths with 2+ hops${_hide1 ? ` · showing ${subpaths.length} of ${data.subpaths.length} (1-byte filtered)` : ''}</p>
          <table class="analytics-table"><thead><tr>
            <th scope="col">#</th><th scope="col">Route</th><th scope="col">Occurrences</th><th scope="col">% of paths</th><th scope="col">Frequency</th>
          </tr></thead><tbody>
          ${subpaths.map((s, i) => {
            const barW = Math.max(2, Math.round(s.count / maxCount * 100));
            const hops = s.path.split(' → ');
            const rawHops = s.rawHops || [];
            const hasSelfLoop = hops.some((h, j) => j > 0 && h === hops[j - 1]);
            const routeDisplay = hops.map(h => esc(h)).join(' → ');
            const prefixDisplay = rawHops.join(' → ');
            return `<tr data-hops="${esc(rawHops.join(','))}" ${hasSelfLoop ? 'class="subpath-selfloop"' : ''} style="cursor:pointer">
              <td>${i + 1}</td>
              <td>${routeDisplay}${hasSelfLoop ? ' <span title="Contains self-loop — likely 1-byte prefix collision" style="cursor:help"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-arrow-clockwise"/></svg></span>' : ''}<br><span class="hop-prefix mono">${esc(prefixDisplay)}</span></td>
              <td>${s.count.toLocaleString()}</td>
              <td>${s.pct}%</td>
              <td><div style="background:${hasSelfLoop ? 'var(--status-yellow)' : 'var(--accent)'};height:14px;border-radius:3px;width:${barW}%;opacity:0.7"></div></td>
            </tr>`;
          }).join('')}
          </tbody></table>`;
      }

      el.innerHTML = `
        <div class="subpath-layout">
          <div class="subpath-list" id="subpathList">
            <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-path"/></svg> Route Pattern Analysis</h3>
            <p>Click a route to see details. Most common subpaths — reveals backbone routes, bottlenecks, and preferred relay chains.</p>
            <label style="display:inline-flex;align-items:center;gap:6px;margin-bottom:12px;cursor:pointer;font-size:0.9em">
              <input type="checkbox" id="hideCollisions" aria-label="Hide likely prefix collisions" ${localStorage.getItem('subpath-hide-collisions') === '1' ? 'checked' : ''}> Hide likely prefix collisions (self-loops)
            </label>
            <div class="subpath-jump-nav">
              <span>Jump to:</span>
              <a href="#sp-pairs">Pairs</a>
              <a href="#sp-triples">Triples</a>
              <a href="#sp-quads">Quads</a>
              <a href="#sp-long">5+ hops</a>
            </div>
            <div id="sp-pairs">${renderTable(d2, 'Pairs (2-hop links)')}</div>
            <div id="sp-triples">${renderTable(d3, 'Triples (3-hop chains)')}</div>
            <div id="sp-quads">${renderTable(d4, 'Quads (4-hop chains)')}</div>
            <div id="sp-long">${renderTable(d5, 'Long chains (5+ hops)')}</div>
          </div>
          <div class="subpath-detail collapsed" id="subpathDetail">
            <div class="text-muted" style="padding:40px;text-align:center">Select a route to view details</div>
          </div>
        </div>`;

      // Click handler for rows
      el.addEventListener('click', e => {
        const tr = e.target.closest('tr[data-hops]');
        if (!tr) return;
        el.querySelectorAll('tr.subpath-selected').forEach(r => r.classList.remove('subpath-selected'));
        tr.classList.add('subpath-selected');
        loadSubpathDetail(tr.dataset.hops);
      });

      // Jump nav — scroll within list panel
      el.querySelectorAll('.subpath-jump-nav a').forEach(a => {
        a.addEventListener('click', e => {
          e.preventDefault();
          const target = document.getElementById(a.getAttribute('href').slice(1));
          if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
        });
      });

      // Collision toggle
      const toggle = document.getElementById('hideCollisions');
      function applyCollisionFilter() {
        const hide = toggle.checked;
        localStorage.setItem('subpath-hide-collisions', hide ? '1' : '0');
        el.querySelectorAll('tr.subpath-selfloop').forEach(r => r.style.display = hide ? 'none' : '');
      }
      toggle.addEventListener('change', applyCollisionFilter);
      applyCollisionFilter();
    } catch (e) {
      el.innerHTML = `<div class="text-muted">Error loading subpath data: ${e.message}</div>`;
    }
  }

  async function loadSubpathDetail(hopsStr) {
    const panel = document.getElementById('subpathDetail');
    panel.classList.remove('collapsed');
    panel.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading…</div>';
    try {
      const data = await api('/analytics/subpath-detail?hops=' + encodeURIComponent(hopsStr), { ttl: CLIENT_TTL.analyticsRF });
      renderSubpathDetail(panel, data);
    } catch (e) {
      panel.innerHTML = `<div class="text-muted">Error: ${e.message}</div>`;
    }
  }

  function renderSubpathDetail(panel, data) {
    const nodesWithLoc = data.nodes.filter(n => n.lat && n.lon && !(n.lat === 0 && n.lon === 0));
    const hasMap = nodesWithLoc.length >= 2;
    const maxHour = Math.max(...data.hourDistribution, 1);

    panel.innerHTML = `
      <div class="subpath-detail-inner">
        <h4>${data.nodes.map(n => esc(n.name)).join(' → ')}</h4>
        <div class="subpath-meta">
          <span class="hop-prefix mono">${data.hops.join(' → ')}</span>
          <span>${data.totalMatches.toLocaleString()} occurrences</span>
        </div>

        ${nodesWithLoc.length >= 2 ? `<div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-ruler"/></svg> Hop Distances</h5>
          ${(() => {
            const dists = [];
            let total = 0;
            for (let i = 0; i < data.nodes.length - 1; i++) {
              const a = data.nodes[i], b = data.nodes[i+1];
              if (a.lat && a.lon && b.lat && b.lon && !(a.lat===0&&a.lon===0) && !(b.lat===0&&b.lon===0)) {
                const km = window.HopResolver && window.HopResolver.haversineKm
                  ? window.HopResolver.haversineKm(a.lat, a.lon, b.lat, b.lon)
                  : (() => { const R=6371, dLat=(b.lat-a.lat)*Math.PI/180, dLon=(b.lon-a.lon)*Math.PI/180, h=Math.sin(dLat/2)**2+Math.cos(a.lat*Math.PI/180)*Math.cos(b.lat*Math.PI/180)*Math.sin(dLon/2)**2; return R*2*Math.atan2(Math.sqrt(h),Math.sqrt(1-h)); })();
                total += km;
                const cls = km > 200 ? 'color:var(--status-red);font-weight:bold' : km > 50 ? 'color:var(--status-yellow)' : 'color:var(--status-green-text)';
                dists.push(`<div style="padding:2px 0"><span style="${cls}">${formatDistance(km)}</span> <span class="text-muted">${esc(a.name)} → ${esc(b.name)}</span></div>`);
              } else {
                dists.push(`<div style="padding:2px 0"><span class="text-muted">? ${esc(a.name)} → ${esc(b.name)} (no coords)</span></div>`);
              }
            }
            if (dists.length > 1) dists.push(`<div style="padding:4px 0;border-top:1px solid var(--border);margin-top:4px"><strong>Total: ${formatDistance(total)}</strong></div>`);
            return dists.join('');
          })()}
        </div>` : ''}

        ${hasMap ? '<div id="subpathMap" style="height:200px;border-radius:8px;margin:12px 0;border:1px solid var(--border,#e5e7eb)"></div>' : ''}

        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg> Observer Receive Signal</h5>
          <p class="text-muted" style="font-size:0.8em;margin:0 0 4px">Last hop → observer only, not between nodes in the route</p>
          ${data.signal.avgSnr != null
            ? `<div>Avg SNR: <strong>${data.signal.avgSnr} dB</strong> · Avg RSSI: <strong>${data.signal.avgRssi} dBm</strong> · ${data.signal.samples} samples</div>`
            : '<div class="text-muted">No signal data</div>'}
        </div>

        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clock"/></svg> Activity by Hour (UTC)</h5>
          <div class="hour-chart">
            ${data.hourDistribution.map((c, h) => `<div class="hour-bar" title="${h}:00 UTC — ${c} packets" style="height:${Math.max(2, c / maxHour * 100)}%"></div>`).join('')}
          </div>
          <div class="hour-labels"><span>0</span><span>6</span><span>12</span><span>18</span><span>23</span></div>
        </div>

        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clock"/></svg> Timeline</h5>
          <div>First seen: ${data.firstSeen ? (typeof formatAbsoluteTimestamp === 'function' ? formatAbsoluteTimestamp(data.firstSeen) : new Date(data.firstSeen).toLocaleString()) : '—'}</div>
          <div>Last seen: ${data.lastSeen ? (typeof formatAbsoluteTimestamp === 'function' ? formatAbsoluteTimestamp(data.lastSeen) : new Date(data.lastSeen).toLocaleString()) : '—'}</div>
        </div>

        ${data.observers.length ? `
        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> Observers</h5>
          ${data.observers.map(o => `<div>${esc(o.name)}: ${o.count}</div>`).join('')}
        </div>` : ''}

        ${data.parentPaths.length ? `
        <div class="subpath-section">
          <h5><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-link"/></svg> Full Paths Containing This Route</h5>
          <div class="parent-paths">
            ${data.parentPaths.map(p => `<div class="parent-path"><span class="mono" style="font-size:0.85em">${esc(p.path)}</span> <span class="text-muted">×${p.count}</span></div>`).join('')}
          </div>
        </div>` : ''}
      </div>`;

    // Render minimap
    if (hasMap && typeof L !== 'undefined') {
      const map = L.map('subpathMap', { zoomControl: false, attributionControl: false });
      L.tileLayer(getTileUrl(), { maxZoom: 18 }).addTo(map);

      const latlngs = [];
      nodesWithLoc.forEach((n, i) => {
        const ll = [n.lat, n.lon];
        latlngs.push(ll);
        const isEnd = i === 0 || i === nodesWithLoc.length - 1;
        L.circleMarker(ll, {
          radius: isEnd ? 8 : 5,
          color: isEnd ? (i === 0 ? statusGreen() : statusRed()) : statusYellow(),
          fillColor: isEnd ? (i === 0 ? statusGreen() : statusRed()) : statusYellow(),
          fillOpacity: 0.9, weight: 2
        }).bindTooltip(esc(n.name), { permanent: false }).addTo(map);
      });

      L.polyline(latlngs, { color: statusYellow(), weight: 3, dashArray: '8,6', opacity: 0.8 }).addTo(map);
      map.fitBounds(L.latLngBounds(latlngs).pad(0.3));
    }
  }

  async function renderNodesTab(el) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading node analytics…</div>';
    try {
      const rq = RegionFilter.regionQueryString() + AreaFilter.areaQueryString();
      const [nodesResp, bulkHealth] = await Promise.all([
        fetchAllNodes('&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList }),
        api('/nodes/bulk-health?limit=50' + rq, { ttl: CLIENT_TTL.analyticsRF })
      ]);
      const nodes = nodesResp.nodes || nodesResp;
      const myNodes = JSON.parse(localStorage.getItem('meshcore-my-nodes') || '[]');
      const myKeys = new Set(myNodes.map(n => n.pubkey));

      // Map bulk health by pubkey
      const healthMap = {};
      bulkHealth.forEach(h => { healthMap[h.public_key] = h; });
      const enriched = nodes.filter(n => healthMap[n.public_key]).map(n => ({ ...n, health: { stats: healthMap[n.public_key].stats, observers: healthMap[n.public_key].observers } }));

      // Compute rankings
      const byPackets = [...enriched].sort((a, b) => (b.health.stats.totalTransmissions || b.health.stats.totalPackets || 0) - (a.health.stats.totalTransmissions || a.health.stats.totalPackets || 0));
      const bySnr = [...enriched].filter(n => n.health.stats.avgSnr != null).sort((a, b) => b.health.stats.avgSnr - a.health.stats.avgSnr);
      const byObservers = [...enriched].sort((a, b) => (b.health.observers?.length || 0) - (a.health.observers?.length || 0));
      const byRecent = [...enriched].filter(n => n.health.stats.lastHeard).sort((a, b) => new Date(b.health.stats.lastHeard) - new Date(a.health.stats.lastHeard));

      // Compute network status client-side from loaded nodes using shared getHealthThresholds()
      const now = Date.now();
      let active = 0, degraded = 0, silent = 0;
      nodes.forEach(function(n) {
        const role = n.role || 'unknown';
        const th = getHealthThresholds(role);
        const lastMs = n.last_heard ? new Date(n.last_heard).getTime()
                     : n.last_seen ? new Date(n.last_seen).getTime()
                     : 0;
        const age = lastMs ? (now - lastMs) : Infinity;
        if (age < th.degradedMs) active++;
        else if (age < th.silentMs) degraded++;
        else silent++;
      });
      const totalNodes = nodesResp.total || nodes.length;
      const roleCounts = nodesResp.counts || {};

      function nodeLink(n) {
        return `<a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link">${esc(n.name || n.public_key.slice(0, 12))}</a>`;
      }
      function claimedBadge(n) {
        return myKeys.has(n.public_key) ? ' <span style="color:var(--link-color);font-size:10px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg> MINE</span>' : '';
      }

      // ROLE_COLORS from shared roles.js

      el.innerHTML = `
        <div class="analytics-section">
          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"/></svg> Network Status</h3>
          <div style="display:flex;gap:16px;flex-wrap:wrap;margin-bottom:20px">
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-green-text)">${active}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)"><span style="color:var(--status-green-text)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span> Active</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-yellow)">${degraded}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)"><span style="color:var(--status-yellow)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span> Degraded</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700;color:var(--status-red)">${silent}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)"><span style="color:var(--status-red)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span> Silent</div>
            </div>
            <div class="analytics-stat-card" style="flex:1;min-width:120px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
              <div style="font-size:28px;font-weight:700">${totalNodes}</div>
              <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)">Total Nodes</div>
            </div>
          </div>

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Role Breakdown</h3>
          <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:24px">
            ${Object.entries(roleCounts).sort((a,b) => b[1]-a[1]).map(([role, count]) => {
              const c = ROLE_COLORS[role] || '#6b7280';
              return `<span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(c)) || ('background:'+c+';color:#fff')};padding:6px 12px;font-size:13px">${role}: ${count}</span>`;
            }).join('')}
          </div>

          ${myKeys.size ? `<h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg> My Claimed Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Packets</th><th scope="col">Avg SNR</th><th scope="col">Observers</th><th scope="col">Last Heard</th></tr></thead>
            <tbody>
              ${enriched.filter(n => myKeys.has(n.public_key)).map(n => {
                const s = n.health.stats;
                return `<tr>
                  <td>${nodeLink(n)}</td>
                  <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                  <td>${s.totalTransmissions || s.totalPackets || 0}</td>
                  <td>${s.avgSnr != null ? s.avgSnr.toFixed(1) + ' dB' : '—'}</td>
                  <td>${n.health.observers?.length || 0}</td>
                  <td>${s.lastHeard ? timeAgo(s.lastHeard) : '—'}</td>
                </tr>`;
              }).join('') || '<tr><td colspan="6" class="text-muted">No claimed nodes have health data</td></tr>'}
            </tbody>
          </table>` : ''}

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trophy"/></svg> Most Active Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Total Packets</th><th scope="col">Packets Today</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byPackets.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                <td>${n.health.stats.totalTransmissions || n.health.stats.totalPackets || 0}</td>
                <td>${n.health.stats.packetsToday || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link" aria-label="Per-node analytics"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-cell-signal-high"/></svg> Best Signal Quality</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Avg SNR</th><th scope="col">Observers</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${bySnr.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                <td>${n.health.stats.avgSnr.toFixed(1)} dB</td>
                <td>${n.health.observers?.length || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link" aria-label="Per-node analytics"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-eye"/></svg> Most Observed Nodes</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">#</th><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Observers</th><th scope="col">Avg SNR</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byObservers.slice(0, 15).map((n, i) => `<tr>
                <td>${i + 1}</td>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                <td>${n.health.observers?.length || 0}</td>
                <td>${n.health.stats.avgSnr != null ? n.health.stats.avgSnr.toFixed(1) + ' dB' : '—'}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link" aria-label="Per-node analytics"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></a></td>
              </tr>`).join('')}
            </tbody>
          </table>

          <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clock"/></svg> Recently Active</h3>
          <table class="analytics-table" style="margin-bottom:24px">
            <thead><tr><th scope="col">Node</th><th scope="col">Role</th><th scope="col">Last Heard</th><th scope="col">Packets Today</th><th scope="col">Analytics</th></tr></thead>
            <tbody>
              ${byRecent.slice(0, 15).map(n => `<tr>
                <td>${nodeLink(n)}${claimedBadge(n)}</td>
                <td><span class="badge" style="${(window.aaBadgeStyle && window.aaBadgeStyle(ROLE_COLORS[n.role]||'#6b7280')) || ('background:'+(ROLE_COLORS[n.role]||'#6b7280')+';color:#fff')}">${n.role}</span></td>
                <td>${timeAgo(n.health.stats.lastHeard)}</td>
                <td>${n.health.stats.packetsToday || 0}</td>
                <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link" aria-label="Per-node analytics"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg></a></td>
              </tr>`).join('')}
            </tbody>
          </table>
        </div>`;
    } catch (e) {
      el.innerHTML = `<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load node analytics: ${esc(e.message)}</div>`;
    }
  }

  // ===================== MY REPEATERS (favorites monitoring dashboard) =====================
  // An at-a-glance monitor over the repeaters the operator has starred
  // (meshcore-favorites). Pure frontend aggregation over existing endpoints:
  //   /api/nodes                      relay activity + traffic/bridge scores
  //   /api/nodes/clock-skew           per-node "is the clock OK" severity
  //   /api/analytics/neighbor-graph   affinity edges, filtered to favorites
  //                                   ("throughput between my repeaters")
  function _mrSummaryCard(value, label, color) {
    return `<div class="analytics-stat-card" style="flex:1;min-width:110px;text-align:center;padding:16px;background:var(--card-bg);border:1px solid var(--border);border-radius:8px">
      <div style="font-size:26px;font-weight:700${color ? ';color:' + color : ''}">${esc(String(value))}</div>
      <div style="font-size:11px;text-transform:uppercase;color:var(--text-muted)">${esc(label)}</div>
    </div>`;
  }

  // Status is carried by colour AND text — never colour alone — so it is
  // legible to color-blind users and screen readers.
  const MR_STATUS_META = {
    active: { color: 'var(--status-green)', label: 'Active' },
    degraded: { color: 'var(--status-yellow)', label: 'Degraded' },
    silent: { color: 'var(--status-red)', label: 'Silent' },
    unknown: { color: 'var(--text-muted)', label: 'Unknown' },
  };
  function _mrStatusCell(status) {
    // Fall back to an explicit "Unknown" rather than silently mislabelling an
    // unexpected status as "Silent".
    const m = MR_STATUS_META[status] || MR_STATUS_META.unknown;
    return `<span style="display:inline-flex;gap:5px;align-items:center;white-space:nowrap"><span style="color:${m.color}" aria-hidden="true"><svg class="ph-icon"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span><span style="font-size:11px">${m.label}</span></span>`;
  }

  function _mrEmptyState() {
    return `
      <div class="analytics-section" style="text-align:center;padding:48px 24px;color:var(--text-muted)">
        <svg class="ph-icon" aria-hidden="true" style="width:40px;height:40px;opacity:0.6"><use href="/icons/phosphor-sprite.svg#ph-star"/></svg>
        <h3 style="margin:12px 0 6px">No favorite repeaters yet</h3>
        <p>Star a repeater (the <svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star"/></svg> on the <a href="#/nodes" class="analytics-link">Nodes</a> page or a node's detail) to add it to your watch-list. Starred repeaters show up here for at-a-glance monitoring.</p>
      </div>`;
  }

  async function renderMyRepeatersTab(el) {
    const haveFavs = () => (typeof getFavorites === 'function' ? getFavorites() : []);
    if (!haveFavs().length) { el.innerHTML = _mrEmptyState(); return; }
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading your repeaters…</div>';
    try {
      const rq = RegionFilter.regionQueryString() + AreaFilter.areaQueryString();
      // When a region/area filter is active the node fetch is scoped, so a
      // starred repeater outside that scope won't come back. Surface that count
      // instead of silently dropping it from the watch-list (#1761 MAJOR).
      const filterActive = rq.length > 0;
      // Fetch the three sources ONCE; star toggles re-paint from this cache
      // (paint() below) instead of re-hitting the network. Trade-off: this
      // pages the full node list (up to ~10k) to keep only the starred few —
      // fine for a handful of favorites and amortized by CLIENT_TTL.nodeList
      // across tabs. If favorite counts ever grow large, switch to per-node
      // Promise.all(favs.map(pk => api('/nodes/' + pk))).
      const [nodesResp, clockData, graphData] = await Promise.all([
        fetchAllNodes('&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList }),
        api('/nodes/clock-skew', { ttl: CLIENT_TTL.analyticsRF }).catch(() => []),
        api('/analytics/neighbor-graph?min_count=1&min_score=0', { ttl: CLIENT_TTL.analyticsRF }).catch(() => ({ nodes: [], edges: [] })),
      ]);
      const allNodes = nodesResp.nodes || nodesResp;
      const clockByPk = {};
      (Array.isArray(clockData) ? clockData : []).forEach(c => { if (c && c.pubkey) clockByPk[c.pubkey] = c; });

      const now = Date.now();
      function ageOf(n) {
        const ms = n.last_heard ? new Date(n.last_heard).getTime() : n.last_seen ? new Date(n.last_seen).getTime() : 0;
        return ms ? now - ms : Infinity;
      }
      function statusOf(n) {
        const th = (typeof getHealthThresholds === 'function') ? getHealthThresholds(n.role) : { degradedMs: 3600000, silentMs: 86400000 };
        const age = ageOf(n);
        return age < th.degradedMs ? 'active' : age < th.silentMs ? 'degraded' : 'silent';
      }
      const pct = v => (v != null ? (v * 100).toFixed(1) + '%' : '—');
      // #1456: prefer traffic_share_score, fall back to usefulness_score.
      const trafficOf = n => (n.traffic_share_score != null ? n.traffic_share_score : (n.usefulness_score != null ? n.usefulness_score : null));
      function roleBadge(role) {
        // Route the unknown-role fallback through ROLE_COLORS.unknown (backed by
        // --mc-role-unknown / the shared Wong palette) instead of a hard-coded
        // literal, so every swatch color flows through the CSS-variable system
        // (#1761 nit). ROLE_COLORS always resolves .unknown.
        const rc = window.ROLE_COLORS || {};
        const c = rc[role] || rc.unknown || 'var(--role-unknown, #6b7280)';
        const style = (window.aaBadgeStyle && window.aaBadgeStyle(c)) || ('background:' + c + ';color:#fff');
        return `<span class="badge" style="${style}">${esc(role)}</span>`;
      }

      // Re-renders from the cached fetch using the LIVE favorites set, so an
      // un-star drops the row with no refetch. Re-binds its stars after each
      // paint (the innerHTML reset clears listeners).
      function paint() {
        const favs = new Set(haveFavs());
        if (!favs.size) { el.innerHTML = _mrEmptyState(); return; }
        const myNodes = allNodes.filter(n => favs.has(n.public_key) && (n.role === 'repeater' || n.role === 'room'));
        const myKeys = new Set(myNodes.map(n => n.public_key));

        // Favorites the scoped fetch didn't return — with a region/area filter
        // active these are (most likely) outside the current scope. Surface the
        // count so a hidden favorite isn't silently missing (#1761 MAJOR).
        const allByPk = new Set(allNodes.map(n => n.public_key));
        // Favorites absent from the scoped fetch: usually outside the active
        // region/area filter, but the set also covers deleted/expired stars
        // and role-filtered clients — so the notice stays causally neutral
        // ("not shown with the active filter") rather than claiming a cause.
        const hiddenByFilter = [...favs].filter(pk => !allByPk.has(pk)).length;
        const filterNotice = (filterActive && hiddenByFilter > 0)
          ? `<p class="text-muted" style="display:flex;gap:6px;align-items:center"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-funnel"/></svg>${hiddenByFilter} favorite${hiddenByFilter === 1 ? '' : 's'} not shown with the active region/area filter.</p>`
          : '';

        let active = 0, degraded = 0, silent = 0, clockOk = 0, totalRelay24 = 0;
        myNodes.forEach(n => {
          const s = statusOf(n); if (s === 'active') active++; else if (s === 'degraded') degraded++; else silent++;
          const cs = clockByPk[n.public_key];
          if (cs && cs.severity === 'ok') clockOk++;
          totalRelay24 += (n.relay_count_24h || 0);
        });

        const rank = { silent: 0, degraded: 1, active: 2 };
        const sorted = myNodes.slice().sort((a, b) => {
          const d = rank[statusOf(a)] - rank[statusOf(b)];
          return d !== 0 ? d : (a.name || '').localeCompare(b.name || '');
        });

        const rowsHtml = sorted.map(n => {
          const cs = clockByPk[n.public_key];
          const clockCell = cs ? renderSkewBadge(cs.severity, currentSkewValue(cs), cs) : '<span class="text-muted">—</span>';
          const battery = n.battery_mv != null ? (n.battery_mv / 1000).toFixed(2) + ' V' : '—';
          return `<tr>
            <td>${favStar(n.public_key)}</td>
            <td><a href="#/nodes/${encodeURIComponent(n.public_key)}/analytics" class="analytics-link">${esc(n.name || n.public_key.slice(0, 12))}</a></td>
            <td>${roleBadge(n.role)}</td>
            <td>${_mrStatusCell(statusOf(n))}</td>
            <td>${clockCell}</td>
            <td style="text-align:right">${pct(trafficOf(n))}</td>
            <td style="text-align:right">${pct(n.bridge_score)}</td>
            <td style="text-align:right">${n.relay_count_1h != null ? n.relay_count_1h : '—'}</td>
            <td style="text-align:right">${n.relay_count_24h != null ? n.relay_count_24h : '—'}</td>
            <td>${n.last_relayed ? timeAgo(n.last_relayed) : '—'}</td>
            <td>${(n.last_heard || n.last_seen) ? timeAgo(n.last_heard || n.last_seen) : '—'}</td>
            <td style="text-align:right">${battery}</td>
          </tr>`;
        }).join('');

        const nameByPk = {};
        myNodes.forEach(n => { nameByPk[n.public_key] = n.name || n.public_key.slice(0, 12); });
        const myEdges = ((graphData && graphData.edges) || [])
          .filter(e => myKeys.has(e.source) && myKeys.has(e.target))
          .sort((a, b) => (b.score || 0) - (a.score || 0));
        let affinityHtml;
        if (!myEdges.length) {
          affinityHtml = `<p class="text-muted" style="padding:8px 0">No affinity links detected between your repeaters yet — they may be too far apart, or not enough shared traffic has been observed.</p>`;
        } else {
          affinityHtml = `<table class="analytics-table">
            <thead><tr><th scope="col">Link</th><th scope="col" style="text-align:right">Affinity</th><th scope="col" style="text-align:right">Packets</th><th scope="col" style="text-align:right">Avg SNR</th></tr></thead>
            <tbody>${myEdges.map(e => {
              // The affinity graph is undirected — the server marks every edge
              // bidirectional — so the link glyph is always a double arrow.
              const arrow = '&harr;';
              const snr = (e.avg_snr != null) ? e.avg_snr.toFixed(1) + ' dB' : '—';
              return `<tr>
                <td>${esc(nameByPk[e.source] || e.source.slice(0, 12))} ${arrow} ${esc(nameByPk[e.target] || e.target.slice(0, 12))}${e.ambiguous ? ' <svg class="ph-icon" role="img" aria-label="Path attribution ambiguous"><title>Path attribution ambiguous</title><use href="/icons/phosphor-sprite.svg#ph-question"/></svg>' : ''}</td>
                <td style="text-align:right">${pct(e.score)}</td>
                <td style="text-align:right">${e.weight != null ? e.weight : '—'}</td>
                <td style="text-align:right">${snr}</td>
              </tr>`;
            }).join('')}</tbody>
          </table>`;
        }

        el.innerHTML = `
          <div class="analytics-section">
            <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-star-fill"/></svg> My Repeaters</h3>
            <p class="text-muted">At-a-glance monitoring over the repeaters you have starred. Un-star a row to drop it from this list.</p>
            ${filterNotice}
            <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(120px,1fr));gap:16px;margin-bottom:20px">
              ${_mrSummaryCard(myNodes.length, 'My Repeaters')}
              ${_mrSummaryCard(active, 'Active')}
              ${_mrSummaryCard(degraded, 'Degraded')}
              ${_mrSummaryCard(silent, 'Silent')}
              ${_mrSummaryCard(clockOk + ' / ' + myNodes.length, 'Clock OK')}
              ${_mrSummaryCard(totalRelay24.toLocaleString(), 'Relays (24h)')}
            </div>

            <table class="analytics-table" id="my-repeaters-table">
              <thead><tr>
                <th scope="col" aria-label="Favorite"></th>
                <th scope="col">Repeater</th>
                <th scope="col">Role</th>
                <th scope="col">Status</th>
                <th scope="col">Clock</th>
                <th scope="col" style="text-align:right">Traffic</th>
                <th scope="col" style="text-align:right">Bridge</th>
                <th scope="col" style="text-align:right" title="Relays in the last hour">Relays 1h</th>
                <th scope="col" style="text-align:right" title="Relays in the last 24 hours">Relays 24h</th>
                <th scope="col">Last Relayed</th>
                <th scope="col">Last Heard</th>
                <th scope="col" style="text-align:right">Battery</th>
              </tr></thead>
              <tbody>${rowsHtml || '<tr><td colspan="12" class="text-muted">None of your favorites are repeaters/rooms in this view.</td></tr>'}</tbody>
            </table>

            <h3 style="margin-top:28px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-graph"/></svg> Links Between My Repeaters</h3>
            <p class="text-muted">Affinity sub-graph filtered to your favorites — how much traffic flows directly between them.</p>
            ${affinityHtml}
          </div>`;

        // Live un-star: re-paint from cache (no refetch) when a star toggles.
        if (typeof bindFavStars === 'function') bindFavStars(el, paint);
      }
      paint();
    } catch (e) {
      el.innerHTML = `<div style="padding:40px;text-align:center;color:var(--status-red)">Failed to load your repeaters: ${esc(e.message)}</div>`;
    }
  }
  // === MY REPEATERS BLOCK END (test harness slices the renderer block to here) ===

  async function renderDistanceTab(el) {
    try {
      const rqs = RegionFilter.regionQueryString();
      const sep = rqs ? '?' + rqs.slice(1) : '';
      const data = await api('/analytics/distance' + sep, { ttl: CLIENT_TTL.analyticsRF });
      const s = data.summary;
      let html = `<div class="analytics-grid">
        <div class="stat-card"><div class="stat-value">${s.totalHops.toLocaleString()}</div><div class="stat-label">Total Hops Analyzed</div></div>
        <div class="stat-card"><div class="stat-value">${s.totalPaths.toLocaleString()}</div><div class="stat-label">Paths Analyzed</div></div>
        <div class="stat-card"><div class="stat-value">${formatDistance(s.avgDist)}</div><div class="stat-label">Avg Hop Distance</div></div>
        <div class="stat-card"><div class="stat-value">${formatDistance(s.maxDist)}</div><div class="stat-label">Max Hop Distance</div></div>
      </div>`;

      // Category stats
      const cats = data.catStats;
      const distUnitLabel = getDistanceUnit() === 'mi' ? 'mi' : 'km';
      html += `<div class="analytics-section"><h3>Distance by Link Type</h3><table class="data-table"><thead><tr><th scope="col">Type</th><th scope="col">Count</th><th scope="col">Avg (${distUnitLabel})</th><th scope="col">Median (${distUnitLabel})</th><th scope="col">Min (${distUnitLabel})</th><th scope="col">Max (${distUnitLabel})</th></tr></thead><tbody>`;
      for (const [cat, st] of Object.entries(cats)) {
        if (!st.count) continue;
        html += `<tr><td><strong>${esc(cat)}</strong></td><td>${st.count.toLocaleString()}</td><td>${formatDistance(st.avg)}</td><td>${formatDistance(st.median)}</td><td>${formatDistance(st.min)}</td><td>${formatDistance(st.max)}</td></tr>`;
      }
      html += `</tbody></table></div>`;

      // Histogram
      if (data.distHistogram && data.distHistogram.bins) {
        const buckets = data.distHistogram.bins.map(b => b.count);
        const labels = data.distHistogram.bins.map(b => b.x.toFixed(1));
        html += `<div class="analytics-section"><h3>Hop Distance Distribution</h3>${barChart(buckets, labels, statusGreen())}</div>`;
      }

      // Distance over time
      if (data.distOverTime && data.distOverTime.length > 1) {
        html += `<div class="analytics-section"><h3>Average Distance Over Time</h3>${sparkSvg(data.distOverTime.map(d => d.avg), 'var(--accent)', 800, 120)}</div>`;
      }

      // Top hops leaderboard
      html += `<div class="analytics-section"><h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-trophy"/></svg> Top 20 Longest Hops</h3><table class="data-table"><thead><tr><th scope="col">#</th><th scope="col">From</th><th scope="col">To</th><th scope="col">Distance (${distUnitLabel})</th><th scope="col">Type</th><th scope="col">Obs</th><th scope="col">Best SNR</th><th scope="col">Median SNR</th><th scope="col">Packet</th><th scope="col"></th></tr></thead><tbody>`;
      const top20 = data.topHops.slice(0, 20);
      top20.forEach((h, i) => {
        const fromLink = h.fromPk ? `<a href="#/nodes/${encodeURIComponent(h.fromPk)}" class="analytics-link">${esc(h.fromName)}</a>` : esc(h.fromName || '?');
        const toLink = h.toPk ? `<a href="#/nodes/${encodeURIComponent(h.toPk)}" class="analytics-link">${esc(h.toName)}</a>` : esc(h.toName || '?');
        const bestSnr = h.bestSnr != null ? Number(h.bestSnr).toFixed(1) + ' dB' : '<span class="text-muted">—</span>';
        const medianSnr = h.medianSnr != null ? Number(h.medianSnr).toFixed(1) + ' dB' : '<span class="text-muted">—</span>';
        const obs = h.obsCount != null ? h.obsCount : 1;
        const pktLink = h.hash ? `<a href="#/packet/${encodeURIComponent(h.hash)}" class="analytics-link mono" style="font-size:0.85em">${esc(h.hash.slice(0, 12))}…</a>` : '—';
        const mapBtn = h.fromPk && h.toPk ? `<button class="btn-icon dist-map-hop" data-from="${esc(h.fromPk)}" data-to="${esc(h.toPk)}" title="View on map" aria-label="View on map"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-trifold"/></svg></button>` : '';
        const tsTitle = h.timestamp ? `Best observation: ${h.timestamp}` : '';
        html += `<tr title="${esc(tsTitle)}"><td>${i+1}</td><td>${fromLink}</td><td>${toLink}</td><td><strong>${formatDistance(h.dist)}</strong></td><td>${esc(h.type)}</td><td>${obs}</td><td>${bestSnr}</td><td>${medianSnr}</td><td>${pktLink}</td><td>${mapBtn}</td></tr>`;
      });
      html += `</tbody></table></div>`;

      // Top paths
      if (data.topPaths.length) {
        html += `<div class="analytics-section"><h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-path"/></svg> Top 10 Longest Multi-Hop Paths</h3><table class="data-table"><thead><tr><th scope="col">#</th><th scope="col">Total Distance (${distUnitLabel})</th><th scope="col">Hops</th><th scope="col">Route</th><th scope="col">Packet</th><th scope="col"></th></tr></thead><tbody>`;
        data.topPaths.slice(0, 10).forEach((p, i) => {
          const route = p.hops.map(h => esc(h.fromName)).concat(esc(p.hops[p.hops.length-1].toName)).join(' → ');
          const pktLink = p.hash ? `<a href="#/packet/${encodeURIComponent(p.hash)}" class="analytics-link mono" style="font-size:0.85em">${esc(p.hash.slice(0, 12))}…</a>` : '—';
          // Collect all unique pubkeys in path order
          const pathPks = [];
          p.hops.forEach(h => { if (h.fromPk && !pathPks.includes(h.fromPk)) pathPks.push(h.fromPk); });
          if (p.hops.length && p.hops[p.hops.length-1].toPk) { const last = p.hops[p.hops.length-1].toPk; if (!pathPks.includes(last)) pathPks.push(last); }
          const mapBtn = pathPks.length >= 2 ? `<button class="btn-icon dist-map-path" data-hops='${JSON.stringify(pathPks)}' title="View on map" aria-label="View on map"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-trifold"/></svg></button>` : '';
          html += `<tr><td>${i+1}</td><td><strong>${formatDistance(p.totalDist)}</strong></td><td>${p.hopCount}</td><td style="font-size:0.9em">${route}</td><td>${pktLink}</td><td>${mapBtn}</td></tr>`;
        });
        html += `</tbody></table></div>`;
      }

      el.innerHTML = html;

      // Wire up map buttons
      el.querySelectorAll('.dist-map-hop').forEach(btn => {
        btn.addEventListener('click', () => {
          sessionStorage.setItem('map-route-hops', JSON.stringify({ hops: [btn.dataset.from, btn.dataset.to] }));
          window.location.hash = '#/map?route=1';
        });
      });
      el.querySelectorAll('.dist-map-path').forEach(btn => {
        btn.addEventListener('click', () => {
          try {
            const hops = JSON.parse(btn.dataset.hops);
            sessionStorage.setItem('map-route-hops', JSON.stringify({ hops }));
            window.location.hash = '#/map?route=1';
          } catch {}
        });
      });
    } catch (e) {
      el.innerHTML = `<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load distance analytics: ${esc(e.message)}</div>`;
    }
  }

function destroy() { _stopRolesRefresh(); _stopScopesRefresh(); _stopForeignTrafficRefresh(); _stopWardrivingRefresh(); _analyticsData = {}; _channelData = null; if (_ngState && _ngState.animId) { cancelAnimationFrame(_ngState.animId); } _ngState = null; if (_themeRefreshHandler) { window.removeEventListener('theme-refresh', _themeRefreshHandler); _themeRefreshHandler = null; } }

  // Expose for testing
  if (typeof window !== 'undefined') {
    window._analyticsDecorateChannels = decorateAnalyticsChannels;
    window._analyticsSortChannels = sortChannels;
    window._analyticsLoadChannelSort = loadChannelSort;
    window._analyticsSaveChannelSort = saveChannelSort;
    window._analyticsChannelTbodyHtml = channelTbodyHtml;
    window._analyticsChannelTheadHtml = channelTheadHtml;
    window._analyticsRfNFColumnChart = rfNFColumnChart;
    window._analyticsRenderMultiByteCapability = renderMultiByteCapability;
    window._analyticsRenderMultiByteAdopters = renderMultiByteAdopters;
    window._analyticsHashStatCardsHtml = hashStatCardsHtml;
    window._analyticsRenderCollisionsFromServer = renderCollisionsFromServer;
    window._analyticsRenderForeignTrafficTab = renderForeignTrafficTab;
    window._analyticsStopForeignTrafficRefresh = _stopForeignTrafficRefresh;
    window._analyticsRenderWardrivingTab = renderWardrivingTab;
    window._analyticsStopWardrivingRefresh = _stopWardrivingRefresh;
    window._analyticsComputeNodesWithoutScope = computeNodesWithoutScope;
    window._analyticsComputeRepeatersNeverRelayingScope = computeRepeatersNeverRelayingScope;
  }

  // ─── Neighbor Graph Tab ─────────────────────────────────────────────────────

  let _ngState = null; // neighbor graph state

  async function renderNeighborGraphTab(el) {
    el.innerHTML = `
      <div class="analytics-card" id="ngCard">
        <h3><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-graph"/></svg> Neighbor Graph</h3>
        <div id="ngFilters" class="ng-filters" style="display:flex;gap:12px;flex-wrap:wrap;align-items:center;margin-bottom:12px">
          <label style="font-size:13px">Roles:
            <span id="ngRoleChecks" style="margin-left:4px"></span>
          </label>
          <label style="font-size:13px">Min Score: <input type="range" id="ngMinScore" min="0" max="100" value="70" style="width:100px;vertical-align:middle">
            <span id="ngMinScoreVal">0.70</span>
          </label>
          <label style="font-size:13px">Confidence:
            <select id="ngConfidence" style="font-size:12px;padding:2px 4px">
              <option value="all">Show All</option>
              <option value="high">High Only</option>
              <option value="hide-ambiguous">Hide Ambiguous</option>
            </select>
          </label>
        </div>
        <div id="ngStats" class="stat-row" style="display:flex;gap:16px;flex-wrap:wrap;margin-bottom:12px"></div>
        <div style="position:relative;border:1px solid var(--border);border-radius:6px;overflow:hidden">
          <canvas id="ngCanvas" width="900" height="600" style="width:100%;height:600px;cursor:grab;outline-offset:2px" role="img" aria-label="Neighbor affinity graph visualization — interactive force-directed network topology" tabindex="0"></canvas>
          <div id="ngTooltip" style="position:absolute;display:none;background:var(--bg-secondary);border:1px solid var(--border);border-radius:4px;padding:6px 10px;font-size:12px;pointer-events:none;z-index:10;box-shadow:0 2px 8px rgba(0,0,0,0.2)"></div>
        </div>
        <details id="ngAccessibleList" style="margin-top:12px">
          <summary style="cursor:pointer;font-size:13px;color:var(--text-secondary)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clipboard-text"/></svg> Text-based neighbor list (accessible alternative)</summary>
          <div id="ngTextList" style="font-size:12px;max-height:300px;overflow-y:auto;padding:8px;background:var(--bg-secondary);border-radius:4px;margin-top:4px"></div>
        </details>
      </div>`;

    // Role checkboxes
    // #1715 — swatches use `.role-swatch--{role}` classes that read the
    // per-theme `--role-*` CSS tokens (public/style.css). The previous
    // inline `style="color:${ROLE_COLORS[r]}"` bypassed dark-theme
    // overrides and failed WCAG AA on #1a1a2e. Do NOT reintroduce inline
    // color hex here.
    const roles = ['repeater','companion','room','sensor'];
    const rcEl = document.getElementById('ngRoleChecks');
    roles.forEach(r => {
      rcEl.innerHTML += `<label style="font-size:12px;margin-right:8px"><input type="checkbox" data-role="${r}" checked> <span class="role-swatch role-swatch--${r}">${esc(r)}</span></label>`;
    });
    // Observer checkbox — unchecked by default (observers create hub-and-spoke noise)
    {
      rcEl.innerHTML += `<label style="font-size:12px;margin-right:8px"><input type="checkbox" data-role="observer"> <span class="role-swatch role-swatch--observer">observer</span></label>`;
    }

    // Load data
    const rqs = RegionFilter.regionQueryString();
    const sep = rqs ? '?' + rqs.slice(1) : '';
    let graphData;
    try {
      graphData = await api('/analytics/neighbor-graph' + sep + (sep ? '&' : '?') + 'min_count=1&min_score=0', { ttl: CLIENT_TTL.analyticsRF });
    } catch (e) {
      el.innerHTML = `<div class="analytics-card"><p class="text-muted">Failed to load neighbor graph: ${esc(e.message)}</p></div>`;
      return;
    }

    _ngState = createGraphState(graphData);
    // Render through the filter path so the first paint already respects the
    // default filters (observers unchecked, saved min-score) and the
    // node-count guard is evaluated against the displayed set, not the full
    // fetched graph.
    applyNGFilters();

    // Filter listeners
    // Restore saved min score from localStorage
    var savedScore = localStorage.getItem('ng-min-score');
    if (savedScore !== null) {
      document.getElementById('ngMinScore').value = savedScore;
      document.getElementById('ngMinScoreVal').textContent = (savedScore / 100).toFixed(2);
      applyNGFilters();
    }

    var ngScoreDebounce;
    document.getElementById('ngMinScore').addEventListener('input', function() {
      document.getElementById('ngMinScoreVal').textContent = (this.value / 100).toFixed(2);
      localStorage.setItem('ng-min-score', this.value);
      // Debounce the O(N+E) refilter so dragging the slider doesn't rebuild the
      // node set on every pixel (review of #1758).
      clearTimeout(ngScoreDebounce);
      ngScoreDebounce = setTimeout(applyNGFilters, 50);
    });
    document.getElementById('ngConfidence').addEventListener('change', applyNGFilters);
    rcEl.addEventListener('change', applyNGFilters);
  }

  function createGraphState(data) {
    const nodes = (data.nodes || []).map((n, i) => ({
      ...n,
      x: 450 + (Math.random() - 0.5) * 400,
      y: 300 + (Math.random() - 0.5) * 300,
      vx: 0, vy: 0,
      radius: Math.max(6, Math.min(18, 6 + (n.neighbor_count || 0)))
    }));
    const nodeIdx = {};
    nodes.forEach((n, i) => { nodeIdx[n.pubkey] = i; });
    const edges = (data.edges || []).filter(e => nodeIdx[e.source] !== undefined && nodeIdx[e.target] !== undefined);
    return {
      allNodes: nodes, allEdges: edges,
      nodes, edges, nodeIdx,
      stats: data.stats || {},
      zoom: 1, panX: 0, panY: 0,
      dragging: null, panning: false,
      hoverNode: null,
      lastMouseX: 0, lastMouseY: 0,
      cooling: 1.0, animId: null
    };
  }

  function applyNGFilters() {
    if (!_ngState) return;
    const minScore = parseInt(document.getElementById('ngMinScore').value, 10) / 100;
    const conf = document.getElementById('ngConfidence').value;
    const checkedRoles = new Set();
    document.querySelectorAll('#ngRoleChecks input:checked').forEach(cb => checkedRoles.add(cb.dataset.role));

    // Filter nodes by role
    const visibleNodes = _ngState.allNodes.filter(n => {
      const role = (n.role || 'unknown').toLowerCase();
      return checkedRoles.has(role) || role === 'unknown';
    });
    const visiblePKs = new Set(visibleNodes.map(n => n.pubkey));

    // Filter edges
    _ngState.edges = _ngState.allEdges.filter(e => {
      if (e.score < minScore) return false;
      if (conf === 'high' && (e.ambiguous || e.score < 0.5)) return false;
      if (conf === 'hide-ambiguous' && e.ambiguous) return false;
      return visiblePKs.has(e.source) && visiblePKs.has(e.target);
    });

    // Only include nodes that have at least one visible edge
    const edgeNodes = new Set();
    _ngState.edges.forEach(e => { edgeNodes.add(e.source); edgeNodes.add(e.target); });
    _ngState.nodes = visibleNodes.filter(n => edgeNodes.has(n.pubkey));

    // Rebuild index
    _ngState.nodeIdx = {};
    _ngState.nodes.forEach((n, i) => { _ngState.nodeIdx[n.pubkey] = i; });

    // A full re-anneal (cooling back to 1.0) on every filter change is
    // deliberate: the displayed node set just changed, so the prior layout
    // is stale and we want the simulation to settle the new graph. This is
    // acceptable at the current 1000-node limit; revisit if the limit grows.
    _ngState.cooling = 1.0;
    renderNGStats(_ngState);
    // Re-evaluate the node-count guard against the new filtered set and
    // (re)start or stop the force-simulation loop accordingly.
    startGraphRenderer();
  }

  function renderNGStats(st) {
    const nodes = st.nodes, edges = st.edges;
    const totalScore = edges.reduce((s, e) => s + e.score, 0);
    const avgScore = edges.length ? (totalScore / edges.length) : 0;
    const ambiguous = edges.filter(e => e.ambiguous).length;
    const resolved = edges.length ? ((edges.length - ambiguous) / edges.length * 100) : 0;
    const statsEl = document.getElementById('ngStats');
    if (!statsEl) return;
    statsEl.innerHTML = `
      <div class="stat-card"><div class="stat-value">${nodes.length}</div><div class="stat-label">Nodes</div></div>
      <div class="stat-card"><div class="stat-value">${edges.length}</div><div class="stat-label">Edges</div></div>
      <div class="stat-card"><div class="stat-value">${avgScore.toFixed(2)}</div><div class="stat-label">Avg Score</div></div>
      <div class="stat-card"><div class="stat-value">${resolved.toFixed(0)}%</div><div class="stat-label">Resolved</div></div>
      <div class="stat-card"><div class="stat-value">${ambiguous}</div><div class="stat-label">Ambiguous</div></div>`;

    // Update canvas aria-label with current graph summary
    var canvas = document.getElementById('ngCanvas');
    if (canvas) {
      canvas.setAttribute('aria-label', 'Neighbor affinity graph: ' + nodes.length + ' nodes, ' + edges.length + ' edges, ' + resolved.toFixed(0) + '% resolved. Use arrow keys to pan, +/- to zoom, 0 to reset.');
    }

    // Update accessible text list
    updateNGTextList(st);
  }

  function updateNGTextList(st) {
    var listEl = document.getElementById('ngTextList');
    if (!listEl) return;
    var nodes = st.nodes, edges = st.edges;
    if (nodes.length === 0) {
      listEl.innerHTML = '<p class="text-muted">No nodes to display.</p>';
      return;
    }
    // Build adjacency for text list
    var adj = {};
    edges.forEach(function(e) {
      if (!adj[e.source]) adj[e.source] = [];
      if (!adj[e.target]) adj[e.target] = [];
      adj[e.source].push({ pk: e.target, score: e.score, ambiguous: e.ambiguous });
      adj[e.target].push({ pk: e.source, score: e.score, ambiguous: e.ambiguous });
    });
    var nodeMap = {};
    nodes.forEach(function(n) { nodeMap[n.pubkey] = n; });
    var html = '<table style="width:100%;border-collapse:collapse"><thead><tr><th style="text-align:left;padding:4px;border-bottom:1px solid var(--border)">Node</th><th style="text-align:left;padding:4px;border-bottom:1px solid var(--border)">Role</th><th style="text-align:left;padding:4px;border-bottom:1px solid var(--border)">Neighbors</th></tr></thead><tbody>';
    nodes.slice().sort(function(a, b) { return (a.name || a.pubkey).localeCompare(b.name || b.pubkey); }).forEach(function(n) {
      var neighbors = (adj[n.pubkey] || []).map(function(nb) {
        var peer = nodeMap[nb.pk];
        var name = peer ? (peer.name || nb.pk.slice(0, 8)) : nb.pk.slice(0, 8);
        var conf = nb.ambiguous ? ' ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>' : (nb.score >= 0.5 ? ' ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg>' : ' ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle"/></svg>');
        return esc(name) + conf;
      }).join(', ');
      html += '<tr><td style="padding:4px;border-bottom:1px solid var(--border)">' + esc(n.name || n.pubkey.slice(0, 12)) + '</td><td style="padding:4px;border-bottom:1px solid var(--border)">' + esc(n.role || 'unknown') + '</td><td style="padding:4px;border-bottom:1px solid var(--border)">' + (neighbors || '<em>none</em>') + '</td></tr>';
    });
    html += '</tbody></table>';
    html += '<p style="margin-top:8px;font-size:11px;color:var(--text-secondary)">' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg>' + ' = high confidence (score ≥ 0.5), ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle"/></svg>' + ' = low confidence, ' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>' + ' = ambiguous/unresolved</p>';
    listEl.innerHTML = html;
  }

  function bindNGCanvasInteraction(canvas) {
    function canvasToGraph(cx, cy) {
      return { x: (cx - _ngState.panX) / _ngState.zoom, y: (cy - _ngState.panY) / _ngState.zoom };
    }

    function findNode(cx, cy) {
      const gp = canvasToGraph(cx, cy);
      for (let i = _ngState.nodes.length - 1; i >= 0; i--) {
        const n = _ngState.nodes[i];
        const dx = gp.x - n.x, dy = gp.y - n.y;
        if (dx * dx + dy * dy <= n.radius * n.radius) return n;
      }
      return null;
    }

    canvas.addEventListener('mousedown', function(e) {
      const rect = canvas.getBoundingClientRect();
      const cx = e.clientX - rect.left, cy = e.clientY - rect.top;
      const n = findNode(cx, cy);
      if (n) {
        _ngState.dragging = n;
        n._pinned = true;
        canvas.style.cursor = 'grabbing';
      } else {
        _ngState.panning = true;
        canvas.style.cursor = 'grabbing';
      }
      _ngState.lastMouseX = e.clientX;
      _ngState.lastMouseY = e.clientY;
    });

    canvas.addEventListener('mousemove', function(e) {
      const rect = canvas.getBoundingClientRect();
      const cx = e.clientX - rect.left, cy = e.clientY - rect.top;
      if (_ngState.dragging) {
        const dx = (e.clientX - _ngState.lastMouseX) / _ngState.zoom;
        const dy = (e.clientY - _ngState.lastMouseY) / _ngState.zoom;
        _ngState.dragging.x += dx;
        _ngState.dragging.y += dy;
        _ngState.lastMouseX = e.clientX;
        _ngState.lastMouseY = e.clientY;
        _ngState.cooling = Math.max(_ngState.cooling, 0.3);
      } else if (_ngState.panning) {
        _ngState.panX += e.clientX - _ngState.lastMouseX;
        _ngState.panY += e.clientY - _ngState.lastMouseY;
        _ngState.lastMouseX = e.clientX;
        _ngState.lastMouseY = e.clientY;
      } else {
        const n = findNode(cx, cy);
        if (n !== _ngState.hoverNode) {
          _ngState.hoverNode = n;
          canvas.style.cursor = n ? 'pointer' : 'grab';
          const tip = document.getElementById('ngTooltip');
          if (n && tip) {
            tip.style.display = 'block';
            tip.style.left = (cx + 12) + 'px';
            tip.style.top = (cy - 8) + 'px';
            tip.innerHTML = `<strong>${escapeHtml(n.name || n.pubkey.slice(0, 12) + '…')}</strong><br>Role: ${escapeHtml(n.role || 'unknown')}<br>Neighbors: ${n.neighbor_count || 0}`;
          } else if (tip) {
            tip.style.display = 'none';
          }
        } else if (_ngState.hoverNode) {
          const tip = document.getElementById('ngTooltip');
          if (tip) { tip.style.left = (cx + 12) + 'px'; tip.style.top = (cy - 8) + 'px'; }
        }
      }
    });

    canvas.addEventListener('mouseup', function() {
      if (_ngState.dragging) {
        _ngState.dragging._pinned = false;
        _ngState._wasDragging = true;
      }
      _ngState.dragging = null;
      _ngState.panning = false;
      canvas.style.cursor = _ngState.hoverNode ? 'pointer' : 'grab';
    });

    canvas.addEventListener('mouseleave', function() {
      _ngState.dragging = null;
      _ngState.panning = false;
      _ngState._wasDragging = false;
      const tip = document.getElementById('ngTooltip');
      if (tip) tip.style.display = 'none';
      _ngState.hoverNode = null;
    });

    canvas.addEventListener('click', function(e) {
      if (_ngState._wasDragging) { _ngState._wasDragging = false; return; }
      if (_ngState.dragging) return;
      const rect = canvas.getBoundingClientRect();
      const n = findNode(e.clientX - rect.left, e.clientY - rect.top);
      if (n) location.hash = '#/nodes/' + n.pubkey;
    });

    canvas.addEventListener('keydown', function(e) {
      const PAN_STEP = 30, ZOOM_STEP = 1.15;
      switch (e.key) {
        case 'ArrowLeft':  _ngState.panX += PAN_STEP; e.preventDefault(); break;
        case 'ArrowRight': _ngState.panX -= PAN_STEP; e.preventDefault(); break;
        case 'ArrowUp':    _ngState.panY += PAN_STEP; e.preventDefault(); break;
        case 'ArrowDown':  _ngState.panY -= PAN_STEP; e.preventDefault(); break;
        case '+': case '=': _ngState.zoom = Math.min(10, _ngState.zoom * ZOOM_STEP); e.preventDefault(); break;
        case '-': case '_': _ngState.zoom = Math.max(0.1, _ngState.zoom / ZOOM_STEP); e.preventDefault(); break;
        case '0':           _ngState.zoom = 1; _ngState.panX = 0; _ngState.panY = 0; e.preventDefault(); break;
      }
    });

    canvas.addEventListener('wheel', function(e) {
      e.preventDefault();
      const rect = canvas.getBoundingClientRect();
      const cx = e.clientX - rect.left, cy = e.clientY - rect.top;
      const factor = e.deltaY < 0 ? 1.1 : 0.9;
      const newZoom = Math.max(0.1, Math.min(10, _ngState.zoom * factor));
      // Zoom towards mouse position
      _ngState.panX = cx - (cx - _ngState.panX) * (newZoom / _ngState.zoom);
      _ngState.panY = cy - (cy - _ngState.panY) * (newZoom / _ngState.zoom);
      _ngState.zoom = newZoom;
    }, { passive: false });
  }

  function startGraphRenderer() {
    if (!_ngState) return;

    // Re-entrant: the filter path calls this to restart against the new node
    // set, so cancel any loop already running before deciding what to do.
    if (_ngState.animId) {
      cancelAnimationFrame(_ngState.animId);
      _ngState.animId = null;
    }
    // Re-entrancy epoch: a just-cancelled in-flight tick() (or one re-entered
    // from a microtask) must not schedule a second loop. Each run bumps the
    // epoch; tick() captures its own and early-returns once a newer run starts.
    _ngState._runEpoch = (_ngState._runEpoch || 0) + 1;
    var myEpoch = _ngState._runEpoch;

    const canvas = document.getElementById('ngCanvas');
    if (!canvas) return;
    var skipMsg = document.getElementById('ngSkipMsg');

    // Node count guard: skip the force simulation for graphs too large to
    // render usefully. Keyed off the DISPLAYED (filtered) set — _ngState.nodes
    // — NOT the full fetched graph (_ngState.allNodes), so narrowing the
    // filters re-enables rendering. The "skipped" notice has a stable id so
    // it can be toggled on the next call.
    var NODE_LIMIT = 1000;
    if (_ngState.nodes.length > NODE_LIMIT) {
      canvas.style.display = 'none';
      if (!skipMsg) {
        skipMsg = document.createElement('div');
        skipMsg.id = 'ngSkipMsg';
        skipMsg.className = 'analytics-card';
        canvas.parentNode.insertBefore(skipMsg, canvas);
      }
      // Build via textContent (never innerHTML) per the project's XSS rule —
      // these are Numbers today, but a future coercion bug must not inject. Show
      // filtered-of-total so the operator sees the rest were filtered out, not
      // lost (review of #1758).
      var ngTotal = (_ngState.allNodes || _ngState.nodes).length;
      skipMsg.textContent = '';
      var skipP = document.createElement('p');
      skipP.className = 'text-muted';
      skipP.textContent = _ngState.nodes.length + ' of ' + ngTotal + ' nodes match the current filters (limit: ' + NODE_LIMIT + '). Force simulation skipped for performance — tighten the filters to render.';
      skipMsg.appendChild(skipP);
      return;
    }
    if (skipMsg) skipMsg.remove();
    canvas.style.display = '';
    const ctx = canvas.getContext('2d');
    const dpr = window.devicePixelRatio || 1;
    canvas.width = canvas.clientWidth * dpr;
    canvas.height = canvas.clientHeight * dpr;
    ctx.scale(dpr, dpr);
    const W = canvas.clientWidth, H = canvas.clientHeight;

    // Interaction listeners are bound once per tab session (the #ngCanvas
    // element and _ngState persist across filter re-renders); re-attaching on
    // every applyNGFilters() would leak handlers (review of #1758). Binding
    // once also means transient flags like _wasDragging survive re-renders.
    // Hover state lives on _ngState so listeners and the draw loop agree.
    if (!_ngState._listenersBound) {
      bindNGCanvasInteraction(canvas);
      _ngState._listenersBound = true;
    }

    // Cache text color to avoid getComputedStyle every frame
    const _labelColor = cssVar('--text-primary') || '#e0e0e0';

    // Force simulation + render loop
    // Performance: 500 nodes brute-force repulsion: avg ~4ms/frame = 250fps headroom (measured Chrome 120, M1)
    var _perfFrameTimes = [], _perfLastTime = 0;
    function tick() {
      if (_ngState._runEpoch !== myEpoch) { return; } // a newer startGraphRenderer run superseded this loop
      if (!document.getElementById('ngCanvas')) { _ngState.animId = null; return; }
      var now = performance.now();
      if (_perfLastTime) _perfFrameTimes.push(now - _perfLastTime);
      _perfLastTime = now;
      if (_perfFrameTimes.length === 100) {
        var avg = _perfFrameTimes.reduce(function(a, b) { return a + b; }, 0) / 100;
        console.log('[NeighborGraph perf] avg frame time over 100 frames: ' + avg.toFixed(2) + 'ms (' + (1000 / avg).toFixed(0) + ' fps)');
        _perfFrameTimes = [];
      }
      const st = _ngState;
      const nodes = st.nodes, edges = st.edges, idx = st.nodeIdx;

      if (st.cooling > 0.001) {
        // Repulsion (all pairs — use grid for large sets, brute force for small)
        const k = 80; // repulsion constant
        for (let i = 0; i < nodes.length; i++) {
          for (let j = i + 1; j < nodes.length; j++) {
            let dx = nodes[j].x - nodes[i].x;
            let dy = nodes[j].y - nodes[i].y;
            let d2 = dx * dx + dy * dy;
            if (d2 < 1) { dx = Math.random() - 0.5; dy = Math.random() - 0.5; d2 = 1; }
            const f = k * k / d2;
            const fx = dx / Math.sqrt(d2) * f;
            const fy = dy / Math.sqrt(d2) * f;
            nodes[i].vx -= fx; nodes[i].vy -= fy;
            nodes[j].vx += fx; nodes[j].vy += fy;
          }
        }

        // Attraction along edges
        const idealLen = 120;
        for (const e of edges) {
          const si = idx[e.source], ti = idx[e.target];
          if (si === undefined || ti === undefined) continue;
          const a = nodes[si], b = nodes[ti];
          let dx = b.x - a.x, dy = b.y - a.y;
          const d = Math.sqrt(dx * dx + dy * dy) || 1;
          const f = (d - idealLen) * 0.05 * (0.5 + e.score * 0.5);
          const fx = dx / d * f, fy = dy / d * f;
          a.vx += fx; a.vy += fy;
          b.vx -= fx; b.vy -= fy;
        }

        // Center gravity
        for (const n of nodes) {
          n.vx += (W / 2 - n.x) * 0.001;
          n.vy += (H / 2 - n.y) * 0.001;
        }

        // Apply velocities with damping
        const damping = 0.85;
        for (const n of nodes) {
          if (n._pinned) { n.vx = 0; n.vy = 0; continue; }
          n.vx *= damping * st.cooling;
          n.vy *= damping * st.cooling;
          const speed = Math.sqrt(n.vx * n.vx + n.vy * n.vy);
          if (speed > 10) { n.vx *= 10 / speed; n.vy *= 10 / speed; }
          n.x += n.vx;
          n.y += n.vy;
        }
        st.cooling *= 0.995;
      }

      // Render
      ctx.save();
      ctx.clearRect(0, 0, W, H);
      ctx.translate(st.panX, st.panY);
      ctx.scale(st.zoom, st.zoom);

      // Edges
      for (const e of edges) {
        const si = idx[e.source], ti = idx[e.target];
        if (si === undefined || ti === undefined) continue;
        const a = nodes[si], b = nodes[ti];
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        ctx.strokeStyle = e.ambiguous ? 'rgba(255,200,0,0.4)' : 'rgba(150,150,150,0.35)';
        ctx.lineWidth = Math.max(0.5, e.score * 4);
        if (e.ambiguous) { ctx.setLineDash([4, 4]); } else { ctx.setLineDash([]); }
        ctx.stroke();
        ctx.setLineDash([]);
      }

      // Nodes
      const roleColors = window.ROLE_COLORS || {};
      for (const n of nodes) {
        const color = roleColors[(n.role || '').toLowerCase()] || '#6b7280';
        ctx.beginPath();
        ctx.arc(n.x, n.y, n.radius, 0, Math.PI * 2);
        ctx.fillStyle = color;
        ctx.fill();
        if (n === _ngState.hoverNode) {
          ctx.strokeStyle = '#fff';
          ctx.lineWidth = 2;
          ctx.stroke();
        }
        // Label
        const label = n.name || (n.pubkey ? n.pubkey.slice(0, 8) + '…' : '');
        if (label && st.zoom > 0.4) {
          ctx.fillStyle = _labelColor;
          ctx.font = '10px sans-serif';
          ctx.textAlign = 'center';
          ctx.fillText(label, n.x, n.y + n.radius + 12);
        }
      }

      ctx.restore();
      st.animId = requestAnimationFrame(tick);
    }

    _ngState.animId = requestAnimationFrame(tick);
  }

  // --- Prefix Tool ---
  async function renderPrefixTool(el) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading prefix data…</div>';

    const rq = RegionFilter.regionQueryString() + AreaFilter.areaQueryString();
    const regionLabel = rq ? (new URLSearchParams(rq.slice(1)).get('region') || '') : '';

    let nodesResp, hashSizesResp;
    try {
      [nodesResp, hashSizesResp] = await Promise.all([
        fetchAllNodes('&sortBy=lastSeen' + rq, { ttl: CLIENT_TTL.nodeList }),
        // #1270: fetch CONFIGURED-hash-size counts so the Network Overview
        // tells the operational story (matching Hash Stats "By Repeaters"),
        // not just a math-only count of unique pubkey slices.
        api('/analytics/hash-sizes' + rq, { ttl: CLIENT_TTL.analyticsRF }).catch(() => null),
      ]);
    } catch (e) {
      el.innerHTML = `<div class="text-muted" role="alert" style="padding:40px">Failed to load: ${esc(e.message)}</div>`;
      return;
    }

    // Deduplicate by public_key, require at least 6 hex chars to build all 3 tiers
    const nodeMap = new Map();
    (nodesResp.nodes || nodesResp).forEach(n => {
      if (n.public_key && n.public_key.length >= 6 && !nodeMap.has(n.public_key)) {
        nodeMap.set(n.public_key, n);
      }
    });
    const allNodes = [...nodeMap.values()];
    // Only repeaters matter for prefix collisions — they relay packets using hash prefixes.
    // Companions, rooms, and sensors don't route, so their prefix collisions are harmless.
    const nodes = allNodes.filter(n => n.role === 'repeater');

    if (nodes.length === 0) {
      el.innerHTML = `<div class="analytics-card"><p class="text-muted">No repeaters in the network yet. Any prefix is available!</p></div>`;
      return;
    }

    // Build 3-tier prefix indexes: prefix (uppercase hex) -> [nodes]
    const idx = { 1: new Map(), 2: new Map(), 3: new Map() };
    nodes.forEach(n => {
      const pk = n.public_key.toUpperCase();
      [1, 2, 3].forEach(b => {
        const p = pk.slice(0, b * 2);
        if (!idx[b].has(p)) idx[b].set(p, []);
        idx[b].get(p).push(n);
      });
    });

    // Network overview stats
    const spaceSizes = { 1: 256, 2: 65536, 3: 16777216 };
    const stats = {};
    [1, 2, 3].forEach(b => {
      stats[b] = {
        usedPrefixes: idx[b].size,
        collidingPrefixes: [...idx[b].values()].filter(arr => arr.length > 1).length,
      };
    });

    // #1270: CONFIGURED-hash-size counts (operational truth) from
    // /api/analytics/hash-sizes — same source the Hash Stats tab uses.
    // distributionByRepeaters keys are stringified ints ("1","2","3").
    // "0" = no adverts observed yet, so the configured size is unknown
    // and that node doesn't count for any tier.
    const distByRepeaters = (hashSizesResp && hashSizesResp.distributionByRepeaters) || {};
    const configuredCount = {
      1: Number(distByRepeaters['1'] || 0),
      2: Number(distByRepeaters['2'] || 0),
      3: Number(distByRepeaters['3'] || 0),
    };
    const totalConfigured = configuredCount[1] + configuredCount[2] + configuredCount[3];

    // Operational collisions per tier: only consider repeaters CONFIGURED
    // for this hash size — same definition the Hash Issues tab uses.
    // n.hash_size is enriched on the /nodes payload from GetNodeHashSizeInfo.
    // #1306: keep the per-tier *node lists* per colliding slice so we can
    // surface WHICH prefixes/nodes collide (not just an aggregate count).
    const opCollisions = { 1: 0, 2: 0, 3: 0 };
    const opIdx = { 1: new Map(), 2: new Map(), 3: new Map() };
    [1, 2, 3].forEach(b => {
      nodes.forEach(n => {
        if (n.hash_size !== b) return;
        const p = n.public_key.toUpperCase().slice(0, b * 2);
        if (!opIdx[b].has(p)) opIdx[b].set(p, []);
        opIdx[b].get(p).push(n);
      });
      opCollisions[b] = [...opIdx[b].values()].filter(arr => arr.length > 1).length;
    });

    // #1306: precompute colliding slices per tier (entries with >1 node) for
    // the "Show N colliding slices →" expandable lists. THEORETICAL = across
    // every repeater's pubkey prefix (math fact). OPERATIONAL = only among
    // repeaters configured for this hash size (matches Hash Issues tab math).
    const collEntries = { theoretical: { 1: [], 2: [], 3: [] }, operational: { 1: [], 2: [], 3: [] } };
    [1, 2, 3].forEach(b => {
      collEntries.theoretical[b] = [...idx[b].entries()]
        .filter(([, arr]) => arr.length > 1)
        .sort((a, b2) => b2[1].length - a[1].length || a[0].localeCompare(b2[0]));
      collEntries.operational[b] = [...opIdx[b].entries()]
        .filter(([, arr]) => arr.length > 1)
        .sort((a, b2) => b2[1].length - a[1].length || a[0].localeCompare(b2[0]));
    });

    // #1306: render a "Prefix · Nodes sharing" table for a list of
    // colliding entries `[ [prefix, [node, ...]], ... ]`.
    function renderCollideTable(entries) {
      if (!entries || !entries.length) {
        return '<div class="text-muted" style="padding:8px;font-size:0.8em">No collisions.</div>';
      }
      const rows = entries.map(([prefix, arr]) => {
        const nodeLinks = arr.map(n => {
          const label = esc(n.name || n.public_key.slice(0, 10));
          return `<a href="#/nodes/${encodeURIComponent(n.public_key)}" style="color:var(--link-color);text-decoration:none">${label}</a>`;
        }).join(', ');
        return `<tr>
          <td style="padding:4px 8px;font-family:var(--mono);font-weight:600;border-bottom:1px solid var(--border);white-space:nowrap">${esc(prefix)}</td>
          <td style="padding:4px 8px;font-size:0.85em;border-bottom:1px solid var(--border)">${nodeLinks} <span class="text-muted" style="font-size:0.85em">(${arr.length})</span></td>
        </tr>`;
      }).join('');
      return `<table style="width:100%;border-collapse:collapse;font-size:0.85em">
        <thead><tr>
          <th scope="col" style="text-align:left;padding:4px 8px;border-bottom:1px solid var(--border);background:var(--bg-secondary,var(--bg))">Prefix</th>
          <th scope="col" style="text-align:left;padding:4px 8px;border-bottom:1px solid var(--border);background:var(--bg-secondary,var(--bg))">Nodes sharing it</th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>`;
    }

    // Recommendation by network size
    const totalNodes = nodes.length;
    let rec, recDetail;
    if (totalNodes < 20) {
      rec = '1-byte'; recDetail = `With only ${totalNodes} repeaters, 1-byte prefixes have low collision risk.`;
    } else if (totalNodes < 500) {
      rec = '2-byte'; recDetail = `With ${totalNodes} repeaters, 2-byte prefixes are recommended to avoid collisions.`;
    } else {
      rec = '2-byte'; recDetail = `With ${totalNodes} repeaters, 2-byte prefixes are strongly recommended.`;
    }

    // URL params for pre-fill / auto-run
    const hashParams = new URLSearchParams((location.hash.split('?')[1] || ''));
    const initPrefix = hashParams.get('prefix') || '';
    const initGenerate = hashParams.get('generate') || '';

    const regionNote = regionLabel
      ? `<p class="text-muted" style="font-size:0.85em;margin:4px 0 0">Showing data for region: <strong>${esc(regionLabel)}</strong>. <a href="#/analytics?tab=prefix-tool" style="color:var(--link-color)">Check all repeaters →</a></p>`
      : '';

    el.innerHTML = `
      <div class="analytics-card" id="ptOverview">
        <div style="display:flex;align-items:center;gap:8px;cursor:pointer;user-select:none" id="ptOverviewToggle">
          <span id="ptOverviewChevron" class="pt-overview-chevron" style="color:var(--text-muted);transition:transform 0.2s;display:inline-flex;align-items:center"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-right"/></svg></span>
          <h3 style="margin:0">Network Overview</h3>
        </div>
        <div id="ptOverviewBody" style="display:none">
          ${regionNote}
          <div style="display:flex;gap:12px;flex-wrap:wrap;margin:12px 0 16px">
            <div class="analytics-stat-card" style="flex:1;min-width:110px">
              <div class="analytics-stat-label">Total repeaters</div>
              <div class="analytics-stat-value">${totalNodes.toLocaleString()}</div>
              <div class="text-muted" style="font-size:0.78em;margin-top:4px">
                ${totalConfigured.toLocaleString()} with known hash size
              </div>
            </div>
            ${[1, 2, 3].map(b => {
              // #1270: PRIMARY = configured-for-this-size repeater count
              // (operational truth, matches Hash Stats "By Repeaters").
              // SECONDARY = math fact (unique pubkey slices). OPERATIONAL
              // address conflicts = colliding slices among configured-for-this-size repeaters only.
              const cfg = configuredCount[b];
              const opC = opCollisions[b];
              const theoC = stats[b].collidingPrefixes;
              const borderVar = opC > 0 ? 'var(--status-red)' : 'var(--border)';
              // #1306: replace bare "collisions" with "address conflicts" to
              // distinguish theoretical/would-collide-if-used from packet-
              // traffic-observed collisions shown on the Hash Issues tab.
              const opLine = opC === 0
                ? `<span style="color:var(--status-green-text)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No address conflicts among configured repeaters</span>`
                : `<span style="color:var(--status-red)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> ${opC} address conflict${opC !== 1 ? 's' : ''} among configured repeaters (would-collide-if-used)</span>`;
              // #1306: expandable WHICH-collides toggles (op + theoretical)
              const opEntries = collEntries.operational[b];
              const theoEntries = collEntries.theoretical[b];
              const opTogId = `ptCollOp${b}`;
              const theoTogId = `ptCollTheo${b}`;
              const opToggle = opC > 0 ? `
                <div style="margin-top:6px">
                  <button type="button" class="pt-collide-toggle-btn" data-pt-collide-toggle="op-${b}" data-target="${opTogId}"
                    aria-expanded="false"
                    style="background:none;border:none;color:var(--link-color);cursor:pointer;font-size:0.82em;padding:0">
                    Show ${opC} colliding slice${opC !== 1 ? 's' : ''} →
                  </button>
                  <div id="${opTogId}" data-pt-collide-panel="op-${b}" style="display:none;margin-top:6px;max-height:400px;overflow-y:auto;border:1px solid var(--border);border-radius:4px;background:var(--bg)">
                    ${renderCollideTable(opEntries)}
                  </div>
                </div>` : '';
              const theoToggle = theoC > 0 ? `
                <div style="margin-top:4px">
                  <button type="button" class="pt-collide-toggle-btn" data-pt-collide-toggle="theo-${b}" data-target="${theoTogId}"
                    aria-expanded="false"
                    style="background:none;border:none;color:var(--link-color);cursor:pointer;font-size:0.78em;padding:0">
                    Show ${theoC} would-collide slice${theoC !== 1 ? 's' : ''} (across all repeaters) →
                  </button>
                  <div id="${theoTogId}" data-pt-collide-panel="theo-${b}" style="display:none;margin-top:6px;max-height:400px;overflow-y:auto;border:1px solid var(--border);border-radius:4px;background:var(--bg)">
                    ${renderCollideTable(theoEntries)}
                  </div>
                </div>` : '';
              return `
            <div class="analytics-stat-card" style="flex:1;min-width:170px;border-color:${borderVar}">
              <div class="analytics-stat-label">${b}-byte prefixes</div>
              <div class="analytics-stat-value" style="font-size:1em"
                   data-pt-configured="${b}" data-value="${cfg}">
                ${cfg.toLocaleString()}
                <span class="text-muted" style="font-size:0.7em"> of ${totalNodes.toLocaleString()} repeaters configured</span>
              </div>
              <div style="font-size:0.82em;margin-top:4px">${opLine}</div>
              ${opToggle}
              <div class="text-muted" style="font-size:0.72em;margin-top:6px;border-top:1px dashed var(--border);padding-top:4px">
                <em>Theoretical:</em> ${stats[b].usedPrefixes.toLocaleString()} unique ${b}-byte slice${stats[b].usedPrefixes !== 1 ? 's' : ''}
                across all repeater pubkeys (of ${spaceSizes[b].toLocaleString()} possible)${theoC > 0 ? ` — ${theoC} would-collide if every repeater used ${b}-byte` : ''}
              </div>
              ${theoToggle}
            </div>`;
            }).join('')}
          </div>
          <div style="background:var(--bg-secondary,var(--bg));border:1px solid var(--accent);border-radius:6px;padding:10px 14px;margin-bottom:12px;font-size:0.85em">
            <strong>ℹ️ Theoretical vs observed:</strong> These are <em>theoretical address conflicts</em> that would occur IF all repeaters used this hash size (would-collide-if-used). For collisions <em>actually observed in packet traffic</em>, see the <a href="#/analytics?tab=collisions" style="color:var(--link-color)">Hash Issues</a> tab.
          </div>
          <div style="background:var(--bg-secondary,var(--bg));border:1px solid var(--border);border-radius:6px;padding:10px 14px;margin-bottom:12px">
            <strong>Recommendation: ${rec} prefixes</strong> — ${recDetail}
            <span class="text-muted" style="font-size:0.8em;display:block;margin-top:4px">Hash size is configured per-node in firmware. Changing requires reflashing.</span>
          </div>
          <div style="background:var(--bg-secondary,var(--bg));border:1px solid var(--border);border-radius:6px;padding:10px 14px;font-size:0.85em">
            <strong>ℹ️ About these numbers:</strong> The primary count is how many repeaters are <em>configured</em> for each hash size (their advertised path hash byte length), matching the
            <a href="#/analytics?tab=hashsizes" style="color:var(--link-color)">Hash Stats</a> tab. Address conflicts (would-collide-if-used) count colliding slices among repeaters configured for the same hash size — same definition the
            <a href="#/analytics?tab=collisions" style="color:var(--link-color)">Hash Issues</a> tab uses, except Hash Issues counts collisions <em>actually observed in packet traffic</em> rather than theoretical. The <em>theoretical</em> line shows the math fact: how many distinct slices appear when every repeater pubkey is truncated to N bytes, regardless of configured hash size.
          </div>
        </div>
      </div>

      <div class="analytics-card" id="ptChecker">
        <h3 style="margin-top:0">Check a Prefix</h3>
        <p class="text-muted" style="margin-top:0;font-size:0.9em">Enter a 1-byte (2 hex chars), 2-byte (4 hex chars), or 3-byte (6 hex chars) prefix — or paste a full public key.</p>
        <div style="display:flex;gap:8px;align-items:flex-start;flex-wrap:wrap">
          <input id="ptPrefixInput" type="text" placeholder="e.g. A3F1" maxlength="64"
            style="font-family:var(--mono);font-size:1em;padding:6px 10px;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:4px;min-width:180px;flex:1"
            value="${esc(initPrefix)}">
          <button id="ptCheckBtn" class="btn-active-accent" style="padding:6px 16px;border:none;border-radius:4px;cursor:pointer;font-size:0.95em">Check</button>
        </div>
        <div id="ptCheckerResults" style="margin-top:14px"></div>
      </div>

      <div class="analytics-card" id="ptGenerator">
        <h3 style="margin-top:0">Generate Available Prefix</h3>
        <p class="text-muted" style="margin-top:0;font-size:0.9em">Find a prefix with zero current collisions.</p>
        <p class="text-muted" style="margin:4px 0 10px;font-size:0.82em">
          <span aria-hidden="true"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-prohibit"/></svg></span>
          <strong>0x00 and 0xFF excluded</strong> as a first byte — the MeshCore firmware keygen routine re-rolls identities whose <code>pub_key[0]</code> is <code>00</code> or <code>FF</code>, so by convention you should not see those prefixes on real nodes (see
          <a href="https://github.com/meshcore-dev/MeshCore/blob/8ede7641/examples/simple_repeater/main.cpp#L83"
             target="_blank" rel="noopener noreferrer" style="color:var(--link-color)">simple_repeater/main.cpp:83</a>).
        </p>
        <div style="display:flex;gap:16px;align-items:center;flex-wrap:wrap;margin-bottom:12px">
          <label style="display:flex;align-items:center;gap:6px;cursor:pointer">
            <input type="radio" name="ptGenSize" value="1" ${initGenerate === '1' ? 'checked' : ''}> 1-byte
          </label>
          <label style="display:flex;align-items:center;gap:6px;cursor:pointer">
            <input type="radio" name="ptGenSize" value="2" ${initGenerate !== '1' && initGenerate !== '3' ? 'checked' : ''}> 2-byte
            <span class="text-muted" style="font-size:0.8em">(recommended)</span>
          </label>
          <label style="display:flex;align-items:center;gap:6px;cursor:pointer">
            <input type="radio" name="ptGenSize" value="3" ${initGenerate === '3' ? 'checked' : ''}> 3-byte
          </label>
          <button id="ptGenBtn" class="btn-active-accent" style="padding:6px 16px;border:none;border-radius:4px;cursor:pointer;font-size:0.95em">Generate</button>
        </div>
        <div id="ptGenResult"></div>
        <div style="margin-top:14px;padding:10px 14px;border:1px solid var(--accent);border-radius:6px;background:var(--bg-secondary,var(--bg));font-size:0.88em">
          <svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-book-open"/></svg> <strong>New to multi-byte prefixes?</strong>
          <a href="https://github.com/meshcore-dev/MeshCore/blob/main/docs/faq.md#39-q-what-is-multi-byte-support--what-do-1-byte-2-byte-3-byte-adverts-and-messages-mean"
            target="_blank" rel="noopener noreferrer" style="color:var(--link-color);margin-left:4px">
            Read the MeshCore FAQ on multi-byte support →
          </a>
        </div>
      </div>`;

    // --- Helpers ---
    function nodeEntry(n) {
      const name = esc(n.name || n.public_key.slice(0, 12));
      const role = n.role ? `<span class="text-muted" style="font-size:0.82em">${esc(n.role)}</span>` : '';
      const hs = n.hash_size ? ` <span class="text-muted" style="font-size:0.78em;opacity:0.7">${n.hash_size}B hash</span>` : '';
      const when = n.last_seen ? ` <span class="text-muted" style="font-size:0.8em">${(typeof formatAbsoluteTimestamp === 'function') ? formatAbsoluteTimestamp(n.last_seen) : new Date(n.last_seen).toLocaleDateString()}</span>` : '';
      return `<div style="padding:3px 0"><a href="#/nodes/${encodeURIComponent(n.public_key)}" class="analytics-link">${name}</a> ${role}${hs}${when}</div>`;
    }

    function severityBadge(count) {
      if (count === 0) return '<span style="color:var(--status-green-text)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> Unique</span>';
      if (count <= 2) return `<span style="color:var(--status-yellow)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> ${count} collision${count !== 1 ? 's' : ''}</span>`;
      return `<span style="color:var(--status-red)"><span style="color:var(--status-red)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle-fill"/></svg></span> ${count} collisions</span>`;
    }

    // --- Checker ---
    function doCheck(raw) {
      const resultsEl = document.getElementById('ptCheckerResults');
      if (!resultsEl) return;
      const input = raw.trim().toUpperCase();
      if (!input) { resultsEl.innerHTML = ''; return; }

      if (!/^[0-9A-F]+$/.test(input)) {
        resultsEl.innerHTML = '<p style="color:var(--status-red);margin:0">Invalid input — hex characters only (0-9, A-F).</p>';
        return;
      }
      if (input.length % 2 !== 0 || (input.length !== 2 && input.length !== 4 && input.length !== 6 && input.length < 8)) {
        resultsEl.innerHTML = '<p style="color:var(--status-red);margin:0">Prefix must be 2, 4, or 6 hex characters. For a full public key, use 64 characters.</p>';
        return;
      }

      const isFullKey = input.length >= 8;
      const tiers = isFullKey
        ? [{ b: 1, prefix: input.slice(0, 2) }, { b: 2, prefix: input.slice(0, 4) }, { b: 3, prefix: input.slice(0, 6) }]
        : [{ b: input.length / 2, prefix: input }];

      let html = '';
      // #1473 — Warn when the user pastes a prefix or full pubkey whose
      // first byte is one the MeshCore firmware keygen routine avoids
      // (pub_key[0] in {0x00, 0xFF}). Firmware keygen CONVENTION, not a
      // protocol-level rejection — see simple_repeater/main.cpp:83.
      if (typeof PrefixReserved !== 'undefined' && PrefixReserved &&
          PrefixReserved.isReservedPrefix(input)) {
        html += `<div role="alert" style="margin-bottom:10px;padding:10px 14px;border:1px solid var(--status-yellow);border-radius:6px;background:var(--bg-secondary,var(--bg))">
          <strong style="color:var(--status-yellow)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg> Firmware avoids this first byte</strong>
          <div class="text-muted" style="font-size:0.85em;margin-top:4px">
            <code class="mono">${input.slice(0,2)}</code> as the first byte of a node pubkey is avoided by the MeshCore firmware keygen convention (the standard repeater re-rolls identities whose <code class="mono">pub_key[0]</code> is <code class="mono">00</code> or <code class="mono">FF</code>). You generally shouldn't see this on real nodes.
          </div>
        </div>`;
      }
      if (isFullKey) {
        const inNetwork = nodes.some(n => n.public_key.toUpperCase() === input);
        html += `<p class="text-muted" style="font-size:0.85em;margin:0 0 10px">Derived prefixes: <code class="mono">${input.slice(0,2)}</code> / <code class="mono">${input.slice(0,4)}</code> / <code class="mono">${input.slice(0,6)}</code>${!inNetwork ? ' — <em>this node is not yet in the network</em>' : ''}</p>`;
      }

      tiers.forEach(({ b, prefix }) => {
        const matches = idx[b].get(prefix) || [];
        const colliders = isFullKey ? matches.filter(n => n.public_key.toUpperCase() !== input) : matches;
        const count = colliders.length;
        html += `
          <div style="margin-bottom:10px;padding:10px 14px;border:1px solid var(--border);border-radius:6px;background:var(--bg-secondary,var(--bg))">
            <div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">
              <code class="mono" style="font-weight:700">${prefix}</code>
              <span class="text-muted" style="font-size:0.82em">${b}-byte</span>
              ${severityBadge(count)}
            </div>
            ${count === 0
              ? '<div class="text-muted" style="font-size:0.85em">No existing nodes use this prefix.</div>'
              : `<div style="font-size:0.85em;max-height:140px;overflow-y:auto">${colliders.map(nodeEntry).join('')}</div>`}
          </div>`;
      });

      resultsEl.innerHTML = html;
    }

    // --- Generator ---
    function doGenerate() {
      const genResultEl = document.getElementById('ptGenResult');
      if (!genResultEl) return;
      const sizeInput = el.querySelector('input[name="ptGenSize"]:checked');
      const b = sizeInput ? parseInt(sizeInput.value) : 2;
      const hexLen = b * 2;
      const totalSpace = spaceSizes[b];
      // #1473 — Reserved prefixes (first byte 0x00 / 0xFF) are dropped from
      // the candidate pool because the MeshCore firmware keygen routine
      // re-rolls identities whose pub_key[0] is 0x00 or 0xFF — a keygen
      // CONVENTION (not a protocol rejection). See firmware
      // examples/simple_repeater/main.cpp:83 (HEAD 8ede7641).
      // Available = space - used - reserved.
      const reservedTotal = (typeof PrefixReserved !== 'undefined' && PrefixReserved)
        ? PrefixReserved.reservedCount(b)
        : 0;
      // Count reserved prefixes that are ALREADY used so we don't subtract them twice.
      let reservedUsed = 0;
      if (typeof PrefixReserved !== 'undefined' && PrefixReserved) {
        for (const p of idx[b].keys()) {
          if (PrefixReserved.isReservedPrefix(p)) reservedUsed++;
        }
      }
      const available = totalSpace - idx[b].size - (reservedTotal - reservedUsed);

      if (available <= 0) {
        const next = b < 3 ? (b + 1) + '-byte' : 'a different size';
        genResultEl.innerHTML = `<p style="color:var(--status-red);margin:0">No collision-free ${b}-byte prefixes available. Try ${next}.</p>`;
        return;
      }

      const isReserved = (p) =>
        (typeof PrefixReserved !== 'undefined' && PrefixReserved)
          ? PrefixReserved.isReservedPrefix(p)
          : false;

      let prefix;
      if (b === 1) {
        // Enumerate all 256 options, skipping used + reserved.
        const free = [];
        for (let i = 0; i < totalSpace; i++) {
          const p = i.toString(16).toUpperCase().padStart(hexLen, '0');
          if (!idx[b].has(p) && !isReserved(p)) free.push(p);
        }
        prefix = free[Math.floor(Math.random() * free.length)];
      } else {
        // Random sampling — with 2K used / 65K space, hit rate >96%.
        let attempts = 0;
        do {
          prefix = Math.floor(Math.random() * totalSpace).toString(16).toUpperCase().padStart(hexLen, '0');
        } while ((idx[b].has(prefix) || isReserved(prefix)) && ++attempts < 500);
        // Fallback to enumeration if sampling kept hitting used/reserved prefixes.
        if (idx[b].has(prefix) || isReserved(prefix)) {
          for (let i = 0; i < totalSpace; i++) {
            const p = i.toString(16).toUpperCase().padStart(hexLen, '0');
            if (!idx[b].has(p) && !isReserved(p)) { prefix = p; break; }
          }
        }
      }

      genResultEl.innerHTML = `
        <div style="padding:12px 16px;border:1px solid var(--status-green);border-radius:6px;background:var(--bg-secondary,var(--bg))">
          <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
            <code class="mono" style="font-size:1.3em;font-weight:700;color:var(--status-green-text)">${prefix}</code>
            <span style="color:var(--status-green-text)"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check-circle"/></svg> No existing nodes use this prefix</span>
          </div>
          <div class="text-muted" style="font-size:0.85em;margin-top:6px">${available.toLocaleString()} of ${totalSpace.toLocaleString()} ${b}-byte prefixes are available.</div>
          <div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap;align-items:center">
            <button id="ptRegenBtn" style="padding:5px 14px;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:4px;cursor:pointer;font-size:0.9em">Try another</button>
            <a href="https://agessaman.github.io/meshcore-web-keygen/?prefix=${prefix}" target="_blank" rel="noopener noreferrer"
              style="padding:5px 14px;background:var(--bg);color:var(--link-color);border:1px solid var(--border);border-radius:4px;text-decoration:none;font-size:0.9em">
              Generate key with this prefix →
            </a>
          </div>
        </div>`;
      document.getElementById('ptRegenBtn').addEventListener('click', doGenerate);
    }

    // --- Wire up ---
    const checkBtn = document.getElementById('ptCheckBtn');
    const prefixInput = document.getElementById('ptPrefixInput');
    const genBtn = document.getElementById('ptGenBtn');

    checkBtn.addEventListener('click', () => doCheck(prefixInput.value));
    prefixInput.addEventListener('keydown', e => { if (e.key === 'Enter') doCheck(prefixInput.value); });
    genBtn.addEventListener('click', doGenerate);

    // Network Overview toggle
    document.getElementById('ptOverviewToggle').addEventListener('click', () => {
      const body = document.getElementById('ptOverviewBody');
      const chevron = document.getElementById('ptOverviewChevron');
      const open = body.style.display === 'none';
      body.style.display = open ? '' : 'none';
      chevron.style.transform = open ? 'rotate(90deg)' : '';
    });

    // #1306: WHICH-collides toggles
    document.querySelectorAll('[data-pt-collide-toggle]').forEach(btn => {
      btn.addEventListener('click', () => {
        const panel = document.getElementById(btn.dataset.target);
        if (!panel) return;
        const open = panel.style.display === 'none' || panel.style.display === '';
        // Toggle: if currently hidden ('none'), open; else close.
        const currentlyHidden = panel.style.display === 'none';
        panel.style.display = currentlyHidden ? 'block' : 'none';
        btn.setAttribute('aria-expanded', String(currentlyHidden));
        // Swap arrow direction in label
        btn.textContent = btn.textContent.replace(currentlyHidden ? '→' : '↓', currentlyHidden ? '↓' : '→');
      });
    });

    // Auto-run from URL params
    if (initPrefix) {
      doCheck(initPrefix);
      setTimeout(() => { document.getElementById('ptChecker')?.scrollIntoView({ behavior: 'smooth', block: 'start' }); }, 150);
    } else if (initGenerate) {
      doGenerate();
      setTimeout(() => { document.getElementById('ptGenerator')?.scrollIntoView({ behavior: 'smooth', block: 'start' }); }, 150);
    }
  }

  // ===================== RF HEALTH =====================

  let _rfHealthState = { range: '24h', selectedObserver: null, customFrom: '', customTo: '' };

  function rfHealthTimeRangeToParams(range, customFrom, customTo) {
    const now = new Date();
    let since, until;
    if (range === 'custom' && customFrom) {
      since = new Date(customFrom).toISOString();
      until = customTo ? new Date(customTo).toISOString() : now.toISOString();
    } else {
      const durations = { '1h': 1, '3h': 3, '6h': 6, '12h': 12, '24h': 24, '3d': 72, '7d': 168, '30d': 720 };
      const hours = durations[range] || 24;
      since = new Date(now.getTime() - hours * 3600000).toISOString();
      until = now.toISOString();
    }
    return { since, until };
  }

  function rfHealthUpdateHash() {
    const params = new URLSearchParams();
    params.set('tab', 'rf-health');
    if (_rfHealthState.range !== '24h') params.set('range', _rfHealthState.range);
    if (_rfHealthState.selectedObserver) params.set('observer', _rfHealthState.selectedObserver);
    if (_rfHealthState.range === 'custom') {
      if (_rfHealthState.customFrom) params.set('from', _rfHealthState.customFrom);
      if (_rfHealthState.customTo) params.set('to', _rfHealthState.customTo);
    }
    history.replaceState(null, '', '#/analytics?' + params.toString());
  }

  async function renderRFHealthTab(el) {
    // Restore state from URL
    const hashParams = new URLSearchParams((location.hash.split('?')[1] || ''));
    if (hashParams.get('range')) _rfHealthState.range = hashParams.get('range');
    if (hashParams.get('observer')) _rfHealthState.selectedObserver = hashParams.get('observer');
    if (hashParams.get('from')) { _rfHealthState.customFrom = hashParams.get('from'); _rfHealthState.range = 'custom'; }
    if (hashParams.get('to')) { _rfHealthState.customTo = hashParams.get('to'); _rfHealthState.range = 'custom'; }

    const ranges = ['1h','3h','6h','12h','24h','3d','7d','30d'];
    const rangeButtons = ranges.map(r =>
      `<button class="rf-range-btn${_rfHealthState.range === r ? ' active' : ''}" data-range="${r}">${r}</button>`
    ).join('');

    el.innerHTML = `
      <div class="rf-health-container">
        <div class="rf-time-selector">
          ${rangeButtons}
          <button class="rf-range-btn${_rfHealthState.range === 'custom' ? ' active' : ''}" data-range="custom">Custom</button>
          <span class="rf-custom-inputs" style="display:${_rfHealthState.range === 'custom' ? 'inline' : 'none'}">
            <input type="datetime-local" class="rf-datetime" id="rfFrom" value="${_rfHealthState.customFrom}">
            <span>→</span>
            <input type="datetime-local" class="rf-datetime" id="rfTo" value="${_rfHealthState.customTo}">
            <button class="rf-range-btn" id="rfCustomApply">Apply</button>
          </span>
        </div>
        <div class="rf-health-split">
          <div id="rfHealthGrid" class="rf-health-grid">
            <div class="text-muted" style="padding:20px">Loading RF metrics…</div>
          </div>
          <div id="rfHealthDetail" class="rf-health-detail rf-panel-empty">
            <span>Select an observer to view details</span>
          </div>
        </div>
      </div>`;

    // Range button handlers
    el.querySelectorAll('.rf-range-btn[data-range]').forEach(btn => {
      btn.addEventListener('click', () => {
        const range = btn.dataset.range;
        _rfHealthState.range = range;
        el.querySelectorAll('.rf-range-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        const customInputs = el.querySelector('.rf-custom-inputs');
        if (customInputs) customInputs.style.display = range === 'custom' ? 'inline' : 'none';
        if (range !== 'custom') {
          rfHealthUpdateHash();
          loadRFHealthData(el);
        }
      });
    });

    const applyBtn = document.getElementById('rfCustomApply');
    if (applyBtn) {
      applyBtn.addEventListener('click', () => {
        _rfHealthState.customFrom = document.getElementById('rfFrom').value;
        _rfHealthState.customTo = document.getElementById('rfTo').value;
        rfHealthUpdateHash();
        loadRFHealthData(el);
      });
    }

    await loadRFHealthData(el);
  }

  async function loadRFHealthData(el) {
    const grid = document.getElementById('rfHealthGrid');
    const detail = document.getElementById('rfHealthDetail');

    try {
      // Compute window string for summary endpoint
      const windowMap = { '1h':'1h', '3h':'3h', '6h':'6h', '12h':'12h', '24h':'24h', '3d':'3d', '7d':'7d', '30d':'30d' };
      const window = windowMap[_rfHealthState.range] || '24h';
      const summaryData = await api('/observers/metrics/summary?window=' + window + (RegionFilter.regionQueryString() || ''));
      const observers = summaryData.observers || [];

      // Filter to observers with sufficient sparkline data (≥2 non-null noise_floor values)
      const filteredObservers = observers.filter(obs => {
        const nfValues = (obs.sparkline || []).filter(v => v != null);
        return nfValues.length >= 2;
      });

      if (!filteredObservers.length) {
        grid.innerHTML = '<div class="text-muted" style="padding:20px">No RF metrics data available yet. Metrics are collected from observer status messages every ~5 minutes.</div>';
        return;
      }

      // Render small multiples grid
      grid.innerHTML = filteredObservers.map(obs => {
        const nf = obs.current_noise_floor != null ? obs.current_noise_floor.toFixed(1) : '—';
        const avgNf = obs.avg_noise_floor_24h != null ? obs.avg_noise_floor_24h.toFixed(1) : '—';
        const maxNf = obs.max_noise_floor_24h != null ? obs.max_noise_floor_24h.toFixed(1) : '—';
        const batt = obs.battery_mv != null ? (obs.battery_mv / 1000).toFixed(2) + 'V' : '';
        const name = obs.observer_name || obs.observer_id.substring(0, 8);
        const isSelected = _rfHealthState.selectedObserver === obs.observer_id;

        // NF status coloring
        let nfClass = '';
        if (obs.current_noise_floor != null) {
          if (obs.current_noise_floor >= -85) nfClass = 'rf-nf-critical';
          else if (obs.current_noise_floor >= -100) nfClass = 'rf-nf-warning';
        }

        return `<div class="rf-cell${isSelected ? ' rf-cell-selected' : ''}" data-observer="${obs.observer_id}" tabindex="0" role="button" aria-label="Observer ${esc(name)}, noise floor ${nf} dBm">
          <div class="rf-cell-header">
            <span class="rf-cell-name">${esc(name)}</span>
            <span class="rf-cell-nf ${nfClass}">${nf} dBm</span>
            ${batt ? `<span class="rf-cell-batt">${batt}</span>` : ''}
          </div>
          <div class="rf-cell-sparkline" id="rf-spark-${obs.observer_id}"></div>
          <div class="rf-cell-stats">
            <span>avg: ${avgNf}</span>
            <span>max: ${maxNf}</span>
            <span>${obs.sample_count} samples</span>
          </div>
        </div>`;
      }).join('');

      // Click handler for cells
      grid.querySelectorAll('.rf-cell').forEach(cell => {
        cell.addEventListener('click', () => {
          const obsId = cell.dataset.observer;
          grid.querySelectorAll('.rf-cell').forEach(c => c.classList.remove('rf-cell-selected'));
          cell.classList.add('rf-cell-selected');
          _rfHealthState.selectedObserver = obsId;
          rfHealthUpdateHash();
          loadRFHealthDetail(obsId, detail);
        });
        cell.addEventListener('keydown', e => {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); cell.click(); }
        });
      });

      // Render sparklines from summary data (no extra API calls)
      for (const obs of filteredObservers) {
        const nfValues = (obs.sparkline || []).filter(v => v != null);
        const container = document.getElementById(`rf-spark-${obs.observer_id}`);
        if (container && nfValues.length > 1) {
          container.innerHTML = rfNFSparkline(nfValues, 140, 24);
        }
      }

      // Auto-expand selected observer from URL
      if (_rfHealthState.selectedObserver) {
        const selectedCell = grid.querySelector(`[data-observer="${_rfHealthState.selectedObserver}"]`);
        if (selectedCell) {
          selectedCell.classList.add('rf-cell-selected');
          loadRFHealthDetail(_rfHealthState.selectedObserver, detail);
        }
      }
    } catch (e) {
      grid.innerHTML = `<div class="text-muted" style="padding:20px">Failed to load RF health data: ${esc(e.message)}</div>`;
    }
  }

  async function loadRFSparkline(observerId) {
    const { since, until } = rfHealthTimeRangeToParams(_rfHealthState.range, _rfHealthState.customFrom, _rfHealthState.customTo);
    try {
      const data = await api(`/observers/${observerId}/metrics?since=${encodeURIComponent(since)}&until=${encodeURIComponent(until)}`);
      const metrics = data.metrics || [];
      const nfValues = metrics.map(m => m.noise_floor).filter(v => v != null);
      const container = document.getElementById(`rf-spark-${observerId}`);
      if (container && nfValues.length > 1) {
        container.innerHTML = rfNFSparkline(nfValues, 140, 24);
      } else if (container) {
        container.innerHTML = '<span class="text-muted" style="font-size:10px">insufficient data</span>';
      }
    } catch (e) {
      // Non-fatal — sparkline just won't render
    }
  }

  function rfNFSparkline(data, w, h) {
    if (!data.length) return '';
    // For noise floor, invert: more negative = better = lower on chart
    const min = Math.min(...data);
    const max = Math.max(...data);
    const range = max - min || 1;
    const pts = data.map((v, i) => {
      const x = (i / Math.max(data.length - 1, 1)) * w;
      // Higher dBm (worse) = higher on chart
      const y = h - 2 - ((v - min) / range) * (h - 4);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(' ');

    // Reference lines
    let refs = '';
    if (min <= -100 && max >= -100) {
      const y100 = h - 2 - ((-100 - min) / range) * (h - 4);
      refs += `<line x1="0" y1="${y100.toFixed(1)}" x2="${w}" y2="${y100.toFixed(1)}" stroke="var(--text-muted)" stroke-width="0.5" stroke-dasharray="2"/>`;
    }

    return `<svg viewBox="0 0 ${w} ${h}" style="width:${w}px;height:${h}px" role="img" aria-label="Noise floor sparkline"><title>Noise floor trend</title>${refs}<polyline points="${pts}" fill="none" stroke="var(--accent)" stroke-width="1.5"/></svg>`;
  }

  async function loadRFHealthDetail(observerId, container) {
    container.classList.remove('rf-panel-empty');
    container.innerHTML = '<div class="text-muted" style="padding:10px">Loading detail…</div>';

    const { since, until } = rfHealthTimeRangeToParams(_rfHealthState.range, _rfHealthState.customFrom, _rfHealthState.customTo);
    // Choose resolution based on time range
    let resolution = '5m';
    const rangeMap = { '7d': '1h', '30d': '1h' };
    if (rangeMap[_rfHealthState.range]) resolution = rangeMap[_rfHealthState.range];

    try {
      const data = await api(`/observers/${observerId}/metrics?since=${encodeURIComponent(since)}&until=${encodeURIComponent(until)}&resolution=${resolution}`);
      const metrics = data.metrics || [];
      const reboots = (data.reboots || []).map(r => new Date(r).getTime());
      const name = data.observer_name || observerId.substring(0, 8);

      if (!metrics.length) {
        container.innerHTML = `<div class="text-muted" style="padding:10px">No metrics data for ${esc(name)} in selected time range.</div>`;
        return;
      }

      // Extract data series
      const nfData = metrics.map(m => ({ t: m.timestamp, v: m.noise_floor })).filter(d => d.v != null);
      const txData = metrics.map(m => ({ t: m.timestamp, v: m.tx_airtime_pct })).filter(d => d.v != null);
      const rxData = metrics.map(m => ({ t: m.timestamp, v: m.rx_airtime_pct })).filter(d => d.v != null);
      const errData = metrics.map(m => ({ t: m.timestamp, v: m.recv_error_rate })).filter(d => d.v != null);
      const battData = metrics.map(m => ({ t: m.timestamp, v: m.battery_mv })).filter(d => d.v != null && d.v > 0);

      const hasAirtime = txData.length > 1 || rxData.length > 1;
      const hasErrors = errData.length > 1;
      const hasBattery = battData.length > 1;

      // Current values
      const latest = metrics[metrics.length - 1];
      const nfValues = metrics.map(m => m.noise_floor).filter(v => v != null);
      const avgNf = nfValues.length ? (nfValues.reduce((a,b) => a+b, 0) / nfValues.length).toFixed(1) : '—';
      const minNf = nfValues.length ? Math.min(...nfValues).toFixed(1) : '—';
      const maxNf = nfValues.length ? Math.max(...nfValues).toFixed(1) : '—';
      const curNf = latest.noise_floor != null ? latest.noise_floor.toFixed(1) : '—';
      const curBatt = latest.battery_mv != null && latest.battery_mv > 0 ? (latest.battery_mv / 1000).toFixed(2) + 'V' : '—';
      const curTx = latest.tx_airtime_pct != null ? latest.tx_airtime_pct.toFixed(1) + '%' : '—';
      const curRx = latest.rx_airtime_pct != null ? latest.rx_airtime_pct.toFixed(1) + '%' : '—';
      const curErr = latest.recv_error_rate != null ? latest.recv_error_rate.toFixed(2) + '%' : '—';

      container.innerHTML = `
        <div class="rf-detail-header">
          <h3>${esc(name)}</h3>
          <button class="rf-detail-close" aria-label="Close detail" title="Close"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg></button>
        </div>
        <div class="rf-detail-charts">
          <div class="rf-detail-chart" id="rfDetailNFChart"></div>
          ${hasAirtime ? '<div class="rf-detail-chart" id="rfDetailAirtimeChart"></div>' : ''}
          ${hasErrors ? '<div class="rf-detail-chart" id="rfDetailErrorChart"></div>' : ''}
          ${hasBattery ? '<div class="rf-detail-chart" id="rfDetailBatteryChart"></div>' : ''}
        </div>
        <div class="rf-detail-summary">
          NF: ${curNf} dBm | avg: ${avgNf} | min: ${minNf} | max: ${maxNf} | TX: ${curTx} | RX: ${curRx} | Err: ${curErr} | Batt: ${curBatt}${reboots.length ? ' | ' + reboots.length + ' reboots' : ''}
        </div>`;

      // Close button
      container.querySelector('.rf-detail-close').addEventListener('click', () => {
        container.classList.add('rf-panel-empty');
        container.innerHTML = '<span>Select an observer to view details</span>';
        _rfHealthState.selectedObserver = null;
        rfHealthUpdateHash();
        document.querySelectorAll('.rf-cell').forEach(c => c.classList.remove('rf-cell-selected'));
      });

      // Compute shared time range across all charts
      const allTimestamps = metrics.map(m => new Date(m.timestamp).getTime());
      const minT = Math.min(...allTimestamps);
      const maxT = Math.max(...allTimestamps);

      // Render noise floor chart
      const nfEl = document.getElementById('rfDetailNFChart');
      if (nfEl && nfData.length > 1) {
        nfEl.innerHTML = rfNFColumnChart(nfData, nfEl.clientWidth || 700, 180, reboots, minT, maxT);
      } else if (nfEl) {
        nfEl.innerHTML = '<span class="text-muted">Not enough noise floor data</span>';
      }

      // Render airtime chart
      if (hasAirtime) {
        const atEl = document.getElementById('rfDetailAirtimeChart');
        if (atEl) {
          atEl.innerHTML = rfAirtimeChart(txData, rxData, atEl.clientWidth || 700, 150, reboots, minT, maxT);
        }
      }

      // Render error rate chart
      if (hasErrors) {
        const errEl = document.getElementById('rfDetailErrorChart');
        if (errEl) {
          errEl.innerHTML = rfErrorRateChart(errData, errEl.clientWidth || 700, 120, reboots, minT, maxT);
        }
      }

      // Render battery chart
      if (hasBattery) {
        const battEl = document.getElementById('rfDetailBatteryChart');
        if (battEl) {
          battEl.innerHTML = rfBatteryChart(battData, battEl.clientWidth || 700, 120, reboots, minT, maxT);
        }
      }
    } catch (e) {
      container.innerHTML = `<div class="text-muted" style="padding:10px">Failed to load detail: ${esc(e.message)}</div>`;
    }
  }

  // Shared helper: render reboot markers as vertical hairlines
  function rfRebootMarkers(reboots, sx, pad, h, w) {
    let svg = '';
    for (const rt of reboots) {
      const x = sx(rt);
      if (x >= pad.left && x <= w - pad.right) {
        svg += `<line x1="${x.toFixed(1)}" y1="${pad.top}" x2="${x.toFixed(1)}" y2="${(h - pad.bottom).toFixed(1)}" stroke="var(--text-muted)" stroke-width="0.5" stroke-dasharray="3,3" opacity="0.6"/>`;
        svg += `<text x="${(x + 2).toFixed(1)}" y="${(pad.top + 8).toFixed(1)}" font-size="7" fill="var(--text-muted)" opacity="0.7">reboot</text>`;
      }
    }
    return svg;
  }

  // Shared helper: render X-axis time labels
  function rfTooltipCircles(data, sx, sy, label, unit, formatV) {
    let svg = '';
    formatV = formatV || (v => v.toFixed(1));
    data.forEach(d => {
      const t = new Date(d.t);
      const x = sx(t.getTime());
      const y = sy(d.v);
      const ts = (typeof formatAbsoluteTimestamp === 'function') ? formatAbsoluteTimestamp(d.t) : t.toISOString().replace('T', ' ').replace(/\.\d+Z/, ' UTC');
      const tip = `${label}: ${formatV(d.v)}${unit}\n${ts}`;
      svg += `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="8" fill="transparent" stroke="none" pointer-events="all"><title>${tip}</title></circle>`;
    });
    return svg;
  }

  function rfXAxisLabels(data, sx, h, pad) {
    let svg = '';
    const xTicks = Math.min(6, data.length);
    for (let i = 0; i < xTicks; i++) {
      const idx = Math.floor(i * (data.length - 1) / Math.max(xTicks - 1, 1));
      const t = new Date(data[idx].t);
      const x = sx(t.getTime());
      const label = (typeof formatChartAxisLabel === 'function') ? formatChartAxisLabel(t, true) : t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
      svg += `<text x="${x.toFixed(1)}" y="${h - 5}" text-anchor="middle" font-size="9" fill="var(--text-muted)">${label}</text>`;
    }
    return svg;
  }

  // Shared: build polyline points string from data, skip nulls (break line)
  // Airtime chart: TX (red/orange) + RX (blue) lines, Y 0-100%
  function rfAirtimeChart(txData, rxData, w, h, reboots, sharedMinT, sharedMaxT) {
    const pad = { top: 20, right: 50, bottom: 30, left: 55 };
    const cw = w - pad.left - pad.right;
    const ch = h - pad.top - pad.bottom;
    const minT = sharedMinT, maxT = sharedMaxT;
    const rangeT = maxT - minT || 1;

    // Auto-scale Y-axis to data range (20% headroom, min 1%)
    let dataMax = 0;
    for (let i = 0; i < txData.length; i++) { if (txData[i].v > dataMax) dataMax = txData[i].v; }
    for (let i = 0; i < rxData.length; i++) { if (rxData[i].v > dataMax) dataMax = rxData[i].v; }
    const yMax = Math.max(dataMax * 1.2, 1);

    const sx = t => pad.left + ((t - minT) / rangeT) * cw;
    const sy = v => pad.top + ch - (v / yMax) * ch;

    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Airtime chart"><title>Airtime %</title>`;

    // Chart title
    svg += `<text x="${pad.left}" y="12" font-size="10" fill="var(--text-muted)" font-weight="600">Airtime %</text>`;

    // Y-axis: 5 ticks from 0 to yMax
    const yTicks = 4;
    for (let i = 0; i <= yTicks; i++) {
      const v = yMax * i / yTicks;
      const y = sy(v);
      svg += `<text x="${pad.left - 4}" y="${(y + 3).toFixed(1)}" text-anchor="end" font-size="9" fill="var(--text-muted)">${v.toFixed(1)}</text>`;
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="0.3"/>`;
    }

    // Reboot markers
    svg += rfRebootMarkers(reboots, sx, pad, h, w);

    // TX line (red/orange)
    if (txData.length > 1) {
      const txPts = txData.map(d => `${sx(new Date(d.t).getTime()).toFixed(1)},${sy(d.v).toFixed(1)}`).join(' ');
      svg += `<polyline points="${txPts}" fill="none" stroke="var(--danger, #e74c3c)" stroke-width="1.5"/>`;
      // Direct label at last point
      const lastTx = txData[txData.length - 1];
      const lx = sx(new Date(lastTx.t).getTime());
      const ly = sy(lastTx.v);
      // Offset label up if RX label would overlap (within 12px)
      const lastRx = rxData.length > 1 ? rxData[rxData.length - 1] : null;
      const rxLy = lastRx ? sy(lastRx.v) : Infinity;
      const txLabelY = (Math.abs(ly - rxLy) < 12) ? ly - 8 : ly + 3;
      svg += `<text x="${(lx + 4).toFixed(1)}" y="${txLabelY.toFixed(1)}" font-size="9" fill="var(--danger, #e74c3c)">TX ${lastTx.v.toFixed(1)}%</text>`;
    }

    // RX line (blue)
    if (rxData.length > 1) {
      const rxPts = rxData.map(d => `${sx(new Date(d.t).getTime()).toFixed(1)},${sy(d.v).toFixed(1)}`).join(' ');
      svg += `<polyline points="${rxPts}" fill="none" stroke="var(--info, #3498db)" stroke-width="1.5"/>`;
      // Direct label at last point
      const lastRx = rxData[rxData.length - 1];
      const lx = sx(new Date(lastRx.t).getTime());
      const ly = sy(lastRx.v);
      // Offset label down if TX label is nearby
      const lastTx = txData.length > 1 ? txData[txData.length - 1] : null;
      const txLy = lastTx ? sy(lastTx.v) : -Infinity;
      const rxLabelY = (Math.abs(ly - txLy) < 12) ? ly + 12 : ly + 3;
      svg += `<text x="${(lx + 4).toFixed(1)}" y="${rxLabelY.toFixed(1)}" font-size="9" fill="var(--info, #3498db)">RX ${lastRx.v.toFixed(1)}%</text>`;
    }

    // X-axis labels
    const allData = txData.length >= rxData.length ? txData : rxData;
    svg += rfXAxisLabels(allData, sx, h, pad);

    // Hover tooltips
    svg += rfTooltipCircles(txData, sx, sy, 'TX', '%');
    svg += rfTooltipCircles(rxData, sx, sy, 'RX', '%');

    svg += '</svg>';
    return svg;
  }

  // Error rate chart: recv_error_rate line
  function rfErrorRateChart(errData, w, h, reboots, sharedMinT, sharedMaxT) {
    const pad = { top: 20, right: 50, bottom: 30, left: 55 };
    const cw = w - pad.left - pad.right;
    const ch = h - pad.top - pad.bottom;
    const minT = sharedMinT, maxT = sharedMaxT;
    const rangeT = maxT - minT || 1;

    const values = errData.map(d => d.v);
    const maxV = Math.max(...values, 1); // at least 1% scale
    const rangeV = maxV || 1;

    const sx = t => pad.left + ((t - minT) / rangeT) * cw;
    const sy = v => pad.top + ch - (v / rangeV) * ch;

    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Error rate chart"><title>Error Rate</title>`;

    // Chart title
    svg += `<text x="${pad.left}" y="12" font-size="10" fill="var(--text-muted)" font-weight="600">Error Rate %</text>`;

    // Y-axis
    const yTicks = 4;
    for (let i = 0; i <= yTicks; i++) {
      const v = (rangeV * i / yTicks);
      const y = sy(v);
      svg += `<text x="${pad.left - 4}" y="${(y + 3).toFixed(1)}" text-anchor="end" font-size="9" fill="var(--text-muted)">${v.toFixed(1)}</text>`;
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="0.3"/>`;
    }

    // Reboot markers
    svg += rfRebootMarkers(reboots, sx, pad, h, w);

    // Error rate line
    const pts = errData.map(d => `${sx(new Date(d.t).getTime()).toFixed(1)},${sy(d.v).toFixed(1)}`).join(' ');
    svg += `<polyline points="${pts}" fill="none" stroke="var(--warning, #f39c12)" stroke-width="1.5"/>`;

    // Direct label at last point
    const last = errData[errData.length - 1];
    const lx = sx(new Date(last.t).getTime());
    const ly = sy(last.v);
    svg += `<text x="${(lx + 4).toFixed(1)}" y="${(ly + 3).toFixed(1)}" font-size="9" fill="var(--warning, #f39c12)">${last.v.toFixed(2)}%</text>`;

    // X-axis labels
    svg += rfXAxisLabels(errData, sx, h, pad);

    // Hover tooltips
    svg += rfTooltipCircles(errData, sx, sy, 'Err', '%', v => v.toFixed(2));

    svg += '</svg>';
    return svg;
  }

  // Battery voltage chart
  function rfBatteryChart(battData, w, h, reboots, sharedMinT, sharedMaxT) {
    const pad = { top: 20, right: 50, bottom: 30, left: 55 };
    const cw = w - pad.left - pad.right;
    const ch = h - pad.top - pad.bottom;
    const minT = sharedMinT, maxT = sharedMaxT;
    const rangeT = maxT - minT || 1;

    const values = battData.map(d => d.v);
    const minV = Math.min(...values);
    const maxV = Math.max(...values);
    const rangeV = maxV - minV || 100; // at least 100mV range

    const sx = t => pad.left + ((t - minT) / rangeT) * cw;
    const sy = v => pad.top + ch - ((v - minV) / rangeV) * ch;

    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Battery voltage chart"><title>Battery</title>`;

    // Chart title
    svg += `<text x="${pad.left}" y="12" font-size="10" fill="var(--text-muted)" font-weight="600">Battery</text>`;

    // Y-axis (in volts)
    const yTicks = 4;
    for (let i = 0; i <= yTicks; i++) {
      const v = minV + (rangeV * i / yTicks);
      const y = sy(v);
      svg += `<text x="${pad.left - 4}" y="${(y + 3).toFixed(1)}" text-anchor="end" font-size="9" fill="var(--text-muted)">${(v/1000).toFixed(2)}V</text>`;
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="0.3"/>`;
    }

    // Low battery reference line at 3.3V
    const lowBattMv = 3300;
    if (lowBattMv >= minV && lowBattMv <= maxV) {
      const y = sy(lowBattMv);
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--warning, #f39c12)" stroke-width="0.5" stroke-dasharray="4,2"/>`;
      svg += `<text x="${w - pad.right + 2}" y="${(y + 3).toFixed(1)}" font-size="8" fill="var(--warning, #f39c12)">3.3V low</text>`;
    }

    // Reboot markers
    svg += rfRebootMarkers(reboots, sx, pad, h, w);

    // Battery line
    const pts = battData.map(d => `${sx(new Date(d.t).getTime()).toFixed(1)},${sy(d.v).toFixed(1)}`).join(' ');
    svg += `<polyline points="${pts}" fill="none" stroke="var(--success, #27ae60)" stroke-width="1.5"/>`;

    // Direct label at last point
    const last = battData[battData.length - 1];
    const lx = sx(new Date(last.t).getTime());
    const ly = sy(last.v);
    svg += `<text x="${(lx + 4).toFixed(1)}" y="${(ly + 3).toFixed(1)}" font-size="9" fill="var(--success, #27ae60)">${(last.v/1000).toFixed(2)}V</text>`;

    // X-axis labels
    svg += rfXAxisLabels(battData, sx, h, pad);

    // Hover tooltips
    svg += rfTooltipCircles(battData, sx, sy, 'Batt', 'V', v => (v/1000).toFixed(2));

    svg += '</svg>';
    return svg;
  }

  /**
   * Noise floor column chart — color-coded bars (green/yellow/red) by threshold.
   * Replaces the old line chart for better discrete-sample readability.
   * Thresholds: green (< -100 dBm), yellow (-100 to -85 dBm), red (≥ -85 dBm).
   */
  function rfNFColumnChart(data, w, h, reboots, sharedMinT, sharedMaxT) {
    if (!data || !data.length) return '<svg viewBox="0 0 1 1"></svg>';
    reboots = reboots || [];
    const pad = { top: 20, right: 40, bottom: 30, left: 55 };
    const cw = w - pad.left - pad.right;
    const ch = h - pad.top - pad.bottom;

    const values = data.map(d => d.v);
    const minT = sharedMinT != null ? sharedMinT : Math.min(...data.map(d => new Date(d.t).getTime()));
    const maxT = sharedMaxT != null ? sharedMaxT : Math.max(...data.map(d => new Date(d.t).getTime()));
    const minV = Math.min(...values);
    const maxV = Math.max(...values);
    // Guard against zero range (single data point or constant values):
    // use a ±5 dBm window so bars are visible and centered in the chart
    const rawRangeV = maxV - minV;
    const rangeV = rawRangeV || 10;
    const adjMinV = rawRangeV ? minV : minV - 5;
    const rangeT = maxT - minT || 1;

    const sx = t => pad.left + ((t - minT) / rangeT) * cw;
    const sy = v => pad.top + ch - ((v - adjMinV) / rangeV) * ch;

    // Column width: proportional to chart width / data points, min 2px, gap of 1px
    const colW = Math.max(2, Math.floor(cw / data.length) - 1);

    const times = data.map(d => new Date(d.t).getTime());

    let svg = `<svg viewBox="0 0 ${w} ${h}" style="width:100%;max-height:${h}px" role="img" aria-label="Noise floor column chart"><title>Noise floor over time</title>`;

    // Inline style for hover highlighting
    svg += `<style>.nf-bar{transition:opacity 0.05s}.nf-bar:hover{opacity:0.75;stroke:var(--text);stroke-width:1}</style>`;

    // Chart title
    svg += `<text x="${pad.left}" y="12" font-size="10" fill="var(--text-muted)" font-weight="600">Noise Floor dBm</text>`;

    // Y-axis labels + grid lines
    const yTicks = 5;
    for (let i = 0; i <= yTicks; i++) {
      const v = adjMinV + (rangeV * i / yTicks);
      const y = sy(v);
      svg += `<text x="${pad.left - 4}" y="${(y + 3).toFixed(1)}" text-anchor="end" font-size="9" fill="var(--text-muted)">${v.toFixed(0)}</text>`;
      svg += `<line x1="${pad.left}" y1="${y.toFixed(1)}" x2="${w - pad.right}" y2="${y.toFixed(1)}" stroke="var(--border)" stroke-width="0.3"/>`;
    }

    // Reboot markers
    svg += rfRebootMarkers(reboots, sx, pad, h, w);

    // X-axis labels
    svg += rfXAxisLabels(data, sx, h, pad);

    // Color-coded columns
    for (let i = 0; i < data.length; i++) {
      const t = times[i];
      const v = data[i].v;
      const x = sx(t) - colW / 2;
      const y = sy(v);
      const barH = pad.top + ch - y;

      // Threshold color: green < -100, yellow -100 to -85, red >= -85
      let color;
      if (v < -100) color = 'var(--success, #22c55e)';
      else if (v < -85) color = 'var(--warning, #eab308)';
      else color = 'var(--danger, #ef4444)';

      const ts = new Date(data[i].t).toISOString().replace('T', ' ').replace(/\.\d+Z/, ' UTC');
      const tip = `NF: ${v.toFixed(1)} dBm\n${ts}`;

      svg += `<rect class="nf-bar" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${colW}" height="${Math.max(0, barH).toFixed(1)}" fill="${color}" rx="0.5"><title>${tip}</title></rect>`;
    }

    // Y-axis label
    svg += `<text x="12" y="${(h / 2)}" text-anchor="middle" font-size="10" fill="var(--text-muted)" transform="rotate(-90,12,${h/2})">dBm</text>`;

    // Legend
    const legendY = pad.top + 2;
    const legendX = w - pad.right - 140;
    svg += `<rect x="${legendX}" y="${legendY}" width="8" height="8" fill="var(--success, #22c55e)" rx="1"/>`;
    svg += `<text x="${legendX + 11}" y="${legendY + 7}" font-size="8" fill="var(--text-muted)">&lt; -100</text>`;
    svg += `<rect x="${legendX + 48}" y="${legendY}" width="8" height="8" fill="var(--warning, #eab308)" rx="1"/>`;
    svg += `<text x="${legendX + 59}" y="${legendY + 7}" font-size="8" fill="var(--text-muted)">-100…-85</text>`;
    svg += `<rect x="${legendX + 105}" y="${legendY}" width="8" height="8" fill="var(--danger, #ef4444)" rx="1"/>`;
    svg += `<text x="${legendX + 116}" y="${legendY + 7}" font-size="8" fill="var(--text-muted)">≥ -85</text>`;

    svg += '</svg>';
    return svg;
  }

  // #690 — Clock Health fleet view (M3)
  async function renderClockHealthTab(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading clock health data…</div>';
    try {
      const aqs = AreaFilter.areaQueryString();
      var data = await (await fetch('/api/nodes/clock-skew' + (aqs ? '?' + aqs.slice(1) : ''))).json();
      if (!Array.isArray(data) || !data.length) {
        el.innerHTML = '<div class="text-center text-muted" style="padding:40px">No clock skew data available. Nodes need recent adverts for clock analysis.</div>';
        return;
      }

      // State
      var activeFilter = 'all';
      var sortKey = 'severity';
      var sortDir = 'asc'; // severity worst-first

      function render() {
        // Filter
        var filtered = activeFilter === 'all' ? data : data.filter(function(n) { return n.severity === activeFilter; });

        // Sort
        filtered = filtered.slice().sort(function(a, b) {
          var v;
          if (sortKey === 'severity') {
            v = (SKEW_SEVERITY_ORDER[a.severity] || 9) - (SKEW_SEVERITY_ORDER[b.severity] || 9);
          } else if (sortKey === 'skew') {
            v = Math.abs(window.currentSkewValue(b) || 0) - Math.abs(window.currentSkewValue(a) || 0);
          } else if (sortKey === 'name') {
            v = (a.nodeName || '').localeCompare(b.nodeName || '');
          } else if (sortKey === 'drift') {
            v = Math.abs(b.driftPerDaySec || 0) - Math.abs(a.driftPerDaySec || 0);
          }
          return sortDir === 'desc' ? -v : v;
        });

        // Summary
        var counts = { ok: 0, warning: 0, critical: 0, absurd: 0 };
        data.forEach(function(n) { if (counts[n.severity] !== undefined) counts[n.severity]++; });

        // Filter buttons (also serve as summary — no separate stats pills needed)
        var filterColors = { ok: 'var(--status-green)', warning: 'var(--status-yellow)', critical: 'var(--status-orange)', absurd: 'var(--status-purple)', no_clock: 'var(--text-muted)' };
        var filters = ['all', 'ok', 'warning', 'critical', 'absurd', 'no_clock'];
        var filterHtml = '<div style="margin-bottom:10px">' + filters.map(function(f) {
          var dot = f !== 'all' ? '<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:' + filterColors[f] + ';margin-right:4px;vertical-align:middle"></span>' : '';
          return '<button class="clock-filter-btn' + (activeFilter === f ? ' active' : '') + '" data-filter="' + f + '">' +
            dot + (f === 'all' ? 'All (' + data.length + ')' : (SKEW_SEVERITY_LABELS[f] || f) + ' (' + (counts[f] || 0) + ')') +
            '</button>';
        }).join('') + '</div>';

        // Table
        var rowsHtml = filtered.map(function(n) {
          var rowClass = 'clock-fleet-row--' + (n.severity || 'ok');
          var lastAdv = n.lastObservedTS ? new Date(n.lastObservedTS * 1000).toISOString().replace('T', ' ').replace(/\.\d+Z/, ' UTC') : '—';
          var skewVal = window.currentSkewValue(n);
          var skewText = n.severity === 'no_clock' ? 'No Clock' : formatSkew(skewVal);
          var driftText = n.severity === 'no_clock' || !n.driftPerDaySec ? '–' : formatDrift(n.driftPerDaySec);
          return '<tr class="' + rowClass + '" data-pubkey="' + esc(n.pubkey) + '" style="cursor:pointer">' +
            '<td><strong>' + esc(n.nodeName || n.pubkey.slice(0, 12)) + '</strong></td>' +
            '<td style="font-family:var(--mono,monospace)">' + skewText + '</td>' +
            '<td>' + renderSkewBadge(n.severity, skewVal, n) + '</td>' +
            '<td style="font-family:var(--mono,monospace)">' + driftText + '</td>' +
            '<td style="font-size:11px">' + lastAdv + '</td>' +
            '</tr>';
        }).join('');

        el.innerHTML = '<h3 style="margin:0 0 10px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-clock"/></svg> Clock Health</h3>' +
          filterHtml +
          '<table class="data-table analytics-table" id="clock-health-table">' +
          '<thead><tr>' +
          '<th data-sort-col="name" style="cursor:pointer">Name</th>' +
          '<th data-sort-col="skew" style="cursor:pointer">Skew</th>' +
          '<th data-sort-col="severity" style="cursor:pointer">Severity</th>' +
          '<th data-sort-col="drift" style="cursor:pointer">Drift Rate</th>' +
          '<th>Last Advert</th>' +
          '</tr></thead><tbody>' + rowsHtml + '</tbody></table>';

        // Bind filter clicks
        el.querySelectorAll('.clock-filter-btn').forEach(function(btn) {
          btn.addEventListener('click', function() {
            activeFilter = btn.dataset.filter;
            render();
          });
        });

        // Bind header sort clicks
        el.querySelectorAll('[data-sort-col]').forEach(function(th) {
          th.addEventListener('click', function() {
            var col = th.dataset.sortCol;
            if (sortKey === col) { sortDir = sortDir === 'asc' ? 'desc' : 'asc'; }
            else { sortKey = col; sortDir = 'asc'; }
            render();
          });
        });

        // Bind row clicks → navigate to node
        el.querySelectorAll('tr[data-pubkey]').forEach(function(tr) {
          tr.addEventListener('click', function() {
            location.hash = '#/nodes/' + encodeURIComponent(tr.dataset.pubkey);
          });
        });
      }

      render();
    } catch (err) {
      el.innerHTML = '<div class="text-center" style="color:var(--status-red);padding:40px">Failed to load clock health data: ' + esc(String(err)) + '</div>';
    }
  }

  // ===================== SCOPES =====================
  async function renderScopesTab(el) {
    var winKey = 'scopes_window';
    var selectedWindow = (typeof sessionStorage !== 'undefined' && sessionStorage.getItem(winKey)) || '24h';

    // #1852: the tab grew to 12 stacked sections (windowed adoption stats,
    // all-time region breakdowns, all-time node/repeater hygiene lists) —
    // grouped into 3 sub-tabs so a visitor sees one coherent screen at a
    // time instead of one long scroll. Purely presentational: all three
    // groups' data still loads together (one #scope-stats fetch + the two
    // hygiene sections' own fetchAllNodes calls), only DOM visibility is
    // gated on the active sub-tab. The 1h/24h/7d window picker only
    // affects the Overview group (the all-time Regions/Hygiene sections
    // ignore it entirely), so it lives inside that panel, not above the
    // sub-tab bar.
    var subtabKey = 'scopes_subtab';
    var selectedSubtab = (typeof sessionStorage !== 'undefined' && sessionStorage.getItem(subtabKey)) || 'overview';

    // Role/text filter for the "Nodes Without a Default Scope" section
    // below. Lives at this scope (not inside updateData) so it survives
    // the 60s auto-refresh re-render — same reasoning as selectedWindow
    // above, just kept in memory rather than sessionStorage since it's a
    // finer-grained, more transient filter.
    var noScopeFilter = { role: '', q: '' };

    // Encrypted/unencrypted filter for "Scope Adoption by Channel" below —
    // same persistence reasoning as noScopeFilter above. '' = all,
    // 'encrypted' = channel_hash prefixed 'enc_' (undecryptable at ingest,
    // see db.go GetChannelScopeAdoption), 'unencrypted' = everything else
    // (plain channel names — never encrypted to begin with).
    var adoptFilter = { mode: '' };

    // Fix 5: write static frame only once
    if (!el.querySelector('#scopes-cards')) {
      el.innerHTML =
        '<h3 style="margin:0 0 4px">Scope Statistics</h3>' +
        '<p class="text-muted" style="margin:0 0 12px;font-size:0.85em">' +
          'Denominator is all observed transmissions. Only TRANSPORT_FLOOD (0) and TRANSPORT_DIRECT (3) routes carry a scope; FLOOD (1) and DIRECT (2) are inherently unscoped per MeshCore protocol.' +
        '</p>' +
        '<div class="analytics-tabs" id="scopesSubtabs" style="margin-bottom:12px">' +
          [
            { key: 'overview', label: 'Overview' },
            { key: 'regions', label: 'Regions' },
            { key: 'hygiene', label: 'Hygiene' },
          ].map(function(t) {
            return '<button class="tab-btn' + (selectedSubtab === t.key ? ' active' : '') + '" data-subtab="' + t.key + '">' + t.label + '</button>';
          }).join('') +
        '</div>' +
        '<div id="scopes-panel-overview" style="display:' + (selectedSubtab === 'overview' ? '' : 'none') + '">' +
          '<div style="margin-bottom:12px">' +
            ['1h', '24h', '7d'].map(function(v) {
              return '<button class="tab-btn' + (selectedWindow === v ? ' active' : '') + '" data-win="' + v + '">' + v + '</button>';
            }).join('') +
          '</div>' +
          '<div id="scopes-cards" class="stats-grid" style="margin-bottom:16px"></div>' +
          '<div id="scopes-highlights" style="margin-bottom:16px"></div>' +
          '<div id="scopes-hourly" style="margin-bottom:16px"></div>' +
          '<div id="scopes-channel-messages" style="margin-bottom:16px"></div>' +
          '<div id="scopes-channel-adoption" style="margin-bottom:16px"></div>' +
          '<div class="text-center text-muted" id="scopes-loading" style="padding:20px">Loading scope stats…</div>' +
          '<details style="margin-top:16px">' +
            '<summary style="cursor:pointer;font-weight:600;padding:2px 0">Region Breakdown &amp; Trend</summary>' +
            '<table class="data-table analytics-table" style="margin:8px 0">' +
              '<thead><tr><th>Region</th><th>Messages</th><th>% of Scoped</th></tr></thead>' +
              '<tbody id="scopes-tbody"></tbody>' +
            '</table>' +
            '<div id="scopes-chart"></div>' +
          '</details>' +
        '</div>' +
        '<div id="scopes-panel-regions" style="display:' + (selectedSubtab === 'regions' ? '' : 'none') + '">' +
          '<div id="scopes-utilization"></div>' +
          '<div id="scopes-repeaters" style="margin-top:16px"></div>' +
          '<div id="scopes-origin-nodes" style="margin-top:16px"></div>' +
          '<div id="scopes-bridges" style="margin-top:16px"></div>' +
        '</div>' +
        '<div id="scopes-panel-hygiene" style="display:' + (selectedSubtab === 'hygiene' ? '' : 'none') + '">' +
          '<div id="scopes-no-scope"></div>' +
          '<div id="scopes-never-relay-scope" style="margin-top:16px"></div>' +
        '</div>';

      // Sub-tab click listener (once) — pure visibility toggle, no re-fetch.
      var subtabsEl = el.querySelector('#scopesSubtabs');
      if (subtabsEl) {
        subtabsEl.addEventListener('click', function(e) {
          var btn = e.target.closest('[data-subtab]');
          if (!btn) return;
          selectedSubtab = btn.dataset.subtab;
          if (typeof sessionStorage !== 'undefined') sessionStorage.setItem(subtabKey, selectedSubtab);
          subtabsEl.querySelectorAll('[data-subtab]').forEach(function(b) { b.classList.toggle('active', b.dataset.subtab === selectedSubtab); });
          ['overview', 'regions', 'hygiene'].forEach(function(key) {
            var panel = document.getElementById('scopes-panel-' + key);
            if (panel) panel.style.display = key === selectedSubtab ? '' : 'none';
          });
        });
      }

      // Attach window-button click listeners (once)
      el.querySelectorAll('[data-win]').forEach(function(btn) {
        btn.addEventListener('click', function() {
          selectedWindow = btn.dataset.win;
          if (typeof sessionStorage !== 'undefined') sessionStorage.setItem(winKey, selectedWindow);
          el.querySelectorAll('[data-win]').forEach(function(b) { b.classList.toggle('active', b.dataset.win === selectedWindow); });
          load(selectedWindow);
        });
      });
    }

    function pct(n, total) {
      if (!total) return '—';
      return (n / total * 100).toFixed(1) + '%';
    }

    // Collapsed-by-default <details> wrapper for a scope-tab subsection —
    // title can include a dynamic count ("Nodes Not Using Any Scope
    // (1423 of 1491)") since it's built at render time with the real data
    // already in hand, unlike a static skeleton-level heading would be.
    // `key` is a stable identifier (independent of the dynamic count in
    // `title`) used by setSectionHtml to restore this section's open/closed
    // state across the 60s auto-refresh re-render below.
    // Description lives inside <summary> (not as a sibling of it) so it
    // stays visible while collapsed — <details> only ever hides its
    // children other than the summary, and the summary itself is always
    // shown regardless of open state.
    function detailsSection(title, description, bodyHtml, key) {
      return '<details style="margin-top:16px"' + (key ? ' data-key="' + esc(key) + '"' : '') + '>' +
        '<summary style="cursor:pointer;padding:2px 0">' +
          '<span style="font-weight:600">' + title + '</span>' +
          (description ? '<div class="text-muted" style="font-weight:400;font-size:0.85em;margin-top:2px">' + description + '</div>' : '') +
        '</summary>' +
        '<div style="margin:8px 0 0 2px">' + bodyHtml + '</div>' +
      '</details>';
    }

    // The 60s auto-refresh below rebuilds each section's innerHTML from
    // scratch, which would otherwise silently re-collapse any <details> a
    // visitor had open mid-read. Captures open state by data-key (falling
    // back to summary text for nested details that don't set one, e.g. the
    // per-region groups) before replacing, then reapplies it after.
    function setSectionHtml(container, html) {
      if (!container) return;
      var openKeys = [];
      container.querySelectorAll('details[open]').forEach(function(d) {
        var s = d.querySelector('summary');
        openKeys.push(d.getAttribute('data-key') || (s ? s.textContent : ''));
      });
      container.innerHTML = html;
      if (openKeys.length) {
        container.querySelectorAll('details').forEach(function(d) {
          var s = d.querySelector('summary');
          var k = d.getAttribute('data-key') || (s ? s.textContent : '');
          if (k && openKeys.indexOf(k) !== -1) d.open = true;
        });
      }
    }

    // Renders (and re-renders, on every filter change) the "Nodes Without
    // a Default Scope" section from an already-fetched node list — role
    // buttons and the search box filter that same in-memory list, so
    // narrowing to e.g. just companions never re-fetches. The search
    // input's own re-render (triggered by typing) would otherwise steal
    // focus every keystroke since setSectionHtml rebuilds the whole
    // section including the input; focus + caret are captured before and
    // restored after for that reason.
    function renderNoScopeSection(allNodes) {
      var noScopeElInner = document.getElementById('scopes-no-scope');
      if (!noScopeElInner) return;

      var searchFocused = document.activeElement && document.activeElement.id === 'noScopeSearch';
      var caretPos = searchFocused ? document.activeElement.selectionStart : null;

      var noScope = computeNodesWithoutScope(allNodes, 100, { role: noScopeFilter.role, q: noScopeFilter.q });
      var roleSummaryText = noScope.roleSummary.map(function(r) { return r.count.toLocaleString() + ' ' + esc(r.role); }).join(', ');

      var roleButtons = '<button type="button" class="tab-btn' + (!noScopeFilter.role ? ' active' : '') +
          '" data-role-filter="">All (' + noScope.total.toLocaleString() + ')</button>' +
        noScope.roleSummary.map(function(r) {
          return '<button type="button" class="tab-btn' + (noScopeFilter.role === r.role ? ' active' : '') +
            '" data-role-filter="' + esc(r.role) + '">' + esc(r.role) + ' (' + r.count.toLocaleString() + ')</button>';
        }).join('');

      var controlsHtml = '<div style="display:flex;flex-wrap:wrap;gap:6px;align-items:center;margin:4px 0 10px">' +
        roleButtons +
        '<input type="text" id="noScopeSearch" placeholder="Search by name or key…" value="' + esc(noScopeFilter.q) + '" ' +
          'style="margin-left:4px;padding:4px 8px;background:var(--card-bg);border:1px solid var(--border);border-radius:4px;color:var(--text);font-size:0.85em;min-width:180px">' +
        '</div>';

      var resultsBody;
      if (noScope.sortedCapped.length > 0) {
        var noScopeRows = noScope.sortedCapped.map(function(n) {
          return '<tr><td><a href="#/nodes/' + encodeURIComponent(n.public_key) + '">' + esc(n.name || n.public_key) + '</a></td>' +
            '<td>' + esc(n.role || '—') + '</td>' +
            '<td>' + timeAgo(n.last_seen) + '</td></tr>';
        }).join('');
        resultsBody = '<table class="data-table analytics-table">' +
          '<thead><tr><th>Node</th><th>Role</th><th>Last Seen</th></tr></thead>' +
          '<tbody>' + noScopeRows + '</tbody>' +
          '</table>';
      } else if (noScopeFilter.role || noScopeFilter.q) {
        resultsBody = '<p class="text-muted" style="font-size:0.85em">No nodes without a default scope match this filter.</p>';
      } else {
        resultsBody = '<p class="text-muted" style="font-size:0.85em">Every known node has a configured default scope.</p>';
      }

      setSectionHtml(noScopeElInner, detailsSection(
        'Nodes Without a Default Scope (' + noScope.total.toLocaleString() + ' of ' + allNodes.length.toLocaleString() + ')',
        'Nodes with no <code>default_scope</code> configured — they never set a hashRegions region for themselves in their own adverts. By role: ' + (roleSummaryText || 'none') + '. ' +
          'Sorted by most-recently-active first' +
          (noScope.filteredTotal !== noScope.total ? ', filtered to ' + noScope.filteredTotal.toLocaleString() + ' matching below' : '') +
          (noScope.truncated ? ' (capped at ' + noScope.sortedCapped.length + ')' : '') + '.',
        controlsHtml + resultsBody,
        'no-scope'
      ));

      var sectionEl = document.getElementById('scopes-no-scope');
      if (!sectionEl) return;

      sectionEl.querySelectorAll('[data-role-filter]').forEach(function(btn) {
        btn.addEventListener('click', function() {
          noScopeFilter.role = btn.dataset.roleFilter || '';
          renderNoScopeSection(allNodes);
        });
      });

      var searchInput = sectionEl.querySelector('#noScopeSearch');
      if (searchInput) {
        searchInput.addEventListener('input', debounce(function() {
          noScopeFilter.q = searchInput.value;
          renderNoScopeSection(allNodes);
        }, 200));
        if (searchFocused) {
          searchInput.focus();
          if (caretPos != null) {
            try { searchInput.setSelectionRange(caretPos, caretPos); } catch (e) { /* ignore */ }
          }
        }
      }
    }

    // Renders (and re-renders, on filter change) "Scope Adoption by
    // Channel" from the already-fetched `adoption` array — uncapped
    // server-side (see db.go GetChannelScopeAdoption), so every channel
    // seen in the window shows up here, filterable by whether its
    // channel_hash carries the 'enc_' prefix db.go uses for channels the
    // ingestor couldn't decrypt.
    function renderAdoptSection(adoption) {
      var adoptElInner = document.getElementById('scopes-channel-adoption');
      if (!adoptElInner) return;

      function isEncrypted(ca) { return !!(ca.channel && ca.channel.indexOf('enc_') === 0); }
      var encryptedCount = adoption.filter(isEncrypted).length;
      var unencryptedCount = adoption.length - encryptedCount;

      var filtered = adoption;
      if (adoptFilter.mode === 'encrypted') filtered = adoption.filter(isEncrypted);
      else if (adoptFilter.mode === 'unencrypted') filtered = adoption.filter(function(ca) { return !isEncrypted(ca); });

      var filterButtons = '<div style="display:flex;flex-wrap:wrap;gap:6px;margin-bottom:8px">' +
        '<button type="button" class="tab-btn' + (!adoptFilter.mode ? ' active' : '') + '" data-adopt-filter="">All (' + adoption.length.toLocaleString() + ')</button>' +
        '<button type="button" class="tab-btn' + (adoptFilter.mode === 'unencrypted' ? ' active' : '') + '" data-adopt-filter="unencrypted">Unencrypted (' + unencryptedCount.toLocaleString() + ')</button>' +
        '<button type="button" class="tab-btn' + (adoptFilter.mode === 'encrypted' ? ' active' : '') + '" data-adopt-filter="encrypted">Encrypted (' + encryptedCount.toLocaleString() + ')</button>' +
        '</div>';

      var adoptBody;
      if (filtered.length > 0) {
        var adoptRows = filtered.map(function(ca) {
          var caOverall = ca.scoped + ca.unscoped;
          var label = isEncrypted(ca) ? 'Encrypted (0x' + ca.channel.slice(4).toUpperCase() + ')' : ca.channel;
          var regionsHtml = (ca.regions && ca.regions.length)
            ? ca.regions.map(function(r) { return '<code>' + esc(r) + '</code>'; }).join(', ')
            : '<span class="text-muted">—</span>';
          return '<tr>' +
            '<td><code>' + esc(label) + '</code></td>' +
            '<td>' + ca.totalMessages.toLocaleString() + '</td>' +
            '<td>' + ca.scoped.toLocaleString() + ' (' + pct(ca.scoped, caOverall) + ')</td>' +
            '<td>' + ca.unscoped.toLocaleString() + '</td>' +
            '<td>' + ca.unknownScope.toLocaleString() + '</td>' +
            '<td>' + regionsHtml + '</td>' +
            '</tr>';
        }).join('');
        adoptBody = '<table class="data-table analytics-table">' +
          '<thead><tr><th>Channel</th><th>Messages</th><th>Scoped</th><th>Unscoped</th><th>Unknown</th><th>Regions Used</th></tr></thead>' +
          '<tbody>' + adoptRows + '</tbody>' +
          '</table>';
      } else if (adoption.length > 0) {
        adoptBody = '<p class="text-muted" style="font-size:0.85em">No channels match this filter.</p>';
      } else {
        adoptBody = '<p class="text-muted" style="font-size:0.85em">No channel messages in this window.</p>';
      }

      setSectionHtml(adoptElInner, detailsSection(
        'Scope Adoption by Channel (' + adoption.length.toLocaleString() + ')',
        'Which channels actually use region scoping vs which never do — every channel seen in this window.',
        adoption.length > 0 ? (filterButtons + adoptBody) : adoptBody,
        'scope-adoption'
      ));

      var sectionEl = document.getElementById('scopes-channel-adoption');
      if (!sectionEl) return;
      sectionEl.querySelectorAll('[data-adopt-filter]').forEach(function(btn) {
        btn.addEventListener('click', function() {
          adoptFilter.mode = btn.dataset.adoptFilter || '';
          renderAdoptSection(adoption);
        });
      });
    }

    async function load(w) {
      var loadingEl = document.getElementById('scopes-loading');
      if (loadingEl) loadingEl.style.display = '';
      try {
        // Fix 4: use api() instead of raw fetch()
        var data = await api('/scope-stats?window=' + encodeURIComponent(w), { ttl: 30000 });
        if (loadingEl) loadingEl.style.display = 'none';
        if (data.error) {
          var cardsEl2 = document.getElementById('scopes-cards');
          if (cardsEl2) cardsEl2.innerHTML = '<div class="text-center text-muted" style="padding:20px">' + esc(data.error) + '</div>';
          return;
        }
        await updateData(data, w);
      } catch (err) {
        if (loadingEl) loadingEl.style.display = 'none';
        var cardsEl3 = document.getElementById('scopes-cards');
        if (cardsEl3) cardsEl3.innerHTML = '<div class="text-center" style="color:var(--status-red);padding:20px">Failed to load scope stats: ' + esc(String(err)) + '</div>';
      }
    }

    async function updateData(d, w) {
      var s = d.summary;
      // #1838: denominator = transport-carrying transmissions (route_type 0,3).
      // Unscoped now includes non-transport routes (1,2) which are inherently
      // unscoped by MeshCore protocol, so unscoped can exceed transportTotal.
      var total = s.transportTotal || 0;
      var overall = (s.scoped || 0) + (s.unscoped || 0);

      // Summary cards
      var cardsEl = document.getElementById('scopes-cards');
      if (cardsEl) {
        cardsEl.innerHTML = [
          { label: 'Transport Total', value: total.toLocaleString(), note: 'routes 0,3 (carry scope)' },
          { label: 'Scoped', value: s.scoped.toLocaleString(), note: pct(s.scoped, overall) + ' of all traffic' },
          { label: 'Unscoped', value: s.unscoped.toLocaleString(), note: pct(s.unscoped, overall) + ' of all traffic' },
          { label: 'Unknown Scope', value: s.unknownScope.toLocaleString(), note: pct(s.unknownScope, s.scoped) + ' of scoped' },
        ].map(function(c) {
          return '<div class="stat-card"><div class="stat-value">' + c.value + '</div>' +
            '<div class="stat-label">' + c.label + '</div>' +
            (c.note ? '<div class="stat-note text-muted" style="font-size:11px">' + c.note + '</div>' : '') +
            '</div>';
        }).join('');
      }

      // Highlights row: the single most-asked-about number from each of
      // the Regions and Hygiene sub-tabs, surfaced here so a visitor
      // doesn't have to switch sub-tabs to see them. Region/bridge counts
      // come straight off `d` (already fetched above); the two
      // node-list-derived counts need `allNodes`, fetched once here and
      // reused below by the Hygiene sections themselves instead of each
      // fetching their own copy.
      var highlightsEl = document.getElementById('scopes-highlights');
      var allNodes = null;
      try {
        var allNodesResp = await fetchAllNodes('', { ttl: CLIENT_TTL.nodeList });
        allNodes = allNodesResp.nodes || allNodesResp;
      } catch (e) {
        allNodes = null;
      }
      if (highlightsEl) {
        var hlConfigured = d.configuredRegions || 0;
        var hlUnused = (d.unusedRegions || []).length;
        var hlUsed = hlConfigured - hlUnused;
        var hlBridges = (d.bridgeRepeaters || []).length;
        var hlNoScope = allNodes ? computeNodesWithoutScope(allNodes, 0).total : null;
        var hlNeverRelay = allNodes ? computeRepeatersNeverRelayingScope(allNodes, 0).total : null;
        highlightsEl.innerHTML = '<div class="stats-grid">' +
          [
            {
              label: 'Regions Used',
              value: hlConfigured > 0 ? hlUsed.toLocaleString() + ' / ' + hlConfigured.toLocaleString() : '—',
              note: hlConfigured > 0 ? pct(hlUsed, hlConfigured) + ' of configured' : 'no hashRegions configured',
            },
            {
              label: 'Bridge Repeaters',
              value: hlBridges.toLocaleString(),
              note: 'relay for 2+ regions',
            },
            {
              label: 'Nodes W/O Default Scope',
              value: hlNoScope != null ? hlNoScope.toLocaleString() : '—',
              note: (hlNoScope != null && allNodes.length) ? pct(hlNoScope, allNodes.length) + ' of all nodes' : '',
            },
            {
              label: 'Never Relaying Scope',
              value: hlNeverRelay != null ? hlNeverRelay.toLocaleString() : '—',
              note: 'repeaters/rooms',
            },
          ].map(function(c) {
            return '<div class="stat-card"><div class="stat-value">' + c.value + '</div>' +
              '<div class="stat-label">' + c.label + '</div>' +
              (c.note ? '<div class="stat-note text-muted" style="font-size:11px">' + c.note + '</div>' : '') +
              '</div>';
          }).join('') +
        '</div>';
      }

      // Channel-messages-only breakdown: same scoped/unscoped/unknown
      // question as the cards above, but restricted to payload_type=5
      // (channel chat) — most channel traffic is plain FLOOD, so this can
      // read very differently from the all-traffic numbers above.
      var chanEl = document.getElementById('scopes-channel-messages');
      if (chanEl) {
        var cm = d.channelMessages;
        if (cm && cm.totalMessages > 0) {
          var cmOverall = cm.scoped + cm.unscoped;
          chanEl.innerHTML =
            '<h4 style="margin:0 0 4px">Channel Messages</h4>' +
            '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">Same scoped/unscoped/unknown breakdown, restricted to channel chat messages only.</p>' +
            '<div class="stats-grid">' +
            [
              { label: 'Total Messages', value: cm.totalMessages.toLocaleString(), note: null },
              { label: 'Scoped', value: cm.scoped.toLocaleString(), note: pct(cm.scoped, cmOverall) + ' of channel messages' },
              { label: 'Unscoped', value: cm.unscoped.toLocaleString(), note: pct(cm.unscoped, cmOverall) + ' of channel messages' },
              { label: 'Unknown Scope', value: cm.unknownScope.toLocaleString(), note: pct(cm.unknownScope, cm.scoped) + ' of scoped' },
            ].map(function(c) {
              return '<div class="stat-card"><div class="stat-value">' + c.value + '</div>' +
                '<div class="stat-label">' + c.label + '</div>' +
                (c.note ? '<div class="stat-note text-muted" style="font-size:11px">' + c.note + '</div>' : '') +
                '</div>';
            }).join('') +
            '</div>';
        } else {
          chanEl.innerHTML = '';
        }
      }

      // Channel scope adoption: the Channel Messages breakdown above, but
      // per channel — which specific channels actually use region scoping
      // vs which never do. Uncapped; filterable by encrypted/unencrypted.
      var adoptEl = document.getElementById('scopes-channel-adoption');
      if (adoptEl) {
        renderAdoptSection(d.channelScopeAdoption || []);
      }

      // Per-region table
      var tbodyEl = document.getElementById('scopes-tbody');
      if (tbodyEl) {
        var tableBody = '';
        if (d.byRegion && d.byRegion.length) {
          tableBody = d.byRegion.map(function(r) {
            return '<tr><td><code>' + esc(r.name) + '</code></td>' +
              '<td>' + r.count.toLocaleString() + '</td>' +
              '<td>' + pct(r.count, s.scoped) + '</td></tr>';
          }).join('');
          if (s.unknownScope > 0) {
            tableBody += '<tr><td><em class="text-muted">Unknown scope</em></td>' +
              '<td>' + s.unknownScope.toLocaleString() + '</td>' +
              '<td>' + pct(s.unknownScope, s.scoped) + '</td></tr>';
          }
        } else if (s.scoped === 0) {
          tableBody = '<tr><td colspan="3" class="text-muted" style="text-align:center">No scoped messages in this window</td></tr>';
        } else {
          tableBody = '<tr><td colspan="3" class="text-muted" style="text-align:center">No regions configured — add <code>hashRegions</code> to your config</td></tr>';
        }
        tbodyEl.innerHTML = tableBody;
      }

      // Time-series chart (two-line SVG)
      var chartEl = document.getElementById('scopes-chart');
      if (chartEl) {
        var chartHtml = '';
        if (d.timeSeries && d.timeSeries.length > 1) {
          var scopedVals = d.timeSeries.map(function(p) { return p.scoped; });
          var unscopedVals = d.timeSeries.map(function(p) { return p.unscoped; });
          var maxVal = Math.max(1, Math.max.apply(null, scopedVals.concat(unscopedVals)));
          var W = 800, H = 180, padL = 44, padT = 10, padR = 10;
          var plotW = W - padL - padR, plotH = H - 24 - padT;
          var n = d.timeSeries.length;

          function pts(vals) {
            return vals.map(function(v, i) {
              var x = padL + i * plotW / Math.max(n - 1, 1);
              var y = padT + plotH - (v / maxVal) * plotH;
              return x.toFixed(1) + ',' + y.toFixed(1);
            }).join(' ');
          }

          var grid = '';
          for (var gi = 0; gi <= 4; gi++) {
            var gy = padT + plotH * gi / 4;
            var gv = Math.round(maxVal * (4 - gi) / 4);
            grid += '<line x1="' + padL + '" y1="' + gy.toFixed(1) + '" x2="' + (W - padR) + '" y2="' + gy.toFixed(1) + '" stroke="var(--border)" stroke-dasharray="2"/>';
            grid += '<text x="' + (padL - 4) + '" y="' + (gy + 4).toFixed(1) + '" text-anchor="end" font-size="9" fill="var(--text-muted)">' + gv + '</text>';
          }

          var legendX = padL + plotW - 120;
          chartHtml = '<div style="margin-top:16px">' +
            '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;max-height:' + H + 'px" role="img" aria-label="Scope time series">' +
            grid +
            '<polyline points="' + pts(scopedVals) + '" fill="none" stroke="var(--accent)" stroke-width="2"/>' +
            '<polyline points="' + pts(unscopedVals) + '" fill="none" stroke="var(--text-muted)" stroke-width="1.5" stroke-dasharray="4"/>' +
            '<rect x="' + legendX + '" y="' + padT + '" width="10" height="10" fill="var(--accent)"/>' +
            '<text x="' + (legendX + 14) + '" y="' + (padT + 9) + '" font-size="10" fill="var(--text)">Scoped</text>' +
            '<rect x="' + legendX + '" y="' + (padT + 16) + '" width="10" height="10" fill="var(--text-muted)"/>' +
            '<text x="' + (legendX + 14) + '" y="' + (padT + 25) + '" font-size="10" fill="var(--text)">Unscoped</text>' +
            '</svg></div>';
        } else {
          chartHtml = '<p class="text-muted" style="font-size:0.85em;margin:12px 0 0">Insufficient data points to render chart — wait for more observations in this window.</p>';
        }
        chartEl.innerHTML = chartHtml;
      }

      // Hour-of-day activity per region: when during a typical day is each
      // region active — a heatmap, not a chronological chart. Color
      // intensity is normalized PER ROW (each region's own busiest hour),
      // not globally, so a quiet region's shape is still visible next to
      // a loud one instead of being crushed to near-zero.
      var hourlyEl = document.getElementById('scopes-hourly');
      if (hourlyEl) {
        var hourly = d.hourlyActivityByRegion || [];
        if (hourly.length > 0) {
          var hourLabels = '';
          for (var hl = 0; hl < 24; hl += 3) {
            hourLabels += '<div style="flex:3;text-align:left;font-size:9px" title="' + hl + ':00 UTC">' + hl + '</div>';
          }
          var hourlyRows = hourly.map(function(ha) {
            var maxV = Math.max.apply(null, ha.hours.concat([1]));
            var cells = ha.hours.map(function(v, h) {
              var alpha = v > 0 ? (0.12 + 0.88 * (v / maxV)) : 0;
              var bg = v > 0 ? 'background:var(--accent);opacity:' + alpha.toFixed(2) : 'background:var(--border)';
              return '<div style="flex:1;height:16px;' + bg + '" title="' + esc(ha.region) + ' ' + h + ':00 UTC — ' + v.toLocaleString() + ' msg' + (v === 1 ? '' : 's') + '"></div>';
            }).join('');
            return '<div style="display:flex;align-items:center;gap:6px;margin-bottom:3px">' +
              '<div style="width:110px;font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="' + esc(ha.region) + '"><code>' + esc(ha.region) + '</code></div>' +
              '<div style="flex:1;display:flex;gap:1px">' + cells + '</div>' +
              '</div>';
          }).join('');
          hourlyEl.innerHTML =
            '<h4 style="margin:0 0 4px">Activity by Hour of Day (' + hourly.length.toLocaleString() + ' region' + (hourly.length === 1 ? '' : 's') + ')</h4>' +
            '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">When during a typical day (UTC) each region is active, aggregated across every day in the window above. Reads best on 7d — color is normalized per region, so quiet and busy regions are both visible.</p>' +
            '<div style="display:flex;gap:6px;margin-bottom:4px">' +
              '<div style="width:110px"></div>' +
              '<div style="flex:1;display:flex">' + hourLabels + '</div>' +
            '</div>' +
            hourlyRows;
        } else {
          hourlyEl.innerHTML =
            '<h4 style="margin:0 0 4px">Activity by Hour of Day</h4>' +
            '<p class="text-muted" style="margin:0;font-size:0.85em">No scoped messages in this window to chart by hour of day.</p>';
        }
      }

      // Region utilization: how much of the configured hashRegions list
      // has never actually matched anything — all-time (not window-scoped),
      // so it doesn't fluctuate with the 1h/24h/7d selector above. Only
      // shown when the server config has hashRegions configured.
      var utilEl = document.getElementById('scopes-utilization');
      if (utilEl) {
        var configured = d.configuredRegions || 0;
        if (configured > 0) {
          var unused = d.unusedRegions || [];
          var usedCount = configured - unused.length;
          var unusedPct = (unused.length / configured * 100).toFixed(1);
          var listHtml = unused.map(function(name) { return esc(name); }).join(', ');
          setSectionHtml(utilEl, detailsSection(
            'Region Utilization (' + usedCount.toLocaleString() + ' of ' + configured.toLocaleString() + ' used)',
            'All-time, not limited to the window above — has this configured region ever matched a message still in retention?',
            '<p style="margin:0 0 8px">' +
              '<strong>' + usedCount.toLocaleString() + '</strong> of <strong>' + configured.toLocaleString() + '</strong> configured regions have matched at least once' +
              (unused.length > 0 ? ' — <strong>' + unused.length.toLocaleString() + '</strong> (' + unusedPct + '%) have never matched anything.' : '.') +
            '</p>' +
            (unused.length > 0 ?
              '<details data-key="unused-regions"><summary style="cursor:pointer">Show ' + unused.length.toLocaleString() + ' unused region' + (unused.length === 1 ? '' : 's') + '</summary>' +
              '<div class="mono text-muted" style="font-size:11px;margin-top:8px;max-height:200px;overflow-y:auto;line-height:1.6">' + listHtml + '</div></details>'
              : ''),
            'region-utilization'
          ));
        } else {
          utilEl.innerHTML = '';
        }
      }

      // Renders a "<region> -> [node links]" breakdown into a container.
      // Shared by "Repeaters by Region" (transported_scopes — who relayed
      // traffic for this region) and "Nodes Running This Region"
      // (default_scope — who is actually configured with it). Both are
      // all-time, not limited to the window selector above.
      function renderRegionNodeGroups(elId, title, description, groups, unitLabel) {
        var el = document.getElementById(elId);
        if (!el) return;
        // Always show the heading — hiding the whole section when `groups`
        // is empty (vs. showing an empty-state message) reads as "this
        // feature doesn't exist" rather than "no data yet", which is
        // especially confusing right after a restart while the neighbor
        // graph + path resolution are still catching back up.
        var body = (groups && groups.length)
          ? groups.map(function(g) {
              var links = g.repeaters.map(function(rp) {
                return '<a href="#/nodes/' + encodeURIComponent(rp.publicKey) + '">' + esc(rp.name) + '</a>';
              }).join(', ');
              return '<details style="margin-bottom:6px" data-key="region:' + esc(g.region) + '">' +
                '<summary style="cursor:pointer"><code>' + esc(g.region) + '</code> — ' + g.count.toLocaleString() + ' ' + unitLabel + (g.count === 1 ? '' : 's') + '</summary>' +
                '<div class="text-muted" style="font-size:11px;margin-top:6px;margin-left:12px;max-height:200px;overflow-y:auto;line-height:1.8">' + links + '</div>' +
                '</details>';
            }).join('')
          : '<p class="text-muted" style="font-size:0.85em">No data yet — path resolution catches up gradually as traffic flows; check back in a few minutes.</p>';
        var count = groups ? groups.length : 0;
        setSectionHtml(el, detailsSection(
          esc(title) + ' (' + count.toLocaleString() + ' region' + (count === 1 ? '' : 's') + ')',
          esc(description),
          body,
          elId
        ));
      }

      renderRegionNodeGroups('scopes-repeaters', 'Repeaters by Region',
        'All-time, not limited to the window above — which repeaters have relayed traffic carrying each region scope. A region carried by only 1 repeater is a single point of failure for that area.',
        d.repeatersByRegion, 'repeater');

      // Bridge repeaters: RepeatersByRegion inverted — repeaters relaying
      // for MORE than one region are the mesh's literal backbone nodes.
      var bridgeEl = document.getElementById('scopes-bridges');
      if (bridgeEl) {
        var bridges = d.bridgeRepeaters || [];
        var bridgeBody;
        if (bridges.length > 0) {
          var bridgeRows = bridges.map(function(b) {
            var regionList = b.regions.map(function(r) { return '<code>' + esc(r) + '</code>'; }).join(', ');
            return '<tr>' +
              '<td><a href="#/nodes/' + encodeURIComponent(b.publicKey) + '">' + esc(b.name) + '</a></td>' +
              '<td>' + b.count + '</td>' +
              '<td>' + regionList + '</td>' +
              '</tr>';
          }).join('');
          bridgeBody = '<table class="data-table analytics-table">' +
            '<thead><tr><th>Repeater</th><th># Regions</th><th>Regions</th></tr></thead>' +
            '<tbody>' + bridgeRows + '</tbody>' +
            '</table>';
        } else {
          // Needs a repeater confirmed in 2+ regions — a stricter bar than
          // "Repeaters by Region" above, so it's normal for this to stay
          // empty longer, especially right after a restart.
          bridgeBody = '<p class="text-muted" style="font-size:0.85em">No bridge repeaters found yet — this needs a repeater confirmed in 2+ different regions, which takes longer to accumulate than the single-region data above.</p>';
        }
        setSectionHtml(bridgeEl, detailsSection(
          'Bridge Repeaters (' + bridges.length.toLocaleString() + ')',
          'All-time — repeaters that have relayed traffic for more than one region. These connect otherwise-separate regional communities; losing one can split the mesh\'s regional coverage.',
          bridgeBody,
          'bridge-repeaters'
        ));
      }

      renderRegionNodeGroups('scopes-origin-nodes', 'Nodes Running This Region',
        'All-time — nodes whose OWN configured scope is this region (not just relaying it for others). This is a much smaller, more specific set than "Repeaters by Region" above.',
        d.originatingNodesByRegion, 'node');

      // Nodes Without a Default Scope: the flip side of "Nodes Running
      // This Region" — every node with no default_scope at all, i.e.
      // never configured a hashRegions region for itself. Computed
      // client-side from the full node list (default_scope isn't
      // windowed/aggregated server-side the way the other scope-stats
      // fields are) — `allNodes` was already fetched above for the
      // highlights row, reused here rather than fetched again. Role
      // filter + name/key search re-filter this same in-memory node list
      // without a re-fetch; the debounced search input restores
      // focus/caret after each re-render since the whole section (search
      // box included) is rebuilt via setSectionHtml.
      var noScopeEl = document.getElementById('scopes-no-scope');
      if (noScopeEl) {
        if (allNodes) {
          renderNoScopeSection(allNodes);
        } else {
          setSectionHtml(noScopeEl, detailsSection('Nodes Without a Default Scope', null, '<p class="text-muted">Failed to load.</p>', 'no-scope'));
        }
      }

      // Repeaters Never Relaying Any Scope: distinct from the section
      // above — a repeater's own default_scope and whether it has ever
      // relayed ANYONE's region-scoped traffic are different signals (see
      // computeRepeatersNeverRelayingScope doc comment). Reuses the same
      // `allNodes` fetched above.
      var neverRelayEl = document.getElementById('scopes-never-relay-scope');
      if (neverRelayEl && !allNodes) {
        setSectionHtml(neverRelayEl, detailsSection('Repeaters Never Relaying Any Scope', null, '<p class="text-muted">Failed to load.</p>', 'never-relay'));
      } else if (neverRelayEl) {
        try {
          var neverRelay = computeRepeatersNeverRelayingScope(allNodes, 100);
          var neverRelayBody;
          if (neverRelay.sortedCapped.length > 0) {
            var neverRelayRows = neverRelay.sortedCapped.map(function(n) {
              return '<tr><td><a href="#/nodes/' + encodeURIComponent(n.public_key) + '">' + esc(n.name || n.public_key) + '</a></td>' +
                '<td>' + esc(n.role || '—') + '</td>' +
                '<td>' + (n.relay_count_24h || 0).toLocaleString() + '</td>' +
                '<td>' + timeAgo(n.last_seen) + '</td></tr>';
            }).join('');
            neverRelayBody = '<table class="data-table analytics-table">' +
              '<thead><tr><th>Repeater</th><th>Role</th><th>Relays (24h)</th><th>Last Seen</th></tr></thead>' +
              '<tbody>' + neverRelayRows + '</tbody>' +
              '</table>';
          } else {
            neverRelayBody = '<p class="text-muted" style="font-size:0.85em">Every known repeater/room has relayed at least one region-scoped packet.</p>';
          }
          setSectionHtml(neverRelayEl, detailsSection(
            'Repeaters Never Relaying Any Scope (' + neverRelay.total.toLocaleString() + ')',
            'Repeater/room nodes that have never carried a single region-scoped (TRANSPORT_FLOOD/DIRECT) packet, ever — not the same set as "no default_scope" above: a repeater\'s hashRegions config can let it relay for others even when its own adverts never carry a matching transport code. ' +
              'Sorted by current relay volume — the busiest ones are the most consequential to configure first' + (neverRelay.truncated ? ' (showing the top ' + neverRelay.sortedCapped.length + ')' : '') + '.',
            neverRelayBody,
            'never-relay'
          ));
        } catch (e) {
          setSectionHtml(neverRelayEl, detailsSection('Repeaters Never Relaying Any Scope', null, '<p class="text-muted">Failed to load.</p>', 'never-relay'));
        }
      }
    }


    load(selectedWindow);

    // Fix 6: auto-refresh every 60s
    _stopScopesRefresh();
    _scopesRefreshTimer = setInterval(function() {
      if (_currentTab !== 'scopes') { _stopScopesRefresh(); return; }
      var cur = document.getElementById('analyticsContent');
      if (!cur) { _stopScopesRefresh(); return; }
      load(selectedWindow);
    }, 60000);
  }

  // Pure (no DOM/network) computation behind the Scopes tab's "Nodes
  // Without a Default Scope" section: filters to nodes with no
  // default_scope, tallies by role (most common first — always over the
  // FULL unfiltered set, so the role filter buttons' counts don't shift
  // as you filter), then optionally narrows further by `opts.role` and/or
  // a case-insensitive `opts.q` match against name or public_key, and
  // returns a most-recently-active-first slice capped at `cap` rows.
  // `total` is always the full unfiltered count; `filteredTotal` reflects
  // opts.
  function computeNodesWithoutScope(allNodes, cap, opts) {
    opts = opts || {};
    var noScopeNodes = allNodes.filter(function(n) { return !n.default_scope; });
    var roleCounts = {};
    noScopeNodes.forEach(function(n) {
      var r = n.role || 'unknown';
      roleCounts[r] = (roleCounts[r] || 0) + 1;
    });
    var roleSummary = Object.keys(roleCounts)
      .sort(function(a, b) { return roleCounts[b] - roleCounts[a]; })
      .map(function(r) { return { role: r, count: roleCounts[r] }; });

    var filtered = noScopeNodes;
    if (opts.role) {
      filtered = filtered.filter(function(n) { return (n.role || 'unknown') === opts.role; });
    }
    if (opts.q) {
      var q = String(opts.q).toLowerCase();
      filtered = filtered.filter(function(n) {
        return ((n.name || '').toLowerCase().indexOf(q) !== -1) ||
          ((n.public_key || '').toLowerCase().indexOf(q) !== -1);
      });
    }

    var sorted = filtered.slice().sort(function(a, b) {
      return new Date(b.last_seen || 0) - new Date(a.last_seen || 0);
    });
    return {
      total: noScopeNodes.length,
      filteredTotal: filtered.length,
      roleSummary: roleSummary,
      sortedCapped: sorted.slice(0, cap),
      truncated: sorted.length > cap,
    };
  }

  // Pure (no DOM/network) computation behind the Scopes tab's "Repeaters
  // Never Relaying Any Scope" section. Distinct from
  // computeNodesWithoutScope: a repeater's OWN default_scope (does it send
  // its own scoped adverts?) and transported_scopes (has it ever relayed
  // ANYONE's region-scoped traffic, ever?) are different signals — on
  // stg.meshview.dk ~14% of no-default_scope repeaters still relay scope
  // for others (their hashRegions config enables relaying even though
  // their own adverts never carry a matching transport code). Only
  // repeater/room nodes can relay at all — every other role is filtered
  // out rather than silently 0-counted alongside them. Sorted by
  // relay_count_24h descending: the busiest unconfigured repeaters are the
  // most consequential ones to fix first.
  function computeRepeatersNeverRelayingScope(allNodes, cap) {
    var candidates = allNodes.filter(function(n) {
      return (n.role === 'repeater' || n.role === 'room') &&
        (!n.transported_scopes || n.transported_scopes.length === 0);
    });
    var sorted = candidates.slice().sort(function(a, b) {
      return Number(b.relay_count_24h || 0) - Number(a.relay_count_24h || 0);
    });
    return {
      total: candidates.length,
      sortedCapped: sorted.slice(0, cap),
      truncated: sorted.length > cap,
    };
  }

  // Repeaters relaying unscoped (route_type FLOOD) traffic, sorted by volume
  // — a quick, no-new-backend-work way to surface an existing but previously
  // unsurfaced metric (unscoped_relay_count_24h, see openapi.go). A little
  // unscoped traffic is normal and expected (flood.max.unscoped 0 isn't
  // realistic in practice) — this view is for spotting disproportionate
  // volume, not for zero-tolerance. Phase 1 of the "foreign traffic"
  // investigation: cross-referencing which of this
  // unscoped volume actually traces back to a foreign-origin sender (via the
  // geo_filter-derived `foreign` flag on nodes) needs foreign_advert data to
  // accumulate first, since it's only set going forward from when geo_filter
  // was configured — not backfilled for existing history.
  async function renderForeignTrafficTab(el) {
    function pct(n, total) {
      if (!total) return '—';
      return (n / total * 100).toFixed(1) + '%';
    }
    async function load() {
      try {
        const nodesResp = await fetchAllNodes('', { ttl: CLIENT_TTL.nodeList });
        const allNodes = nodesResp.nodes || nodesResp;
        const relays = allNodes.filter(function(n) {
          return (n.role === 'repeater' || n.role === 'room') && (n.unscoped_relay_count_24h || 0) > 0;
        });
        relays.sort(function(a, b) { return Number(b.unscoped_relay_count_24h || 0) - Number(a.unscoped_relay_count_24h || 0); });
        const foreignCount = allNodes.filter(function(n) { return n.foreign; }).length;

        let body;
        if (relays.length > 0) {
          const rows = relays.map(function(n) {
            const total = n.relay_count_24h || 0;
            const unscoped = n.unscoped_relay_count_24h || 0;
            return '<tr>' +
              '<td><a href="#/nodes/' + encodeURIComponent(n.public_key) + '">' + esc(n.name || n.public_key) + '</a></td>' +
              '<td>' + esc(n.role) + '</td>' +
              '<td>' + unscoped.toLocaleString() + '</td>' +
              '<td>' + total.toLocaleString() + '</td>' +
              '<td>' + pct(unscoped, total) + '</td>' +
              '</tr>';
          }).join('');
          body = '<table class="data-table analytics-table">' +
            '<thead><tr><th>Repeater</th><th>Role</th><th>Unscoped Relays (24h)</th><th>Total Relays (24h)</th><th>% Unscoped</th></tr></thead>' +
            '<tbody>' + rows + '</tbody>' +
            '</table>';
        } else {
          body = '<p class="text-muted" style="font-size:0.85em">No repeater has relayed an unscoped flood packet in the last 24 hours.</p>';
        }

        const foreignNote = foreignCount > 0
          ? foreignCount.toLocaleString() + ' node(s) so far carry an advertised GPS position outside the configured geo_filter — cross-referencing which rows below actually trace back to those is a planned follow-up once more accumulates.'
          : 'geo_filter is configured, but no foreign-origin node has advertised since — check back once traffic accumulates to cross-reference which of the unscoped relays below trace back to a foreign sender.';

        // Foreign-flagged nodes so far (#730 foreign_advert), newest-heard
        // first. Only set going forward from when geo_filter was
        // configured — this list grows as nodes send their next ADVERT.
        const foreignNodes = allNodes.filter(function(n) { return n.foreign; });
        // Parse last_seen once per node (decorate-sort-undecorate) instead
        // of twice per comparator call — cheap either way at this list
        // size, but avoids the repeated Date() allocations on principle.
        foreignNodes
          .map(function(n) { return { n: n, t: new Date(n.last_seen || 0).getTime() }; })
          .sort(function(a, b) { return b.t - a.t; })
          .forEach(function(entry, i) { foreignNodes[i] = entry.n; });
        let foreignNodesBody;
        if (foreignNodes.length > 0) {
          const foreignRows = foreignNodes.map(function(n) {
            const lat = typeof n.lat === 'number' ? n.lat.toFixed(3) : '—';
            const lon = typeof n.lon === 'number' ? n.lon.toFixed(3) : '—';
            return '<tr>' +
              '<td><a href="#/nodes/' + encodeURIComponent(n.public_key) + '">' + esc(n.name || n.public_key) + '</a></td>' +
              '<td>' + esc(n.role || '—') + '</td>' +
              '<td>' + lat + ', ' + lon + '</td>' +
              '<td>' + timeAgo(n.first_seen) + '</td>' +
              '<td>' + timeAgo(n.last_seen) + '</td>' +
              '</tr>';
          }).join('');
          foreignNodesBody = '<table class="data-table analytics-table">' +
            '<thead><tr><th>Node</th><th>Role</th><th>Lat, Lon</th><th>First Seen</th><th>Last Seen</th></tr></thead>' +
            '<tbody>' + foreignRows + '</tbody>' +
            '</table>';
        } else {
          foreignNodesBody = '<p class="text-muted" style="font-size:0.85em">None yet — a node is only listed here after it advertises with a GPS position outside the configured geo_filter.</p>';
        }

        el.innerHTML =
          '<h3 style="margin:0 0 4px">Foreign Traffic</h3>' +
          '<p class="text-muted" style="margin:0 0 16px;font-size:0.85em">' +
            'Repeaters relaying unscoped (route_type FLOOD) packets in the last 24 hours, sorted by volume. ' +
            'A small amount of unscoped traffic is normal — <code>flood.max.unscoped</code> caps it rather than blocking it outright. Disproportionate volume or % here is what\'s worth investigating on that specific node. ' +
            foreignNote +
          '</p>' +
          '<h4 style="margin:0 0 4px">Foreign-Flagged Nodes (' + foreignNodes.length.toLocaleString() + ')</h4>' +
          '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">' +
            'Nodes whose most recent advertised GPS position fell outside the configured geo_filter — self-reported GPS from the node\'s own ADVERT. ' +
            'A FLOOD packet can legitimately travel many hops through a dense mesh, so a node whose traffic you can trace through a real local relay chain (check its Trace) is genuinely reachable via that chain — that\'s foreign traffic actually entering the mesh, not a data artifact.' +
          '</p>' +
          foreignNodesBody +
          '<h4 style="margin:24px 0 4px">Repeaters Relaying Unscoped Traffic</h4>' +
          body;
      } catch (e) {
        el.innerHTML = '<p class="text-muted">Failed to load repeater relay stats.</p>';
      }
    }

    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading repeater relay stats…</div>';
    await load();

    // Auto-refresh every 60s while this tab is active (matches Roles/Scopes).
    _stopForeignTrafficRefresh();
    _foreignTrafficRefreshTimer = setInterval(function() {
      if (_currentTab !== 'foreign-traffic') { _stopForeignTrafficRefresh(); return; }
      var cur = document.getElementById('analyticsContent');
      if (!cur) { _stopForeignTrafficRefresh(); return; }
      load();
    }, 60000);
  }

  // Wardriving analytics: activity/entry-point/coverage for the
  // #wardriving channel (MeshMapper's community wardriving convention —
  // see /api/analytics/wardriving doc). MeshMapper's on-air message is an
  // anonymous per-session token by default, not the sender's live GPS
  // (that goes to MeshMapper's own server via a separate API call we
  // never see) — so sender location can't be plotted here. What IS
  // reliable: which repeater first relayed each message (Entry Points,
  // same path[0]/unique_prefix discipline as the Foreign Traffic tab),
  // and which observer stations — fixed, known locations — actually
  // heard the traffic (Coverage).
  async function renderWardrivingTab(el) {
    var winKey = 'wardriving_window';
    var selectedWindow = (typeof sessionStorage !== 'undefined' && sessionStorage.getItem(winKey)) || '24h';

    function pct(n, total) {
      if (!total) return '—';
      return (n / total * 100).toFixed(1) + '%';
    }

    function cardsHtml(d) {
      return [
        { label: 'Messages', value: d.totalMessages.toLocaleString(), note: 'window: ' + d.window },
        { label: 'Active Senders', value: (d.topSenders || []).length.toLocaleString(), note: null },
        { label: 'Entry-Point Repeaters', value: (d.entryPoints || []).length.toLocaleString(), note: 'distinct path[0] prefixes' },
        { label: 'Observers Reached', value: (d.observers || []).length.toLocaleString(), note: null },
        { label: 'Avg SNR', value: (d.avgSnr != null ? d.avgSnr.toFixed(1) + ' dB' : '—'), note: null },
        { label: 'Avg RSSI', value: (d.avgRssi != null ? d.avgRssi.toFixed(1) + ' dBm' : '—'), note: null },
        { label: 'Sessions', value: (d.sessions || []).length.toLocaleString(), note: '15min+ gap starts a new one' },
        { label: 'GPS Shared', value: (d.gpsShares || []).length.toLocaleString(), note: 'senders who shared a position' },
      ].map(function(c) {
        return '<div class="stat-card"><div class="stat-value">' + c.value + '</div>' +
          '<div class="stat-label">' + c.label + '</div>' +
          (c.note ? '<div class="stat-note text-muted" style="font-size:11px">' + c.note + '</div>' : '') +
          '</div>';
      }).join('');
    }

    // Single-line SVG time series — matches the Scopes tab's two-line chart style.
    function chartHtml(ts) {
      if (!ts || ts.length <= 1) {
        return '<p class="text-muted" style="font-size:0.85em">Insufficient data points to chart — wait for more wardriving activity in this window.</p>';
      }
      var vals = ts.map(function(p) { return p.count; });
      var maxVal = Math.max(1, Math.max.apply(null, vals));
      var W = 800, H = 160, padL = 44, padT = 10, padR = 10;
      var plotW = W - padL - padR, plotH = H - 24 - padT;
      var n = ts.length;
      var pts = vals.map(function(v, i) {
        var x = padL + i * plotW / Math.max(n - 1, 1);
        var y = padT + plotH - (v / maxVal) * plotH;
        return x.toFixed(1) + ',' + y.toFixed(1);
      }).join(' ');
      var grid = '';
      for (var gi = 0; gi <= 4; gi++) {
        var gy = padT + plotH * gi / 4;
        var gv = Math.round(maxVal * (4 - gi) / 4);
        grid += '<line x1="' + padL + '" y1="' + gy.toFixed(1) + '" x2="' + (W - padR) + '" y2="' + gy.toFixed(1) + '" stroke="var(--border)" stroke-dasharray="2"/>';
        grid += '<text x="' + (padL - 4) + '" y="' + (gy + 4).toFixed(1) + '" text-anchor="end" font-size="9" fill="var(--text-muted)">' + gv + '</text>';
      }
      return '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;max-height:' + H + 'px" role="img" aria-label="Wardriving message volume over time">' +
        grid +
        '<polyline points="' + pts + '" fill="none" stroke="var(--accent)" stroke-width="2"/>' +
        '</svg>';
    }

    // Signal quality line chart — scaled to the data's own min/max (unlike
    // chartHtml's 0-baseline) since SNR and RSSI have no natural zero floor.
    function signalChartHtml(sig, key, color, ariaLabel) {
      if (!sig || sig.length <= 1) {
        return '<p class="text-muted" style="font-size:0.85em">Insufficient data points to chart.</p>';
      }
      var vals = sig.map(function(p) { return p[key]; });
      var minVal = Math.min.apply(null, vals);
      var maxVal = Math.max.apply(null, vals);
      if (minVal === maxVal) { minVal -= 1; maxVal += 1; }
      var W = 800, H = 140, padL = 44, padT = 10, padR = 10, padB = 20;
      var plotW = W - padL - padR, plotH = H - padB - padT;
      var n = sig.length;
      var pts = vals.map(function(v, i) {
        var x = padL + i * plotW / Math.max(n - 1, 1);
        var y = padT + plotH - ((v - minVal) / (maxVal - minVal)) * plotH;
        return x.toFixed(1) + ',' + y.toFixed(1);
      }).join(' ');
      var grid = '';
      for (var gi = 0; gi <= 3; gi++) {
        var gy = padT + plotH * gi / 3;
        var gv = maxVal - (maxVal - minVal) * gi / 3;
        grid += '<line x1="' + padL + '" y1="' + gy.toFixed(1) + '" x2="' + (W - padR) + '" y2="' + gy.toFixed(1) + '" stroke="var(--border)" stroke-dasharray="2"/>';
        grid += '<text x="' + (padL - 4) + '" y="' + (gy + 4).toFixed(1) + '" text-anchor="end" font-size="9" fill="var(--text-muted)">' + gv.toFixed(1) + '</text>';
      }
      return '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;max-height:' + H + 'px" role="img" aria-label="' + ariaLabel + '">' +
        grid +
        '<polyline points="' + pts + '" fill="none" stroke="' + color + '" stroke-width="2"/>' +
        '</svg>';
    }

    function formatSessionDuration(mins) {
      if (mins < 1) return '<1m';
      if (mins < 60) return Math.round(mins) + 'm';
      var h = Math.floor(mins / 60), m = Math.round(mins % 60);
      return h + 'h ' + m + 'm';
    }

    // Sender name → clickable trigger for the per-message drill-down (see
    // attachSenderDrilldown). data-wd-since/until scope the drill-down to
    // an exact range (e.g. one session); omitted, it covers the whole
    // window for that sender.
    function senderTriggerHtml(sender, since, until) {
      var attrs = 'data-wd-sender="' + esc(sender) + '"';
      if (since) attrs += ' data-wd-since="' + esc(since) + '"';
      if (until) attrs += ' data-wd-until="' + esc(until) + '"';
      return '<button type="button" ' + attrs + ' aria-expanded="false" style="background:none;border:none;padding:0;margin:0;font:inherit;color:var(--link-color);cursor:pointer">' + esc(sender) + '</button>';
    }

    // Sessions — each sender's messages grouped into runs (backend splits
    // on any gap over 15 minutes). Pre-sorted most-recent-first by the API.
    function sessionsHtml(sessions) {
      if (!sessions || sessions.length === 0) {
        return '<p class="text-muted" style="font-size:0.85em">No wardriving sessions in this window.</p>';
      }
      var rows = sessions.map(function(s) {
        return '<tr><td>' + senderTriggerHtml(s.sender, s.startTime, s.endTime) + '</td>' +
          '<td>' + (typeof timeAgo === 'function' ? timeAgo(s.startTime) : s.startTime) + '</td>' +
          '<td>' + formatSessionDuration(s.durationMinutes) + '</td>' +
          '<td>' + s.messageCount.toLocaleString() + '</td>' +
          '<td>' + s.entryPointCount.toLocaleString() + '</td>' +
          '<td>' + s.observerCount.toLocaleString() + '</td></tr>';
      }).join('');
      return '<table class="data-table analytics-table" data-wd-cols="6">' +
        '<thead><tr><th>Sender</th><th>Started</th><th>Duration</th><th>Messages</th><th>Entry Points</th><th>Observers</th></tr></thead>' +
        '<tbody>' + rows + '</tbody>' +
        '</table>';
    }

    function sendersHtml(senders, totalMessages) {
      if (!senders || senders.length === 0) {
        return '<p class="text-muted" style="font-size:0.85em">No wardriving messages in this window.</p>';
      }
      var rows = senders.map(function(s) {
        return '<tr><td>' + senderTriggerHtml(s.sender) + '</td><td>' + s.count.toLocaleString() + '</td><td>' + pct(s.count, totalMessages) + '</td></tr>';
      }).join('');
      return '<table class="data-table analytics-table" data-wd-cols="3">' +
        '<thead><tr><th>Sender</th><th>Messages</th><th>% of Total</th></tr></thead>' +
        '<tbody>' + rows + '</tbody>' +
        '</table>';
    }

    // Entry Points — resolve raw path[0] hash prefixes to repeater names,
    // same unique_prefix-only discipline as the Foreign Traffic tab (a
    // non-unique_prefix resolution is a genuine hash collision across
    // multiple candidate repeaters — folded into "Ambiguous" rather than
    // guessing).
    async function entryPointsHtml(prefixes) {
      if (!prefixes || prefixes.length === 0) {
        return '<p class="text-muted" style="font-size:0.85em">No wardriving messages with a relay path in this window.</p>';
      }
      try {
        var resp = await api('/resolve-hops?hops=' + prefixes.map(function(p) { return p.prefix; }).join(','), { ttl: CLIENT_TTL.nodeDetail }).catch(function() { return null; });
        var resolved = (resp && resp.resolved) || {};
        var totalObs = prefixes.reduce(function(sum, p) { return sum + p.observationCount; }, 0);
        var named = [];
        var ambiguousObs = 0, ambiguousMsgs = 0;
        prefixes.forEach(function(p) {
          var r = resolved[p.prefix];
          if (r && r.confidence === 'unique_prefix') {
            named.push({ name: r.name, pubkey: r.pubkey, obs: p.observationCount, msgs: p.messageCount });
          } else {
            ambiguousObs += p.observationCount;
            ambiguousMsgs += p.messageCount;
          }
        });
        named.sort(function(a, b) { return b.obs - a.obs; });
        var rows = named.map(function(e) {
          return '<tr><td><a href="#/nodes/' + encodeURIComponent(e.pubkey) + '">' + esc(e.name) + '</a></td>' +
            '<td>' + e.obs.toLocaleString() + '</td>' +
            '<td>' + pct(e.obs, totalObs) + '</td>' +
            '<td>' + e.msgs.toLocaleString() + '</td></tr>';
        }).join('') + (ambiguousObs > 0
          ? '<tr><td class="text-muted">Ambiguous (hash prefix collides across multiple candidate repeaters)</td>' +
            '<td>' + ambiguousObs.toLocaleString() + '</td><td>' + pct(ambiguousObs, totalObs) + '</td><td>' + ambiguousMsgs.toLocaleString() + '</td></tr>'
          : '');
        return (named.length > 0 || ambiguousObs > 0)
          ? '<table class="data-table analytics-table">' +
            '<thead><tr><th>Entry-Point Repeater</th><th>Observations</th><th>% of Observations</th><th>Distinct Messages</th></tr></thead>' +
            '<tbody>' + rows + '</tbody>' +
            '</table>'
          : '<p class="text-muted" style="font-size:0.85em">No traceable relay path yet for any wardriving message.</p>';
      } catch (e) {
        return '<p class="text-muted">Failed to resolve entry points.</p>';
      }
    }

    function observersHtml(observers) {
      if (!observers || observers.length === 0) {
        return '<p class="text-muted" style="font-size:0.85em">No observer has heard wardriving traffic in this window.</p>';
      }
      var totalObsCount = observers.reduce(function(sum, o) { return sum + o.observationCount; }, 0);
      var rows = observers.map(function(o) {
        var loc = (o.lat != null && o.lon != null) ? (o.lat.toFixed(2) + ', ' + o.lon.toFixed(2)) : '—';
        return '<tr><td>' + esc(o.observerName) + '</td>' +
          '<td>' + esc(o.iata || '—') + '</td>' +
          '<td>' + loc + '</td>' +
          '<td>' + o.observationCount.toLocaleString() + '</td>' +
          '<td>' + pct(o.observationCount, totalObsCount) + '</td>' +
          '<td>' + o.messageCount.toLocaleString() + '</td></tr>';
      }).join('');
      return '<table class="data-table analytics-table">' +
        '<thead><tr><th>Observer</th><th>Region</th><th>Lat, Lon</th><th>Observations</th><th>% of Observations</th><th>Distinct Messages</th></tr></thead>' +
        '<tbody>' + rows + '</tbody>' +
        '</table>';
    }

    function mapLinkHtml(lat, lon) {
      return '<a href="#/map?lat=' + encodeURIComponent(lat) + '&lon=' + encodeURIComponent(lon) + '&zoom=15" target="_blank" rel="noopener">' +
        lat.toFixed(5) + ', ' + lon.toFixed(5) + '</a>';
    }

    // GPS Sharing — some wardriving clients append plaintext "<lat>,<lon>"
    // after the standard token; this is a deliberate choice by that
    // sender's client to share their position in-band, confirmed
    // empirically against live traffic, not something CoreScope infers.
    function gpsSharesHtml(shares) {
      if (!shares || shares.length === 0) {
        return '<p class="text-muted" style="font-size:0.85em">No sender has shared an explicit position in this window.</p>';
      }
      var rows = shares.map(function(s) {
        return '<tr><td>' + esc(s.sender) + '</td>' +
          '<td>' + mapLinkHtml(s.lat, s.lon) + '</td>' +
          '<td>' + s.messageCount.toLocaleString() + '</td>' +
          '<td>' + (typeof timeAgo === 'function' ? timeAgo(s.lastSeen) : s.lastSeen) + '</td></tr>';
      }).join('');
      return '<table class="data-table analytics-table">' +
        '<thead><tr><th>Sender</th><th>Position (most recent)</th><th>Times Shared</th><th>Last Seen</th></tr></thead>' +
        '<tbody>' + rows + '</tbody>' +
        '</table>';
    }

    function attachWindowButtons() {
      el.querySelectorAll('[data-wdwin]').forEach(function(btn) {
        btn.addEventListener('click', function() {
          selectedWindow = btn.dataset.wdwin;
          if (typeof sessionStorage !== 'undefined') sessionStorage.setItem(winKey, selectedWindow);
          load(selectedWindow);
        });
      });
    }

    // Per-message drill-down for one sender (whole window, or one exact
    // since/until range when opened from a Sessions row). Resolves every
    // distinct path[0..] prefix across the fetched messages in a single
    // /resolve-hops call — same unique_prefix-only discipline as the
    // aggregate Entry Points table (a non-unique_prefix match is shown as
    // the raw hash, not guessed).
    async function buildSenderDrilldownHtml(sender, since, until) {
      try {
        var qs = 'sender=' + encodeURIComponent(sender);
        if (since && until) {
          qs += '&since=' + encodeURIComponent(since) + '&until=' + encodeURIComponent(until);
        } else {
          qs += '&window=' + encodeURIComponent(selectedWindow);
        }
        var data = await api('/analytics/wardriving/sender-messages?' + qs, { ttl: 10000 });
        var messages = data.messages || [];
        if (messages.length === 0) {
          return '<p class="text-muted" style="font-size:0.85em;margin:8px 0">No individual messages found for this sender in range.</p>';
        }

        var allPrefixes = [];
        messages.forEach(function(m) {
          (m.pathPrefixes || []).forEach(function(p) { if (allPrefixes.indexOf(p) === -1) allPrefixes.push(p); });
        });
        var resolved = {};
        if (allPrefixes.length > 0) {
          try {
            var hopsResp = await api('/resolve-hops?hops=' + allPrefixes.join(','), { ttl: CLIENT_TTL.nodeDetail });
            resolved = (hopsResp && hopsResp.resolved) || {};
          } catch (e) { /* fall back to raw prefixes below */ }
        }
        function resolvePrefix(p) {
          var r = resolved[p];
          if (r && r.confidence === 'unique_prefix') return esc(r.name);
          return '<span class="text-muted">' + esc(p) + '</span>';
        }

        var rows = messages.map(function(m) {
          var pathStr = (m.pathPrefixes && m.pathPrefixes.length > 0)
            ? m.pathPrefixes.map(resolvePrefix).join(' → ')
            : '<span class="text-muted">—</span>';
          var obsStr = (m.observations && m.observations.length > 0)
            ? m.observations.map(function(o) {
                return esc(o.observerName) + ' (' + o.snr.toFixed(1) + 'dB / ' + o.rssi.toFixed(0) + 'dBm)';
              }).join(', ')
            : '<span class="text-muted">—</span>';
          var posStr = (m.lat != null && m.lon != null) ? mapLinkHtml(m.lat, m.lon) : '<span class="text-muted">—</span>';
          return '<tr><td>' + (typeof timeAgo === 'function' ? timeAgo(m.timestamp) : m.timestamp) + '</td>' +
            '<td>' + pathStr + '</td>' +
            '<td>' + obsStr + '</td>' +
            '<td>' + posStr + '</td></tr>';
        }).join('');
        return '<table class="data-table analytics-table" style="margin:4px 0 12px;font-size:0.85em">' +
          '<thead><tr><th>Time</th><th>Path (path[0] first)</th><th>Heard By (SNR / RSSI)</th><th>Shared Position</th></tr></thead>' +
          '<tbody>' + rows + '</tbody>' +
          '</table>';
      } catch (err) {
        return '<p style="color:var(--status-red);font-size:0.85em">Failed to load messages: ' + esc(String(err)) + '</p>';
      }
    }

    // Clicking a sender name toggles an inline expansion row directly
    // below it with that sender's individual messages. Only one expansion
    // stays open per table at a time, to keep the tab from growing
    // unbounded if a user clicks through several senders.
    function attachSenderDrilldown() {
      el.querySelectorAll('[data-wd-sender]').forEach(function(btn) {
        btn.addEventListener('click', async function() {
          var tr = btn.closest('tr');
          if (!tr) return;
          var next = tr.nextElementSibling;
          if (next && next.hasAttribute('data-wd-expansion')) {
            next.remove();
            btn.setAttribute('aria-expanded', 'false');
            return;
          }
          var table = tr.closest('table');
          if (table) {
            table.querySelectorAll('[data-wd-expansion]').forEach(function(r) { r.remove(); });
            table.querySelectorAll('[data-wd-sender]').forEach(function(b) { b.setAttribute('aria-expanded', 'false'); });
          }
          var cols = (table && table.dataset.wdCols) || '1';
          var expansionRow = document.createElement('tr');
          expansionRow.setAttribute('data-wd-expansion', '1');
          expansionRow.innerHTML = '<td colspan="' + cols + '"><div class="text-muted" style="padding:8px">Loading messages…</div></td>';
          tr.parentNode.insertBefore(expansionRow, tr.nextSibling);
          btn.setAttribute('aria-expanded', 'true');
          var html = await buildSenderDrilldownHtml(btn.dataset.wdSender, btn.dataset.wdSince, btn.dataset.wdUntil);
          var cell = expansionRow.querySelector('td');
          if (cell) cell.innerHTML = html;
        });
      });
    }

    async function load(w) {
      var body;
      try {
        var d = await api('/analytics/wardriving?window=' + encodeURIComponent(w), { ttl: 30000 });
        var entryHtml = await entryPointsHtml(d.entryPoints || []);
        body =
          '<div id="wardrivingCards" class="stats-grid" style="margin-bottom:16px">' + cardsHtml(d) + '</div>' +
          '<div id="wardrivingChart" style="margin-bottom:16px">' + chartHtml(d.timeSeries) + '</div>' +
          '<h4 style="margin:16px 0 4px">Top Senders</h4>' +
          '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">Who\'s actively wardriving in this window, by message count.</p>' +
          '<div id="wardrivingSenders">' + sendersHtml(d.topSenders, d.totalMessages) + '</div>' +
          '<h4 style="margin:24px 0 4px">Sessions</h4>' +
          '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">Each sender\'s messages grouped into distinct runs — a gap of more than 15 minutes starts a new session.</p>' +
          '<div id="wardrivingSessions">' + sessionsHtml(d.sessions) + '</div>' +
          '<h4 style="margin:24px 0 4px">Entry Points</h4>' +
          '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">Which local repeater first relayed each wardriving message — the hop closest to the origin (path[0]) across every observed copy.</p>' +
          '<div id="wardrivingEntryPoints">' + entryHtml + '</div>' +
          '<h4 style="margin:24px 0 4px">Coverage by Observer</h4>' +
          '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">Which observer stations actually heard wardriving traffic — observers sit at fixed, known locations, so this is the reliable half of "how far did it reach."</p>' +
          '<div id="wardrivingObservers">' + observersHtml(d.observers) + '</div>' +
          '<h4 style="margin:24px 0 4px">Signal Quality Trends</h4>' +
          '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">Average SNR and RSSI across every observation of wardriving traffic in each time bucket — a rough proxy for link quality, not tied to any one observer.</p>' +
          '<div id="wardrivingSignal" style="display:grid;grid-template-columns:1fr 1fr;gap:16px">' +
            '<div><div class="text-muted" style="font-size:0.8em;margin-bottom:4px">Avg SNR (dB)</div>' + signalChartHtml(d.signalTimeSeries, 'avgSnr', 'var(--accent)', 'Average SNR over time') + '</div>' +
            '<div><div class="text-muted" style="font-size:0.8em;margin-bottom:4px">Avg RSSI (dBm)</div>' + signalChartHtml(d.signalTimeSeries, 'avgRssi', 'var(--warning, #f39c12)', 'Average RSSI over time') + '</div>' +
          '</div>' +
          '<h4 style="margin:24px 0 4px">GPS Sharing</h4>' +
          '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">Senders whose client appended their own position after the standard token — a deliberate choice by that sender to share their location, not something inferred from the anonymous token itself.</p>' +
          '<div id="wardrivingGPSShares">' + gpsSharesHtml(d.gpsShares) + '</div>';
      } catch (err) {
        body = '<div class="text-center" style="color:var(--status-red);padding:20px">Failed to load wardriving stats: ' + esc(String(err)) + '</div>';
      }

      el.innerHTML =
        '<h3 style="margin:0 0 4px">Wardriving (#wardriving)</h3>' +
        '<p class="text-muted" style="margin:0 0 16px;font-size:0.85em">' +
          'Community coverage-mapping traffic on the #wardriving channel. MeshMapper\'s on-air ping carries an anonymous session token by default, not the sender\'s live GPS — coordinates go to MeshMapper\'s own server separately. What we can see: who\'s active, which repeater first relayed their signal, and which observer stations (fixed, known locations) actually heard it.' +
        '</p>' +
        '<div style="margin-bottom:12px">' +
          ['1h', '24h', '7d'].map(function(v) {
            return '<button class="tab-btn' + (selectedWindow === v ? ' active' : '') + '" data-wdwin="' + v + '">' + v + '</button>';
          }).join('') +
        '</div>' +
        body;
      attachWindowButtons();
      attachSenderDrilldown();
    }

    await load(selectedWindow);

    // Auto-refresh every 60s while this tab is active (matches Roles/Scopes/Foreign Traffic).
    _stopWardrivingRefresh();
    _wardrivingRefreshTimer = setInterval(function() {
      if (_currentTab !== 'wardriving') { _stopWardrivingRefresh(); return; }
      var cur = document.getElementById('analyticsContent');
      if (!cur) { _stopWardrivingRefresh(); return; }
      load(selectedWindow);
    }, 60000);
  }

  // #1085 — Roles tab (folded in from former /#/roles page).
  // Renders distribution of node roles + per-role clock-skew posture.
  // Auto-refreshes every 60s while the Roles tab is active (matches the
  // behavior of the former standalone roles-page.js).
  async function renderRolesTab(el) {
    el.innerHTML = '<div class="text-center text-muted" style="padding:40px">Loading roles…</div>';
    await _renderRolesTabBody(el);
    // (Re)start the 60s auto-refresh.
    _stopRolesRefresh();
    _rolesRefreshTimer = setInterval(function () {
      // Bail if the user navigated away from the Roles tab.
      if (_currentTab !== 'roles') { _stopRolesRefresh(); return; }
      var cur = document.getElementById('analyticsContent');
      if (!cur) { _stopRolesRefresh(); return; }
      _renderRolesTabBody(cur);
    }, 60000);
  }

  async function _renderRolesTabBody(el) {
    try {
      var data = await api('/analytics/roles', { ttl: CLIENT_TTL.analyticsRF });
      var roles = (data && data.roles) || [];
      var total = (data && data.totalNodes) || 0;
      if (!roles.length) {
        el.innerHTML = '<div class="text-center text-muted" style="padding:40px">No roles to show.</div>';
        return;
      }
      var maxCount = roles.reduce(function (m, r) { return Math.max(m, r.nodeCount || 0); }, 0) || 1;
      var rows = roles.map(function (r) {
        var pct = total > 0 ? ((r.nodeCount / total) * 100).toFixed(1) : '0.0';
        var barW = Math.round((r.nodeCount / maxCount) * 100);
        var sevCells =
          '<span title="OK (skew &lt; 5min)" style="color:var(--color-success,#0a0)">' + (r.okCount || 0) + '</span> / ' +
          '<span title="Warning (5min – 1h)" style="color:var(--color-warning,#e80)">' + (r.warningCount || 0) + '</span> / ' +
          '<span title="Critical (1h – 30d)" style="color:var(--color-error,#c00)">' + (r.criticalCount || 0) + '</span> / ' +
          '<span title="Absurd (&gt; 30d)" style="color:#a0a">' + (r.absurdCount || 0) + '</span> / ' +
          '<span title="No clock (&gt; 365d)" style="color:var(--text-muted)">' + (r.noClockCount || 0) + '</span>';
        return '' +
          '<tr data-role="' + esc(r.role) + '">' +
            '<td>' + _rolesEmoji(r.role) + ' <strong>' + esc(r.role) + '</strong></td>' +
            '<td style="text-align:right">' + r.nodeCount + '</td>' +
            '<td style="text-align:right">' + pct + '%</td>' +
            '<td style="min-width:140px">' +
              '<div style="background:var(--color-surface-2,#eee);height:10px;border-radius:5px;overflow:hidden">' +
                '<div style="background:var(--color-accent,#06c);width:' + barW + '%;height:100%"></div>' +
              '</div>' +
            '</td>' +
            '<td style="text-align:right">' + (r.withSkew || 0) + '</td>' +
            '<td style="text-align:right">' + _rolesFmtSec(r.medianAbsSkewSec || 0) + '</td>' +
            '<td style="text-align:right">' + _rolesFmtSec(r.meanAbsSkewSec || 0) + '</td>' +
            '<td style="white-space:nowrap">' + sevCells + '</td>' +
          '</tr>';
      }).join('');
      el.innerHTML =
        '<p class="text-muted" style="margin:0 0 12px 0">Distribution of node roles across the mesh, with per-role clock-skew posture.</p>' +
        '<div class="roles-summary" style="margin-bottom:12px;color:var(--text-muted)">' +
          '<strong>' + total + '</strong> nodes across <strong>' + roles.length + '</strong> roles' +
        '</div>' +
        '<table id="rolesTable" class="data-table analytics-table" style="width:100%">' +
          '<thead><tr>' +
            '<th>Role</th>' +
            '<th style="text-align:right">Count</th>' +
            '<th style="text-align:right">Share</th>' +
            '<th>Distribution</th>' +
            '<th style="text-align:right" title="Nodes with clock-skew samples">w/ Skew</th>' +
            '<th style="text-align:right" title="Median absolute skew">Median |skew|</th>' +
            '<th style="text-align:right" title="Mean absolute skew">Mean |skew|</th>' +
            '<th title="OK / Warning / Critical / Absurd / No-clock">Severity</th>' +
          '</tr></thead>' +
          '<tbody>' + rows + '</tbody>' +
        '</table>';
    } catch (err) {
      el.innerHTML = '<div class="text-center" style="color:var(--status-red);padding:40px">Failed to load roles: ' + esc(String(err.message || err)) + '</div>';
    }
  }

  registerPage('analytics', { init, destroy });
})();
