/* window.PacketPathMap.open(hash) — on-demand modal showing every branch
   of a packet's flood spread (see GET /api/packets/{hash}/path,
   cmd/server/db.go GetPacketPath) as a Leaflet map: one branch per
   distinct station that observed the packet, each drawn as its own
   ordered relay chain (or, when no hop resolved, just that station's own
   position) ending at that station. The deepest branch is drawn on top
   in the accent color; every other branch is drawn muted underneath it,
   so the map reads as "how far AND how wide did this packet spread"
   rather than a single route. Reuses node-reach-map.js's Leaflet setup
   conventions (tile helper, circleMarker points, theme-aware colors).

   Entry point today: the ping-bot reply's "View path" link
   (public/channels.js botReplyHtml) -- kept general (keyed by packet
   hash, not ping-specific) since any packet with observations could use
   the same view later. */
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

  // Turns one branch into a plottable chain: resolved hops with a known
  // position, then the observer's own position when known. A branch with
  // no locatable hops still contributes a single-point chain -- just the
  // observer -- so a station we can't trace a route through is still
  // visible on the map rather than silently dropped.
  function chainForBranch(b) {
    var located = (b.points || []).filter(function (p) { return p.lat != null && p.lon != null; });
    var chain = located.map(function (p, hi) {
      return { lat: p.lat, lon: p.lon, name: p.name, label: 'hop ' + (hi + 1) + ' of ' + b.hops };
    });
    if (b.observer && b.observer.lat != null && b.observer.lon != null) {
      chain.push({
        lat: b.observer.lat, lon: b.observer.lon, name: b.observer.name,
        label: b.hops + ' hop' + (b.hops === 1 ? '' : 's'), isObserver: true,
      });
    }
    return { chain: chain, missing: (b.points || []).length - located.length };
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
        '<p class="text-muted" style="margin:0 0 10px;font-size:12px">How far and how wide this packet spread. The highlighted route is the farthest-traveled branch; every other station that heard it is shown too.</p>' +
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

    var branches = data.branches || [];
    var plotted = branches.map(function (b, i) {
      var built = chainForBranch(b);
      return { branch: b, chain: built.chain, missing: built.missing, primary: i === 0 };
    }).filter(function (p) { return p.chain.length > 0; });

    if (plotted.length === 0) {
      if (statusEl) {
        if (branches.length === 0) {
          statusEl.textContent = 'This packet has no observations yet.';
        } else {
          var deepestUnplottable = branches[0].hops;
          statusEl.textContent = 'None of the ' + branches.length + ' station' + (branches.length === 1 ? '' : 's') +
            ' that heard this packet have a known position yet (farthest reached ' +
            deepestUnplottable + ' hop' + (deepestUnplottable === 1 ? '' : 's') + ').';
        }
      }
      return;
    }

    if (typeof L === 'undefined') {
      if (statusEl) statusEl.textContent = 'Map library unavailable.';
      return;
    }

    var primaryChain = plotted[0].chain;
    var center = primaryChain[Math.floor(primaryChain.length / 2)];
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
    var muted = cssVar('--text-muted');

    var bounds = [];
    var missingTotal = 0;
    // Draw secondary branches first so the primary (deepest) one ends up on top.
    var ordered = plotted.slice().sort(function (a, b) { return (a.primary ? 1 : 0) - (b.primary ? 1 : 0); });
    ordered.forEach(function (p) {
      missingTotal += p.missing;
      var lineColor = p.primary ? accent : muted;
      var line = [];
      p.chain.forEach(function (pt) {
        bounds.push([pt.lat, pt.lon]);
        line.push([pt.lat, pt.lon]);
        var color = pt.isObserver ? observerColor : lineColor;
        var radius = p.primary ? (pt.isObserver ? 7 : 6) : (pt.isObserver ? 5 : 4);
        L.circleMarker([pt.lat, pt.lon], {
          radius: radius, color: outline, weight: p.primary ? 2 : 1,
          fillColor: color, fillOpacity: p.primary ? 1 : 0.8,
        })
          .addTo(map)
          .bindTooltip(escapeHtml(pt.name) + ' (' + pt.label + ')');
      });
      if (line.length > 1) {
        L.polyline(line, { color: lineColor, weight: p.primary ? 2.5 : 1.5, opacity: p.primary ? 0.85 : 0.5 }).addTo(map);
      }
    });
    try { map.fitBounds(bounds, { padding: [30, 30] }); } catch (e) { /* single point */ }
    setTimeout(function () { map.invalidateSize(); }, 120);
    activeMap = map;

    var deepestHops = branches[0].hops;
    var statusParts = [
      plotted.length + ' of ' + branches.length + ' station' + (branches.length === 1 ? '' : 's') + ' shown',
      'deepest reached ' + deepestHops + ' hop' + (deepestHops === 1 ? '' : 's'),
    ];
    if (missingTotal > 0) statusParts.push(missingTotal + ' hop' + (missingTotal === 1 ? '' : 's') + ' without a known position (not shown)');
    if (statusEl) statusEl.textContent = statusParts.join(' · ');
  }

  window.PacketPathMap = { open: open, close: close };
})();
