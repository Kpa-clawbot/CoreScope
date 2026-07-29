// Position-Fix Coverage Gaps tool — Tools > Position Gaps. dborup asked
// for a dedicated Tools page covering the same ground as the Analytics
// Areas tab's "Position-Fix Coverage Gaps by Area" section, "and maybe
// more" like the Map's estimatedNodes=1 view (#/map?estimatedNodes=1).
// Reuses GET /api/analytics/areas (computeAreaPositionGaps,
// cmd/server/db.go) -- no new backend needed, the areaAnalyticsCache is
// already 30s-fresh for the Areas tab.
//
// The "more": that flat per-node estimatedNodes list only ever drove
// anonymous map markers before this -- nowhere in the app could you see
// it as a table (name, area, contributor count) or filter/sort it. This
// page adds exactly that, alongside the same per-area breakdown table
// the Areas tab already shows.
(function () {
  'use strict';

  var container = null;
  var estimatedRows = [];
  var areaRows = [];
  var unpositionedTotal = 0;
  var unpositionedNoNeighborFix = 0;
  var filterText = '';
  var sortState = { col: 'contributorCount', dir: 'desc' };
  var areaSortState = { col: 'pctEstimated', dir: 'desc' };
  var areaExpanded = false;
  var lastAreaRows = [];

  function escapeHtml(s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function pct(n, total) {
    if (!total) return '—';
    return (n / total * 100).toFixed(1) + '%';
  }

  function init(app) {
    container = app;
    estimatedRows = [];
    areaRows = [];
    unpositionedTotal = 0;
    unpositionedNoNeighborFix = 0;
    filterText = '';
    sortState = { col: 'contributorCount', dir: 'desc' };
    areaSortState = { col: 'pctEstimated', dir: 'desc' };
    areaExpanded = false;
    lastAreaRows = [];

    container.innerHTML =
      '<div class="tools-landing" style="max-width:1100px">' +
        '<h2><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-pin"/></svg> Position-Fix Coverage Gaps</h2>' +
        '<p class="help-text">How many nodes in each area have an actual reported GPS position vs. only a neighbor-based estimate.</p>' +
        '<div id="position-gaps-status" class="text-muted" style="font-size:12px;margin-bottom:8px"></div>' +
        '<div id="position-gaps-content"></div>' +
      '</div>';

    var contentEl = document.getElementById('position-gaps-content');
    if (contentEl) {
      // Delegated once here (not re-wired per render) since it's bound
      // to the content wrapper, which persists across every render()
      // call -- only its innerHTML is replaced. Each branch is scoped to
      // #position-gaps-area-table so it can't fire for clicks inside the
      // separately-wired Estimated Nodes table below it.
      contentEl.addEventListener('click', function (evt) {
        var el = evt.target;

        var toggleBtn = el && el.closest ? el.closest('[data-area-toggle]') : null;
        if (toggleBtn) {
          areaExpanded = !areaExpanded;
          rerenderAreaTable();
          return;
        }

        var areaTh = el && el.closest ? el.closest('#position-gaps-area-table th[data-sort-col]') : null;
        if (areaTh) {
          var col = areaTh.dataset.sortCol;
          if (areaSortState.col === col) {
            areaSortState.dir = areaSortState.dir === 'asc' ? 'desc' : 'asc';
          } else {
            areaSortState.col = col;
            areaSortState.dir = col === 'area' ? 'asc' : 'desc';
          }
          rerenderAreaTable();
          return;
        }

        var areaRow = el && el.closest ? el.closest('#position-gaps-area-table tr[data-area-key]') : null;
        if (areaRow) {
          openAreaMap(areaRow.dataset.areaKey, areaRow.dataset.areaLabel);
        }
      });
    }

    load();
  }

  function destroy() {
    container = null;
    estimatedRows = [];
    areaRows = [];
  }

  function load() {
    var statusEl = document.getElementById('position-gaps-status');
    var contentEl = document.getElementById('position-gaps-content');
    if (contentEl) contentEl.innerHTML = '<p class="text-muted">Loading…</p>';
    if (statusEl) statusEl.textContent = '';
    api('/analytics/areas', { ttl: 30000 })
      .then(function (data) {
        areaRows = (data && Array.isArray(data.positionGaps)) ? data.positionGaps : [];
        estimatedRows = (data && Array.isArray(data.estimatedNodes)) ? data.estimatedNodes : [];
        unpositionedTotal = (data && data.unpositionedTotal) || 0;
        unpositionedNoNeighborFix = (data && data.unpositionedNoNeighborFix) || 0;
        render();
      })
      .catch(function (e) {
        if (contentEl) contentEl.innerHTML = '';
        if (statusEl) statusEl.textContent = 'Failed to load: ' + escapeHtml(e.message);
      });
  }

  // ---- Area breakdown table (sortable, collapse-to-10, same pattern
  // as network-digest.js's Area Breakdown) ----

  var AREA_LIMIT = 10;

  function areaSortValue(g, col) {
    var total = g.realFix + g.approximated;
    switch (col) {
      case 'area': return (g.label || '').toLowerCase();
      case 'realFix': return g.realFix;
      case 'approximated': return g.approximated;
      case 'pctEstimated': return total ? g.approximated / total : 0;
      default: return 0;
    }
  }

  // Rows for an area with at least one estimated node are clickable --
  // opens a small map of just that area's estimated nodes (same "map
  // that pops up" pattern as Channels' View Path). Nothing to show for
  // an area sitting at 0% estimated, so those stay plain text.
  function areaRowHtml(g) {
    var total = g.realFix + g.approximated;
    var clickable = g.approximated > 0;
    var labelHtml = clickable
      ? '<span style="color:var(--link-color)">' + escapeHtml(g.label) + '</span>'
      : escapeHtml(g.label);
    var rowAttrs = clickable
      ? ' data-area-key="' + escapeHtml(g.areaKey) + '" data-area-label="' + escapeHtml(g.label) + '" style="cursor:pointer" title="Click to see these nodes on a map"'
      : '';
    return '<tr' + rowAttrs + '>' +
      '<td>' + labelHtml + '</td>' +
      '<td style="text-align:right">' + g.realFix.toLocaleString() + '</td>' +
      '<td style="text-align:right">' + g.approximated.toLocaleString() + '</td>' +
      '<td style="text-align:right">' + pct(g.approximated, total) + '</td>' +
      '</tr>';
  }

  function areaTableInnerHtml(areas) {
    var mult = areaSortState.dir === 'asc' ? 1 : -1;
    var sorted = areas.slice().sort(function (a, b) {
      var av = areaSortValue(a, areaSortState.col), bv = areaSortValue(b, areaSortState.col);
      if (av < bv) return -1 * mult;
      if (av > bv) return 1 * mult;
      return a.label.localeCompare(b.label);
    });
    var shown = areaExpanded ? sorted : sorted.slice(0, AREA_LIMIT);
    var rows = shown.map(areaRowHtml).join('');
    var toggle = sorted.length > AREA_LIMIT
      ? '<div style="margin-top:6px;text-align:right"><button type="button" data-area-toggle class="btn-link" style="font-size:12px;cursor:pointer;background:none;border:none;color:var(--link-color);padding:0">' +
        (areaExpanded ? 'Show fewer' : 'Show all ' + sorted.length + ' areas') + '</button></div>'
      : '';
    return '<table class="data-table"><thead><tr>' +
      sortTh(areaSortState, 'area', 'Area') +
      sortTh(areaSortState, 'realFix', 'Real GPS Fix', 'right') +
      sortTh(areaSortState, 'approximated', 'Estimated (via Neighbors)', 'right') +
      sortTh(areaSortState, 'pctEstimated', '% Estimated', 'right') +
      '</tr></thead><tbody>' + rows + '</tbody></table>' + toggle;
  }

  function rerenderAreaTable() {
    var el = document.getElementById('position-gaps-area-table');
    if (el) el.innerHTML = areaTableInnerHtml(lastAreaRows);
  }

  function openAreaMap(areaKey, areaLabel) {
    var points = estimatedRows.filter(function (r) { return r.areaKey === areaKey; });
    if (window.AreaNodesMap && typeof window.AreaNodesMap.open === 'function') {
      window.AreaNodesMap.open(areaLabel, points);
    }
  }

  // Same "map that pops up" pattern as an area row, but for a single
  // estimated node -- lets you jump straight to where one specific node
  // was placed without going through its area first.
  function openNodeMap(publicKey) {
    var row = estimatedRows.filter(function (r) { return r.publicKey === publicKey; });
    if (!row.length) return;
    if (window.AreaNodesMap && typeof window.AreaNodesMap.open === 'function') {
      window.AreaNodesMap.open(row[0].name || row[0].publicKey, row);
    }
  }

  // ---- Estimated nodes table (sortable + filterable + resizable,
  // same conventions as new-nodes.js / node-changes.js) ----

  function sortValue(row, col) {
    switch (col) {
      case 'name': return (row.name || row.publicKey || '').toLowerCase();
      case 'area': return (row.label || '').toLowerCase();
      case 'contributorCount': return row.contributorCount || 0;
      default: return '';
    }
  }

  // Both tables on this page (Area Breakdown, Estimated Nodes) have
  // their own independent sort state, so these take it explicitly
  // rather than closing over a single module-level one.
  function sortArrow(state, col) {
    if (col !== state.col) return '<span class="sort-arrow">⇅</span>';
    return '<span class="sort-arrow">' + (state.dir === 'asc' ? '↑' : '↓') + '</span>';
  }

  function sortTh(state, col, label, align) {
    var cls = 'sortable' + (col === state.col ? ' sort-active' : '');
    var style = align === 'right' ? ' style="text-align:right"' : '';
    return '<th class="' + cls + '" data-sort-col="' + col + '"' + style + '>' + label + sortArrow(state, col) + '</th>';
  }

  function matchesFilter(row) {
    if (!filterText) return true;
    var haystack = [row.name, row.publicKey, row.label].filter(Boolean).join(' ').toLowerCase();
    return haystack.indexOf(filterText) !== -1;
  }

  function renderEstimatedTable() {
    var statusEl = document.getElementById('position-gaps-est-status');
    var wrap = document.getElementById('position-gaps-est-table-wrap');
    if (!wrap) return;

    if (estimatedRows.length === 0) {
      if (statusEl) statusEl.textContent = '';
      wrap.innerHTML = '<p class="text-muted">No node currently needs a neighbor-based position estimate.</p>';
      return;
    }

    var filtered = estimatedRows.filter(matchesFilter);
    var mult = sortState.dir === 'asc' ? 1 : -1;
    var sorted = filtered.slice().sort(function (a, b) {
      var av = sortValue(a, sortState.col), bv = sortValue(b, sortState.col);
      if (av < bv) return -1 * mult;
      if (av > bv) return 1 * mult;
      return 0;
    });

    if (statusEl) {
      statusEl.textContent = filtered.length.toLocaleString() + ' of ' + estimatedRows.length.toLocaleString() + ' estimated node' + (estimatedRows.length === 1 ? '' : 's') +
        (filterText ? ' (filtered)' : '');
    }

    if (sorted.length === 0) {
      wrap.innerHTML = '<p class="text-muted">No rows match "' + escapeHtml(filterText) + '".</p>';
      return;
    }

    var rows = sorted.map(function (row) {
      var nameLabel = row.name ? escapeHtml(row.name) : escapeHtml(row.publicKey.slice(0, 8)) + '…';
      var nameCell = '<a href="#/nodes/' + encodeURIComponent(row.publicKey) + '">' + nameLabel + '</a>';
      var areaCell = row.label ? '<span class="badge-region">' + escapeHtml(row.label) + '</span>' : '<span class="text-muted">—</span>';
      return '<tr data-node-pk="' + escapeHtml(row.publicKey) + '" style="cursor:pointer" title="Click to see this node on a map">' +
        '<td>' + nameCell + '</td>' +
        '<td>' + areaCell + '</td>' +
        '<td style="text-align:right">' + (row.contributorCount || 0).toLocaleString() + '</td>' +
        '</tr>';
    }).join('');

    wrap.innerHTML =
      '<table class="data-table" id="position-gaps-est-table"><thead><tr>' +
        sortTh(sortState, 'name', 'Node') +
        sortTh(sortState, 'area', 'Area') +
        sortTh(sortState, 'contributorCount', 'Contributors', 'right') +
      '</tr></thead><tbody>' + rows + '</tbody></table>';

    var table = document.getElementById('position-gaps-est-table');
    if (table) {
      table.querySelectorAll('th[data-sort-col]').forEach(function (th) {
        th.addEventListener('click', function () {
          var col = th.dataset.sortCol;
          if (sortState.col === col) {
            sortState.dir = sortState.dir === 'asc' ? 'desc' : 'asc';
          } else {
            sortState.col = col;
            sortState.dir = (col === 'name' || col === 'area') ? 'asc' : 'desc';
          }
          renderEstimatedTable();
        });
      });
      // Row click opens the same pop-up map as an Area Breakdown row,
      // scoped to just this one node -- skip it when the click landed on
      // the node-name link itself, which already navigates on its own.
      table.querySelectorAll('tbody tr[data-node-pk]').forEach(function (tr) {
        tr.addEventListener('click', function (evt) {
          if (evt.target && evt.target.closest && evt.target.closest('a')) return;
          openNodeMap(tr.dataset.nodePk);
        });
      });
    }

    if (typeof makeColumnsResizable === 'function') makeColumnsResizable('#position-gaps-est-table', 'meshcore-position-gaps-col-widths');
  }

  function render() {
    var statusEl = document.getElementById('position-gaps-status');
    var contentEl = document.getElementById('position-gaps-content');
    if (!contentEl) return;

    if (statusEl) {
      statusEl.textContent = unpositionedTotal.toLocaleString() + ' node' + (unpositionedTotal === 1 ? '' : 's') + ' network-wide have no real GPS fix' +
        (unpositionedNoNeighborFix ? ', of which ' + unpositionedNoNeighborFix.toLocaleString() + ' also have no positioned neighbor to estimate from -- those can\'t be placed anywhere, not even approximately.' : '.');
    }

    if (!areaRows.length && !estimatedRows.length) {
      contentEl.innerHTML = '<p class="text-muted" style="padding:20px 0">No Areas are configured -- this page needs at least one drawn-polygon Area (meshguide.dk sync) to report on.</p>';
      return;
    }

    lastAreaRows = areaRows;

    var mapLink = estimatedRows.length
      ? '<a href="#/map?estimatedNodes=1" class="btn-link" style="font-size:12px;font-weight:400;text-decoration:none;background:none;border:1px solid var(--border);border-radius:4px;padding:4px 10px;color:var(--link-color)">View on Map (' + estimatedRows.length.toLocaleString() + ')</a>'
      : '';

    contentEl.innerHTML =
      '<div class="analytics-card">' +
        '<h3>Area Breakdown</h3>' +
        '<p class="help-text" style="margin:0 0 8px;font-size:12px">Click a column header to sort. Click an area with an estimate to see those nodes on a map.</p>' +
        '<div id="position-gaps-area-table">' + areaTableInnerHtml(areaRows) + '</div>' +
      '</div>' +
      '<div class="analytics-card" style="margin-top:16px">' +
        '<h3 style="display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap">' +
          '<span>Estimated Nodes</span>' + mapLink +
        '</h3>' +
        '<p class="help-text" style="margin:0 0 8px;font-size:12px">Every node with no real GPS fix that was placed anyway, via a weighted centroid of its positioned neighbors. Click a column header to sort, or a row to see that node on a map.</p>' +
        '<div style="margin:0 0 12px"><input type="text" id="position-gaps-filter" class="input" placeholder="Filter by name, pubkey, or area…" style="width:100%;max-width:420px"></div>' +
        '<div id="position-gaps-est-status" class="text-muted" style="font-size:12px;margin-bottom:8px"></div>' +
        '<div id="position-gaps-est-table-wrap" class="table-fluid-wrap"></div>' +
      '</div>';

    var filterInput = document.getElementById('position-gaps-filter');
    if (filterInput) {
      filterInput.addEventListener('input', function () {
        filterText = filterInput.value.toLowerCase();
        renderEstimatedTable();
      });
    }

    renderEstimatedTable();
  }

  window.PositionGapsTool = { init: init, destroy: destroy, sortValue: sortValue, areaSortValue: areaSortValue };
  if (typeof registerPage === 'function') registerPage('position-gaps-tool', { init: init, destroy: destroy });
})();
