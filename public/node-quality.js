/* Node quality report section — fetches /api/nodes/:pk/quality and renders
   importance cards + a Leaflet link-map + a 2-way link table. IIFE, exposes
   window.NodeQuality.render(pubkey). */
(function () {
  'use strict';

  function colorVar(b) {
    if (b >= 300) return 'var(--link-strong)';
    if (b >= 100) return 'var(--link-medium)';
    return 'var(--link-weak)';
  }

  function statCard(label, value) {
    return '<div class="nq-stat"><div class="nq-stat-v">' + value +
      '</div><div class="nq-stat-k">' + label + '</div></div>';
  }

  function row(i, l) {
    var dist = l.distance_km != null ? Number(l.distance_km).toFixed(1) : '—';
    return '<tr data-bidir="' + (l.bidir ? '1' : '0') + '">' +
      '<td class="nq-num">' + i + '</td>' +
      '<td>' + escapeHtml(l.name || l.pubkey.slice(0, 8)) + '</td>' +
      '<td class="nq-n">' + l.we_hear + '</td>' +
      '<td class="nq-n">' + l.they_hear + '</td>' +
      '<td class="nq-n" style="color:' + colorVar(l.bottleneck) + '"><b>' + l.bottleneck + '</b></td>' +
      '<td class="nq-n">' + dist + '</td></tr>';
  }

  function render(pubkey) {
    var el = document.getElementById('nodeQualityContent');
    if (!el) return;
    fetch('/api/nodes/' + encodeURIComponent(pubkey) + '/quality?days=7')
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (!d || !d.reliable_tokens) { el.innerHTML = '<div class="text-muted" style="padding:8px">Geen kwaliteitsdata.</div>'; return; }
        if (d.reliable_tokens.length === 0) {
          el.innerHTML = '<div class="text-muted" style="padding:8px">Node is niet betrouwbaar identificeerbaar in paden (alleen 1-byte prefix — botst).</div>';
          return;
        }
        var imp = d.importance || {};
        var twoWay = d.links.filter(function (l) { return l.bidir; });
        var html =
          '<div class="nq-stats">' +
          statCard('buren', imp.neighbor_degree) +
          statCard('rang', '#' + imp.degree_rank + '/' + imp.nodes_with_edges) +
          statCard('relay-obs', imp.relay_observations) +
          statCard('2-weg links', imp.bidirectional_links) +
          statCard('observers', imp.direct_observers) +
          '</div>' +
          '<div class="nq-actions">' +
          '<label><input type="checkbox" id="nqShowOneWay"> toon één-weg-links</label>' +
          '<button id="nqPrintBtn" class="btn">🖨️ Print / PDF</button></div>' +
          '<div id="nqMap" class="nq-map"></div>' +
          '<table class="nq-table"><thead><tr><th>#</th><th>Buur</th><th>wij horen</th>' +
          '<th>zij horen ons</th><th>bottleneck</th><th>km</th></tr></thead><tbody id="nqRows"></tbody></table>';
        el.innerHTML = html;

        function paint(showOneWay) {
          var list = (showOneWay ? d.links : twoWay).slice()
            .sort(function (a, b) { return b.bottleneck - a.bottleneck; });
          document.getElementById('nqRows').innerHTML =
            list.map(function (l, i) { return row(i + 1, l); }).join('');
        }
        paint(false);
        document.getElementById('nqShowOneWay').addEventListener('change', function (e) { paint(e.target.checked); });
        document.getElementById('nqPrintBtn').addEventListener('click', function () { window.print(); });

        if (window.NodeQualityMap && d.node.lat != null) {
          window.NodeQualityMap.render('nqMap', d.node, twoWay, colorVar);
        }
      })
      .catch(function () { el.innerHTML = '<div class="text-muted" style="padding:8px">Kon kwaliteitsdata niet laden.</div>'; });
  }

  window.NodeQuality = { render: render };
})();
