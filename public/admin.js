/* #/admin: the admin area of optional user management
 * (docs/specs/2026-10-07-admin-dashboard-design.md). A tab shell; each tab
 * is a module with mount(container) and unmount():
 *   overview  window.CSAdminOverview (admin-overview.js)
 *   users     window.CSAdminUsers    (admin-users.js)
 *   audit     window.CSAdminAudit    (admin-audit.js)
 * Deep link: #/admin?tab=<tab>&<tab filters>. The old #/admin/users?... is
 * rewritten to #/admin?tab=users&... with replaceState. Access relies on
 * the server's withAdmin check and CSAuth.isAdmin(); the shell adds no
 * access mechanism of its own. */
(function () {
  'use strict';
  var TABS = [
    { id: 'overview', label: 'Overview', mod: 'CSAdminOverview' },
    { id: 'users', label: 'Users', mod: 'CSAdminUsers' },
    { id: 'audit', label: 'Audit', mod: 'CSAdminAudit' }
  ];
  // The page on screen, whether it was rendered for an admin, and the
  // mounted tab, so an auth change can redirect or re-render.
  var mounted = { app: null, admin: false, tab: null };
  // Bumped by every init and destroy, so an init whose CSAuth.ready()
  // resolves after the page was left (or re-rendered) mounts nothing.
  var renderSeq = 0;

  function readTab(hash) {
    var t = new URLSearchParams(String(hash || '').split('?')[1] || '').get('tab');
    for (var i = 0; i < TABS.length; i++) if (TABS[i].id === t) return TABS[i];
    return TABS[0];
  }

  // #/admin/users?x=1 becomes #/admin?tab=users&x=1; any other hash: null.
  function legacyRewrite(hash) {
    var m = /^#\/admin\/users(?:\?(.*))?$/.exec(String(hash || ''));
    if (!m) return null;
    return '#/admin?tab=users' + (m[1] ? '&' + m[1] : '');
  }

  function tabsHtml(active) {
    var html = '<nav class="admin-tabs" aria-label="Admin sections">';
    TABS.forEach(function (t) {
      var on = t.id === active;
      html += '<a class="tab-btn' + (on ? ' active' : '') + '" href="#/admin?tab=' + t.id + '"' +
        (on ? ' aria-current="page"' : '') + '>' + t.label + '</a>';
    });
    return html + '</nav>';
  }

  function page(inner) { return '<div class="um-page admin-page">' + inner + '</div>'; }

  function unmountTab() {
    if (mounted.tab) window[mounted.tab.mod].unmount();
    mounted.tab = null;
  }

  function init(app, routeParam) {
    unmountTab();
    var seq = ++renderSeq;
    var legacy = routeParam === 'users' ? legacyRewrite(location.hash) : null;
    if (legacy) {
      history.replaceState(null, '', legacy);
      routeParam = null;
    }
    app.innerHTML = page('<p>Loading…</p>');
    var ready = window.CSAuth ? CSAuth.ready() : Promise.resolve();
    return ready.then(function () {
      if (seq !== renderSeq) return;
      if (routeParam || !window.CSAuth || !CSAuth.isEnabled()) {
        app.innerHTML = page('<h2>Not found</h2>');
        return;
      }
      mounted.app = app;
      mounted.admin = CSAuth.isAdmin();
      if (!mounted.admin) {
        app.innerHTML = page('<h2>Admin</h2><p>Admins only. <a href="#/account/login">Log in</a></p>');
        return;
      }
      var tab = readTab(location.hash);
      app.innerHTML = page('<h2>Admin</h2>' + tabsHtml(tab.id) + '<div id="adminTab"></div>');
      mounted.tab = tab;
      window[tab.mod].mount(document.getElementById('adminTab'));
    }).catch(function () {
      if (seq !== renderSeq) return;
      app.innerHTML = page('<h2>Admin</h2><p>Network error, reload the page.</p>');
    });
  }

  // Logout (header, account page or a 401) goes to the login view; a login
  // or role change that flips admin access re-renders the page.
  window.addEventListener('cs-auth-changed', function (e) {
    // A logout that already moved elsewhere (the header goes home) wins.
    if (!mounted.app || location.hash.split('?')[0] !== '#/admin') return;
    if (!e.detail) { location.hash = '#/account/login'; return; }
    if (CSAuth.isAdmin() !== mounted.admin) init(mounted.app, null);
  });

  registerPage('admin', { init: init, destroy: function () { renderSeq++; unmountTab(); mounted.app = null; } });
  window.CSAdmin = { _test: { readTab: readTab, legacyRewrite: legacyRewrite, tabsHtml: tabsHtml } };
})();
