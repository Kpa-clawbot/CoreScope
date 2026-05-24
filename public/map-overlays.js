/* === CoreScope — map-overlays.js (shared map overlay module) === */
'use strict';

(function () {

  // ─── Route History ────────────────────────────────────────────────────────

  var _rhLayer = null;

  function _rhEdgeColor(count) {
    if (count >= 50) return '#22c55e';
    if (count >= 20) return '#84cc16';
    if (count >= 10) return '#eab308';
    if (count >= 3)  return '#f97316';
    return '#ef4444';
  }

  function _rhEdgeWeight(count) {
    return 2 + Math.min(count / 10, 6);
  }

  function initRouteHistory(map, hours) {
    destroyRouteHistory();
    hours = hours || 24;
    fetch('/api/route-history?hours=' + hours)
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        if (!data || !data.edges || !data.edges.length) return;
        _rhLayer = L.layerGroup().addTo(map);
        data.edges.forEach(function (e) {
          var line = L.polyline(
            [[e.lat_a, e.lon_a], [e.lat_b, e.lon_b]],
            { color: _rhEdgeColor(e.count), weight: _rhEdgeWeight(e.count), opacity: 0.75 }
          );
          var nameA = e.name_a || e.node_a.slice(0, 8) + '…';
          var nameB = e.name_b || e.node_b.slice(0, 8) + '…';
          var sampleLinks = (e.samples || []).map(function (h) {
            return '<a href="#/tools/trace/' + encodeURIComponent(h) + '" style="color:var(--accent,#3b82f6)">' +
              h.slice(0, 8) + '…</a>';
          }).join(' ');
          line.bindPopup(
            '<strong>' + nameA + ' ↔ ' + nameB + '</strong><br>' +
            'Packets: <strong>' + e.count + '</strong><br>' +
            'Last seen: ' + (e.last_seen ? new Date(e.last_seen).toLocaleString() : '—') +
            (sampleLinks ? '<br>Samples: ' + sampleLinks : '')
          );
          _rhLayer.addLayer(line);
        });
      })
      .catch(function () {});
  }

  function destroyRouteHistory() {
    if (_rhLayer) { _rhLayer.clearLayers(); _rhLayer.remove(); _rhLayer = null; }
  }

  // ─── Radar ────────────────────────────────────────────────────────────────

  var _radarLayer   = null;
  var _radarTimer   = null;
  var _radarMap     = null;

  function _fetchRadarFrame() {
    return fetch('https://api.rainviewer.com/public/weather-maps.json')
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        if (!d || !d.radar) return null;
        var frames = (d.radar.nowcast && d.radar.nowcast.length)
          ? d.radar.nowcast
          : (d.radar.past && d.radar.past.length ? d.radar.past : null);
        if (!frames) return null;
        var latest = frames[frames.length - 1];
        return d.host + latest.path + '/256/{z}/{x}/{y}/2/1_1.png';
      });
  }

  function initRadar(map, onUnavailable) {
    destroyRadar();
    _radarMap = map;

    function refresh() {
      _fetchRadarFrame().then(function (url) {
        if (!url) { if (onUnavailable) onUnavailable(); return; }
        if (_radarLayer) {
          _radarLayer.setUrl(url);
        } else {
          _radarLayer = L.tileLayer(url, { opacity: 0.5, attribution: '© RainViewer' }).addTo(map);
        }
      }).catch(function () { if (onUnavailable) onUnavailable(); });
    }

    refresh();
    _radarTimer = setInterval(refresh, 10 * 60 * 1000); // refresh every 10 min
  }

  function destroyRadar() {
    if (_radarTimer)  { clearInterval(_radarTimer); _radarTimer = null; }
    if (_radarLayer)  { _radarLayer.remove(); _radarLayer = null; }
    _radarMap = null;
  }

  // ─── Wind ─────────────────────────────────────────────────────────────────

  var _windGroup    = null;
  var _windTimer    = null;
  var _windMap      = null;
  var _windDebounce = null;

  function _windArrowIcon(speed, direction) {
    var color = speed < 20 ? '#22c55e' : speed < 50 ? '#eab308' : '#ef4444';
    return L.divIcon({
      html: '<div style="transform:rotate(' + direction + 'deg);font-size:18px;line-height:1;color:' + color + '" title="' + speed.toFixed(1) + ' km/h">↑</div>' +
            '<div style="font-size:9px;color:#888;text-align:center;white-space:nowrap">' + speed.toFixed(0) + ' km/h</div>',
      className: '',
      iconSize: [40, 30],
      iconAnchor: [20, 10],
    });
  }

  function _fetchWind(map) {
    if (!_windGroup || !map) return;
    var bounds = map.getBounds();
    var latMin = bounds.getSouth(), latMax = bounds.getNorth();
    var lonMin = bounds.getWest(), lonMax = bounds.getEast();
    var N = 5;
    var lats = [], lons = [];
    for (var i = 0; i < N; i++) {
      for (var j = 0; j < N; j++) {
        lats.push((latMin + (latMax - latMin) * i / (N - 1)).toFixed(4));
        lons.push((lonMin + (lonMax - lonMin) * j / (N - 1)).toFixed(4));
      }
    }
    var url = 'https://api.open-meteo.com/v1/forecast' +
      '?latitude=' + lats.join(',') +
      '&longitude=' + lons.join(',') +
      '&current=windspeed_10m,winddirection_10m' +
      '&wind_speed_unit=kmh';
    fetch(url)
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        if (!_windGroup || !data) return;
        _windGroup.clearLayers();
        var results = Array.isArray(data) ? data : [data];
        results.forEach(function (d, idx) {
          if (!d || !d.current) return;
          var lat = parseFloat(lats[idx]);
          var lon = parseFloat(lons[idx]);
          if (isNaN(lat) || isNaN(lon)) return;
          L.marker([lat, lon], {
            icon: _windArrowIcon(d.current.windspeed_10m, d.current.winddirection_10m),
          }).addTo(_windGroup);
        });
      })
      .catch(function () {});
  }

  function initWind(map) {
    destroyWind();
    _windMap = map;
    _windGroup = L.layerGroup().addTo(map);
    _fetchWind(map);
    _windTimer = setInterval(function () { _fetchWind(_windMap); }, 15 * 60 * 1000);
    map.on('moveend zoomend', function () {
      clearTimeout(_windDebounce);
      _windDebounce = setTimeout(function () { _fetchWind(_windMap); }, 1500);
    });
  }

  function destroyWind() {
    if (_windTimer)   { clearInterval(_windTimer); _windTimer = null; }
    clearTimeout(_windDebounce);
    if (_windGroup)   { _windGroup.clearLayers(); _windGroup.remove(); _windGroup = null; }
    if (_windMap) {
      _windMap.off('moveend zoomend');
      _windMap = null;
    }
  }

  // ─── MeshMapper ───────────────────────────────────────────────────────────

  var _mmLayer = null;

  var _MM_PRIORITY = { BIDIR: 6, DISC: 5, TRACE: 5, TX: 4, RX: 3, DEAD: 2, DROP: 1 };
  var _MM_COLORS   = {
    BIDIR: '#22c55e', DISC: '#84cc16', TRACE: '#84cc16',
    TX: '#eab308', RX: '#f97316', DEAD: '#ef4444', DROP: '#6b7280',
  };

  function initMeshMapper(map, onNotConfigured) {
    destroyMeshMapper();
    fetch('/api/coverage/meshmapper')
      .then(function (r) {
        if (r.status === 503) {
          if (onNotConfigured) onNotConfigured();
          return null;
        }
        return r.ok ? r.json() : null;
      })
      .then(function (data) {
        if (!data) return;
        var squares = Array.isArray(data) ? data : (data.squares || data.coverage || []);

        // Conflict resolution: highest-priority type wins per grid cell.
        var grid = {};
        squares.forEach(function (sq) {
          var id = sq.grid_id || (sq.bounds.south + ',' + sq.bounds.west);
          var pri = _MM_PRIORITY[sq.coverage_type] || 0;
          if (!grid[id] || pri > (_MM_PRIORITY[grid[id].coverage_type] || 0)) {
            grid[id] = sq;
          }
        });

        _mmLayer = L.layerGroup().addTo(map);
        Object.keys(grid).forEach(function (id) {
          var sq = grid[id];
          var color = sq.fill_color || _MM_COLORS[sq.coverage_type] || '#6b7280';
          var b = sq.bounds;
          var rect = L.rectangle(
            [[b.south, b.west], [b.north, b.east]],
            { color: color, weight: 1, opacity: 0.6, fillColor: color, fillOpacity: 0.35 }
          );
          var ts = sq.timestamp ? new Date(sq.timestamp * 1000).toLocaleString() : '—';
          rect.bindPopup(
            '<strong>' + (sq.coverage_type || '?') + '</strong><br>' +
            (sq.snr != null ? 'SNR: ' + sq.snr + ' dB<br>' : '') +
            'Seen: ' + ts + '<br>' +
            'Grid: ' + (sq.grid_id || '—')
          );
          _mmLayer.addLayer(rect);
        });
      })
      .catch(function () {});
  }

  function destroyMeshMapper() {
    if (_mmLayer) { _mmLayer.clearLayers(); _mmLayer.remove(); _mmLayer = null; }
  }

  // ─── Exports ──────────────────────────────────────────────────────────────

  window.MapOverlays = {
    initRouteHistory:    initRouteHistory,
    destroyRouteHistory: destroyRouteHistory,
    initRadar:           initRadar,
    destroyRadar:        destroyRadar,
    initWind:            initWind,
    destroyWind:         destroyWind,
    initMeshMapper:      initMeshMapper,
    destroyMeshMapper:   destroyMeshMapper,
  };

})();
