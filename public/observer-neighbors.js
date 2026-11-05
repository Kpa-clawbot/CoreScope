/* === CoreScope — observer-neighbors.js ===
 * #2270 / #2271: the latest /neighbors report of an observer, on the
 * observers list (cell) and the observer page (cards + table). Pure render
 * helpers, no fetching; window global so observers.js and observer-detail.js
 * share them and the XSS sink test can call them directly.
 *
 * Every string in a report is observer-published (MQTT) or mesh-published
 * (node names), so everything is escaped here. */
'use strict';

window.ObserverNeighbors = (function () {
  function esc(s) {
    return String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;')
      .replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function ago(ms) {
    if (!isFinite(ms) || ms < 0) return '—';
    var s = Math.floor(ms / 1000);
    if (s < 60) return s + 's ago';
    var m = Math.floor(s / 60);
    if (m < 60) return m + 'm ago';
    var h = Math.floor(m / 60);
    if (h < 48) return h + 'h ago';
    return Math.floor(h / 24) + 'd ago';
  }

  function since(iso, nowMs) {
    var t = Date.parse(iso);
    return isNaN(t) ? '—' : ago(nowMs - t);
  }

  // cell: "3h ago · 12" for the observers table, "—" when never reported.
  function cell(o, nowMs) {
    if (!o || !o.neighbors_report_at) return '<span class="text-muted" title="No /neighbors report received">—</span>';
    var n = Number(o.neighbors_count);
    var count = isFinite(n) ? n : 0;
    return '<span title="Latest /neighbors report ' + esc(o.neighbors_report_at) + ': ' + count +
      ' neighbor' + (count === 1 ? '' : 's') + '">' + since(o.neighbors_report_at, nowMs || Date.now()) +
      ' · ' + count + '</span>';
  }

  function snr(v) {
    var n = Number(v);
    return v == null || !isFinite(n) ? '—' : n.toFixed(1) + ' dB';
  }

  function heard(secs) {
    var n = Number(secs);
    return secs == null || !isFinite(n) ? '—' : ago(n * 1000);
  }

  // detailHtml: the report as a table for the observer page, "" without one.
  function detailHtml(rep) {
    if (!rep || !Array.isArray(rep.neighbors)) return '';
    var cellStyle = 'padding:3px 8px';
    var rows = rep.neighbors.map(function (n) {
      var key = String(n.pubkey || '');
      var label = n.name ? esc(n.name) : '<span class="mono">' + esc(key.slice(0, 8)) + '…</span>';
      return '<tr>' +
        '<td style="' + cellStyle + '"><a href="#/nodes/' + encodeURIComponent(key) + '">' + label + '</a>' +
          (n.role ? ' <span class="text-muted">' + esc(n.role) + '</span>' : '') + '</td>' +
        '<td style="' + cellStyle + '">' + snr(n.snr) + '</td>' +
        '<td style="' + cellStyle + '">' + heard(n.heard_secs_ago) + '</td>' +
        '<td style="' + cellStyle + '">' + (n.status ? esc(n.status) : '—') + '</td>' +
        '<td style="' + cellStyle + '" class="mono">' + (n.scopes ? esc(n.scopes) : '—') + '</td>' +
        '</tr>';
    }).join('');
    var head = '<th style="' + cellStyle + ';text-align:left">';
    var meta = [];
    if (rep.reported_at) meta.push('reported ' + esc(rep.reported_at));
    if (rep.queried_neighbors != null) meta.push(esc(rep.queried_neighbors) + ' scope queries sent');
    if (rep.self_scopes) meta.push('own scopes ' + esc(rep.self_scopes));
    if (rep.self_default_scope) meta.push('default scope ' + esc(rep.self_default_scope));
    var shown = rep.neighbors.length;
    var count = Number(rep.neighbor_count);
    var more = isFinite(count) && count > shown ? ' (' + shown + ' of ' + count + ' listed)' : '';
    return '<div class="node-full-card" data-testid="observer-neighbors" style="margin-bottom:20px;padding:12px">' +
      '<h4 style="margin:0 0 6px">Neighbors' + more + '</h4>' +
      (meta.length ? '<div class="text-muted" style="font-size:12px;margin-bottom:6px">' + meta.join(' · ') + '</div>' : '') +
      (shown ? '<table style="border-collapse:collapse;font-size:0.85em"><thead><tr>' +
        head + 'Node</th>' + head + 'SNR</th>' + head + 'Heard</th>' + head + 'Scope query</th>' + head + 'Scopes</th>' +
        '</tr></thead><tbody>' + rows + '</tbody></table>'
        : '<div class="text-muted">The latest report lists no neighbors.</div>') +
      '</div>';
  }

  return { cell: cell, detailHtml: detailHtml, since: since };
})();
