/* === CoreScope — url-state.js ===
 *
 * Shared helpers for encoding/decoding view & filter state in the URL hash.
 * Pages use these so deep links restore the exact view (issue #749).
 *
 * Hash format: "#/<route>?key1=val1&key2=val2"
 *
 * Existing deep links remain intact:
 *   #/nodes/<pubkey>            (path segment after route)
 *   #/packets/<hash>            (path segment after route)
 *   #/packets?filter=...        (query after route)
 *
 * This module ONLY parses/serializes — it never mutates location.
 */
'use strict';

(function (root) {
  // Stub — implemented in green commit
  function parseSort(s) {
    throw new Error('not implemented');
  }
  function serializeSort(column, direction) {
    throw new Error('not implemented');
  }
  function parseHash(hash) {
    throw new Error('not implemented');
  }
  function buildHash(route, params) {
    throw new Error('not implemented');
  }
  function updateHashParams(updates) {
    throw new Error('not implemented');
  }

  var api = {
    parseSort: parseSort,
    serializeSort: serializeSort,
    parseHash: parseHash,
    buildHash: buildHash,
    updateHashParams: updateHashParams,
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.URLState = api;
})(typeof window !== 'undefined' ? window : globalThis);
