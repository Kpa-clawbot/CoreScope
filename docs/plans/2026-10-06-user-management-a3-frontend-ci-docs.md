# User Management A3 — Frontend, E2E/CI, Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
- The browser side: account control, account pages, admin user table.
- Admin-session support in the customizer and on the Perf page.
- An E2E build with a fake mailer and its Playwright suite in CI.
- Operator and API docs.

**Architecture:**
- `roles.js` already fetches `/api/config/client` and now also sets
  `window.MC_USER_MGMT`.
- `auth.js` waits for `MeshConfigReady` and does nothing unless that flag is set. When
  it is set, it renders the header control and keeps `window.CS_USER`.
- `account.js` and `admin-users.js` register SPA pages (`#/account/...`,
  `#/admin/users`) through the existing `registerPage`.
- The E2E binary is the normal server built with `-tags e2etest`. That build enables
  the `fake` mail provider and `GET /__e2e/last-mail`.

**Tech Stack:** Vanilla JS (no build step, no npm runtime deps), Playwright +
`@axe-core/playwright` (already dev deps), GitHub Actions.

**Prerequisite:** A1 and A2 complete. Spec: `docs/specs/2026-10-06-user-management-design.md`.
Ground rules: `docs/plans/2026-10-06-user-management-a.md`.

## File map

| File | Responsibility |
|---|---|
| `public/icons/phosphor-sprite.svg` | Add `ph-user-circle` |
| `public/roles.js` | Set `window.MC_USER_MGMT` from client config |
| `public/auth.js` | `window.CSAuth`: request helper with CSRF, header control, toast, `cs-auth-changed` |
| `public/account.js` | `account` page: login/register/activate/forgot/reset/confirm-email/profile |
| `public/admin-users.js` | `admin` page: `#/admin/users` table, actions, detail panel |
| `public/account.css` | Styles for the above (theme tokens only) |
| `public/index.html` | Link the CSS and the three scripts |
| `public/customize-v2.js` | Geofilter tab uses the admin session when present |
| `public/perf.js` | "Reset Stats" sends admin credentials |
| `cmd/server/auth_e2e.go` | `//go:build e2etest`: fake provider + `/__e2e/last-mail` |
| `tests/e2e/test-user-management-e2e.js` | Playwright suite |
| `.github/workflows/deploy.yml` | Module tests, e2etest build, second server, suite run |
| `config.example.json` | `userManagement` block + `_comment` |
| `docs/user-guide/accounts.md` | Operator + user guide |
| `docs/api-spec.md` | User-management section |
| `AGENTS.md` | Restated read/write invariant |

Every `innerHTML` in new code is built from string literals and `escapeHtml(...)` only.
That is what `scripts/check-xss-sinks.sh` and `test-xss-escape-sinks.js` check. Run
`bash scripts/check-xss-sinks.sh --file <file>` on each new JS file.

---

### Task 1: Icon, client flag, `auth.js`, CSS, script tags

**Files:**
- Modify: `public/icons/phosphor-sprite.svg`, `public/roles.js`, `public/index.html`
- Create: `public/auth.js`, `public/account.css`

- [ ] **Step 1: Add the `ph-user-circle` icon**

Fetch the official Phosphor "regular" glyph:
```bash
curl -s https://unpkg.com/@phosphor-icons/core@2/assets/regular/user-circle.svg
```
Insert a `<symbol>` with that path into `public/icons/phosphor-sprite.svg`, keeping the
ids in alphabetical order. The expected content is below; if the fetched path differs,
use the fetched one:
```xml
<symbol id="ph-user-circle" viewBox="0 0 256 256"><path d="M128,24A104,104,0,1,0,232,128,104.11,104.11,0,0,0,128,24ZM74.08,197.5a64,64,0,0,1,107.84,0,87.83,87.83,0,0,1-107.84,0ZM96,120a32,32,0,1,1,32,32A32,32,0,0,1,96,120Zm97.76,66.41a79.66,79.66,0,0,0-36.06-28.75,48,48,0,1,0-59.4,0,79.66,79.66,0,0,0-36.06,28.75,88,88,0,1,1,131.52,0Z"/></symbol>
```

- [ ] **Step 2: Publish the flag from `roles.js`**

In `public/roles.js`, directly after the `window.MC_CUSTOMIZER_CFG = …;` statement inside
the `MeshConfigReady` handler, add:
```js
    // Optional user management: present only when the server enables it.
    window.MC_USER_MGMT = (cfg.userManagement && cfg.userManagement.enabled) ? { enabled: true } : null;
```

- [ ] **Step 3: Create `public/auth.js`**

```js
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
        if (res.status === 401 && state.user && path !== '/api/auth/login') {
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
      wrap.innerHTML = '<a class="nav-btn nav-account-btn" id="accountToggle" href="#/account/login">' +
        icon('ph-user-circle') + '<span class="nav-account-label">Log in</span></a>';
      return;
    }
    var html = '<button class="nav-btn nav-account-btn" id="accountToggle" aria-haspopup="true" aria-expanded="false" aria-controls="accountMenu" title="Account">' +
      icon('ph-user-circle') + '<span class="nav-account-label">' + escapeHtml(u.displayName) + '</span></button>' +
      '<div class="nav-account-menu" id="accountMenu" role="menu" hidden>' +
      '<a role="menuitem" href="#/account">My account</a>' +
      (u.role === 'admin' ? '<a role="menuitem" href="#/admin/users">Users</a>' : '') +
      '<button type="button" role="menuitem" id="accountLogout">Log out</button></div>';
    wrap.innerHTML = html;
    var btn = document.getElementById('accountToggle');
    var menu = document.getElementById('accountMenu');
    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      var open = menu.hidden;
      menu.hidden = !open;
      btn.setAttribute('aria-expanded', String(open));
    });
    menu.addEventListener('click', closeMenu);
    document.getElementById('accountLogout').addEventListener('click', function () {
      request('POST', '/api/auth/logout').then(function () {
        setUser(null);
        location.hash = '#/home';
      });
    });
  }

  document.addEventListener('click', closeMenu);
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
    setUser: setUser,
    notify: notify,
    ready: function () { return state.ready; },
    isEnabled: function () { return state.enabled; },
    user: function () { return state.user; },
    isAdmin: function () { return !!(state.user && state.user.role === 'admin'); },
    adminHeaders: function () {
      return (state.user && state.user.role === 'admin') ? { 'X-CS-CSRF': state.user.csrfToken } : {};
    }
  };
})();
```

- [ ] **Step 4: Create `public/account.css`**

```css
/* Optional user management: header control, account pages, admin table. */
.nav-account-wrap { position: relative; }
.nav-account-btn { display: inline-flex; align-items: center; gap: 6px; text-decoration: none; color: var(--nav-text); }
.nav-account-label { max-width: 12ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-sm); }
@media (max-width: 640px) { .nav-account-label { display: none; } }
.nav-account-menu { position: absolute; right: 0; top: calc(100% + 4px); min-width: 160px; z-index: 1000;
  background: var(--card-bg); border: 1px solid var(--border); border-radius: 8px; padding: 4px 0; }
.nav-account-menu a, .nav-account-menu button { display: block; width: 100%; text-align: left; padding: 8px 14px; min-height: 40px;
  color: var(--text); background: none; border: 0; font: inherit; text-decoration: none; cursor: pointer; box-sizing: border-box; }
.nav-account-menu a:hover, .nav-account-menu button:hover { background: var(--row-hover); }

.account-page { display: flex; justify-content: center; padding: var(--space-md); }
.account-card { width: 100%; max-width: 520px; background: var(--card-bg); border: 1px solid var(--border); border-radius: 10px; padding: 20px; }
.account-card h2 { margin-top: 0; }
.account-card h3 { margin: 24px 0 8px; font-size: 1rem; }
.account-form { display: flex; flex-direction: column; gap: 12px; }
.account-field { display: flex; flex-direction: column; gap: 4px; font-size: var(--fs-sm); color: var(--text-muted); }
.account-field input { padding: 8px 10px; min-height: 40px; border: 1px solid var(--border); border-radius: 6px;
  background: var(--input-bg); color: var(--text); font: inherit; }
.account-form .btn-primary, .account-form .btn-secondary { align-self: flex-start; min-height: 40px; }
.account-hint { font-size: var(--fs-sm); color: var(--text-muted); margin: 0; }
.account-msg { margin: 0; min-height: 1.2em; font-size: var(--fs-sm); }
.account-msg.ok { color: var(--status-green); }
.account-msg.err { color: var(--status-red); }
.account-links { font-size: var(--fs-sm); }
.account-links a { color: var(--link-color); }
.account-sessions { list-style: none; padding: 0; margin: 0; }
.account-sessions li { display: flex; justify-content: space-between; align-items: center; gap: 8px; padding: 6px 0; border-bottom: 1px solid var(--border); font-size: var(--fs-sm); }

.cs-auth-toast { position: fixed; left: 50%; bottom: 24px; transform: translateX(-50%); z-index: 2000; opacity: 0; pointer-events: none;
  background: var(--card-bg); color: var(--text); border: 1px solid var(--border); border-radius: 8px; padding: 10px 16px; transition: opacity .2s; }
.cs-auth-toast.visible { opacity: 1; }

.um-page { padding: var(--space-md); }
.um-filters { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.um-filters input, .um-filters select { padding: 6px 8px; min-height: 36px; border: 1px solid var(--border); border-radius: 6px; background: var(--input-bg); color: var(--text); }
.um-table { width: 100%; border-collapse: collapse; font-size: var(--fs-sm); }
.um-table th, .um-table td { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--border); vertical-align: top; }
.um-table tr:hover td { background: var(--row-hover); }
.um-actions { display: flex; flex-wrap: wrap; gap: 4px; }
.um-actions button { min-height: 32px; padding: 4px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-1); color: var(--text); cursor: pointer; }
.um-status, .um-chip { display: inline-block; padding: 1px 8px; border-radius: 10px; border: 1px solid var(--border); white-space: nowrap; }
.um-status-active, .um-chip-delivered, .um-chip-opened, .um-chip-clicked { color: var(--status-green); }
.um-status-pending, .um-chip-sent, .um-chip-deferred, .um-chip-soft_bounce { color: var(--status-yellow); }
.um-status-disabled, .um-chip-hard_bounce, .um-chip-invalid_email, .um-chip-blocked, .um-chip-spam, .um-chip-error, .um-chip-bouncing { color: var(--status-red); }
.um-detail { margin-top: 16px; background: var(--card-bg); border: 1px solid var(--border); border-radius: 10px; padding: 16px; }
.um-detail ol { padding-left: 18px; }
@media (max-width: 760px) { .um-col-optional { display: none; } }
```

- [ ] **Step 5: Link CSS and scripts in `public/index.html`**

After `<link rel="stylesheet" href="bottom-nav.css?v=__BUST__">` add:
```html
  <link rel="stylesheet" href="account.css?v=__BUST__">
```
After `<script src="nav-drawer.js?v=__BUST__"></script>` add:
```html
  <script src="auth.js?v=__BUST__"></script>
  <script src="account.js?v=__BUST__" onerror="console.error('Failed to load:', this.src)"></script>
  <script src="admin-users.js?v=__BUST__" onerror="console.error('Failed to load:', this.src)"></script>
```
`account.js` and `admin-users.js` are created in Tasks 2–3. Until then the tags 404
harmlessly. Commit them together with Task 3 if you prefer a clean history.

- [ ] **Step 6: Lint**

Run:
```bash
node scripts/check-css-vars.js && bash scripts/check-xss-sinks.sh --file public/auth.js && sh test-all.sh
```
Expected: all pass. `check-css-vars` must not report undefined variables; every token
used above already exists in `style.css`.

- [ ] **Step 7: Commit**

```bash
git add public/icons/phosphor-sprite.svg public/roles.js public/auth.js public/account.css public/index.html
git commit -m "feat(ui): add the account control behind the user-management flag"
```

---

### Task 2: `account.js`

**Files:**
- Create: `public/account.js`

- [ ] **Step 1: Create the page module**

```js
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

  function tokenView(app, title, path, okText) {
    var token = query().get('token') || '';
    app.innerHTML = shell(title, msgBox() + '<p class="account-links"><a href="#/account/login">Log in</a></p>');
    if (!token) { say('This link is incomplete. Open the link from the mail again.', false); return; }
    say('Working…', true);
    return CSAuth.request('POST', path, { token: token }).then(function (r) {
      if (!r.ok) { say(errText(r), false); return r; }
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
        '<button type="submit" class="btn-primary">Log in</button>' + msgBox() + '</form>' +
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
        '<button type="submit" class="btn-primary">Create account</button>' + msgBox() + '</form>' +
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
      var p = tokenView(app, 'Activate your account', '/api/auth/activate', 'Your account is active. You are logged in.');
      if (p) p.then(function (r) { if (r && r.ok) CSAuth.setUser(r.data); });
    },

    forgot: function (app) {
      app.innerHTML = shell('Forgot password',
        '<form id="forgotForm" class="account-form" novalidate>' +
        field('forgotEmail', 'Email', 'email', 'username') +
        '<button type="submit" class="btn-primary">Send reset link</button>' + msgBox() + '</form>');
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
        '<button type="submit" class="btn-primary">Set password</button>' + msgBox() + '</form>' +
        '<p class="account-links"><a href="#/account/login">Log in</a></p>');
      onSubmit('resetForm', function () {
        if (val('resetPassword') !== val('resetPassword2')) { say('The passwords do not match.', false); return; }
        return CSAuth.request('POST', '/api/auth/reset', { token: token, password: val('resetPassword') }).then(function (r) {
          say(r.ok ? r.data.message : errText(r), r.ok);
        });
      });
    },

    'confirm-email': function (app) {
      var p = tokenView(app, 'Confirm your new address', '/api/account/confirm-email');
      if (p) p.then(function (r) { if (r && r.ok && CSAuth.user()) CSAuth.refreshMe(); });
    },

    profile: function (app) {
      var u = CSAuth.user();
      if (!u) { location.hash = '#/account/login'; return; }
      app.innerHTML = shell('My account',
        '<p class="account-hint">Signed in as ' + escapeHtml(u.email) + (u.role === 'admin' ? ' (admin)' : '') + '</p>' +
        '<h3>Profile</h3><form id="profileForm" class="account-form" novalidate>' +
        field('profName', 'Display name', 'text', 'nickname', ' minlength="2" maxlength="32"') +
        '<button type="submit" class="btn-primary">Save</button>' + msgBox('profMsg') + '</form>' +
        '<h3>Password</h3><form id="pwForm" class="account-form" novalidate>' +
        field('pwCurrent', 'Current password', 'password', 'current-password') +
        field('pwNew', 'New password', 'password', 'new-password', ' minlength="10" maxlength="128"') +
        '<button type="submit" class="btn-primary">Change password</button>' + msgBox('pwMsg') + '</form>' +
        '<h3>Email address</h3><form id="emailForm" class="account-form" novalidate>' +
        field('emailNew', 'New address', 'email', 'email') +
        field('emailPw', 'Current password', 'password', 'current-password') +
        '<button type="submit" class="btn-primary">Change address</button>' + msgBox('emailMsg') + '</form>' +
        '<h3>Devices</h3><ul class="account-sessions" id="sessList"></ul>' + msgBox('sessMsg') +
        '<h3>Delete account</h3><form id="delForm" class="account-form" novalidate>' +
        '<p class="account-hint">This removes your account permanently.</p>' +
        field('delPw', 'Current password', 'password', 'current-password') +
        '<button type="submit" class="btn-secondary">Delete my account</button>' + msgBox('delMsg') + '</form>');
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
        CSAuth.request('GET', '/api/account/sessions').then(function (r) {
          var list = document.getElementById('sessList');
          if (!list) return;
          if (!r.ok) { say(errText(r), false, 'sessMsg'); return; }
          var html = '';
          r.data.forEach(function (s) {
            html += '<li><span>' + escapeHtml(s.userAgent || 'Unknown device') + '<br><small>last seen ' + escapeHtml(fmtDate(s.lastSeenAt)) + '</small></span>' +
              (s.current ? '<span class="um-chip">this device</span>'
                         : '<button type="button" class="btn-secondary" data-sess="' + escapeHtml(String(s.id)) + '">Log out</button>') + '</li>';
          });
          list.innerHTML = html;
        });
      }
      document.getElementById('sessList').addEventListener('click', function (e) {
        var id = e.target && e.target.getAttribute('data-sess');
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
})();
```

- [ ] **Step 2: Lint and smoke-check**

Run: `bash scripts/check-xss-sinks.sh --file public/account.js && node -e "require('vm').createScript(require('fs').readFileSync('public/account.js','utf8'))"`
Expected: no XSS findings, no syntax error.

- [ ] **Step 3: Commit**

```bash
git add public/account.js
git commit -m "feat(ui): add the account pages for login, registration, reset and profile"
```

---

### Task 3: `admin-users.js`

**Files:**
- Create: `public/admin-users.js`

- [ ] **Step 1: Create the page module**

```js
/* #/admin/users — admin user management (optional user management).
 * Table with filters, per-row actions, and a detail panel with the mail
 * delivery timeline and audit log. Every dynamic value goes through escapeHtml. */
(function () {
  'use strict';
  var filters = { status: '', role: '', q: '' };
  var openId = null;

  function errText(r) { return (r.data && r.data.error) || ('Request failed (HTTP ' + r.status + ')'); }
  function fmt(iso) { if (!iso) return '—'; try { return new Date(iso).toLocaleString(); } catch (_) { return iso; } }
  function chip(event, reason) {
    var e = String(event || 'sent');
    return '<span class="um-chip um-chip-' + escapeHtml(e) + '" title="' + escapeHtml(reason || '') + '">' + escapeHtml(e.replace(/_/g, ' ')) + '</span>';
  }

  function actionsFor(u) {
    var me = CSAuth.user();
    var b = function (act, label) { return '<button type="button" data-act="' + act + '" data-id="' + escapeHtml(String(u.id)) + '">' + label + '</button>'; };
    var out = [b('detail', 'Details')];
    if (u.status === 'pending') { out.push(b('activate', 'Activate'), b('resend', 'Resend mail')); }
    if (u.status === 'active' && u.id !== me.id && !u.configAdmin) out.push(b('disable', 'Disable'));
    if (u.status === 'disabled') out.push(b('enable', 'Enable'));
    if (!u.configAdmin) out.push(u.role === 'admin' ? b('demote', 'Make user') : b('promote', 'Make admin'));
    if (u.id !== me.id && !u.configAdmin) out.push(b('delete', 'Delete'));
    return '<div class="um-actions">' + out.join('') + '</div>';
  }

  function rowHtml(u) {
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
      '<td>' + actionsFor(u) + '</td></tr>';
  }

  function load(app) {
    var q = new URLSearchParams();
    if (filters.status) q.set('status', filters.status);
    if (filters.role) q.set('role', filters.role);
    if (filters.q) q.set('q', filters.q);
    return CSAuth.request('GET', '/api/admin/users?' + q.toString()).then(function (r) {
      var body = document.getElementById('umBody');
      if (!body) return;
      if (!r.ok) { document.getElementById('umMsg').textContent = errText(r); return; }
      var html = '';
      r.data.forEach(function (u) { html += rowHtml(u); });
      body.innerHTML = html || '<tr><td colspan="8">No users match.</td></tr>';
      if (openId) showDetail(openId);
    });
  }

  function showDetail(id) {
    openId = id;
    CSAuth.request('GET', '/api/admin/users/' + encodeURIComponent(id)).then(function (r) {
      var el = document.getElementById('umDetail');
      if (!el) return;
      if (!r.ok) { el.hidden = true; return; }
      var d = r.data;
      var html = '<h3>' + escapeHtml(d.user.displayName) + ' — ' + escapeHtml(d.user.email) + '</h3>' +
        '<p class="account-hint">' + d.sessions.length + ' active session(s). "Opened" depends on tracking pixels: some mail apps load them automatically, others block them.</p>' +
        '<h4>Mail</h4><ol>';
      d.mail.forEach(function (m) {
        html += '<li>' + escapeHtml(m.purpose) + ' to ' + escapeHtml(m.to) + ', ' + escapeHtml(fmt(m.sentAt)) + ' ' + chip(m.lastEvent, m.lastReason) +
          ' <button type="button" data-act="refresh" data-id="' + escapeHtml(String(d.user.id)) + '" data-mail="' + escapeHtml(String(m.id)) + '">Refresh status</button>';
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
      d.audit.forEach(function (a) {
        html += '<li>' + escapeHtml(fmt(a.at)) + ': ' + escapeHtml(a.action) +
          (a.actorUserId != null ? ' by #' + escapeHtml(String(a.actorUserId)) : '') + '</li>';
      });
      html += '</ol>';
      el.innerHTML = html;
      el.hidden = false;
    });
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

  function onAction(app, e) {
    var btn = e.target.closest('button[data-act]');
    if (!btn) return;
    var act = btn.getAttribute('data-act');
    var id = btn.getAttribute('data-id');
    var msg = document.getElementById('umMsg');
    if (act === 'detail') { showDetail(id); return; }
    if (act === 'refresh') {
      CSAuth.request('POST', '/api/admin/users/' + encodeURIComponent(id) + '/mail/' + encodeURIComponent(btn.getAttribute('data-mail')) + '/refresh')
        .then(function (r) { msg.textContent = r.ok ? 'Mail status refreshed.' : errText(r); load(app); });
      return;
    }
    if (confirmText[act] && !confirm(confirmText[act])) return;
    var ep = endpoints[act];
    CSAuth.request(ep[0], '/api/admin/users/' + encodeURIComponent(id) + ep[1], ep[2]).then(function (r) {
      msg.textContent = r.ok ? 'Done.' : errText(r);
      if (act === 'delete' && r.ok && openId === id) { openId = null; document.getElementById('umDetail').hidden = true; }
      load(app);
    });
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
      app.innerHTML = '<div class="um-page"><h2>Users</h2>' +
        '<div class="um-filters">' +
        '<label>Search <input id="umQ" type="search" autocomplete="off"></label>' +
        '<label>Status <select id="umStatus"><option value="">any</option><option>pending</option><option>active</option><option>disabled</option></select></label>' +
        '<label>Role <select id="umRole"><option value="">any</option><option>user</option><option>admin</option></select></label>' +
        '</div><p class="account-msg" id="umMsg" role="status" aria-live="polite"></p>' +
        '<div style="overflow-x:auto"><table class="um-table"><thead><tr>' +
        '<th scope="col">Name</th><th scope="col">Email</th><th scope="col">Role</th><th scope="col">Status</th><th scope="col">Last mail</th>' +
        '<th scope="col" class="um-col-optional">Created</th><th scope="col" class="um-col-optional">Last login</th><th scope="col">Actions</th>' +
        '</tr></thead><tbody id="umBody"></tbody></table></div>' +
        '<section class="um-detail" id="umDetail" hidden></section></div>';
      var deb = debounce(function () { filters.q = document.getElementById('umQ').value.trim(); load(app); }, 250);
      document.getElementById('umQ').addEventListener('input', deb);
      document.getElementById('umStatus').addEventListener('change', function (e) { filters.status = e.target.value; load(app); });
      document.getElementById('umRole').addEventListener('change', function (e) { filters.role = e.target.value; load(app); });
      app.querySelector('.um-page').addEventListener('click', function (e) { onAction(app, e); });
      load(app);
    });
  }

  registerPage('admin', { init: init, destroy: function () { openId = null; } });
})();
```

- [ ] **Step 2: Lint**

Run: `bash scripts/check-xss-sinks.sh --file public/admin-users.js && node -e "require('vm').createScript(require('fs').readFileSync('public/admin-users.js','utf8'))"`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add public/admin-users.js
git commit -m "feat(ui): add the admin users page with actions and mail status"
```

---

### Task 4: Customizer geofilter and Perf reset use the admin session

**Files:**
- Modify: `public/customize-v2.js`, `public/perf.js`

- [ ] **Step 1: Add the header helper**

In `public/customize-v2.js`, directly above `function _gfSave(container) {`, add:
```js
  // Optional user management: a logged-in admin's session (cookie + CSRF
  // header) replaces the API key. Returns null when neither is available.
  function _gfAuthHeaders(container, base) {
    var h = Object.assign({}, base || {});
    if (window.CSAuth && CSAuth.isAdmin()) return Object.assign(h, CSAuth.adminHeaders());
    var apiKey = (container.querySelector('#cv2-gf-apikey') || {}).value || '';
    if (!apiKey) return null;
    h['X-API-Key'] = apiKey;
    return h;
  }
```

- [ ] **Step 2: Replace the four API-key reads**

In `_gfSave`, replace
```js
    var apiKey = (container.querySelector('#cv2-gf-apikey') || {}).value || '';
    if (!apiKey) { _gfMsg(container, 'API key required to save.', false); return; }
```
with
```js
    var headers = _gfAuthHeaders(container, { 'Content-Type': 'application/json' });
    if (!headers) { _gfMsg(container, 'API key required to save.', false); return; }
```
In `_gfRemove`, replace
```js
    var apiKey = (container.querySelector('#cv2-gf-apikey') || {}).value || '';
    if (!apiKey) { _gfMsg(container, 'API key required.', false); return; }
```
with
```js
    var headers = _gfAuthHeaders(container, { 'Content-Type': 'application/json' });
    if (!headers) { _gfMsg(container, 'API key required.', false); return; }
```
In both `_gfSave` and `_gfRemove`, replace
`headers: { 'Content-Type': 'application/json', 'X-API-Key': apiKey },` with `headers: headers,`.

In `_gfPrunePreview`, replace
```js
    var apiKey = (container.querySelector('#cv2-gf-apikey') || {}).value || '';
    if (!apiKey) { _gfPruneMsg(container, 'API key required.', false); return; }
```
with
```js
    var headers = _gfAuthHeaders(container, {});
    if (!headers) { _gfPruneMsg(container, 'API key required.', false); return; }
```
and `headers: { 'X-API-Key': apiKey }` with `headers: headers`.

In `_gfPruneConfirm`, make the same two-line replacement but with
`_gfAuthHeaders(container, { 'Content-Type': 'application/json' })`, and replace
`headers: { 'X-API-Key': apiKey, 'Content-Type': 'application/json' },` with
`headers: headers,`.

Then check: `grep -n "apiKey" public/customize-v2.js` should show only the
`_gfAuthHeaders` helper and the `#cv2-gf-apikey` input markup.

- [ ] **Step 3: Hide the key field for admins**

Replace
```js
        '<div class="cust-field"><label>Server API Key</label>' +
```
with
```js
        '<div class="cust-field"' + (window.CSAuth && CSAuth.isAdmin() ? ' hidden' : '') + '><label>Server API Key</label>' +
```

- [ ] **Step 4: Perf reset**

In `public/perf.js`, replace
```js
        await fetch('/api/perf/reset', { method: 'POST' });
```
with
```js
        await fetch('/api/perf/reset', { method: 'POST', headers: window.CSAuth ? CSAuth.adminHeaders() : {} });
```

- [ ] **Step 5: Run the existing suites**

Run: `sh test-all.sh && bash scripts/check-xss-sinks.sh --file public/customize-v2.js`
Expected: PASS. The customizer unit/E2E tests that use the API-key field keep working,
because `CSAuth` is absent or not admin there.

- [ ] **Step 6: Commit**

```bash
git add public/customize-v2.js public/perf.js
git commit -m "feat(ui): let a logged-in admin use the geofilter and perf-reset actions without the API key"
```

---

### Task 5: E2E build hooks

**Files:**
- Create: `cmd/server/auth_e2e.go`

- [ ] **Step 1: Create the build-tagged file**

```go
//go:build e2etest

package main

// E2E-only hooks, compiled only with -tags e2etest (CI's corescope-server-e2e):
// the "fake" mail provider and GET /__e2e/last-mail, which returns the newest
// fake mail so Playwright can follow its link. Never part of a release build.

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/mailer"
)

type e2eMail struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

func init() {
	fakeMailerAllowed = true
	e2eRoutes = func(s *Server, r *mux.Router) {
		r.HandleFunc("/__e2e/last-mail", func(w http.ResponseWriter, _ *http.Request) {
			f, ok := s.auth.mail.(*mailer.Fake)
			if !ok {
				writeError(w, http.StatusNotFound, "not using the fake mailer")
				return
			}
			m, _, ok := f.Last()
			if !ok {
				writeError(w, http.StatusNotFound, "no mail sent yet")
				return
			}
			writeJSON(w, e2eMail{To: m.To, Subject: m.Subject, Text: m.Text})
		}).Methods("GET")
	}
}
```

- [ ] **Step 2: Verify both builds**

Run: `cd cmd/server && go build -o /dev/null . && go build -tags e2etest -o /dev/null . && go vet -tags e2etest . && go test ./...`
Expected: both builds succeed and the normal tests pass. The release build has no
`/__e2e` route and rejects the `fake` provider (`TestResolveUserManagementErrors`).

- [ ] **Step 3: Commit**

```bash
git add cmd/server/auth_e2e.go
git commit -m "test(server): add e2etest build hooks for a fake mailer and the last-mail probe"
```

---

### Task 6: Playwright suite

**Files:**
- Create: `tests/e2e/test-user-management-e2e.js`

- [ ] **Step 1: Write the suite**

```js
/**
 * E2E: optional user management (docs/specs/2026-10-06-user-management-design.md).
 *   BASE_URL      server with userManagement on + fake mailer (-tags e2etest build)
 *   BASE_URL_OFF  the regular fixture server (feature off)
 * Usage: BASE_URL=http://localhost:13582 BASE_URL_OFF=http://localhost:13581 node tests/e2e/test-user-management-e2e.js
 */
'use strict';
const { chromium } = require('playwright');
const { AxeBuilder } = require('@axe-core/playwright');
const BASE = process.env.BASE_URL || 'http://localhost:13582';
const BASE_OFF = process.env.BASE_URL_OFF || 'http://localhost:13581';
const PW = 'correct horse battery';

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

async function lastMailToken(page) {
  const r = await page.request.get(BASE + '/__e2e/last-mail');
  assert(r.ok(), 'last-mail HTTP ' + r.status());
  const m = await r.json();
  const sm = /token=([A-Za-z0-9_%-]+)/.exec(m.text);
  assert(sm, 'no token in mail text: ' + m.text);
  return { to: m.to, token: decodeURIComponent(sm[1]) };
}

async function registerAndActivate(page, email, name) {
  await page.goto(BASE + '/#/account/register', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#registerForm');
  await page.fill('#regEmail', email);
  await page.fill('#regName', name);
  await page.fill('#regPassword', PW);
  await page.click('#registerForm button[type="submit"]');
  await page.waitForSelector('#accountMsg.ok');
  const { to, token } = await lastMailToken(page);
  assert(to === email, 'activation mail went to ' + to);
  await page.goto(BASE + '/#/account/activate?token=' + encodeURIComponent(token));
  await page.waitForSelector('#accountToggle .nav-account-label:has-text("' + name + '")');
}

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== user management E2E against ${BASE} (off: ${BASE_OFF}) ===`);

  const off = await (await browser.newContext()).newPage();
  off.setDefaultTimeout(8000);
  await step('feature off: no account control and no auth routes', async () => {
    await off.goto(BASE_OFF + '/', { waitUntil: 'domcontentloaded' });
    await off.waitForFunction(() => !!window.MeshConfigReady);
    await off.evaluate(() => window.MeshConfigReady);
    await off.waitForTimeout(300);
    assert(await off.locator('#accountToggle').count() === 0, 'account control rendered while off');
    const r = await off.request.get(BASE_OFF + '/api/auth/me');
    assert(r.status() === 404, '/api/auth/me with the feature off = ' + r.status());
  });

  const admin = await (await browser.newContext()).newPage();
  admin.setDefaultTimeout(8000);
  admin.on('dialog', (d) => d.accept());
  admin.on('pageerror', (e) => console.error('[pageerror admin]', e.message));
  await step('config admin registers, activates by mail link, sees the Users entry', async () => {
    await registerAndActivate(admin, 'admin@e2e.test', 'E2E Admin');
    await admin.click('#accountToggle');
    assert(await admin.locator('#accountMenu a[href="#/admin/users"]').isVisible(), 'no Users menu entry');
  });

  const user = await (await browser.newContext()).newPage();
  user.setDefaultTimeout(8000);
  user.on('pageerror', (e) => console.error('[pageerror user]', e.message));
  await step('second user registers and activates', async () => {
    await registerAndActivate(user, 'user@e2e.test', 'E2E User');
    assert(await user.locator('#accountMenu a[href="#/admin/users"]').count() === 0, 'non-admin sees Users');
  });

  await step('admin disables the user; the user is logged out', async () => {
    await admin.goto(BASE + '/#/admin/users');
    const row = admin.locator('tr[data-email="user@e2e.test"]');
    await row.waitFor();
    await row.locator('button[data-act="disable"]').click();
    await admin.waitForSelector('tr[data-email="user@e2e.test"] .um-status-disabled');
    await user.goto(BASE + '/#/home');
    await user.reload();
    await user.waitForSelector('#accountToggle .nav-account-label:has-text("Log in")');
  });

  await step('axe: no serious or critical violations on the new views', async () => {
    for (const [pg, route, sel] of [[user, '/#/account/login', '#loginForm'], [admin, '/#/admin/users', '.um-table']]) {
      await pg.goto(BASE + route);
      await pg.waitForSelector(sel);
      const res = await new AxeBuilder({ page: pg }).include('#app').withTags(['wcag2a', 'wcag2aa']).analyze();
      const bad = res.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
      assert(bad.length === 0, route + ': ' + bad.map((v) => v.id).join(', '));
    }
  });

  await browser.close();
  console.log('\n' + passed + '/' + (passed + failed) + ' tests passed');
  process.exit(failed > 0 ? 1 : 0);
})();
```

- [ ] **Step 2: Run it locally against two servers**

```bash
cd cmd/server && go build -o ../../corescope-server . && go build -tags e2etest -o ../../corescope-server-e2e . && cd ../..
mkdir -p "$TMPDIR/cs-um" && cat > "$TMPDIR/cs-um/config.json" <<'JSON'
{
  "port": 13582,
  "userManagement": {
    "enabled": true,
    "dbPath": "users.db",
    "adminEmails": ["admin@e2e.test"],
    "publicBaseUrl": "http://localhost:13582",
    "mail": { "provider": "fake", "fromEmail": "noreply@e2e.test" }
  }
}
JSON
./corescope-server -port 13581 -db test-fixtures/e2e-fixture.db -public public &
(cd "$TMPDIR/cs-um" && "$OLDPWD/corescope-server-e2e" -config-dir . -port 13582 -db "$OLDPWD/test-fixtures/e2e-fixture.db" -public "$OLDPWD/public") &
sleep 5
BASE_URL=http://localhost:13582 BASE_URL_OFF=http://localhost:13581 node tests/e2e/test-user-management-e2e.js
kill %1 %2
```
Expected: `5/5 tests passed`. On Windows use Git Bash, and set `TMPDIR` to the
scratchpad if it is unset. If the fixture DB is missing locally, generate it the way CI
does (see the "Seed" steps in `deploy.yml`), or skip the local run and rely on CI.

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/test-user-management-e2e.js
git commit -m "test(e2e): cover registration, mail activation, admin disable and a11y for user management"
```

---

### Task 7: CI wiring

**Files:**
- Modify: `.github/workflows/deploy.yml`

- [ ] **Step 1: Unit-test the new modules**

After the step `- name: Build and test channel library + decrypt CLI`, add:
```yaml
      - name: Test user-management modules (users, mailer)
        run: |
          set -e -o pipefail
          cd internal/users && go test -race ./...
          cd ../mailer && go test -race ./...
          echo "--- users + mailer tests passed ---"
```

- [ ] **Step 2: Build the e2etest binary**

In `- name: Build Go server`, after `go build -o ../../corescope-server .`, add:
```yaml
          go build -tags e2etest -o ../../corescope-server-e2e .
```

- [ ] **Step 3: Start the second server**

Directly after the step `- name: Start Go server with fixture DB`, add:
```yaml
      - name: Start user-management E2E server (fake mailer)
        run: |
          fuser -k 13582/tcp 2>/dev/null || true
          mkdir -p /tmp/cs-um
          cat > /tmp/cs-um/config.json <<'JSON'
          {
            "port": 13582,
            "userManagement": {
              "enabled": true,
              "dbPath": "/tmp/cs-um/users.db",
              "adminEmails": ["admin@e2e.test"],
              "publicBaseUrl": "http://localhost:13582",
              "mail": { "provider": "fake", "fromEmail": "noreply@e2e.test" }
            }
          }
          JSON
          ./corescope-server-e2e -config-dir /tmp/cs-um -port 13582 -db test-fixtures/e2e-fixture.db -public public-instrumented &
          for i in $(seq 1 30); do
            if curl -sf http://localhost:13582/api/healthz > /dev/null 2>&1; then echo "UM server ready after ${i}s"; break; fi
            if [ "$i" -eq 30 ]; then echo "UM server failed to start within 30s"; exit 1; fi
            sleep 1
          done
```

- [ ] **Step 4: Run the suite**

In `- name: Run Playwright E2E tests (fail-fast)`, after the `test-show-neighbors.js` lines,
add:
```yaml
          echo "=== E2E SUITE: test-user-management-e2e.js ==="
          BASE_URL=http://localhost:13582 BASE_URL_OFF=http://localhost:13581 node tests/e2e/test-user-management-e2e.js 2>&1 | tee -a e2e-output.txt
```

- [ ] **Step 5: Validate YAML and commit**

Run: `node -e "require('js-yaml')" 2>/dev/null && node -e "require('js-yaml').load(require('fs').readFileSync('.github/workflows/deploy.yml','utf8'))" || python -c "import yaml,sys; yaml.safe_load(open('.github/workflows/deploy.yml'))"`
Expected: no parse error.

```bash
git add .github/workflows/deploy.yml
git commit -m "ci: test the user-management modules and run its E2E suite against an e2etest server"
```

---

### Task 8: Documentation

**Files:**
- Modify: `config.example.json`, `docs/api-spec.md`, `AGENTS.md`
- Create: `docs/user-guide/accounts.md`

- [ ] **Step 1: `config.example.json`**

Insert before the final `"customizer": {` key (keep valid JSON: add a trailing comma to
the new block):
```json
  "userManagement": {
    "enabled": false,
    "dbPath": "",
    "adminEmails": ["operator@example.org"],
    "publicBaseUrl": "https://corescope.example.org",
    "sessionDays": 30,
    "trustedProxies": [],
    "mail": {
      "provider": "brevo",
      "brevoApiKey": "",
      "fromEmail": "noreply@example.org",
      "fromName": "CoreScope",
      "webhookSecret": ""
    }
  },
  "_comment_userManagement": "Optional accounts (off by default; see docs/user-guide/accounts.md). When enabled: visitors can register with email + password and activate through a mailed link; addresses in adminEmails become admins; admins manage users at #/admin/users and can use the operator endpoints without the apiKey. The dashboard stays public. dbPath defaults to users.db next to the analyzer database — a separate file, the server never writes measurement data. publicBaseUrl is required and is used for every link in every mail. Mail goes through Brevo's transactional API (a free account is enough); brevoApiKey and webhookSecret may instead come from CORESCOPE_BREVO_API_KEY and CORESCOPE_BREVO_WEBHOOK_SECRET. Set webhookSecret (16+ chars) and point a Brevo transactional webhook at <publicBaseUrl>/api/mail/brevo/webhook with bearer auth to see delivery status per user. trustedProxies (CIDRs) lets login rate limits use X-Forwarded-For behind a reverse proxy. Startup fails if enabled with an incomplete mail setup.",
```
Run: `node -e "JSON.parse(require('fs').readFileSync('config.example.json','utf8'))" && cd cmd/server && go test -run Config .`
Expected: valid JSON, config tests pass.

- [ ] **Step 2: `docs/user-guide/accounts.md`**

```markdown
# Accounts (optional user management)

CoreScope runs without accounts by default: every page is public and settings live in
each visitor's browser. Operators can turn on **accounts**. The dashboard stays public;
logging in only adds things.

- Visitors can register with an email address, a display name and a password, and
  activate the account through a link mailed to that address.
- **Admins** manage users (activate, disable, delete, promote) and can use the operator
  actions (geofilter save, prune, backup, perf reset) without the API key.

## For operators

### 1. Get a Brevo account

1. Create a free account at brevo.com. The free plan's daily volume is far more than
   account mails need.
2. Verify the sender address or domain you will send from (Brevo: *Senders, Domains &
   Dedicated IPs*). Mail from an unverified sender is rejected or lands in spam.
3. Create an API key (*SMTP & API → API Keys*).

### 2. Configure

Add to `config.json`:

```json
"userManagement": {
  "enabled": true,
  "adminEmails": ["you@example.org"],
  "publicBaseUrl": "https://corescope.example.org",
  "mail": { "fromEmail": "noreply@example.org", "fromName": "My CoreScope" }
}
```

Provide the API key as `mail.brevoApiKey` or, better, the environment variable
`CORESCOPE_BREVO_API_KEY`. Restart the server. It refuses to start if the mail setup is
incomplete, and the log says what is missing.

| Key | Meaning |
|---|---|
| `adminEmails` | Addresses that become admin on activation. They cannot be demoted, disabled or deleted from the UI. Remove an address here first. |
| `publicBaseUrl` | The address visitors use. Every mail link is built from it, never from the request. It must match the browser origin, because state-changing requests from another origin are refused. |
| `dbPath` | Where accounts are stored. Default: `users.db` next to the analyzer database. |
| `sessionDays` | Login lifetime, extended while in use. Default 30. |
| `trustedProxies` | CIDRs of your reverse proxy, so login rate limits can see real client IPs. Without it, limits apply per address only. |
| `mail.webhookSecret` | Enables delivery status (below). |

### 3. The first admin

Register at `#/account/register` with an address from `adminEmails` and click the
activation link. You are now admin and can promote others in **Users**.

### 4. Delivery status (optional)

To see per user whether mails were delivered, bounced, blocked or marked as spam:

1. Set `mail.webhookSecret` (16+ random characters), or `CORESCOPE_BREVO_WEBHOOK_SECRET`.
2. In Brevo, create a **transactional** webhook to
   `https://<publicBaseUrl>/api/mail/brevo/webhook` with these events: delivered,
   opened, clicked, soft bounce, hard bounce, invalid email, deferred, spam, blocked,
   error. Use **bearer** authentication with the same secret.

If Brevo cannot reach your instance, use **Refresh status** in a user's details instead:
CoreScope then asks Brevo directly. "Opened" is indicative only. Some mail apps load
tracking pixels automatically, and others block them.

### When mail fails

- In **Users**, *Resend mail* sends a fresh activation link.
- *Activate* activates a pending account by hand. The address is then unverified; the
  row shows "manual" and the audit log records who did it.

### Backups

`users.db` holds password hashes and addresses. Back it up together with the analyzer
database, and protect it the same way. Deleting it removes all accounts and nothing else.

## For users

- **Register:** *Log in → Create an account*, then click the link in the mail within 48
  hours.
- **Forgot password:** *Log in → Forgot password?* The link works once, for one hour, and
  logs out all your devices.
- **My account:** change your display name, password or address (confirmed from the new
  address), see your logged-in devices, or delete your account.

Your display name is visible to others; your email address is not. Channel keys you add
on the Channels page stay in your browser and are never sent to the server.
```

- [ ] **Step 3: `docs/api-spec.md`**

Add a section before `## GET /api/stats`:
```markdown
## User management (optional)

These routes exist only when `userManagement.enabled` is true; otherwise they return
404. Sessions use the `cs_session` cookie (HttpOnly, SameSite=Lax). Every
state-changing request with the cookie needs an `Origin` equal to `publicBaseUrl` and
the header `X-CS-CSRF: <csrfToken from GET /api/auth/me>`. Errors are
`{"error": "<message>"}`. Rate-limited calls answer `429` with `Retry-After`.

| Method & path | Auth | Body → response |
|---|---|---|
| `POST /api/auth/register` | origin | `{email, displayName, password}` → `{ok, message}` (identical for known addresses) |
| `POST /api/auth/activate` | origin | `{token}` → me + session cookie · `410` expired/used |
| `POST /api/auth/login` | origin | `{email, password}` → me + cookie · `401` generic |
| `POST /api/auth/logout` | origin | → `{ok}` |
| `GET /api/auth/me` | session | → `{id, email, displayName, role, csrfToken}` · `401` |
| `POST /api/auth/forgot` | origin | `{email}` → `{ok, message}` |
| `POST /api/auth/reset` | origin | `{token, password}` → `{ok, message}`; ends all sessions |
| `PATCH /api/account` | session | `{displayName}` → me |
| `POST /api/account/password` | session | `{currentPassword, newPassword}` → `{ok}`; ends other sessions |
| `POST /api/account/email` | session | `{newEmail, currentPassword}` → `{ok}`; confirmation mailed to the new address |
| `POST /api/account/confirm-email` | origin | `{token}` → `{ok}` |
| `GET /api/account/sessions` | session | → `[{id, createdAt, lastSeenAt, expiresAt, userAgent, current}]` |
| `DELETE /api/account/sessions/{id}` | session | → `{ok}` |
| `DELETE /api/account` | session | `{currentPassword}` → `{ok}` · `409` last admin |
| `GET /api/admin/users?status=&role=&q=` | admin | → `[adminUser]` |
| `GET /api/admin/users/{id}` | admin | → `{user, sessions, mail, audit}` |
| `POST /api/admin/users/{id}/disable` · `/enable` | admin | → adminUser |
| `DELETE /api/admin/users/{id}` | admin | → `{ok}` |
| `POST /api/admin/users/{id}/role` | admin | `{role}` → adminUser |
| `POST /api/admin/users/{id}/resend-activation` | admin | → adminUser |
| `POST /api/admin/users/{id}/activate` | admin | → adminUser (manual, address unverified) |
| `POST /api/admin/users/{id}/mail/{mailId}/refresh` | admin | → mail record with events |
| `POST /api/mail/brevo/webhook` | `Authorization: Bearer <webhookSecret>` | Brevo event(s) → `{ok}` |

`adminUser` = `{id, email, displayName, role, status, createdAt, activatedAt,
activatedManually, activatedBy, lastLoginAt, emailBouncing, configAdmin, lastMail}`.
Admin-only guards answer `409`: you cannot disable/delete yourself, config admins, or
the last admin.

With user management on, every endpoint that needs `X-API-Key` also accepts an admin
session (plus CSRF for unsafe methods). A request that sends `X-API-Key` is judged on
the key alone. `GET /api/config/client` gains `"userManagement": {"enabled": true}`
only when the feature is on.
```
Add a matching line to the Table of Contents.

- [ ] **Step 4: `AGENTS.md`**

Replace the bullet
```markdown
- **`cmd/server/` is read-only.** It opens SQLite with `mode=ro` and must not
  acquire a write lock. Adding a write-side helper (e.g. a `cachedRW`-style
  RW connection) regresses this invariant and races the ingestor → SQLITE_BUSY.
```
with
```markdown
- **`cmd/server/` never writes measurement data.** It opens the analyzer DB with
  `mode=ro` and must not acquire a write lock on it. Adding a write-side helper
  (e.g. a `cachedRW`-style RW connection) regresses this invariant and races the
  ingestor → SQLITE_BUSY.
- **Single exception: `users.db` (optional user management).** When
  `userManagement.enabled`, the server owns a *separate* SQLite file through
  `internal/users` only. `users.Open` refuses the analyzer DB path, and
  `TestUsersOpenIsTheOnlyServerWritePath` pins the one call site. Account data
  never goes into the analyzer DB, and measurement writes never go through
  `internal/users`.
```

- [ ] **Step 5: Commit**

```bash
git add config.example.json docs/user-guide/accounts.md docs/api-spec.md AGENTS.md
git commit -m "docs: document optional user management for operators, users and API clients"
```

---

## A3 done when

- `sh test-all.sh` passes. The XSS gate is clean on `auth.js`, `account.js`,
  `admin-users.js` and `customize-v2.js`. `check-css-vars` passes.
- `test-user-management-e2e.js` passes locally or in CI (5/5).
- With the feature off, the existing E2E suites pass unchanged. The off-check in the
  new suite proves that no account control renders.
- Manual check with a real Brevo key on staging, done once by the operator:
  1. A registration mail arrives.
  2. The webhook updates the status chip.
  3. *Refresh status* pulls events.
  4. The events-API names map to sensible chips.
  5. Any unmapped name shows up lowercased and gets added to `brevoEventNames`.
