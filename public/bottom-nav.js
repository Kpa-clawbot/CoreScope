/* Issue #1061 — Bottom navigation for narrow viewports.
 *
 * Renders 5 primary tabs (Home, Packets, Live, Map, Channels) anchored to
 * the bottom on viewports ≤768px. Tabs are <a href="#/..."> so they reuse
 * the existing hashchange-driven router in app.js (no full reload, no
 * reimplementation of routing logic).
 *
 * Stable selectors for tests / future automation: every tab carries
 *   data-bottom-nav-tab="<route>"
 * The container is <nav data-bottom-nav> (also class="bottom-nav").
 *
 * Active-tab highlight is a class toggle ("active") set on hashchange.
 * Visual treatment lives in bottom-nav.css and respects
 * prefers-reduced-motion (transitions disabled).
 *
 * Top-nav suppression is handled in CSS (display:none at ≤768px). The
 * existing hamburger / nav-more dropdown behavior at ≤767px already
 * covered the long-tail routes (Tools, Lab, Perf, etc.); since the
 * top-nav is fully suppressed, those long-tail routes remain reachable
 * by direct URL (e.g. #/tools). A future follow-up may add a "More"
 * tab or a hamburger fallback (deferred per issue body).
 */
(function () {
  'use strict';

  if (typeof document === 'undefined') return;

  // 5 tabs in spec'd order. Each entry: { route, hash, label, icon }.
  // Labels are kept short to fit narrow widths; icons are emoji to avoid
  // shipping an icon font. Routes match the data-route values used by
  // the existing top-nav so any future router work stays consistent.
  var TABS = [
    { route: 'home',     hash: '#/home',     label: 'Home',     icon: '🏠' },
    { route: 'packets',  hash: '#/packets',  label: 'Packets',  icon: '📦' },
    { route: 'live',     hash: '#/live',     label: 'Live',     icon: '🔴' },
    { route: 'map',      hash: '#/map',      label: 'Map',      icon: '🗺️' },
    { route: 'channels', hash: '#/channels', label: 'Channels', icon: '💬' },
  ];

  function currentRoute() {
    // Mirror app.js navigate(): strip "#/" and any trailing "?…" / "/…".
    var h = (location.hash || '').replace(/^#\//, '');
    if (!h) return 'packets'; // app.js default
    var slash = h.indexOf('/');
    if (slash >= 0) h = h.substring(0, slash);
    var q = h.indexOf('?');
    if (q >= 0) h = h.substring(0, q);
    return h || 'packets';
  }

  function build() {
    if (document.querySelector('[data-bottom-nav]')) return;

    var nav = document.createElement('nav');
    nav.className = 'bottom-nav';
    nav.setAttribute('data-bottom-nav', '');
    nav.setAttribute('role', 'navigation');
    nav.setAttribute('aria-label', 'Bottom navigation');

    TABS.forEach(function (t) {
      var a = document.createElement('a');
      a.className = 'bottom-nav-tab';
      a.setAttribute('data-bottom-nav-tab', t.route);
      a.setAttribute('data-route', t.route);
      a.setAttribute('href', t.hash);
      a.setAttribute('aria-label', t.label);

      var ic = document.createElement('span');
      ic.className = 'bottom-nav-icon';
      ic.setAttribute('aria-hidden', 'true');
      ic.textContent = t.icon;

      var lb = document.createElement('span');
      lb.className = 'bottom-nav-label';
      lb.textContent = t.label;

      a.appendChild(ic);
      a.appendChild(lb);
      nav.appendChild(a);
    });

    // Insert after <main> so it's a sibling at the body level — keeps
    // it out of the <main> scroll container. The CSS pins it bottom:0
    // via position:fixed so DOM order beyond "after the nav" doesn't
    // matter for layout, but document order matters for screen readers.
    var main = document.getElementById('app') || document.querySelector('main');
    if (main && main.parentNode) {
      main.parentNode.insertBefore(nav, main.nextSibling);
    } else {
      document.body.appendChild(nav);
    }
  }

  function syncActive() {
    var route = currentRoute();
    var tabs = document.querySelectorAll('[data-bottom-nav-tab]');
    for (var i = 0; i < tabs.length; i++) {
      var t = tabs[i];
      if (t.getAttribute('data-bottom-nav-tab') === route) {
        t.classList.add('active');
        t.setAttribute('aria-current', 'page');
      } else {
        t.classList.remove('active');
        t.removeAttribute('aria-current');
      }
    }
  }

  function init() {
    build();
    syncActive();
    window.addEventListener('hashchange', syncActive);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
