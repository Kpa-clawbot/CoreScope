/*
 * warmup-banner.js — global top-banner / status pill surfacing server warm-up state.
 *
 * Consumes:
 *   - X-Corescope-Load-Status response header ("loading" | "ready"), set by
 *     cmd/server/chunked_load.go on every response.
 *   - GET /api/healthz, polled every 30s while not steady-state.
 *
 * Renders a role="status" live region so screen readers announce transitions.
 * Auto-dismisses once ready=true AND from_pubkey_backfill.done=true.
 *
 * Stub: pure helpers are implemented below; mount/poll IIFE is at the bottom.
 */
(function () {
  'use strict';

  var STALE_INGEST_MS = 5 * 60 * 1000; // 5 min — matches acceptance criteria

  /**
   * Build the list of human-readable warm-up messages from current state.
   * Pure function — testable in isolation.
   *
   * @param {object} healthz - parsed /api/healthz body, or null if unknown
   * @param {string} loadStatus - last seen X-Corescope-Load-Status header value
   * @param {number} nowMs - current time in ms (injectable for tests)
   * @returns {string[]} ordered list of messages; empty when steady-state.
   */
  function getWarmupMessages(healthz, loadStatus, nowMs) {
    // STUB — not yet implemented. Returns empty so the red test fails on
    // assertion (length === 0 vs expected >= 1) rather than on undefined access.
    return [];
  }

  /**
   * Decide whether the banner should be visible.
   * @returns {boolean}
   */
  function shouldShowBanner(healthz, loadStatus, nowMs) {
    return getWarmupMessages(healthz, loadStatus, nowMs).length > 0;
  }

  // Expose for tests and for other modules that want to peek at state.
  var api = {
    getWarmupMessages: getWarmupMessages,
    shouldShowBanner: shouldShowBanner,
    STALE_INGEST_MS: STALE_INGEST_MS,
  };

  if (typeof window !== 'undefined') {
    window.__warmupBanner = api;
  }
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  }
})();
