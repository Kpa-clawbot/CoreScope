/* window.NodeQualityMap.render(containerId, node, links, colorFn) — focused
   Leaflet map of a node and its bidirectional links, coloured by bottleneck.
   Returns the Leaflet map instance (with _nqBounds) so the caller can resize
   for printing. */
(function () {
  'use strict';

  function cssColor(varExpr) {
    var name = varExpr.replace('var(', '').replace(')', '').trim();
    var v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || '#888';
  }

  function render(containerId, node, links, colorFn) {
    var c = document.getElementById(containerId);
    if (!c || typeof L === 'undefined') return null;
    var map = L.map(containerId, { zoomControl: true, attributionControl: false })
      .setView([node.lat, node.lon], 11);
    if (typeof window._applyTilesToNodeMap === 'function') {
      window._applyTilesToNodeMap(map);
    } else {
      L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    }
    var pts = [[node.lat, node.lon]];
    links.forEach(function (l) {
      if (l.lat == null || l.lon == null) return;
      pts.push([l.lat, l.lon]);
      var col = cssColor(colorFn(l.bottleneck));
      L.polyline([[node.lat, node.lon], [l.lat, l.lon]], {
        color: col,
        weight: Math.max(1.5, Math.min(7, 1.2 + 1.6 * Math.log10(l.bottleneck + 1))),
        opacity: 0.85
      }).addTo(map).bindPopup(escapeHtml(l.name) + '<br>we ' + l.we_hear + ' / they ' + l.they_hear);
      // Neighbour dot: filled with the link colour (was white/invisible before).
      L.circleMarker([l.lat, l.lon], { radius: 5, color: '#ffffff', weight: 1, fillColor: col, fillOpacity: 1 })
        .addTo(map).bindTooltip(escapeHtml(l.name));
    });
    // The node itself: a default Leaflet pin marker — always clearly visible.
    L.marker([node.lat, node.lon]).addTo(map).bindPopup(escapeHtml(node.name));
    try { map.fitBounds(pts, { padding: [30, 30] }); } catch (e) {}
    map._nqBounds = pts;
    setTimeout(function () { map.invalidateSize(); }, 120);
    return map;
  }

  window.NodeQualityMap = { render: render };
})();
