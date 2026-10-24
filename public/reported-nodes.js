/* Repeaters known only from their reported name (/api/nodes/reported,
   cmd/server/reported_nodes.go): they told a mobile companion their name but
   never sent an advert, so /api/nodes has no row for them. This module makes
   them findable: a section on the Nodes page, a detail view where the node
   page would say "not found", and entries in the header search. Positions are
   always presented as estimates (see map-estimated.js for the map). */
(function () {
  'use strict';
  // i18n.js defines tr()/trn(); sandboxes that load this file alone get English.
  var tr = globalThis.tr || function (s, p) { return p ? s.replace(/\{(\w+)\}/g, function (m, k) { return Object.prototype.hasOwnProperty.call(p, k) ? String(p[k]) : m; }) : s; };
  var trn = globalThis.trn || function (n, one, other, p) { return tr(n === 1 ? one : other, Object.assign({ count: n }, p)); };

  var TTL_MS = 5 * 60 * 1000; // the server caches the list for the same time
  var cache = null;           // { at, promise }

  // load returns { nodes, method }; an older server without the route, or a
  // failed request, yields an empty list so callers never break on it.
  function load() {
    if (cache && Date.now() - cache.at < TTL_MS) return cache.promise;
    var p = fetch('/api/nodes/reported')
      .then(function (r) { return r.ok ? r.json() : { nodes: [] }; })
      .then(function (j) { return { nodes: (j && j.nodes) || [], method: (j && j.method) || null }; })
      .catch(function () { return { nodes: [], method: null }; });
    cache = { at: Date.now(), promise: p };
    return p;
  }

  // matches: name contains q (any case) or pubkey starts with q.
  function matches(n, q) {
    q = String(q || '').trim().toLowerCase();
    if (!q) return true;
    return String(n.name || '').toLowerCase().indexOf(q) >= 0 || String(n.pubkey || '').indexOf(q) === 0;
  }

  // find resolves a full pubkey or a prefix of at least 8 hex characters to
  // one reported node; null when none or several match.
  function find(nodes, key) {
    key = String(key || '').toLowerCase();
    if (key.length < 8) return null;
    var hits = (nodes || []).filter(function (n) { return n.pubkey === key || n.pubkey.indexOf(key) === 0; });
    return hits.length === 1 ? hits[0] : null;
  }

  // formatRadius and coverageHref are shared with map-estimated.js.
  function formatRadius(m) {
    if (!isFinite(m)) return '';
    return m >= 1000 ? (Math.round(m / 100) / 10) + ' km' : Math.round(m) + ' m';
  }

  function coverageHref(lat, lon) {
    return '#/rx-coverage?days=30&lat=' + Number(lat).toFixed(5) + '&lon=' + Number(lon).toFixed(5) + '&zoom=14';
  }

  // positionText says where the repeater is, as far as anyone knows.
  function positionText(n, method) {
    if (n.estimate) return '≈ ' + tr('estimated, ± {radius}', { radius: formatRadius(n.estimate.radius_m) });
    var need = method && method.min_spots ? method.min_spots : 3;
    return trn(n.spots, 'Unknown, heard at {count} spot (estimate needs {need})', 'Unknown, heard at {count} spots (estimate needs {need})', { need: need });
  }

  function day(s) { return s ? String(s).slice(0, 10) : '—'; }

  function sectionHtml(nodes, method, q) {
    var shown = (nodes || []).filter(function (n) { return matches(n, q); });
    if (!shown.length) return '';
    var rows = shown.map(function (n) {
      var href = '#/nodes/' + encodeURIComponent(n.pubkey);
      return '<tr>' +
        '<td><a href="' + href + '">' + escapeHtml(n.name || n.pubkey.slice(0, 8)) + '</a></td>' +
        '<td class="mono">' + escapeHtml(n.pubkey.slice(0, 16)) + '…</td>' +
        '<td>' + escapeHtml(day(n.last_heard)) + '</td>' +
        '<td class="' + (n.estimate ? 'reported-pos-est' : 'reported-pos-none') + '">' + escapeHtml(positionText(n, method)) + '</td>' +
        '</tr>';
    }).join('');
    return '<section class="reported-section" aria-labelledby="reportedHeading">' +
      '<h3 id="reportedHeading">' + tr('Self-reported repeaters') + ' <span class="reported-count">' + shown.length + '</span></h3>' +
      '<p class="reported-intro">' + tr('These repeaters never sent an advert. They told a CoreDrive companion their name when asked, so there is no role data, no packets and no advert position. A position, where shown, is estimated from where companions heard them.') + '</p>' +
      '<div class="table-fluid-wrap"><table class="data-table reported-table"><thead><tr>' +
      '<th scope="col">' + tr('Name') + '</th><th scope="col">' + tr('Public Key') + '</th><th scope="col">' + tr('Last heard') + '</th><th scope="col">' + tr('Position') + '</th>' +
      '</tr></thead><tbody>' + rows + '</tbody></table></div></section>';
  }

  function detailHtml(n, method) {
    var e = n.estimate;
    var pos = e
      ? '<dd>≈ ' + escapeHtml(e.lat.toFixed(5)) + ', ' + escapeHtml(e.lon.toFixed(5)) + ' (' + tr('estimated, within about {radius}', { radius: escapeHtml(formatRadius(e.radius_m)) }) + ')</dd>'
      : '<dd>' + trn(n.spots, 'Unknown. Heard at {count} spot; an estimate needs {min} spots at least {spread} m apart.', 'Unknown. Heard at {count} spots; an estimate needs {min} spots at least {spread} m apart.',
          { count: escapeHtml(n.spots), min: escapeHtml(method && method.min_spots ? method.min_spots : 3), spread: escapeHtml(method && method.min_spread_m ? method.min_spread_m : 200) }) + '</dd>';
    var links = e
      ? '<a class="btn-primary" href="#/map">' + tr('Map') + '</a> <a class="btn-primary" href="' + coverageHref(e.lat, e.lon) + '">' + tr('Receptions on the coverage map') + '</a>'
      : '<a class="btn-primary" href="#/rx-coverage">' + tr('Coverage map') + '</a>';
    return '<div class="node-full-card reported-detail">' +
      '<div class="reported-badge">' + tr('Self-reported repeater') + '</div>' +
      '<div class="node-detail-name">' + escapeHtml(n.name || '(' + tr('unnamed') + ')') + '</div>' +
      '<div class="node-detail-key mono">' + escapeHtml(n.pubkey) + '</div>' +
      '<p>' + tr('No advert from this repeater has been received, so this instance has no packets, role data or advert position for it. Its name comes from the repeater itself, asked by a CoreDrive companion.') + '</p>' +
      '<dl class="reported-facts">' +
      '<dt>' + tr('Name reported') + '</dt><dd>' + trn(n.reporters, '{date} by {count} companion', '{date} by {count} companions', { date: escapeHtml(day(n.name_reported_at)), count: escapeHtml(n.reporters) }) + '</dd>' +
      '<dt>' + tr('Heard directly') + '</dt><dd>' + trn(n.spots, '{receptions} times at {count} spot, {from} to {to}', '{receptions} times at {count} spots, {from} to {to}',
          { receptions: escapeHtml(n.receptions), count: escapeHtml(n.spots), from: escapeHtml(day(n.first_heard)), to: escapeHtml(day(n.last_heard)) }) + '</dd>' +
      '<dt>' + tr('Position') + '</dt>' + pos +
      '</dl>' +
      (e ? '<p class="reported-caveat">' + tr('The estimate is a centroid of where companions drove, not a measured position: a repeater heard from one side only is placed towards that side.') + '</p>' : '') +
      '<div class="reported-links">' + links + ' <a href="#/nodes" class="btn-primary">← ' + tr('Back to Nodes') + '</a></div>' +
      '</div>';
  }

  // searchItemsHtml renders header-search results in the same markup as the
  // node results there (app.js), tagged so they read as self-reported.
  function searchItemsHtml(nodes, q, limit) {
    return (nodes || []).filter(function (n) { return matches(n, q); }).slice(0, limit || 5).map(function (n) {
      return '<div class="search-result-item" tabindex="0" role="option" data-href="#/nodes/' + escapeHtml(n.pubkey) + '">' +
        '<span class="search-result-type">' + tr('Node') + '</span>' + escapeHtml(n.name || n.pubkey.slice(0, 8)) + ' — ' + escapeHtml(n.pubkey.slice(0, 16)) + '…' +
        ' <span class="reported-tag">' + tr('self-reported') + (n.estimate ? ', ≈ ' + tr('position') : '') + '</span></div>';
    }).join('');
  }

  window.ReportedNodes = {
    load: load, matches: matches, find: find, positionText: positionText,
    formatRadius: formatRadius, coverageHref: coverageHref,
    sectionHtml: sectionHtml, detailHtml: detailHtml, searchItemsHtml: searchItemsHtml,
    _reset: function () { cache = null; },
  };
})();
