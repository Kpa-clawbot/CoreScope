(function () {
  'use strict';

  var tbody = document.getElementById('admins-table-body');
  var showAddBtn = document.getElementById('show-add-admin');
  var addForm = document.getElementById('add-admin-form');
  var addError = document.getElementById('add-admin-error');

  function escapeHTML(s) {
    var div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
  }

  function getCookie(name) {
    var match = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return match ? decodeURIComponent(match[1]) : '';
  }

  // Double-submit CSRF: the server sets a non-HttpOnly cookie at login;
  // we echo its value back as a header on every request. A cross-site
  // page can't read the cookie (same-origin policy) so can't forge this.
  function csrfHeaders() {
    return { 'X-CSRF-Token': getCookie('corescope_admin_csrf') };
  }

  function fetchJSON(url, opts) {
    opts = opts || {};
    var headers = Object.assign({}, opts.headers, csrfHeaders());
    return fetch(url, Object.assign({ credentials: 'same-origin' }, opts, { headers: headers })).then(function (res) {
      if (res.status === 401) {
        window.location.href = '/admin/login';
        return Promise.reject(new Error('not logged in'));
      }
      return res.json().then(function (body) {
        if (!res.ok) throw new Error(body.error || ('request failed (' + res.status + ')'));
        return body;
      });
    });
  }

  function renderAdmins(admins) {
    tbody.innerHTML = '';
    admins.forEach(function (a) {
      var tr = document.createElement('tr');
      var created = new Date(a.createdAt);
      tr.innerHTML =
        '<td class="' + (a.disabled ? 'disabled-name' : '') + '">' + escapeHTML(a.username) + '</td>' +
        '<td><span class="role-badge ' + escapeHTML(a.role) + '">' + escapeHTML(a.role) + '</span></td>' +
        '<td>' + (isNaN(created.getTime()) ? escapeHTML(a.createdAt) : created.toLocaleDateString()) + '</td>';
      tbody.appendChild(tr);
    });
  }

  function loadAdmins() {
    return fetchJSON('/api/admin/admins').then(function (body) {
      renderAdmins(body.admins || []);
    });
  }

  function setupAddAdmin(isSuperAdmin) {
    if (!isSuperAdmin) return;
    showAddBtn.style.display = 'inline-block';
    showAddBtn.addEventListener('click', function () {
      addForm.classList.toggle('visible');
    });

    addForm.addEventListener('submit', function (e) {
      e.preventDefault();
      addError.style.display = 'none';

      var username = document.getElementById('new-username').value;
      var password = document.getElementById('new-password').value;
      var role = document.getElementById('new-role').value;

      fetchJSON('/api/admin/admins', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: username, password: password, role: role })
      })
        .then(function () {
          addForm.reset();
          addForm.classList.remove('visible');
          return loadAdmins();
        })
        .catch(function (err) {
          addError.textContent = err.message || 'Failed to create admin';
          addError.style.display = 'block';
        });
    });
  }

  fetchJSON('/api/admin/me')
    .then(function (me) {
      window.renderAccountMenu(me);
      setupAddAdmin(me.role === 'super_admin');
      return loadAdmins();
    })
    .then(function () {
      // Reveal only once the page has something real to show — body starts
      // hidden (see index.html) so a logged-out visitor never sees a flash
      // of the dashboard before fetchJSON's 401 handler redirects them.
      document.body.classList.add('authed');
    })
    .catch(function (err) {
      // fetchJSON already redirects on 401 without ever revealing the
      // page; anything else, surface it and reveal so the user isn't
      // left staring at a blank screen.
      if (err.message !== 'not logged in') {
        console.error('[admin]', err);
        document.body.classList.add('authed');
      }
    });
})();
