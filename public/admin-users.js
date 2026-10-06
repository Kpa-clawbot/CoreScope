/* #/admin/users — admin user management (optional user management).
 * Table with filters, per-row actions, and a detail panel with the mail
 * delivery timeline and audit log. Every dynamic value goes through escapeHtml.
 * Deep link: #/admin/users?status=&role=&q=&id= (read in init, written back
 * with replaceState so filter changes add no history entries). */
(function () {
  'use strict';
  var filters = { status: '', role: '', q: '' };
  var openId = null;
  var loadSeq = 0;

  function errText(r) { return (r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')'); }
  function fmt(iso) { if (!iso) return '—'; try { return new Date(iso).toLocaleString(); } catch (_) { return iso; } }
  function say(text, ok) {
    var el = document.getElementById('umMsg');
    if (!el) return;
    el.textContent = text;
    el.classList.toggle('ok', !!ok);
    el.classList.toggle('err', !ok);
  }
  function netErr() { say('Network error, try again.', false); }
  function chip(event, reason) {
    var e = String(event || 'sent');
    return '<span class="um-chip um-chip-' + escapeHtml(e) + '" title="' + escapeHtml(reason || '') + '">' + escapeHtml(e.replace(/_/g, ' ')) + '</span>';
  }

  function readHash(hash) {
    var p = new URLSearchParams(String(hash || '').split('?')[1] || '');
    return { status: p.get('status') || '', role: p.get('role') || '', q: p.get('q') || '', id: p.get('id') || '' };
  }
  function hashFor(f, id) {
    var p = new URLSearchParams();
    if (f.status) p.set('status', f.status);
    if (f.role) p.set('role', f.role);
    if (f.q) p.set('q', f.q);
    if (id) p.set('id', id);
    var s = p.toString();
    return '#/admin/users' + (s ? '?' + s : '');
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
    var html = '<h3>' + escapeHtml(d.user.displayName) + ' — ' + escapeHtml(d.user.email) + '</h3>' +
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
    var seq = ++loadSeq;
    return CSAuth.request('GET', '/api/admin/users?' + q.toString()).then(function (r) {
      var body = document.getElementById('umBody');
      if (!body || seq !== loadSeq) return;
      if (!r.ok) { say(errText(r), false); return; }
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

  function showDetail(id) {
    openId = id;
    syncHash();
    return CSAuth.request('GET', '/api/admin/users/' + encodeURIComponent(id)).then(function (r) {
      var el = document.getElementById('umDetail');
      if (!el || openId !== id) return;
      if (!r.ok) { say(errText(r), false); closeDetail(); return; }
      el.innerHTML = detailHtml(r.data);
      el.hidden = false;
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
    if (act === 'detail') { showDetail(id); return; }
    if (act === 'refresh') {
      CSAuth.request('POST', '/api/admin/users/' + encodeURIComponent(id) + '/mail/' + encodeURIComponent(btn.getAttribute('data-mail')) + '/refresh')
        .then(function (r) { say(r.ok ? 'Mail status refreshed.' : errText(r), r.ok); load(); }).catch(netErr);
      return;
    }
    if (confirmText[act] && !confirm(confirmText[act])) return;
    var ep = endpoints[act];
    CSAuth.request(ep[0], '/api/admin/users/' + encodeURIComponent(id) + ep[1], ep[2]).then(function (r) {
      say(r.ok ? 'Done.' : errText(r), r.ok);
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

  function init(app, routeParam) {
    app.innerHTML = '<div class="um-page"><p>Loading…</p></div>';
    var ready = window.CSAuth ? CSAuth.ready() : Promise.resolve();
    ready.then(function () {
      if (routeParam !== 'users' || !window.CSAuth || !CSAuth.isEnabled()) {
        app.innerHTML = '<div class="um-page"><h2>Not found</h2></div>';
        return;
      }
      if (!CSAuth.isAdmin()) {
        app.innerHTML = '<div class="um-page"><h2>Users</h2><p>Admins only. <a href="#/account/login">Log in</a></p></div>';
        return;
      }
      var h = readHash(location.hash);
      filters = { status: h.status, role: h.role, q: h.q };
      openId = h.id || null;
      app.innerHTML = '<div class="um-page"><h2>Users</h2>' +
        '<div class="um-filters">' +
        '<label>Search <input id="umQ" type="search" autocomplete="off"></label>' +
        '<label>Status <select id="umStatus"><option value="">any</option><option>pending</option><option>active</option><option>disabled</option></select></label>' +
        '<label>Role <select id="umRole"><option value="">any</option><option>user</option><option>admin</option></select></label>' +
        '</div><p class="account-msg" id="umMsg" role="status" aria-live="polite"></p>' +
        '<div class="um-table-wrap"><table class="um-table"><thead><tr>' +
        '<th scope="col">Name</th><th scope="col">Email</th><th scope="col">Role</th><th scope="col">Status</th><th scope="col">Last mail</th>' +
        '<th scope="col" class="um-col-optional">Created</th><th scope="col" class="um-col-optional">Last login</th><th scope="col">Actions</th>' +
        '</tr></thead><tbody id="umBody"></tbody></table></div>' +
        '<section class="um-detail" id="umDetail" hidden></section></div>';
      document.getElementById('umQ').value = filters.q;
      document.getElementById('umStatus').value = filters.status;
      document.getElementById('umRole').value = filters.role;
      var deb = debounce(function () { filters.q = document.getElementById('umQ').value.trim(); syncHash(); load(); }, 250);
      document.getElementById('umQ').addEventListener('input', deb);
      document.getElementById('umStatus').addEventListener('change', function (e) { filters.status = e.target.value; syncHash(); load(); });
      document.getElementById('umRole').addEventListener('change', function (e) { filters.role = e.target.value; syncHash(); load(); });
      app.querySelector('.um-page').addEventListener('click', onAction);
      load();
    }).catch(function () {
      app.innerHTML = '<div class="um-page"><h2>Users</h2><p>Network error, reload the page.</p></div>';
    });
  }

  registerPage('admin', { init: init, destroy: function () { openId = null; loadSeq++; } });
  window.CSAdminUsers = { _test: { actionsFor: actionsFor, rowHtml: rowHtml, detailHtml: detailHtml, readHash: readHash, hashFor: hashFor } };
})();
