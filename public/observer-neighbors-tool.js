// Observer Neighbors tool — network-wide list of every observer's
// firmware-reported direct (zero-hop) neighbors, flattened across all
// observers into one searchable/sortable table (Tools > Observer
// Neighbors). Requested by dborup as a single place to see this instead
// of clicking into each observer's Direct Neighbors panel individually
// (public/observer-detail.js's renderDirectNeighbors, whose row shape and
// col-scope-list wrap-fix this reuses).
(function () {
  'use strict';

  var container = null;
  var allRows = [];
  var unknownScopes = [];
  var filterText = '';
  var sortState = { col: 'observer', dir: 'asc' };

  function escapeHtml(s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function init(app) {
    container = app;
    allRows = [];
    unknownScopes = [];
    filterText = '';
    sortState = { col: 'observer', dir: 'asc' };

    container.innerHTML =
      '<div class="tools-landing" style="max-width:1100px">' +
        '<h2>Observer Neighbors</h2>' +
        '<p class="help-text">Every observer\'s firmware-reported direct (zero-hop) neighbors, network-wide. Ground truth from each observer\'s own /neighbors report -- distinct from the packet-path-inferred neighbor graph. Click a column header to sort.</p>' +
        '<div id="obs-nb-unknown-scopes-wrap"></div>' +
        '<div style="margin:12px 0"><input type="text" id="obs-nb-filter" class="input" placeholder="Filter by observer or neighbor name…" style="max-width:320px"></div>' +
        '<div id="obs-nb-status" class="text-muted" style="font-size:12px;margin-bottom:8px"></div>' +
        '<div id="obs-nb-table-wrap" class="table-fluid-wrap"></div>' +
      '</div>';

    var filterInput = document.getElementById('obs-nb-filter');
    if (filterInput) {
      filterInput.addEventListener('input', function () {
        filterText = filterInput.value.toLowerCase();
        renderTable();
      });
    }

    load();
  }

  function destroy() {
    container = null;
    allRows = [];
    unknownScopes = [];
  }

  function load() {
    var statusEl = document.getElementById('obs-nb-status');
    var wrap = document.getElementById('obs-nb-table-wrap');
    if (wrap) wrap.innerHTML = '<p class="text-muted">Loading…</p>';
    fetch('/api/observers/neighbors')
      .then(function (r) {
        if (!r.ok) return r.json().then(function (d) { throw new Error(d.error || 'Request failed'); });
        return r.json();
      })
      .then(function (data) {
        allRows = (data && Array.isArray(data.neighbors)) ? data.neighbors : [];
        unknownScopes = (data && Array.isArray(data.unknownScopes)) ? data.unknownScopes : [];
        renderUnknownScopes();
        renderTable();
      })
      .catch(function (e) {
        if (wrap) wrap.innerHTML = '';
        if (statusEl) statusEl.textContent = 'Failed to load: ' + e.message;
      });
  }

  // "Scopes CoreScope doesn't know about yet" -- region-scope names seen
  // in reported neighbor scope lists that aren't part of this
  // deployment's configured hashRegions (dborup: "kan vi have en panel
  // med scopes vi ikke kender på corescope som observer neighbors har
  // fundet"). Computed server-side (computeUnknownScopes, db.go) from the
  // same rows this page already fetches -- no second request.
  function renderUnknownScopes() {
    var wrap = document.getElementById('obs-nb-unknown-scopes-wrap');
    if (!wrap) return;
    if (unknownScopes.length === 0) {
      wrap.innerHTML = '';
      return;
    }
    var rows = unknownScopes.map(function (u) {
      return '<tr>' +
        '<td><code>' + escapeHtml(u.scope) + '</code></td>' +
        '<td style="text-align:right">' + u.count.toLocaleString() + '</td>' +
        '<td class="text-muted" style="font-size:0.85em">' + (u.examples || []).map(escapeHtml).join(', ') + '</td>' +
        '</tr>';
    }).join('');
    wrap.innerHTML =
      '<div class="analytics-card" style="margin:12px 0">' +
        '<h3 style="margin:0 0 4px">Scopes CoreScope Doesn\'t Know About Yet (' + unknownScopes.length.toLocaleString() + ')</h3>' +
        '<p class="text-muted" style="margin:0 0 8px;font-size:0.85em">Region-scope names reported in the wild by neighbors\' OTA scope query, but not part of this deployment\'s configured regions. Might be worth adding to config -- or just neighboring mesh communities using their own naming.</p>' +
        '<table class="data-table"><thead><tr><th>Scope</th><th style="text-align:right">Seen By</th><th>Example Neighbors</th></tr></thead><tbody>' + rows + '</tbody></table>' +
      '</div>';
  }

  function sortValue(row, col) {
    switch (col) {
      case 'observer': return (row.observerName || row.observerId || '').toLowerCase();
      case 'neighbor': return (row.neighborName || row.neighborPubkey || '').toLowerCase();
      case 'evidence': return row.seenViaPackets ? 1 : 0;
      case 'status': return (row.status || '').toLowerCase();
      case 'reportedAt': return row.reportedAt || '';
      default: return '';
    }
  }

  function sortArrow(col) {
    if (col !== sortState.col) return '<span class="sort-arrow">⇅</span>';
    return '<span class="sort-arrow">' + (sortState.dir === 'asc' ? '↑' : '↓') + '</span>';
  }

  function sortTh(col, label) {
    var cls = 'sortable' + (col === sortState.col ? ' sort-active' : '');
    return '<th class="' + cls + '" data-sort-col="' + col + '">' + label + sortArrow(col) + '</th>';
  }

  function matchesFilter(row) {
    if (!filterText) return true;
    var observer = (row.observerName || row.observerId || '').toLowerCase();
    var neighbor = (row.neighborName || row.neighborPubkey || '').toLowerCase();
    return observer.indexOf(filterText) !== -1 || neighbor.indexOf(filterText) !== -1;
  }

  function renderTable() {
    var statusEl = document.getElementById('obs-nb-status');
    var wrap = document.getElementById('obs-nb-table-wrap');
    if (!wrap) return;

    if (allRows.length === 0) {
      if (statusEl) statusEl.textContent = '';
      wrap.innerHTML = '<p class="text-muted">No observer has reported any direct neighbors yet.</p>';
      return;
    }

    var filtered = allRows.filter(matchesFilter);
    var mult = sortState.dir === 'asc' ? 1 : -1;
    var sorted = filtered.slice().sort(function (a, b) {
      var av = sortValue(a, sortState.col), bv = sortValue(b, sortState.col);
      if (av < bv) return -1 * mult;
      if (av > bv) return 1 * mult;
      return 0;
    });

    if (statusEl) {
      statusEl.textContent = filtered.length.toLocaleString() + ' of ' + allRows.length.toLocaleString() + ' neighbor pairs' +
        (filterText ? ' (filtered)' : '');
    }

    if (sorted.length === 0) {
      wrap.innerHTML = '<p class="text-muted">No rows match "' + escapeHtml(filterText) + '".</p>';
      return;
    }

    var rows = sorted.map(function (row) {
      var observerLabel = row.observerName ? escapeHtml(row.observerName) : escapeHtml(row.observerId);
      var observerCell = '<a href="#/observers/' + encodeURIComponent(row.observerId) + '">' + observerLabel + '</a>';

      var neighborLabel = row.neighborName ? escapeHtml(row.neighborName) : escapeHtml(String(row.neighborPubkey).slice(0, 12)) + '…';
      var neighborCell = row.neighborName
        ? '<a href="#/nodes/' + encodeURIComponent(row.neighborPubkey) + '">' + neighborLabel + '</a>'
        : '<span class="mono">' + neighborLabel + '</span>';

      var scopeCell = row.scopes
        ? '<span class="badge-region">' + escapeHtml(row.scopes) + '</span>'
        : (row.status === 'timeout'
          ? '<span class="text-muted" title="Scope query timed out">no reply</span>'
          : '<span class="text-muted">—</span>');

      var evidenceCell = row.seenViaPackets
        ? '<span class="text-muted" title="A packet path connecting this station and the observer has been resolved">confirmed</span>'
        : '<span style="color:var(--text-muted)" title="Firmware reports this as a direct RF neighbor, but no packet path between the two has been resolved yet.">not seen yet</span>';

      var reportedCell = row.reportedAt
        ? '<span title="' + escapeHtml(row.reportedAt) + '">' + (typeof timeAgo === 'function' ? timeAgo(row.reportedAt) : escapeHtml(row.reportedAt)) + '</span>'
        : '<span class="text-muted">—</span>';

      return '<tr><td>' + observerCell + '</td><td>' + neighborCell + '</td><td class="col-scope-list">' + scopeCell + '</td>' +
        '<td>' + evidenceCell + '</td><td>' + escapeHtml(row.status || '') + '</td><td>' + reportedCell + '</td></tr>';
    }).join('');

    wrap.innerHTML =
      '<table class="data-table" id="obs-nb-table"><thead><tr>' +
        sortTh('observer', 'Observer') +
        sortTh('neighbor', 'Neighbor') +
        '<th>Configured Scope</th>' +
        sortTh('evidence', 'Packet Evidence') +
        sortTh('status', 'Status') +
        sortTh('reportedAt', 'Reported') +
      '</tr></thead><tbody>' + rows + '</tbody></table>';

    var table = document.getElementById('obs-nb-table');
    if (table) {
      table.querySelectorAll('th[data-sort-col]').forEach(function (th) {
        th.addEventListener('click', function () {
          var col = th.dataset.sortCol;
          if (sortState.col === col) {
            sortState.dir = sortState.dir === 'asc' ? 'desc' : 'asc';
          } else {
            sortState.col = col;
            sortState.dir = (col === 'observer' || col === 'neighbor' || col === 'status') ? 'asc' : 'desc';
          }
          renderTable();
        });
      });
    }
  }

  window.ObserverNeighborsTool = { init: init, destroy: destroy, sortValue: sortValue };
  if (typeof registerPage === 'function') registerPage('observer-neighbors-tool', { init: init, destroy: destroy });
})();
