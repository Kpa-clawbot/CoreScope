/* === CoreScope — los.js (Line-of-Sight Analyzer) === */
'use strict';

(function () {
  var losMap = null;
  var markerA = null;
  var markerB = null;
  var losPolyline = null;
  var relayMarker = null;
  var losChart = null;
  var pickingPoint = null; // 'a' | 'b' | null
  var _cleanups = []; // teardown callbacks for destroy()

  // ── Icons ──────────────────────────────────────────────────────────────────
  function makePin(color) {
    return L.divIcon({
      html: '<div style="width:14px;height:14px;background:' + color + ';border:2px solid #fff;border-radius:50%;box-shadow:0 1px 3px rgba(0,0,0,0.4)"></div>',
      className: '',
      iconSize: [14, 14],
      iconAnchor: [7, 7],
    });
  }

  function makeTowerIcon() {
    return L.divIcon({
      html: '<div title="Suggested relay" style="font-size:20px;line-height:1;text-shadow:0 1px 2px rgba(0,0,0,0.5)">📡</div>',
      className: '',
      iconSize: [24, 24],
      iconAnchor: [12, 12],
    });
  }

  // ── Map setup ──────────────────────────────────────────────────────────────
  function initMap(container) {
    if (losMap) { losMap.remove(); losMap = null; }
    losMap = L.map(container, { zoomControl: true, attributionControl: false });
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 18,
    }).addTo(losMap);
    losMap.setView([52.0, 5.0], 9);
    losMap.on('click', onMapClick);
  }

  function onMapClick(e) {
    if (!pickingPoint) return;
    var lat = e.latlng.lat.toFixed(6);
    var lon = e.latlng.lng.toFixed(6);
    if (pickingPoint === 'a') setPointA(lat, lon);
    else setPointB(lat, lon);
    stopPickMode();
  }

  function setPointA(lat, lon) {
    document.getElementById('los-lat-a').value = lat;
    document.getElementById('los-lon-a').value = lon;
    if (markerA) markerA.remove();
    markerA = L.marker([parseFloat(lat), parseFloat(lon)], { icon: makePin('#3b82f6') })
      .addTo(losMap).bindTooltip('Point A').openTooltip();
    fitMapToPoints();
    updatePolyline();
  }

  function setPointB(lat, lon) {
    document.getElementById('los-lat-b').value = lat;
    document.getElementById('los-lon-b').value = lon;
    if (markerB) markerB.remove();
    markerB = L.marker([parseFloat(lat), parseFloat(lon)], { icon: makePin('#ef4444') })
      .addTo(losMap).bindTooltip('Point B').openTooltip();
    fitMapToPoints();
    updatePolyline();
  }

  function fitMapToPoints() {
    if (markerA && markerB) {
      losMap.fitBounds(L.latLngBounds(markerA.getLatLng(), markerB.getLatLng()).pad(0.2));
    } else if (markerA) {
      losMap.setView(markerA.getLatLng(), 12);
    } else if (markerB) {
      losMap.setView(markerB.getLatLng(), 12);
    }
  }

  function updatePolyline() {
    if (losPolyline) { losPolyline.remove(); losPolyline = null; }
    if (markerA && markerB) {
      losPolyline = L.polyline([markerA.getLatLng(), markerB.getLatLng()], {
        color: '#3b82f6', weight: 2, dashArray: '6,4', opacity: 0.7,
      }).addTo(losMap);
    }
  }

  function startPickMode(point) {
    pickingPoint = point;
    losMap.getContainer().style.cursor = 'crosshair';
    var btnId = point === 'a' ? 'los-pick-a' : 'los-pick-b';
    var btn = document.getElementById(btnId);
    if (btn) { btn.textContent = 'Cancel'; btn.classList.add('los-pick-active'); }
  }

  function stopPickMode() {
    pickingPoint = null;
    losMap.getContainer().style.cursor = '';
    ['los-pick-a', 'los-pick-b'].forEach(function (id) {
      var btn = document.getElementById(id);
      if (btn) { btn.textContent = '📍 Pick'; btn.classList.remove('los-pick-active'); }
    });
  }

  // ── Node autocomplete ──────────────────────────────────────────────────────
  function setupAutocomplete(inputId, latId, lonId, setPointFn) {
    var input = document.getElementById(inputId);
    var list = document.getElementById(inputId + '-list');
    if (!input || !list) return;
    var debounce = null;
    function onInput() {
      clearTimeout(debounce);
      var q = input.value.trim();
      if (q.length < 2) { list.innerHTML = ''; list.hidden = true; return; }
      debounce = setTimeout(function () {
        fetch('/api/nodes/search?q=' + encodeURIComponent(q) + '&limit=8')
          .then(function (r) { return r.ok ? r.json() : []; })
          .then(function (nodes) {
            list.innerHTML = '';
            if (!nodes || !nodes.length) { list.hidden = true; return; }
            list.hidden = false;
            nodes.forEach(function (node) {
              if (!node.latitude || !node.longitude) return;
              var li = document.createElement('li');
              li.className = 'los-autocomplete-item';
              li.textContent = (node.name || node.public_key.slice(0, 12)) +
                ' (' + (+node.latitude).toFixed(4) + ', ' + (+node.longitude).toFixed(4) + ')';
              li.addEventListener('mousedown', function (e) {
                e.preventDefault();
                input.value = node.name || node.public_key.slice(0, 12);
                list.innerHTML = ''; list.hidden = true;
                setPointFn((+node.latitude).toFixed(6), (+node.longitude).toFixed(6));
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
    var latA = parseFloat(document.getElementById('los-lat-a').value);
    var lonA = parseFloat(document.getElementById('los-lon-a').value);
    var latB = parseFloat(document.getElementById('los-lat-b').value);
    var lonB = parseFloat(document.getElementById('los-lon-b').value);
    var htA  = parseFloat(document.getElementById('los-ht-a').value) || 2;
    var htB  = parseFloat(document.getElementById('los-ht-b').value) || 2;

    if (isNaN(latA) || isNaN(lonA) || isNaN(latB) || isNaN(lonB)) {
      showError('Please set both Point A and Point B before running.');
      return;
    }

    var resultEl = document.getElementById('los-result');
    resultEl.innerHTML = '<div class="los-spinner">⏳ Fetching elevation data…</div>';

    fetch('/api/los', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ lat_a: latA, lon_a: lonA, lat_b: latB, lon_b: lonB,
                             antenna_height_a: htA, antenna_height_b: htB }),
    })
      .then(function (r) {
        if (!r.ok) return r.json().then(function (e) { throw new Error(e.error || 'Server error'); });
        return r.json();
      })
      .then(renderResult)
      .catch(function (err) {
        showError(err.message || 'Elevation API unavailable. Try again later.', true);
      });
  }

  function renderResult(data) {
    var resultEl = document.getElementById('los-result');
    var statusClass = data.los_clear ? 'los-clear' : 'los-blocked';
    var statusText  = data.los_clear
      ? '🟢 Clear — direct LOS confirmed'
      : '🔴 Blocked — ' + data.max_violation_m.toFixed(1) + ' m max violation';

    if (relayMarker) { relayMarker.remove(); relayMarker = null; }
    var relayHtml = '';
    if (data.relay) {
      relayHtml = '<div class="los-relay-info">' +
        '📡 Relay suggestion: <strong>' + data.relay.lat.toFixed(5) + '°, ' +
        data.relay.lon.toFixed(5) + '°</strong>' +
        ' (' + Math.round(data.relay.terrain_elev) + ' m ASL)' +
        ' <button class="los-btn los-btn-sm" id="los-show-relay">Show on map</button>' +
        '</div>';
      relayMarker = L.marker([data.relay.lat, data.relay.lon], { icon: makeTowerIcon() })
        .addTo(losMap)
        .bindTooltip('Relay suggestion (' + Math.round(data.relay.terrain_elev) + ' m ASL)');
    }

    var gapsHtml = data.data_gaps
      ? '<div class="los-warning">⚠️ Some elevation values unavailable — estimated as sea level.</div>'
      : '';

    resultEl.innerHTML =
      '<div class="los-status ' + statusClass + '">' + statusText + '</div>' +
      '<div class="los-distance">Distance: <strong>' + data.distance_km.toFixed(2) + ' km</strong></div>' +
      gapsHtml +
      '<div class="los-chart-wrap"><canvas id="los-chart"></canvas></div>' +
      relayHtml;

    if (data.relay) {
      var showBtn = document.getElementById('los-show-relay');
      if (showBtn) {
        showBtn.addEventListener('click', function () {
          losMap.setView([data.relay.lat, data.relay.lon], 13);
        });
      }
    }

    renderChart(data.profile, data.distance_km);
  }

  function renderChart(profile, totalKm) {
    var canvas = document.getElementById('los-chart');
    if (!canvas || typeof Chart === 'undefined') return;
    if (losChart) { losChart.destroy(); losChart = null; }

    var n = profile.length;
    var labels = profile.map(function (_, i) {
      return (i / Math.max(n - 1, 1) * totalKm).toFixed(2);
    });
    var terrain = profile.map(function (p) { return p.terrain_elev; });
    var losLine  = profile.map(function (p) { return p.los_elev + p.bulge; });

    losChart = new Chart(canvas, {
      type: 'line',
      data: {
        labels: labels,
        datasets: [
          {
            label: 'Terrain (m ASL)',
            data: terrain,
            fill: true,
            backgroundColor: 'rgba(139,90,43,0.35)',
            borderColor: 'rgba(139,90,43,0.8)',
            borderWidth: 1.5,
            pointRadius: 0,
            tension: 0.2,
          },
          {
            label: 'LOS line (with curvature)',
            data: losLine,
            fill: false,
            borderColor: 'rgba(59,130,246,0.85)',
            borderWidth: 2,
            borderDash: [6, 3],
            pointRadius: 0,
            tension: 0.1,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
          legend: { display: true, position: 'top' },
          tooltip: {
            callbacks: {
              title: function (items) { return 'Distance: ' + items[0].label + ' km'; },
              label: function (item) {
                return item.dataset.label + ': ' + (+item.raw).toFixed(1) + ' m';
              },
            },
          },
        },
        scales: {
          x: { title: { display: true, text: 'Distance (km)' }, ticks: { maxTicksLimit: 10 } },
          y: { title: { display: true, text: 'Elevation (m ASL)' } },
        },
      },
    });
  }

  function showError(msg, retryable) {
    var resultEl = document.getElementById('los-result');
    var retryBtn = retryable
      ? '<button class="los-btn los-btn-primary" id="los-retry" style="margin-top:8px">Retry</button>'
      : '';
    resultEl.innerHTML = '<div class="los-error">❌ ' + msg + '</div>' + retryBtn;
    if (retryable) {
      var btn = document.getElementById('los-retry');
      if (btn) btn.addEventListener('click', runAnalysis);
    }
  }

  // ── Layout ─────────────────────────────────────────────────────────────────
  function buildHTML() {
    return '<div class="los-page">' +
      '<h2>🔭 Line-of-Sight Analyzer</h2>' +
      '<div class="los-body">' +
        '<div class="los-controls">' +
          '<div class="los-point-group">' +
            '<h3>Point A</h3>' +
            '<div class="los-autocomplete-wrap">' +
              '<input id="los-node-a" class="los-input" type="text" placeholder="Search node…" autocomplete="off">' +
              '<ul id="los-node-a-list" class="los-autocomplete-list" hidden></ul>' +
            '</div>' +
            '<div class="los-coord-row">' +
              '<label>Lat <input id="los-lat-a" class="los-input los-coord" type="number" step="any" placeholder="52.000"></label>' +
              '<label>Lon <input id="los-lon-a" class="los-input los-coord" type="number" step="any" placeholder="4.000"></label>' +
              '<button id="los-pick-a" class="los-btn los-pick-btn">📍 Pick</button>' +
            '</div>' +
            '<label class="los-ht-label">Antenna height (m) <input id="los-ht-a" class="los-input los-coord" type="number" value="2" min="0" step="0.5"></label>' +
          '</div>' +
          '<div class="los-point-group">' +
            '<h3>Point B</h3>' +
            '<div class="los-autocomplete-wrap">' +
              '<input id="los-node-b" class="los-input" type="text" placeholder="Search node…" autocomplete="off">' +
              '<ul id="los-node-b-list" class="los-autocomplete-list" hidden></ul>' +
            '</div>' +
            '<div class="los-coord-row">' +
              '<label>Lat <input id="los-lat-b" class="los-input los-coord" type="number" step="any" placeholder="52.100"></label>' +
              '<label>Lon <input id="los-lon-b" class="los-input los-coord" type="number" step="any" placeholder="4.100"></label>' +
              '<button id="los-pick-b" class="los-btn los-pick-btn">📍 Pick</button>' +
            '</div>' +
            '<label class="los-ht-label">Antenna height (m) <input id="los-ht-b" class="los-input los-coord" type="number" value="2" min="0" step="0.5"></label>' +
          '</div>' +
          '<button id="los-run" class="los-btn los-btn-primary">Run Analysis</button>' +
          '<div id="los-result" class="los-result-area"></div>' +
        '</div>' +
        '<div id="los-map" class="los-map"></div>' +
      '</div>' +
    '</div>';
  }

  function buildCSS() {
    if (document.getElementById('los-styles')) return;
    var style = document.createElement('style');
    style.id = 'los-styles';
    style.textContent = [
      '.los-page { padding: 20px; max-width: 1200px; margin: 0 auto; }',
      '.los-page h2 { margin-bottom: 16px; font-size: 1.4rem; }',
      '.los-body { display: flex; gap: 20px; }',
      '.los-controls { flex: 0 0 340px; display: flex; flex-direction: column; gap: 16px; }',
      '.los-map { flex: 1; min-height: 480px; border-radius: 8px; border: 1px solid var(--border); }',
      '.los-point-group { background: var(--card-bg); border: 1px solid var(--border); border-radius: 8px; padding: 14px; }',
      '.los-point-group h3 { margin: 0 0 10px; font-size: 0.95rem; }',
      '.los-input { background: var(--input-bg); border: 1px solid var(--border); color: var(--text); border-radius: 4px; padding: 6px 8px; font-size: 13px; width: 100%; box-sizing: border-box; }',
      '.los-coord-row { display: flex; gap: 6px; align-items: flex-end; margin-top: 8px; }',
      '.los-coord-row label { flex: 1; font-size: 12px; color: var(--text-muted); }',
      '.los-coord { margin-top: 3px; }',
      '.los-ht-label { font-size: 12px; color: var(--text-muted); display: block; margin-top: 8px; }',
      '.los-ht-label .los-coord { width: 80px; }',
      '.los-btn { padding: 7px 14px; border-radius: 6px; border: 1px solid var(--border); cursor: pointer; font-size: 13px; background: var(--card-bg); color: var(--text); }',
      '.los-btn:hover { background: var(--row-hover); }',
      '.los-btn-primary { background: var(--accent); color: #fff; border-color: var(--accent); font-weight: 600; width: 100%; padding: 10px; }',
      '.los-btn-primary:hover { background: var(--accent-hover); border-color: var(--accent-hover); }',
      '.los-pick-btn { flex: 0 0 auto; padding: 6px 10px; font-size: 12px; width: auto; }',
      '.los-pick-active { background: var(--status-yellow, #f59e0b) !important; color: #fff !important; border-color: transparent !important; }',
      '.los-autocomplete-wrap { position: relative; }',
      '.los-autocomplete-list { position: absolute; top: 100%; left: 0; right: 0; background: var(--card-bg); border: 1px solid var(--border); border-radius: 4px; list-style: none; margin: 2px 0 0; padding: 0; max-height: 200px; overflow-y: auto; z-index: 500; }',
      '.los-autocomplete-item { padding: 7px 10px; font-size: 12px; cursor: pointer; }',
      '.los-autocomplete-item:hover { background: var(--row-hover); }',
      '.los-result-area { margin-top: 4px; }',
      '.los-spinner { padding: 16px; text-align: center; color: var(--text-muted); font-size: 13px; }',
      '.los-status { padding: 10px 14px; border-radius: 6px; font-weight: 600; font-size: 14px; margin-bottom: 8px; }',
      '.los-clear { background: rgba(34,197,94,0.12); color: var(--status-green, #22c55e); border: 1px solid rgba(34,197,94,0.3); }',
      '.los-blocked { background: rgba(239,68,68,0.10); color: var(--status-red, #ef4444); border: 1px solid rgba(239,68,68,0.3); }',
      '.los-distance { font-size: 13px; color: var(--text-muted); margin-bottom: 8px; }',
      '.los-warning { font-size: 12px; color: var(--status-yellow, #f59e0b); padding: 6px 8px; background: rgba(245,158,11,0.1); border-radius: 4px; margin-bottom: 8px; }',
      '.los-error { padding: 10px 14px; background: rgba(239,68,68,0.08); color: var(--status-red, #ef4444); border-radius: 6px; font-size: 13px; }',
      '.los-chart-wrap { height: 200px; margin-bottom: 10px; }',
      '.los-relay-info { font-size: 13px; padding: 8px 10px; background: var(--section-bg, var(--card-bg)); border: 1px solid var(--border); border-radius: 6px; }',
      '.los-btn-sm { padding: 3px 8px; font-size: 12px; width: auto; margin-left: 6px; }',
      '@media (max-width: 768px) {',
      '  .los-body { flex-direction: column; }',
      '  .los-controls { flex: none; }',
      '  .los-map { min-height: 300px; }',
      '}',
    ].join('\n');
    document.head.appendChild(style);
  }

  // ── Register page ──────────────────────────────────────────────────────────
  registerPage('los', {
    init: function (container) {
      buildCSS();
      container.innerHTML = buildHTML();
      setTimeout(function () {
        initMap(document.getElementById('los-map'));
        setupAutocomplete('los-node-a', 'los-lat-a', 'los-lon-a', setPointA);
        setupAutocomplete('los-node-b', 'los-lat-b', 'los-lon-b', setPointB);
        document.getElementById('los-pick-a').addEventListener('click', function () {
          if (pickingPoint === 'a') stopPickMode(); else startPickMode('a');
        });
        document.getElementById('los-pick-b').addEventListener('click', function () {
          if (pickingPoint === 'b') stopPickMode(); else startPickMode('b');
        });
        document.getElementById('los-run').addEventListener('click', runAnalysis);
      }, 0);
    },
    destroy: function () {
      _cleanups.forEach(function (fn) { fn(); });
      _cleanups = [];
      if (losMap) {
        losMap.off('click', onMapClick);
        losMap.remove();
        losMap = null;
      }
      if (losChart) { losChart.destroy(); losChart = null; }
      markerA = markerB = losPolyline = relayMarker = null;
      pickingPoint = null;
      var s = document.getElementById('los-styles');
      if (s) s.remove();
    },
  });
})();
