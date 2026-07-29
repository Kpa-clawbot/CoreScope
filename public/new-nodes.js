// New Nodes tool — most recently first-seen nodes network-wide (Tools >
// New Nodes). Backend: cmd/server/new_nodes.go, GET /api/analytics/new-nodes.
// Pattern modeled on observer-neighbors-tool.js (filter input + sortable
// data-table + status line), the closest existing Tools sub-page.
(function () {
  'use strict';

  var container = null;
  var allRows = [];
  var filterText = '';
  var originFilter = 'all'; // 'all' | 'domestic' | 'foreign' -- nodes.foreign_advert (#730)
  var knownRoles = []; // distinct role keys present in allRows, computed once per load()
  var enabledRoles = null; // Set of role keys currently checked; null until first computed (defaults to "all checked")
  var sortState = { col: 'firstSeen', dir: 'desc' };

  function escapeHtml(s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  var ORIGIN_TABS = [
    { key: 'all', label: 'All' },
    { key: 'domestic', label: 'Domestic' },
    { key: 'foreign', label: 'Foreign' },
  ];

  function originTabsHtml() {
    return ORIGIN_TABS.map(function (t) {
      return '<button type="button" class="tab-btn' + (t.key === originFilter ? ' active' : '') + '" data-origin="' + t.key + '">' + t.label + '</button>';
    }).join('');
  }

  var ROLE_LABELS = { repeater: 'Repeater', room: 'Room Server', companion: 'Companion', sensor: 'Sensor', chat: 'Companion', none: 'None', unknown: 'Unknown' };
  function roleLabel(role) {
    if (!role) return '';
    return ROLE_LABELS[role.toLowerCase()] || role;
  }

  // Role checkboxes are built from whatever roles actually appear in the
  // loaded data (not a hardcoded list) so a network with no sensors, say,
  // doesn't show a dead "Sensor" checkbox. All checked by default -- the
  // control narrows the view, it doesn't start empty.
  function roleCheckboxesHtml() {
    return knownRoles.map(function (role) {
      var checked = enabledRoles.has(role) ? ' checked' : '';
      var id = 'new-nodes-role-' + role;
      return '<label for="' + id + '" style="display:inline-flex;align-items:center;gap:4px;margin-right:14px;font-size:13px;cursor:pointer">' +
        '<input type="checkbox" id="' + id + '" data-role="' + role + '"' + checked + '> ' + escapeHtml(roleLabel(role)) +
        '</label>';
    }).join('');
  }

  function init(app) {
    container = app;
    allRows = [];
    filterText = '';
    originFilter = 'all';
    knownRoles = [];
    enabledRoles = null;
    sortState = { col: 'firstSeen', dir: 'desc' };

    container.innerHTML =
      '<div class="tools-landing" style="max-width:1100px">' +
        '<h2><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-rocket"/></svg> New Nodes</h2>' +
        '<p class="help-text">Nodes seen on the mesh for the very first time, newest first. Excludes nodes returning after a period of inactivity -- those aren\'t new, just quiet for a while. Click a column header to sort.</p>' +
        '<div id="new-nodes-origin-tabs" style="display:flex;gap:6px;margin:12px 0 8px">' + originTabsHtml() + '</div>' +
        '<div id="new-nodes-role-filters" style="margin:0 0 12px"></div>' +
        '<div style="margin:0 0 12px"><input type="text" id="new-nodes-filter" class="input" placeholder="Filter by name, pubkey, role, or area…" style="width:100%;max-width:420px"></div>' +
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

    // Delegated on the wrap (not per-button) so re-rendering the tab
    // buttons' HTML on selection change doesn't lose the listener.
    var tabsWrap = document.getElementById('new-nodes-origin-tabs');
    if (tabsWrap) {
      tabsWrap.addEventListener('click', function (evt) {
        var el = evt.target;
        if (el && el.closest) el = el.closest('[data-origin]');
        var origin = el && el.dataset ? el.dataset.origin : null;
        if (!origin || origin === originFilter) return;
        originFilter = origin;
        tabsWrap.innerHTML = originTabsHtml();
        renderTable();
      });
    }

    // Delegated the same way as the origin tabs -- roleCheckboxesHtml() is
    // only re-rendered on toggle (not on every keystroke), so a per-input
    // listener would be lost; this survives that.
    var roleFiltersWrap = document.getElementById('new-nodes-role-filters');
    if (roleFiltersWrap) {
      roleFiltersWrap.addEventListener('change', function (evt) {
        var el = evt.target;
        var role = el && el.dataset ? el.dataset.role : null;
        if (!role || !enabledRoles) return;
        if (el.checked) enabledRoles.add(role);
        else enabledRoles.delete(role);
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
        knownRoles = Array.from(new Set(allRows.map(function (r) { return (r.role || '').toLowerCase() || 'unknown'; }))).sort();
        enabledRoles = new Set(knownRoles);
        var roleFiltersWrap = document.getElementById('new-nodes-role-filters');
        if (roleFiltersWrap) roleFiltersWrap.innerHTML = roleCheckboxesHtml();
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

  function matchesOrigin(row) {
    if (originFilter === 'domestic') return !row.foreign;
    if (originFilter === 'foreign') return !!row.foreign;
    return true;
  }

  function matchesRole(row) {
    if (!enabledRoles) return true; // not computed yet (pre-load)
    var key = (row.role || '').toLowerCase() || 'unknown';
    return enabledRoles.has(key);
  }

  function matchesFilter(row) {
    if (!matchesOrigin(row)) return false;
    if (!matchesRole(row)) return false;
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

    var roleNarrowed = !!enabledRoles && enabledRoles.size < knownRoles.length;
    if (statusEl) {
      statusEl.textContent = filtered.length.toLocaleString() + ' of ' + allRows.length.toLocaleString() + ' new node' + (allRows.length === 1 ? '' : 's') +
        ((filterText || originFilter !== 'all' || roleNarrowed) ? ' (filtered)' : '');
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

    // Drag-resizable columns (widths persisted to localStorage), same
    // utility as the main Nodes/Observers/Packets tables -- dborup asked
    // to be able to widen the Node column to see the full name instead
    // of it ellipsis-truncating to fit.
    if (typeof makeColumnsResizable === 'function') makeColumnsResizable('#new-nodes-table', 'meshcore-new-nodes-col-widths');
  }

  window.NewNodesTool = { init: init, destroy: destroy, sortValue: sortValue, roleLabel: roleLabel };
  if (typeof registerPage === 'function') registerPage('new-nodes-tool', { init: init, destroy: destroy });
})();
