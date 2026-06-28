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

  // Packet-level path-hash size (1|2|3), or 0 when unresolvable.
  // Reads the path-length byte from raw_hex; its top two bits encode
  // hashSize-1. Offset is 5 for transport routes (route_type 0/3, which
  // carry 4 next/last-hop code bytes before path-length) else 1 — mirrors
  // getPathLenOffset()/computeBreakdownRanges() in public/app.js. Reading
  // from raw_hex (not path hops) is correct even for zero-hop packets.
  function packetHashSize(rawHex, routeType) {
    if (!rawHex || typeof rawHex !== 'string') return 0;
    var clean = rawHex.replace(/\s+/g, '');
    var bytes = clean.length >> 1;
    var offset = (routeType === 0 || routeType === 3) ? 5 : 1;
    if (bytes < offset + 1) return 0;
    var pathByte = parseInt(clean.slice(offset * 2, offset * 2 + 2), 16);
    if (isNaN(pathByte)) return 0;
    return (pathByte >> 6) + 1;
  }

  // Render-time predicate. opts may be omitted — if so, falls back to the
  // current localStorage value. The hop is hidden only when:
  //   - opts.hide1ByteHops === true AND
  //   - the hop hex encodes exactly 1 byte (length === 2)
  // Anything else (origin/destination payload hops, multi-byte path hops,
  // null/undefined sentinels) stays visible — those callers already
  // bypass the filter when they pass undefined/falsey tokens.
  function isVisibleHop(hop, opts) {
    var enabled = (opts && typeof opts.hide1ByteHops === 'boolean')
      ? opts.hide1ByteHops
      : getHide1ByteHops();
    if (!enabled) return true;
    return hopByteLen(hop) !== 1;
  }

  // Filter a path hop array. Returns a NEW array; never mutates the input
  // (callers depend on the original path_json staying intact for downstream
  // consumers like hash-size detection / raw-hex rendering).
  function filterPathHops(hops, opts) {
    if (!hops || !hops.length) return hops || [];
    var enabled = (opts && typeof opts.hide1ByteHops === 'boolean')
      ? opts.hide1ByteHops
      : getHide1ByteHops();
    if (!enabled) return hops;
    var out = [];
    for (var i = 0; i < hops.length; i++) {
      if (hopByteLen(hops[i]) !== 1) out.push(hops[i]);
    }
    return out;
  }

  if (typeof window !== 'undefined') {
    window.MC_getHide1ByteHops = getHide1ByteHops;
    window.MC_setHide1ByteHops = setHide1ByteHops;
    window.MC_isVisibleHop = isVisibleHop;
    window.MC_filterPathHops = filterPathHops;
    window.MC_hopByteLen = hopByteLen;
    window.MC_packetHashSize = packetHashSize;
  }

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      getHide1ByteHops: getHide1ByteHops,
      setHide1ByteHops: setHide1ByteHops,
      isVisibleHop: isVisibleHop,
      filterPathHops: filterPathHops,
      hopByteLen: hopByteLen,
      packetHashSize: packetHashSize,
      _STORAGE_KEY: STORAGE_KEY
    };
  }
})();
