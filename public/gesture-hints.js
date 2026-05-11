/* gesture-hints.js — Issue #1065
 * First-visit gesture discoverability hints.
 *
 * - localStorage namespace: meshcore-gesture-hints-<hint>
 *   keys: row-swipe, tab-swipe, edge-drawer, pull-refresh
 *   value: "seen"
 * - Show hint 800ms after page settle; auto-fade 8s; "Got it" dismisses.
 * - aria-live=polite, role=status, no focus stealing, pointer-events:none.
 * - prefers-reduced-motion: animation-name: none (style.css handles via media query).
 * - Singleton + cleanup: module-scoped guard; SPA re-mount must not re-show dismissed.
 * - Pull-to-refresh hint only when .pull-to-reconnect element exists in DOM.
 * - Edge-drawer hint only at viewport > 768px (where edge-swipe drawer applies).
 * - Row-swipe hint only on table pages: /#/packets, /#/nodes, etc.
 */
(function () {
  'use strict';
  if (window.__gestureHints1065Init) {
    window.__gestureHints1065Init++;
    return;
  }
  window.__gestureHints1065Init = 1;

  var NS = 'meshcore-gesture-hints-';
  var HINTS = {
    'row-swipe': {
      key: NS + 'row-swipe',
      text: 'Tip: swipe a row left for quick actions.',
      relevant: function () {
        var h = location.hash || '';
        return /^#\/(packets|nodes|live)/.test(h);
      },
      position: 'bottom',
    },
    'tab-swipe': {
      key: NS + 'tab-swipe',
      text: 'Tip: swipe left or right to switch tabs.',
      relevant: function () {
        return !!document.querySelector('[data-bottom-nav]');
      },
      position: 'bottom',
    },
    'edge-drawer': {
      key: NS + 'edge-drawer',
      text: 'Tip: swipe in from the left edge to open navigation.',
      relevant: function () {
        return window.innerWidth > 768 && !!document.querySelector('.nav-drawer, [data-nav-drawer]');
      },
      position: 'top-left',
    },
    'pull-refresh': {
      key: NS + 'pull-refresh',
      text: 'Tip: pull down to refresh the connection.',
      relevant: function () {
        return !!document.querySelector('.pull-to-reconnect');
      },
      position: 'top',
    },
  };

  var SHOW_DELAY_MS = 800;
  var AUTO_FADE_MS = 8000;

  var _shown = Object.create(null); // hint id → element (currently rendered)
  var _scheduledTimer = null;
  var _routeChangeBound = false;

  function isSeen(id) {
    try { return localStorage.getItem(HINTS[id].key) === 'seen'; }
    catch (_e) { return false; }
  }
  function markSeen(id) {
    try { localStorage.setItem(HINTS[id].key, 'seen'); } catch (_e) {}
  }
  function clearAll() {
    try {
      Object.keys(HINTS).forEach(function (id) { localStorage.removeItem(HINTS[id].key); });
    } catch (_e) {}
  }

  function buildHintEl(id) {
    var def = HINTS[id];
    var wrap = document.createElement('div');
    wrap.className = 'gesture-hint gesture-hint-' + def.position;
    // Belt-and-suspenders: inline style guarantees pointer-events:none
    // regardless of CSS load order or cascade collisions. The hint must
    // never capture clicks; only the inner button does (via .gesture-hint-inner).
    wrap.style.pointerEvents = 'none';
    wrap.setAttribute('data-gesture-hint', id);
    wrap.setAttribute('role', 'status');
    wrap.setAttribute('aria-live', 'polite');
    wrap.setAttribute('aria-atomic', 'true');

    var inner = document.createElement('div');
    inner.className = 'gesture-hint-inner';

    var msg = document.createElement('span');
    msg.className = 'gesture-hint-text';
    msg.textContent = def.text;
    inner.appendChild(msg);

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'gesture-hint-dismiss';
    btn.setAttribute('data-gesture-hint-dismiss', '');
    btn.setAttribute('aria-label', 'Dismiss hint');
    btn.textContent = 'Got it';
    btn.addEventListener('click', function (e) {
      e.preventDefault();
      e.stopPropagation();
      dismiss(id);
    });
    inner.appendChild(btn);

    wrap.appendChild(inner);
    return wrap;
  }

  function show(id) {
    if (_shown[id]) return;
    if (isSeen(id)) return;
    var def = HINTS[id];
    if (!def || !def.relevant()) return;

    var el = buildHintEl(id);
    document.body.appendChild(el);
    _shown[id] = el;

    // Auto-fade after AUTO_FADE_MS — does NOT mark seen; user must explicitly dismiss
    // (per AC: "Got it" button clears the flag).
    var fadeTimer = setTimeout(function () {
      if (_shown[id] === el) {
        el.classList.add('gesture-hint-fading');
        setTimeout(function () {
          if (el.parentNode) el.parentNode.removeChild(el);
          if (_shown[id] === el) delete _shown[id];
        }, 350);
      }
    }, AUTO_FADE_MS);
    el._gestureHintFadeTimer = fadeTimer;
  }

  function dismiss(id) {
    var el = _shown[id];
    markSeen(id);
    if (el) {
      if (el._gestureHintFadeTimer) clearTimeout(el._gestureHintFadeTimer);
      if (el.parentNode) el.parentNode.removeChild(el);
      delete _shown[id];
    }
  }

  function scheduleHints() {
    if (_scheduledTimer) clearTimeout(_scheduledTimer);
    _scheduledTimer = setTimeout(function () {
      _scheduledTimer = null;
      Object.keys(HINTS).forEach(function (id) {
        if (!isSeen(id)) show(id);
      });
    }, SHOW_DELAY_MS);
  }

  function onRouteChange() {
    // Remove hints that are no longer relevant for the new route.
    Object.keys(_shown).slice().forEach(function (id) {
      var def = HINTS[id];
      if (!def || !def.relevant()) {
        var el = _shown[id];
        if (el && el._gestureHintFadeTimer) clearTimeout(el._gestureHintFadeTimer);
        if (el && el.parentNode) el.parentNode.removeChild(el);
        delete _shown[id];
      }
    });
    // Re-evaluate: show any not-yet-seen relevant hints.
    scheduleHints();
  }

  function init() {
    if (!_routeChangeBound) {
      _routeChangeBound = true;
      window.addEventListener('hashchange', onRouteChange);
    }
    scheduleHints();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init, { once: true });
  } else {
    init();
  }

  window.GestureHints = {
    show: show,
    dismiss: dismiss,
    reset: function () {
      clearAll();
      // Remove any visible.
      Object.keys(_shown).slice().forEach(function (id) {
        var el = _shown[id];
        if (el && el._gestureHintFadeTimer) clearTimeout(el._gestureHintFadeTimer);
        if (el && el.parentNode) el.parentNode.removeChild(el);
        delete _shown[id];
      });
    },
    _keys: function () {
      return Object.keys(HINTS).map(function (id) { return HINTS[id].key; });
    },
  };
})();
