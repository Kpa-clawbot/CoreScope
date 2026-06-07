/* === CoreScope — node-reach.js ===
   Standalone per-node "Reach" page: importance stats + a map of the node's
   bidirectional RF links + a link table. Registered as page 'node-reach'
   (route #/nodes/<pubkey>/reach), mirroring node-analytics.js. */
'use strict';
(function () {
  var qmap = null;
  var current = null;

  function colorVar(b) {
    if (b >= 300) return 'var(--link-strong)';
    if (b >= 100) return 'var(--link-medium)';
    return 'var(--link-weak)';
  }

  function statCard(label, value, desc) {
    return '<div class="analytics-stat-card"><div class="analytics-stat-label">' + label +
      '</div><div class="analytics-stat-value">' + value +
      '</div><div class="analytics-stat-desc">' + desc + '</div></div>';
  }

  function linkRow(i, l) {
    var dist = l.distance_km != null ? Number(l.distance_km).toFixed(1) : '—';
    var dir = l.bidir ? '' : (l.we_hear > 0 ? 'incoming' : 'outgoing');
    var href = '#/nodes/' + encodeURIComponent(l.pubkey);
    return '<tr' + (l.bidir ? '' : ' class="nq-oneway"') + '>' +
      '<td class="nq-num">' + i + '</td>' +
      '<td><a href="' + href + '" class="nq-link">' + escapeHtml(l.name || l.pubkey.slice(0, 8)) + '</a>' +
      (dir ? ' <span class="nq-dir">' + dir + '</span>' : '') + '</td>' +
      '<td class="nq-n">' + l.we_hear + '</td>' +
      '<td class="nq-n">' + l.they_hear + '</td>' +
      '<td class="nq-n" style="color:' + colorVar(l.bottleneck) + '"><b>' + l.bottleneck + '</b></td>' +
      '<td class="nq-n">' + dist + '</td></tr>';
  }

  function dayBtn(d, cur, label) {
    return '<button data-days="' + d + '"' + (d === cur ? ' class="active"' : '') + '>' + label + '</button>';
  }

  function headerHtml(n, nodeName, days) {
    return '<div class="nq-head" style="max-width:1000px;margin:0 auto;padding:12px 16px">' +
      '<a href="#/nodes/' + encodeURIComponent(n.pubkey) + '" style="color:var(--accent);text-decoration:none;font-size:12px">← Back to ' + nodeName + '</a>' +
      '<h2 style="margin:4px 0 2px;font-size:18px">📡 ' + nodeName + ' — Reach</h2>' +
      '<div style="color:var(--text-muted);font-size:11px">' + escapeHtml(n.role || 'Unknown role') + ' · two-way RF link reach</div>' +
      '<div class="nq-note">ℹ️ Reliable by design: built only from unique <b>2–3 byte</b> path-hash (multibyte) matches. 1-byte hops collide between nodes and are excluded, so the links shown are trustworthy.</div>' +
      '<div class="analytics-time-range nq-noprint" id="nqDays" style="margin-top:8px">' +
      dayBtn(1, days, '24h') + dayBtn(7, days, '7d') + dayBtn(14, days, '14d') + dayBtn(30, days, '30d') +
      '</div></div>';
  }

  function wireTimeRange(container, pubkey) {
    var bar = container.querySelector('#nqDays');
    if (!bar) return;
    bar.addEventListener('click', function (e) {
      var b = e.target.closest('button[data-days]');
      if (b) load(container, pubkey, parseInt(b.getAttribute('data-days'), 10));
    });
  }

  function printReport() {
    // Leaflet only renders tiles for the on-screen size; on a wide screen the
    // printed page is narrower and the right half is clipped. Resize the map to
    // a print-safe width and invalidate before printing.
    var mapEl = document.getElementById('nqMap');
    if (qmap && mapEl) {
      mapEl.style.width = '680px';
      qmap.invalidateSize();
      try { qmap.fitBounds(qmap._nqBounds, { padding: [20, 20] }); } catch (e) {}
      setTimeout(function () {
        window.print();
        mapEl.style.width = '';
        qmap.invalidateSize();
        try { qmap.fitBounds(qmap._nqBounds, { padding: [30, 30] }); } catch (e) {}
      }, 350);
    } else {
      window.print();
    }
  }

  async function load(container, pubkey, days) {
    current = { pubkey: pubkey, days: days };
    if (qmap) { try { qmap.remove(); } catch (e) {} qmap = null; }
    container.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-muted)">Loading reach…</div>';

    var d;
    try {
      d = await api('/nodes/' + encodeURIComponent(pubkey) + '/reach?days=' + days, { ttl: 30000 });
    } catch (e) {
      container.innerHTML = '<div style="padding:40px;text-align:center;color:#ff6b6b">Failed to load reach: ' + escapeHtml(e.message) + '</div>';
      return;
    }
    current.data = d;
    var n = d.node;
    var nodeName = escapeHtml(n.name || n.pubkey.slice(0, 12));
    var imp = d.importance || {};
    var twoWay = d.links.filter(function (l) { return l.bidir; });

    if (!d.reliable_tokens || d.reliable_tokens.length === 0) {
      container.innerHTML = headerHtml(n, nodeName, days) +
        '<div style="max-width:1000px;margin:0 auto;padding:0 16px;color:var(--text-muted)">This node has no unique 1–3 byte prefix, so it cannot be reliably identified in paths — no link data available.</div>';
      wireTimeRange(container, pubkey);
      return;
    }

    var statsHtml = headerHtml(n, nodeName, days) +
      '<div class="nq-body" style="max-width:1000px;margin:0 auto;padding:0 16px">' +
      '<div class="nq-group-h">Network position (all-time)</div>' +
      '<div class="analytics-stats">' +
      statCard('Neighbours', imp.neighbor_degree, 'Distinct neighbours in the all-time neighbour graph (built only from advert first-hop + observer last-hop, geo-filtered).') +
      statCard('Rank', '#' + imp.degree_rank + ' / ' + imp.nodes_with_edges, 'Rank by neighbour count among all nodes that have edges. #1 = the most-connected node in the network.') +
      '</div>' +
      '<div class="nq-group-h">Last ' + d.window.days + ' days</div>' +
      '<div class="analytics-stats">' +
      statCard('Links', d.links.length, 'Distinct direct neighbours seen in paths this window (any direction).') +
      statCard('Two-way', imp.bidirectional_links, 'Neighbours heard in BOTH directions — stable links. Counts mid-path adjacency too, so this can exceed the all-time "Neighbours".') +
      statCard('Relay obs', imp.relay_observations, 'Observations where this node appears anywhere in the path (its relay throughput).') +
      statCard('Direct observers', imp.direct_observers, 'Stations that received this node directly, at 0 hops.') +
      '</div>';

    // Identifiable, but no path adjacency observed in this window → explain rather
    // than showing an empty map + table.
    if (!d.links.length) {
      container.innerHTML = statsHtml +
        '<div class="nq-empty">No observed RF links in the last ' + d.window.days +
        ' days — this node advertises but hasn’t been seen relaying traffic (or no observers captured it). Try a longer window.</div>' +
        '</div>';
      wireTimeRange(container, pubkey);
      return;
    }

    container.innerHTML = statsHtml +
      '<div class="nq-actions nq-noprint">' +
      '<label><input type="checkbox" id="nqIncoming"> incoming <span class="nq-dir">(we hear them)</span></label>' +
      '<label><input type="checkbox" id="nqOutgoing"> outgoing <span class="nq-dir">(they hear us)</span></label>' +
      '<span id="nqCount" class="nq-count"></span>' +
      '<button id="nqPrintBtn" class="btn-primary" style="margin-left:auto">🖨️ Print / PDF</button>' +
      '</div>' +
      '<div id="nqMap" class="nq-map"></div>' +
      '<table class="nq-table"><thead><tr><th>#</th><th>Neighbour</th><th>we hear</th>' +
      '<th>they hear us</th><th>bottleneck</th><th>km</th></tr></thead><tbody id="nqRows"></tbody></table>' +
      '</div>';

    function renderMap(list) {
      if (qmap) { try { qmap.remove(); } catch (e) {} qmap = null; }
      if (window.NodeReachMap && n.lat != null) {
        qmap = window.NodeReachMap.render('nqMap', n, list, colorVar);
      }
    }

    // Two-way links are always shown; the two checkboxes add the asymmetric ones.
    // incoming = we hear them only; outgoing = they hear us only.
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
      // Keep the map in sync with the table.
      renderMap(list);
    }
    paint();
    document.getElementById('nqIncoming').addEventListener('change', paint);
    document.getElementById('nqOutgoing').addEventListener('change', paint);
    document.getElementById('nqPrintBtn').addEventListener('click', printReport);

    wireTimeRange(container, pubkey);
  }

  function init(container, routeParam) {
    if (!routeParam || !routeParam.endsWith('/reach')) {
      container.innerHTML = '<div style="padding:40px;text-align:center">Invalid reach URL</div>';
      return;
    }
    load(container, routeParam.slice(0, -'/reach'.length), 7);
  }

  function destroy() {
    if (qmap) { try { qmap.remove(); } catch (e) {} qmap = null; }
    current = null;
  }

  registerPage('node-reach', { init: init, destroy: destroy });
})();
