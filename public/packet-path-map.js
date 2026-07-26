/* window.PacketPathMap.open(hash) — on-demand modal showing every branch
   of a packet's flood spread (see GET /api/packets/{hash}/path,
   cmd/server/db.go GetPacketPath) as a Leaflet map: one branch per
   distinct station that observed the packet, each drawn as its own
   ordered relay chain (or, when no hop resolved, just that station's own
   position) ending at that station. The deepest branch is drawn on top
   in the accent color; every other branch is drawn muted underneath it,
   so the map reads as "how far AND how wide did this packet spread"
   rather than a single route. The response's `first` field (the single
   earliest-arriving observation, usually 0 hops) is additionally drawn
   as a distinct landmark ring on top of everything else -- an
   approximate "where the message entered the mesh" anchor, since a
   deepest-first branch list has no natural starting point of its own.
   Reuses node-reach-map.js's Leaflet setup conventions (tile helper,
   circleMarker points, theme-aware colors).

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

  // Formats PacketPathBranch.secondsAfterFirst for a tooltip: how long
  // after the earliest-arriving observation (the green landmark ring)
  // this station's own observation arrived.
  function formatElapsed(seconds) {
    if (seconds === 0) return 'first to arrive';
    if (seconds < 60) return '+' + seconds.toFixed(1) + 's';
    var m = Math.floor(seconds / 60);
    var s = Math.round(seconds % 60);
    return '+' + m + 'm ' + s + 's';
  }

  // How much bigger/fuzzier an approximate marker's ring should be than
  // a normal marker, given how many positioned neighbors fed the
  // estimate (more = tighter) and how much they disagreed (a wide
  // spread lowers confidence even with several contributors).
  function approxRadiusBonus(count, spreadKm) {
    var bonus;
    if (!count || count <= 1) bonus = 6;
    else if (count <= 3) bonus = 4;
    else bonus = 2;
    if (spreadKm != null && spreadKm > 100) bonus += 2;
    return bonus;
  }

  function approxFillOpacity(count) {
    if (!count || count <= 1) return 0.12;
    if (count <= 3) return 0.2;
    return 0.3;
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

  // A short prefix marking a node's role in tooltips -- purely a label,
  // markers stay circleMarker dots throughout (a role-specific shape
  // would clash with the color/dash coding already carrying primary,
  // approx, and observer meaning).
  function roleIcon(role) {
    switch (role) {
      case 'repeater': return '📡 ';
      case 'room': return '🏠 ';
      case 'client': return '📱 ';
      case 'sensor': return '🌡️ ';
      default: return '';
    }
  }

  // Turns one branch into a plottable chain: resolved hops with a known
  // position, then the observer's own position when known. A branch with
  // no locatable hops still contributes a single-point chain -- just the
  // observer -- so a station we can't trace a route through is still
  // visible on the map rather than silently dropped. A point/observer
  // can be `approx` (server used a weighted centroid of its positioned
  // neighbors' positions, see GetPacketPath) -- carried through so it
  // renders as a hollow, dashed marker instead of a solid one, never
  // mistaken for a real fix.
  function chainForBranch(b) {
    var located = (b.points || []).filter(function (p) { return p.lat != null && p.lon != null; });
    var chain = located.map(function (p, hi) {
      return {
        lat: p.lat, lon: p.lon, name: p.name, label: 'hop ' + (hi + 1) + ' of ' + b.hops, approx: !!p.approx,
        approxNeighborCount: p.approxNeighborCount, approxSpreadKm: p.approxSpreadKm, role: p.role,
        publicKey: p.publicKey,
      };
    });
    if (b.observer && b.observer.lat != null && b.observer.lon != null) {
      var observerLabel = b.hops + ' hop' + (b.hops === 1 ? '' : 's');
      if (typeof b.secondsAfterFirst === 'number') observerLabel += ', ' + formatElapsed(b.secondsAfterFirst);
      if (typeof b.distanceFromFirstKm === 'number' && b.distanceFromFirstKm > 0) observerLabel += ', ' + b.distanceFromFirstKm.toFixed(1) + ' km away';
      chain.push({
        lat: b.observer.lat, lon: b.observer.lon, name: b.observer.name,
        label: observerLabel, isObserver: true, approx: !!b.observer.approx,
        approxNeighborCount: b.observer.approxNeighborCount, approxSpreadKm: b.observer.approxSpreadKm,
        role: b.observer.role, publicKey: b.observer.publicKey,
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
        '<p class="text-muted" style="margin:0 0 10px;font-size:12px">How far and how wide this packet spread. The highlighted route is the farthest-traveled branch; every other station that heard it is shown too. The green ring marks whoever heard it first. Dashed markers are approximate -- estimated from nearby positioned neighbors, not the station\'s own position. Click a marker to open that node\'s detail page.</p>' +
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
    var approxTotal = 0;
    // The same physical node (e.g. a shared entry-point repeater near the
    // sender) commonly appears in many branches' chains -- dedupe by
    // identity so "N approximate" counts distinct stations, not chain
    // appearances (#1... a packet heard by 12 stations through one shared
    // repeater was showing "11 approximate" for what was really 1 node).
    var approxSeen = {};
    // Draw secondary branches first so the primary (deepest) one ends up on top.
    var ordered = plotted.slice().sort(function (a, b) { return (a.primary ? 1 : 0) - (b.primary ? 1 : 0); });
    ordered.forEach(function (p) {
      missingTotal += p.missing;
      var lineColor = p.primary ? accent : muted;
      var line = [];
      p.chain.forEach(function (pt) {
        if (pt.approx) {
          var approxKey = pt.publicKey || pt.name;
          if (!approxSeen[approxKey]) {
            approxSeen[approxKey] = true;
            approxTotal++;
          }
        }
        bounds.push([pt.lat, pt.lon]);
        line.push([pt.lat, pt.lon]);
        var color = pt.isObserver ? observerColor : lineColor;
        var radius = p.primary ? (pt.isObserver ? 7 : 6) : (pt.isObserver ? 5 : 4);
        var markerOpts = pt.approx
          // Approximate (borrowed-from-neighbor) position: larger,
          // thick-dashed ring with a faint fill -- a plain hollow outline
          // at normal marker size was too easy to miss against map
          // tiles, so this deliberately reads as a bigger, softer blob
          // rather than a precise dot. Size/fill scale with confidence:
          // more agreeing neighbors = tighter, more solid; one neighbor
          // or a wide spread among several = bigger, fainter.
          ? {
              radius: radius + approxRadiusBonus(pt.approxNeighborCount, pt.approxSpreadKm), color: color, weight: 3,
              fillColor: color, fillOpacity: approxFillOpacity(pt.approxNeighborCount), dashArray: '5,4',
            }
          : { radius: radius, color: outline, weight: p.primary ? 2 : 1, fillColor: color, fillOpacity: p.primary ? 1 : 0.8 };
        var approxNote = '';
        if (pt.approx) {
          approxNote = ', approx. position';
          if (pt.approxNeighborCount) approxNote += ' from ' + pt.approxNeighborCount + ' neighbor' + (pt.approxNeighborCount === 1 ? '' : 's');
        }
        var clickNote = pt.publicKey ? ' — click for node detail' : '';
        var marker = L.circleMarker([pt.lat, pt.lon], markerOpts)
          .addTo(map)
          .bindTooltip(roleIcon(pt.role) + escapeHtml(pt.name) + ' (' + pt.label + approxNote + ')' + clickNote, { className: 'packet-path-tooltip' });
        if (pt.publicKey) {
          // Same #/nodes/{pubkey} hash route the rest of the app already
          // links to (see e.g. public/channels.js's node-detail links).
          marker.on('click', function () {
            close();
            window.location.hash = '#/nodes/' + encodeURIComponent(pt.publicKey);
          });
        }
      });
      if (line.length > 1) {
        L.polyline(line, { color: lineColor, weight: p.primary ? 2.5 : 1.5, opacity: p.primary ? 0.85 : 0.5 }).addTo(map);
      }
    });
    // The earliest-arriving observation, drawn last so its landmark ring
    // sits on top even when it coincides with one of the branch dots
    // above (very often it does, since `first` is usually also one of
    // the stations already plotted as its own branch).
    var firstPoint = null;
    if (data.first) {
      var firstChain = chainForBranch(data.first).chain;
      if (firstChain.length > 0) firstPoint = firstChain[firstChain.length - 1];
    }
    if (firstPoint) {
      bounds.push([firstPoint.lat, firstPoint.lon]);
      L.circleMarker([firstPoint.lat, firstPoint.lon], {
        radius: 11, color: cssVar('--status-green'), weight: 3, fillOpacity: 0, opacity: 0.9,
      })
        .addTo(map)
        .bindTooltip('🏁 First to hear it: ' + escapeHtml(firstPoint.name) + ' (' + data.first.hops + ' hop' + (data.first.hops === 1 ? '' : 's') + (firstPoint.approx ? ', approx. position' : '') + ')', { className: 'packet-path-tooltip' });
    }

    try { map.fitBounds(bounds, { padding: [30, 30] }); } catch (e) { /* single point */ }
    setTimeout(function () { map.invalidateSize(); }, 120);
    activeMap = map;

    var deepestHops = branches[0].hops;
    var statusParts = [
      plotted.length + ' of ' + branches.length + ' station' + (branches.length === 1 ? '' : 's') + ' shown',
      'deepest reached ' + deepestHops + ' hop' + (deepestHops === 1 ? '' : 's'),
    ];
    if (firstPoint) statusParts.push('entered near ' + firstPoint.name);
    if (approxTotal > 0) statusParts.push(approxTotal + ' approximate (estimated from neighbors)');
    if (missingTotal > 0) statusParts.push(missingTotal + ' hop' + (missingTotal === 1 ? '' : 's') + ' without a known position (not shown)');
    if (data.touchedAreas && data.touchedAreas.length > 0) statusParts.push('touched ' + data.touchedAreas.join(', '));
    if (statusEl) statusEl.textContent = statusParts.join(' · ');
  }

  window.PacketPathMap = { open: open, close: close };
})();
