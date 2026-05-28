/* #1461 mobile page-actions: mirror per-page header buttons (pause, filter
 * toggle) into the top nav's .nav-right area on mobile so the page-header
 * row can be hidden entirely. On desktop this script is a no-op. */
(function () {
  'use strict';
  const MOBILE_BP = 900;
  const SLOT_ID = 'navPageActions';

  function isMobile() { return window.innerWidth <= MOBILE_BP; }

  function ensureSlot() {
    let slot = document.getElementById(SLOT_ID);
    if (slot) return slot;
    // On mobile, .nav-right is display:none — use .nav-left so the slot is
    // visible. Append after the brand link.
    const navLeft = document.querySelector('.nav-left');
    if (!navLeft) return null;
    slot = document.createElement('div');
    slot.id = SLOT_ID;
    slot.style.cssText = 'display:inline-flex;gap:4px;align-items:center;margin-left:8px;';
    navLeft.appendChild(slot);
    return slot;
  }

  function clearSlot() {
    const slot = document.getElementById(SLOT_ID);
    if (slot) slot.innerHTML = '';
  }

  function makeBtn(label, title, onClick) {
    const b = document.createElement('button');
    b.className = 'nav-btn';
    b.type = 'button';
    b.title = title;
    b.textContent = label;
    b.addEventListener('click', onClick);
    return b;
  }

  function syncForRoute() {
    // #1461 #6: close mobile detail sheet on route change away from packets
    try {
      const sheet = document.getElementById('mobileDetailSheet');
      if (sheet && !/^#\/packets/.test(location.hash || '')) {
        sheet.classList.remove('open');
      }
    } catch (_e) {}

    if (!isMobile()) { clearSlot(); return; }
    const hash = location.hash || '';
    const slot = ensureSlot();
    if (!slot) return;
    slot.innerHTML = '';

    if (/^#\/packets(\/|$|\?)/.test(hash)) {
      // Mirror pause button (icon only — small)
      const pause = makeBtn('⏸', 'Pause live updates', function () {
        const real = document.getElementById('pktPauseBtn');
        if (real) real.click();
      });
      pause.style.cssText = 'padding:4px 6px;font-size:14px;background:transparent;border:1px solid var(--border,#444);border-radius:6px;color:var(--nav-text,#fff);cursor:pointer;';
      slot.appendChild(pause);
      // Mirror filter toggle as a labeled "Filters ▾" pill (matches inline style)
      const filt = makeBtn('Filters ▾', 'Toggle filters', function () {
        const real = document.querySelector('.filter-bar .filter-toggle-btn, #filterToggleBtn');
        if (real) real.click();
      });
      filt.className = 'nav-btn filter-toggle-btn-mirror';
      filt.style.cssText = 'padding:4px 10px;font-size:12px;background:transparent;border:1px solid var(--border,#444);border-radius:999px;color:var(--nav-text,#fff);cursor:pointer;font-weight:500;';
      slot.appendChild(filt);
    }
  }

  window.addEventListener('hashchange', syncForRoute);
  window.addEventListener('resize', syncForRoute);

  /* #1461 #7: on mobile, packets-list group-header expand is a UX dead-end
   * (we hid the chevron so there's no way to collapse). Intercept those
   * clicks and force them to the single-select code path instead — the
   * detail pane has all the obs info anyway. */
  document.addEventListener('click', function (e) {
    if (!isMobile()) return;
    const row = e.target.closest && e.target.closest('#pktTable tr[data-action="toggle-select"]');
    if (!row) return;
    // Convert to a select-hash event by re-dispatching synthetically — simpler
    // to mutate the attribute briefly so the existing delegated handler
    // routes it correctly.
    row.setAttribute('data-action', 'select-hash');
  }, true);

  /* #1461 #8: traffic_share_score / bridge_score tooltips use title= which
   * doesn't fire on touch. Show a click-to-toast popover on mobile when
   * operator taps a TD whose title mentions traffic/bridge/centrality. */
  document.addEventListener('click', function (e) {
    if (!isMobile()) return;
    const el = e.target.closest('[title]');
    if (!el) return;
    const text = el.getAttribute('title');
    if (!text) return;
    // Limit to score / metric explanations to avoid spamming on every titled element
    if (!/traffic share|bridge|centrality|score|usefulness/i.test(text + ' ' + el.textContent)) return;
    e.preventDefault();
    e.stopPropagation();
    showToast(text);
  }, true);
  function showToast(msg) {
    let t = document.getElementById('mcMobileToast');
    if (!t) {
      t = document.createElement('div');
      t.id = 'mcMobileToast';
      t.style.cssText = 'position:fixed;left:50%;bottom:90px;transform:translateX(-50%);background:var(--surface-2,#222);color:var(--text,#fff);padding:10px 16px;border:1px solid var(--border,#444);border-radius:8px;font-size:13px;max-width:80vw;z-index:9999;box-shadow:0 4px 12px rgba(0,0,0,0.4);transition:opacity .3s;';
      document.body.appendChild(t);
    }
    t.textContent = msg;
    t.style.opacity = '1';
    clearTimeout(t._timer);
    t._timer = setTimeout(() => { t.style.opacity = '0'; }, 4000);
  }

  /* #1467: mirror missing top-nav controls (Favorites, Search, Customize)
   * into the bottom-nav More sheet. bottom-nav.js only wired Dark mode;
   * the others have no mobile surface today. Insert above the existing
   * dark-mode separator so the new items group with the other route items. */
  function addMissingMoreSheetItems() {
    const sheet = document.querySelector('[data-bottom-nav-sheet]');
    if (!sheet) { setTimeout(addMissingMoreSheetItems, 500); return; }
    if (sheet.querySelector('[data-mpa-mirror]')) return;  // already injected

    const mirrors = [
      { id: 'favToggle',       icon: '⭐', label: 'Favorites' },
      { id: 'searchToggle',    icon: '🔍', label: 'Search' },
      { id: 'customizeToggle', icon: '🎨', label: 'Customize' },
    ];

    const sep = sheet.querySelector('.bottom-nav-sheet-sep');
    mirrors.forEach((m) => {
      const real = document.getElementById(m.id);
      if (!real) return;
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'bottom-nav-sheet-item';
      btn.setAttribute('role', 'menuitem');
      btn.setAttribute('data-mpa-mirror', m.id);

      const ic = document.createElement('span');
      ic.className = 'bottom-nav-sheet-icon';
      ic.setAttribute('aria-hidden', 'true');
      ic.textContent = m.icon;

      const lb = document.createElement('span');
      lb.className = 'bottom-nav-sheet-label';
      lb.textContent = m.label;

      btn.appendChild(ic);
      btn.appendChild(lb);
      btn.addEventListener('click', function () {
        real.click();
        // close the sheet after delegating
        try { sheet.classList.remove('open'); } catch (_e) {}
      });

      if (sep) sheet.insertBefore(btn, sep);
      else sheet.appendChild(btn);
    });
  }
  // Also re-run when sheet is opened (bottom-nav rebuilds it on open)
  document.addEventListener('click', function (e) {
    const target = e.target.closest && e.target.closest('[data-bottom-nav-more]');
    if (target) setTimeout(addMissingMoreSheetItems, 50);
  }, true);

  // Run after page-header is rendered (packets.js builds it async); retry briefly
  let tries = 0;
  function init() {
    syncForRoute();
    addMissingMoreSheetItems();
    if (tries++ < 20 && /^#\/packets/.test(location.hash) && !document.getElementById('pktPauseBtn')) {
      setTimeout(init, 250);
    }
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
