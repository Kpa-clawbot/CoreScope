/* Optional user management client (docs/specs/2026-10-06-user-management-design.md).
 * Inert unless /api/config/client advertises userManagement.enabled (roles.js
 * sets window.MC_USER_MGMT). Exposes window.CSAuth and window.CS_USER, and
 * fires 'cs-auth-changed' on window whenever the user changes. */
(function () {
  'use strict';
  var state = { enabled: false, user: null, ready: null };

  function request(method, path, body) {
    var opts = { method: method, credentials: 'same-origin', headers: { 'Accept': 'application/json' } };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    if (method !== 'GET' && state.user && state.user.csrfToken) {
      opts.headers['X-CS-CSRF'] = state.user.csrfToken;
    }
    return fetch(path, opts).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (res.status === 401 && state.user && path !== '/api/auth/login' && path !== '/api/auth/activate') {
          setUser(null);
          notify('You were logged out.');
        }
        return { ok: res.ok, status: res.status, data: data || {} };
      });
    });
  }

  function notify(msg) {
    var el = document.getElementById('csAuthToast');
    if (!el) {
      el = document.createElement('div');
      el.id = 'csAuthToast';
      el.className = 'cs-auth-toast';
      el.setAttribute('role', 'status');
      el.setAttribute('aria-live', 'polite');
      document.body.appendChild(el);
    }
    el.textContent = msg;
    el.classList.add('visible');
    clearTimeout(notify._t);
    notify._t = setTimeout(function () { el.classList.remove('visible'); }, 4000);
  }

  function setUser(u) {
    state.user = u || null;
    window.CS_USER = state.user;
    renderControl();
    window.dispatchEvent(new CustomEvent('cs-auth-changed', { detail: state.user }));
  }

  // say shows text in the message box id (account pages, settings sync).
  function say(id, text, ok) {
    var el = document.getElementById(id);
    if (!el) return;
    el.textContent = text;
    el.classList.toggle('ok', !!ok);
    el.classList.toggle('err', !ok);
  }
  function errText(r) { return (r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')'); }

  // An optional handler (settings-sync.js) runs first: it may cancel the
  // logout or hand back afterLogout, which runs once the server ended the
  // session and before the user is cleared.
  var logoutHandler = null;
  function setLogoutHandler(fn) { logoutHandler = fn; }

  // logout ends the session on the server. Only when that succeeded does it
  // move to next and then clear the user, so pages listening for
  // 'cs-auth-changed' already see the new view. A refusal keeps the user
  // and is returned for the caller to show; a cancel returns cancelled. One
  // logout runs at a time: another call while the dialog is open or the
  // POST is out returns cancelled.
  var loggingOut = null;
  function logout(next) {
    if (loggingOut) return Promise.resolve({ ok: false, cancelled: true, status: 0, data: {} });
    var done = function () { loggingOut = null; };
    loggingOut = runLogout(next);
    loggingOut.then(done, done);
    return loggingOut;
  }

  function runLogout(next) {
    return Promise.resolve(logoutHandler ? logoutHandler() : null).then(function (h) {
      h = h || {};
      if (h.cancel) return { ok: false, cancelled: true, status: 0, data: {} };
      return request('POST', '/api/auth/logout').then(function (r) {
        if (r.ok) {
          if (h.afterLogout) h.afterLogout();
          location.hash = next;
          setUser(null);
        }
        return r;
      });
    });
  }

  function refreshMe() {
    return request('GET', '/api/auth/me').then(function (r) {
      setUser(r.ok ? r.data : null);
      return state.user;
    });
  }

  function icon(id) {
    return '<svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#' + id + '"></use></svg>';
  }

  function closeMenu() {
    var m = document.getElementById('accountMenu');
    var b = document.getElementById('accountToggle');
    if (m && !m.hidden) { m.hidden = true; if (b) b.setAttribute('aria-expanded', 'false'); }
  }

  // The menu is position:fixed (.top-nav clips overflow); place it under the toggle, like the More menu (#1406).
  function positionMenu() {
    var m = document.getElementById('accountMenu');
    var b = document.getElementById('accountToggle');
    if (!m || !b) return;
    var r = b.getBoundingClientRect();
    m.style.top = (r.bottom + 4) + 'px';
    m.style.right = (window.innerWidth - r.right) + 'px';
    m.style.left = 'auto';
  }

  function renderControl() {
    if (!state.enabled) return;
    var right = document.querySelector('.top-nav .nav-right');
    if (!right) return;
    var wrap = document.getElementById('accountWrap');
    if (!wrap) {
      wrap = document.createElement('div');
      wrap.id = 'accountWrap';
      wrap.className = 'nav-account-wrap';
      right.insertBefore(wrap, document.getElementById('hamburger'));
    }
    var u = state.user;
    if (!u) {
      wrap.innerHTML = '<a class="nav-account-btn" id="accountToggle" href="#/account/login">' +
        icon('ph-user-circle') + '<span class="nav-account-label">Log in</span></a>';
      return;
    }
    var html = '<button class="nav-account-btn" id="accountToggle" aria-haspopup="true" aria-expanded="false" aria-controls="accountMenu" title="Account">' +
      icon('ph-user-circle') + '<span class="nav-account-label">' + escapeHtml(u.displayName) + '</span></button>' +
      '<div class="nav-account-menu" id="accountMenu" role="menu" hidden>' +
      '<a role="menuitem" href="#/account">My account</a>' +
      (u.role === 'admin' ? '<a role="menuitem" href="#/admin">Admin</a>' : '') +
      '<button type="button" role="menuitem" id="accountLogout">Log out</button></div>';
    wrap.innerHTML = html;
    var btn = document.getElementById('accountToggle');
    var menu = document.getElementById('accountMenu');
    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      var open = menu.hidden;
      if (open) positionMenu();
      menu.hidden = !open;
      btn.setAttribute('aria-expanded', String(open));
    });
    menu.addEventListener('click', closeMenu);
    document.getElementById('accountLogout').addEventListener('click', function () {
      logout('#/home').then(function (r) {
        if (!r.ok && !r.cancelled) notify((r.data && r.data.error) || ('Logout failed (HTTP ' + r.status + ')'));
      }, function () { notify('Network error, try again.'); });
    });
  }

  document.addEventListener('click', closeMenu);
  window.addEventListener('resize', function () {
    var m = document.getElementById('accountMenu');
    if (m && !m.hidden) positionMenu();
  });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape') closeMenu(); });

  state.ready = Promise.resolve(window.MeshConfigReady).then(function () {
    if (!window.MC_USER_MGMT || !window.MC_USER_MGMT.enabled) return null;
    state.enabled = true;
    renderControl();
    return refreshMe();
  }).catch(function () { return null; });

  window.CSAuth = {
    request: request,
    refreshMe: refreshMe,
    logout: logout,
    setLogoutHandler: setLogoutHandler,
    say: say,
    errText: errText,
    setUser: setUser,
    notify: notify,
    ready: function () { return state.ready; },
    isEnabled: function () { return state.enabled; },
    user: function () { return state.user; },
    isAdmin: function () { return !!(state.user && state.user.role === 'admin'); },
    adminHeaders: function () {
      return (state.user && state.user.role === 'admin') ? { 'X-CS-CSRF': state.user.csrfToken } : {};
    },
    // Unit-test hook (tests/unit/test-user-management-ui.js).
    _test: { renderControl: renderControl, state: state }
  };
})();
