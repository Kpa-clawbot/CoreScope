/* === CoreScope — observers.js === */
'use strict';

(function () {
  let observers = [];
  let wsHandler = null;
  let refreshTimer = null;
  let regionChangeHandler = null;
  let _obsSortCtrl = null;

  function init(app) {
    const isMobile = Layout.isTabletOrBelow();
    app.innerHTML = `
      <div class="observers-page">
        <div class="page-header">
          <h2>Observer Status</h2>
        </div>
        <div class="obs-toolbar">
          <div id="obsRegionFilter" class="region-filter-container"></div>
          ${isMobile ? `<button class="obs-refresh-btn" data-action="obs-refresh" data-tooltip="Refresh observer data" aria-label="Refresh">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
            Refresh
          </button>` : ''}
        </div>
        <div class="obs-top-fw-row">
          <div id="obsTopMonthly"></div>
          <div id="obsFirmware" style="display:none"></div>
        </div>
        <div id="obsContent"><div class="text-center text-muted" style="padding:40px">Loading…</div></div>
      </div>`;
    RegionFilter.init(document.getElementById('obsRegionFilter'));
    regionChangeHandler = RegionFilter.onChange(function () { render(); });
    loadObservers();
    loadTopMonthly();
    loadFirmware();
    // Event delegation for data-action buttons and row navigation
    app.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-action]');
      if (btn && btn.dataset.action === 'obs-refresh') loadObservers();
      var row = e.target.closest('tr[data-action="navigate"]');
      if (row) location.hash = row.dataset.value;
    });
    // #209 — Keyboard accessibility for observer rows
    app.addEventListener('keydown', function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      location.hash = row.dataset.value;
    });
    // On mobile/tablet: snapshot only — no auto-refresh or WS-driven reloads
    if (!isMobile) {
      refreshTimer = setInterval(loadObservers, 30000);
      wsHandler = debouncedOnWS(function (msgs) {
        if (msgs.some(function (m) { return m.type === 'packet'; })) loadObservers();
      });
    }
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (refreshTimer) clearInterval(refreshTimer);
    refreshTimer = null;
    if (regionChangeHandler) RegionFilter.offChange(regionChangeHandler);
    regionChangeHandler = null;
    if (_obsSortCtrl) { _obsSortCtrl.destroy(); _obsSortCtrl = null; }
    observers = [];
  }

  async function loadTopMonthly() {
    const el = document.getElementById('obsTopMonthly');
    if (!el) return;
    try {
      // No client-side cache — avoid caching empty results on first load
      const data = await api('/observers/top-monthly?n=4', { bust: true });
      const entries = data.topMonthly || [];
      if (!entries.length) { el.innerHTML = ''; return; }

      // All entries are for the same (last completed) month
      const month = entries[0].month;
      const [y, mo] = month.split('-');
      const monthLabel = new Date(+y, +mo - 1, 1).toLocaleString('default', { month: 'long', year: 'numeric' });

      const medals = ['🥇', '🥈', '🥉'];
      const maxPkts = entries[0].packet_count || 1;

      const rowsHtml = entries.map((e, i) => {
        const pct = Math.round((e.packet_count / maxPkts) * 100);
        const medal = medals[i] || `<span style="display:inline-block;width:18px;text-align:center;font-size:11px;color:var(--text-muted)">${e.rank}</span>`;
        return `<div class="obs-top-row">
          <span class="obs-top-rank">${medal}</span>
          <span class="obs-top-name">${escapeHtml(e.name || e.id)}</span>
          <div class="obs-top-bar-wrap">
            <div class="obs-top-bar" style="width:${pct}%"></div>
          </div>
          <span class="obs-top-count" data-tooltip="Packets stored for ${monthLabel} after duplicates removed. The table's Total Raw Pkts column counts everything received, including duplicates." data-tooltip-pos="left">${e.packet_count.toLocaleString()}</span>
        </div>`;
      }).join('');

      el.innerHTML = `<div class="obs-top-section">
        <div class="obs-top-title">Top Observers — <span style="color:var(--accent)">${monthLabel}</span></div>
        ${rowsHtml}
      </div>`;
    } catch (e) {
      el.innerHTML = '';
    }
  }

  async function loadObservers() {
    try {
      const data = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = data.observers || [];
      render();
    } catch (e) {
      document.getElementById('obsContent').innerHTML =
        `<div class="text-muted" role="alert" aria-live="polite" style="padding:40px">Error loading observers: ${e.message}</div>`;
    }
  }

  function _fwDisplayName(name, maxLen = 48) {
    if (name.length <= maxLen) return name;
    const dot = name.lastIndexOf('.');
    const suffix = dot >= 0 ? name.slice(dot) : '';
    return name.slice(0, maxLen - suffix.length - 1) + '…' + suffix;
  }

  function _fwHumanize(raw) {
    return raw.replace(/[_-]/g, ' ').trim().split(/\s+/).map(w => {
      if (/^\d+dbm$/i.test(w)) return w.replace(/dbm$/i, 'dBm');
      if (w.toLowerCase() === 'nrf') return 'nRF';
      return w.charAt(0).toUpperCase() + w.slice(1).toLowerCase();
    }).join(' ');
  }

  function _fwParse(files) {
    return files.map(f => {
      const m = f.name.match(/_v(\d+\.\d+(?:\.\d+)?)/);
      const version = m ? m[1] : 'unknown';
      const variantRaw = m ? f.name.slice(0, m.index) : f.name.replace(/\.[^.]+$/, '');
      return { ...f, version, variant: _fwHumanize(variantRaw) };
    });
  }

  function _fwSortVersions(versions) {
    return versions.slice().sort((a, b) => {
      const ap = a.split('.').map(Number), bp = b.split('.').map(Number);
      for (let i = 0; i < 3; i++) { const d = (bp[i]||0) - (ap[i]||0); if (d) return d; }
      return 0;
    });
  }

  function _fwRender(el, parsed) {
    const versionSel = el.querySelector('#fwVersionSel');
    const variantSel = el.querySelector('#fwVariantSel');
    const listEl     = el.querySelector('#fwFileList');
    const fmtSize = b => b >= 1048576 ? (b/1048576).toFixed(1)+' MB' : b >= 1024 ? (b/1024).toFixed(1)+' KB' : b+' B';

    function repopulateVariants(selVersion) {
      const pool = parsed.filter(f => f.version === selVersion);
      const variants = [...new Set(pool.map(f => f.variant))].sort();
      const cur = variantSel.value;
      variantSel.innerHTML = `<option value="">Choose variant</option>` +
        variants.map(v => `<option value="${escapeHtml(v)}"${v === cur ? ' selected' : ''}>${escapeHtml(v)}</option>`).join('');
      if (cur && !variants.includes(cur)) variantSel.value = '';
    }

    function renderList() {
      const sv = versionSel.value, sd = variantSel.value;
      if (!sd) { listEl.innerHTML = ''; return; }
      const hits = parsed.filter(f => f.version === sv && f.variant === sd);
      if (!hits.length) {
        listEl.innerHTML = '<div class="fw-empty">No files match the selected filters.</div>';
        return;
      }
      listEl.innerHTML = hits.map(f => `
        <div class="fw-row">
          <span class="fw-name" title="${escapeHtml(f.name)}">${escapeHtml(_fwDisplayName(f.name))}</span>
          <span class="fw-size">${fmtSize(f.size)}</span>
          <a class="fw-dl-btn" href="${escapeHtml(f.url)}" download="${escapeHtml(f.name)}">Download</a>
        </div>`).join('');
    }

    versionSel.addEventListener('change', function() { repopulateVariants(this.value); renderList(); });
    variantSel.addEventListener('change', renderList);
    repopulateVariants(versionSel.value);
    renderList();
  }

  async function loadFirmware() {
    const el = document.getElementById('obsFirmware');
    if (!el) return;
    try {
      const data = await api('/firmware', { bust: true });
      const files = data.files || [];
      if (!files.length) { el.style.display = 'none'; el.innerHTML = ''; return; }

      const parsed   = _fwParse(files);
      const versions = _fwSortVersions([...new Set(parsed.map(f => f.version))]);
      const variants = [...new Set(parsed.map(f => f.variant))].sort();

      el.innerHTML = `
        <div class="data-card fw-card">
          <div class="fw-header">Observer Firmware Downloads</div>
          <div class="fw-filters">
            <label class="fw-filter-group">
              <span class="fw-filter-label">Version</span>
              <select id="fwVersionSel" class="fw-select">
                ${versions.map((v, i) => `<option value="${escapeHtml(v)}"${i === 0 ? ' selected' : ''}>${escapeHtml(v)}</option>`).join('')}
              </select>
            </label>
            <label class="fw-filter-group">
              <span class="fw-filter-label">Device</span>
              <select id="fwVariantSel" class="fw-select">
                <option value="">Choose variant</option>
                ${variants.map(v => `<option value="${escapeHtml(v)}">${escapeHtml(v)}</option>`).join('')}
              </select>
            </label>
          </div>
          <div id="fwFileList"></div>
          <div class="fw-become-observer">
            <button class="fw-become-btn" id="fwBecomeBtn">Become an Observer</button>
          </div>
        </div>

        <div class="modal-overlay" id="fwObserverModal" style="display:none">
          <div class="modal fw-observer-modal">
            <h3>Become an Observer</h3>
            <p class="fw-modal-intro">Observers are always-on nodes that receive and forward packets to this network. Here's how to get set up:</p>
            <div class="fw-modal-step">
              <div class="fw-modal-step-num">1</div>
              <div>
                <div class="fw-modal-step-title">Flash Observer Firmware</div>
                <div class="fw-modal-step-body">Download the firmware for your device above. The easiest way to flash is the <a href="https://meshcore.io/flasher" target="_blank" rel="noopener" class="fw-modal-link">MeshCore Web Flasher</a> — select <strong>Custom</strong> and upload the downloaded file. For nRF52 devices you can also drag-and-drop the .uf2 file onto the device's USB drive.</div>
              </div>
            </div>
            <div class="fw-modal-step">
              <div class="fw-modal-step-num">2</div>
              <div>
                <div class="fw-modal-step-title">Install MeshCore-to-MQTT</div>
                <div class="fw-modal-step-body">Run the <strong>meshcore-to-mqtt</strong> bridge on a Raspberry Pi or always-on computer. It connects your observer node via USB serial and publishes packets to the network MQTT broker.<br><br>One-line install:<br><code class="fw-modal-code">curl -fsSL https://raw.githubusercontent.com/Cisien/meshcoretomqtt/main/install.sh | sudo bash</code></div>
              </div>
            </div>
            <div class="fw-modal-step">
              <div class="fw-modal-step-num">3</div>
              <div>
                <div class="fw-modal-step-title">Get in Touch</div>
                <div class="fw-modal-step-body">Reach out on Discord for MQTT credentials and help getting connected: <a href="https://discord.com/users/1184030791685115935" target="_blank" rel="noopener" class="fw-discord fw-modal-link">jesters_deead</a></div>
              </div>
            </div>
            <div class="fw-modal-actions">
              <button class="btn-close" id="fwObserverModalClose">Close</button>
            </div>
          </div>
        </div>`;

      el.style.display = '';
      _fwRender(el, parsed);

      const modal = document.getElementById('fwObserverModal');
      document.getElementById('fwBecomeBtn').addEventListener('click', () => { modal.style.display = 'flex'; });
      document.getElementById('fwObserverModalClose').addEventListener('click', () => { modal.style.display = 'none'; });
      modal.addEventListener('click', e => { if (e.target === modal) modal.style.display = 'none'; });
    } catch (e) {
      el.style.display = 'none';
      el.innerHTML = '';
    }
  }

  // NOTE: Comparing server timestamps to Date.now() can skew if client/server
  // clocks differ. We add ±30s tolerance to thresholds to reduce false positives.
  function healthStatus(lastSeen) {
    if (!lastSeen) return { cls: 'health-red', label: 'Unknown', sortVal: 2 };
    const ago = Date.now() - new Date(lastSeen).getTime();
    const tolerance = 30000; // 30s tolerance for clock skew
    if (ago < 600000 + tolerance) return { cls: 'health-green', label: 'Online',   sortVal: 0 };
    if (ago < 3600000 + tolerance) return { cls: 'health-yellow', label: 'Stale',  sortVal: 1 };
    return { cls: 'health-red', label: 'Offline', sortVal: 2 };
  }

  function uptimeStr(firstSeen) {
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

  // Build a row HTML string with data-value attributes for TableSort
  function buildRow(o, maxPkts) {
    const h = healthStatus(o.last_seen);
    const rate = o.packetsLastHour || 0;
    const uptimeMs = o.first_seen ? new Date(o.first_seen).getTime() : 0;
    return `<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/observers/${encodeURIComponent(o.id)}" data-obs-id="${encodeURIComponent(o.id)}">
      <td data-value="${h.sortVal}"><span class="health-dot ${h.cls}" data-tooltip="${h.label}"></span> ${h.label}</td>
      <td class="mono">${o.name || o.id}</td>
      <td>${o.iata || '—'}</td>
      <td>${timeAgo(o.last_seen)}</td>
      <td>${(o.packet_count || 0).toLocaleString()}</td>
      <td data-value="${rate}">${sparkBar(rate, maxPkts)}</td>
      <td data-value="${uptimeMs}">${uptimeStr(o.first_seen)}</td>
    </tr>`;
  }

  function render() {
    const el = document.getElementById('obsContent');
    if (!el) return;

    // Apply region filter
    const selectedRegions = RegionFilter.getSelected();
    const filtered = selectedRegions
      ? observers.filter(o => o.iata && selectedRegions.includes(o.iata))
      : observers;

    if (filtered.length === 0) {
      el.innerHTML = '<div class="text-center text-muted" style="padding:40px">No observers found.</div>';
      return;
    }

    const maxPktsHr = Math.max(1, ...filtered.map(o => o.packetsLastHour || 0));

    // Summary counts
    const online  = filtered.filter(o => healthStatus(o.last_seen).cls === 'health-green').length;
    const stale   = filtered.filter(o => healthStatus(o.last_seen).cls === 'health-yellow').length;
    const offline = filtered.filter(o => healthStatus(o.last_seen).cls === 'health-red').length;

    // If the table already exists, patch cells in-place to preserve hover state
    const existingTable = el.querySelector('#obsTable');
    if (existingTable) {
      const summaryEl = el.querySelector('.obs-summary');
      if (summaryEl) {
        summaryEl.innerHTML = `
          <span class="obs-stat"><span class="health-dot health-green"></span> ${online} Online</span>
          <span class="obs-stat"><span class="health-dot health-yellow"></span> ${stale} Stale</span>
          <span class="obs-stat"><span class="health-dot health-red"></span> ${offline} Offline</span>
          <span class="obs-stat">📡 ${filtered.length} Total</span>`;
      }
      const tbody = existingTable.querySelector('tbody');
      const existingIds = new Set(Array.from(tbody.querySelectorAll('tr[data-obs-id]')).map(r => r.dataset.obsId));
      const newIds = new Set(filtered.map(o => encodeURIComponent(o.id)));
      // If observer list changed (added/removed), do a full tbody replace then re-sort
      const setsMatch = existingIds.size === newIds.size && [...newIds].every(id => existingIds.has(id));
      if (!setsMatch) {
        tbody.innerHTML = filtered.map(o => buildRow(o, maxPktsHr)).join('');
        if (_obsSortCtrl) _obsSortCtrl.sort();
        return;
      }
      // Patch only the changing cells (status, last seen, packets, rate) — leave row nodes intact
      for (const o of filtered) {
        const row = tbody.querySelector(`tr[data-obs-id="${encodeURIComponent(o.id)}"]`);
        if (!row) continue;
        const cells = row.cells;
        const h = healthStatus(o.last_seen);
        const rate = o.packetsLastHour || 0;
        cells[0].setAttribute('data-value', h.sortVal);
        cells[0].innerHTML = `<span class="health-dot ${h.cls}" data-tooltip="${h.label}"></span> ${h.label}`;
        cells[3].textContent = timeAgo(o.last_seen);
        cells[4].textContent = (o.packet_count || 0).toLocaleString();
        cells[5].setAttribute('data-value', rate);
        cells[5].innerHTML = sparkBar(rate, maxPktsHr);
      }
      // Re-sort with updated values
      if (_obsSortCtrl) _obsSortCtrl.sort();
      return;
    }

    el.innerHTML = `
      <div class="data-card obs-card">
        <div class="obs-summary">
          <span class="obs-stat"><span class="health-dot health-green"></span> ${online} Online</span>
          <span class="obs-stat"><span class="health-dot health-yellow"></span> ${stale} Stale</span>
          <span class="obs-stat"><span class="health-dot health-red"></span> ${offline} Offline</span>
          <span class="obs-stat">📡 ${filtered.length} Total</span>
        </div>
        <div class="obs-table-scroll"><table class="data-table obs-table" id="obsTable">
        <caption class="sr-only">Observer status and statistics</caption>
        <colgroup>
          <col class="obs-col-status"><col class="obs-col-name"><col class="obs-col-region">
          <col class="obs-col-lastseen"><col class="obs-col-rawpkts"><col class="obs-col-rate"><col class="obs-col-uptime">
        </colgroup>
        <thead><tr>
          <th scope="col" data-sort-key="status" data-type="numeric">Status</th>
          <th scope="col" data-sort-key="name">Name</th>
          <th scope="col" data-sort-key="region">Region</th>
          <th scope="col">Last Seen</th>
          <th scope="col">Total Raw Pkts</th>
          <th scope="col" data-sort-key="rate" data-type="numeric">Pkts/Hour</th>
          <th scope="col" data-sort-key="uptime" data-type="numeric">Uptime</th>
        </tr></thead>
        <tbody>${filtered.map(o => buildRow(o, maxPktsHr)).join('')}</tbody>
      </table></div>
      </div>`;

    const table = el.querySelector('#obsTable');
    if (_obsSortCtrl) { _obsSortCtrl.destroy(); _obsSortCtrl = null; }
    _obsSortCtrl = TableSort.init(table, {
      defaultColumn: 'status',
      defaultDirection: 'asc',
      storageKey: 'meshcore-obs-sort',
    });

    makeColumnsResizable('#obsTable', 'meshcore-obs-col-widths');
  }


  registerPage('observers', { init, destroy });
})();
