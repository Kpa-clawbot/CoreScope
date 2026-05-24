/* === CoreScope — rf-coverage.js (RF Coverage Analyzer) === */
'use strict';

(function () {
  var rfMap = null;
  var txMarker = null;
  var coveragePolygon = null;
  var pickingTX = false;
  var _cleanups = [];

  // ── Icons ──────────────────────────────────────────────────────────────────
  function makeTXIcon() {
    return L.divIcon({
      html: '<div title="TX Node" style="font-size:20px;line-height:1;text-shadow:0 1px 2px rgba(0,0,0,0.5)">📡</div>',
      className: '',
      iconSize: [24, 24],
      iconAnchor: [12, 12],
    });
  }

  // ── Map setup ──────────────────────────────────────────────────────────────
  function initMap(container) {
    if (rfMap) { rfMap.remove(); rfMap = null; }
    rfMap = L.map(container, { zoomControl: true, attributionControl: false });
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 18,
    }).addTo(rfMap);
    rfMap.setView([52.0, 5.0], 9);
    rfMap.on('click', onMapClick);
  }

  function onMapClick(e) {
    if (!pickingTX) return;
    setTX(e.latlng.lat.toFixed(6), e.latlng.lng.toFixed(6));
    stopPickMode();
  }

  function setTX(lat, lon) {
    var latEl = document.getElementById('rfc-lat');
    var lonEl = document.getElementById('rfc-lon');
    if (latEl) latEl.value = lat;
    if (lonEl) lonEl.value = lon;
    if (txMarker) txMarker.remove();
    txMarker = L.marker([parseFloat(lat), parseFloat(lon)], { icon: makeTXIcon() })
      .addTo(rfMap)
      .bindTooltip('TX Node').openTooltip();
    rfMap.setView([parseFloat(lat), parseFloat(lon)], rfMap.getZoom() < 10 ? 11 : rfMap.getZoom());
  }

  function startPickMode() {
    pickingTX = true;
    rfMap.getContainer().style.cursor = 'crosshair';
    var btn = document.getElementById('rfc-pick-tx');
    if (btn) { btn.textContent = 'Cancel'; btn.classList.add('rfc-pick-active'); }
  }

  function stopPickMode() {
    pickingTX = false;
    rfMap.getContainer().style.cursor = '';
    var btn = document.getElementById('rfc-pick-tx');
    if (btn) { btn.textContent = '📍 Pick'; btn.classList.remove('rfc-pick-active'); }
  }

  // ── Node autocomplete ──────────────────────────────────────────────────────
  function setupAutocomplete() {
    var input = document.getElementById('rfc-node-search');
    var list  = document.getElementById('rfc-node-search-list');
    if (!input || !list) return;
    var debounce = null;
    function onInput() {
      clearTimeout(debounce);
      var q = input.value.trim();
      if (q.length < 2) { list.innerHTML = ''; list.hidden = true; return; }
      debounce = setTimeout(function () {
        fetch('/api/nodes/search?q=' + encodeURIComponent(q) + '&limit=8')
          .then(function (r) { return r.ok ? r.json() : { nodes: [] }; })
          .then(function (data) {
            var nodes = data.nodes || [];
            list.innerHTML = '';
            if (!nodes.length) { list.hidden = true; return; }
            list.hidden = false;
            nodes.forEach(function (node) {
              if (!node.latitude || !node.longitude) return;
              var li = document.createElement('li');
              li.className = 'rfc-autocomplete-item';
              li.textContent = (node.name || node.public_key.slice(0, 12)) +
                ' (' + (+node.latitude).toFixed(4) + ', ' + (+node.longitude).toFixed(4) + ')';
              li.addEventListener('mousedown', function (e) {
                e.preventDefault();
                input.value = node.name || node.public_key.slice(0, 12);
                list.innerHTML = ''; list.hidden = true;
                setTX((+node.latitude).toFixed(6), (+node.longitude).toFixed(6));
              });
              list.appendChild(li);
            });
          }).catch(function () { list.hidden = true; });
      }, 250);
    }
    function onBlur() {
      setTimeout(function () { list.innerHTML = ''; list.hidden = true; }, 200);
    }
    input.addEventListener('input', onInput);
    input.addEventListener('blur', onBlur);
    _cleanups.push(function () {
      clearTimeout(debounce);
      input.removeEventListener('input', onInput);
      input.removeEventListener('blur', onBlur);
    });
  }

  // ── Analysis ───────────────────────────────────────────────────────────────
  function runAnalysis() {
    var lat   = parseFloat(document.getElementById('rfc-lat').value);
    var lon   = parseFloat(document.getElementById('rfc-lon').value);
    var power = parseFloat(document.getElementById('rfc-power').value) || 20;
    var freq  = parseFloat(document.getElementById('rfc-freq').value)  || 869.618;
    var sf    = parseInt(document.getElementById('rfc-sf').value, 10)  || 7;
    var ht    = parseFloat(document.getElementById('rfc-ht').value)    || 2;
    var model = document.getElementById('rfc-model').value             || 'free';

    if (isNaN(lat) || isNaN(lon)) {
      showStatus('error', '❌ Set the TX position before running.');
      return;
    }

    showStatus('info', '⏳ Computing coverage…');

    fetch('/api/rf-coverage', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        lat: lat, lon: lon,
        tx_power_dbm: power, freq_mhz: freq,
        sf: sf, antenna_height: ht, model: model,
      }),
    })
      .then(function (r) {
        if (!r.ok) return r.json().then(function (e) { throw new Error(e.error || 'Server error'); });
        return r.json();
      })
      .then(renderResult)
      .catch(function (err) {
        showStatus('error', '❌ ' + (err.message || 'Elevation API unavailable.'));
      });
  }

  function renderResult(data) {
    // Draw coverage polygon
    if (coveragePolygon) { coveragePolygon.remove(); coveragePolygon = null; }
    var latlngs = data.coverage.map(function (pt) { return [pt.lat, pt.lon]; });
    if (latlngs.length > 0) {
      latlngs.push(latlngs[0]); // close polygon
      coveragePolygon = L.polygon(latlngs, {
        color: '#3b82f6', weight: 2,
        fillColor: '#3b82f6', fillOpacity: 0.18,
      }).addTo(rfMap);
      rfMap.fitBounds(coveragePolygon.getBounds().pad(0.1));
    }

    var ranges = data.coverage.map(function (pt) { return pt.range_km; });
    var maxKm  = Math.max.apply(null, ranges).toFixed(1);
    var avgKm  = (ranges.reduce(function (a, b) { return a + b; }, 0) / ranges.length).toFixed(1);

    var gapsNote = data.data_gaps
      ? ' <span style="color:#f59e0b">(⚠️ Some elevation data missing)</span>'
      : '';

    var sfLabel = data.sf ? ('SF' + data.sf) : 'SF7';
    showStatus('ok',
      '✅ ' + sfLabel +
      ' · ' + data.freq_mhz.toFixed(3) + ' MHz' +
      ' · ' + data.tx_power_dbm + ' dBm' +
      ' · Model: ' + data.model +
      '<br>Sensitivity: <strong>' + data.sensitivity_dbm + ' dBm</strong>' +
      ' · Max range: <strong>' + maxKm + ' km</strong>' +
      ' · Avg range: <strong>' + avgKm + ' km</strong>' +
      gapsNote
    );
  }

  function showStatus(type, html) {
    var el = document.getElementById('rfc-status');
    if (!el) return;
    el.className = 'rfc-status rfc-status-' + type;
    el.innerHTML = html;
    el.hidden = false;
  }

  // ── Page HTML ──────────────────────────────────────────────────────────────
  function buildHTML() {
    return [
      '<style>',
      '.rfc-layout{display:flex;gap:12px;height:calc(100vh - 120px);min-height:400px}',
      '.rfc-panel{width:300px;flex-shrink:0;display:flex;flex-direction:column;gap:10px;overflow-y:auto}',
      '.rfc-map-wrap{flex:1;border-radius:8px;overflow:hidden;min-height:300px}',
      '#rfc-map{width:100%;height:100%}',
      '.rfc-section{background:var(--surface1,#fff);border:1px solid var(--border,#e2e5ea);',
      '  border-radius:8px;padding:12px}',
      '.rfc-section h3{margin:0 0 8px;font-size:13px;font-weight:600;color:var(--text-muted,#5b6370)}',
      '.rfc-field{display:flex;flex-direction:column;gap:4px;margin-bottom:8px}',
      '.rfc-field label{font-size:12px;font-weight:500;color:var(--text-muted,#5b6370)}',
      '.rfc-field input,.rfc-field select{',
      '  padding:6px 8px;border:1px solid var(--border,#ddd);border-radius:6px;',
      '  font-size:13px;background:var(--input-bg,#fff);color:var(--text,#1a1a2e);width:100%;box-sizing:border-box}',
      '.rfc-coord-row{display:flex;gap:6px}',
      '.rfc-coord-row input{flex:1}',
      '.rfc-btn{padding:7px 14px;border:none;border-radius:6px;cursor:pointer;',
      '  font-size:13px;font-weight:500;transition:opacity .15s}',
      '.rfc-btn:hover{opacity:.85}',
      '.rfc-btn-primary{background:#3b82f6;color:#fff;width:100%;margin-top:4px}',
      '.rfc-btn-outline{background:transparent;border:1px solid var(--border,#ddd);',
      '  color:var(--text,#333);font-size:12px;padding:5px 10px}',
      '.rfc-pick-active{background:#f59e0b !important;color:#fff !important}',
      '.rfc-autocomplete-list{position:absolute;z-index:999;background:var(--surface1,#fff);',
      '  border:1px solid var(--border,#ddd);border-radius:6px;list-style:none;',
      '  margin:0;padding:4px 0;max-height:180px;overflow-y:auto;min-width:260px}',
      '.rfc-autocomplete-item{padding:7px 12px;cursor:pointer;font-size:13px}',
      '.rfc-autocomplete-item:hover{background:var(--row-hover,#eef2ff)}',
      '.rfc-autocomplete-wrap{position:relative}',
      '.rfc-status{padding:8px 12px;border-radius:6px;font-size:13px;line-height:1.5}',
      '.rfc-status-ok{background:#dcfce7;color:#166534;border:1px solid #bbf7d0}',
      '.rfc-status-error{background:#fee2e2;color:#991b1b;border:1px solid #fecaca}',
      '.rfc-status-info{background:#dbeafe;color:#1e40af;border:1px solid #bfdbfe}',
      '@media(max-width:640px){.rfc-layout{flex-direction:column}',
      '  .rfc-panel{width:100%;height:auto}.rfc-map-wrap{height:300px}}',
      '</style>',
      '<h2 style="margin:0 0 12px;font-size:18px">📡 RF Coverage Analyzer</h2>',
      '<div class="rfc-layout">',
      '  <div class="rfc-panel">',
      '    <div class="rfc-section">',
      '      <h3>TX Node</h3>',
      '      <div class="rfc-field rfc-autocomplete-wrap">',
      '        <label>Search node</label>',
      '        <input id="rfc-node-search" type="text" placeholder="Name or pubkey…" autocomplete="off">',
      '        <ul id="rfc-node-search-list" class="rfc-autocomplete-list" hidden></ul>',
      '      </div>',
      '      <div class="rfc-field">',
      '        <label>Coordinates</label>',
      '        <div class="rfc-coord-row">',
      '          <input id="rfc-lat" type="number" placeholder="Lat" step="any">',
      '          <input id="rfc-lon" type="number" placeholder="Lon" step="any">',
      '        </div>',
      '      </div>',
      '      <button class="rfc-btn rfc-btn-outline" id="rfc-pick-tx">📍 Pick</button>',
      '    </div>',
      '    <div class="rfc-section">',
      '      <h3>Link Parameters</h3>',
      '      <div class="rfc-field">',
      '        <label>TX Power (dBm)</label>',
      '        <input id="rfc-power" type="number" value="20" min="1" max="30">',
      '      </div>',
      '      <div class="rfc-field">',
      '        <label>Frequency (MHz)</label>',
      '        <input id="rfc-freq" type="number" value="869.618" step="0.001">',
      '      </div>',
      '      <div class="rfc-field">',
      '        <label>Spreading Factor</label>',
      '        <select id="rfc-sf">',
      '          <option value="7">SF7 (−123 dBm)</option>',
      '          <option value="8">SF8 (−126 dBm)</option>',
      '          <option value="9">SF9 (−129 dBm)</option>',
      '          <option value="10">SF10 (−132 dBm)</option>',
      '          <option value="11">SF11 (−134.5 dBm)</option>',
      '          <option value="12">SF12 (−137 dBm)</option>',
      '        </select>',
      '      </div>',
      '      <div class="rfc-field">',
      '        <label>Antenna Height (m)</label>',
      '        <input id="rfc-ht" type="number" value="2" min="1" max="100">',
      '      </div>',
      '      <div class="rfc-field">',
      '        <label>Environment</label>',
      '        <select id="rfc-model">',
      '          <option value="free">Free space (n=2.0)</option>',
      '          <option value="suburban" selected>Suburban (n=2.2)</option>',
      '          <option value="urban">Urban (n=2.3)</option>',
      '          <option value="indoor">Indoor (n=2.7)</option>',
      '        </select>',
      '      </div>',
      '      <button class="rfc-btn rfc-btn-primary" id="rfc-run">▶ Compute Coverage</button>',
      '    </div>',
      '    <div id="rfc-status" class="rfc-status rfc-status-info" hidden></div>',
      '  </div>',
      '  <div class="rfc-map-wrap"><div id="rfc-map"></div></div>',
      '</div>',
    ].join('\n');
  }

  // ── Page lifecycle ─────────────────────────────────────────────────────────
  function init(container) {
    container.innerHTML = buildHTML();

    initMap(document.getElementById('rfc-map'));
    setupAutocomplete();

    var pickBtn = document.getElementById('rfc-pick-tx');
    var runBtn  = document.getElementById('rfc-run');

    function onPickClick() {
      if (pickingTX) stopPickMode();
      else startPickMode();
    }
    function onRunClick() { runAnalysis(); }

    if (pickBtn) pickBtn.addEventListener('click', onPickClick);
    if (runBtn)  runBtn.addEventListener('click', onRunClick);

    _cleanups.push(function () {
      if (pickBtn) pickBtn.removeEventListener('click', onPickClick);
      if (runBtn)  runBtn.removeEventListener('click', onRunClick);
    });
  }

  function destroy() {
    _cleanups.forEach(function (fn) { fn(); });
    _cleanups = [];
    if (rfMap) {
      rfMap.off('click', onMapClick);
      rfMap.remove();
      rfMap = null;
    }
    txMarker = null;
    coveragePolygon = null;
    pickingTX = false;
  }

  // ── Register ───────────────────────────────────────────────────────────────
  if (window.registerPage) {
    window.registerPage('rf-coverage', { init: init, destroy: destroy });
  }
})();
