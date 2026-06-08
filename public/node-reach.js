/* === CoreScope — node-reach.js ===
   Standalone per-node "Reach" page: importance stats + a map of the node's
   bidirectional RF links + a link table. Registered as page 'node-reach'
   (route #/nodes/<pubkey>/reach), mirroring node-analytics.js. */
'use strict';
(function () {
  var qmap = null;       // map controller from NodeReachMap.render (built once per load)
  var current = null;
  var loadGen = 0;       // bumped per load + on destroy; guards against in-flight races

  // Single source of the bottleneck tiers: colour + threshold + colour-blind
  // glyph/label. The map legend and the table both read from this.
  var TIERS = [
    { min: 300, label: 'strong', varName: '--link-strong', glyph: '●●●' },
    { min: 100, label: 'medium', varName: '--link-medium', glyph: '●●' },
    { min: 0, label: 'weak', varName: '--link-weak', glyph: '●' }
  ];
  function tierOf(b) {
    for (var i = 0; i < TIERS.length; i++) {
      if (b >= TIERS[i].min) return TIERS[i];
    }
    return TIERS[TIERS.length - 1];
  }

  function statCard(label, value, descShort, descFull) {
    return '<div class="analytics-stat-card" title="' + escapeHtml(descFull) + '">' +
      '<div class="analytics-stat-label">' + escapeHtml(label) + '</div>' +
      '<div class="analytics-stat-value">' + escapeHtml(String(value)) + '</div>' +
      '<div class="analytics-stat-desc">' + escapeHtml(descShort) + '</div></div>';
  }

  function linkRow(i, l) {
    var dist = l.distance_km != null ? Number(l.distance_km).toFixed(1) : '—';
    var dir = l.bidir ? '' : (l.we_hear > 0 ? 'incoming' : 'outgoing');
    var href = '#/nodes/' + encodeURIComponent(l.pubkey);
    var t = tierOf(l.bottleneck);
    return '<tr' + (l.bidir ? '' : ' class="nq-oneway"') + '>' +
      '<td class="nq-num">' + i + '</td>' +
      '<td><a href="' + href + '" class="nq-link">' + escapeHtml(l.name || l.pubkey.slice(0, 8)) + '</a>' +
      (dir ? ' <span class="nq-dir">' + dir + '</span>' : '') + '</td>' +
      '<td class="nq-n">' + l.we_hear + '</td>' +
      '<td class="nq-n">' + l.they_hear + '</td>' +
      '<td class="nq-n" title="' + t.label + '"><span class="nq-tier" style="color:var(' + t.varName + ')">' + l.bottleneck +
      '</span><span class="nq-tier-glyph" style="color:var(' + t.varName + ')">' + t.glyph + '</span></td>' +
      '<td class="nq-n">' + dist + '</td></tr>';
  }

  function dayBtn(d, cur, label) {
    var on = d === cur;
    return '<button data-days="' + d + '" aria-pressed="' + (on ? 'true' : 'false') + '"' +
      (on ? ' class="active"' : '') + '>' + label + '</button>';
  }

  function headerHtml(n, nodeName, days) {
    return '<div class="nq-head">' +
      '<a class="nq-back" href="#/nodes/' + encodeURIComponent(n.pubkey) + '" aria-label="Back to ' + nodeName + ' detail">← Back to ' + nodeName + '</a>' +
      '<h2 class="nq-title">' + nodeName + ' — Reach</h2>' +
      '<div class="nq-sub">' + escapeHtml(n.role || 'Unknown role') + ' · two-way RF link reach</div>' +
      '<div class="nq-note">Reliable by design: built only from unique <b>2–3 byte</b> path-hash (multibyte) matches. 1-byte hops collide between nodes and are excluded, so the links shown are trustworthy.</div>' +
      '<div class="analytics-time-range nq-noprint" id="nqDays" style="margin-top:8px">' +
      dayBtn(1, days, '24h') + dayBtn(7, days, '7d') + dayBtn(14, days, '14d') + dayBtn(30, days, '30d') +
      '</div></div>';
  }

  function wireTimeRange(container, pubkey) {
    var bar = container.querySelector('#nqDays');
    if (!bar) return;
    bar.addEventListener('click', function (e) {
      var b = e.target.closest('button[data-days]');
      if (b) load(container, pubkey, parseInt(b.getAttribute('data-days'), 10), false);
    });
  }

  function printReport() {
    // Leaflet only renders tiles for the on-screen size; on a wide screen the
    // printed page is narrower and the right half is clipped. Resize the map to
    // the print width (single source: --nq-print-width) and invalidate first.
    var mapEl = document.getElementById('nqMap');
    if (qmap && qmap.map && mapEl) {
      var pw = getComputedStyle(document.documentElement).getPropertyValue('--nq-print-width').trim() || '680px';
      mapEl.style.width = pw;
      qmap.map.invalidateSize();
      try { qmap.map.fitBounds(qmap.bounds, { padding: [20, 20] }); } catch (e) {}
      setTimeout(function () {
        window.print();
        mapEl.style.width = '';
        qmap.map.invalidateSize();
        try { qmap.map.fitBounds(qmap.bounds, { padding: [30, 30] }); } catch (e) {}
      }, 350);
    } else {
      window.print();
    }
  }

  // isInitial=true shows the centred loader (first paint); time-range changes
  // pass false so the current report stays on screen until the swap (no flash).
  async function load(container, pubkey, days, isInitial) {
    var myGen = ++loadGen;
    current = { pubkey: pubkey, days: days };
    if (qmap) { qmap.destroy(); qmap = null; }
    if (isInitial) {
      container.innerHTML = '<div class="nq-load">Loading reach…</div>';
    }

    var d;
    try {
      d = await api('/nodes/' + encodeURIComponent(pubkey) + '/reach?days=' + days, { ttl: 30000 });
    } catch (e) {
      if (myGen !== loadGen) return; // superseded or destroyed mid-flight
      container.innerHTML = '<div id="nq-report"><div class="nq-error">Failed to load reach: ' + escapeHtml(e.message) + '</div></div>';
      return;
    }
    if (myGen !== loadGen) return; // a newer load (or destroy) won the race
    current.data = d;
    var n = d.node;
    var nodeName = escapeHtml(n.name || n.pubkey.slice(0, 12));
    var imp = d.importance || {};
    var twoWay = d.links.filter(function (l) { return l.bidir; });

    if (!d.reliable_tokens || d.reliable_tokens.length === 0) {
      // nodeName is already escaped; build then assign (keeps it off the
      // innerHTML line for the XSS-sink gate, like statsHtml below).
      var emptyHtml = '<div id="nq-report">' + headerHtml(n, nodeName, days) +
        '<div class="nq-msg">This node has no unique 1–3 byte prefix, so it cannot be reliably identified in paths — no link data available.</div></div>';
      container.innerHTML = emptyHtml;
      wireTimeRange(container, pubkey);
      return;
    }

    var statsHtml = headerHtml(n, nodeName, days) +
      '<div class="nq-body">' +
      '<div class="nq-group-h">Network position (all-time)</div>' +
      '<div class="analytics-stats">' +
      statCard('Neighbours', imp.neighbor_degree, 'All-time distinct neighbours',
        'Distinct neighbours in the all-time neighbour graph (advert first-hop + observer last-hop, geo-filtered).') +
      statCard('Rank', '#' + imp.degree_rank + ' / ' + imp.nodes_with_edges, 'Rank by neighbour count',
        'Rank by neighbour count among all nodes with edges. #1 = most-connected node in the network.') +
      '</div>' +
      '<div class="nq-group-h">Last ' + d.window.days + ' days</div>' +
      '<div class="analytics-stats">' +
      statCard('Links', d.links.length, 'Neighbours seen this window',
        'Distinct direct neighbours seen in paths this window (any direction).') +
      statCard('Two-way', imp.bidirectional_links, 'Heard both directions',
        'Neighbours heard in BOTH directions — stable links. Counts mid-path adjacency, so can exceed all-time Neighbours.') +
      statCard('Relay obs', imp.relay_observations, 'Times seen in a path',
        'Observations where this node appears anywhere in the path (its relay throughput).') +
      statCard('Direct observers', imp.direct_observers, 'Heard it at 0 hops',
        'Stations that received this node directly, at 0 hops.') +
      '</div>';

    // Identifiable, but no path adjacency observed in this window.
    if (!d.links.length) {
      container.innerHTML = '<div id="nq-report">' + statsHtml +
        '<div class="nq-empty">No observed RF links in the last ' + d.window.days +
        ' days — this node advertises but hasn’t been seen relaying traffic (or no observers captured it). Try a longer window.</div>' +
        '</div></div>';
      wireTimeRange(container, pubkey);
      return;
    }

    container.innerHTML = '<div id="nq-report">' + statsHtml +
      '<div class="nq-actions nq-noprint">' +
      '<fieldset class="nq-filter"><legend>Show one-way links</legend>' +
      '<label for="nqIncoming"><input type="checkbox" id="nqIncoming"> incoming <span class="nq-dir">(we hear them)</span></label>' +
      '<label for="nqOutgoing"><input type="checkbox" id="nqOutgoing"> outgoing <span class="nq-dir">(they hear us)</span></label>' +
      '</fieldset>' +
      '<span id="nqCount" class="nq-count" aria-live="polite"></span>' +
      '<button id="nqPrintBtn" class="btn-primary nq-print-btn">Print / PDF</button>' +
      '</div>' +
      '<div id="nqNoGps" class="nq-nogps"></div>' +
      '<div id="nqMap" class="nq-map"></div>' +
      '<table class="nq-table"><thead><tr><th>#</th><th>Neighbour</th><th>we hear</th>' +
      '<th>they hear us</th><th title="smaller of we-hear / they-hear — gates two-way stability">bottleneck</th>' +
      '<th>distance (km)</th></tr></thead><tbody id="nqRows"></tbody></table>' +
      '</div></div>';

    // Build the map ONCE; toggles update the link layer in place (no flicker).
    if (window.NodeReachMap && n.lat != null) {
      qmap = window.NodeReachMap.render('nqMap', n, TIERS);
    }

    // Two-way links are always shown; the two checkboxes add the asymmetric ones.
    function paint() {
      var inc = document.getElementById('nqIncoming').checked;
      var out = document.getElementById('nqOutgoing').checked;
      var list = d.links.filter(function (l) {
        if (l.bidir) return true;
        var weOnly = l.we_hear > 0 && l.they_hear === 0;
        return (inc && weOnly) || (out && !weOnly);
      }).sort(function (a, b) {
        return (b.bidir - a.bidir) || (b.bottleneck - a.bottleneck) ||
          ((b.we_hear + b.they_hear) - (a.we_hear + a.they_hear));
      });
      document.getElementById('nqRows').innerHTML = list.map(function (l, i) { return linkRow(i + 1, l); }).join('');
      document.getElementById('nqCount').textContent =
        'showing ' + list.length + ' of ' + d.links.length + ' (' + twoWay.length + ' two-way)';
      var noGps = list.filter(function (l) { return l.lat == null || l.lon == null; }).length;
      document.getElementById('nqNoGps').textContent =
        noGps ? noGps + ' link' + (noGps === 1 ? '' : 's') + ' have no location and are not drawn on the map.' : '';
      if (qmap) qmap.setLinks(list);
    }
    paint();
    document.getElementById('nqIncoming').addEventListener('change', paint);
    document.getElementById('nqOutgoing').addEventListener('change', paint);
    document.getElementById('nqPrintBtn').addEventListener('click', printReport);

    wireTimeRange(container, pubkey);
  }

  function init(container, routeParam) {
    if (!routeParam || !routeParam.endsWith('/reach')) {
      container.innerHTML = '<div class="nq-load">Invalid reach URL</div>';
      return;
    }
    load(container, routeParam.slice(0, -'/reach'.length), 7, true);
  }

  function destroy() {
    loadGen++; // invalidate any in-flight load so it won't mutate a foreign container
    if (qmap) { qmap.destroy(); qmap = null; }
    current = null;
  }

  registerPage('node-reach', { init: init, destroy: destroy });
})();
