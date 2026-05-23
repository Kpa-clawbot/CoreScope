(function () {
  'use strict';

  // ── state ──────────────────────────────────────────────────────────────────
  let bootstrap = null;        // bootstrap response
  let obsById = {};            // key → observer record (from observerDirectory)
  let currentSession = null;
  let map = null;
  let markers = {};
  let mapTileLayer = null;
  let mapThemeObs = null;      // MutationObserver for auto dark/light tile swap
  let mapScope = 'expected';
  let wsHandler = null;
  let currentApp = null;

  function isDarkTheme() {
    const t = document.documentElement.getAttribute('data-theme');
    if (t === 'dark') return true;
    if (t === 'light') return false;
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
  }

  // ── init ───────────────────────────────────────────────────────────────────
  async function init(app) {
    currentApp = app;
    currentSession = null;
    markers = {};
    map = null;
    mapTileLayer = null;
    mapScope = 'expected';
    obsById = {};

    app.innerHTML = '<div class="hc-loading">Loading…</div>';
    try {
      bootstrap = await fetchBootstrap();
    } catch (e) {
      app.innerHTML = '<div class="hc-error">Failed to load health check data.</div>';
      return;
    }

    // Build fast lookup map from directory.
    (bootstrap.observerDirectory || []).forEach(function (o) { obsById[o.key] = o; });

    renderCreateForm(app);
    wsHandler = handleWSMessage;
    onWS(wsHandler);
  }

  function destroy() {
    if (wsHandler) { offWS(wsHandler); wsHandler = null; }
    if (mapThemeObs) { mapThemeObs.disconnect(); mapThemeObs = null; }
    if (map) { map.remove(); map = null; }
    currentApp = null;
    currentSession = null;
    markers = {};
    obsById = {};
  }

  // ── bootstrap ──────────────────────────────────────────────────────────────
  async function fetchBootstrap() {
    const res = await fetch('/api/health/bootstrap');
    if (!res.ok) throw new Error('bootstrap ' + res.status);
    return res.json();
  }

  // ── WebSocket ──────────────────────────────────────────────────────────────
  function handleWSMessage(msg) {
    if (msg.type !== 'health_receipt') return;
    if (!currentSession || msg.data.sessionId !== currentSession.id) return;
    onReceipt(msg.data);
  }

  // ── CREATE STATE ────────────────────────────────────────────────────────────
  function renderCreateForm(app) {
    const directory = bootstrap.observerDirectory || [];

    // Collect unique regions from observer directory.
    const regions = [...new Set(directory.map(o => o.region).filter(Boolean))].sort();

    const turnstile = bootstrap.turnstile || {};
    const testChannel = bootstrap.testChannel || {};
    const channelName = testChannel.name || 'test';
    const mqttOk = bootstrap.mqtt && bootstrap.mqtt.connected;
    const decryptionOk = testChannel.decryptionConfigured !== false; // treat absent as ok (older server)

    // Tear down any previous map before replacing innerHTML.
    if (mapThemeObs) { mapThemeObs.disconnect(); mapThemeObs = null; }
    if (map) { map.remove(); map = null; }
    markers = {};

    app.innerHTML = `
      <div class="hc-wrap">
        <h2>Mesh Health Check</h2>
        <p class="hc-intro">Generate a test code, broadcast it to <strong>#${escHtml(channelName)}</strong>, and see which observers received it.</p>
        ${!mqttOk ? '<div class="hc-warn">⚠ MQTT not connected — packets will not be received until the broker is reachable.</div>' : ''}
        ${!decryptionOk ? '<div class="hc-warn">⚠ Channel decryption not configured — set <code>testChannelSecret</code> in the health check config to enable receipt detection.</div>' : ''}
        <div id="hc-map" style="height:260px;border-radius:4px;overflow:hidden;margin-bottom:1rem;"></div>
        <div class="hc-create">
          ${regions.length > 0 ? `<div class="hc-field">
            <label>Region filter</label>
            <select id="hc-region">
              <option value="">All regions</option>
              ${regions.map(r => `<option value="${escHtml(r)}">${escHtml(r)}</option>`).join('')}
            </select>
          </div>` : ''}
          <div class="hc-field">
            <label>Expected observers <span class="hc-hint">(leave empty to use all active observers)</span></label>
            <div id="hc-obs-list" class="hc-obs-list"></div>
          </div>
          ${turnstile.enabled ? '<div id="hc-turnstile" class="hc-field"></div>' : ''}
          <button id="hc-start" class="btn btn-primary">Generate Code</button>
        </div>
      </div>`;

    renderObsList('');

    const regionSel = app.querySelector('#hc-region');
    if (regionSel) {
      regionSel.addEventListener('change', function () { renderObsList(this.value); });
    }
    app.querySelector('#hc-start').addEventListener('click', onCreateSession);

    if (turnstile.enabled) {
      loadTurnstile(turnstile.siteKey);
    }

    // Show all known observers on the preview map (scope = directory, no session).
    mapScope = 'directory';
    initMap();
    requestAnimationFrame(function () {
      if (map) map.invalidateSize();
      refreshMapMarkers();
    });
  }

  function renderObsList(region) {
    const directory = bootstrap.observerDirectory || [];
    const obs = directory.filter(function (o) {
      return (!region || o.region === region);
    });
    const container = currentApp && currentApp.querySelector('#hc-obs-list');
    if (!container) return;
    if (obs.length === 0) {
      container.innerHTML = '<span class="hc-hint">No observers in directory yet — they appear as packets are received over MQTT.</span>';
      return;
    }
    container.innerHTML = obs.map(function (o) {
      const label = o.name || o.shortKey || o.key;
      const regionTag = o.region ? `<span class="hc-region-tag">${escHtml(o.region)}</span>` : '';
      const activeDot = o.isActive ? '<span class="hc-active-dot" title="Active">●</span>' : '';
      return `<label class="hc-obs-item">
        <input type="checkbox" value="${escHtml(o.key)}" data-name="${escHtml(label)}">
        ${activeDot}${escHtml(label)}${regionTag}
      </label>`;
    }).join('');
  }

  function loadTurnstile(siteKey) {
    const render = function () {
      if (window.turnstile) {
        window.turnstile.render('#hc-turnstile', {
          sitekey: siteKey,
          callback: onTurnstileSuccess,
        });
      }
    };
    if (window.turnstile) { render(); return; }
    const s = document.createElement('script');
    s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js';
    s.onload = render;
    document.head.appendChild(s);
  }

  async function onTurnstileSuccess(token) {
    try {
      await fetch('/api/health/verify-turnstile', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token }),
      });
    } catch (e) {
      console.error('[health] Turnstile verify error', e);
    }
  }

  async function onCreateSession() {
    const checked = Array.from((currentApp || document).querySelectorAll('#hc-obs-list input:checked'));
    const expectedObserverKeys = checked.map(function (c) { return c.value; });
    const allowlistEnabled = checked.length > 0;

    const btn = currentApp && currentApp.querySelector('#hc-start');
    if (btn) { btn.disabled = true; btn.textContent = 'Creating…'; }

    let res;
    try {
      res = await fetch('/api/health/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ allowlistEnabled, expectedObserverKeys }),
      });
    } catch (e) {
      if (btn) { btn.disabled = false; btn.textContent = 'Generate Code'; }
      alert('Network error. Please try again.');
      return;
    }

    if (res.status === 401) {
      if (btn) { btn.disabled = false; btn.textContent = 'Generate Code'; }
      alert('Bot verification required. Please complete the Turnstile challenge first.');
      return;
    }
    if (res.status === 429) {
      if (btn) { btn.disabled = false; btn.textContent = 'Generate Code'; }
      alert('Rate limit reached. Please wait before creating another session.');
      return;
    }
    if (!res.ok) {
      if (btn) { btn.disabled = false; btn.textContent = 'Generate Code'; }
      alert('Failed to create session. Please try again.');
      return;
    }

    currentSession = await res.json();
    saveSessionHistory(currentSession.id, currentSession.code);
    renderLiveState(currentApp);
  }

  // ── LIVE STATE ─────────────────────────────────────────────────────────────
  function renderLiveState(app) {
    if (!app) return;
    // Tear down the create-form map before replacing innerHTML.
    if (mapThemeObs) { mapThemeObs.disconnect(); mapThemeObs = null; }
    if (map) { map.remove(); map = null; }
    markers = {};

    const testChannel = bootstrap.testChannel || {};
    const channelName = testChannel.name || 'test';

    app.innerHTML = `
      <div class="hc-wrap">
        <h2>Mesh Health Check</h2>
        <div class="hc-code-block">
          <span class="hc-label">Broadcast this code to <strong>#${escHtml(channelName)}</strong>:</span>
          <span class="hc-code" id="hc-code">${escHtml(currentSession.code)}</span>
          <button class="hc-copy btn" id="hc-copy">Copy</button>
        </div>

        <div class="hc-score-block" id="hc-score-block">
          <span class="hc-score-label" id="hc-score-label">Waiting for first receipt…</span>
          <span class="hc-score-pct" id="hc-score-pct"></span>
        </div>

        <div class="hc-map-controls">
          <label><input type="radio" name="hc-scope" value="expected" ${currentSession.allowlistEnabled ? 'checked' : ''}> Expected only</label>
          <label><input type="radio" name="hc-scope" value="directory" ${!currentSession.allowlistEnabled ? 'checked' : ''}> All known</label>
        </div>
        <div id="hc-map" style="height:320px;border-radius:4px;overflow:hidden;margin-bottom:1rem;"></div>

        <div class="hc-timeline">
          <h4>Receipt timeline</h4>
          <div id="hc-timeline-bars"></div>
        </div>

        <div id="hc-share-block" style="display:none;margin-top:1rem;">
          <button id="hc-share" class="btn">Copy share link</button>
        </div>
      </div>`;

    app.querySelector('#hc-copy').addEventListener('click', function () {
      navigator.clipboard.writeText(currentSession.code).catch(function () {});
      this.textContent = 'Copied!';
      setTimeout(function () { const btn = currentApp && currentApp.querySelector('#hc-copy'); if (btn) btn.textContent = 'Copy'; }, 1500);
    });

    app.querySelectorAll('input[name="hc-scope"]').forEach(function (radio) {
      radio.addEventListener('change', function () {
        mapScope = this.value;
        refreshMapMarkers();
      });
    });

    app.querySelector('#hc-share').addEventListener('click', function () {
      if (!currentSession || !currentSession.id) return;
      const url = window.location.origin + '/share/' + currentSession.id;
      navigator.clipboard.writeText(url).catch(function () {});
      this.textContent = 'Copied!';
      setTimeout(function () { const btn = currentApp && currentApp.querySelector('#hc-share'); if (btn) btn.textContent = 'Copy share link'; }, 1500);
    });

    mapScope = currentSession.allowlistEnabled ? 'expected' : 'directory';

    initMap();
    requestAnimationFrame(function () {
      if (map) map.invalidateSize();
      refreshMapMarkers();
    });
  }

  // ── MAP ────────────────────────────────────────────────────────────────────
  const TILE_LIGHT = 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png';
  const TILE_DARK  = 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png';
  const TILE_ATTR  = '&copy; <a href="https://www.openstreetmap.org/copyright">OSM</a>';

  function initMap() {
    if (!window.L || !currentApp) return;
    const el = currentApp.querySelector('#hc-map');
    if (!el) return;
    map = L.map(el, { zoomControl: true }).setView([20, 0], 2);
    mapTileLayer = L.tileLayer(isDarkTheme() ? TILE_DARK : TILE_LIGHT, {
      maxZoom: 18, attribution: TILE_ATTR,
    }).addTo(map);

    // Auto-swap tile layer when the app theme changes.
    mapThemeObs = new MutationObserver(function () {
      if (mapTileLayer) {
        mapTileLayer.setUrl(isDarkTheme() ? TILE_DARK : TILE_LIGHT);
      }
    });
    mapThemeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  }

  function refreshMapMarkers() {
    if (!map || !currentApp) return;

    // Build the set of observers to display on the map.
    const directory = bootstrap.observerDirectory || [];
    let obsToShow;
    if (mapScope === 'expected' && currentSession && currentSession.allowlistEnabled) {
      const expectedSet = new Set(currentSession.expectedObserverKeys || []);
      obsToShow = directory.filter(function (o) { return expectedSet.has(o.key); });
    } else {
      obsToShow = directory;
    }

    const seenKeys = new Set(
      (currentSession && currentSession.receipts || []).map(function (r) { return r.observerKey; })
    );

    // Remove stale markers.
    const showSet = new Set(obsToShow.map(function (o) { return o.key; }));
    Object.keys(markers).forEach(function (key) {
      if (!showSet.has(key)) {
        map.removeLayer(markers[key]);
        delete markers[key];
      }
    });

    const bounds = [];
    obsToShow.forEach(function (obs) {
      // Only plot observers that have coordinates.
      if (!obs.hasLocation || obs.lat == null || obs.lon == null) return;
      const seen = seenKeys.has(obs.key);
      const color = seen ? '#22c55e' : '#6b7280';
      const label = escHtml(obs.name || obs.shortKey || obs.key);
      const keyPreview = escHtml(obs.key.length > 12 ? obs.key.slice(0, 12) + '…' : obs.key);
      const popup = `<b>${label}</b><br>${seen ? '✓ Received' : 'Not seen yet'}<br><code>${keyPreview}</code>`;

      if (markers[obs.key]) {
        markers[obs.key].setIcon(makeMarkerIcon(color));
        if (markers[obs.key].getPopup()) markers[obs.key].getPopup().setContent(popup);
      } else {
        markers[obs.key] = L.marker([obs.lat, obs.lon], { icon: makeMarkerIcon(color) })
          .bindPopup(popup)
          .addTo(map);
      }
      bounds.push([obs.lat, obs.lon]);
    });

    if (bounds.length > 0) {
      map.fitBounds(bounds, { padding: [20, 20], maxZoom: 10 });
    }
  }

  function makeMarkerIcon(color) {
    return L.divIcon({
      className: '',
      html: '<div style="width:14px;height:14px;border-radius:50%;background:' + escHtml(color) + ';border:2px solid #fff;box-shadow:0 1px 3px rgba(0,0,0,.45)"></div>',
      iconSize: [14, 14],
      iconAnchor: [7, 7],
    });
  }

  // ── RECEIPT EVENTS ─────────────────────────────────────────────────────────
  function onReceipt(data) {
    if (!currentSession) return;
    if (!currentSession.receipts) currentSession.receipts = [];

    const existing = currentSession.receipts.find(function (r) { return r.observerKey === data.receipt.observerKey; });
    if (existing) {
      existing.count = (existing.count || 1) + 1;
      existing.lastSeenAt = data.receipt.lastSeenAt;
      if (data.receipt.rssi) existing.rssi = data.receipt.rssi;
      if (data.receipt.snr) existing.snr = data.receipt.snr;
      if (data.receipt.path && data.receipt.path.length) existing.path = data.receipt.path;
    } else {
      currentSession.receipts.push(data.receipt);
    }
    currentSession.status = data.status;

    // Keep obsById up-to-date with any new observer name from the receipt.
    if (data.receipt.observerKey && data.receipt.observerName && !obsById[data.receipt.observerKey]) {
      obsById[data.receipt.observerKey] = { key: data.receipt.observerKey, name: data.receipt.observerName };
    }

    updateScore(data.score);
    refreshMapMarkers();
    renderTimeline();

    const shareBlock = currentApp && currentApp.querySelector('#hc-share-block');
    if (shareBlock) shareBlock.style.display = 'block';

    if (data.status === 'exhausted' || data.status === 'expired') {
      renderResultState();
    }
  }

  function updateScore(score) {
    if (!currentApp || !score) return;
    const labelEl = currentApp.querySelector('#hc-score-label');
    const pctEl = currentApp.querySelector('#hc-score-pct');
    if (!labelEl) return;
    labelEl.textContent = score.label;
    labelEl.className = 'hc-score-label hc-score-' + score.label.toLowerCase().replace(/\s+/g, '-');
    if (pctEl) {
      pctEl.textContent = score.expectedCount > 0
        ? Math.round(score.percentage) + '% (' + score.seenCount + '/' + score.expectedCount + ')'
        : score.seenCount + ' observer' + (score.seenCount !== 1 ? 's' : '') + ' received';
    }
  }

  function renderTimeline() {
    if (!currentApp || !currentSession || !currentSession.receipts) return;
    const container = currentApp.querySelector('#hc-timeline-bars');
    if (!container) return;

    const receipts = currentSession.receipts.slice().sort(function (a, b) { return a.firstSeenAt - b.firstSeenAt; });
    if (receipts.length === 0) { container.innerHTML = ''; return; }

    const t0 = receipts[0].firstSeenAt;
    const tMax = (receipts[receipts.length - 1].firstSeenAt - t0) || 1;

    container.innerHTML = receipts.map(function (r) {
      const pct = tMax > 0 ? ((r.firstSeenAt - t0) / tMax * 100) : 0;
      const obs = obsById[r.observerKey];
      const displayName = (obs && obs.name) || r.observerName || (r.observerKey ? r.observerKey.slice(0, 12) + '…' : '?');
      const snrText = r.snr ? r.snr.toFixed(1) + ' dB' : '';
      const pathText = r.path && r.path.length ? r.path.length + ' hop' + (r.path.length !== 1 ? 's' : '') : '';
      return '<div class="hc-tl-row">'
        + '<span class="hc-tl-name">' + escHtml(displayName) + '</span>'
        + '<div class="hc-tl-bar-wrap"><div class="hc-tl-bar" style="width:' + Math.max(2, pct).toFixed(1) + '%"></div></div>'
        + '<span class="hc-tl-snr">' + escHtml(snrText || pathText) + '</span>'
        + '</div>';
    }).join('');
  }

  // ── RESULT STATE ───────────────────────────────────────────────────────────
  function renderResultState() {
    if (!currentApp) return;
    const shareBlock = currentApp.querySelector('#hc-share-block');
    if (shareBlock) shareBlock.style.display = 'block';
    const scoreBlock = currentApp.querySelector('#hc-score-block');
    if (scoreBlock && !scoreBlock.querySelector('.hc-done-badge')) {
      const badge = document.createElement('span');
      badge.className = 'hc-done-badge';
      badge.textContent = currentSession.status === 'exhausted' ? 'Complete' : 'Expired';
      scoreBlock.appendChild(badge);
    }
  }

  // ── SESSION HISTORY ────────────────────────────────────────────────────────
  function saveSessionHistory(id, code) {
    try {
      const stored = JSON.parse(localStorage.getItem('hc-sessions') || '[]');
      stored.unshift({ id: id, code: code, createdAt: Date.now() });
      localStorage.setItem('hc-sessions', JSON.stringify(stored.slice(0, 20)));
    } catch (_) {}
  }

  // ── UTILS ──────────────────────────────────────────────────────────────────
  function escHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  registerPage('health-check', { init: init, destroy: destroy });
})();
