/* window.AreaNodesMap.open(label, points) — small modal map showing a
   set of node pins. First use: Tools > Position-Fix Coverage Gaps,
   clicking an area row to see where its estimated nodes actually are.
   Reuses packet-path-map.js's modal + Leaflet conventions (tile
   helper, circleMarker points, Escape/click-outside close), stripped
   down to plain pins -- no path lines, no branch highlighting, since
   this isn't about a route, just "where are these nodes".

   `points` is an array of {publicKey, name, lat, lon, contributorCount}
   -- the same shape as EstimatedAreaNode (cmd/server/types.go) already
   used by GET /api/analytics/areas' estimatedNodes list, so callers can
   pass a filtered slice of that straight through with no reshaping. */
(function () {
  'use strict';

  var activeMap = null;

  function escapeHtml(s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function cssVar(name) {
    var v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || '#888';
  }

  function onKeydown(e) {
    if (e.key === 'Escape') close();
  }

  function close() {
    var overlay = document.getElementById('areaNodesMapModal');
    if (overlay) overlay.remove();
    if (activeMap) {
      try { activeMap.remove(); } catch (e) { /* already gone */ }
      activeMap = null;
    }
    document.removeEventListener('keydown', onKeydown);
  }

  function open(label, points) {
    close(); // in case one's already open

    var pts = (points || []).filter(function (p) { return p && typeof p.lat === 'number' && typeof p.lon === 'number'; });

    var overlay = document.createElement('div');
    overlay.id = 'areaNodesMapModal';
    overlay.className = 'modal-overlay';
    overlay.innerHTML =
      '<div class="modal" style="max-width:min(92vw,700px);padding:16px">' +
        '<button type="button" id="areaNodesMapClose" aria-label="Close" ' +
          'style="position:absolute;top:8px;right:8px;background:none;border:none;cursor:pointer;font-size:22px;line-height:1;color:var(--text-muted)">&times;</button>' +
        '<h3 style="margin:0 0 4px;padding-right:24px">' + escapeHtml(label) + '</h3>' +
        '<p class="text-muted" style="margin:0 0 8px;font-size:12px">' +
          pts.length + ' estimated node' + (pts.length === 1 ? '' : 's') + ' in this area (dashed = approximate position). Click a marker to open that node\'s detail page.' +
        '</p>' +
        '<div id="areaNodesMapContainer" style="height:360px;border-radius:8px;overflow:hidden;background:var(--surface-1)"></div>' +
      '</div>';
    document.body.appendChild(overlay);
    overlay.addEventListener('click', function (e) { if (e.target === overlay) close(); });
    var closeBtn = document.getElementById('areaNodesMapClose');
    if (closeBtn) closeBtn.addEventListener('click', close);
    document.addEventListener('keydown', onKeydown);

    if (typeof L === 'undefined') return;

    var map = L.map('areaNodesMapContainer', { zoomControl: true, attributionControl: false });
    activeMap = map;
    if (typeof window._applyTilesToNodeMap === 'function') {
      window._applyTilesToNodeMap(map);
    } else {
      L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    }

    var accent = cssVar('--accent');
    var outline = cssVar('--surface-0');
    var bounds = [];
    pts.forEach(function (p) {
      bounds.push([p.lat, p.lon]);
      var contribNote = p.contributorCount
        ? ' (' + p.contributorCount + ' contributor' + (p.contributorCount === 1 ? '' : 's') + ')'
        : '';
      var marker = L.circleMarker([p.lat, p.lon], {
        radius: 7, color: outline, weight: 2, fillColor: accent, fillOpacity: 0.5, dashArray: '5,4',
      }).addTo(map).bindTooltip(escapeHtml(p.name || p.publicKey) + contribNote);
      if (p.publicKey) {
        marker.on('click', function () {
          close();
          window.location.hash = '#/nodes/' + encodeURIComponent(p.publicKey);
        });
      }
    });

    if (bounds.length > 0) {
      try { map.fitBounds(bounds, { padding: [30, 30] }); } catch (e) { /* single point */ }
    }
    setTimeout(function () { map.invalidateSize(); }, 120);
  }

  window.AreaNodesMap = { open: open, close: close };
})();
