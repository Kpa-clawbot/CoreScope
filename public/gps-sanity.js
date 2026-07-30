// GPS Sanity Check tool -- Tools > Suspicious GPS Positions. dborup asked
// whether we can detect nodes with a wrong/stale self-reported GPS
// position by comparing it against its own RF neighbors -- flipping the
// neighbor-centroid ESTIMATE technique (Position-Fix Coverage Gaps,
// nearestPositionedNeighbor) around to sanity-CHECK a position a node
// already reports, instead of filling one in when it's missing.
//
// Reuses GET /api/analytics/gps-sanity (computeSuspiciousGPSPositions,
// cmd/server/gps_sanity.go) -- v1, kept deliberately simple: no
// confidence-weighting by the neighbor hash-prefix ambiguity mode (the
// green/yellow/red indicator the Neighbors panel on a node's own detail
// page shows), just raw neighbor_edges observation counts. See that
// function's doc comment for the two-step algorithm.
(function () {
  'use strict';

  var container = null;
  var rows = [];
  var totalRealGPS = 0;
  var evaluated = 0;
  var filterText = '';
  var sortState = { col: 'distanceKm', dir: 'desc' };

  function escapeHtml(s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function init(app) {
    container = app;
    rows = [];
    totalRealGPS = 0;
    evaluated = 0;
    filterText = '';
    sortState = { col: 'distanceKm', dir: 'desc' };

    container.innerHTML =
      '<div class="tools-landing" style="max-width:1100px">' +
        '<h2><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-crosshair"/></svg> Suspicious GPS Positions</h2>' +
        '<p class="help-text">Nodes whose self-reported GPS disagrees with a tight, trusted cluster of their own RF neighbors -- likely a stale or wrong position, not just an estimate gap.</p>' +
        '<div id="gps-sanity-status" class="text-muted" style="font-size:12px;margin-bottom:8px"></div>' +
        '<div style="margin:0 0 12px"><input type="text" id="gps-sanity-filter" class="input" placeholder="Filter by name or pubkey…" style="width:100%;max-width:420px"></div>' +
        '<div id="gps-sanity-content"></div>' +
      '</div>';

    var filterInput = document.getElementById('gps-sanity-filter');
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
    rows = [];
  }

  function load() {
    var statusEl = document.getElementById('gps-sanity-status');
    var contentEl = document.getElementById('gps-sanity-content');
    if (contentEl) contentEl.innerHTML = '<p class="text-muted">Loading…</p>';
    if (statusEl) statusEl.textContent = '';
    api('/analytics/gps-sanity', { ttl: 30000 })
      .then(function (data) {
        rows = (data && Array.isArray(data.nodes)) ? data.nodes : [];
        totalRealGPS = (data && data.totalRealGps) || 0;
        evaluated = (data && data.evaluated) || 0;
        render();
      })
      .catch(function (e) {
        if (contentEl) contentEl.innerHTML = '';
        if (statusEl) statusEl.textContent = 'Failed to load: ' + escapeHtml(e.message);
      });
  }

  function sortValue(row, col) {
    switch (col) {
      case 'name': return (row.name || row.publicKey || '').toLowerCase();
      case 'distanceKm': return row.distanceKm || 0;
      case 'clusterSize': return row.clusterSize || 0;
      case 'clusterSpreadKm': return row.clusterSpreadKm || 0;
      default: return '';
    }
  }

  function sortArrow(col) {
    if (col !== sortState.col) return '<span class="sort-arrow">⇅</span>';
    return '<span class="sort-arrow">' + (sortState.dir === 'asc' ? '↑' : '↓') + '</span>';
  }

  function sortTh(col, label, align) {
    var cls = 'sortable' + (col === sortState.col ? ' sort-active' : '');
    var style = align === 'right' ? ' style="text-align:right"' : '';
    return '<th class="' + cls + '" data-sort-col="' + col + '"' + style + '>' + label + sortArrow(col) + '</th>';
  }

  function matchesFilter(row) {
    if (!filterText) return true;
    var haystack = [row.name, row.publicKey].filter(Boolean).join(' ').toLowerCase();
    return haystack.indexOf(filterText) !== -1;
  }

  function renderTable() {
    var statusEl = document.getElementById('gps-sanity-table-status');
    var wrap = document.getElementById('gps-sanity-table-wrap');
    if (!wrap) return;

    if (rows.length === 0) {
      if (statusEl) statusEl.textContent = '';
      wrap.innerHTML = '<p class="text-muted">No suspicious GPS positions found.</p>';
      return;
    }

    var filtered = rows.filter(matchesFilter);
    var mult = sortState.dir === 'asc' ? 1 : -1;
    var sorted = filtered.slice().sort(function (a, b) {
      var av = sortValue(a, sortState.col), bv = sortValue(b, sortState.col);
      if (av < bv) return -1 * mult;
      if (av > bv) return 1 * mult;
      return 0;
    });

    if (statusEl) {
      statusEl.textContent = filtered.length.toLocaleString() + ' of ' + rows.length.toLocaleString() + ' flagged node' + (rows.length === 1 ? '' : 's') +
        (filterText ? ' (filtered)' : '');
    }

    if (sorted.length === 0) {
      wrap.innerHTML = '<p class="text-muted">No rows match "' + escapeHtml(filterText) + '".</p>';
      return;
    }

    var rowsHtml = sorted.map(function (row) {
      var nameLabel = row.name ? escapeHtml(row.name) : escapeHtml(row.publicKey.slice(0, 8)) + '…';
      var nameCell = '<a href="#/nodes/' + encodeURIComponent(row.publicKey) + '">' + nameLabel + '</a>';
      return '<tr>' +
        '<td>' + nameCell + '</td>' +
        '<td class="mono" style="font-size:11px">' + row.lat.toFixed(4) + ', ' + row.lon.toFixed(4) + '</td>' +
        '<td class="mono" style="font-size:11px">' + row.clusterLat.toFixed(4) + ', ' + row.clusterLon.toFixed(4) + '</td>' +
        '<td style="text-align:right">' + row.distanceKm.toFixed(1) + ' km</td>' +
        '<td style="text-align:right">' + row.clusterSize + '</td>' +
        '</tr>';
    }).join('');

    wrap.innerHTML =
      '<table class="data-table" id="gps-sanity-table"><thead><tr>' +
        sortTh('name', 'Node') +
        '<th>Own Position</th>' +
        '<th>Neighbor Consensus</th>' +
        sortTh('distanceKm', 'Distance', 'right') +
        sortTh('clusterSize', 'Cluster Size', 'right') +
      '</tr></thead><tbody>' + rowsHtml + '</tbody></table>';

    var table = document.getElementById('gps-sanity-table');
    if (table) {
      table.querySelectorAll('th[data-sort-col]').forEach(function (th) {
        th.addEventListener('click', function () {
          var col = th.dataset.sortCol;
          if (sortState.col === col) {
            sortState.dir = sortState.dir === 'asc' ? 'desc' : 'asc';
          } else {
            sortState.col = col;
            sortState.dir = col === 'name' ? 'asc' : 'desc';
          }
          renderTable();
        });
      });
    }

    if (typeof makeColumnsResizable === 'function') makeColumnsResizable('#gps-sanity-table', 'meshcore-gps-sanity-col-widths');
  }

  function render() {
    var statusEl = document.getElementById('gps-sanity-status');
    var contentEl = document.getElementById('gps-sanity-content');
    if (!contentEl) return;

    if (statusEl) {
      statusEl.textContent = totalRealGPS.toLocaleString() + ' node' + (totalRealGPS === 1 ? '' : 's') + ' network-wide have a real GPS fix, ' +
        evaluated.toLocaleString() + ' had enough agreeing neighbors to check.';
    }

    contentEl.innerHTML = '<div class="analytics-card">' +
      '<div id="gps-sanity-table-status" class="text-muted" style="font-size:12px;margin-bottom:8px"></div>' +
      '<div id="gps-sanity-table-wrap" class="table-fluid-wrap"></div>' +
    '</div>';
    renderTable();
  }

  window.GPSSanityTool = { init: init, destroy: destroy, sortValue: sortValue };
  if (typeof registerPage === 'function') registerPage('gps-sanity-tool', { init: init, destroy: destroy });
})();
