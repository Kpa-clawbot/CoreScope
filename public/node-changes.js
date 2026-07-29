// Node Changes tool — audit log of role/name/position changes and
// pruned-node returns, network-wide (Tools > Node Changes). Backend:
// cmd/server/node_changes.go, GET /api/analytics/node-changes. Written by
// cmd/ingestor's detectAndLogNodeChange as ADVERTs arrive.
// Pattern modeled on new-nodes.js (origin/type toggle + filter input +
// sortable data-table + status line).
(function () {
  'use strict';

  var container = null;
  var allRows = [];
  var filterText = '';
  var knownTypes = []; // distinct changeType keys present in allRows
  var enabledTypes = null; // Set of changeType keys currently checked; null until first computed
  var sortState = { col: 'detectedAt', dir: 'desc' };

  function escapeHtml(s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  var TYPE_LABELS = { role: 'Role', name: 'Name', position: 'Position', resurrected: 'Returned' };
  function typeLabel(t) {
    return TYPE_LABELS[t] || t;
  }

  // Type checkboxes are built from whatever change types actually appear
  // in the loaded data, all checked by default -- the control narrows the
  // view, it doesn't start empty.
  function typeCheckboxesHtml() {
    return knownTypes.map(function (t) {
      var checked = enabledTypes.has(t) ? ' checked' : '';
      var id = 'node-changes-type-' + t;
      return '<label for="' + id + '" style="display:inline-flex;align-items:center;gap:4px;margin-right:14px;font-size:13px;cursor:pointer">' +
        '<input type="checkbox" id="' + id + '" data-type="' + t + '"' + checked + '> ' + escapeHtml(typeLabel(t)) +
        '</label>';
    }).join('');
  }

  function init(app) {
    container = app;
    allRows = [];
    filterText = '';
    knownTypes = [];
    enabledTypes = null;
    sortState = { col: 'detectedAt', dir: 'desc' };

    container.innerHTML =
      '<div class="tools-landing" style="max-width:1100px">' +
        '<h2><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-pulse"/></svg> Node Changes</h2>' +
        '<p class="help-text">Audit log of role/name/position changes and nodes returning after being pruned for inactivity, network-wide. Written as it happens, not inferred from periodic snapshots. Click a column header to sort.</p>' +
        '<div id="node-changes-type-filters" style="margin:12px 0"></div>' +
        '<div style="margin:0 0 12px"><input type="text" id="node-changes-filter" class="input" placeholder="Filter by name or pubkey…" style="width:100%;max-width:420px"></div>' +
        '<div id="node-changes-status" class="text-muted" style="font-size:12px;margin-bottom:8px"></div>' +
        '<div id="node-changes-table-wrap" class="table-fluid-wrap"></div>' +
      '</div>';

    var filterInput = document.getElementById('node-changes-filter');
    if (filterInput) {
      filterInput.addEventListener('input', function () {
        filterText = filterInput.value.toLowerCase();
        renderTable();
      });
    }

    // Delegated on the wrap (not per-checkbox) so re-rendering the
    // checkbox HTML on load doesn't lose the listener.
    var typeFiltersWrap = document.getElementById('node-changes-type-filters');
    if (typeFiltersWrap) {
      typeFiltersWrap.addEventListener('change', function (evt) {
        var el = evt.target;
        var t = el && el.dataset ? el.dataset.type : null;
        if (!t || !enabledTypes) return;
        if (el.checked) enabledTypes.add(t);
        else enabledTypes.delete(t);
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
    var statusEl = document.getElementById('node-changes-status');
    var wrap = document.getElementById('node-changes-table-wrap');
    if (wrap) wrap.innerHTML = '<p class="text-muted">Loading…</p>';
    api('/analytics/node-changes?limit=200')
      .then(function (data) {
        allRows = (data && Array.isArray(data.nodeChanges)) ? data.nodeChanges : [];
        knownTypes = Array.from(new Set(allRows.map(function (r) { return r.changeType; }))).sort();
        enabledTypes = new Set(knownTypes);
        var typeFiltersWrap = document.getElementById('node-changes-type-filters');
        if (typeFiltersWrap) typeFiltersWrap.innerHTML = typeCheckboxesHtml();
        renderTable();
      })
      .catch(function (e) {
        if (wrap) wrap.innerHTML = '';
        if (statusEl) statusEl.textContent = 'Failed to load: ' + escapeHtml(e.message);
      });
  }

  function sortValue(row, col) {
    switch (col) {
      case 'name': return (row.name || row.publicKey || '').toLowerCase();
      case 'changeType': return (row.changeType || '').toLowerCase();
      case 'detectedAt': return row.detectedAt || '';
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

  function matchesType(row) {
    if (!enabledTypes) return true; // not computed yet (pre-load)
    return enabledTypes.has(row.changeType);
  }

  function matchesFilter(row) {
    if (!matchesType(row)) return false;
    if (!filterText) return true;
    var haystack = [row.name, row.publicKey].filter(Boolean).join(' ').toLowerCase();
    return haystack.indexOf(filterText) !== -1;
  }

  // detailHtml renders the human-readable "what changed" cell, distinct
  // per changeType since old/new mean different things for each.
  function detailHtml(row) {
    if (row.changeType === 'position') {
      var km = row.distanceKm != null ? row.distanceKm.toFixed(1) + ' km' : '';
      return 'Moved' + (km ? ' ' + km : '');
    }
    if (row.changeType === 'resurrected') {
      var lastSeen = row.oldValue
        ? '<span title="' + escapeHtml(row.oldValue) + '">' + (typeof timeAgo === 'function' ? timeAgo(row.oldValue) : escapeHtml(row.oldValue)) + '</span>'
        : '';
      return 'Returned' + (lastSeen ? ' (last seen ' + lastSeen + ')' : '');
    }
    // role / name
    var oldV = row.oldValue ? escapeHtml(row.oldValue) : '?';
    var newV = row.newValue ? escapeHtml(row.newValue) : '?';
    return oldV + ' <span class="arrow">&rarr;</span> ' + newV;
  }

  function renderTable() {
    var statusEl = document.getElementById('node-changes-status');
    var wrap = document.getElementById('node-changes-table-wrap');
    if (!wrap) return;

    if (allRows.length === 0) {
      if (statusEl) statusEl.textContent = '';
      wrap.innerHTML = '<p class="text-muted">No node changes recorded yet.</p>';
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

    var typeNarrowed = !!enabledTypes && enabledTypes.size < knownTypes.length;
    if (statusEl) {
      statusEl.textContent = filtered.length.toLocaleString() + ' of ' + allRows.length.toLocaleString() + ' change' + (allRows.length === 1 ? '' : 's') +
        ((filterText || typeNarrowed) ? ' (filtered)' : '');
    }

    if (sorted.length === 0) {
      wrap.innerHTML = '<p class="text-muted">No rows match "' + escapeHtml(filterText) + '".</p>';
      return;
    }

    var rows = sorted.map(function (row) {
      var nameLabel = row.name ? escapeHtml(row.name) : escapeHtml(row.publicKey.slice(0, 8)) + '…';
      var nameCell = '<a href="#/nodes/' + encodeURIComponent(row.publicKey) + '">' + nameLabel + '</a>';
      var typeCell = '<span class="badge-region">' + escapeHtml(typeLabel(row.changeType)) + '</span>';
      var detectedCell = row.detectedAt
        ? '<span title="' + escapeHtml(row.detectedAt) + '">' + (typeof timeAgo === 'function' ? timeAgo(row.detectedAt) : escapeHtml(row.detectedAt)) + '</span>'
        : '<span class="text-muted">—</span>';

      return '<tr><td>' + nameCell + '</td><td>' + typeCell + '</td><td>' + detailHtml(row) + '</td><td>' + detectedCell + '</td></tr>';
    }).join('');

    wrap.innerHTML =
      '<table class="data-table" id="node-changes-table"><thead><tr>' +
        sortTh('name', 'Node') +
        sortTh('changeType', 'Change') +
        '<th>Detail</th>' +
        sortTh('detectedAt', 'Detected') +
      '</tr></thead><tbody>' + rows + '</tbody></table>';

    var table = document.getElementById('node-changes-table');
    if (table) {
      table.querySelectorAll('th[data-sort-col]').forEach(function (th) {
        th.addEventListener('click', function () {
          var col = th.dataset.sortCol;
          if (sortState.col === col) {
            sortState.dir = sortState.dir === 'asc' ? 'desc' : 'asc';
          } else {
            sortState.col = col;
            sortState.dir = (col === 'detectedAt') ? 'desc' : 'asc';
          }
          renderTable();
        });
      });
    }
  }

  window.NodeChangesTool = { init: init, destroy: destroy, sortValue: sortValue, typeLabel: typeLabel };
  if (typeof registerPage === 'function') registerPage('node-changes-tool', { init: init, destroy: destroy });
})();
