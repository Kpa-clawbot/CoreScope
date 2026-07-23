/* window.PacketPathMap.open(hash) — on-demand modal showing a packet's
   resolved relay path (see GET /api/packets/{hash}/path, cmd/server/db.go
   GetPacketPath) as a sequential Leaflet map: each hop plotted in path
   order and connected by a line, ending at the observer that produced
   the deepest observation. Reuses node-reach-map.js's Leaflet setup
   conventions (tile helper, circleMarker points, theme-aware colors) but
   draws an ORDERED CHAIN instead of a star, since a relay path is a
   sequence, not a hub-and-spoke.

   Entry point today: the ping-bot reply's "View path" link
   (public/channels.js botReplyHtml) -- kept general (keyed by packet
   hash, not ping-specific) since any packet with a resolved path could
   use the same view later. */
(function () {
  'use strict';

  function cssVar(name) {
    var v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || '#888';
  }

  var activeMap = null;

  function onKeydown(e) {
    if (e.key === 'Escape') close();
  }

  function close() {
    var overlay = document.getElementById('packetPathModal');
    if (overlay) overlay.remove();
    if (activeMap) {
      try { activeMap.remove(); } catch (e) { /* already gone */ }
      activeMap = null;
    }
    document.removeEventListener('keydown', onKeydown);
  }

  async function open(hash) {
    close(); // in case one's already open

    var overlay = document.createElement('div');
    overlay.id = 'packetPathModal';
    overlay.className = 'modal-overlay';
    overlay.innerHTML =
      '<div class="modal" style="max-width:min(92vw,700px);padding:16px">' +
        '<button type="button" id="packetPathClose" aria-label="Close" ' +
          'style="position:absolute;top:8px;right:8px;background:none;border:none;cursor:pointer;font-size:22px;line-height:1;color:var(--text-muted)">&times;</button>' +
        '<h3 style="margin:0 0 4px;padding-right:24px">Relay Path</h3>' +
        '<p class="text-muted" style="margin:0 0 10px;font-size:12px">How far this packet traveled before reaching the farthest-along observer. Hops without a known GPS position are omitted from the line.</p>' +
        '<div id="packetPathMapContainer" style="height:360px;border-radius:8px;overflow:hidden;background:var(--surface-1)"></div>' +
        '<div id="packetPathStatus" style="margin-top:8px;font-size:12px;color:var(--text-muted)">Loading…</div>' +
      '</div>';
    document.body.appendChild(overlay);
    overlay.addEventListener('click', function (e) { if (e.target === overlay) close(); });
    var closeBtn = document.getElementById('packetPathClose');
    if (closeBtn) closeBtn.addEventListener('click', close);
    document.addEventListener('keydown', onKeydown);

    var statusEl = document.getElementById('packetPathStatus');

    var data;
    try {
      data = await api('/packets/' + encodeURIComponent(hash) + '/path');
    } catch (e) {
      if (statusEl) statusEl.textContent = 'Failed to load path: ' + e.message;
      return;
    }

    var allHops = data.points || [];
    var located = allHops.filter(function (p) { return p.lat != null && p.lon != null; });
    var missing = allHops.length - located.length;
    var hasObserver = !!(data.observer && data.observer.lat != null && data.observer.lon != null);

    if (located.length === 0 && !hasObserver) {
      if (statusEl) {
        statusEl.textContent = data.hops > 0
          ? 'None of the ' + data.hops + ' hop' + (data.hops === 1 ? '' : 's') + ' in this path have a known position yet.'
          : 'This packet has no resolved relay path yet.';
      }
      return;
    }

    if (typeof L === 'undefined') {
      if (statusEl) statusEl.textContent = 'Map library unavailable.';
      return;
    }

    var chain = located.map(function (p, i) {
      return { lat: p.lat, lon: p.lon, name: p.name, label: 'hop ' + (i + 1) + ' of ' + data.hops };
    });
    if (hasObserver) {
      chain.push({ lat: data.observer.lat, lon: data.observer.lon, name: data.observer.name, label: 'observer', isObserver: true });
    }

    var center = chain[Math.floor(chain.length / 2)];
    var map = L.map('packetPathMapContainer', { zoomControl: true, attributionControl: false })
      .setView([center.lat, center.lon], 10);
    if (typeof window._applyTilesToNodeMap === 'function') {
      window._applyTilesToNodeMap(map);
    } else {
      L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    }

    var outline = cssVar('--surface-0');
    var accent = cssVar('--accent');
    var observerColor = cssVar('--status-yellow');

    var bounds = [];
    var line = [];
    chain.forEach(function (p) {
      bounds.push([p.lat, p.lon]);
      line.push([p.lat, p.lon]);
      var color = p.isObserver ? observerColor : accent;
      L.circleMarker([p.lat, p.lon], { radius: p.isObserver ? 7 : 6, color: outline, weight: 2, fillColor: color, fillOpacity: 1 })
        .addTo(map)
        .bindTooltip(escapeHtml(p.name) + ' (' + p.label + ')');
    });
    if (line.length > 1) {
      L.polyline(line, { color: accent, weight: 2.5, opacity: 0.85 }).addTo(map);
    }
    try { map.fitBounds(bounds, { padding: [30, 30] }); } catch (e) { /* single point */ }
    setTimeout(function () { map.invalidateSize(); }, 120);
    activeMap = map;

    var statusParts = [data.hops + ' hop' + (data.hops === 1 ? '' : 's') + ' total'];
    if (missing > 0) statusParts.push(missing + ' without a known position (not shown)');
    if (statusEl) statusEl.textContent = statusParts.join(' · ');
  }

  window.PacketPathMap = { open: open, close: close };
})();
