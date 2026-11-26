/* Neighbor Rank tab (#2100): every ranked node ordered by all-time neighbour count.
   Pure helpers (buildQuery, parseHashState, rowHtml, pagerText) have no DOM
   dependency; render(el) wires the tab. Deep link:
   #/analytics?tab=neighbor-rank&q=<q>&role=<role>&page=<n>&focus=<pubkey> */
(function () {
  'use strict';

  var PAGE_SIZE = 50;
  var MAX_Q = 64;
  var ROLES = ['repeater', 'room', 'companion', 'sensor'];
  var ROLE_LABELS = { '': 'All', repeater: 'Repeater', room: 'Room', companion: 'Companion', sensor: 'Sensor' };

  function esc(s) {
    if (typeof escapeHtml === 'function') return escapeHtml(s);
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function normState(s) {
    s = s || {};
    // Code points, as the server counts them (no lone surrogate from a cut emoji).
    var q = Array.from(String(s.q == null ? '' : s.q).trim()).slice(0, MAX_Q).join('');
    var role = ROLES.indexOf(s.role) >= 0 ? s.role : '';
    var page = parseInt(s.page, 10);
    if (!isFinite(page) || page < 1) page = 1;
    var focus = /^[0-9a-fA-F]{1,64}$/.test(String(s.focus || '')) ? String(s.focus).toLowerCase() : '';
    return { q: q, role: role, page: page, focus: focus };
  }

  // State -> API query string. Empty q/role are omitted.
  function buildQuery(state) {
    var s = normState(state);
    var parts = [];
    if (s.q) parts.push('q=' + encodeURIComponent(s.q));
    if (s.role) parts.push('role=' + s.role);
    parts.push('offset=' + (s.page - 1) * PAGE_SIZE);
    parts.push('limit=' + PAGE_SIZE);
    // The server picks the page that holds this node (rank ties make a client-side guess wrong).
    if (/^[0-9a-f]{64}$/.test(s.focus)) parts.push('focus=' + s.focus);
    return parts.join('&');
  }

  // Hash query string ("tab=neighbor-rank&q=x" or with a leading "?") -> state.
  function parseHashState(hashQuery) {
    var qs = String(hashQuery == null ? '' : hashQuery);
    var i = qs.indexOf('?');
    if (i >= 0) qs = qs.slice(i + 1);
    var p = new URLSearchParams(qs);
    return normState({ q: p.get('q'), role: p.get('role'), page: p.get('page'), focus: p.get('focus') });
  }

  // State -> URL hash params; empty/default values map to '' (removed).
  function hashUpdates(state) {
    var s = normState(state);
    return { q: s.q, role: s.role, page: s.page > 1 ? String(s.page) : '', focus: s.focus };
  }

  function displayName(e) {
    if (e.name) return e.name;
    return String(e.pubkey || '').slice(0, 12) + '…';
  }

  function rowHtml(e, focusPubkey) {
    var pk = String(e.pubkey || '');
    var pkEnc = encodeURIComponent(pk);
    var focused = focusPubkey && pk.toLowerCase() === String(focusPubkey).toLowerCase();
    var role = e.role ? esc(e.role) : '<span class="text-muted">—</span>';
    return '<tr' + (focused ? ' class="nr-focus" aria-current="true"' : '') + ' data-pubkey="' + esc(pk) + '">' +
      '<td class="nr-rank nr-c-rank">#' + esc(e.rank) + '</td>' +
      '<td><a href="#/nodes/' + pkEnc + '" class="nr-name">' + esc(displayName(e)) + '</a></td>' +
      '<td class="nr-c-role">' + role + '</td>' +
      '<td class="nr-num nr-c-num">' + esc(e.neighbors) + '</td>' +
      '<td><a href="#/nodes/' + pkEnc + '/reach" class="nr-reach">Reach</a></td>' +
      '</tr>';
  }

  // "Showing 51–100 of 812"; count = rows on this page.
  function pagerText(page, count, total) {
    if (!total || !count) return 'Showing 0 of ' + (total || 0);
    var start = (page - 1) * PAGE_SIZE + 1;
    return 'Showing ' + start + '–' + (start + count - 1) + ' of ' + total;
  }

  function fmtTime(iso) {
    if (!iso) return '—';
    if (typeof formatAbsoluteTimestamp === 'function') return formatAbsoluteTimestamp(iso);
    var d = new Date(iso);
    return isFinite(d.getTime()) ? d.toLocaleString() : '—';
  }

  function errorMessage(err) {
    var st = err && err.status;
    if (st === 400) return 'The search was not valid. Change it and try again.';
    if (st === 503) return 'The ranking is still being prepared. Try again in a moment.';
    return 'Could not load the neighbour ranking.';
  }

  function render(el) {
    var state = parseHashState(location.hash);
    var reqId = 0;
    var timer = null;
    var lastTotal = 0;
    // A focus link with no explicit page: let the server choose the page.
    var focusPending = !!state.focus && !/[?&]page=/.test(location.hash);

    el.innerHTML =
      '<div class="nr-wrap">' +
      '<p class="nr-explain text-muted">Ranked by all-time neighbour count — a historical count, not a measure of radio quality or range.</p>' +
      '<div class="nr-controls">' +
      '<label class="nr-search-label" for="nrSearch">Search</label>' +
      '<input id="nrSearch" class="nr-search" type="search" maxlength="' + MAX_Q + '" placeholder="Name or public key" autocomplete="off" value="' + esc(state.q) + '">' +
      '<div class="nr-chips" role="group" aria-label="Filter by role">' +
      [''].concat(ROLES).map(function (r) {
        return '<button type="button" class="nr-chip" data-role="' + r + '" aria-pressed="false">' + ROLE_LABELS[r] + '</button>';
      }).join('') +
      '</div></div>' +
      '<div id="nrStatus" class="nr-status" role="status" aria-live="polite"></div>' +
      '<div class="nr-table-wrap"><table class="analytics-table nr-table" data-resizable="1">' +
      '<thead><tr><th scope="col" class="nr-c-rank">Rank</th><th scope="col">Name</th><th scope="col" class="nr-c-role">Role</th><th scope="col" class="nr-c-num">Neighbors</th><th scope="col"><span class="nr-sr">Reach page</span></th></tr></thead>' +
      '<tbody id="nrRows"></tbody></table></div>' +
      '<div class="nr-pager"><button type="button" id="nrPrev" class="nr-btn">Prev</button>' +
      '<span id="nrPagerText" class="nr-pager-text"></span>' +
      '<button type="button" id="nrNext" class="nr-btn">Next</button></div>' +
      '<div class="nr-foot text-muted"><span id="nrSnap"></span> <span id="nrRanked"></span></div>' +
      '</div>';

    var $ = function (id) { return el.querySelector('#' + id); };
    var rows = $('nrRows'), status = $('nrStatus'), prev = $('nrPrev'), next = $('nrNext');
    // el is the shared #analyticsContent, still connected on every other tab;
    // only this render's own rows say whether the tab is still the one shown.
    function live() { return rows.isConnected; }

    function syncChips() {
      el.querySelectorAll('.nr-chip').forEach(function (b) {
        b.setAttribute('aria-pressed', b.dataset.role === state.role ? 'true' : 'false');
      });
    }

    function syncUrl() {
      if (!window.URLState || typeof history === 'undefined') return;
      var h = URLState.updateHashParams(hashUpdates(state), location.hash);
      if (h !== location.hash) history.replaceState(null, '', h);
    }

    function setStatus(html) { status.innerHTML = html; }

    function showError(err) {
      rows.innerHTML = '';
      prev.disabled = next.disabled = true;
      $('nrPagerText').textContent = '';
      setStatus('<div class="nr-error" role="alert">' + esc(errorMessage(err)) +
        ' <button type="button" class="nr-btn" id="nrRetry">Retry</button></div>');
      var r = $('nrRetry');
      if (r) r.addEventListener('click', load);
    }

    function load() {
      var id = ++reqId;
      syncChips();
      setStatus('<span class="text-muted">Loading…</span>');
      var pendingNow = focusPending;
      var qs = buildQuery(pendingNow ? state : Object.assign({}, state, { focus: '' }));
      api('/nodes/neighbor-rank?' + qs, { bust: true }).then(function (d) {
        if (id !== reqId || !live()) return;
        var entries = (d && d.entries) || [];
        var total = (d && d.total) || 0;
        lastTotal = total;
        if (pendingNow) {
          focusPending = false;
          if (typeof d.offset === 'number' && d.offset >= 0) {
            state.page = Math.floor(d.offset / PAGE_SIZE) + 1;
            syncUrl();
          }
        }
        // Page past the end (stale deep link): jump to the last page.
        if (!entries.length && total > 0 && state.page > 1) {
          state.page = Math.ceil(total / PAGE_SIZE);
          syncUrl();
          return load();
        }
        rows.innerHTML = entries.map(function (e) { return rowHtml(e, state.focus); }).join('');
        if (!entries.length) {
          var filtered = state.q || state.role;
          setStatus('<div class="nr-empty text-muted">' + (filtered
            ? 'No nodes match this search. Clear the search or choose another role.'
            : 'No nodes have neighbour data yet.') + '</div>');
        } else {
          setStatus('');
        }
        $('nrPagerText').textContent = pagerText(state.page, entries.length, total);
        prev.disabled = state.page <= 1;
        next.disabled = state.page * PAGE_SIZE >= total;
        $('nrSnap').textContent = d && d.snapshot_at ? 'Snapshot: ' + fmtTime(d.snapshot_at) + ' · ' : '';
        $('nrRanked').textContent = ((d && d.ranked) || 0) + ' ranked nodes';
        var f = rows.querySelector('.nr-focus');
        if (f && f.scrollIntoView) f.scrollIntoView({ block: 'center' });
      }).catch(function (err) {
        if (id !== reqId || !live()) return;
        showError(err);
      });
    }

    function change(patch) {
      // A debounced search or a click that outlives the tab changes nothing.
      if (!live()) return;
      focusPending = false;
      for (var k in patch) state[k] = patch[k];
      state = normState(state);
      syncUrl();
      load();
    }

    $('nrSearch').addEventListener('input', function (e) {
      var v = e.target.value;
      clearTimeout(timer);
      timer = setTimeout(function () { change({ q: v, page: 1, focus: '' }); }, 250);
    });
    el.querySelector('.nr-chips').addEventListener('click', function (e) {
      var b = e.target.closest('.nr-chip');
      if (b) change({ role: b.dataset.role, page: 1, focus: '' });
    });
    prev.addEventListener('click', function () { if (state.page > 1) change({ page: state.page - 1, focus: '' }); });
    next.addEventListener('click', function () { if (state.page * PAGE_SIZE < lastTotal) change({ page: state.page + 1, focus: '' }); });

    syncChips();
    load();
  }

  window.NeighborRank = { render: render, buildQuery: buildQuery, parseHashState: parseHashState, hashUpdates: hashUpdates, rowHtml: rowHtml, pagerText: pagerText, PAGE_SIZE: PAGE_SIZE };
})();
