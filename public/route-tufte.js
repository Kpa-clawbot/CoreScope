/* route-tufte.js — Tufte-prescribed redesign of the packet-route map view.
 *
 * Principles (from consultation):
 *   - The data is a SEQUENCE. Geography is annotation.
 *   - Sequence encoded ONCE: viridis edge gradient (1=purple → N=yellow).
 *   - NO numeric chips on markers. NO mid-edge arrows. NO floating labels.
 *   - Origin = filled square 2x. Destination = filled triangle 2x. Intermediates = plain 8px circles.
 *   - Sidebar timeline (320px) is the PRIMARY view; map is secondary locator.
 *   - Hop-distance sparkline at top of sidebar.
 *   - Hover sidebar row → marker scales 1.5x, edge segment highlights.
 *   - Mobile: sidebar full-width, map toggle-only.
 */
(function () {
  'use strict';

  function haversineKm(a, b) {
    if (a == null || b == null || a.lat == null || b.lat == null) return null;
    var R = 6371, dLat = (b.lat - a.lat) * Math.PI / 180;
    var dLon = (b.lon - a.lon) * Math.PI / 180;
    var la1 = a.lat * Math.PI / 180, la2 = b.lat * Math.PI / 180;
    var h = Math.sin(dLat/2)**2 + Math.cos(la1)*Math.cos(la2)*Math.sin(dLon/2)**2;
    return Math.round(R * 2 * Math.atan2(Math.sqrt(h), Math.sqrt(1-h)));
  }

  // Viridis-ish gradient (5 stops). For dark mode, use TRIMMED magma so the
  // dark end is visible against Carto dark_all tiles (untrimmed magma starts
  // at #000004 which is invisible on a near-black tile).
  // Sampled from matplotlib magma at 0.15, 0.30, 0.45, 0.60, 0.75, 0.90, 1.00.
  var VIRIDIS = ['#440154', '#3b528b', '#21918c', '#5ec962', '#fde725'];
  var MAGMA   = ['#3b0f70', '#641a80', '#9c179e', '#cc4778', '#ed7953', '#fb9f3a', '#fcfdbf'];

  function isDark() {
    return document.documentElement.getAttribute('data-theme') === 'dark';
  }
  function rampColor(i, n) {
    var ramp = isDark() ? MAGMA : VIRIDIS;
    if (n <= 1) return ramp[ramp.length - 1];
    var t = i / (n - 1);
    var bucket = t * (ramp.length - 1);
    var lo = Math.floor(bucket), hi = Math.min(lo + 1, ramp.length - 1);
    var f = bucket - lo;
    function mix(c1, c2, f) {
      var r1 = parseInt(c1.slice(1,3),16), g1 = parseInt(c1.slice(3,5),16), b1 = parseInt(c1.slice(5,7),16);
      var r2 = parseInt(c2.slice(1,3),16), g2 = parseInt(c2.slice(3,5),16), b2 = parseInt(c2.slice(5,7),16);
      var r = Math.round(r1 + (r2-r1)*f), g = Math.round(g1 + (g2-g1)*f), b = Math.round(b1 + (b2-b1)*f);
      return '#' + ((1<<24)+(r<<16)+(g<<8)+b).toString(16).slice(1);
    }
    return mix(ramp[lo], ramp[hi], f);
  }

  function relativeTime(iso) {
    if (!iso) return '–';
    var t = new Date(iso).getTime();
    var d = Date.now() - t;
    if (d < 60000) return Math.round(d/1000) + 's ago';
    if (d < 3600000) return Math.round(d/60000) + 'm ago';
    if (d < 86400000) return Math.round(d/3600000) + 'h ago';
    return Math.round(d/86400000) + 'd ago';
  }

  function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c];
    });
  }

  function buildSnrSparkline(snrTrend) {
    if (!snrTrend || !snrTrend.length) return '<span class="mc-rt-detail-na">no SNR data</span>';
    var pts = snrTrend.filter(function (p) { return p && p.snr != null; });
    if (!pts.length) return '<span class="mc-rt-detail-na">no SNR data</span>';
    var W = 200, H = 28;
    var snrs = pts.map(function (p) { return p.snr; });
    var minS = Math.min.apply(null, snrs), maxS = Math.max.apply(null, snrs);
    if (maxS === minS) { minS -= 1; maxS += 1; }
    var poly = pts.map(function (p, i) {
      var x = (i / (pts.length - 1 || 1)) * W;
      var y = H - 2 - ((p.snr - minS) / (maxS - minS)) * (H - 4);
      return x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
    return '<svg class="mc-rt-detail-spark" width="' + W + '" height="' + H + '" viewBox="0 0 ' + W + ' ' + H + '" aria-label="SNR across route observations">' +
      '<polyline points="' + poly + '" fill="none" stroke="currentColor" stroke-width="1.2"/>' +
      '</svg>' +
      '<span class="mc-rt-detail-spark-meta">' + pts.length + ' obs · ' + minS.toFixed(1) + '..' + maxS.toFixed(1) + ' dB</span>';
  }

  var _detailCache = {};
  function fetchHopDetail(pubkey) {
    if (!pubkey) return Promise.resolve(null);
    var pk = String(pubkey).toLowerCase();
    if (_detailCache[pk]) return Promise.resolve(_detailCache[pk]);
    return Promise.all([
      fetch('/api/nodes/' + pk).then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; }),
      fetch('/api/nodes/' + pk + '/analytics?window=24h').then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; }),
      fetch('/api/nodes/' + pk + '/paths?limit=20').then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; }),
    ]).then(function (out) {
      var result = { detail: out[0], analytics: out[1], paths: out[2] };
      _detailCache[pk] = result;
      return result;
    });
  }

  function renderHopDetail(p, container) {
    container.innerHTML = '<div class="mc-rt-detail-loading">Loading hop info…</div>';
    fetchHopDetail(p.pubkey).then(function (data) {
      if (!data) {
        container.innerHTML = '<div class="mc-rt-detail-na">No data for this node</div>';
        return;
      }
      var node = (data.detail && data.detail.node) || {};
      var ana = data.analytics || {};
      var paths = data.paths || {};
      var pkShort = p.pubkey ? (String(p.pubkey).slice(0, 6) + '…' + String(p.pubkey).slice(-4)) : '?';
      var suspectedWarn = '';
      if (node.multi_byte_status && node.multi_byte_status !== 'confirmed') {
        suspectedWarn = '<span class="mc-rt-detail-warn">⚠ ' + escapeHtml(node.multi_byte_status) + '</span>';
      }
      var rel = node.last_seen ? relativeTime(node.last_seen) : '–';
      var snr = buildSnrSparkline(ana.snrTrend || []);
      var rx = node.relay_count_24h, total_tx = paths.totalTransmissions;
      var ratioHtml = (rx != null && total_tx != null && total_tx > 0)
        ? '<b>' + rx + '</b> relays / <b>' + total_tx + '</b> tx (24h)'
        : (rx != null ? '<b>' + rx + '</b> relays (24h)' : '<span class="mc-rt-detail-na">no relay data</span>');
      var routeCount = paths.totalPaths || (paths.paths ? paths.paths.length : 0);
      var alsoIn = routeCount > 1
        ? '<a class="mc-rt-detail-link" href="#/nodes/' + escapeHtml(node.public_key || p.pubkey) + '/analytics?tab=paths">also in ' + routeCount + ' routes →</a>'
        : '<span class="mc-rt-detail-na">unique to this route</span>';

      container.innerHTML =
        '<div class="mc-rt-detail">' +
          '<div class="mc-rt-detail-row1">' +
            '<span class="mc-rt-detail-name">' + escapeHtml(node.name || p.name || '?') + '</span>' +
            suspectedWarn +
            '<span class="mc-rt-detail-meta">' + rel + ' · ' + escapeHtml(node.role || p.role || '?') + ' · ' + pkShort + '</span>' +
          '</div>' +
          '<div class="mc-rt-detail-snr"><span class="mc-rt-detail-label">SNR</span>' + snr + '</div>' +
          '<div class="mc-rt-detail-relay"><span class="mc-rt-detail-label">activity</span>' + ratioHtml + '</div>' +
          '<div class="mc-rt-detail-also">' + alsoIn + '</div>' +
        '</div>';
    });
  }


  function buildMarkerSVG(p, opts) {
    // Origin: filled square 2x. Destination: filled triangle 2x.
    // Intermediates: 8px circle. Unresolved: dashed circle.
    var size = (p.isOrigin || p.isDest) ? 18 : 10;
    var color = opts.color;
    var stroke = opts.stroke || '#fff';
    var html = '<svg width="' + size + '" height="' + size + '" viewBox="0 0 ' + size + ' ' + size + '" aria-hidden="true">';
    if (p.isOrigin) {
      html += '<rect x="1" y="1" width="' + (size-2) + '" height="' + (size-2) + '" fill="' + color + '" stroke="' + stroke + '" stroke-width="1.5"/>';
    } else if (p.isDest) {
      var midX = size/2;
      html += '<polygon points="' + midX + ',1 ' + (size-1) + ',' + (size-1) + ' 1,' + (size-1) + '" fill="' + color + '" stroke="' + stroke + '" stroke-width="1.5"/>';
    } else if (p.resolved === false) {
      html += '<circle cx="' + (size/2) + '" cy="' + (size/2) + '" r="' + (size/2-1) + '" fill="none" stroke="' + color + '" stroke-width="1.5" stroke-dasharray="2 2"/>';
    } else {
      html += '<circle cx="' + (size/2) + '" cy="' + (size/2) + '" r="' + (size/2-1) + '" fill="' + color + '" stroke="' + stroke + '" stroke-width="1"/>';
    }
    html += '</svg>';
    return { html: html, size: size };
  }

  function roleGlyph(role) {
    return ({repeater:'●', companion:'■', room:'⬢', sensor:'▲', observer:'◆'})[role] || '○';
  }

  function buildSidebar(positions, mapRef, layer, edges, markers, opts) {
    opts = opts || {};
    var total = positions.length;
    // Compute hop distances
    var dists = [], maxDist = 0;
    for (var i = 1; i < total; i++) {
      var d = haversineKm(positions[i-1], positions[i]);
      dists.push(d);
      if (d != null && d > maxDist) maxDist = d;
    }
    // Sparkline header with inline title + max
    var maxDistRound = maxDist || 0;
    var sparkW = 280, sparkH = 36;
    var sparkSvg = '<svg class="mc-rt-spark" viewBox="0 0 ' + sparkW + ' ' + sparkH + '" width="' + sparkW + '" height="' + sparkH + '" aria-label="Hop distance per sequence">';
    var dotPositions = [];
    if (dists.length && maxDist > 0) {
      var pts = dists.map(function (d, idx) {
        var x = (idx / (dists.length - 1 || 1)) * sparkW;
        var y = sparkH - 2 - (d != null ? (d / maxDist) * (sparkH - 4) : 0);
        dotPositions.push({ x: x, y: y, idx: idx + 1, d: d });
        return x.toFixed(1) + ',' + y.toFixed(1);
      }).join(' ');
      sparkSvg += '<polyline points="' + pts + '" fill="none" stroke="var(--text-muted, #94a3b8)" stroke-width="1.5"/>';
      dotPositions.forEach(function (p) {
        sparkSvg += '<circle class="mc-rt-spark-dot" data-hop-idx="' + p.idx + '" data-dist="' + (p.d != null ? p.d : '') + '" cx="' + p.x.toFixed(1) + '" cy="' + p.y.toFixed(1) + '" r="2" fill="' + rampColor(p.idx - 1, dists.length) + '"/>';
      });
    }
    sparkSvg += '</svg>';
    var sparkTitle = '<div class="mc-rt-spark-title"><span>Hop distance</span><b>max ' + (maxDistRound || 0) + ' km</b></div>';
    var spark = sparkTitle + sparkSvg;

    // Build rows — stripe color = the edge ENTERING this hop (color of edge i-1).
    // Hop 0 has no incoming edge; use color of outgoing edge 0 as visual seed.
    var rows = positions.map(function (p, idx) {
      var dist = idx > 0 ? dists[idx - 1] : null;
      var distBar = '';
      if (dist != null && maxDist > 0) {
        var pct = Math.max(2, (dist / maxDist) * 100);
        distBar = '<div class="mc-rt-distbar" style="width:' + pct.toFixed(1) + '%;background:' + rampColor(idx - 1, dists.length) + '"></div>';
      }
      var distLabel = dist != null ? dist + ' km' : '–';
      var pinned = p.isOrigin ? 'origin' : (p.isDest ? 'dest' : '');
      var glyph = roleGlyph(p.role);
      var name = escapeHtml(p.name || (p.pubkey ? String(p.pubkey).slice(0,8) : '?'));
      // Show a status badge for unresolved hops:
      //  - gpsless: node identified but missing GPS → "📍 no GPS"
      //  - else:    couldn't resolve prefix       → "🔍 unknown"
      var statusBadge = '';
      if (p.resolved === false) {
        if (p.gpsless) {
          statusBadge = ' <span class="mc-rt-status-chip mc-rt-status-nogps" title="Node identified but has no GPS coordinates">📍 no GPS</span>';
        } else {
          statusBadge = ' <span class="mc-rt-status-chip mc-rt-status-unknown" title="Could not resolve this hop prefix to a known node">🔍 unknown</span>';
        }
      }
      var unresolved = p.resolved === false ? ' mc-rt-unresolved' : '';
      // Stripe color: incoming edge color (idx-1) for non-origin, outgoing for origin.
      var stripeIdx = idx === 0 ? 0 : (idx - 1);
      var stripeColor = total > 1 ? rampColor(stripeIdx, total - 1) : 'transparent';
      // Multi-path observer chip (passed via p.observerCount / p.observerTotal if available)
      var obsChip = '';
      if (p.observerCount != null && p.observerTotal != null && p.observerTotal > 1) {
        obsChip = '<span class="mc-rt-obs-chip" title="Observed by ' + p.observerCount + ' of ' + p.observerTotal + ' observers">' + p.observerCount + '/' + p.observerTotal + '</span>';
      }
      return '<li class="mc-rt-row ' + pinned + unresolved + '" data-hop-idx="' + idx + '" tabindex="0" style="--mc-rt-row-color:' + stripeColor + '">' +
        '<span class="mc-rt-stripe" aria-hidden="true"></span>' +
        '<span class="mc-rt-seq">' + (idx + 1) + '</span>' +
        '<span class="mc-rt-glyph" title="' + (p.role || 'unknown') + '">' + glyph + '</span>' +
        '<span class="mc-rt-name">' + name + obsChip + statusBadge + '</span>' +
        '<span class="mc-rt-distlabel">' + distLabel + '</span>' +
        '<div class="mc-rt-distbar-wrap">' + distBar + '</div>' +
        '</li>';
    });

    var totalKm = dists.filter(function(d){return d!=null}).reduce(function(a,b){return a+b},0);
    var unresolvedCount = positions.filter(function(p){return p.resolved===false}).length;
    var multiPath = (opts && opts.multiPath) === true;
    var totalObservers = (opts && opts.totalObservers) || 1;
    var uniquePathsCount = (opts && opts.allPaths) ? (function () {
      var seen = {};
      opts.allPaths.forEach(function (p) { seen[(p.path || []).join('-')] = true; });
      return Object.keys(seen).length;
    })() : 1;
    var multiPathChip = '';
    if (multiPath) {
      multiPathChip = '<div class="mc-rt-multipath-chip">' +
        '<b>' + totalObservers + '</b> observers · <b>' + uniquePathsCount + '</b> unique paths' +
        '</div>';
    }
    var headerHtml =
      '<div class="mc-rt-header">' +
        '<div class="mc-rt-title">Route</div>' +
        '<div class="mc-rt-meta">' + total + ' hops · ' + totalKm + ' km' +
          (unresolvedCount ? ' · ' + unresolvedCount + ' unresolved' : '') +
        '</div>' +
        multiPathChip +
        '<div class="mc-rt-spark-wrap">' + spark + '</div>' +
        '<button class="mc-rt-close" aria-label="Close route view" type="button">✕</button>' +
      '</div>';

    // origin row pinned at top, dest at bottom; middle scrollable
    var originRow = rows[0] || '';
    var destRow = rows[total - 1] || '';
    var middleRows = rows.slice(1, -1).join('');

    var bodyHtml =
      '<div class="mc-rt-pinned mc-rt-pinned-top">' + originRow + '</div>' +
      '<ul class="mc-rt-list" role="list">' + middleRows + '</ul>' +
      '<div class="mc-rt-pinned mc-rt-pinned-bottom">' + destRow + '</div>';

    var sidebar = document.createElement('aside');
    sidebar.className = 'mc-rt-sidebar';
    sidebar.setAttribute('role', 'region');
    sidebar.setAttribute('aria-label', 'Route timeline');
    sidebar.innerHTML = headerHtml + bodyHtml;

    // Wire hover/focus on rows
    var rowEls = sidebar.querySelectorAll('.mc-rt-row');
    function highlightHop(idx, on) {
      var mk = markers[idx];
      if (mk && mk._icon) mk._icon.classList.toggle('mc-rt-hover', on);
      // edges around this hop
      if (idx > 0 && edges[idx-1]) edges[idx-1].setStyle({ weight: on ? 6 : 3.5, opacity: on ? 1 : 0.85 });
      if (idx < edges.length && edges[idx]) edges[idx].setStyle({ weight: on ? 6 : 3.5, opacity: on ? 1 : 0.85 });
    }
    function scrollRowIntoView(idx) {
      var row = sidebar.querySelector('.mc-rt-row[data-hop-idx="' + idx + '"]');
      if (!row) return;
      row.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      rowEls.forEach(function (r) { r.classList.remove('mc-rt-row-active'); });
      row.classList.add('mc-rt-row-active');
      setTimeout(function () { row.classList.remove('mc-rt-row-active'); }, 1500);
    }
    // Expose so marker-click can call it from render()
    sidebar._highlightHop = highlightHop;
    sidebar._scrollRowIntoView = scrollRowIntoView;
    rowEls.forEach(function (row) {
      var idx = parseInt(row.dataset.hopIdx, 10);
      row.addEventListener('mouseenter', function () { highlightHop(idx, true); });
      row.addEventListener('mouseleave', function () { highlightHop(idx, false); });
      row.addEventListener('focus', function () { highlightHop(idx, true); });
      row.addEventListener('blur', function () { highlightHop(idx, false); });
      row.addEventListener('click', function (e) {
        e.stopPropagation();
        e.preventDefault();
        // Toggle drill-in panel (Tufte expanding row pattern)
        var existing = row.querySelector('.mc-rt-detail-panel');
        if (existing) {
          existing.remove();
          row.classList.remove('mc-rt-row-expanded');
          return;
        }
        // Close any other open panels first
        sidebar.querySelectorAll('.mc-rt-detail-panel').forEach(function (el) { el.remove(); });
        sidebar.querySelectorAll('.mc-rt-row-expanded').forEach(function (el) { el.classList.remove('mc-rt-row-expanded'); });
        var panel = document.createElement('div');
        panel.className = 'mc-rt-detail-panel';
        row.appendChild(panel);
        row.classList.add('mc-rt-row-expanded');
        renderHopDetail(positions[idx], panel);
        // Also fly map to the hop
        var p = positions[idx];
        if (p.lat != null && p.lon != null) mapRef.flyTo([p.lat, p.lon], 13, { duration: 0.6 });
      });
    });

    // Sparkline interactivity
    var sparkDots = sidebar.querySelectorAll('.mc-rt-spark-dot');
    var tipEl = null;
    function showTip(evt, text) {
      if (!tipEl) {
        tipEl = document.createElement('div');
        tipEl.className = 'mc-rt-spark-tooltip';
        document.body.appendChild(tipEl);
      }
      tipEl.textContent = text;
      tipEl.style.left = (evt.clientX + 10) + 'px';
      tipEl.style.top = (evt.clientY - 24) + 'px';
      tipEl.style.display = 'block';
    }
    function hideTip() { if (tipEl) tipEl.style.display = 'none'; }
    sparkDots.forEach(function (dot) {
      var hopIdx = parseInt(dot.dataset.hopIdx, 10); // 1-based (hop N from prev)
      var dist = dot.dataset.dist;
      dot.addEventListener('mouseenter', function (e) {
        showTip(e, 'hop ' + (hopIdx + 1) + ' · ' + (dist ? dist + ' km from prev' : '–'));
        highlightHop(hopIdx, true);
      });
      dot.addEventListener('mousemove', function (e) { if (tipEl && tipEl.style.display !== 'none') { tipEl.style.left = (e.clientX + 10) + 'px'; tipEl.style.top = (e.clientY - 24) + 'px'; } });
      dot.addEventListener('mouseleave', function () { hideTip(); highlightHop(hopIdx, false); });
      dot.addEventListener('click', function () {
        var p = positions[hopIdx];
        if (p && p.lat != null) mapRef.flyTo([p.lat, p.lon], 13, { duration: 0.6 });
        scrollRowIntoView(hopIdx);
      });
    });

    // Close handler
    var closeBtn = sidebar.querySelector('.mc-rt-close');
    if (closeBtn) {
      closeBtn.addEventListener('click', function () {
        try { sessionStorage.removeItem('map-route-hops'); } catch (e) {}
        sidebar.remove();
        // Remove route layer + restore Map Controls
        if (layer && mapRef.removeLayer) mapRef.removeLayer(layer);
        document.body.classList.remove('mc-route-active');
        location.hash = '#/map';
      });
    }

    return sidebar;
  }

  function render(mapRef, layer, positions, opts) {
    if (!positions || !positions.length) return;
    opts = opts || {};
    var total = positions.length;

    // Mark origin/destination
    positions.forEach(function (p, i) {
      p.isOrigin = (i === 0);
      p.isDest = (i === total - 1);
    });

    document.body.classList.add('mc-route-active');

    // Edges. If a hop is unresolved (no lat/lon), bridge across it by drawing
    // a dashed line from the previous resolved hop to the next resolved hop —
    // otherwise the route appears truncated everywhere an intermediate is
    // unresolved. Each unresolved bridge gets the average position visually so
    // the path remains continuous.
    //
    // #1418 Phase C: multi-path mode. opts.edgeCounts maps "AB→60" to count
    // of observers that saw that edge. Stroke width scales with count
    // (consensus = thick, lone-witness = hairline).
    var edges = [];
    var multiPath = opts.multiPath === true;
    var edgeCounts = opts.edgeCounts || {};
    var totalObservers = opts.totalObservers || 1;
    function resolveCoord(idx) {
      if (positions[idx].lat != null && positions[idx].lon != null) {
        return { lat: positions[idx].lat, lon: positions[idx].lon, resolved: true };
      }
      var l = idx - 1, r = idx + 1;
      while (l >= 0 && positions[l].lat == null) l--;
      while (r < total && positions[r].lat == null) r++;
      var lp = l >= 0 ? positions[l] : null;
      var rp = r < total ? positions[r] : null;
      if (lp && rp) return { lat: (lp.lat + rp.lat)/2, lon: (lp.lon + rp.lon)/2, resolved: false };
      if (lp) return { lat: lp.lat, lon: lp.lon, resolved: false };
      if (rp) return { lat: rp.lat, lon: rp.lon, resolved: false };
      return null;
    }
    function edgeWeight(idx) {
      if (!multiPath) return 3.5;
      // Map sequence-edge index to canonical-path edge key.
      // Edges are between positions[i] and positions[i+1]; if origin was
      // prepended, sequence-edges count from 0 = origin→hop1, 1 = hop1→hop2.
      // The edgeCounts keys are based on canonical-path hops (no origin).
      // For now, scale by (count / totalObservers) → range 1.5 to 5.5.
      var fromKey = positions[idx].pubkey;
      var toKey = positions[idx + 1] && positions[idx + 1].pubkey;
      if (!fromKey || !toKey) return 3.5;
      // edgeCounts is keyed on the SHORT prefix (e.g. "AB"), but here we have
      // full pubkeys. Match by prefix.
      var matchCount = 0;
      var fromPrefix = String(fromKey).slice(0, 2).toUpperCase();
      var toPrefix = String(toKey).slice(0, 2).toUpperCase();
      // Try exact prefix-pair lookup; the canonical edgeCounts uses the
      // hopKey strings exactly as they appeared in the original paths.
      Object.keys(edgeCounts).forEach(function (k) {
        var parts = k.split('\u2192');
        if (parts.length !== 2) return;
        var a = parts[0].toUpperCase(), b = parts[1].toUpperCase();
        if ((a === fromPrefix || fromPrefix.startsWith(a)) &&
            (b === toPrefix || toPrefix.startsWith(b))) {
          matchCount += edgeCounts[k];
        }
      });
      if (matchCount === 0) return 2;
      var ratio = matchCount / totalObservers;
      return 1.5 + ratio * 4.5; // 1.5..6
    }
    for (var i = 0; i < total - 1; i++) {
      var ca = resolveCoord(i), cb = resolveCoord(i + 1);
      if (!ca || !cb) { edges.push(null); continue; }
      var color = rampColor(i, total - 1);
      var unresolvedEdge = !ca.resolved || !cb.resolved;
      var w = edgeWeight(i);
      var poly = L.polyline([[ca.lat, ca.lon], [cb.lat, cb.lon]], {
        color: color, weight: w, opacity: unresolvedEdge ? 0.5 : 0.85,
        dashArray: unresolvedEdge ? '6 4' : null,
        className: 'mc-rt-edge'
      }).addTo(layer);
      edges.push(poly);
    }

    // Markers (no chips, no labels, no arrows)
    var markers = positions.map(function (p, i) {
      if (p.lat == null || p.lon == null) return null;
      var color = (window.ROLE_COLORS && window.ROLE_COLORS[p.role]) || '#3b82f6';
      var built = buildMarkerSVG(p, { color: color });
      var html = '<div class="mc-rt-marker" data-hop-idx="' + i + '" tabindex="0" aria-label="Hop ' + (i+1) + ' of ' + total + ', ' + escapeHtml(p.name || '?') + '">' + built.html + '</div>';
      var icon = L.divIcon({
        html: html, className: 'mc-rt-marker-icon',
        iconSize: [built.size + 4, built.size + 4],
        iconAnchor: [(built.size + 4)/2, (built.size + 4)/2]
      });
      var mk = L.marker([p.lat, p.lon], { icon: icon }).addTo(layer);
      return mk;
    });

    // Sidebar
    var prevSidebar = document.querySelector('.mc-rt-sidebar');
    if (prevSidebar) prevSidebar.remove();
    var sidebar = buildSidebar(positions, mapRef, layer, edges, markers, opts);
    var mapContainer = document.querySelector('#leaflet-map');
    if (mapContainer && mapContainer.parentElement) {
      mapContainer.parentElement.insertBefore(sidebar, mapContainer);
    } else {
      document.body.appendChild(sidebar);
    }

    // Wire marker → sidebar (after sidebar exists). Click marker = scroll sidebar
    // to corresponding row + highlight. Hover marker = tooltip (Leaflet popup
    // already exists from .bindPopup, we add a click handler too).
    markers.forEach(function (mk, idx) {
      if (!mk) return;
      mk.on('click', function () {
        if (sidebar._scrollRowIntoView) sidebar._scrollRowIntoView(idx);
        // Trigger row click to open detail panel
        var row = sidebar.querySelector('.mc-rt-row[data-hop-idx="' + idx + '"]');
        if (row && !row.querySelector('.mc-rt-detail-panel')) {
          row.click();
        }
      });
      // Hover tooltip — Leaflet's built-in
      var p = positions[idx];
      var dist = idx > 0 ? (function () {
        var a = positions[idx-1], b = positions[idx];
        if (a.lat == null || b.lat == null) return null;
        return haversineKm(a, b);
      })() : null;
      var tipText = 'hop ' + (idx + 1) + ' · ' + (p.name || '?') + (dist != null ? ' · ' + dist + ' km from prev' : '');
      mk.bindTooltip(tipText, { direction: 'top', offset: [0, -10] });
    });

    // Fit bounds
    if (positions.some(function(p){return p.lat!=null})) {
      var bounds = L.latLngBounds(positions.filter(function(p){return p.lat!=null}).map(function(p){return [p.lat, p.lon]}));
      mapRef.fitBounds(bounds, { padding: [40, 40] });
    }
  }

  window.MeshRouteTufte = { render: render };
})();
