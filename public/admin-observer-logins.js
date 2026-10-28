/* Observer logins tab of #/admin (docs/superpowers/specs/2026-10-25-observer-mqtt-logins-design.md):
 * personal MQTT logins with Revoke / Reissue, and every observer with its
 * owner ("not migrated" without a binding, "orphaned" when the owner's
 * account is gone) and a Reassign button that swaps one shared owner select
 * into that row only. A Confirmed column shows when the owner's own login
 * first published as the observer. One GET /api/admin/observer-logins and one
 * GET /api/observers per load, joined client-side. The observers table is
 * built on load only; a reassign updates its own row and the summary, and
 * the Revoke / Reissue inline confirm (no browser dialog) touches only the
 * logins table. Deep link: #/admin?tab=observer-logins. Mounted by admin.js
 * after its admin check. Every dynamic value goes through escapeHtml;
 * messages use textContent. */
(function () {
  'use strict';
  var KEY_RE = /^[0-9A-Fa-f]{64}$/;
  var CONFIRM = {
    revoke: ['Revoke this login? Its observers can no longer connect with it and stay bound to this user.', 'Revoke'],
    reissue: ['Mail a new username and password? The current ones stop working.', 'Reissue']
  };
  var DONE = { revoke: 'revoked', reissue: 'reissued, the new credentials were mailed' };
  // byKey: rows by pubkey; options: the owner select's options, built once per load;
  // editing: the pubkey whose row shows the select.
  var state = { seq: 0, logins: [], byId: {}, rows: [], byKey: {}, options: '', editing: null, confirm: null, busy: false };

  function fmt(iso) { if (!iso) return '—'; try { return new Date(iso).toLocaleString(); } catch (_) { return iso; } }
  function shortKey(pk) { return String(pk || '').slice(0, 12) + '…'; }
  function say(text, ok) { CSAuth.say('obsAdminMsg', text, ok); }
  function label(row) { return row.name || shortKey(row.pubkey); }

  // joinObservers merges the analyzer's observers with the bindings, keyed by
  // uppercase pubkey; a binding without an analyzer row still gets a row.
  // Each row: { pubkey, name, lastSeen, bound, userId, confirmedAt }.
  function joinObservers(observers, bindings) {
    var byKey = {};
    var rows = [];
    (observers || []).forEach(function (o) {
      var k = String(o.id || '').toUpperCase();
      if (!k || byKey[k]) return;
      byKey[k] = { pubkey: k, name: o.name || '', lastSeen: o.last_seen || null, bound: false, userId: null, confirmedAt: null };
      rows.push(byKey[k]);
    });
    (bindings || []).forEach(function (b) {
      var k = String(b.pubkey || '').toUpperCase();
      var row = byKey[k];
      if (!row) {
        row = byKey[k] = { pubkey: k, name: '', lastSeen: null, bound: false, userId: null, confirmedAt: null };
        rows.push(row);
      }
      row.bound = true;
      row.userId = b.userId == null ? null : b.userId;
      row.confirmedAt = b.confirmedAt || null;
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

  // confirmedLabel: when the owner's own login first published under the
  // key; until then the shared login may still publish as it.
  function confirmedLabel(row) {
    if (!row.bound) return '—';
    return row.confirmedAt ? fmt(row.confirmedAt) : 'not yet';
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

  // ownerOptionsHtml: "not bound" plus every user with a login. Built once
  // per load and shared by whichever row is being reassigned.
  function ownerOptionsHtml(logins) {
    return '<option value="">not bound</option>' + (logins || []).map(function (l) {
      return '<option value="' + escapeHtml(String(l.userId)) + '">' + escapeHtml(l.displayName + ' (' + l.username + ')') + '</option>';
    }).join('');
  }

  // ownerValue: the select value for a row; an orphaned binding has its own
  // disabled entry until it is reassigned.
  function ownerValue(row) {
    if (!row.bound) return '';
    return row.userId == null ? 'orphaned' : String(row.userId);
  }

  function ownerSelectHtml(row, options) {
    return '<select data-pubkey="' + escapeHtml(row.pubkey) + '" aria-label="Owner of ' + escapeHtml(label(row)) + '">' +
      (row.bound && row.userId == null ? '<option value="orphaned" disabled>orphaned</option>' : '') + options + '</select> ' +
      '<button type="button" class="account-btn account-btn-secondary" data-cancel="' + escapeHtml(row.pubkey) + '">Cancel</button>';
  }

  // observerCellsHtml: a row's cells. The last one holds the Reassign button,
  // or the shared select when editing; a key the PUT would refuse gets neither.
  function observerCellsHtml(row, byId, editing, options) {
    var owner = ownerLabel(row, byId);
    var cls = !row.bound ? ' um-status-pending' : (row.userId == null ? ' um-status-disabled' : '');
    var action = '';
    if (KEY_RE.test(row.pubkey)) {
      action = editing ? ownerSelectHtml(row, options)
        : '<button type="button" class="account-btn account-btn-secondary" data-reassign="' + escapeHtml(row.pubkey) +
          '" aria-label="Reassign ' + escapeHtml(label(row)) + '">Reassign</button>';
    }
    return '<td>' + escapeHtml(label(row)) + '<br><small><code>' + escapeHtml(shortKey(row.pubkey)) + '</code></small></td>' +
      '<td class="um-col-optional">' + escapeHtml(row.lastSeen ? fmt(row.lastSeen) : '—') + '</td>' +
      '<td>' + (cls ? '<span class="um-status' + cls + '">' + escapeHtml(owner) + '</span>' : escapeHtml(owner)) + '</td>' +
      '<td class="um-col-optional obs-confirmed">' + escapeHtml(confirmedLabel(row)) + '</td>' +
      '<td>' + action + '</td>';
  }

  function observerRowHtml(row, byId) {
    return '<tr data-pubkey="' + escapeHtml(row.pubkey) + '">' + observerCellsHtml(row, byId, false, '') + '</tr>';
  }

  function summaryText(rows) {
    var open = rows.filter(function (r) { return !r.bound; }).length;
    return open + ' of ' + rows.length + ' observers not migrated (no personal login yet).';
  }

  function renderLogins() {
    var lb = document.getElementById('obsAdminLogins');
    if (!lb) return;
    lb.innerHTML = state.logins.map(function (l) { return loginRowHtml(l, state.confirm); }).join('') ||
      '<tr><td colspan="6">No observer logins yet.</td></tr>';
  }

  function renderSummary() {
    var sum = document.getElementById('obsAdminSummary');
    if (sum) sum.textContent = summaryText(state.rows);
  }

  // renderObservers builds the whole table: on load only.
  function renderObservers() {
    var ob = document.getElementById('obsAdminObservers');
    if (!ob) return;
    ob.innerHTML = state.rows.map(function (r) { return observerRowHtml(r, state.byId); }).join('') ||
      '<tr><td colspan="5">No observers.</td></tr>';
    renderSummary();
  }

  // rowEl finds a row; pubkeys reaching here passed KEY_RE (hex only), so
  // they are safe inside the selector.
  function rowEl(pk) {
    var ob = document.getElementById('obsAdminObservers');
    return ob ? ob.querySelector('tr[data-pubkey="' + pk + '"]') : null;
  }

  // renderRow redraws one observer row, with the select when it is being edited.
  function renderRow(pk) {
    var row = state.byKey[pk];
    var tr = rowEl(pk);
    if (!row || !tr) return null;
    var editing = state.editing === pk;
    tr.innerHTML = observerCellsHtml(row, state.byId, editing, state.options);
    if (!editing) return null;
    var sel = tr.querySelector('select');
    if (sel) sel.value = ownerValue(row);
    return sel;
  }

  function startEdit(pk) {
    if (state.busy || !state.byKey[pk] || !KEY_RE.test(pk)) return;
    var prev = state.editing;
    state.editing = pk;
    if (prev && prev !== pk) renderRow(prev);
    var sel = renderRow(pk);
    if (sel && sel.focus) sel.focus();
  }

  function stopEdit(focusButton) {
    var pk = state.editing;
    if (!pk || state.busy) return;
    state.editing = null;
    renderRow(pk);
    if (!focusButton) return;
    var tr = rowEl(pk);
    var btn = tr && tr.querySelector('button[data-reassign]');
    if (btn && btn.focus) btn.focus();
  }

  function load() {
    var seq = ++state.seq;
    return Promise.all([CSAuth.request('GET', '/api/admin/observer-logins'), CSAuth.request('GET', '/api/observers')]).then(function (res) {
      if (seq !== state.seq) return;
      if (!res[0].ok) { say(CSAuth.errText(res[0]), false); return; }
      if (!res[1].ok) { say(CSAuth.errText(res[1]), false); return; }
      state.logins = res[0].data.logins || [];
      state.byId = loginsById(state.logins);
      state.rows = joinObservers(res[1].data.observers, res[0].data.bindings);
      state.byKey = {};
      state.rows.forEach(function (r) { state.byKey[r.pubkey] = r; });
      state.options = ownerOptionsHtml(state.logins);
      state.confirm = null;
      state.editing = null;
      renderLogins();
      renderObservers();
    }).catch(function () { if (seq === state.seq) say('Network error, try again.', false); });
  }

  // Revoke and reissue change the logins (and reissue the usernames in the
  // owner options), so they reload both tables.
  function post(act, userId) {
    var l = state.byId[userId];
    if (!l || state.busy) return Promise.resolve();
    state.busy = true;
    return CSAuth.request('POST', '/api/admin/observer-logins/' + encodeURIComponent(userId) + '/' + act).then(function (r) {
      say(r.ok ? l.username + ': ' + DONE[act] : CSAuth.errText(r), r.ok);
      return load();
    }).catch(function () { say('Network error, try again.', false); }).then(function () { state.busy = false; });
  }

  function onLoginClick(ev) {
    var b = ev.target && ev.target.closest ? ev.target.closest('button[data-act]') : null;
    if (!b) return;
    var act = b.getAttribute('data-act');
    var userId = Number(b.getAttribute('data-id'));
    if (act === 'revoke' || act === 'reissue') { state.confirm = { act: act, userId: userId }; renderLogins(); return; }
    if (act === 'cancel') { state.confirm = null; renderLogins(); return; }
    if (act === 'revoke-confirm' || act === 'reissue-confirm') return post(act.split('-')[0], userId);
  }

  function onObserverClick(ev) {
    var t = ev.target;
    var b = t && t.closest ? t.closest('button[data-reassign], button[data-cancel]') : null;
    if (!b) return;
    var pk = b.getAttribute('data-reassign');
    if (pk) startEdit(pk);
    else stopEdit(true);
  }

  function onObserverKey(ev) {
    if (ev.key === 'Escape' && state.editing) stopEdit(true);
  }

  // onChange saves the chosen owner. The select stays disabled until the
  // answer, so an unsaved choice never shows as current; a refusal puts the
  // saved value back.
  function onChange(ev) {
    var sel = ev.target;
    var pk = sel && sel.getAttribute && sel.getAttribute('data-pubkey');
    var row = pk && state.byKey[pk];
    if (!row || state.busy) return;
    var v = sel.value;
    var userId = v === '' ? null : Number(v);
    state.busy = true;
    sel.disabled = true;
    return CSAuth.request('PUT', '/api/admin/observer-bindings/' + encodeURIComponent(pk), { userId: userId }).then(function (r) {
      if (!r.ok) {
        say(CSAuth.errText(r), false);
        sel.value = ownerValue(row);
        return;
      }
      // The server keeps confirmed_at only for the same owner.
      if (userId == null || userId !== row.userId) row.confirmedAt = null;
      row.bound = userId != null;
      row.userId = userId;
      say(shortKey(pk) + ': ' + (userId == null ? 'unbound' : 'reassigned'), true);
      if (state.editing === pk) state.editing = null;
      renderRow(pk);
      renderSummary();
    }).catch(function () {
      say('Network error, try again.', false);
      sel.value = ownerValue(row);
    }).then(function () { state.busy = false; sel.disabled = false; });
  }

  function mount(container) {
    state.logins = [];
    state.byId = {};
    state.rows = [];
    state.byKey = {};
    state.options = '';
    state.editing = null;
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
      '<th scope="col">Observer</th><th scope="col" class="um-col-optional">Last seen</th><th scope="col">Owner</th><th scope="col" class="um-col-optional">Confirmed</th><th scope="col">Reassign</th>' +
      '</tr></thead><tbody id="obsAdminObservers"></tbody></table></div></div>';
    document.getElementById('obsAdminLogins').addEventListener('click', onLoginClick);
    var ob = document.getElementById('obsAdminObservers');
    ob.addEventListener('click', onObserverClick);
    ob.addEventListener('change', onChange);
    ob.addEventListener('keydown', onObserverKey);
    return load();
  }

  function unmount() { state.seq++; state.confirm = null; state.editing = null; state.busy = false; }

  window.CSAdminObserverLogins = { mount: mount, unmount: unmount,
    _test: { joinObservers: joinObservers, ownerLabel: ownerLabel, loginRowHtml: loginRowHtml, observerRowHtml: observerRowHtml,
      observerCellsHtml: observerCellsHtml, ownerOptionsHtml: ownerOptionsHtml, ownerValue: ownerValue, summaryText: summaryText,
      onLoginClick: onLoginClick, onObserverClick: onObserverClick, onObserverKey: onObserverKey, onChange: onChange, state: state } };
})();
