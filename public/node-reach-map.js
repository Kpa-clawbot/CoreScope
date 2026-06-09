/* window.NodeReachMap.render(containerId, node, tiers) — focused Leaflet map of
   a node and its links, coloured by bottleneck tier. Returns a controller:
     { map, setLinks(links), bounds, destroy() }
   The map + tiles + node pin + legend are built once; setLinks() redraws ONLY
   the link layer in place (no teardown/flicker) when the table filter changes.
   `tiers` is [{min, label, varName}] ordered strong→weak (from node-reach.js,
   the single source of the thresholds). */
(function () {
  'use strict';

  function cssVar(name) {
    var v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || '#888';
  }

  function tierFor(tiers, bottleneck) {
    for (var i = 0; i < tiers.length; i++) {
      if (bottleneck >= tiers[i].min) return tiers[i];
    }
    return tiers[tiers.length - 1];
  }

  function legendControl(tiers, colors) {
    var ctl = L.control({ position: 'bottomright' });
    ctl.onAdd = function () {
      var div = L.DomUtil.create('div', 'nq-legend');
      var rows = tiers.map(function (t) {
        return '<div><span class="nq-sw" style="background:' + colors[t.varName] + '"></span>' + escapeHtml(t.legend) + '</div>';
      }).join('');
      div.innerHTML = '<div><strong>Bottleneck</strong> (weaker direction)</div>' + rows;
      return div;
    };
    return ctl;
  }

  function render(containerId, node, tiers) {
    var c = document.getElementById(containerId);
    if (!c || typeof L === 'undefined') return null;

    // Resolve the tier colours + marker outline ONCE (not per polyline/marker).
    var colors = {};
    tiers.forEach(function (t) { colors[t.varName] = cssVar(t.varName); });
    var outline = cssVar('--surface-0'); // themed marker stroke (was hardcoded #fff)
    var accent = cssVar('--accent');

    var map = L.map(containerId, { zoomControl: true, attributionControl: false })
      .setView([node.lat, node.lon], 11);
    if (typeof window._applyTilesToNodeMap === 'function') {
      window._applyTilesToNodeMap(map);
    } else {
      // Loud, not silent: the tile-preference helper is missing.
      console.warn('NodeReachMap: _applyTilesToNodeMap unavailable — using OSM fallback');
      L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    }

    // Center node: a circleMarker like the neighbours (one glyph family) — just
    // larger + accent-filled — rather than the heavy default droplet icon.
    L.circleMarker([node.lat, node.lon], { radius: 8, color: outline, weight: 2, fillColor: accent, fillOpacity: 1 })
      .addTo(map).bindPopup(escapeHtml(node.name));
    legendControl(tiers, colors).addTo(map);

    var linkLayer = L.layerGroup().addTo(map);
    var bounds = [[node.lat, node.lon]];

    function setLinks(links) {
      linkLayer.clearLayers();
      bounds = [[node.lat, node.lon]];
      links.forEach(function (l) {
        if (l.lat == null || l.lon == null) return;
        bounds.push([l.lat, l.lon]);
        var col = colors[tierFor(tiers, l.bottleneck).varName];
        // Constant weight — colour alone encodes bottleneck (no double-encoding).
        L.polyline([[node.lat, node.lon], [l.lat, l.lon]], { color: col, weight: 2.5, opacity: 0.85 })
          .addTo(linkLayer)
          .bindPopup(escapeHtml(l.name) + '<br>we ' + l.we_hear + ' / they ' + l.they_hear);
        L.circleMarker([l.lat, l.lon], { radius: 5, color: outline, weight: 1, fillColor: col, fillOpacity: 1 })
          .addTo(linkLayer).bindTooltip(escapeHtml(l.name));
      });
      try { map.fitBounds(bounds, { padding: [30, 30] }); } catch (e) {}
      map._nqBounds = bounds;
    }

    setTimeout(function () { map.invalidateSize(); }, 120);

    return {
      map: map,
      setLinks: setLinks,
      get bounds() { return bounds; },
      destroy: function () { try { map.remove(); } catch (e) {} }
    };
  }

  window.NodeReachMap = { render: render };
})();
