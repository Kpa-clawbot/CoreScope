/* window.NodeQualityMap.render(containerId, node, links, colorFn) — focused
   Leaflet map of a node and its bidirectional links, coloured by bottleneck. */
(function () {
  'use strict';
  var map = null;

  function cssColor(varExpr) {
    // varExpr looks like "var(--link-strong)" → resolve to a concrete colour.
    var name = varExpr.replace('var(', '').replace(')', '').trim();
    var v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || '#888';
  }

  function render(containerId, node, links, colorFn) {
    var c = document.getElementById(containerId);
    if (!c || typeof L === 'undefined') return;
    if (map) { map.remove(); map = null; }
    map = L.map(containerId, { zoomControl: true, attributionControl: false })
      .setView([node.lat, node.lon], 11);
    // Reuse the node-detail tile applier if present; else plain OSM.
    if (typeof window._applyTilesToNodeMap === 'function') {
      window._applyTilesToNodeMap(map);
    } else {
      L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    }
    var pts = [[node.lat, node.lon]];
    links.forEach(function (l) {
      if (l.lat == null || l.lon == null) return;
      pts.push([l.lat, l.lon]);
      L.polyline([[node.lat, node.lon], [l.lat, l.lon]], {
        color: cssColor(colorFn(l.bottleneck)),
        weight: Math.max(1.5, Math.min(7, 1.2 + 1.6 * Math.log10(l.bottleneck + 1))),
        opacity: 0.8
      }).addTo(map).bindPopup(escapeHtml(l.name) + '<br>wij ' + l.we_hear + ' / zij ' + l.they_hear);
      L.circleMarker([l.lat, l.lon], { radius: 5, color: '#fff', weight: 1, fillOpacity: 1 })
        .addTo(map).bindTooltip(escapeHtml(l.name));
    });
    L.circleMarker([node.lat, node.lon], { radius: 8, color: '#fff', weight: 2, fillColor: '#0969da', fillOpacity: 1 })
      .addTo(map).bindPopup(escapeHtml(node.name));
    try { map.fitBounds(pts, { padding: [30, 30] }); } catch (e) {}
    setTimeout(function () { map.invalidateSize(); }, 120);
  }

  window.NodeQualityMap = { render: render };
})();
