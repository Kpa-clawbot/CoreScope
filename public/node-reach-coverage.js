/* === CoreScope — node-reach-coverage.js ===
   Draws per-node mobile RX coverage as an H3-style hex layer on the existing
   Reach Leaflet map, from the /api/nodes/{pubkey}/rx-coverage GeoJSON.
   No external deps; colours via CSS variables. */
'use strict';
(function () {
  // coverageColorVar maps a GeoJSON feature's properties to a CSS variable name.
  // Grey = received but no signal metric; otherwise SF8 SNR thresholds: ≥ −5 green
  // (good margin), −9..−5 orange (near the limit), < −9 red (packet loss likely).
  function coverageColorVar(props) {
    if (!props || !props.has_sig || props.best_snr == null) return '--nq-cov-grey';
    var s = Number(props.best_snr);
    if (s >= -5) return '--nq-cov-strong';
    if (s >= -9) return '--nq-cov-mid';
    return '--nq-cov-weak';
  }

  function cssColor(varName) {
    try {
      var v = getComputedStyle(document.documentElement).getPropertyValue(varName).trim();
      return v || '#888';
    } catch (e) { return '#888'; }
  }

  // addLayer fetches coverage for the current map bbox/zoom and draws hex
  // polygons. Returns a handle with off() so the caller can remove it.
  function addLayer(map, pubkey) {
    var group = L.layerGroup().addTo(map);
    function refresh() {
      var b = map.getBounds();
      var bbox = [b.getSouth(), b.getWest(), b.getNorth(), b.getEast()].join(',');
      var url = '/api/nodes/' + encodeURIComponent(pubkey) + '/rx-coverage?bbox=' + bbox + '&z=' + map.getZoom();
      fetch(url).then(function (r) { return r.json(); }).then(function (fc) {
        group.clearLayers();
        (fc.features || []).forEach(function (f) {
          var ring = (f.geometry.coordinates[0] || []).map(function (c) { return [c[1], c[0]]; }); // [lon,lat]→[lat,lon]
          var col = cssColor(coverageColorVar(f.properties));
          L.polygon(ring, { color: col, weight: 1, fillColor: col, fillOpacity: 0.45 })
            .addTo(group)
            .bindTooltip('n=' + f.properties.count +
              (f.properties.best_snr != null ? ' · SNR ' + f.properties.best_snr : ' · no signal'));
        });
      }).catch(function (e) {
        // Leave the layer empty on error; never crash the reach page.
        console.warn('node-reach-coverage: coverage fetch failed', e);
      });
    }
    // Debounce pan/zoom redraws so dragging doesn't storm the coverage endpoint
    // (#6). Keep the reference so off() can unbind the same handler.
    var debouncedRefresh = (typeof debounce === 'function') ? debounce(refresh, 200) : refresh;
    map.on('moveend zoomend', debouncedRefresh);
    refresh();
    return {
      off: function () {
        map.off('moveend zoomend', debouncedRefresh);
        try { map.removeLayer(group); } catch (e) {}
      }
    };
  }

  window.NodeReachCoverage = { coverageColorVar: coverageColorVar, addLayer: addLayer };
})();
