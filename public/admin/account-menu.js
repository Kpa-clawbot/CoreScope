// Shared account dropdown menu — trigger button showing username + role,
// opening a menu with Log out (and room for future items). Used by both
// admin.js and infrastructure.js so menu items only need to be added in
// one place. Mirrors the open/outside-click/Escape pattern used by the
// main app's favorites dropdown (see app.js favToggle/favDropdown and
// .nav-fav-dropdown in style.css).
(function () {
  'use strict';

  function escapeHTML(s) {
    var div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
  }

  function getCookie(name) {
    var match = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return match ? decodeURIComponent(match[1]) : '';
  }

  function logout() {
    fetch('/api/admin/logout', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'X-CSRF-Token': getCookie('corescope_admin_csrf') }
    }).then(function () {
      window.location.href = '/admin/login';
    });
  }

  // renderAccountMenu(me) builds the dropdown into <div id="who"> (inside
  // .nav-right). me is the {username, role} object from GET /api/admin/me.
  window.renderAccountMenu = function (me) {
    var whoEl = document.getElementById('who');
    if (!whoEl) return;

    whoEl.innerHTML =
      '<div class="account-menu-wrap" id="accountMenuWrap">' +
        '<button class="account-menu-btn" id="accountMenuBtn" type="button" aria-haspopup="true" aria-expanded="false">' +
          '<strong>' + escapeHTML(me.username) + '</strong>' +
          '<span class="role-badge ' + escapeHTML(me.role) + '">' + escapeHTML(me.role) + '</span>' +
          '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-caret-down"></use></svg>' +
        '</button>' +
        '<div class="account-menu" id="accountMenu" role="menu">' +
          '<div class="account-menu-header">Signed in as <strong>' + escapeHTML(me.username) + '</strong></div>' +
          '<button class="account-menu-item danger" type="button" role="menuitem" id="accountLogoutBtn">Log out</button>' +
        '</div>' +
      '</div>';

    var btn = document.getElementById('accountMenuBtn');
    var menu = document.getElementById('accountMenu');
    var open = false;

    // .account-menu is position:fixed (see admin.css for why — .top-nav's
    // overflow:hidden would otherwise clip it), so it isn't positioned by
    // CSS relative to the trigger. Compute top/right from the trigger's
    // bounding rect each time it opens, mirroring positionMoreMenu() in
    // the main app's app.js.
    function positionMenu() {
      var r = btn.getBoundingClientRect();
      menu.style.top = (r.bottom + 6) + 'px';
      menu.style.right = (window.innerWidth - r.right) + 'px';
      menu.style.left = 'auto';
    }

    function setOpen(v) {
      open = v;
      if (open) positionMenu();
      menu.classList.toggle('open', open);
      btn.setAttribute('aria-expanded', String(open));
    }

    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      setOpen(!open);
    });
    document.addEventListener('click', function (e) {
      if (open && !e.target.closest('#accountMenuWrap')) setOpen(false);
    });
    document.addEventListener('keydown', function (e) {
      if (open && e.key === 'Escape') setOpen(false);
    });
    document.getElementById('accountLogoutBtn').addEventListener('click', function () {
      setOpen(false);
      logout();
    });
  };
})();
