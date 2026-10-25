/* Observer logins tab of #/admin (docs/superpowers/specs/2026-10-25-observer-mqtt-logins-design.md):
 * personal MQTT logins with Revoke / Reissue, and every observer with its
 * owner ("not migrated" without a binding, "orphaned" when the owner's
 * account is gone) and a select to reassign or unbind it. One
 * GET /api/admin/observer-logins and one GET /api/observers per load, joined
 * client-side. Revoke and Reissue ask inline (no browser dialog). Deep link:
 * #/admin?tab=observer-logins. Mounted by admin.js after its admin check.
 * Every dynamic value goes through escapeHtml; messages use textContent. */
(function () {
  'use strict';
  var KEY_RE = /^[0-9A-Fa-f]{64}$/;
  var CONFIRM = {
    revoke: ['Revoke this login? Its observers can no longer connect with it and stay bound to this user.', 'Revoke'],
    reissue: ['Mail a new username and password? The current ones stop working.', 'Reissue']
  };
  var DONE = { revoke: 'revoked', reissue: 'reissued, the new credentials were mailed' };
  var state = { seq: 0, logins: [], rows: [], confirm: null, busy: false };

  function fmt(iso) { if (!iso) return '—'; try { return new Date(iso).toLocaleString(); } catch (_) { return iso; } }
  function shortKey(pk) { return String(pk || '').slice(0, 12) + '…'; }
  function say(text, ok) { CSAuth.say('obsAdminMsg', text, ok); }

  // joinObservers merges the analyzer's observers with the bindings, keyed by
  // uppercase pubkey; a binding without an analyzer row still gets a row.
  // Each row: { pubkey, name, lastSeen, bound, userId }.
  function joinObservers(observers, bindings) {
    var byKey = {};
    var rows = [];
    (observers || []).forEach(function (o) {
      var k = String(o.id || '').toUpperCase();
      if (!k || byKey[k]) return;
      byKey[k] = { pubkey: k, name: o.name || '', lastSeen: o.last_seen || null, bound: false, userId: null };
      rows.push(byKey[k]);
    });
    (bindings || []).forEach(function (b) {
      var k = String(b.pubkey || '').toUpperCase();
      var row = byKey[k];
      if (!row) {
        row = byKey[k] = { pubkey: k, name: '', lastSeen: null, bound: false, userId: null };
        rows.push(row);
      }
      row.bound = true;
      row.userId = b.userId == null ? null : b.userId;
    });
    return rows;
  }

  // ownerLabel: the owning user's name, "orphaned" for a binding whose
  // account was deleted, "not migrated" for an observer without a binding.
  function ownerLabel(row, loginsById) {
    if (!row.bound) return 'not migrated';
    if (row.userId == null) return 'orphaned';
    var l = loginsById[row.userId];
    return l ? l.displayName : 'user #' + row.userId;
  }

  function loginsById(logins) {
    var m = {};
    (logins || []).forEach(function (l) { m[l.userId] = l; });
    return m;
  }

  function loginRowHtml(l, confirm) {
    var id = escapeHtml(String(l.userId));
    var acts;
    if (confirm && confirm.userId === l.userId) {
      var c = CONFIRM[confirm.act];
      acts = '<span class="um-confirm" role="group" aria-label="Confirm">' + escapeHtml(c[0]) + ' ' +
        '<button type="button" class="account-btn account-btn-primary" data-act="' + confirm.act + '-confirm" data-id="' + id + '">' + c[1] + '</button>' +
        '<button type="button" class="account-btn account-btn-secondary" data-act="cancel" data-id="' + id + '">Cancel</button></span>';
    } else {
      acts = (l.revoked ? '' : '<button type="button" class="account-btn account-btn-secondary" data-act="revoke" data-id="' + id + '">Revoke</button>') +
        '<button type="button" class="account-btn account-btn-secondary" data-act="reissue" data-id="' + id + '">Reissue</button>';
    }
    return '<tr data-id="' + id + '" data-username="' + escapeHtml(l.username) + '">' +
      '<td><a href="#/admin?tab=users&amp;id=' + id + '">' + escapeHtml(l.displayName) + '</a><br><small>' + escapeHtml(l.email) + '</small></td>' +
      '<td><code>' + escapeHtml(l.username) + '</code>' +
        (l.revoked ? ' <span class="um-status um-status-disabled">revoked</span>' : '') + '</td>' +
      '<td class="um-col-optional">' + escapeHtml(fmt(l.createdAt)) + '</td>' +
      '<td class="um-col-optional">' + escapeHtml(l.lastAuthAt ? fmt(l.lastAuthAt) : 'never') + '</td>' +
      '<td>' + escapeHtml(String((l.observers || []).length)) + '</td>' +
      '<td><div class="um-actions">' + acts + '</div></td></tr>';
  }

  // The select offers every user with a login, plus unbinding. An orphaned
  // binding shows a disabled "orphaned" entry until it is reassigned.
  function ownerSelectHtml(row, logins) {
    var cur = row.bound && row.userId != null ? String(row.userId) : '';
    var html = '<select data-pubkey="' + escapeHtml(row.pubkey) + '" aria-label="Owner of ' + escapeHtml(row.name || shortKey(row.pubkey)) + '">';
    if (row.bound && row.userId == null) html += '<option value="orphaned" disabled selected>orphaned</option>';
    html += '<option value=""' + (!row.bound ? ' selected' : '') + '>not bound</option>';
    (logins || []).forEach(function (l) {
      var v = String(l.userId);
      html += '<option value="' + escapeHtml(v) + '"' + (v === cur ? ' selected' : '') + '>' +
        escapeHtml(l.displayName + ' (' + l.username + ')') + '</option>';
    });
    return html + '</select>';
  }

  function observerRowHtml(row, byId, logins) {
    var owner = ownerLabel(row, byId);
    var cls = !row.bound ? ' um-status-pending' : (row.userId == null ? ' um-status-disabled' : '');
    return '<tr data-pubkey="' + escapeHtml(row.pubkey) + '">' +
      '<td>' + escapeHtml(row.name || shortKey(row.pubkey)) + '<br><small><code>' + escapeHtml(shortKey(row.pubkey)) + '</code></small></td>' +
      '<td class="um-col-optional">' + escapeHtml(row.lastSeen ? fmt(row.lastSeen) : '—') + '</td>' +
      '<td>' + (cls ? '<span class="um-status' + cls + '">' + escapeHtml(owner) + '</span>' : escapeHtml(owner)) + '</td>' +
      '<td>' + (KEY_RE.test(row.pubkey) ? ownerSelectHtml(row, logins) : '') + '</td></tr>';
  }

  function summaryText(rows) {
    var open = rows.filter(function (r) { return !r.bound; }).length;
    return open + ' of ' + rows.length + ' observers not migrated (no personal login yet).';
  }

  // Both tables are bounded (logins per account, observers per instance)
  // and rebuilt after each action.
  function render() {
    var lb = document.getElementById('obsAdminLogins');
    var ob = document.getElementById('obsAdminObservers');
    if (!lb || !ob) return;
    var byId = loginsById(state.logins);
    lb.innerHTML = state.logins.map(function (l) { return loginRowHtml(l, state.confirm); }).join('') ||
      '<tr><td colspan="6">No observer logins yet.</td></tr>';
    ob.innerHTML = state.rows.map(function (r) { return observerRowHtml(r, byId, state.logins); }).join('') ||
      '<tr><td colspan="4">No observers.</td></tr>';
    var sum = document.getElementById('obsAdminSummary');
    if (sum) sum.textContent = summaryText(state.rows);
  }

  function load() {
    var seq = ++state.seq;
    return Promise.all([CSAuth.request('GET', '/api/admin/observer-logins'), CSAuth.request('GET', '/api/observers')]).then(function (res) {
      if (seq !== state.seq) return;
      if (!res[0].ok) { say(CSAuth.errText(res[0]), false); return; }
      if (!res[1].ok) { say(CSAuth.errText(res[1]), false); return; }
      state.logins = res[0].data.logins || [];
      state.rows = joinObservers(res[1].data.observers, res[0].data.bindings);
      state.confirm = null;
      render();
    }).catch(function () { if (seq === state.seq) say('Network error, try again.', false); });
  }

  function post(act, userId) {
    var l = loginsById(state.logins)[userId];
    if (!l || state.busy) return Promise.resolve();
    state.busy = true;
    return CSAuth.request('POST', '/api/admin/observer-logins/' + encodeURIComponent(userId) + '/' + act).then(function (r) {
      say(r.ok ? l.username + ': ' + DONE[act] : CSAuth.errText(r), r.ok);
      return load();
    }).catch(function () { say('Network error, try again.', false); }).then(function () { state.busy = false; });
  }

  function onClick(ev) {
    var b = ev.target && ev.target.closest ? ev.target.closest('button[data-act]') : null;
    if (!b) return;
    var act = b.getAttribute('data-act');
    var userId = Number(b.getAttribute('data-id'));
    if (act === 'revoke' || act === 'reissue') { state.confirm = { act: act, userId: userId }; render(); return; }
    if (act === 'cancel') { state.confirm = null; render(); return; }
    if (act === 'revoke-confirm' || act === 'reissue-confirm') return post(act.split('-')[0], userId);
  }

  function onChange(ev) {
    var sel = ev.target;
    var pk = sel && sel.getAttribute && sel.getAttribute('data-pubkey');
    if (!pk || state.busy) return;
    var v = sel.value;
    state.busy = true;
    return CSAuth.request('PUT', '/api/admin/observer-bindings/' + encodeURIComponent(pk), { userId: v === '' ? null : Number(v) }).then(function (r) {
      say(r.ok ? shortKey(pk) + ': ' + (v === '' ? 'unbound' : 'reassigned') : CSAuth.errText(r), r.ok);
      return load();
    }).catch(function () { say('Network error, try again.', false); }).then(function () { state.busy = false; });
  }

  function mount(container) {
    state.logins = [];
    state.rows = [];
    state.confirm = null;
    state.busy = false;
    container.innerHTML = '<div class="admin-observer-logins">' +
      '<p class="account-hint">Personal MQTT logins for observers. Revoking a login keeps its observers bound to the user; reissuing mails a new username and password.</p>' +
      '<p class="account-msg" id="obsAdminMsg" role="status" aria-live="polite"></p>' +
      '<h3>Logins</h3><div class="um-table-wrap"><table class="um-table"><thead><tr>' +
      '<th scope="col">User</th><th scope="col">Username</th><th scope="col" class="um-col-optional">Created</th>' +
      '<th scope="col" class="um-col-optional">Last auth</th><th scope="col">Observers</th><th scope="col">Actions</th>' +
      '</tr></thead><tbody id="obsAdminLogins"></tbody></table></div>' +
      '<h3>Observers</h3><p class="account-hint" id="obsAdminSummary"></p>' +
      '<div class="um-table-wrap"><table class="um-table"><thead><tr>' +
      '<th scope="col">Observer</th><th scope="col" class="um-col-optional">Last seen</th><th scope="col">Owner</th><th scope="col">Reassign</th>' +
      '</tr></thead><tbody id="obsAdminObservers"></tbody></table></div></div>';
    document.getElementById('obsAdminLogins').addEventListener('click', onClick);
    document.getElementById('obsAdminObservers').addEventListener('change', onChange);
    return load();
  }

  function unmount() { state.seq++; state.confirm = null; state.busy = false; }

  window.CSAdminObserverLogins = { mount: mount, unmount: unmount,
    _test: { joinObservers: joinObservers, ownerLabel: ownerLabel, loginRowHtml: loginRowHtml, observerRowHtml: observerRowHtml,
      ownerSelectHtml: ownerSelectHtml, summaryText: summaryText, onClick: onClick, onChange: onChange, state: state } };
})();
