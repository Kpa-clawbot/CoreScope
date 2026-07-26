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

  // Plain "Xs"/"Xm Ys" duration, no leading "+" or "first to arrive" --
  // for footer stats standing on their own (unlike formatElapsed's
  // tooltip use, these aren't continuing a "this station arrived..."
  // sentence).
  function formatDuration(seconds) {
    if (seconds < 60) return seconds.toFixed(1) + 's';
    var m = Math.floor(seconds / 60);
    var s = Math.round(seconds % 60);
    return m + 'm ' + s + 's';
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
        '<p class="text-muted" style="margin:0 0 8px;font-size:12px">How far and how wide this packet spread. Click a marker to open that node\'s detail page.</p>' +
        '<div style="display:flex;flex-wrap:wrap;gap:10px 14px;align-items:center;margin:0 0 10px;font-size:11px;color:var(--text-muted)">' +
          '<span style="display:inline-flex;align-items:center;gap:4px"><span style="display:inline-block;width:14px;height:2px;background:var(--accent)"></span><span id="packetPathPrimaryLegendLabel">farthest-traveled route</span></span>' +
          '<span id="packetPathDeepestLegendItem" style="display:none;align-items:center;gap:4px"><span style="display:inline-block;width:14px;height:2px;background:var(--status-purple)"></span>deepest (most hops) route</span>' +
          '<span style="display:inline-flex;align-items:center;gap:4px"><span style="display:inline-block;width:14px;height:2px;background:var(--text-muted)"></span>other station</span>' +
          '<span style="display:inline-flex;align-items:center;gap:4px"><span style="display:inline-block;width:9px;height:9px;border:2px dashed var(--text-muted);border-radius:50%;box-sizing:border-box"></span>approximate position</span>' +
          '<span style="display:inline-flex;align-items:center;gap:4px"><span style="display:inline-block;width:9px;height:9px;border:2px solid var(--status-green);border-radius:50%;box-sizing:border-box"></span>first to hear it</span>' +
        '</div>' +
        '<div id="packetPathControls"></div>' +
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
    var plotted = branches.map(function (b) {
      var built = chainForBranch(b);
      return { branch: b, chain: built.chain, missing: built.missing, highlightRole: null };
    }).filter(function (p) { return p.chain.length > 0; });

    // Two different questions, not always the same branch: which station
    // did the packet reach at the greatest real-world DISTANCE
    // (distanceFromFirstKm), and which took the most HOPS to reach it (a
    // dense area can take many short hops; a couple of long-range links
    // can cover more real distance in fewer -- caught on a real packet:
    // 7 hops but 117km vs. 6 hops but 124km). Both get their own
    // highlight when they're different branches; when they coincide (the
    // common case) it's shown once, combined, exactly as before.
    var hasDistanceData = plotted.some(function (p) { return typeof p.branch.distanceFromFirstKm === 'number'; });
    var deepestIdx = 0;
    var deepestHopsSeen = -1;
    plotted.forEach(function (p, i) {
      if (p.branch.hops > deepestHopsSeen) {
        deepestHopsSeen = p.branch.hops;
        deepestIdx = i;
      }
    });
    var farthestIdx = -1;
    if (hasDistanceData) {
      var farthestKm = -1;
      plotted.forEach(function (p, i) {
        var d = p.branch.distanceFromFirstKm;
        if (typeof d === 'number' && d > farthestKm) {
          farthestKm = d;
          farthestIdx = i;
        }
      });
    }
    // 'deepestFallback' (no distance data anywhere) reuses the farthest
    // slot's accent color -- there's only one highlighted concept in
    // that case, same single-highlight look as before this branch/role
    // split existed. 'deepest' (purple) only appears when it's actually
    // a DIFFERENT branch from farthest -- the genuinely new information.
    if (!hasDistanceData) {
      if (plotted[deepestIdx]) plotted[deepestIdx].highlightRole = 'deepestFallback';
    } else if (farthestIdx === deepestIdx) {
      if (plotted[deepestIdx]) plotted[deepestIdx].highlightRole = 'both';
    } else {
      if (plotted[farthestIdx]) plotted[farthestIdx].highlightRole = 'farthest';
      if (plotted[deepestIdx]) plotted[deepestIdx].highlightRole = 'deepest';
    }

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
    var deepestColor = cssVar('--status-purple');
    var observerColor = cssVar('--status-yellow');
    var muted = cssVar('--text-muted');
    // 'both'/'farthest'/'deepestFallback' all read as the accent color;
    // only a genuinely-distinct 'deepest' branch gets the second color.
    function colorForRole(role) {
      if (role === 'deepest') return deepestColor;
      if (role) return accent;
      return muted;
    }

    // Shade every touched area's configured boundary as a faint
    // background layer -- ties the "touched: X, Y" footer text to actual
    // geography. Non-interactive so it never steals clicks from branch
    // markers, and deliberately excluded from the fitBounds calculation
    // below: a huge area (e.g. a whole region) would zoom the map out
    // past the point where individual branches are still readable.
    // Tracked separately from marker/polyline layers below -- toggled by
    // its own "show area boundaries" checkbox, independent of the
    // branch/approx filters.
    var areaShapeLayers = [];
    (data.touchedAreas || []).forEach(function (area) {
      var shapeOpts = { color: accent, weight: 1, opacity: 0.35, fillColor: accent, fillOpacity: 0.06, interactive: false };
      var shape = null;
      if (area.polygon && area.polygon.length > 0) {
        shape = L.polygon(area.polygon, shapeOpts).addTo(map);
      } else if (area.latMin != null && area.latMax != null && area.lonMin != null && area.lonMax != null) {
        shape = L.rectangle([[area.latMin, area.lonMin], [area.latMax, area.lonMax]], shapeOpts).addTo(map);
      }
      if (shape) areaShapeLayers.push(shape);
    });

    var bounds = [];
    var missingTotal = 0;
    var approxTotal = 0;
    // Every branch marker/polyline, tagged with which filters apply to it
    // -- the "show only farthest route" and "show only approximate"
    // checkboxes are independent and combinable (e.g. both checked at
    // once), so visibility is recomputed from BOTH current checkbox
    // states together (applyMarkerFilters below) rather than each
    // checkbox naively add/removing its own layers -- that naive
    // approach breaks as soon as two filters overlap on the same layer
    // and get toggled in different orders. The landmark "first to hear
    // it" ring (drawn separately below) is never tracked here: it's not
    // a branch, it stays visible regardless of either filter.
    var markerEntries = [];
    var polylineEntries = [];
    // The same physical node (e.g. a shared entry-point repeater near the
    // sender) commonly appears in many branches' chains -- dedupe by
    // identity so "N approximate" counts distinct stations, not chain
    // appearances (#1... a packet heard by 12 stations through one shared
    // repeater was showing "11 approximate" for what was really 1 node).
    var approxSeen = {};
    // Draw non-highlighted branches first so the highlighted one(s) --
    // farthest and/or deepest -- end up drawn on top.
    var ordered = plotted.slice().sort(function (a, b) { return (a.highlightRole ? 1 : 0) - (b.highlightRole ? 1 : 0); });
    ordered.forEach(function (p) {
      missingTotal += p.missing;
      var isHighlighted = !!p.highlightRole;
      var lineColor = colorForRole(p.highlightRole);
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
        var radius = isHighlighted ? (pt.isObserver ? 7 : 6) : (pt.isObserver ? 5 : 4);
        // Observer dots always FILL yellow (the established "this is a
        // hearing station" language, kept regardless of role) -- but for
        // an observer that's also the branch's only point (a direct/
        // 0-hop reception has no relay hops and thus no polyline
        // either), fill color alone can't show whether it's the
        // farthest, deepest, or neither. The STROKE carries the role
        // color instead when highlighted, so a farthest/deepest observer
        // still reads as accent/purple-ringed, not just "a yellow dot"
        // indistinguishable from every other observer.
        var strokeColor = isHighlighted ? lineColor : outline;
        var markerOpts = pt.approx
          // Approximate (borrowed-from-neighbor) position: larger,
          // thick-dashed ring with a faint fill -- a plain hollow outline
          // at normal marker size was too easy to miss against map
          // tiles, so this deliberately reads as a bigger, softer blob
          // rather than a precise dot. Size/fill scale with confidence:
          // more agreeing neighbors = tighter, more solid; one neighbor
          // or a wide spread among several = bigger, fainter.
          ? {
              radius: radius + approxRadiusBonus(pt.approxNeighborCount, pt.approxSpreadKm), color: isHighlighted ? lineColor : color, weight: 3,
              fillColor: color, fillOpacity: approxFillOpacity(pt.approxNeighborCount), dashArray: '5,4',
            }
          : { radius: radius, color: strokeColor, weight: isHighlighted ? 2 : 1, fillColor: color, fillOpacity: isHighlighted ? 1 : 0.8 };
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
        markerEntries.push({ layer: marker, primary: isHighlighted, approx: !!pt.approx });
      });
      if (line.length > 1) {
        var polyline = L.polyline(line, { color: lineColor, weight: isHighlighted ? 2.5 : 1.5, opacity: isHighlighted ? 0.85 : 0.5 }).addTo(map);
        polylineEntries.push({ layer: polyline, primary: isHighlighted });
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

    // Label the highlight(s) honestly, matching the role split above:
    // no distance data at all -> one accent-colored "deepest (most
    // hops)" route (old single-highlight look); farthest and deepest
    // are the same branch -> one accent-colored route, combined label;
    // genuinely different branches -> two legend entries, accent
    // "farthest-traveled" plus a second purple "deepest (most hops)".
    var primaryLabel;
    var showDeepestLegendItem = false;
    if (!hasDistanceData) {
      primaryLabel = 'deepest (most hops)';
    } else if (farthestIdx === deepestIdx) {
      primaryLabel = 'farthest-traveled & deepest';
    } else {
      primaryLabel = 'farthest-traveled';
      showDeepestLegendItem = true;
    }
    var primaryLegendLabel = document.getElementById('packetPathPrimaryLegendLabel');
    if (primaryLegendLabel) primaryLegendLabel.textContent = primaryLabel + ' route';
    var deepestLegendItem = document.getElementById('packetPathDeepestLegendItem');
    if (deepestLegendItem && showDeepestLegendItem) deepestLegendItem.style.display = 'inline-flex';

    // Build whichever filter checkboxes are actually relevant for this
    // packet -- a single-branch packet gets no declutter toggle, one
    // with no approximate positions gets no approx-only toggle, etc.
    var controlsEl = document.getElementById('packetPathControls');
    var controlsHtml = '';
    var highlightedCount = plotted.filter(function (p) { return !!p.highlightRole; }).length;
    var hiddenCount = plotted.length - highlightedCount;
    var toggleRouteLabel = showDeepestLegendItem ? 'farthest-traveled and deepest routes' : (primaryLabel + ' route');
    if (plotted.length > highlightedCount) {
      controlsHtml +=
        '<label style="display:flex;align-items:center;gap:6px;font-size:12px;margin:0 0 8px;cursor:pointer;color:var(--text-muted)">' +
          '<input type="checkbox" id="packetPathPrimaryOnly">' +
          'Show only the ' + toggleRouteLabel + ' (' + hiddenCount + ' other station' + (hiddenCount === 1 ? '' : 's') + ' hidden when checked)' +
        '</label>';
    }
    if (approxTotal > 0) {
      controlsHtml +=
        '<label style="display:flex;align-items:center;gap:6px;font-size:12px;margin:0 0 8px;cursor:pointer;color:var(--text-muted)">' +
          '<input type="checkbox" id="packetPathApproxOnly">' +
          'Show only approximate positions (' + approxTotal + ' estimated from neighbors)' +
        '</label>';
    }
    if (data.touchedAreas && data.touchedAreas.length > 0) {
      controlsHtml +=
        '<label style="display:flex;align-items:center;gap:6px;font-size:12px;margin:0 0 8px;cursor:pointer;color:var(--text-muted)">' +
          '<input type="checkbox" id="packetPathShowAreas">' +
          'Show area boundaries on the map' +
        '</label>';
    }
    if (controlsEl && controlsHtml) controlsEl.innerHTML = controlsHtml;

    var primaryOnlyCheckbox = document.getElementById('packetPathPrimaryOnly');
    var approxOnlyCheckbox = document.getElementById('packetPathApproxOnly');
    // Recomputes every marker/polyline's visibility from BOTH checkboxes'
    // CURRENT state together, rather than each checkbox naively add/
    // removing only its own layers -- the two filters are independent
    // and combinable (e.g. both checked at once), and a naive per-
    // checkbox toggle breaks as soon as they overlap on the same layer
    // and get unchecked in a different order than they were checked.
    function applyMarkerFilters() {
      var primaryOnly = !!(primaryOnlyCheckbox && primaryOnlyCheckbox.checked);
      var approxOnly = !!(approxOnlyCheckbox && approxOnlyCheckbox.checked);
      markerEntries.forEach(function (m) {
        var visible = (!primaryOnly || m.primary) && (!approxOnly || m.approx);
        if (visible) m.layer.addTo(map); else map.removeLayer(m.layer);
      });
      polylineEntries.forEach(function (p) {
        var visible = !primaryOnly || p.primary;
        if (visible) p.layer.addTo(map); else map.removeLayer(p.layer);
      });
    }
    if (primaryOnlyCheckbox) primaryOnlyCheckbox.addEventListener('change', applyMarkerFilters);
    if (approxOnlyCheckbox) approxOnlyCheckbox.addEventListener('change', applyMarkerFilters);

    // Area-boundary visibility is a separate, independent toggle -- not
    // part of applyMarkerFilters since it's a different layer type
    // entirely and never interacts with the branch/approx filters above.
    var showAreasCheckbox = document.getElementById('packetPathShowAreas');
    if (showAreasCheckbox) {
      showAreasCheckbox.checked = true; // shapes are drawn (visible) by default; unchecking hides them
      showAreasCheckbox.addEventListener('change', function () {
        areaShapeLayers.forEach(function (layer) {
          if (showAreasCheckbox.checked) layer.addTo(map); else map.removeLayer(layer);
        });
      });
    }

    var deepestHops = branches[0].hops;
    var deepestBranch = plotted[deepestIdx] && plotted[deepestIdx].branch;
    var deepestSeconds = deepestBranch && typeof deepestBranch.secondsAfterFirst === 'number' ? deepestBranch.secondsAfterFirst : null;
    var statusParts = [
      plotted.length + ' of ' + branches.length + ' station' + (branches.length === 1 ? '' : 's') + ' shown',
      'deepest reached ' + deepestHops + ' hop' + (deepestHops === 1 ? '' : 's') + (deepestSeconds != null ? ' (' + formatDuration(deepestSeconds) + ')' : ''),
    ];
    if (hasDistanceData) {
      var farthestBranch = plotted[farthestIdx] && plotted[farthestIdx].branch;
      var farthestSeconds = farthestBranch && typeof farthestBranch.secondsAfterFirst === 'number' ? farthestBranch.secondsAfterFirst : null;
      statusParts.push('farthest reached ' + farthestKm.toFixed(1) + 'km' + (farthestSeconds != null ? ' (' + formatDuration(farthestSeconds) + ')' : ''));
    }
    // How long the whole flood took to finish reaching every station it
    // ever reached -- the largest secondsAfterFirst across ALL branches,
    // not just the farthest/deepest ones (a station that's neither can
    // still be the last to hear it).
    var maxSpreadSeconds = null;
    plotted.forEach(function (p) {
      var s = p.branch.secondsAfterFirst;
      if (typeof s === 'number' && (maxSpreadSeconds === null || s > maxSpreadSeconds)) maxSpreadSeconds = s;
    });
    if (maxSpreadSeconds != null && maxSpreadSeconds > 0) statusParts.push('fully spread in ' + formatDuration(maxSpreadSeconds));
    if (firstPoint) statusParts.push('entered near ' + firstPoint.name);
    if (approxTotal > 0) statusParts.push(approxTotal + ' approximate (estimated from neighbors)');
    if (missingTotal > 0) statusParts.push(missingTotal + ' hop' + (missingTotal === 1 ? '' : 's') + ' without a known position (not shown)');
    if (data.touchedAreas && data.touchedAreas.length > 0) {
      statusParts.push('touched ' + data.touchedAreas.map(function (a) { return a.label; }).join(', '));
    }
    if (statusEl) statusEl.textContent = statusParts.join(' · ');
  }

  window.PacketPathMap = { open: open, close: close };
})();
