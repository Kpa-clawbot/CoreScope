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
  /**
   * Parse a sort token "column[:direction]" into { column, direction }.
   * Direction defaults to 'desc'. Anything other than 'asc'/'desc' falls back to 'desc'.
   * Empty/null input returns null.
   */
  function parseSort(s) {
    if (s == null || s === '') return null;
    var str = String(s);
    var idx = str.indexOf(':');
    var column = idx >= 0 ? str.slice(0, idx) : str;
    var dir = idx >= 0 ? str.slice(idx + 1) : 'desc';
    if (dir !== 'asc' && dir !== 'desc') dir = 'desc';
    return { column: column, direction: dir };
  }

  /**
   * Serialize a sort state to a token. 'desc' is the default and omitted.
   * Empty/null column returns ''.
   */
  function serializeSort(column, direction) {
    if (!column) return '';
    if (direction === 'asc') return column + ':asc';
    return String(column);
  }

  /**
   * Parse a location.hash string into { route, params }.
   * - Strips leading '#' and '/'.
   * - Splits on first '?'; left = route (may include subpath like 'nodes/abc'),
   *   right = querystring parsed via URLSearchParams.
   */
  function parseHash(hash) {
    var h = String(hash || '');
    if (h.charAt(0) === '#') h = h.slice(1);
    if (h.charAt(0) === '/') h = h.slice(1);
    if (h === '') return { route: '', params: {} };
    var qi = h.indexOf('?');
    var route = qi >= 0 ? h.slice(0, qi) : h;
    var qs = qi >= 0 ? h.slice(qi + 1) : '';
    var params = {};
    if (qs) {
      var sp = new URLSearchParams(qs);
      sp.forEach(function (v, k) { params[k] = v; });
    }
    return { route: route, params: params };
  }

  /**
   * Build a hash string '#/<route>?k=v&...'. Skips keys with null/undefined/'' values.
   * 'route' may be passed as '#/foo', '/foo' or 'foo'.
   */
  function buildHash(route, params) {
    var r = String(route || '');
    if (r.charAt(0) === '#') r = r.slice(1);
    if (r.charAt(0) === '/') r = r.slice(1);
    var sp = new URLSearchParams();
    if (params && typeof params === 'object') {
      for (var k in params) {
        if (!Object.prototype.hasOwnProperty.call(params, k)) continue;
        var v = params[k];
        if (v == null || v === '') continue;
        sp.set(k, String(v));
      }
    }
    var qs = sp.toString();
    return '#/' + r + (qs ? '?' + qs : '');
  }

  /**
   * Apply a partial-update to the params of an existing hash, preserving the route
   * (including any subpath like 'nodes/<pubkey>'). Returns the new hash string —
   * caller decides whether to history.replaceState() it.
   *
   * Setting a key to '' / null / undefined removes it.
   */
  function updateHashParams(updates, currentHash) {
    var src = currentHash != null ? currentHash :
      (typeof location !== 'undefined' ? location.hash : '');
    var parsed = parseHash(src);
    var merged = {};
    var k;
    for (k in parsed.params) {
      if (Object.prototype.hasOwnProperty.call(parsed.params, k)) merged[k] = parsed.params[k];
    }
    if (updates && typeof updates === 'object') {
      for (k in updates) {
        if (!Object.prototype.hasOwnProperty.call(updates, k)) continue;
        var v = updates[k];
        if (v == null || v === '') delete merged[k];
        else merged[k] = v;
      }
    }
    return buildHash(parsed.route, merged);
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
