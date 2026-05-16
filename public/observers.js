/* === CoreScope — observers.js === */
'use strict';

(function () {
  let observers = [];
  let obsSkewMap = {};   // observerID → {offsetSec, samples}
  let obsStatsData = {}; // observerID → {packetsLast24h, packetsLast7d} — lazy-loaded by stats block
  let wsHandler = null;
  let refreshTimer = null;
  let regionChangeHandler = null;
  let clickHandler = null;
  let changeHandler = null;
  let inputHandler = null;
  let keydownHandler = null;
  let currentApp = null;
  let sortState = { col: null, dir: 'asc' };
  let hideStale = false;
  let hideOffline = false;

  var STATS_OPEN_KEY = 'meshcore-obs-stats-open';

  function loadSortState() {
    try {
      var s = localStorage.getItem('meshcore-obs-sort');
      if (s) sortState = JSON.parse(s);
    } catch (e) {}
  }

  function saveSortState() {
    try { localStorage.setItem('meshcore-obs-sort', JSON.stringify(sortState)); } catch (e) {}
  }

  function applySortState(arr) {
    if (!sortState.col) return arr;
    return arr.slice().sort(function (a, b) {
      var va, vb;
      switch (sortState.col) {
        case 'status': {
          var order = { 'health-green': 0, 'health-yellow': 1, 'health-red': 2 };
          va = order[healthStatus(a.last_seen).cls] ?? 3;
          vb = order[healthStatus(b.last_seen).cls] ?? 3;
          break;
        }
        case 'name':
          va = (a.name || a.id || '').toLowerCase();
          vb = (b.name || b.id || '').toLowerCase();
          break;
        case 'region':
          va = (a.iata || '').toLowerCase();
          vb = (b.iata || '').toLowerCase();
          break;
        case 'last_seen':
          va = a.last_seen ? new Date(a.last_seen).getTime() : 0;
          vb = b.last_seen ? new Date(b.last_seen).getTime() : 0;
          break;
        case 'last_packet':
          va = a.last_packet_at ? new Date(a.last_packet_at).getTime() : 0;
          vb = b.last_packet_at ? new Date(b.last_packet_at).getTime() : 0;
          break;
        case 'packets':
          va = a.packet_count || 0;
          vb = b.packet_count || 0;
          break;
        case 'packets_hr':
          va = a.packetsLastHour || 0;
          vb = b.packetsLastHour || 0;
          break;
        case 'clock_offset':
          va = obsSkewMap[a.id] && obsSkewMap[a.id].samples ? Math.abs(obsSkewMap[a.id].offsetSec || 0) : Infinity;
          vb = obsSkewMap[b.id] && obsSkewMap[b.id].samples ? Math.abs(obsSkewMap[b.id].offsetSec || 0) : Infinity;
          break;
        case 'uptime':
          va = a.uptime_secs != null ? a.uptime_secs * 1000 : (a.first_seen ? Date.now() - new Date(a.first_seen).getTime() : 0);
          vb = b.uptime_secs != null ? b.uptime_secs * 1000 : (b.first_seen ? Date.now() - new Date(b.first_seen).getTime() : 0);
          break;
        case 'sf':
          va = a.radio ? parseInt(a.radio.split(',')[2]) || 0 : 0;
          vb = b.radio ? parseInt(b.radio.split(',')[2]) || 0 : 0;
          break;
        default:
          return 0;
      }
      var cmp = va < vb ? -1 : va > vb ? 1 : 0;
      return sortState.dir === 'asc' ? cmp : -cmp;
    });
  }

  function loadVisibilityState() {
    try {
      hideStale   = localStorage.getItem('meshcore-obs-hide-stale')   === '1';
      hideOffline = localStorage.getItem('meshcore-obs-hide-offline') === '1';
    } catch (e) {}
  }

  function init(app) {
    loadSortState();
    loadVisibilityState();
    app.innerHTML = `
      <div class="observers-page">
        <div class="page-header">
          <h2>Observer Status</h2>
          <a href="#/compare" class="btn-icon" title="Compare observers" aria-label="Compare observers" style="text-decoration:none">🔍</a>
          <button class="btn-icon" data-action="obs-refresh" title="Refresh" aria-label="Refresh observers">🔄</button>
        </div>
        <div class="obs-stats-panel" id="obsStatsPanel">
          <div class="obs-stats-header" data-action="toggle-stats">
            <strong>📊 Observer Statistics</strong>
            <span class="obs-stats-toggle">▶</span>
          </div>
          <div class="obs-stats-body" id="obsStatsBody" style="max-height:0px">
            <div class="obs-stats-grid" id="obsStatsGrid"></div>
          </div>
        </div>
        <div class="obs-help">
          <div class="help-box">
            <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px;cursor:pointer" data-action="toggle-help">
              <strong>ℹ️ How to connect your observer to Cornmeister.nl</strong>
              <span class="help-toggle" style="font-size:14px;user-select:none">▶</span>
            </div>

            <div class="help-content" style="overflow:hidden;transition:max-height 0.3s ease;max-height:0px">
              <div class="text-muted" style="font-size:12px;margin-bottom:16px">
                Connect your node to the DutchMeshcore.nl MQTT ingestors to share raw packets with Cornmeister.nl.
              </div>

              <!-- Step 1: IATA -->
              <div style="margin-bottom:12px;padding:10px 12px;background:var(--surface-0);border:1px solid var(--border);border-radius:6px">
                <strong style="font-size:12px">📍 Step 1 — Your region (IATA code)</strong>
                <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:8px">
                  Cornmeister.nl groups observers by the IATA airport code nearest to them.
                  If your observer shows as <strong>Offline</strong> or doesn't appear in the list, you most likely haven't set this yet.
                  Pick your region below — the commands update automatically.
                </div>
                <select id="obsIataSelect" style="width:100%;padding:5px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text);font-size:12px;cursor:pointer">
                  <option value="AMS">AMS – Schiphol Airport</option>
                  <option value="DHR">DHR – Den Helder Airport</option>
                  <option value="EIN">EIN – Eindhoven Airport</option>
                  <option value="ENS">ENS – Enschede Airport</option>
                  <option value="GLZ">GLZ – Gilze-Rijen Airbase</option>
                  <option value="GRQ">GRQ – Eelde Airport</option>
                  <option value="LEY">LEY – Lelystad Airport</option>
                  <option value="LID">LID – Valkenburg Airbase (Closed)</option>
                  <option value="LWR">LWR – Leeuwarden Airbase</option>
                  <option value="MST">MST – Maastricht Airport</option>
                  <option value="QAR">QAR – Deelen Airbase</option>
                  <option value="RTM">RTM – Rotterdam Airport</option>
                  <option value="UDE">UDE – Volkel Airbase</option>
                  <option value="UTC">UTC – Soesterberg Airbase (Closed)</option>
                  <option value="WOE">WOE – Woensdrecht Airbase</option>
                </select>
              </div>

              <!-- Step 2: Email -->
              <div style="margin-bottom:16px;padding:10px 12px;background:var(--surface-0);border:1px solid var(--border);border-radius:6px">
                <strong style="font-size:12px">✉️ Step 2 — Owner email address (optional)</strong>
                <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:8px">
                  Used for ownership verification or observer notifications.
                </div>
                <input id="obsEmailInput" type="email" placeholder="name@example.nl" style="width:100%;padding:5px 8px;border:1px solid var(--border);border-radius:6px;background:var(--input-bg);color:var(--text);font-size:12px;box-sizing:border-box" />
              </div>

              <!-- Step 3: Commands -->
              <div style="margin-bottom:16px">
                <strong style="font-size:12px">⌨️ Step 3 — Paste into your device console</strong>
                <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:8px">
                  Requires the <a href="https://dutch-meshcore.github.io/Dutch-Meshcore-Toolbox/#/firmware" target="_blank" rel="noopener" style="color:var(--accent)">DutchMeshcore.nl MQTT Bridge firmware</a> (Heltec, LilyGO, T-Beam, RAK and more).
                </div>
                <div style="background:var(--surface-1,#1a1a2e);border:1px solid var(--border);border-radius:6px;overflow:hidden">
                  <div style="display:flex;justify-content:space-between;align-items:center;padding:6px 10px;border-bottom:1px solid var(--border);background:var(--surface-0)">
                    <span style="font-size:11px;color:var(--text-muted)">MeshCore console</span>
                    <button data-action="copy-commands" style="font-size:11px;padding:2px 8px;border-radius:4px;border:1px solid var(--border);background:var(--surface-2);color:var(--text);cursor:pointer">📋 Copy</button>
                  </div>
                  <pre class="help-code" style="margin:0;padding:10px 12px;border-radius:0"><code id="obsCommandBlock">set mqtt.iata <span class="obs-iata-val">AMS</span>
<span class="obs-email-line" style="display:none">set mqtt.email <span class="obs-email-val"></span>
</span>set mqtt1.preset dutchmeshcore-1
set mqtt2.preset dutchmeshcore-2
reboot</code></pre>
                </div>
                <div style="margin-top:8px;padding:8px 10px;background:color-mix(in srgb,var(--accent) 10%,transparent);border:1px solid color-mix(in srgb,var(--accent) 30%,transparent);border-radius:6px;font-size:12px">
                  <strong>⚡ Both ingestors required for high availability.</strong>
                  <span class="text-muted"> If one goes down your observer stays connected via the other.
                  <code>mqtt1.preset</code> → Ingestor 1 &nbsp;·&nbsp; <code>mqtt2.preset</code> → Ingestor 2.</span>
                </div>
              </div>

              <hr style="margin:16px 0;border:none;border-top:1px solid var(--border)">

              <!-- Manual config reference -->
              <div style="margin-bottom:12px">
                <strong style="font-size:12px">🔧 Manual configuration (reference)</strong>
                <div class="text-muted" style="font-size:12px;margin-top:4px;margin-bottom:10px">
                  For custom firmware builds, generic MQTT clients, or anyone integrating directly without using the preset commands above.
                </div>
                <div style="display:grid;grid-template-columns:1fr 1fr;gap:10px">
                  <div>
                    <strong style="font-size:12px">Ingestor 1</strong>
                    <table class="help-table" style="margin-top:6px">
                      <tr><td>Server:</td><td><code>collector1.dutchmeshcore.nl</code></td></tr>
                      <tr><td>Port:</td><td><code>443</code></td></tr>
                      <tr><td>Transport:</td><td><code>WebSockets (TLS)</code></td></tr>
                    </table>
                  </div>
                  <div>
                    <strong style="font-size:12px">Ingestor 2</strong>
                    <table class="help-table" style="margin-top:6px">
                      <tr><td>Server:</td><td><code>collector2.dutchmeshcore.nl</code></td></tr>
                      <tr><td>Port:</td><td><code>443</code></td></tr>
                      <tr><td>Transport:</td><td><code>WebSockets (TLS)</code></td></tr>
                    </table>
                  </div>
                </div>
              </div>

              <hr style="margin:16px 0;border:none;border-top:1px solid var(--border)">

              <div style="font-size:12px;color:var(--text-muted)">
                Missing the info for your specific device? Take a look at the DutchMeshcore.nl <a href="https://dutch-meshcore.github.io/Dutch-Meshcore-Toolbox/" target="_blank" rel="noopener" style="color:var(--accent)">toolbox</a>.
              </div>
            </div>
          </div>
        </div>
		<hr class="section-divider">
        <div id="obsRegionFilter" class="region-filter-container"></div>
        <div id="obsContent">${PageState.loading('Loading observers…')}</div>
      </div>`;
    RegionFilter.init(document.getElementById('obsRegionFilter'));
    regionChangeHandler = RegionFilter.onChange(function () { render(); });
    // Restore stats panel open/close state
    try {
      if (localStorage.getItem(STATS_OPEN_KEY) === '1') {
        var body = document.getElementById('obsStatsBody');
        var tog = app.querySelector('.obs-stats-toggle');
        if (body) body.style.maxHeight = '2000px';
        if (tog) tog.textContent = '▼';
      }
    } catch (e) {}
    currentApp = app;
    loadObservers();
    // Event delegation for data-action buttons
    clickHandler = function (e) {
      var th = e.target.closest('th[data-sort-col]');
      if (th) {
        var col = th.dataset.sortCol;
        sortState.dir = sortState.col === col && sortState.dir === 'asc' ? 'desc' : 'asc';
        sortState.col = col;
        saveSortState();
        render();
        return;
      }
      var btn = e.target.closest('[data-action]');
      if (btn && btn.dataset.action === 'obs-refresh') loadObservers();
      if (btn && btn.dataset.action === 'toggle-stats') {
        var statsBody = document.getElementById('obsStatsBody');
        var statsTog = btn.querySelector('.obs-stats-toggle');
        var isOpen = statsBody.style.maxHeight !== '0px';
        statsBody.style.maxHeight = isOpen ? '0px' : '2000px';
        statsTog.textContent = isOpen ? '▶' : '▼';
        try { localStorage.setItem(STATS_OPEN_KEY, isOpen ? '0' : '1'); } catch (e) {}
      }
      if (btn && btn.dataset.action === 'toggle-hide-stale') {
        hideStale = !hideStale;
        try { localStorage.setItem('meshcore-obs-hide-stale', hideStale ? '1' : '0'); } catch (e) {}
        render();
        return;
      }
      if (btn && btn.dataset.action === 'toggle-hide-offline') {
        hideOffline = !hideOffline;
        try { localStorage.setItem('meshcore-obs-hide-offline', hideOffline ? '1' : '0'); } catch (e) {}
        render();
        return;
      }
      if (btn && btn.dataset.action === 'toggle-help') {
        var content = btn.closest('.help-box').querySelector('.help-content');
        var toggle = btn.querySelector('.help-toggle');
        var isCollapsed = toggle.textContent === '▶';
        content.style.maxHeight = isCollapsed ? '4000px' : '0px';
        toggle.textContent = isCollapsed ? '▼' : '▶';
      }
      if (btn && btn.dataset.action === 'copy-commands') {
        var block = document.getElementById('obsCommandBlock');
        if (block) {
          var text = block.innerText || block.textContent;
          navigator.clipboard.writeText(text.trim()).then(function () {
            btn.textContent = '✅ Copied';
            setTimeout(function () { btn.textContent = '📋 Copy'; }, 2000);
          }).catch(function () {
            btn.textContent = '❌ Failed';
            setTimeout(function () { btn.textContent = '📋 Copy'; }, 2000);
          });
        }
        return;
      }
      var row = e.target.closest('tr[data-action="navigate"]');
      if (row) {
        // #1056 AC#4: at narrow widths, open detail in slide-over instead of
        // navigating to a separate page.
        if (window.SlideOver && window.SlideOver.shouldUse()) {
          e.preventDefault();
          openObserverSlideOver(row.dataset.value);
          return;
        }
        location.hash = row.dataset.value;
      }
    };
    app.addEventListener('click', clickHandler);
    // IATA picker — update code block when a region is selected
    changeHandler = function (e) {
      if (e.target.id === 'obsIataSelect') {
        var code = e.target.value;
        app.querySelectorAll('.obs-iata-val').forEach(function (span) {
          span.textContent = code;
        });
      }
    };
    app.addEventListener('change', changeHandler);
    // Email input — show/hide and update the mqtt.email line live
    inputHandler = function (e) {
      if (e.target.id === 'obsEmailInput') {
        var val = e.target.value.trim();
        app.querySelectorAll('.obs-email-val').forEach(function (span) {
          span.textContent = val;
        });
        app.querySelectorAll('.obs-email-line').forEach(function (span) {
          span.style.display = val ? 'inline' : 'none';
        });
      }
    };
    app.addEventListener('input', inputHandler);
    // #209 — Keyboard accessibility for observer rows
    keydownHandler = function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      if (window.SlideOver && window.SlideOver.shouldUse()) {
        openObserverSlideOver(row.dataset.value);
        return;
      }
      location.hash = row.dataset.value;
    };
    app.addEventListener('keydown', keydownHandler);
    // Auto-refresh every 30s
    refreshTimer = setInterval(function() { loadObservers(true); }, 30000);
    wsHandler = debouncedOnWS(function (msgs) {
      if (msgs.some(function (m) { return m.type === 'packet'; })) loadObservers(true);
    });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (refreshTimer) clearInterval(refreshTimer);
    refreshTimer = null;
    if (regionChangeHandler) RegionFilter.offChange(regionChangeHandler);
    regionChangeHandler = null;
    if (currentApp) {
      if (clickHandler)   currentApp.removeEventListener('click',   clickHandler);
      if (changeHandler)  currentApp.removeEventListener('change',  changeHandler);
      if (inputHandler)   currentApp.removeEventListener('input',   inputHandler);
      if (keydownHandler) currentApp.removeEventListener('keydown', keydownHandler);
    }
    clickHandler = changeHandler = inputHandler = keydownHandler = currentApp = null;
    observers = [];
    obsSkewMap = {};
    obsStatsData = {};
  }

  function invalidateObserversCache() {
    invalidateApiCache('/observers');
  }

  async function loadObservers(force) {
    if (force) invalidateObserversCache();
    try {
      const [data, skewData] = await Promise.all([
        api('/observers', { ttl: CLIENT_TTL.observers }),
        api('/observers/clock-skew', { ttl: 30000 }).catch(function() { return []; })
      ]);
      observers = data.observers || [];
      obsSkewMap = {};
      (Array.isArray(skewData) ? skewData : []).forEach(function(s) {
        if (s && s.observerID) obsSkewMap[s.observerID] = s;
      });
      render();
    } catch (e) {
      PageState.error(document.getElementById('obsContent'), e, loadObservers);
    }
  }

  async function loadObserverStats(force) {
    if (force) invalidateApiCache('/observers/stats');
    try {
      const data = await api('/observers/stats', { ttl: 300000 });
      obsStatsData = {};
      (data.observers || []).forEach(function(s) {
        if (s && s.id) obsStatsData[s.id] = s;
      });
      render();
    } catch (e) {}
  }

  function isStatsPanelOpen() {
    var body = document.getElementById('obsStatsBody');
    return body ? body.style.maxHeight !== '0px' : false;
  }

  // NOTE: Comparing server timestamps to Date.now() can skew if client/server
  // clocks differ. We add ±30s tolerance to thresholds to reduce false positives.
  //
  // We use last_seen (status message recency) as the health signal — it is stamped
  // whenever the device sends a status update (which carries uptime_secs, battery,
  // etc.), so observers that are alive but forwarding no mesh packets still
  // show Online.  last_packet_at reflects data forwarding activity, not liveness.
  function healthStatus(o) {
    const ts = (o && typeof o === 'object') ? o.last_seen : o;
    if (!ts) return { cls: 'health-red', label: 'Unknown' };
    const ago = Date.now() - new Date(ts).getTime();
    const tolerance = 30000; // 30s tolerance for clock skew
    if (ago < 600000 + tolerance) return { cls: 'health-green', label: 'Online' };    // < 10 min + tolerance
    if (ago < 3600000 + tolerance) return { cls: 'health-yellow', label: 'Stale' };   // < 1 hour + tolerance
    return { cls: 'health-red', label: 'Offline' };
  }

  function packetBadge(o) {
    if (!o.last_packet_at) return '<span title="No packets ever observed">📡⚠ never</span>';
    const pktAgo = Date.now() - new Date(o.last_packet_at).getTime();
    const statusAgo = o.last_seen ? Date.now() - new Date(o.last_seen).getTime() : Infinity;
    const gap = pktAgo - statusAgo;
    if (gap > 600000) {
      return `<span title="Last packet ${timeAgo(o.last_packet_at)} — status is newer by ${Math.round(gap/60000)}min. Observer may be alive but not forwarding packets.">📡⚠ ${timeAgo(o.last_packet_at)}</span>`;
    }
    return timeAgo(o.last_packet_at);
  }

  function uptimeStr(o) {
    // Prefer device-reported uptime_secs: reflects actual node uptime, independent
    // of when our ingestor first saw the observer or how long the MQTT bridge has run.
    if (o && o.uptime_secs != null) {
      const s = o.uptime_secs;
      const d = Math.floor(s / 86400);
      const h = Math.floor((s % 86400) / 3600);
      if (d > 0) return `${d}d ${h}h`;
      const m = Math.floor((s % 3600) / 60);
      return h > 0 ? `${h}h ${m}m` : `${m}m`;
    }
    // Fall back to time-since-first-seen when device hasn't reported hardware stats
    const firstSeen = o && typeof o === 'object' ? o.first_seen : o;
    if (!firstSeen) return '—';
    const ms = Date.now() - new Date(firstSeen).getTime();
    const d = Math.floor(ms / 86400000);
    const h = Math.floor((ms % 86400000) / 3600000);
    if (d > 0) return `${d}d ${h}h`;
    const m = Math.floor((ms % 3600000) / 60000);
    return h > 0 ? `${h}h ${m}m` : `${m}m`;
  }

  function sparkBar(count, max) {
    if (max === 0) return `<span class="text-muted">0/hr</span>`;
    const pct = Math.min(100, Math.round((count / max) * 100));
    return `<span style="display:inline-flex;align-items:center;gap:6px;white-space:nowrap"><span style="display:inline-block;width:60px;height:12px;background:var(--border);border-radius:3px;overflow:hidden;vertical-align:middle"><span style="display:block;height:100%;width:${pct}%;background:linear-gradient(90deg,#3b82f6,#60a5fa);border-radius:3px"></span></span><span style="font-size:11px">${count}/hr</span></span>`;
  }

  function renderStatsGrid(data) {
    var grid = document.getElementById('obsStatsGrid');
    if (!grid) return;

    function statBlock(title, items) {
      var rows = items.length
        ? items.map(function (item, i) {
            return `<li><span class="obs-stat-rank">${i + 1}</span><span class="obs-stat-name" title="${item.title || item.name}">${item.name}</span><span class="obs-stat-val">${item.val}</span></li>`;
          }).join('')
        : '<li><span class="text-muted" style="font-size:11px">No data</span></li>';
      return `<div class="obs-stat-block"><div class="obs-stat-block-title">${title}</div><ol class="obs-stat-list">${rows}</ol></div>`;
    }

    var byPackets = data.slice().sort(function (a, b) { return (b.packet_count || 0) - (a.packet_count || 0); }).slice(0, 5)
      .map(function (o) { return { name: o.name || o.id, val: (o.packet_count || 0).toLocaleString() }; });

    var byPktsHr = data.slice().sort(function (a, b) { return (b.packetsLastHour || 0) - (a.packetsLastHour || 0); }).slice(0, 5)
      .map(function (o) { return { name: o.name || o.id, val: (o.packetsLastHour || 0) + '/hr' }; });

    var byUptime = data.slice().sort(function (a, b) {
      var ua = a.first_seen ? Date.now() - new Date(a.first_seen).getTime() : 0;
      var ub = b.first_seen ? Date.now() - new Date(b.first_seen).getTime() : 0;
      return ub - ua;
    }).slice(0, 5).map(function (o) { return { name: o.name || o.id, val: uptimeStr(o.first_seen) }; });

    var regionMap = {};
    data.forEach(function (o) { if (o.iata) regionMap[o.iata] = (regionMap[o.iata] || 0) + 1; });
    var byRegion = Object.entries(regionMap).sort(function (a, b) { return b[1] - a[1]; }).slice(0, 5)
      .map(function (entry) { return { name: `<span class="badge-region">${entry[0]}</span>`, title: entry[0], val: entry[1] + (entry[1] === 1 ? ' observer' : ' observers') }; });

    grid.innerHTML =
      statBlock('Top 5 · Total Packets', byPackets) +
      statBlock('Top 5 · Packets / Hour', byPktsHr) +
      statBlock('Top 5 · Uptime', byUptime) +
      statBlock('Top Regions', byRegion);
  }

  function render() {
    const el = document.getElementById('obsContent');
    if (!el) return;

    // Apply region filter
    const selectedRegions = RegionFilter.getSelected();
    const filtered = selectedRegions
      ? observers.filter(o => o.iata && selectedRegions.includes(o.iata))
      : observers;

    renderStatsGrid(filtered);

    if (filtered.length === 0) {
      el.innerHTML = '<div class="text-center text-muted" style="padding:40px">No observers found.</div>';
      return;
    }

    // Apply status visibility toggles (stats panel always uses full filtered set)
    const visible = filtered.filter(function(o) {
      if (hideStale && healthStatus(o).cls === 'health-yellow') return false;
      if (hideOffline && healthStatus(o).cls === 'health-red') return false;
      return true;
    });

    const sorted = applySortState(visible);
    const maxPktsHr = Math.max(1, ...filtered.map(o => o.packetsLastHour || 0));

    // Summary counts (always from full region-filtered set, regardless of visibility toggles)
    const online = filtered.filter(o => healthStatus(o).cls === 'health-green').length;
    const stale = filtered.filter(o => healthStatus(o).cls === 'health-yellow').length;
    const offline = filtered.filter(o => healthStatus(o).cls === 'health-red').length;

    function sortTh(label, col, priority) {
      var active = sortState.col === col;
      var arrow = active ? (sortState.dir === 'asc' ? '▲' : '▼') : '⇅';
      var pAttr = priority > 1 ? ` data-priority="${priority}"` : '';
      return `<th scope="col" class="sortable-col${active ? ' sort-active' : ''}" data-sort-col="${col}"${pAttr}>${label}<span class="sort-arrow">${arrow}</span></th>`;
    }

    const totalLabel = visible.length < filtered.length
      ? `${visible.length}/${filtered.length}`
      : `${filtered.length}`;

    const summaryHtml = `
        <span class="obs-stat"><span class="health-dot health-green">●</span> ${online} Online</span>
        <span class="obs-stat"><span class="health-dot health-yellow">▲</span> ${stale} Stale <button class="obs-filter-btn${hideStale ? ' active' : ''}" data-action="toggle-hide-stale" title="${hideStale ? 'Show stale observers' : 'Hide stale observers'}">${hideStale ? 'show' : 'hide'}</button></span>
        <span class="obs-stat"><span class="health-dot health-red">✕</span> ${offline} Offline <button class="obs-filter-btn${hideOffline ? ' active' : ''}" data-action="toggle-hide-offline" title="${hideOffline ? 'Show offline observers' : 'Hide offline observers'}">${hideOffline ? 'show' : 'hide'}</button></span>
        <span class="obs-stat">📡 ${totalLabel} Total</span>`;

    const tbodyHtml = sorted.map(o => {
      const h = healthStatus(o);
      const shape = h.cls === 'health-green' ? '●' : h.cls === 'health-yellow' ? '▲' : '✕';
      return `<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/observers/${encodeURIComponent(o.id)}" onclick="location.hash='#/observers/${encodeURIComponent(o.id)}'">
            <td><span class="health-dot ${h.cls}" title="${h.label}">${shape}</span> ${h.label}</td>
            <td class="mono">${o.name || o.id}</td>
            <td>${o.radio ? 'SF' + (o.radio.split(',')[2] || '?') : '<span class="text-muted">—</span>'}</td>
            <td>${packetBadge(o)}</td>
            <td>${o.iata ? `<span class="badge-region">${o.iata}</span>` : '—'}</td>
            <td>${timeAgo(o.last_seen)}</td>
            <td>${(function() {
              var sk = obsSkewMap[o.id];
              if (!sk || sk.samples == null || sk.samples === 0) return '<span class="text-muted">—</span>';
              var sev = observerSkewSeverity(sk.offsetSec);
              return renderSkewBadge(sev, sk.offsetSec) + ' <span class="text-muted" title="Computed from ' + sk.samples + ' multi-observer packets. Positive = observer ahead of consensus.">(' + sk.samples + ')</span>';
            })()}</td>
            <td>${uptimeStr(o)}</td>
            <td>${(o.packet_count || 0).toLocaleString()}</td>
            <td>${sparkBar(o.packetsLastHour || 0, maxPktsHr)}</td>
            <td>${o.last_packet_at ? timeAgo(o.last_packet_at) : '<span class="text-muted">—</span>'}</td>
          </tr>`;
    }).join('');

    const existingTable = el.querySelector('#obsTable');
    if (existingTable) {
      // Partial update — keep .obs-table-scroll intact so scroll position
      // (and any active touch-scroll) is never interrupted by a re-render.
      var summaryEl = el.querySelector('.obs-summary');
      if (summaryEl) summaryEl.innerHTML = summaryHtml;
      // Update sort indicators in-place without replacing <th> elements
      // (replacing ths would destroy the column-resize handles).
      el.querySelectorAll('#obsTable thead th[data-sort-col]').forEach(function(th) {
        var col = th.dataset.sortCol;
        var active = sortState.col === col;
        th.classList.toggle('sort-active', active);
        var arrowSpan = th.querySelector('.sort-arrow');
        if (arrowSpan) arrowSpan.textContent = active ? (sortState.dir === 'asc' ? '▲' : '▼') : '⇅';
      });
      existingTable.querySelector('tbody').innerHTML = tbodyHtml;
    } else {
      // Full render — first load or recovering from the "no observers" state.
      el.innerHTML = `
      <div class="obs-summary">${summaryHtml}
      </div>
        <div class="obs-table-scroll table-fluid-wrap"><table class="data-table obs-table" id="obsTable">
          <caption class="sr-only">Observer status and statistics</caption>
        <thead><tr>
          ${sortTh('Status','status')}${sortTh('Name','name')}${sortTh('SF','sf',2)}${sortTh('Packet Health','forwarding',2)}${sortTh('Region','region',3)}${sortTh('Last Status','last_seen',3)}
          ${sortTh('Clock Offset','clock_offset',4)}${sortTh('Uptime','uptime',4)}${sortTh('Total Packets','packets',5)}${sortTh('Packets/Hour','packets_hr',5)}${sortTh('Last Packet','last_packet',5)}
        </tr></thead>
        <tbody>${tbodyHtml}</tbody>
      </table></div>`;
      makeColumnsResizable('#obsTable', 'meshcore-obs-col-widths');
    }
    // #1056: fluid columns + +N hidden pill
    if (window.TableResponsive) {
      var _obsTbl = document.getElementById('obsTable');
      if (_obsTbl) window.TableResponsive.register(_obsTbl);
    }
  }


  registerPage('observers', { init, destroy });

  // #1056 AC#4: row-detail slide-over (narrow viewports). Renders a compact
  // summary from the in-memory observer + a link to the full page.
  function openObserverSlideOver(hashHref) {
    if (!window.SlideOver) return;
    var m = String(hashHref || '').match(/#\/observers\/(.+)$/);
    if (!m) return;
    var id = decodeURIComponent(m[1]);
    var o = (observers || []).find(function (x) { return String(x.id) === id; });
    if (!o) return;
    var h = healthStatus(o);
    var sk = obsSkewMap[o.id];
    var skewLine = (sk && sk.samples) ? renderSkewBadge(observerSkewSeverity(sk.offsetSec), sk.offsetSec) + ' (' + sk.samples + ' samples)' : '—';
    var pkts = sparkBar(o.packetsLastHour || 0, Math.max(1, o.packetsLastHour || 1));
    var content = window.SlideOver.open({ title: o.name || o.id });
    content.innerHTML =
      '<dl class="slide-over-dl" style="margin:0;display:grid;grid-template-columns:auto 1fr;gap:6px 12px;font-size:13px">' +
        '<dt>Status</dt><dd><span class="health-dot ' + h.cls + '">●</span> ' + h.label + '</dd>' +
        '<dt>Region</dt><dd>' + (o.iata ? '<span class="badge-region">' + o.iata + '</span>' : '—') + '</dd>' +
        '<dt>Last status</dt><dd>' + timeAgo(o.last_seen) + '</dd>' +
        '<dt>Last packet</dt><dd>' + (o.last_packet_at ? timeAgo(o.last_packet_at) : '—') + '</dd>' +
        '<dt>Total packets</dt><dd>' + (o.packet_count || 0).toLocaleString() + '</dd>' +
        '<dt>Packets/hr</dt><dd>' + pkts + '</dd>' +
        '<dt>Clock offset</dt><dd>' + skewLine + '</dd>' +
        '<dt>Uptime</dt><dd>' + uptimeStr(o) + '</dd>' +
      '</dl>' +
      '<p style="margin-top:14px"><a class="btn-primary" href="' + hashHref + '">Open full detail →</a></p>';
  }
})();
