/* public/touch-gestures.js — Gesture system for #1062.
 *
 * Three gestures for narrow viewports (≤768px):
 *   1. Swipe-LEFT on a packets/nodes/observers row → reveal row-action overlay.
 *   2. Horizontal swipe on the bottom-nav strip → advance tabs in TAB order.
 *   3. Swipe-DOWN on a slide-over panel → close it.
 *
 * Hard rules (per #1062 brief):
 *   - Pointer Events ONLY (no touchstart/touchend mixing). setPointerCapture.
 *   - Axis-lock: commit to one axis in the first 8–12px; vertical scroll never
 *     blocked unless we explicitly committed to a horizontal swipe.
 *   - Leaflet exclusion: bail if e.target.closest('.leaflet-container').
 *   - Threshold: row-action triggers only at 24% of row width OR 80px swiped.
 *   - touch-action: body { touch-action: pan-y } so browser owns vertical
 *     scroll natively. [data-bottom-nav] gets touch-action: none.
 *   - Singleton + cleanup: module-scoped guard, document-level listeners
 *     registered ONCE (mirrors the #1180 MQL leak fix class).
 *   - prefers-reduced-motion: animations disabled (CSS handles this), gesture
 *     still works.
 */
(function () {
  'use strict';

  if (typeof window === 'undefined' || typeof document === 'undefined') return;

  // ── Singleton guard (matches #1180 pattern) ──
  if (typeof window.__touchGestures1062InitCount !== 'number') {
    window.__touchGestures1062InitCount = 0;
  }
  if (window.__touchGestures1062InitCount > 0) {
    // Already initialized — never re-register document listeners.
    return;
  }
  window.__touchGestures1062InitCount += 1;

  // ── Tunables ──
  var AXIS_LOCK_DISTANCE = 10;     // px before we commit to an axis (8–12 range)
  var ROW_ACTION_PX = 80;          // absolute px threshold
  var ROW_ACTION_PCT = 0.24;       // OR 24% of row width
  var SLIDE_OVER_DISMISS_PX = 100; // downward swipe to dismiss slide-over
  var TAB_SWIPE_PX = 60;           // horizontal swipe on bottom-nav strip
  var NARROW_BP = 768;             // gestures only matter on phones

  // ── Module state ──
  var pointerActive = false;
  var pointerId = null;
  var startX = 0, startY = 0;
  var lastX = 0, lastY = 0;
  var axis = null;          // 'h' | 'v' | null
  var startTarget = null;
  var gestureContext = null; // 'row' | 'bottom-nav' | 'slide-over' | null
  var activeRow = null;
  var rowOverlay = null;
  var capturedEl = null;
  // PR #1185 mesh-op review: scroll-discriminator for slide-over.
  // Captured at pointerdown when the slide-over context is selected; if the
  // panel content is mid-scroll (scrollTop > 0) at gesture start, the gesture
  // is a normal scroll, NOT a dismiss — we must not close the panel.
  var slideOverScroller = null;
  var slideOverStartScrollTop = 0;

  function isNarrow() {
    return window.innerWidth <= NARROW_BP;
  }

  function inLeaflet(target) {
    return !!(target && target.closest && target.closest('.leaflet-container'));
  }

  function findRow(target) {
    if (!target || !target.closest) return null;
    // Packets/nodes/observers tables — generic: any tr inside a tbody whose
    // table is inside one of the relevant pages.
    var tr = target.closest('tr[data-hash], tr[data-id]');
    if (!tr) return null;
    var tbody = tr.closest('tbody');
    if (!tbody) return null;
    // Restrict to the three target tables. id="pktBody" for packets,
    // and we treat any tbody inside .nodes-table / .observers-table as eligible.
    if (tbody.id === 'pktBody') return tr;
    var table = tbody.closest('table');
    if (table && (table.id === 'nodesTable' || table.id === 'observersTable' ||
                  table.classList.contains('nodes-table') ||
                  table.classList.contains('observers-table'))) {
      return tr;
    }
    return tr; // permissive — still skip leaflet via inLeaflet().
  }

  function findBottomNav(target) {
    if (!target || !target.closest) return null;
    return target.closest('[data-bottom-nav]');
  }

  function findSlideOver(target) {
    if (!target || !target.closest) return null;
    return target.closest('.slide-over-panel');
  }

  // Locate the open slide-over panel by querying the DOM (not via target
  // ancestry). Used as a fallback when the pointerdown's hit-test target
  // is something outside the panel subtree (e.g. a focused button whose
  // event was retargeted, or a panel mid-animation where elementFromPoint
  // returned an unrelated element). Pairs the lookup with a coordinate
  // check so we don't claim slide-over context for taps elsewhere.
  function findOpenSlideOverAt(x, y) {
    if (!window.SlideOver || typeof window.SlideOver.isOpen !== 'function') return null;
    if (!window.SlideOver.isOpen()) return null;
    var panel = document.querySelector('.slide-over-panel');
    if (!panel || panel.hidden) return null;
    var r = panel.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return null;
    if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) return panel;
    return null;
  }

  // ── Bottom-nav: read TAB order from bottom-nav.js ──
  // The TAB list there is module-private; we re-derive order from the rendered
  // DOM (which IS the source of truth for what the user sees) — primary tabs only,
  // i.e. excluding "more".
  function getNavTabsInOrder() {
    var nodes = document.querySelectorAll('[data-bottom-nav] [data-bottom-nav-tab]');
    var out = [];
    for (var i = 0; i < nodes.length; i++) {
      var r = nodes[i].getAttribute('data-bottom-nav-tab');
      if (r && r !== 'more') out.push(r);
    }
    return out;
  }

  function currentRouteShort() {
    var h = (location.hash || '').replace(/^#\//, '');
    if (!h) return 'packets';
    var slash = h.indexOf('/');
    if (slash >= 0) h = h.substring(0, slash);
    var q = h.indexOf('?');
    if (q >= 0) h = h.substring(0, q);
    return h || 'packets';
  }

  function navigateRelative(delta) {
    var tabs = getNavTabsInOrder();
    if (!tabs.length) return;
    var cur = currentRouteShort();
    var idx = tabs.indexOf(cur);
    if (idx < 0) return; // current route isn't a primary tab
    var next = idx + delta;
    if (next < 0 || next >= tabs.length) return;
    location.hash = '#/' + tabs[next];
  }

  // ── Row-action overlay ──
  function ensureRowOverlay(row) {
    if (rowOverlay && rowOverlay.parentNode) return rowOverlay;
    var o = document.createElement('div');
    o.className = 'row-action-overlay';
    o.setAttribute('role', 'group');
    o.setAttribute('aria-label', 'Row actions');
    var hash = row.getAttribute('data-hash') || row.getAttribute('data-id') || '';
    o.innerHTML =
      '<button type="button" class="row-action-btn" data-row-action="trace">Trace</button>' +
      '<button type="button" class="row-action-btn" data-row-action="filter">Filter</button>' +
      '<button type="button" class="row-action-btn" data-row-action="copy" data-hash="' +
      String(hash).replace(/"/g, '&quot;') + '">Copy hash</button>';
    document.body.appendChild(o);
    rowOverlay = o;
    return o;
  }

  function showRowOverlay(row) {
    var o = ensureRowOverlay(row);
    var rect = row.getBoundingClientRect();
    o.style.position = 'fixed';
    o.style.top = rect.top + 'px';
    o.style.left = (rect.right - 240) + 'px';
    o.style.height = rect.height + 'px';
    o.style.width = '240px';
    o.classList.add('row-action-overlay-open');
    o.hidden = false;
  }

  function dismissRowAction() {
    if (rowOverlay) {
      rowOverlay.classList.remove('row-action-overlay-open');
      // Remove from DOM after animation; CSS handles instant under reduce.
      var el = rowOverlay;
      rowOverlay = null;
      try {
        if (el.parentNode) el.parentNode.removeChild(el);
      } catch (_) {}
    }
    if (activeRow) {
      activeRow.style.transform = '';
      activeRow.classList.remove('row-swiping');
      activeRow = null;
    }
  }

  // ── Pointer handlers ──
  function onPointerDown(e) {
    if (e.pointerType !== 'touch') return;
    if (pointerActive) return;
    var t = e.target;
    if (inLeaflet(t)) return;
    if (!isNarrow()) return;

    var row = findRow(t);
    var nav = findBottomNav(t);
    var so = findSlideOver(t) || findOpenSlideOverAt(e.clientX, e.clientY);

    if (so) gestureContext = 'slide-over';
    else if (nav) gestureContext = 'bottom-nav';
    else if (row) gestureContext = 'row';
    else gestureContext = null;

    if (!gestureContext) return;

    pointerActive = true;
    pointerId = e.pointerId;
    startX = lastX = e.clientX;
    startY = lastY = e.clientY;
    axis = null;
    startTarget = t;
    activeRow = (gestureContext === 'row') ? row : null;

    // Slide-over scroll-discriminator (PR #1185): record where the user is
    // reading from. The slide-over panel itself is the scroller (CSS sets
    // `.slide-over-panel { overflow-y: auto; }`) — `.slide-over-content` is a
    // flex child without its own overflow-y, so its scrollTop is always 0.
    // To be robust against markup/CSS drift, walk every candidate (panel +
    // any inner `.slide-over-content`) and take the MAX scrollTop. Whichever
    // element actually scrolls becomes the discriminator source — this
    // guarantees production reads from the same element a test (or a future
    // refactor) writes to.
    if (gestureContext === 'slide-over') {
      var candidates = [];
      if (so) candidates.push(so);
      var inner = so && so.querySelector && so.querySelector('.slide-over-content');
      if (inner) candidates.push(inner);
      slideOverScroller = so || null;
      slideOverStartScrollTop = 0;
      for (var i = 0; i < candidates.length; i++) {
        var st = (candidates[i] && typeof candidates[i].scrollTop === 'number')
          ? candidates[i].scrollTop : 0;
        if (st > slideOverStartScrollTop) {
          slideOverStartScrollTop = st;
          slideOverScroller = candidates[i];
        }
      }
    } else {
      slideOverScroller = null;
      slideOverStartScrollTop = 0;
    }

    // Capture so subsequent move events flow to us regardless of element.
    try {
      var capTarget = (gestureContext === 'bottom-nav') ? nav :
                       (gestureContext === 'slide-over') ? so :
                       row || t;
      if (capTarget && typeof capTarget.setPointerCapture === 'function') {
        capTarget.setPointerCapture(pointerId);
        capturedEl = capTarget;
      }
    } catch (_) { capturedEl = null; }
  }

  function onPointerMove(e) {
    if (!pointerActive || e.pointerId !== pointerId) return;
    var dx = e.clientX - startX;
    var dy = e.clientY - startY;
    lastX = e.clientX;
    lastY = e.clientY;

    if (axis === null) {
      var adx = Math.abs(dx), ady = Math.abs(dy);
      if (adx < AXIS_LOCK_DISTANCE && ady < AXIS_LOCK_DISTANCE) return;
      // For slide-over, dismiss on vertical down swipe; commit accordingly.
      if (gestureContext === 'slide-over') {
        axis = (ady > adx) ? 'v' : 'h';
        if (axis !== 'v') {
          // Horizontal on slide-over — release, do nothing.
          releasePointer();
          return;
        }
        // Scroll-discriminator (PR #1185): if user started mid-scroll, this
        // gesture belongs to the browser's native scroll. Release immediately
        // so we never preventDefault / drag the panel / dismiss.
        if (slideOverStartScrollTop > 0) {
          releasePointer();
          return;
        }
      } else if (gestureContext === 'bottom-nav') {
        axis = (adx > ady) ? 'h' : 'v';
        if (axis !== 'h') { releasePointer(); return; }
      } else if (gestureContext === 'row') {
        axis = (adx > ady) ? 'h' : 'v';
        if (axis !== 'h') {
          // Vertical → release; let browser handle scroll.
          releasePointer();
          return;
        }
      }
    }

    // Apply visual feedback only after axis commit.
    if (gestureContext === 'row' && axis === 'h' && activeRow) {
      // Only show the peek for left-swipes (reveal action panel on right side).
      if (dx < 0) {
        activeRow.classList.add('row-swiping');
        activeRow.style.transform = 'translateX(' + Math.max(dx, -240) + 'px)';
      } else {
        activeRow.style.transform = '';
      }
      // Prevent default so the browser doesn't start a text-selection drag.
      if (e.cancelable) { try { e.preventDefault(); } catch (_) {} }
    } else if (gestureContext === 'bottom-nav' && axis === 'h') {
      if (e.cancelable) { try { e.preventDefault(); } catch (_) {} }
    } else if (gestureContext === 'slide-over' && axis === 'v') {
      if (dy > 0) {
        // Drag panel down with the finger.
        var so = findSlideOver(startTarget) || document.querySelector('.slide-over-panel');
        if (so) {
          so.style.transform = 'translateY(' + dy + 'px)';
        }
      }
      if (e.cancelable) { try { e.preventDefault(); } catch (_) {} }
    }
  }

  function onPointerUp(e) {
    if (!pointerActive || e.pointerId !== pointerId) return;
    var dx = e.clientX - startX;
    var dy = e.clientY - startY;

    try {
      if (gestureContext === 'row' && axis === 'h' && activeRow) {
        var rowRect = activeRow.getBoundingClientRect();
        var threshold = Math.min(ROW_ACTION_PX, rowRect.width * ROW_ACTION_PCT);
        if (dx < 0 && Math.abs(dx) >= threshold) {
          // Commit — show overlay, snap row back.
          activeRow.style.transform = '';
          activeRow.classList.remove('row-swiping');
          showRowOverlay(activeRow);
          activeRow = null; // overlay owns lifecycle now
        } else {
          // Snap back.
          activeRow.style.transform = '';
          activeRow.classList.remove('row-swiping');
          activeRow = null;
        }
      } else if (gestureContext === 'bottom-nav' && axis === 'h') {
        if (dx <= -TAB_SWIPE_PX) {
          // Drag content leftward → next tab.
          navigateRelative(+1);
        } else if (dx >= TAB_SWIPE_PX) {
          navigateRelative(-1);
        }
      } else if (gestureContext === 'slide-over' && axis === 'v') {
        var so = findSlideOver(startTarget) || document.querySelector('.slide-over-panel');
        if (so) so.style.transform = '';
        // Scroll-discriminator (PR #1185): if the user started mid-scroll,
        // never dismiss — onPointerMove should already have released, this
        // is a defense-in-depth guard.
        if (slideOverStartScrollTop > 0) {
          // no-op
        } else if (dy >= SLIDE_OVER_DISMISS_PX && window.SlideOver && typeof window.SlideOver.close === 'function') {
          try { window.SlideOver.close(); } catch (_) {}
        }
      }
    } finally {
      releasePointer();
    }
  }

  function onPointerCancel(e) {
    if (!pointerActive || e.pointerId !== pointerId) return;
    if (activeRow) {
      activeRow.style.transform = '';
      activeRow.classList.remove('row-swiping');
      activeRow = null;
    }
    var so = findSlideOver(startTarget) || document.querySelector('.slide-over-panel');
    if (so) so.style.transform = '';
    releasePointer();
  }

  // Browser may steal pointer capture (e.g. orientation change, parent
  // scroll start, focus change). When that happens neither pointerup nor
  // pointercancel are guaranteed — we'd leak state and visuals. Treat
  // lost-capture identically to cancel.
  function onPointerLostCapture(e) {
    if (!pointerActive || e.pointerId !== pointerId) return;
    if (activeRow) {
      activeRow.style.transform = '';
      activeRow.classList.remove('row-swiping');
      activeRow = null;
    }
    var so = findSlideOver(startTarget) || document.querySelector('.slide-over-panel');
    if (so) so.style.transform = '';
    releasePointer();
  }

  function releasePointer() {
    try {
      if (capturedEl && pointerId != null && typeof capturedEl.releasePointerCapture === 'function') {
        capturedEl.releasePointerCapture(pointerId);
      }
    } catch (_) {}
    pointerActive = false;
    pointerId = null;
    axis = null;
    startTarget = null;
    capturedEl = null;
    gestureContext = null;
    slideOverScroller = null;
    slideOverStartScrollTop = 0;
  }

  // ── Row-overlay click delegation ──
  function onClickAction(e) {
    var btn = e.target && e.target.closest && e.target.closest('.row-action-btn');
    if (!btn) {
      // Click outside overlay dismisses it.
      if (rowOverlay && !(e.target.closest && e.target.closest('.row-action-overlay'))) {
        dismissRowAction();
      }
      return;
    }
    var action = btn.getAttribute('data-row-action');
    var hash = btn.getAttribute('data-hash') || '';
    if (action === 'copy' && hash && navigator.clipboard) {
      try { navigator.clipboard.writeText(hash); } catch (_) {}
    } else if (action === 'filter' && hash) {
      location.hash = '#/packets?hash=' + encodeURIComponent(hash);
    } else if (action === 'trace' && hash) {
      location.hash = '#/packets/' + encodeURIComponent(hash);
    }
    dismissRowAction();
  }

  // ── Register listeners ONCE at document level ──
  // passive:false on move/up so we can preventDefault when we own the axis.
  document.addEventListener('pointerdown', onPointerDown, { passive: true });
  document.addEventListener('pointermove', onPointerMove, { passive: false });
  document.addEventListener('pointerup', onPointerUp, { passive: true });
  document.addEventListener('pointercancel', onPointerCancel, { passive: true });
  document.addEventListener('lostpointercapture', onPointerLostCapture, { passive: true });
  document.addEventListener('click', onClickAction, true);

  // Public API used by tests / future callers.
  window.TouchGestures = {
    dismissRowAction: dismissRowAction,
    _navigateRelative: navigateRelative,
  };
})();
