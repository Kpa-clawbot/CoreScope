(function () {
  'use strict';

  var whoEl = document.getElementById('who');
  var tbody = document.getElementById('admins-table-body');
  var showAddBtn = document.getElementById('show-add-admin');
  var addForm = document.getElementById('add-admin-form');
  var addError = document.getElementById('add-admin-error');

  function escapeHTML(s) {
    var div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
  }

  function fetchJSON(url, opts) {
    return fetch(url, Object.assign({ credentials: 'same-origin' }, opts)).then(function (res) {
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

  function renderWho(me) {
    whoEl.innerHTML = 'Logged in as <strong>' + escapeHTML(me.username) + '</strong> (' + escapeHTML(me.role) + ')';
    var logout = document.createElement('button');
    logout.className = 'link';
    logout.textContent = 'Log out';
    logout.addEventListener('click', function () {
      fetch('/api/admin/logout', { method: 'POST', credentials: 'same-origin' }).then(function () {
        window.location.href = '/admin/login';
      });
    });
    whoEl.appendChild(logout);
  }

  function renderAdmins(admins) {
    tbody.innerHTML = '';
    admins.forEach(function (a) {
      var tr = document.createElement('tr');
      var created = new Date(a.createdAt);
      tr.innerHTML =
        '<td class="' + (a.disabled ? 'disabled' : '') + '">' + escapeHTML(a.username) + '</td>' +
        '<td class="role-' + escapeHTML(a.role) + '">' + escapeHTML(a.role) + '</td>' +
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
      renderWho(me);
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
