// Shared light/dark toggle for the admin panel — mirrors the main app's
// handling in app.js (minus the user-theme-customization overlay, which
// doesn't apply here). Reads/writes the same 'meshcore-theme' localStorage
// key so the admin panel and main app always agree.
(function () {
  'use strict';

  var checkbox = document.getElementById('darkModeCheckbox');
  if (!checkbox) return;

  function applyTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
    checkbox.checked = theme === 'dark';
    localStorage.setItem('meshcore-theme', theme);
  }

  checkbox.addEventListener('change', function () {
    applyTheme(checkbox.checked ? 'dark' : 'light');
  });

  window.addEventListener('storage', function (ev) {
    if (!ev || ev.key !== 'meshcore-theme' || !ev.newValue) return;
    if (ev.newValue !== 'dark' && ev.newValue !== 'light') return;
    document.documentElement.setAttribute('data-theme', ev.newValue);
    checkbox.checked = ev.newValue === 'dark';
  });

  // Reflect whatever the pre-paint <head> script already applied.
  checkbox.checked = document.documentElement.getAttribute('data-theme') === 'dark';
})();
