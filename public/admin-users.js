/* Users tab of #/admin (optional user management): table with filters,
 * per-row actions, and a detail panel with the mail delivery timeline and
 * audit log. Mounted by admin.js after its admin check. Every dynamic value
 * goes through escapeHtml.
 * Deep link: #/admin?tab=users&status=&role=&q=&bouncing=1&id= (read on
 * mount, written back with replaceState so filter changes add no history
 * entries). Every filter, bouncing included, is applied by the server. */
(function () {
  'use strict';
  var filters = { status: '', role: '', q: '', bouncing: false };
  var openId = null;
  var loadSeq = 0;
  var mountSeq = 0;
  var STATUSES = ['pending', 'active', 'disabled'];
  var ROLES = ['user', 'admin'];

  function fmt(iso) { if (!iso) return '—'; try { return new Date(iso).toLocaleString(); } catch (_) { return iso; } }
  function say(text, ok) { CSAuth.say('umMsg', text, ok); }
  function netErr() { say('Network error, try again.', false); }
  function chip(event, reason) {
    var e = String(event || 'sent');
    return '<span class="um-chip um-chip-' + escapeHtml(e) + '" title="' + escapeHtml(reason || '') + '">' + escapeHtml(e.replace(/_/g, ' ')) + '</span>';
  }

  // Only known filter values and a numeric id are taken from the address bar.
  function readHash(hash) {
    var p = new URLSearchParams(String(hash || '').split('?')[1] || '');
    var pick = function (v, allowed) { return allowed.indexOf(v) !== -1 ? v : ''; };
    var id = p.get('id') || '';
    return { status: pick(p.get('status'), STATUSES), role: pick(p.get('role'), ROLES), q: p.get('q') || '',
      bouncing: p.get('bouncing') === '1', id: /^\d+$/.test(id) ? id : '' };
  }
  function hashFor(f, id) {
    var p = new URLSearchParams();
    p.set('tab', 'users');
    if (f.status) p.set('status', f.status);
    if (f.role) p.set('role', f.role);
    if (f.q) p.set('q', f.q);
    if (f.bouncing) p.set('bouncing', '1');
    if (id) p.set('id', id);
    return '#/admin?' + p.toString();
  }
  function syncHash() {
    var h = hashFor(filters, openId);
    if (h !== location.hash) history.replaceState(null, '', h);
  }

  // Mirrors the server guards (admin_users_handlers.go): disable only active,
  // enable only disabled, role change not for pending, no self/config-admin removal.
  function actionsFor(u, me) {
    me = me || {};
    var b = function (act, label) {
      return '<button type="button" class="account-btn account-btn-secondary" data-act="' + act + '" data-id="' + escapeHtml(String(u.id)) + '">' + label + '</button>';
    };
    var self = u.id === me.id;
    var out = [b('detail', 'Details')];
    if (u.status === 'pending') out.push(b('activate', 'Activate'), b('resend', 'Resend mail'));
    if (u.status === 'active' && !self && !u.configAdmin) out.push(b('disable', 'Disable'));
    if (u.status === 'disabled') out.push(b('enable', 'Enable'));
    if (u.status !== 'pending' && !u.configAdmin) out.push(u.role === 'admin' ? b('demote', 'Make user') : b('promote', 'Make admin'));
    if (!self && !u.configAdmin) out.push(b('delete', 'Delete'));
    return '<div class="um-actions">' + out.join('') + '</div>';
  }

  function rowHtml(u, me) {
    var mail = u.lastMail ? chip(u.lastMail.lastEvent, u.lastMail.lastReason) : '—';
    if (u.emailBouncing) mail += ' <span class="um-chip um-chip-bouncing">bouncing</span>';
    return '<tr data-email="' + escapeHtml(u.email) + '">' +
      '<td>' + escapeHtml(u.displayName) + (u.configAdmin ? ' <span class="um-chip" title="listed in adminEmails">config</span>' : '') + '</td>' +
      '<td>' + escapeHtml(u.email) + '</td>' +
      '<td>' + escapeHtml(u.role) + '</td>' +
      '<td><span class="um-status um-status-' + escapeHtml(u.status) + '">' + escapeHtml(u.status) + '</span>' +
        (u.activatedManually ? ' <span class="um-chip" title="activated by an admin; address not verified">manual</span>' : '') + '</td>' +
      '<td>' + mail + '</td>' +
      '<td class="um-col-optional">' + escapeHtml(fmt(u.createdAt)) + '</td>' +
      '<td class="um-col-optional">' + escapeHtml(fmt(u.lastLoginAt)) + '</td>' +
      '<td>' + actionsFor(u, me) + '</td></tr>';
  }

  function detailHtml(d) {
    var html = '<button type="button" class="account-btn account-btn-secondary um-detail-close" data-act="close">Close</button>' +
      '<h3 tabindex="-1">' + escapeHtml(d.user.displayName) + ' — ' + escapeHtml(d.user.email) + '</h3>' +
      '<p class="account-hint">' + escapeHtml(String((d.sessions || []).length)) + ' active session(s). "Opened" depends on tracking pixels: some mail apps load them automatically, others block them.</p>' +
      '<h4>Mail</h4><ol>';
    (d.mail || []).forEach(function (m) {
      html += '<li>' + escapeHtml(m.purpose) + ' to ' + escapeHtml(m.to) + ', ' + escapeHtml(fmt(m.sentAt)) + ' ' + chip(m.lastEvent, m.lastReason) +
        ' <button type="button" class="account-btn account-btn-secondary" data-act="refresh" data-id="' + escapeHtml(String(d.user.id)) + '" data-mail="' + escapeHtml(String(m.id)) + '">Refresh status</button>';
      if (m.events && m.events.length) {
        html += '<ul>';
        m.events.forEach(function (e) {
          html += '<li>' + escapeHtml(fmt(e.at)) + ': ' + escapeHtml(e.event) + (e.reason ? ' (' + escapeHtml(e.reason) + ')' : '') + '</li>';
        });
        html += '</ul>';
      }
      html += '</li>';
    });
    html += '</ol><h4>Audit</h4><ol>';
    (d.audit || []).forEach(function (a) {
      html += '<li>' + escapeHtml(fmt(a.at)) + ': ' + escapeHtml(a.action) +
        (a.actorUserId != null ? ' by #' + escapeHtml(String(a.actorUserId)) : '') + '</li>';
    });
    return html + '</ol>';
  }

  function load() {
    var q = new URLSearchParams();
    if (filters.status) q.set('status', filters.status);
    if (filters.role) q.set('role', filters.role);
    if (filters.q) q.set('q', filters.q);
    if (filters.bouncing) q.set('bouncing', '1');
    var seq = ++loadSeq;
    return CSAuth.request('GET', '/api/admin/users?' + q.toString()).then(function (r) {
      var body = document.getElementById('umBody');
      if (!body || seq !== loadSeq) return;
      if (!r.ok) { say(CSAuth.errText(r), false); return; }
      var me = CSAuth.user();
      var html = '';
      // The server caps the list at 1000 rows, so a full tbody rebuild is bounded.
      r.data.forEach(function (u) { html += rowHtml(u, me); });
      body.innerHTML = html || '<tr><td colspan="8">No users match.</td></tr>';
      if (openId) showDetail(openId);
    }).catch(netErr);
  }

  function closeDetail() {
    openId = null;
    var el = document.getElementById('umDetail');
    if (el) el.hidden = true;
    syncHash();
  }

  // focus: opened by the admin (not a deep link or a list reload), so move
  // keyboard focus to the panel heading.
  function showDetail(id, focus) {
    openId = id;
    syncHash();
    return CSAuth.request('GET', '/api/admin/users/' + encodeURIComponent(id)).then(function (r) {
      var el = document.getElementById('umDetail');
      if (!el || openId !== id) return;
      if (!r.ok) { say(CSAuth.errText(r), false); closeDetail(); return; }
      el.innerHTML = detailHtml(r.data);
      el.hidden = false;
      if (focus) {
        var h = el.querySelector('h3');
        if (h) h.focus();
      }
    }).catch(netErr);
  }

  var confirmText = {
    activate: 'Activate this account without mail confirmation? The address will stay unverified.',
    disable: 'Disable this account? The user is logged out everywhere.',
    delete: 'Delete this account permanently?',
    demote: 'Remove admin rights from this user?'
  };
  var endpoints = {
    activate: ['POST', '/activate'], resend: ['POST', '/resend-activation'], disable: ['POST', '/disable'],
    enable: ['POST', '/enable'], delete: ['DELETE', ''], promote: ['POST', '/role', { role: 'admin' }],
    demote: ['POST', '/role', { role: 'user' }]
  };

  function onAction(e) {
    var btn = e.target.closest('button[data-act]');
    if (!btn) return;
    var act = btn.getAttribute('data-act');
    var id = btn.getAttribute('data-id');
    if (act === 'detail') { showDetail(id, true); return; }
    if (act === 'close') {
      var was = openId;
      closeDetail();
      // ids are numeric (server ids, readHash), safe in the selector.
      var opener = was && document.querySelector('button[data-act="detail"][data-id="' + was + '"]');
      if (opener) opener.focus();
      return;
    }
    if (act === 'refresh') {
      CSAuth.request('POST', '/api/admin/users/' + encodeURIComponent(id) + '/mail/' + encodeURIComponent(btn.getAttribute('data-mail')) + '/refresh')
        .then(function (r) { say(r.ok ? 'Mail status refreshed.' : CSAuth.errText(r), r.ok); load(); }).catch(netErr);
      return;
    }
    if (confirmText[act] && !confirm(confirmText[act])) return;
    var ep = endpoints[act];
    CSAuth.request(ep[0], '/api/admin/users/' + encodeURIComponent(id) + ep[1], ep[2]).then(function (r) {
      say(r.ok ? 'Done.' : CSAuth.errText(r), r.ok);
      if (act === 'delete' && r.ok && openId === id) closeDetail();
      var me = CSAuth.user();
      if (r.ok && (act === 'promote' || act === 'demote') && me && String(me.id) === id) {
        // Own role changed: refresh so admin-only UI disappears.
        return CSAuth.refreshMe().then(function () {
          if (!CSAuth.isAdmin()) location.hash = '#/account';
          else load();
        });
      }
      return load();
    }).catch(netErr);
  }

  function mount(container) {
    var h = readHash(location.hash);
    filters = { status: h.status, role: h.role, q: h.q, bouncing: h.bouncing };
    openId = h.id || null;
    container.innerHTML = '<div class="um-users">' +
      '<div class="um-filters">' +
      '<label>Search <input id="umQ" type="search" autocomplete="off"></label>' +
      '<label>Status <select id="umStatus"><option value="">any</option><option>pending</option><option>active</option><option>disabled</option></select></label>' +
      '<label>Role <select id="umRole"><option value="">any</option><option>user</option><option>admin</option></select></label>' +
      '<label><input id="umBouncing" type="checkbox"> Bouncing mail only</label>' +
      '</div><p class="account-msg" id="umMsg" role="status" aria-live="polite"></p>' +
      '<div class="um-table-wrap"><table class="um-table"><thead><tr>' +
      '<th scope="col">Name</th><th scope="col">Email</th><th scope="col">Role</th><th scope="col">Status</th><th scope="col">Last mail</th>' +
      '<th scope="col" class="um-col-optional">Created</th><th scope="col" class="um-col-optional">Last login</th><th scope="col">Actions</th>' +
      '</tr></thead><tbody id="umBody"></tbody></table></div>' +
      '<section class="um-detail" id="umDetail" hidden></section></div>';
    document.getElementById('umQ').value = filters.q;
    document.getElementById('umStatus').value = filters.status;
    document.getElementById('umRole').value = filters.role;
    document.getElementById('umBouncing').checked = filters.bouncing;
    var token = ++mountSeq;
    var deb = debounce(function () {
      var q = document.getElementById('umQ');
      if (token !== mountSeq || !q) return;
      filters.q = q.value.trim(); syncHash(); load();
    }, 250);
    document.getElementById('umQ').addEventListener('input', deb);
    document.getElementById('umStatus').addEventListener('change', function (e) { filters.status = e.target.value; syncHash(); load(); });
    document.getElementById('umRole').addEventListener('change', function (e) { filters.role = e.target.value; syncHash(); load(); });
    document.getElementById('umBouncing').addEventListener('change', function (e) { filters.bouncing = !!e.target.checked; syncHash(); load(); });
    container.querySelector('.um-users').addEventListener('click', onAction);
    return load();
  }

  function unmount() { openId = null; loadSeq++; mountSeq++; }

  window.CSAdminUsers = { mount: mount, unmount: unmount,
    _test: { actionsFor: actionsFor, rowHtml: rowHtml, detailHtml: detailHtml, readHash: readHash, hashFor: hashFor } };
})();
