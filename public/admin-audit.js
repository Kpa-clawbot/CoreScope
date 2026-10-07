/* Audit tab of #/admin (docs/specs/2026-10-07-admin-dashboard-design.md):
 * the global audit log, newest first, with "Load more" (keyset paging on the
 * server's next id). Filters live in the hash:
 * #/admin?tab=audit&action=&user=&period= (invalid values are ignored).
 * Mounted by admin.js after its admin check. Every dynamic value goes
 * through escapeHtml. */
(function () {
  'use strict';
  var ACTIONS = [
    ['', 'All actions'],
    ['user.login.*', 'Logins (all)'],
    ['user.login', 'Successful logins'],
    ['user.login.failed', 'Failed logins'],
    ['user.register', 'Registrations'],
    ['user.activate.*', 'Activations'],
    ['user.activation.resend', 'Activation mail resent'],
    ['user.disable', 'Disabled'],
    ['user.enable', 'Enabled'],
    ['user.delete.*', 'Deletions'],
    ['user.role.*', 'Role changes'],
    ['user.password.*', 'Password changes and resets'],
    ['user.email.*', 'Address changes'],
    ['user.mail.refresh', 'Mail status refreshed']
  ];
  var DAY_MS = 24 * 60 * 60 * 1000;
  var PERIODS = [['', 'Any time', 0], ['24h', 'Last 24 hours', DAY_MS], ['7d', 'Last 7 days', 7 * DAY_MS], ['30d', 'Last 30 days', 30 * DAY_MS]];
  var PAGE = 100;
  var state = { filters: { action: '', user: '', period: '' }, next: null, seq: 0 };

  function known(list, v) {
    for (var i = 0; i < list.length; i++) if (list[i][0] === v) return list[i];
    return null;
  }

  function readHash(hash) {
    var p = new URLSearchParams(String(hash || '').split('?')[1] || '');
    var a = known(ACTIONS, p.get('action'));
    var per = known(PERIODS, p.get('period'));
    var user = p.get('user') || '';
    return { action: a ? a[0] : '', user: /^\d+$/.test(user) ? user : '', period: per ? per[0] : '' };
  }

  function hashFor(f) {
    var p = new URLSearchParams();
    p.set('tab', 'audit');
    if (f.action) p.set('action', f.action);
    if (f.user) p.set('user', f.user);
    if (f.period) p.set('period', f.period);
    return '#/admin?' + p.toString();
  }

  function apiPath(f, before, nowMs) {
    var p = new URLSearchParams();
    if (f.action) p.set('action', f.action);
    if (f.user) p.set('user', f.user);
    var per = f.period ? known(PERIODS, f.period) : null;
    if (per) p.set('from', new Date(nowMs - per[2]).toISOString());
    if (before) p.set('before', String(before));
    p.set('limit', String(PAGE));
    return '/api/admin/audit?' + p.toString();
  }

  function refHtml(ref) {
    if (!ref) return '<span class="account-hint">system</span>';
    var id = escapeHtml(String(ref.id));
    if (ref.deleted) return '#' + id + ' <span class="account-hint">(deleted)</span>';
    return '<a href="#/admin?tab=users&amp;id=' + id + '">' + escapeHtml(ref.displayName) + '</a> <span class="account-hint">' + escapeHtml(ref.email) + '</span>';
  }

  function detailText(d) {
    return Object.keys(d || {}).sort().map(function (k) { return k + '=' + d[k]; }).join(', ');
  }

  function rowHtml(e) {
    return '<tr data-action="' + escapeHtml(e.action) + '">' +
      '<td>' + escapeHtml(new Date(e.at).toLocaleString()) + '</td>' +
      '<td>' + escapeHtml(e.action) + '</td>' +
      '<td>' + refHtml(e.actor) + '</td>' +
      '<td>' + refHtml(e.target) + '</td>' +
      '<td>' + escapeHtml(detailText(e.detail)) + '</td></tr>';
  }

  function optionsHtml(list) {
    return list.map(function (o) { return '<option value="' + escapeHtml(o[0]) + '">' + escapeHtml(o[1]) + '</option>'; }).join('');
  }

  function say(text) { CSAuth.say('auditMsg', text, false); }

  function load(more) {
    var seq = ++state.seq;
    CSAuth.say('auditMsg', '', true);
    return CSAuth.request('GET', apiPath(state.filters, more ? state.next : null, Date.now())).then(function (r) {
      var body = document.getElementById('auditBody');
      if (!body || seq !== state.seq) return;
      if (!r.ok) { say(CSAuth.errText(r)); return; }
      var html = '';
      r.data.entries.forEach(function (e) { html += rowHtml(e); });
      // Appending keeps the rows on screen; a page is at most PAGE rows.
      if (more) body.insertAdjacentHTML('beforeend', html);
      else body.innerHTML = html || '<tr><td colspan="5">No entries match.</td></tr>';
      state.next = r.data.next;
      document.getElementById('auditMore').hidden = r.data.next == null;
    }).catch(function () { if (seq === state.seq) say('Network error, try again.'); });
  }

  function setFilter(key, value) {
    state.filters[key] = value;
    var h = hashFor(state.filters);
    if (h !== location.hash) history.replaceState(null, '', h);
    state.next = null;
    document.getElementById('auditMore').hidden = true;
    document.getElementById('auditBody').innerHTML = '';
    return load(false);
  }

  function mount(container) {
    state.filters = readHash(location.hash);
    state.next = null;
    container.innerHTML = '<div class="admin-audit">' +
      '<div class="um-filters">' +
      '<label>Action <select id="auditAction">' + optionsHtml(ACTIONS) + '</select></label>' +
      '<label>Period <select id="auditPeriod">' + optionsHtml(PERIODS) + '</select></label>' +
      '<label>User id <input id="auditUser" type="text" inputmode="numeric" autocomplete="off"></label></div>' +
      '<p class="account-msg" id="auditMsg" role="status" aria-live="polite"></p>' +
      '<div class="um-table-wrap"><table class="um-table"><thead><tr>' +
      '<th scope="col">Time</th><th scope="col">Action</th><th scope="col">By</th><th scope="col">Account</th><th scope="col">Detail</th>' +
      '</tr></thead><tbody id="auditBody"></tbody></table></div>' +
      '<button type="button" class="account-btn account-btn-secondary" id="auditMore" hidden>Load more</button></div>';
    document.getElementById('auditAction').value = state.filters.action;
    document.getElementById('auditPeriod').value = state.filters.period;
    document.getElementById('auditUser').value = state.filters.user;
    document.getElementById('auditAction').addEventListener('change', function (ev) { setFilter('action', ev.target.value); });
    document.getElementById('auditPeriod').addEventListener('change', function (ev) { setFilter('period', ev.target.value); });
    document.getElementById('auditUser').addEventListener('change', function (ev) {
      var v = String(ev.target.value || '').trim();
      if (v === '' || /^\d+$/.test(v)) setFilter('user', v);
      else ev.target.value = state.filters.user;
    });
    document.getElementById('auditMore').addEventListener('click', function () { load(true); });
    return load(false);
  }

  function unmount() { state.seq++; state.next = null; }

  window.CSAdminAudit = { mount: mount, unmount: unmount,
    _test: { readHash: readHash, hashFor: hashFor, apiPath: apiPath, rowHtml: rowHtml, refHtml: refHtml, detailText: detailText } };
})();
