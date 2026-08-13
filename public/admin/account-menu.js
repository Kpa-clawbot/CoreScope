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

  function csrfHeaders() {
    return { 'X-CSRF-Token': getCookie('corescope_admin_csrf') };
  }

  function logout() {
    fetch('/api/admin/logout', {
      method: 'POST',
      credentials: 'same-origin',
      headers: csrfHeaders()
    }).then(function () {
      window.location.href = '/admin/login';
    });
  }

  // Modal is built once and reused across opens (not rebuilt per
  // renderAccountMenu call, since #who — and the account menu inside it
  // — gets re-rendered by loadAdmins()/loadInfraList() refresh cycles on
  // the pages that use it; the modal itself doesn't need to move).
  var modalBackdrop = null;

  function ensureChangePasswordModal() {
    if (modalBackdrop) return modalBackdrop;

    modalBackdrop = document.createElement('div');
    modalBackdrop.className = 'modal-backdrop';
    modalBackdrop.id = 'changePasswordBackdrop';
    modalBackdrop.innerHTML =
      '<div class="modal-card">' +
        '<h2>Change password</h2>' +
        '<div class="form-error" id="changePasswordError"></div>' +
        '<div class="modal-success" id="changePasswordSuccess">Password changed. Your other sessions have been logged out.</div>' +
        '<form id="changePasswordForm">' +
          '<label for="currentPassword">Current password</label>' +
          '<input type="password" id="currentPassword" autocomplete="current-password" required>' +
          '<label for="newPassword">New password</label>' +
          '<input type="password" id="newPassword" autocomplete="new-password" required minlength="8">' +
          '<label for="confirmPassword">Confirm new password</label>' +
          '<input type="password" id="confirmPassword" autocomplete="new-password" required minlength="8">' +
          '<div class="modal-actions">' +
            '<button type="button" class="btn-secondary" id="changePasswordCancel">Cancel</button>' +
            '<button type="submit" class="btn-primary" id="changePasswordSubmit">Save</button>' +
          '</div>' +
        '</form>' +
      '</div>';
    document.body.appendChild(modalBackdrop);

    var form = document.getElementById('changePasswordForm');
    var errorEl = document.getElementById('changePasswordError');
    var successEl = document.getElementById('changePasswordSuccess');
    var submitBtn = document.getElementById('changePasswordSubmit');

    function closeModal() {
      modalBackdrop.classList.remove('open');
      form.reset();
      errorEl.style.display = 'none';
      successEl.style.display = 'none';
      submitBtn.disabled = false;
      submitBtn.textContent = 'Save';
    }

    modalBackdrop.addEventListener('click', function (e) {
      if (e.target === modalBackdrop) closeModal();
    });
    document.getElementById('changePasswordCancel').addEventListener('click', closeModal);
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && modalBackdrop.classList.contains('open')) closeModal();
    });

    form.addEventListener('submit', function (e) {
      e.preventDefault();
      errorEl.style.display = 'none';
      successEl.style.display = 'none';

      var current = document.getElementById('currentPassword').value;
      var next = document.getElementById('newPassword').value;
      var confirm = document.getElementById('confirmPassword').value;

      if (next !== confirm) {
        errorEl.textContent = 'New password and confirmation do not match.';
        errorEl.style.display = 'block';
        return;
      }
      if (next.length < 8) {
        errorEl.textContent = 'New password must be at least 8 characters.';
        errorEl.style.display = 'block';
        return;
      }

      submitBtn.disabled = true;
      submitBtn.textContent = 'Saving…';

      fetch('/api/admin/change-password', {
        method: 'POST',
        credentials: 'same-origin',
        headers: Object.assign({ 'Content-Type': 'application/json' }, csrfHeaders()),
        body: JSON.stringify({ currentPassword: current, newPassword: next })
      })
        .then(function (res) {
          return res.json().catch(function () { return {}; }).then(function (body) {
            if (!res.ok) throw new Error(body.error || ('request failed (' + res.status + ')'));
            return body;
          });
        })
        .then(function () {
          form.reset();
          successEl.style.display = 'block';
          submitBtn.disabled = false;
          submitBtn.textContent = 'Save';
        })
        .catch(function (err) {
          errorEl.textContent = err.message || 'Failed to change password';
          errorEl.style.display = 'block';
          submitBtn.disabled = false;
          submitBtn.textContent = 'Save';
        });
    });

    return modalBackdrop;
  }

  function openChangePasswordModal() {
    ensureChangePasswordModal().classList.add('open');
    document.getElementById('currentPassword').focus();
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
          '<button class="account-menu-item" type="button" role="menuitem" id="accountChangePasswordBtn">Change password</button>' +
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
    document.getElementById('accountChangePasswordBtn').addEventListener('click', function () {
      setOpen(false);
      openChangePasswordModal();
    });
    document.getElementById('accountLogoutBtn').addEventListener('click', function () {
      setOpen(false);
      logout();
    });
  };
})();
