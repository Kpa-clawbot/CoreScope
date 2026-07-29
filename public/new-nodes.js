// New Nodes tool — most recently first-seen nodes network-wide (Tools >
// New Nodes). Backend: cmd/server/new_nodes.go, GET /api/analytics/new-nodes.
// Pattern modeled on observer-neighbors-tool.js (filter input + sortable
// data-table + status line), the closest existing Tools sub-page.
(function () {
  'use strict';

  var container = null;
  var allRows = [];
  var filterText = '';
  var sortState = { col: 'firstSeen', dir: 'desc' };

  function escapeHtml(s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function init(app) {
    container = app;
    allRows = [];
    filterText = '';
    sortState = { col: 'firstSeen', dir: 'desc' };

    container.innerHTML =
      '<div class="tools-landing" style="max-width:1100px">' +
        '<h2><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-rocket"/></svg> New Nodes</h2>' +
        '<p class="help-text">Nodes seen on the mesh for the very first time, newest first. Excludes nodes returning after a period of inactivity -- those aren\'t new, just quiet for a while. Click a column header to sort.</p>' +
        '<div style="margin:12px 0"><input type="text" id="new-nodes-filter" class="input" placeholder="Filter by name, pubkey, role, or area…" style="width:100%;max-width:420px"></div>' +
        '<div id="new-nodes-status" class="text-muted" style="font-size:12px;margin-bottom:8px"></div>' +
        '<div id="new-nodes-table-wrap" class="table-fluid-wrap"></div>' +
      '</div>';

    var filterInput = document.getElementById('new-nodes-filter');
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
  }

  function load() {
    var statusEl = document.getElementById('new-nodes-status');
    var wrap = document.getElementById('new-nodes-table-wrap');
    if (wrap) wrap.innerHTML = '<p class="text-muted">Loading…</p>';
    api('/analytics/new-nodes?limit=200')
      .then(function (data) {
        allRows = (data && Array.isArray(data.newNodes)) ? data.newNodes : [];
        renderTable();
      })
      .catch(function (e) {
        if (wrap) wrap.innerHTML = '';
        if (statusEl) statusEl.textContent = 'Failed to load: ' + escapeHtml(e.message);
      });
  }

  var ROLE_LABELS = { repeater: 'Repeater', room: 'Room Server', companion: 'Companion', sensor: 'Sensor', chat: 'Companion', none: 'None' };
  function roleLabel(role) {
    if (!role) return '';
    return ROLE_LABELS[role.toLowerCase()] || role;
  }

  function sortValue(row, col) {
    switch (col) {
      case 'name': return (row.name || row.publicKey || '').toLowerCase();
      case 'role': return (row.role || '').toLowerCase();
      case 'areas': return (row.areas || []).join(', ').toLowerCase();
      case 'firstSeen': return row.firstSeen || '';
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
    var haystack = [row.name, row.publicKey, row.role, (row.areas || []).join(' ')].filter(Boolean).join(' ').toLowerCase();
    return haystack.indexOf(filterText) !== -1;
  }

  function renderTable() {
    var statusEl = document.getElementById('new-nodes-status');
    var wrap = document.getElementById('new-nodes-table-wrap');
    if (!wrap) return;

    if (allRows.length === 0) {
      if (statusEl) statusEl.textContent = '';
      wrap.innerHTML = '<p class="text-muted">No new nodes yet.</p>';
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
      statusEl.textContent = filtered.length.toLocaleString() + ' of ' + allRows.length.toLocaleString() + ' new node' + (allRows.length === 1 ? '' : 's') +
        (filterText ? ' (filtered)' : '');
    }

    if (sorted.length === 0) {
      wrap.innerHTML = '<p class="text-muted">No rows match "' + escapeHtml(filterText) + '".</p>';
      return;
    }

    var rows = sorted.map(function (row) {
      var nameLabel = row.name ? escapeHtml(row.name) : escapeHtml(row.publicKey.slice(0, 8)) + '…';
      var nameCell = '<a href="#/nodes/' + encodeURIComponent(row.publicKey) + '">' + nameLabel + '</a>';
      var roleCell = row.role ? escapeHtml(roleLabel(row.role)) : '<span class="text-muted">—</span>';
      var areasCell = (row.areas && row.areas.length)
        ? row.areas.map(function (a) { return '<span class="badge-region">' + escapeHtml(a) + '</span>'; }).join(' ')
        : '<span class="text-muted">—</span>';
      var firstSeenCell = row.firstSeen
        ? '<span title="' + escapeHtml(row.firstSeen) + '">' + (typeof timeAgo === 'function' ? timeAgo(row.firstSeen) : escapeHtml(row.firstSeen)) + '</span>'
        : '<span class="text-muted">—</span>';

      return '<tr><td>' + nameCell + '</td><td>' + roleCell + '</td><td class="col-scope-list">' + areasCell + '</td><td>' + firstSeenCell + '</td></tr>';
    }).join('');

    wrap.innerHTML =
      '<table class="data-table" id="new-nodes-table"><thead><tr>' +
        sortTh('name', 'Node') +
        sortTh('role', 'Role') +
        '<th>Area</th>' +
        sortTh('firstSeen', 'First Seen') +
      '</tr></thead><tbody>' + rows + '</tbody></table>';

    var table = document.getElementById('new-nodes-table');
    if (table) {
      table.querySelectorAll('th[data-sort-col]').forEach(function (th) {
        th.addEventListener('click', function () {
          var col = th.dataset.sortCol;
          if (sortState.col === col) {
            sortState.dir = sortState.dir === 'asc' ? 'desc' : 'asc';
          } else {
            sortState.col = col;
            sortState.dir = (col === 'firstSeen') ? 'desc' : 'asc';
          }
          renderTable();
        });
      });
    }
  }

  window.NewNodesTool = { init: init, destroy: destroy, sortValue: sortValue, roleLabel: roleLabel };
  if (typeof registerPage === 'function') registerPage('new-nodes-tool', { init: init, destroy: destroy });
})();
