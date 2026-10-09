/* Account pages for optional user management.
 *   #/account/login | register | activate?token= | forgot | reset?token= | confirm-email?token= | check-mail
 *   #/account                   : profile, password, address, sessions, my proposals, notifications, delete
 *   #/account/unsubscribe?token= : confirm turning notification mails off (link from a notification mail)
 * Every dynamic string goes through escapeHtml; messages use textContent. */
(function () {
  'use strict';

  function query() { return new URLSearchParams(location.hash.split('?')[1] || ''); }
  // takeToken reads a link token once and strips it from the address bar, so
  // it does not linger in history or a bookmark.
  function takeToken(view) {
    var token = query().get('token') || '';
    if (token) history.replaceState(null, '', '#/account/' + view);
    return token;
  }
  // Unknown names, inherited keys included, show the profile.
  function viewName(routeParam) {
    var name = routeParam || 'profile';
    return Object.prototype.hasOwnProperty.call(views, name) ? name : 'profile';
  }
  // The view the address bar shows (null when not on an account page).
  function currentView() {
    var parts = location.hash.split('?')[0].split('/');
    return parts[1] === 'account' ? viewName(parts[2]) : null;
  }
  function val(id) { var el = document.getElementById(id); return el ? el.value : ''; }

  function shell(title, inner) {
    return '<div class="account-page"><div class="account-card"><h2>' + escapeHtml(title) + '</h2>' + inner + '</div></div>';
  }
  // Inputs carry no name attribute: a native form submission (script not
  // attached) then sends no field, so a password can never reach the URL.
  function field(id, label, type, autocomplete, extra) {
    return '<label class="account-field" for="' + id + '"><span>' + escapeHtml(label) + '</span>' +
      '<input id="' + id + '" type="' + type + '" autocomplete="' + autocomplete + '" required' + (extra || '') + '></label>';
  }
  function submitBtn(label, secondary) {
    return '<button type="submit" class="account-btn ' + (secondary ? 'account-btn-secondary' : 'account-btn-primary') + '">' + escapeHtml(label) + '</button>';
  }
  function msgBox(id) { return '<p class="account-msg" id="' + (id || 'accountMsg') + '" role="status" aria-live="polite"></p>'; }
  function onSubmit(formId, fn, msgId) {
    var f = document.getElementById(formId);
    if (!f) return;
    f.addEventListener('submit', function (e) {
      e.preventDefault();
      var btn = f.querySelector('button[type="submit"]');
      if (btn) btn.disabled = true;
      Promise.resolve(fn(f)).catch(function () { CSAuth.say(msgId || 'accountMsg', 'Network error, try again.', false); })
        .then(function () { if (btn) btn.disabled = false; });
    });
  }
  // What the check-mail view shows. Module-level, never in the URL: a refresh
  // finds it empty and shows the generic text.
  var mailSent = null;
  // location.replace swaps the form's history entry, so Back skips it, and
  // fires hashchange for the router.
  function goCheckMail(kind, email) {
    mailSent = { kind: kind, email: email };
    location.replace('#/account/check-mail');
  }
  function checkMailHtml(sent) {
    var who = sent ? '<strong>' + escapeHtml(sent.email) + '</strong>' : '';
    var intro, hint = '', links;
    if (!sent) {
      intro = 'If the address can be used, we sent you a link. Check your mailbox.';
      links = '<a href="#/account/login">Log in</a>';
    } else if (sent.kind === 'forgot') {
      intro = 'If an account exists for ' + who + ', we sent a link to reset your password. It expires in 1 hour.';
      links = '<a href="#/account/login">Back to log in</a>';
    } else {
      intro = 'If ' + who + ' can be used, we sent an activation link to it. ' +
        'The link works once and expires in 48 hours. Open it and enter the password you just chose.';
      hint = 'No mail after a few minutes? Check your spam folder, or register again to get a new link.';
      links = '<a href="#/account/login">Log in</a> · <a href="#/account/register">Register again</a>';
    }
    return '<div class="account-page"><div class="account-card account-mail">' +
      '<svg class="ph-icon account-mail-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-envelope-simple"/></svg>' +
      '<h2 class="account-mail-title" id="mailSentHeading" tabindex="-1">Check your mailbox</h2>' +
      '<p class="account-mail-intro">' + intro + '</p>' + (hint ? '<p class="account-hint">' + hint + '</p>' : '') +
      '<p class="account-links">' + links + '</p></div></div>';
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
      '<div class="account-actions">' +
      '<button type="button" id="accountPageLogout" class="account-btn account-btn-secondary">Log out</button>' +
      (u.role === 'admin' ? '<a class="account-btn account-btn-secondary" href="#/admin">Admin</a>' : '') +
      '</div>' + msgBox('logoutMsg') +
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
      '<h3>Companions</h3><ul class="account-sessions" id="compList"></ul>' + msgBox('compMsg') +
      (window.CSProposals && window.CSProposals.enabled() ? '<h3>My proposals</h3><div id="propList"></div>' + msgBox('propMsg') : '') +
      (window.CSNotify && window.CSNotify.enabled() ? '<h3 id="notifications">Notifications</h3><div id="notifySection"></div>' + msgBox('notifyMsg') : '') +
      (window.CSSettingsSync ? '<h3>Settings sync</h3><div id="syncSection"></div>' : '') +
      '<h3>My data</h3>' +
      '<p class="account-hint">One JSON file with everything this instance stores about your account: profile, devices, synced settings, proposals, notifications, and your account and mail history. Passwords and login tokens are not included.</p>' +
      '<div class="account-actions"><a class="account-btn account-btn-secondary" id="accountExport" href="/api/account/export" download>Download my data</a></div>' +
      '<h3>Delete account</h3><form id="delForm" class="account-form" novalidate>' +
      '<p class="account-hint">This removes your account permanently. Server backups can still hold a copy for a few days until they rotate out.</p>' +
      field('delPw', 'Current password', 'password', 'current-password') +
      submitBtn('Delete my account', true) + msgBox('delMsg') + '</form>');
  }

  // A CoreDrive RX device token (kind "device") shows its label and a marker;
  // a browser session its user agent. Both are revoked with the same button.
  function sessionsHtml(list) {
    var html = '';
    (list || []).forEach(function (s) {
      var device = s.kind === 'device';
      var name = device ? 'CoreDrive RX' + (s.label ? ' – ' + s.label : '') : (s.userAgent || 'Unknown device');
      html += '<li' + (device ? ' data-kind="device"' : '') + '><span>' + escapeHtml(name) +
        (device ? ' <span class="um-chip um-chip-device">app</span>' : '') +
        '<br><small>last seen ' + escapeHtml(fmtDate(s.lastSeenAt)) + '</small></span>' +
        (s.current ? '<span class="um-chip">this device</span>'
                   : '<button type="button" class="account-btn account-btn-secondary" data-sess="' + escapeHtml(String(s.id)) + '">Log out</button>') + '</li>';
    });
    return html;
  }

  function shortKey(pk) { return String(pk || '').slice(0, 12) + '…'; }

  // Companions linked to this account (CoreDrive RX links them by signing a
  // challenge with the companion's key). Private to the owner.
  function companionsHtml(list) {
    if (!list || !list.length) {
      return '<li class="account-empty" id="compEmpty"><span>No companions linked yet. ' +
        'To link one, log in to this site from the CoreDrive RX app while it is connected to your companion; ' +
        'it then shows up here and in My nodes.</span></li>';
    }
    var html = '';
    list.forEach(function (c) {
      var name = c.name || shortKey(c.pubkey);
      html += '<li><span>' + escapeHtml(name) + ' <small><code>' + escapeHtml(shortKey(c.pubkey)) + '</code></small>' +
        '<br><small>linked ' + escapeHtml(fmtDate(c.linkedAt)) + ' · last seen ' +
        escapeHtml(c.lastSeenAt ? fmtDate(c.lastSeenAt) : 'never') + '</small></span>' +
        '<button type="button" class="account-btn account-btn-secondary" data-unlink="' + escapeHtml(c.pubkey) +
        '" aria-label="Unlink ' + escapeHtml(name) + '">Unlink</button></li>';
    });
    return html;
  }

  function tokenView(app, title, view, path, okText, renew) {
    var token = takeToken(view);
    app.innerHTML = shell(title, msgBox() + '<p class="account-links"><a href="#/account/login">Log in</a></p>');
    if (!token) { CSAuth.say('accountMsg', 'This link is incomplete. Open the link from the mail again.', false); return; }
    CSAuth.say('accountMsg', 'Working…', true);
    return CSAuth.request('POST', path, { token: token }).then(function (r) {
      if (!r.ok) { CSAuth.say('accountMsg', CSAuth.errText(r), false); if (renew) showGone(r, renew.href, renew.label); return r; }
      CSAuth.say('accountMsg', okText || (r.data && r.data.message) || 'Done.', true);
      return r;
    });
  }

  // The profile view on screen, so an auth change can redirect or re-render.
  var shown = { app: null, userId: null };

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
          if (!r.ok) { CSAuth.say('accountMsg', CSAuth.errText(r), false); return; }
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
      onSubmit('registerForm', function () {
        var email = val('regEmail');
        return CSAuth.request('POST', '/api/auth/register', {
          email: email, displayName: val('regName'), password: val('regPassword')
        }).then(function (r) {
          if (!r.ok) { CSAuth.say('accountMsg', CSAuth.errText(r), false); return; }
          goCheckMail('register', email);
        });
      });
    },

    'check-mail': function (app) {
      app.innerHTML = checkMailHtml(mailSent);
      var h = document.getElementById('mailSentHeading');
      if (h && h.focus) h.focus();
    },

    activate: function (app) {
      var token = takeToken('activate');
      if (!token) {
        app.innerHTML = shell('Activate your account', msgBox() + '<p class="account-links"><a href="#/account/login">Log in</a></p>');
        CSAuth.say('accountMsg', 'This link is incomplete. Open the link from the mail again.', false);
        return;
      }
      app.innerHTML = shell('Activate your account',
        '<form id="activateForm" class="account-form" novalidate>' +
        '<p class="account-hint">Enter the password you chose when you registered.</p>' +
        field('actPassword', 'Your password', 'password', 'current-password') +
        submitBtn('Activate') + msgBox() + '</form>' +
        '<p class="account-links"><a href="#/account/login">Log in</a></p>');
      onSubmit('activateForm', function () {
        return CSAuth.request('POST', '/api/auth/activate', { token: token, password: val('actPassword') }).then(function (r) {
          if (r.status === 401) { CSAuth.say('accountMsg', 'Wrong password for this account', false); return; }
          if (!r.ok) {
            CSAuth.say('accountMsg', CSAuth.errText(r), false);
            // An already active account needs a login, not a new registration.
            if (!/already activated/i.test(CSAuth.errText(r))) showGone(r, '#/account/register', 'Register again to get a new link');
            return;
          }
          CSAuth.say('accountMsg', 'Your account is active. You are logged in.', true);
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
        var email = val('forgotEmail');
        return CSAuth.request('POST', '/api/auth/forgot', { email: email }).then(function (r) {
          if (!r.ok) { CSAuth.say('accountMsg', CSAuth.errText(r), false); return; }
          goCheckMail('forgot', email);
        });
      });
    },

    reset: function (app) {
      var token = takeToken('reset');
      app.innerHTML = shell('Choose a new password',
        '<form id="resetForm" class="account-form" novalidate>' +
        field('resetPassword', 'New password', 'password', 'new-password', ' minlength="10" maxlength="128"') +
        field('resetPassword2', 'Repeat new password', 'password', 'new-password', ' minlength="10" maxlength="128"') +
        submitBtn('Set password') + msgBox() + '</form>' +
        '<p class="account-links"><a href="#/account/login">Log in</a></p>');
      onSubmit('resetForm', function () {
        if (val('resetPassword') !== val('resetPassword2')) { CSAuth.say('accountMsg', 'The passwords do not match.', false); return; }
        return CSAuth.request('POST', '/api/auth/reset', { token: token, password: val('resetPassword') }).then(function (r) {
          CSAuth.say('accountMsg', r.ok ? r.data.message : CSAuth.errText(r), r.ok);
          // The reset ended every session, this browser's included.
          if (r.ok) CSAuth.setUser(null);
          if (!r.ok) showGone(r, '#/account/forgot', 'Send a new link');
        });
      });
    },

    'confirm-email': function (app) {
      var p = tokenView(app, 'Confirm your new address', 'confirm-email', '/api/account/confirm-email', null,
        { href: '#/account', label: 'Send a new link from your account page' });
      if (p) p.then(function (r) { if (r && r.ok && CSAuth.user()) CSAuth.refreshMe(); });
    },

    unsubscribe: function (app) {
      var token = takeToken('unsubscribe');
      app.innerHTML = shell('Stop notification mails',
        '<p class="account-hint">This turns off all node notification mails for the account the link was sent to. You can turn them back on from your account page.</p>' +
        '<button type="button" id="unsubBtn" class="account-btn account-btn-primary">Turn notifications off</button>' + msgBox() +
        '<p class="account-links"><a href="#/account?section=notifications">Notification settings</a></p>');
      var btn = document.getElementById('unsubBtn');
      if (!token) {
        btn.disabled = true;
        CSAuth.say('accountMsg', 'This link is incomplete. Open the link from the mail again.', false);
        return;
      }
      btn.addEventListener('click', function () {
        btn.disabled = true;
        return CSAuth.request('POST', '/api/notifications/unsubscribe?token=' + encodeURIComponent(token)).then(function (r) {
          CSAuth.say('accountMsg', r.ok ? r.data.message : CSAuth.errText(r), r.ok);
          if (!r.ok) btn.disabled = false;
        }, function () { btn.disabled = false; CSAuth.say('accountMsg', 'Network error, try again.', false); });
      });
    },

    profile: function (app) {
      var u = CSAuth.user();
      if (!u) { location.hash = '#/account/login'; return; }
      shown.app = app;
      shown.userId = u.id;
      app.innerHTML = profileHtml(u);
      document.getElementById('profName').value = u.displayName;
      if (window.CSSettingsSync) window.CSSettingsSync.mountSection(document.getElementById('syncSection'));
      if (window.CSProposals && window.CSProposals.enabled()) window.CSProposals.loadMine(document.getElementById('propList'), 'propMsg');
      if (window.CSNotify && window.CSNotify.enabled()) {
        window.CSNotify.mountSection(document.getElementById('notifySection'), 'notifyMsg').then(function () {
          var h = query().get('section') === 'notifications' && document.getElementById('notifications');
          if (h && h.scrollIntoView) h.scrollIntoView();
        });
      }

      document.getElementById('accountPageLogout').addEventListener('click', function () {
        return CSAuth.logout('#/account/login').then(function (r) {
          if (!r.ok && !r.cancelled) CSAuth.say('logoutMsg', CSAuth.errText(r), false);
        }, function () { CSAuth.say('logoutMsg', 'Network error, try again.', false); });
      });

      onSubmit('profileForm', function () {
        return CSAuth.request('PATCH', '/api/account', { displayName: val('profName') }).then(function (r) {
          if (r.ok) CSAuth.setUser(r.data);
          CSAuth.say('profMsg', r.ok ? 'Saved.' : CSAuth.errText(r), r.ok);
        });
      }, 'profMsg');
      onSubmit('pwForm', function (form) {
        return CSAuth.request('POST', '/api/account/password', { currentPassword: val('pwCurrent'), newPassword: val('pwNew') }).then(function (r) {
          if (r.ok) form.reset();
          CSAuth.say('pwMsg', r.ok ? r.data.message : CSAuth.errText(r), r.ok);
          if (r.ok) loadSessions();
        });
      }, 'pwMsg');
      onSubmit('emailForm', function (form) {
        return CSAuth.request('POST', '/api/account/email', { newEmail: val('emailNew'), currentPassword: val('emailPw') }).then(function (r) {
          if (r.ok) form.reset();
          CSAuth.say('emailMsg', r.ok ? r.data.message : CSAuth.errText(r), r.ok);
        });
      }, 'emailMsg');
      onSubmit('delForm', function () {
        if (!confirm('Delete your account permanently?')) return;
        return CSAuth.request('DELETE', '/api/account', { currentPassword: val('delPw') }).then(function (r) {
          if (!r.ok) { CSAuth.say('delMsg', CSAuth.errText(r), false); return; }
          CSAuth.setUser(null);
          CSAuth.notify('Your account was deleted.');
          location.hash = '#/home';
        });
      }, 'delMsg');

      function loadSessions() {
        return CSAuth.request('GET', '/api/account/sessions').then(function (r) {
          var list = document.getElementById('sessList');
          if (!list) return;
          if (!r.ok) { CSAuth.say('sessMsg', CSAuth.errText(r), false); return; }
          list.innerHTML = sessionsHtml(r.data);
        }).catch(function () { CSAuth.say('sessMsg', 'Network error, try again.', false); });
      }
      document.getElementById('sessList').addEventListener('click', function (e) {
        var id = e.target && e.target.getAttribute && e.target.getAttribute('data-sess');
        if (!id) return;
        CSAuth.request('DELETE', '/api/account/sessions/' + encodeURIComponent(id)).then(function (r) {
          CSAuth.say('sessMsg', r.ok ? 'Device logged out.' : CSAuth.errText(r), r.ok);
          loadSessions();
        }).catch(function () { CSAuth.say('sessMsg', 'Network error, try again.', false); });
      });
      loadSessions();

      function loadCompanions() {
        return CSAuth.request('GET', '/api/account/companions').then(function (r) {
          var list = document.getElementById('compList');
          if (!list) return;
          if (!r.ok) { CSAuth.say('compMsg', CSAuth.errText(r), false); return; }
          list.innerHTML = companionsHtml(r.data);
        }).catch(function () { CSAuth.say('compMsg', 'Network error, try again.', false); });
      }
      document.getElementById('compList').addEventListener('click', function (e) {
        var pk = e.target && e.target.getAttribute && e.target.getAttribute('data-unlink');
        if (!pk) return;
        if (!window.confirm('Unlink this companion? It stays in My nodes, but its coverage no longer counts as yours. ' +
          'Link it again by logging in from CoreDrive RX.')) return;
        return CSAuth.request('DELETE', '/api/account/companions/' + encodeURIComponent(pk)).then(function (r) {
          CSAuth.say('compMsg', r.ok ? 'Companion unlinked.' : CSAuth.errText(r), r.ok);
          loadCompanions();
        }).catch(function () { CSAuth.say('compMsg', 'Network error, try again.', false); });
      });
      loadCompanions();
    }
  };

  function init(app, routeParam) {
    if (!window.CSAuth) { app.innerHTML = shell('Accounts', '<p>Accounts are not available.</p>'); return; }
    app.innerHTML = shell('Accounts', '<p>Loading…</p>');
    CSAuth.ready().then(function () {
      if (!CSAuth.isEnabled()) { app.innerHTML = shell('Accounts', '<p>Accounts are not enabled on this instance.</p>'); return; }
      views[viewName(routeParam)](app);
    });
  }

  // Logout (here, in the header or by a 401) leaves the profile for the
  // login view; another user logging in re-renders it. A display-name save
  // is the same user and keeps the form and its message.
  window.addEventListener('cs-auth-changed', function (e) {
    if (currentView() !== 'profile' || !shown.app) return;
    var u = e.detail;
    if (!u) { location.hash = '#/account/login'; return; }
    if (u.id !== shown.userId) views.profile(shown.app);
  });

  registerPage('account', { init: init, destroy: function () {} });
  window.CSAccount = { _test: { profileHtml: profileHtml, sessionsHtml: sessionsHtml, companionsHtml: companionsHtml, renewLinkHtml: renewLinkHtml, views: views, checkMailHtml: checkMailHtml } };
})();
