/*
 * warmup-banner.js — global top-banner / status pill surfacing server warm-up state.
 *
 * Issue: #1660. FE-only sub-deliverables (1)+(3); per-card pill (2) is deferred
 * pending the recomputer.first_pass_done server flag (#1659).
 *
 * Consumes:
 *   - X-Corescope-Load-Status response header ("loading" | "ready"), set by
 *     cmd/server/chunked_load.go on every response.
 *   - GET /api/healthz, polled every 30s while not in steady-state.
 *
 * Renders a role="status" live region so screen readers announce transitions.
 * Auto-dismisses (fades out) once ready=true AND from_pubkey_backfill.done=true.
 */
(function () {
  'use strict';

  var STALE_INGEST_MS = 5 * 60 * 1000; // 5 min — matches acceptance criteria
  var POLL_INTERVAL_MS = 30 * 1000;    // 30s while warming up

  // -------- Pure helpers (testable in isolation) ----------------------------

  function fmtNum(n) {
    try { return Number(n).toLocaleString('en-US'); }
    catch (e) { return String(n); }
  }

  /**
   * Build ordered list of human-readable warm-up messages from current state.
   *
   * @param {object|null} healthz   - parsed /api/healthz body, or null when unknown
   * @param {string|null} loadStatus - last seen X-Corescope-Load-Status header value
   * @param {number} nowMs           - current time in ms (injectable for tests)
   * @returns {string[]} ordered list of messages; empty when steady-state.
   */
  function getWarmupMessages(healthz, loadStatus, nowMs) {
    var msgs = [];
    var h = healthz || {};
    var backfill = h.from_pubkey_backfill || null;

    var loading = loadStatus === 'loading' || h.ready === false;
    if (loading) {
      msgs.push('\u23F3 Loading historical data \u2014 counts may be incomplete.');
    }

    if (backfill && backfill.done === false) {
      var processed = Number(backfill.processed) || 0;
      var total = Number(backfill.total) || 0;
      // Clamp pct: total=0 → 0%; processed>total (race) → 100%.
      // Never NaN, never >100%.
      var rawPct = total > 0 ? Math.floor((processed / total) * 100) : 0;
      var pct = Math.max(0, Math.min(100, rawPct));
      msgs.push('Backfilling pubkey index: ' + fmtNum(processed) +
        ' / ' + fmtNum(total) + ' (' + pct + '%)');
    }

    var liveness = h.ingest_liveness || {};
    var srcs = Object.keys(liveness).sort();
    for (var i = 0; i < srcs.length; i++) {
      var src = srcs[i];
      var info = liveness[src] || {};
      var lastUnix = Number(info.lastReceiptUnix);
      if (!lastUnix || !isFinite(lastUnix)) continue;
      var ageMs = nowMs - lastUnix * 1000;
      if (ageMs >= STALE_INGEST_MS) {
        var ageMin = Math.floor(ageMs / 60000);
        msgs.push('No packets from ' + src + ' in ' + ageMin + ' min.');
      }
    }

    return msgs;
  }

  function shouldShowBanner(healthz, loadStatus, nowMs) {
    return getWarmupMessages(healthz, loadStatus, nowMs).length > 0;
  }

  /**
   * Steady-state predicate: ready=true AND from_pubkey_backfill.done=true.
   * Once true, banner is dismissed and polling is torn down.
   */
  function isSteadyState(healthz) {
    if (!healthz) return false;
    if (healthz.ready !== true) return false;
    var bf = healthz.from_pubkey_backfill;
    if (bf && bf.done === false) return false;
    return true;
  }

  // -------- Browser integration (mount + poll + fetch intercept) ------------

  var state = {
    healthz: null,
    loadStatus: null,
    el: null,
    listEl: null,
    timer: null,
    inflight: false,
    mounted: false,
  };

  function ensureMounted() {
    if (state.mounted || typeof document === 'undefined') return;
    var body = document.body;
    if (!body) return;
    var el = document.createElement('div');
    el.id = 'warmup-banner';
    el.setAttribute('role', 'status');
    el.setAttribute('aria-live', 'polite');
    el.setAttribute('aria-atomic', 'true');
    el.className = 'warmup-banner warmup-banner--hidden';
    var inner = document.createElement('div');
    inner.className = 'warmup-banner__inner';
    var list = document.createElement('ul');
    list.className = 'warmup-banner__list';
    inner.appendChild(list);
    el.appendChild(inner);
    body.insertBefore(el, body.firstChild);
    state.el = el;
    state.listEl = list;
    state.mounted = true;
  }

  function render() {
    if (!state.mounted) ensureMounted();
    if (!state.el || !state.listEl) return;
    var now = Date.now();
    var msgs = getWarmupMessages(state.healthz, state.loadStatus, now);
    if (msgs.length === 0) {
      // Fade out.
      state.el.classList.add('warmup-banner--hidden');
      state.listEl.innerHTML = '';
      return;
    }
    // Diff-render: rebuild list (always small — <=3 items).
    var html = '';
    for (var i = 0; i < msgs.length; i++) {
      var safe = String(msgs[i])
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
      html += '<li class="warmup-banner__item">' + safe + '</li>';
    }
    state.listEl.innerHTML = html;
    state.el.classList.remove('warmup-banner--hidden');
  }

  function noteLoadStatus(value) {
    if (!value) return;
    if (value !== 'loading' && value !== 'ready') return;
    if (state.loadStatus === value) return;
    state.loadStatus = value;
    render();
    // First time we observe "loading" — make sure polling is on.
    if (value === 'loading') startPolling();
  }

  function pollOnce() {
    if (state.inflight || typeof fetch !== 'function') return;
    state.inflight = true;
    fetch('/api/healthz', { credentials: 'same-origin', cache: 'no-store' })
      .then(function (resp) {
        if (resp && resp.headers && resp.headers.get) {
          noteLoadStatus(resp.headers.get('X-Corescope-Load-Status'));
        }
        if (!resp || !resp.ok) return null;
        return resp.json();
      })
      .then(function (body) {
        if (body) state.healthz = body;
        render();
        if (isSteadyState(state.healthz) && state.loadStatus !== 'loading') {
          stopPolling();
        }
      })
      .catch(function () { /* swallow — banner is best-effort */ })
      .then(function () { state.inflight = false; });
  }

  function startPolling() {
    if (state.timer) return;
    pollOnce();
    state.timer = setInterval(pollOnce, POLL_INTERVAL_MS);
  }

  function stopPolling() {
    if (state.timer) {
      clearInterval(state.timer);
      state.timer = null;
    }
  }

  function installFetchInterceptor() {
    if (typeof window === 'undefined' || typeof window.fetch !== 'function') return;
    // Double-install guard: check both the module flag AND a marker stamped
    // onto the wrapper itself. Prevents nested wrap if window.fetch was
    // reassigned externally between installs.
    if (window.__warmupBannerFetchPatched && window.fetch.__warmupWrapped) return;
    if (window.fetch.__warmupWrapped) { window.__warmupBannerFetchPatched = true; return; }
    var orig = window.fetch.bind(window);
    var wrapped = function () {
      var p = orig.apply(null, arguments);
      try {
        p.then(function (resp) {
          if (resp && resp.headers && resp.headers.get) {
            noteLoadStatus(resp.headers.get('X-Corescope-Load-Status'));
          }
        }, function () {});
      } catch (e) { /* ignore */ }
      return p;
    };
    wrapped.__warmupWrapped = true;
    window.fetch = wrapped;
    window.__warmupBannerFetchPatched = true;
  }

  function init() {
    if (typeof document === 'undefined') return;
    ensureMounted();
    installFetchInterceptor();
    // Kick off an immediate health check so the banner appears within ~2s
    // of first paint (acceptance criterion).
    startPolling();
  }

  if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', init);
    } else {
      init();
    }
  }

  // -------- Exports ---------------------------------------------------------

  var api = {
    getWarmupMessages: getWarmupMessages,
    shouldShowBanner: shouldShowBanner,
    isSteadyState: isSteadyState,
    STALE_INGEST_MS: STALE_INGEST_MS,
    POLL_INTERVAL_MS: POLL_INTERVAL_MS,
    // Test hooks
    _state: state,
    _pollOnce: pollOnce,
    _installFetchInterceptor: installFetchInterceptor,
  };

  if (typeof window !== 'undefined') {
    window.__warmupBanner = api;
  }
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  }
})();
