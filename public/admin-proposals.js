/* Proposals tab of #/admin (docs/specs/2026-10-07-channel-proposals-design.md):
 * hashtag channels users proposed, with approve / reject / revoke and an
 * optional note. Approving asks first: the channel becomes readable for every
 * visitor of this instance. Deep link: #/admin?tab=proposals&status=<status>
 * (pending by default; all = no filter). Mounted by admin.js after its admin
 * check. Every dynamic value goes through escapeHtml; messages use textContent. */
(function () {
  'use strict';
  var STATUSES = [['pending', 'Pending'], ['approved', 'Approved'], ['rejected', 'Rejected'], ['revoked', 'Revoked'], ['all', 'All']];
  var ACTIONS = { pending: ['approve', 'reject'], approved: ['revoke'] };
  var LABELS = { approve: 'Approve', reject: 'Reject', revoke: 'Revoke' };
  var DONE = { approve: 'approved', reject: 'rejected', revoke: 'revoked' };
  var state = { status: 'pending', seq: 0, byId: {} };

  function readHash(hash) {
    var s = new URLSearchParams(String(hash || '').split('?')[1] || '').get('status');
    for (var i = 0; i < STATUSES.length; i++) if (STATUSES[i][0] === s) return s;
    return 'pending';
  }
  function hashFor(status) { return '#/admin?tab=proposals&status=' + encodeURIComponent(status); }
  function apiPath(status) { return '/api/admin/proposals' + (status === 'all' ? '' : '?status=' + encodeURIComponent(status)); }

  function refHtml(ref) {
    if (!ref || ref.deleted) return '<span class="account-hint">deleted account</span>';
    return '<a href="#/admin?tab=users&amp;id=' + escapeHtml(String(ref.id)) + '">' + escapeHtml(ref.displayName) + '</a>';
  }

  function approveWarning(subject) {
    return 'Approving ' + subject + ' makes its messages readable for every visitor of this instance, ' +
      'from the ingestor\'s next refresh (about a minute) on. Approve?';
  }

  function rowHtml(p) {
    var id = escapeHtml(String(p.id));
    var acts = (ACTIONS[p.status] || []).map(function (a) {
      return '<button type="button" class="account-btn ' + (a === 'approve' ? 'account-btn-primary' : 'account-btn-secondary') +
        '" data-act="' + a + '" data-id="' + id + '">' + LABELS[a] + '</button>';
    }).join(' ');
    var decided = p.decidedAt ? escapeHtml(window.CSProposals.fmtDate(p.decidedAt)) + ' by ' + refHtml(p.reviewer) : '';
    return '<tr data-id="' + id + '" data-subject="' + escapeHtml(p.subject) + '" data-status="' + escapeHtml(p.status) + '">' +
      '<td>' + escapeHtml(p.subject) + '</td>' +
      '<td>' + window.CSProposals.statusChip(p.status) + '</td>' +
      '<td>' + refHtml(p.proposer) + '<br><small>' + escapeHtml(window.CSProposals.fmtDate(p.createdAt)) + '</small></td>' +
      '<td>' + decided + (p.note ? '<br><small>' + escapeHtml(p.note) + '</small>' : '') + '</td>' +
      '<td>' + (acts ? '<input type="text" class="um-note" data-note="' + id + '" maxlength="500" placeholder="Note (optional)" aria-label="Note for ' +
        escapeHtml(p.subject) + '"> ' + acts : '') + '</td></tr>';
  }

  function optionsHtml() {
    return STATUSES.map(function (s) { return '<option value="' + s[0] + '">' + s[1] + '</option>'; }).join('');
  }

  function say(text, ok) { CSAuth.say('propAdminMsg', text, ok); }

  function load() {
    var seq = ++state.seq;
    return CSAuth.request('GET', apiPath(state.status)).then(function (r) {
      var body = document.getElementById('propAdminBody');
      if (!body || seq !== state.seq) return;
      if (!r.ok) { say(CSAuth.errText(r), false); return; }
      state.byId = {};
      r.data.forEach(function (p) { state.byId[p.id] = p; });
      // At most 500 rows (server cap); re-rendered after each action.
      body.innerHTML = r.data.map(rowHtml).join('') || '<tr><td colspan="5">No proposals.</td></tr>';
    }).catch(function () { if (seq === state.seq) say('Network error, try again.', false); });
  }

  function act(id, action, confirmFn) {
    var p = state.byId[id];
    if (!p) return Promise.resolve();
    if (action === 'approve' && !confirmFn(approveWarning(p.subject))) return Promise.resolve();
    var noteEl = document.querySelector('[data-note="' + id + '"]');
    var note = noteEl ? String(noteEl.value || '').trim() : '';
    return CSAuth.request('POST', '/api/admin/proposals/' + encodeURIComponent(id) + '/' + action, { note: note }).then(function (r) {
      if (!r.ok) { say(CSAuth.errText(r), false); return; }
      say(p.subject + ': ' + DONE[action], true);
      return load();
    }).catch(function () { say('Network error, try again.', false); });
  }

  function setStatus(v) {
    state.status = readHash('?status=' + v);
    var h = hashFor(state.status);
    if (h !== location.hash) history.replaceState(null, '', h);
    return load();
  }

  function mount(container) {
    state.status = readHash(location.hash);
    state.byId = {};
    container.innerHTML = '<div class="admin-proposals">' +
      '<p class="account-hint">Hashtag channels users proposed. Approving one makes its messages readable for every visitor of this instance.</p>' +
      '<div class="um-filters"><label>Status <select id="propAdminStatus">' + optionsHtml() + '</select></label></div>' +
      '<p class="account-msg" id="propAdminMsg" role="status" aria-live="polite"></p>' +
      '<div class="um-table-wrap"><table class="um-table"><thead><tr>' +
      '<th scope="col">Channel</th><th scope="col">Status</th><th scope="col">Proposed by</th><th scope="col">Decision</th><th scope="col">Actions</th>' +
      '</tr></thead><tbody id="propAdminBody"></tbody></table></div></div>';
    document.getElementById('propAdminStatus').value = state.status;
    document.getElementById('propAdminStatus').addEventListener('change', function (ev) { return setStatus(ev.target.value); });
    document.getElementById('propAdminBody').addEventListener('click', function (ev) {
      var b = ev.target && ev.target.closest ? ev.target.closest('[data-act]') : null;
      if (!b) return;
      act(b.getAttribute('data-id'), b.getAttribute('data-act'), function (m) { return window.confirm(m); });
    });
    return load();
  }

  function unmount() { state.seq++; state.byId = {}; }

  window.CSAdminProposals = { mount: mount, unmount: unmount,
    _test: { readHash: readHash, hashFor: hashFor, apiPath: apiPath, rowHtml: rowHtml, refHtml: refHtml,
      approveWarning: approveWarning, act: act, setStatus: setStatus } };
})();
