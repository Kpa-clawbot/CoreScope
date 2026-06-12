/**
 * #1374 — Packet-route map renderer.
 *
 * Pure-ish renderer for a resolved packet route on top of a Leaflet map.
 * Caller resolves hops (server- or client-side) and passes the positions
 * array as [origin, hop1, hop2, …, destination]. This module owns:
 *
 *   - role-aware shape markers (reuses window.makeRoleMarkerSVG)
 *   - origin / destination visual + semantic distinction
 *   - sequence-number badges beside each marker (not in label text)
 *   - directional <marker-end> arrows on edges
 *   - per-hop color gradient (bright → fading)
 *   - per-marker role="img" + aria-label "Hop N of M, <name>, <role>"
 *   - per-edge aria-label "Hop N → N+1, ~Xkm"
 *   - reuses window.deconflictLabels (registered by map.js)
 *   - collapsible legend panel
 *   - "Route observed at <timestamp>" toolbar context label
 *   - partial-route: ch-unresolved class + "X of N hops resolved" badge
 *
 * Animations gate on `prefers-reduced-motion`; high-contrast / forced-colors
 * mode is handled by CSS.
 *
 * See test-issue-1374-route-map-a11y-e2e.js for the contract.
 */
(function () {
  'use strict';

  // Wong palette: per-hop sequence gradient, bright → fading.
  // Used purely as a redundant carrier alongside the sequence-number badge,
  // so colorblind / forced-colors users still read the order from the badge.
  function seqColor(idx, total) {
    if (total <= 1) return '#56F0A0';
    // HSL: 152° (green) full-bright at idx=0 → 18° (orange) at last hop.
    var t = idx / Math.max(1, total - 1);
    var hue = 152 - 134 * t;
    var sat = 70;
    var light = 50 + 8 * t;
    return 'hsl(' + hue.toFixed(0) + ',' + sat + '%,' + light + '%)';
  }

  function haversineKm(a, b) {
    if (a.lat == null || b.lat == null) return null;
    var R = 6371;
    var dLat = (b.lat - a.lat) * Math.PI / 180;
    var dLon = (b.lon - a.lon) * Math.PI / 180;
    var la1 = a.lat * Math.PI / 180, la2 = b.lat * Math.PI / 180;
    var h = Math.sin(dLat / 2) * Math.sin(dLat / 2) +
            Math.cos(la1) * Math.cos(la2) *
            Math.sin(dLon / 2) * Math.sin(dLon / 2);
    return Math.round(R * 2 * Math.atan2(Math.sqrt(h), Math.sqrt(1 - h)));
  }

  function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
    });
  }

  /**
   * Build the role-aware marker SVG for a hop. Origin and destination get a
   * larger outline + a Phosphor sprite glyph (play/flag) layered on the
   * standard role shape so the role information remains visible.
   * #1648 M4: prior glyphs were inline <text> chars (\u25B6 / \u2691). // EMOJI-OK: comment
   */
  function buildHopSVG(p, opts) {
    var size = opts.size || 22;
    var role = p.role || 'companion';
    var color = opts.color;
    var inner = (window.makeRoleMarkerSVG &&
      window.makeRoleMarkerSVG(role, color, size)) ||
      '<svg width="' + size + '" height="' + size + '"><circle cx="' + (size / 2) +
      '" cy="' + (size / 2) + '" r="' + (size / 2 - 2) + '" fill="' + color +
      '" stroke="#fff" stroke-width="1"/></svg>';
    // Outer ring for origin/destination
    var outerSize = (opts.isOrigin || opts.isDest) ? size + 10 : size + 4;
    var pad = (outerSize - size) / 2;
    var ringStroke = opts.isOrigin ? '#06b6d4' : opts.isDest ? '#ef4444' : '#666';
    var ringWidth = (opts.isOrigin || opts.isDest) ? 2.4 : 1.2;
    var ringDash = opts.unresolved ? '4 3' : 'none';
    var ringFill = opts.unresolved ? 'rgba(150,150,150,0.15)' : 'none';

    // Phosphor sprite <use> overlaid on the role marker. Sized to ~55% of
    // outer ring, centered on the marker. fill="#0f172a" preserves the
    // dark-on-light glyph contrast of the prior <text> implementation.
    var glyph = '';
    if (opts.isOrigin || opts.isDest) {
      var gid = opts.isOrigin ? 'ph-play' : 'ph-flag';
      var gSize = Math.round(outerSize * 0.55);
      var gOff = (outerSize - gSize) / 2;
      glyph = '<svg x="' + gOff + '" y="' + gOff + '" width="' + gSize +
        '" height="' + gSize + '" viewBox="0 0 256 256" fill="#0f172a"' +
        ' aria-hidden="true"><use href="/icons/phosphor-sprite.svg#' + gid +
        '"/></svg>';
    }

    // Strip outer <svg> from inner SVG, re-wrap with outer ring + glyph
    var innerBody = inner.replace(/^<svg[^>]*>/, '').replace(/<\/svg>$/, '');
    var svg = '<svg width="' + outerSize + '" height="' + outerSize +
      '" viewBox="0 0 ' + outerSize + ' ' + outerSize +
      '" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">' +
      '<circle cx="' + (outerSize / 2) + '" cy="' + (outerSize / 2) +
      '" r="' + (outerSize / 2 - ringWidth / 2) +
      '" fill="' + ringFill + '" stroke="' + ringStroke +
      '" stroke-width="' + ringWidth + '" stroke-dasharray="' + ringDash + '"/>' +
      '<g transform="translate(' + pad + ',' + pad + ')">' + innerBody + '</g>' +
      glyph +
      '</svg>';
    return { svg: svg, size: outerSize };
  }

  function buildBadge(idx, total, opts) {
    // Intermediate hops render the hop number; origin/destination render a
    // Phosphor sprite glyph (play/flag) in place of the prior \u25B6 / \u2691. // EMOJI-OK: comment
    if (opts.isOrigin || opts.isDest) {
      var gid = opts.isOrigin ? 'ph-play' : 'ph-flag';
      return '<span class="mc-route-seq-badge" aria-hidden="true">' +
        '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#' +
        gid + '"/></svg></span>';
    }
    return '<span class="mc-route-seq-badge" aria-hidden="true">' + String(idx) + '</span>';
  }

  function buildPopupHtml(p, hopNum, total) {
    var pubkeyShort = p.pubkey ? String(p.pubkey).slice(0, 12) : '—';
    var roleLine = escapeHtml(p.role || 'unknown');
    var lastSeen = p.last_seen
      ? new Date(p.last_seen).toLocaleString()
      : (p.last_heard ? new Date(p.last_heard).toLocaleString() : '—');
    var obsCount = p.observation_count != null ? p.observation_count : '—';
    var coords = (p.lat != null && p.lon != null)
      ? (p.lat.toFixed(4) + ', ' + p.lon.toFixed(4))
      : '—';
    var deepLink = p.pubkey
      ? '<div style="margin-top:6px"><a class="mc-route-popup-link" href="#/map?node=' +
        encodeURIComponent(p.pubkey) + '">Show on main map \u2192</a></div>'
      : '';
    return '<div class="mc-route-popup">' +
      '<div class="mc-route-popup-title">Hop ' + hopNum + ' of ' + total +
      ': ' + escapeHtml(p.name || pubkeyShort) + '</div>' +
      '<div class="mc-route-popup-row"><span>Role</span><b>' + roleLine + '</b></div>' +
      '<div class="mc-route-popup-row"><span>Pubkey</span><code>' +
      escapeHtml(pubkeyShort) + '\u2026</code></div>' +
      '<div class="mc-route-popup-row"><span>Last seen</span>' + escapeHtml(lastSeen) + '</div>' +
      '<div class="mc-route-popup-row"><span>Observations</span>' + escapeHtml(String(obsCount)) + '</div>' +
      '<div class="mc-route-popup-row"><span>Coords</span>' + escapeHtml(coords) + '</div>' +
      deepLink +
      '</div>';
  }

  function ariaLabelFor(p, idx, total) {
    var name = p.name || (p.pubkey ? String(p.pubkey).slice(0, 8) : 'unknown');
    var role = p.role || 'unknown';
    var base = 'Hop ' + (idx + 1) + ' of ' + total + ', ' + name + ', ' + role;
    if (p.isOrigin) base += ', originator';
    if (p.isDest) base += ', destination';
    if (p.resolved === false) base += ', unresolved';
    return base;
  }

  function ensureArrowDefs(mapRef) {
    // Inject a single SVG <defs> into Leaflet's overlay pane.
    var pane = mapRef.getPane && mapRef.getPane('overlayPane');
    if (!pane) return;
    if (document.getElementById('mc-route-arrow-defs')) return;
    var ns = 'http://www.w3.org/2000/svg';
    var svgNS = document.createElementNS(ns, 'svg');
    svgNS.setAttribute('id', 'mc-route-arrow-defs');
    svgNS.setAttribute('width', '0');
    svgNS.setAttribute('height', '0');
    svgNS.setAttribute('style', 'position:absolute;width:0;height:0;overflow:hidden;');
    svgNS.setAttribute('aria-hidden', 'true');
    var defs = document.createElementNS(ns, 'defs');
    var marker = document.createElementNS(ns, 'marker');
    marker.setAttribute('id', 'mc-route-arrow');
    marker.setAttribute('viewBox', '0 0 10 10');
    marker.setAttribute('refX', '8');
    marker.setAttribute('refY', '5');
    marker.setAttribute('markerWidth', '6');
    marker.setAttribute('markerHeight', '6');
    marker.setAttribute('orient', 'auto-start-reverse');
    var poly = document.createElementNS(ns, 'path');
    poly.setAttribute('d', 'M0,0 L10,5 L0,10 z');
    poly.setAttribute('fill', 'currentColor');
    marker.appendChild(poly);
    defs.appendChild(marker);
    svgNS.appendChild(defs);
    document.body.appendChild(svgNS);
  }

  function buildLegend(container, resolvedCount, totalCount) {
    // Remove any prior legend
    var prior = container.querySelector('.mc-route-legend');
    if (prior) prior.remove();

    var roles = ['repeater', 'companion', 'room', 'sensor', 'observer'];
    var roleEntries = roles.map(function (r) {
      var color = (window.ROLE_COLORS && window.ROLE_COLORS[r]) || '#888';
      var svg = window.makeRoleMarkerSVG ? window.makeRoleMarkerSVG(r, color, 14) : '';
      return '<li class="mc-route-legend-entry mc-route-legend-role">' +
        '<span class="mc-route-legend-swatch">' + svg + '</span>' +
        '<span>' + r + '</span></li>';
    }).join('');

    var html =
      '<div class="mc-route-legend" role="region" aria-label="Route legend">' +
        '<button type="button" class="mc-route-legend-toggle" aria-expanded="true" aria-controls="mc-route-legend-body">' +
          'Legend' +
        '</button>' +
        '<div id="mc-route-legend-body" class="mc-route-legend-body">' +
          (resolvedCount < totalCount
            ? '<div class="mc-route-resolved-badge" role="status">' +
              resolvedCount + ' of ' + totalCount + ' hops resolved</div>'
            : '<div class="mc-route-resolved-badge" role="status">' +
              totalCount + ' of ' + totalCount + ' hops resolved</div>') +
          '<ul class="mc-route-legend-list">' +
            '<li class="mc-route-legend-entry"><span class="mc-route-legend-glyph" aria-hidden="true">\u25B6</span><span>origin (originator)</span></li>' +
            '<li class="mc-route-legend-entry"><span class="mc-route-legend-glyph" aria-hidden="true">\u2691</span><span>destination</span></li>' +
            '<li class="mc-route-legend-entry"><span class="mc-route-legend-gradient" aria-hidden="true"></span><span>hop-order color (bright \u2192 fading)</span></li>' +
          '</ul>' +
          '<div class="mc-route-legend-section">role shapes</div>' +
          '<ul class="mc-route-legend-list">' + roleEntries + '</ul>' +
        '</div>' +
      '</div>';

    var wrap = document.createElement('div');
    wrap.innerHTML = html;
    var node = wrap.firstChild;
    container.appendChild(node);

    var btn = node.querySelector('.mc-route-legend-toggle');
    var body = node.querySelector('.mc-route-legend-body');
    btn.addEventListener('click', function () {
      var open = btn.getAttribute('aria-expanded') === 'true';
      btn.setAttribute('aria-expanded', String(!open));
      body.style.display = open ? 'none' : '';
    });
  }

  function buildContextLabel(container, timestamp) {
    var prior = container.querySelector('.mc-route-context-label');
    if (prior) prior.remove();
    var ts = timestamp ? new Date(timestamp).toLocaleString() : 'unknown time';
    var el = document.createElement('div');
    el.className = 'mc-route-context-label';
    el.setAttribute('role', 'status');
    el.textContent = 'Route observed at ' + ts;
    container.appendChild(el);
  }

  /**
   * Render the route. Caller passes the Leaflet map, a clean layer group,
   * and the ordered positions array.
   *
   * @param {L.Map} mapRef
   * @param {L.LayerGroup} layer
   * @param {Array<{lat,lon,name,role,pubkey,isOrigin?,isDest?,resolved?,
   *                last_seen?,last_heard?,observation_count?}>} positions
   * @param {{timestamp?:string|number}} [opts]
   */
  function render(mapRef, layer, positions, opts) {
    opts = opts || {};
    if (!mapRef || !layer || !Array.isArray(positions) || positions.length === 0) return;

    layer.clearLayers();
    ensureArrowDefs(mapRef);

    // Mark origin / destination explicitly. If caller didn't set isDest, the
    // last resolved hop becomes the destination.
    var total = positions.length;
    var resolvedCount = positions.filter(function (p) { return p.resolved !== false; }).length;
    positions.forEach(function (p, i) {
      if (i === 0 && !('isOrigin' in p)) p.isOrigin = true;
      if (i === total - 1 && !('isDest' in p)) p.isDest = true;
    });

    // Partial-route placement: unresolved hops with no lat/lon are
    // interpolated between the nearest resolved neighbors so they render as
    // dashed-gray placeholders on the route line.
    for (var pi = 0; pi < positions.length; pi++) {
      var cur = positions[pi];
      if (cur.lat != null && cur.lon != null) continue;
      var before = null, after = null;
      for (var k = pi - 1; k >= 0; k--) {
        if (positions[k].lat != null && positions[k].lon != null) { before = positions[k]; break; }
      }
      for (var k2 = pi + 1; k2 < positions.length; k2++) {
        if (positions[k2].lat != null && positions[k2].lon != null) { after = positions[k2]; break; }
      }
      if (before && after) {
        cur.lat = (before.lat + after.lat) / 2;
        cur.lon = (before.lon + after.lon) / 2;
      } else if (before) {
        cur.lat = before.lat; cur.lon = before.lon;
      } else if (after) {
        cur.lat = after.lat; cur.lon = after.lon;
      }
    }

    var reduceMotion = window.matchMedia &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    // ── Edges ───────────────────────────────────────────────────────
    // #1689 r1 (adv #2): when the 1-byte hop filter dropped hops, draw the
    // edges as dashed + lighter so operators don't misread the resulting
    // origin→dest polyline as "direct delivery, no hops in path".
    var hopsHiddenCount = (opts && typeof opts.hopsHiddenCount === 'number') ? opts.hopsHiddenCount : 0;
    var redacted = hopsHiddenCount > 0;
    for (var i = 0; i < total - 1; i++) {
      var a = positions[i], b = positions[i + 1];
      if (a.lat == null || a.lon == null || b.lat == null || b.lon == null) continue;
      var color = seqColor(i, total - 1);
      var dist = haversineKm(a, b);
      var ariaLabel = 'Hop ' + (i + 1) + ' \u2192 ' + (i + 2) +
        (dist != null ? ', ~' + dist + 'km' : '') +
        (redacted ? ' (' + hopsHiddenCount + ' 1-byte hop(s) hidden)' : '');
      var edgeDash = (a.resolved === false || b.resolved === false)
        ? '6 4'
        : (redacted ? '4 6' : null);
      var poly = L.polyline([[a.lat, a.lon], [b.lat, b.lon]], {
        color: color,
        weight: 3.5,
        opacity: redacted ? 0.6 : 0.92,
        dashArray: edgeDash,
        className: 'mc-route-edge' + (redacted ? ' mc-route-edge-redacted' : '')
      }).addTo(layer);

      // Midpoint badge — only on the first redacted edge — that reads
      // "N hops hidden (1-byte)". Reuses Leaflet's divIcon so it picks up
      // the theme tokens via .mc-route-redacted-badge in CSS.
      if (redacted && i === 0) {
        var mid = [(a.lat + b.lat) / 2, (a.lon + b.lon) / 2];
        var badgeHtml = '<span class="mc-route-redacted-badge" title="Hidden by customizer: 1-byte path hops are filtered. Toggle off in Customize to see the full path.">' +
          hopsHiddenCount + ' hop' + (hopsHiddenCount === 1 ? '' : 's') + ' hidden (1-byte)' +
          '</span>';
        L.marker(mid, {
          icon: L.divIcon({ className: 'mc-route-redacted-badge-wrap', html: badgeHtml, iconSize: [120, 18], iconAnchor: [60, 9] }),
          interactive: true,
          keyboard: false
        }).addTo(layer);
      }

      // Patch the rendered <path> element to add aria-label + marker-end.
      // Leaflet builds it on the next animation frame, so defer.
      (function (polyRef, lbl, col) {
        setTimeout(function () {
          var el = polyRef.getElement && polyRef.getElement();
          if (!el) return;
          el.setAttribute('aria-label', lbl);
          el.setAttribute('role', 'img');
          el.classList.add('mc-route-edge');
          el.setAttribute('marker-end', 'url(#mc-route-arrow)');
          el.style.color = col; // arrow inherits via currentColor
          if (reduceMotion) el.style.transition = 'none';
        }, 0);
      })(poly, ariaLabel, color);
    }

    // ── Markers + labels ────────────────────────────────────────────
    var labelItems = [];
    positions.forEach(function (p, i) {
      if (p.lat == null || p.lon == null) return;
      var unresolved = (p.resolved === false);
      // #1648 M6 (M4 CDP carry-forward): hop fallback color now reads
      // --status-info from CSS so dark theme picks up the brighter
      // #60a5fa variant instead of the baked-in #3b82f6.
      var fallbackColor = (typeof window !== 'undefined' && window.getComputedStyle)
        ? (getComputedStyle(document.documentElement).getPropertyValue('--status-info').trim() || '#3b82f6')
        : '#3b82f6';
      var color = unresolved ? '#9ca3af' : ((window.ROLE_COLORS && window.ROLE_COLORS[p.role]) || fallbackColor);
      var size = (p.isOrigin || p.isDest) ? 24 : 18;
      var built = buildHopSVG(p, { color: color, size: size, isOrigin: p.isOrigin, isDest: p.isDest, unresolved: unresolved });
      var badge = buildBadge(i + 1, total, { isOrigin: p.isOrigin, isDest: p.isDest });
      var classNames = 'mc-route-marker' + (unresolved ? ' ch-unresolved' : '') +
        (p.isOrigin ? ' mc-route-origin' : '') + (p.isDest ? ' mc-route-dest' : '');
      var aria = ariaLabelFor(p, i, total);
      var html =
        '<div class="' + classNames + '" role="img" aria-label="' + escapeHtml(aria) +
          '" tabindex="0" data-hop-index="' + i + '">' +
          built.svg +
          badge +
        '</div>';
      var icon = L.divIcon({
        html: html,
        className: 'mc-route-marker-icon',
        iconSize: [built.size + 14, built.size + 14],
        iconAnchor: [(built.size + 14) / 2, (built.size + 14) / 2]
      });
      var marker = L.marker([p.lat, p.lon], { icon: icon, keyboard: true }).addTo(layer);
      marker.bindPopup(buildPopupHtml(p, i + 1, total), { className: 'mc-route-popup-wrap' });

      labelItems.push({
        latLng: L.latLng(p.lat, p.lon),
        isLabel: true,
        text: p.name || (p.pubkey ? String(p.pubkey).slice(0, 8) : 'hop')
      });
    });

    // Deconflict label boxes — reuses map.js' shared algorithm.
    if (typeof window.deconflictLabels === 'function') {
      window.deconflictLabels(labelItems, mapRef);
    }
    labelItems.forEach(function (m) {
      var pos = m.adjustedLatLng || m.latLng;
      var labelHtml = '<div class="mc-route-label">' + escapeHtml(m.text) + '</div>';
      var icon = L.divIcon({
        html: labelHtml,
        className: 'mc-route-label-icon',
        iconSize: null,
        iconAnchor: [0, -16]
      });
      var lblMarker = L.marker(pos, { icon: icon, interactive: false }).addTo(layer);
      m._lblMarker = lblMarker;
      if (m.offset && m.offset > 2) {
        L.polyline([m.latLng, pos], {
          weight: 1, color: '#475569', opacity: 0.5, dashArray: '3 3'
        }).addTo(layer);
      }
    });

    // Second-pass overlap resolution: shared `deconflictLabels` uses a fixed
    // 38×24 collision box, but our role-aware labels are often wider. After
    // Leaflet paints, measure the real DOM rects and nudge any overlapping
    // labels vertically using an L.DomUtil offset (no relayout).
    //
    // We run the nudge once immediately AND again after `fitBounds`
    // completes its async pan (`moveend`), because fitBounds re-projects
    // the labels and can re-introduce overlap that the first nudge missed.
    function nudgeOverlappingLabels() {
      var containerEl = mapRef.getContainer ? mapRef.getContainer() : document.body;
      var labelEls = Array.from(containerEl.querySelectorAll('.mc-route-label'));
      // Reset prior nudges so we recompute from scratch (otherwise stacked
      // nudges from successive passes drift labels off-screen).
      for (var li = 0; li < labelEls.length; li++) {
        var parent = labelEls[li].parentElement;
        if (parent && parent.dataset && parent.dataset.mcRouteDy) {
          parent.style.marginTop = '';
          delete parent.dataset.mcRouteDy;
        }
      }
      var rects = labelEls.map(function (el) { return el.getBoundingClientRect(); });
      var maxIter = 8;
      for (var iter = 0; iter < maxIter; iter++) {
        var moved = false;
        for (var i = 0; i < labelEls.length; i++) {
          for (var j = i + 1; j < labelEls.length; j++) {
            var a = rects[i], b = rects[j];
            if (a.x < b.x + b.width && a.x + a.width > b.x &&
                a.y < b.y + b.height && a.y + a.height > b.y) {
              // Push the later label downward by the overlap height + 6px.
              var dy = (a.y + a.height) - b.y + 6;
              var p2 = labelEls[j].parentElement;
              if (p2 && p2.style) {
                var prev = p2.dataset.mcRouteDy ? Number(p2.dataset.mcRouteDy) : 0;
                var next = prev + dy;
                p2.dataset.mcRouteDy = String(next);
                p2.style.marginTop = next + 'px';
              }
              rects[j] = labelEls[j].getBoundingClientRect();
              moved = true;
            }
          }
        }
        if (!moved) break;
      }
    }
    setTimeout(nudgeOverlappingLabels, 30);
    mapRef.once('moveend', function () { setTimeout(nudgeOverlappingLabels, 30); });

    // Fit map to route
    var coords = positions.filter(function (p) { return p.lat != null && p.lon != null; })
      .map(function (p) { return [p.lat, p.lon]; });
    if (coords.length >= 2) {
      mapRef.fitBounds(L.latLngBounds(coords).pad(0.3));
    } else if (coords.length === 1) {
      mapRef.setView(coords[0], 13);
    }

    // ── Overlay UI: legend + context label ──────────────────────────
    var container = mapRef.getContainer ? mapRef.getContainer() : document.getElementById('leaflet-map');
    if (container) {
      buildLegend(container, resolvedCount, total);
      buildContextLabel(container, opts.timestamp);
    }
  }

  window.MeshRoute = {
    render: render,
    _seqColor: seqColor,
    _haversineKm: haversineKm,
    _ariaLabelFor: ariaLabelFor
  };
})();
