/**
 * channel-qr.js — STUB (red commit)
 *
 * Real implementation lands in the green commit. This stub exists so
 * the test file can require/load the module and fail on assertions
 * (per repo TDD rule: red commit must compile + fail on assertion).
 */
(function (root) {
  'use strict';

  function buildUrl(_name, _secretHex) { return null; }
  function parseChannelUrl(_url) { return null; }
  function generate(_name, _secretHex, _targetElement) { /* no-op */ }
  function scan() { return Promise.resolve(null); }

  root.ChannelQR = { buildUrl, parseChannelUrl, generate, scan };
})(typeof window !== 'undefined' ? window : globalThis);
