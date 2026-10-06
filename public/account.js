/* Account pages for optional user management.
 *   #/account/login | register | activate?token= | forgot | reset?token= | confirm-email?token=
 *   #/account                     — profile, password, address, sessions, delete
 * Every dynamic string goes through escapeHtml; messages use textContent. */
(function () {
  'use strict';

  function query() { return new URLSearchParams(location.hash.split('?')[1] || ''); }
  function val(id) { var el = document.getElementById(id); return el ? el.value : ''; }

  function shell(title, inner) {
    return '<div class="account-page"><div class="account-card"><h2>' + escapeHtml(title) + '</h2>' + inner + '</div></div>';
  }
  function field(id, label, type, autocomplete, extra) {
    return '<label class="account-field" for="' + id + '"><span>' + escapeHtml(label) + '</span>' +
      '<input id="' + id + '" name="' + id + '" type="' + type + '" autocomplete="' + autocomplete + '" required' + (extra || '') + '></label>';
  }
  function submitBtn(label, secondary) {
    return '<button type="submit" class="account-btn ' + (secondary ? 'account-btn-secondary' : 'account-btn-primary') + '">' + escapeHtml(label) + '</button>';
  }
  function msgBox(id) { return '<p class="account-msg" id="' + (id || 'accountMsg') + '" role="status" aria-live="polite"></p>'; }
  function say(text, ok, id) {
    var el = document.getElementById(id || 'accountMsg');
    if (!el) return;
    el.textContent = text;
    el.classList.toggle('ok', !!ok);
    el.classList.toggle('err', !ok);
  }
  function errText(r) { return (r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')'); }
  function onSubmit(formId, fn) {
    var f = document.getElementById(formId);
    if (!f) return;
    f.addEventListener('submit', function (e) {
      e.preventDefault();
      var btn = f.querySelector('button[type="submit"]');
      if (btn) btn.disabled = true;
      Promise.resolve(fn(f)).catch(function () { say('Network error, try again.', false); })
        .then(function () { if (btn) btn.disabled = false; });
    });
  }
  function fmtDate(iso) { try { return new Date(iso).toLocaleString(); } catch (_) { return iso; } }

  // An expired or used link answers 410: offer the way to get a new one.
  function renewLinkHtml(href, label) {
    return '<p class="account-links" id="renewLink"><a href="' + escapeHtml(href) + '">' + escapeHtml(label) + '</a></p>';
  }
  function showGone(r, href, label) {
    if (r.status !== 410) return;
    var box = document.getElementById('accountMsg');
    if (box && !document.getElementById('renewLink')) box.insertAdjacentHTML('afterend', renewLinkHtml(href, label));
  }

  function profileHtml(u) {
    return shell('My account',
      '<p class="account-hint">Signed in as ' + escapeHtml(u.email) + (u.role === 'admin' ? ' (admin)' : '') + '</p>' +
      '<h3>Profile</h3><form id="profileForm" class="account-form" novalidate>' +
      field('profName', 'Display name', 'text', 'nickname', ' minlength="2" maxlength="32"') +
      submitBtn('Save') + msgBox('profMsg') + '</form>' +
      '<h3>Password</h3><form id="pwForm" class="account-form" novalidate>' +
      field('pwCurrent', 'Current password', 'password', 'current-password') +
      field('pwNew', 'New password', 'password', 'new-password', ' minlength="10" maxlength="128"') +
      submitBtn('Change password') + msgBox('pwMsg') + '</form>' +
      '<h3>Email address</h3><form id="emailForm" class="account-form" novalidate>' +
      field('emailNew', 'New address', 'email', 'email') +
      field('emailPw', 'Current password', 'password', 'current-password') +
      submitBtn('Change address') + msgBox('emailMsg') + '</form>' +
      '<h3>Devices</h3><ul class="account-sessions" id="sessList"></ul>' + msgBox('sessMsg') +
      '<h3>Delete account</h3><form id="delForm" class="account-form" novalidate>' +
      '<p class="account-hint">This removes your account permanently.</p>' +
      field('delPw', 'Current password', 'password', 'current-password') +
      submitBtn('Delete my account', true) + msgBox('delMsg') + '</form>');
  }

  function sessionsHtml(list) {
    var html = '';
    (list || []).forEach(function (s) {
      html += '<li><span>' + escapeHtml(s.userAgent || 'Unknown device') + '<br><small>last seen ' + escapeHtml(fmtDate(s.lastSeenAt)) + '</small></span>' +
        (s.current ? '<span class="um-chip">this device</span>'
                   : '<button type="button" class="account-btn account-btn-secondary" data-sess="' + escapeHtml(String(s.id)) + '">Log out</button>') + '</li>';
    });
    return html;
  }

  function tokenView(app, title, path, okText, renew) {
    var token = query().get('token') || '';
    app.innerHTML = shell(title, msgBox() + '<p class="account-links"><a href="#/account/login">Log in</a></p>');
    if (!token) { say('This link is incomplete. Open the link from the mail again.', false); return; }
    say('Working…', true);
    return CSAuth.request('POST', path, { token: token }).then(function (r) {
      if (!r.ok) { say(errText(r), false); if (renew) showGone(r, renew.href, renew.label); return r; }
      say(okText || (r.data && r.data.message) || 'Done.', true);
      return r;
    });
  }

  var views = {
    login: function (app) {
      app.innerHTML = shell('Log in',
        '<form id="loginForm" class="account-form" novalidate>' +
        field('loginEmail', 'Email', 'email', 'username') +
        field('loginPassword', 'Password', 'password', 'current-password') +
        submitBtn('Log in') + msgBox() + '</form>' +
        '<p class="account-links"><a href="#/account/forgot">Forgot password?</a> · <a href="#/account/register">Create an account</a></p>');
      onSubmit('loginForm', function () {
        return CSAuth.request('POST', '/api/auth/login', { email: val('loginEmail'), password: val('loginPassword') }).then(function (r) {
          if (!r.ok) { say(errText(r), false); return; }
          CSAuth.setUser(r.data);
          location.hash = '#/account';
        });
      });
    },

    register: function (app) {
      app.innerHTML = shell('Create an account',
        '<form id="registerForm" class="account-form" novalidate>' +
        field('regEmail', 'Email', 'email', 'email') +
        field('regName', 'Display name', 'text', 'nickname', ' minlength="2" maxlength="32"') +
        field('regPassword', 'Password', 'password', 'new-password', ' minlength="10" maxlength="128"') +
        '<p class="account-hint">At least 10 characters. Others see your display name, never your email.</p>' +
        submitBtn('Create account') + msgBox() + '</form>' +
        '<p class="account-links">Already registered? <a href="#/account/login">Log in</a></p>');
      onSubmit('registerForm', function (form) {
        return CSAuth.request('POST', '/api/auth/register', {
          email: val('regEmail'), displayName: val('regName'), password: val('regPassword')
        }).then(function (r) {
          if (!r.ok) { say(errText(r), false); return; }
          form.reset();
          say(r.data.message || 'Check your inbox for the activation link.', true);
        });
      });
    },

    activate: function (app) {
      var token = query().get('token') || '';
      app.innerHTML = shell('Activate your account',
        '<form id="activateForm" class="account-form" novalidate>' +
        '<p class="account-hint">Enter the password you chose when you registered.</p>' +
        field('actPassword', 'Your password', 'password', 'current-password') +
        submitBtn('Activate') + msgBox() + '</form>' +
        '<p class="account-links"><a href="#/account/login">Log in</a></p>');
      if (!token) { say('This link is incomplete. Open the link from the mail again.', false); return; }
      onSubmit('activateForm', function () {
        return CSAuth.request('POST', '/api/auth/activate', { token: token, password: val('actPassword') }).then(function (r) {
          if (r.status === 401) { say('Wrong password for this account', false); return; }
          if (!r.ok) {
            say(errText(r), false);
            showGone(r, '#/account/register', 'Register again to get a new link');
            return;
          }
          say('Your account is active. You are logged in.', true);
          CSAuth.setUser(r.data);
          location.hash = '#/account';
        });
      });
    },

    forgot: function (app) {
      app.innerHTML = shell('Forgot password',
        '<form id="forgotForm" class="account-form" novalidate>' +
        field('forgotEmail', 'Email', 'email', 'username') +
        submitBtn('Send reset link') + msgBox() + '</form>');
      onSubmit('forgotForm', function () {
        return CSAuth.request('POST', '/api/auth/forgot', { email: val('forgotEmail') }).then(function (r) {
          say(r.ok ? r.data.message : errText(r), r.ok);
        });
      });
    },

    reset: function (app) {
      var token = query().get('token') || '';
      app.innerHTML = shell('Choose a new password',
        '<form id="resetForm" class="account-form" novalidate>' +
        field('resetPassword', 'New password', 'password', 'new-password', ' minlength="10" maxlength="128"') +
        field('resetPassword2', 'Repeat new password', 'password', 'new-password', ' minlength="10" maxlength="128"') +
        submitBtn('Set password') + msgBox() + '</form>' +
        '<p class="account-links"><a href="#/account/login">Log in</a></p>');
      onSubmit('resetForm', function () {
        if (val('resetPassword') !== val('resetPassword2')) { say('The passwords do not match.', false); return; }
        return CSAuth.request('POST', '/api/auth/reset', { token: token, password: val('resetPassword') }).then(function (r) {
          say(r.ok ? r.data.message : errText(r), r.ok);
          if (!r.ok) showGone(r, '#/account/forgot', 'Send a new link');
        });
      });
    },

    'confirm-email': function (app) {
      var p = tokenView(app, 'Confirm your new address', '/api/account/confirm-email', null,
        { href: '#/account', label: 'Send a new link from your account page' });
      if (p) p.then(function (r) { if (r && r.ok && CSAuth.user()) CSAuth.refreshMe(); });
    },

    profile: function (app) {
      var u = CSAuth.user();
      if (!u) { location.hash = '#/account/login'; return; }
      app.innerHTML = profileHtml(u);
      document.getElementById('profName').value = u.displayName;

      onSubmit('profileForm', function () {
        return CSAuth.request('PATCH', '/api/account', { displayName: val('profName') }).then(function (r) {
          if (r.ok) CSAuth.setUser(r.data);
          say(r.ok ? 'Saved.' : errText(r), r.ok, 'profMsg');
        });
      });
      onSubmit('pwForm', function (form) {
        return CSAuth.request('POST', '/api/account/password', { currentPassword: val('pwCurrent'), newPassword: val('pwNew') }).then(function (r) {
          if (r.ok) form.reset();
          say(r.ok ? r.data.message : errText(r), r.ok, 'pwMsg');
          if (r.ok) loadSessions();
        });
      });
      onSubmit('emailForm', function (form) {
        return CSAuth.request('POST', '/api/account/email', { newEmail: val('emailNew'), currentPassword: val('emailPw') }).then(function (r) {
          if (r.ok) form.reset();
          say(r.ok ? r.data.message : errText(r), r.ok, 'emailMsg');
        });
      });
      onSubmit('delForm', function () {
        if (!confirm('Delete your account permanently?')) return;
        return CSAuth.request('DELETE', '/api/account', { currentPassword: val('delPw') }).then(function (r) {
          if (!r.ok) { say(errText(r), false, 'delMsg'); return; }
          CSAuth.setUser(null);
          CSAuth.notify('Your account was deleted.');
          location.hash = '#/home';
        });
      });

      function loadSessions() {
        return CSAuth.request('GET', '/api/account/sessions').then(function (r) {
          var list = document.getElementById('sessList');
          if (!list) return;
          if (!r.ok) { say(errText(r), false, 'sessMsg'); return; }
          list.innerHTML = sessionsHtml(r.data);
        });
      }
      document.getElementById('sessList').addEventListener('click', function (e) {
        var id = e.target && e.target.getAttribute && e.target.getAttribute('data-sess');
        if (!id) return;
        CSAuth.request('DELETE', '/api/account/sessions/' + encodeURIComponent(id)).then(function (r) {
          say(r.ok ? 'Device logged out.' : errText(r), r.ok, 'sessMsg');
          loadSessions();
        });
      });
      loadSessions();
    }
  };

  function init(app, routeParam) {
    if (!window.CSAuth) { app.innerHTML = shell('Accounts', '<p>Accounts are not available.</p>'); return; }
    app.innerHTML = shell('Accounts', '<p>Loading…</p>');
    CSAuth.ready().then(function () {
      if (!CSAuth.isEnabled()) { app.innerHTML = shell('Accounts', '<p>Accounts are not enabled on this instance.</p>'); return; }
      var view = views[routeParam || 'profile'] || views.profile;
      view(app);
    });
  }

  registerPage('account', { init: init, destroy: function () {} });
  window.CSAccount = { _test: { profileHtml: profileHtml, sessionsHtml: sessionsHtml, renewLinkHtml: renewLinkHtml, views: views } };
})();
