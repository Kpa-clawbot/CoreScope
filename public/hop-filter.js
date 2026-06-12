/* === CoreScope — hop-filter.js === */
/* #1633 — Render-time filter that hides 1-byte path hops when the
 * customize-v2 toggle is ON. Pure render-time; firmware behavior and
 * wire/store contents are untouched.
 *
 * A "hop" here is a hex-string prefix as stored in observations.path_json
 * (e.g. "AB" = 1-byte, "CDEF" = 2-byte, "ABCDEF" = 3-byte). Byte count
 * is `floor(hopHex.length / 2)`.
 *
 * Wire & store stay intact: every consumer call site reads the toggle
 * via window.MC_getHide1ByteHops() and filters its rendered/aggregated
 * view at the boundary (no upstream mutation).
 */
'use strict';

(function () {
  var STORAGE_KEY = 'meshcore-hide-1byte-hops';

  function getHide1ByteHops() {
    try { return localStorage.getItem(STORAGE_KEY) === 'true'; }
    catch (_e) { return false; }
  }

  function setHide1ByteHops(on) {
    try {
      if (on) localStorage.setItem(STORAGE_KEY, 'true');
      else localStorage.removeItem(STORAGE_KEY);
    } catch (_e) { /* private mode */ }
    if (typeof window !== 'undefined' && typeof window.CustomEvent === 'function') {
      window.dispatchEvent(new window.CustomEvent('mc-hide-1byte-hops-changed', {
        detail: { value: !!on }
      }));
    }
  }

  // bytes of a hop hex token — handles undefined / non-string.
  function hopByteLen(h) {
    if (h == null) return 0;
    var s = String(h);
    return s.length >> 1;
  }

  // STUB (RED): returns true for everything regardless of opts.
  // GREEN commit replaces this with the real filter.
  function isVisibleHop(hop, opts) {
    return true;
  }

  // STUB (RED): returns input unchanged.
  function filterPathHops(hops, opts) {
    return hops || [];
  }

  if (typeof window !== 'undefined') {
    window.MC_getHide1ByteHops = getHide1ByteHops;
    window.MC_setHide1ByteHops = setHide1ByteHops;
    window.MC_isVisibleHop = isVisibleHop;
    window.MC_filterPathHops = filterPathHops;
    window.MC_hopByteLen = hopByteLen;
  }

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      getHide1ByteHops: getHide1ByteHops,
      setHide1ByteHops: setHide1ByteHops,
      isVisibleHop: isVisibleHop,
      filterPathHops: filterPathHops,
      hopByteLen: hopByteLen,
      _STORAGE_KEY: STORAGE_KEY
    };
  }
})();
