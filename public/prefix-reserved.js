/* === CoreScope — prefix-reserved.js =====================================
 *
 * Issue #1473 — Flag prefixes that the MeshCore firmware keygen routine
 * avoids by convention.
 *
 * Scope (narrow, per meshcore-protocol-expert review):
 *   - This is a FIRMWARE KEYGEN CONVENTION, not a protocol-level rule.
 *     The standard repeater example re-rolls any new identity whose
 *     public-key FIRST BYTE is 0x00 or 0xFF, so in practice you should
 *     never see a node prefix of 00 or FF in the wild.
 *   - We only check the FIRST byte. Other bytes 00/FF inside a pubkey are
 *     perfectly normal (~96% of pubkeys contain a 00 or FF byte somewhere).
 *   - There is NO protocol rejection of such pubkeys and NO routing-level
 *     wildcard semantics tied to dest_hash == 0xFF.
 *
 * Firmware citation (HEAD 8ede7641, examples/simple_repeater/main.cpp:83):
 *
 *   while (count < 10 && (the_mesh.self_id.pub_key[0] == 0x00
 *                      || the_mesh.self_id.pub_key[0] == 0xFF)) {
 *     // reserved id hashes
 *     the_mesh.self_id = radio_new_identity(); count++;
 *   }
 *
 *   https://github.com/meshcore-dev/MeshCore/blob/8ede7641/examples/simple_repeater/main.cpp#L83
 *
 * Surfaces that consume this helper:
 *   - Prefix matrix (analytics.js → renderHashMatrixFromServer, 1-byte view):
 *     grey 00 / FF cells and disable click, tooltip explains the convention.
 *   - Prefix generator (analytics.js → renderPrefixTool.doGenerate):
 *     never suggest a prefix whose first byte is 00 / FF; visible note.
 *
 * Reporter: @halo779 (community).
 * ========================================================================= */
'use strict';

(function (root) {
  // First-byte reservations as uppercase 2-char hex strings.
  var RESERVED_FIRST_BYTES = ['00', 'FF'];
  var RESERVED_CLASS = 'prefix-reserved';
  var RESERVED_NOTE = '0x00 and 0xFF excluded — the MeshCore firmware keygen routine avoids these as the first byte of a node pubkey.';
  var RESERVED_TITLE =
    '0x00 and 0xFF as a first byte are avoided by the MeshCore firmware keygen convention (the standard repeater re-rolls identities whose pub_key[0] is 0x00 or 0xFF), so you should not pick them as a node prefix.';

  function isReservedPrefix(prefix) {
    if (prefix == null) return false;
    var s = String(prefix);
    if (s.length < 2) return false;
    var head = s.slice(0, 2).toUpperCase();
    for (var i = 0; i < RESERVED_FIRST_BYTES.length; i++) {
      if (head === RESERVED_FIRST_BYTES[i]) return true;
    }
    return false;
  }

  function filterReserved(prefixes) {
    var out = [];
    for (var i = 0; i < prefixes.length; i++) {
      if (!isReservedPrefix(prefixes[i])) out.push(prefixes[i]);
    }
    return out;
  }

  // How many prefixes of `bytes` length the reservation removes from the
  // total space of 256^bytes. (For each reserved first byte the entire
  // 256^(bytes-1) tail is reserved.)
  function reservedCount(bytes) {
    var b = Number(bytes) || 1;
    if (b < 1) return 0;
    return RESERVED_FIRST_BYTES.length * Math.pow(256, b - 1);
  }

  // Given a DOM root (or any object exposing querySelectorAll), find
  // hash-matrix cells whose data-hex first byte is reserved, mark them
  // .prefix-reserved + aria-disabled, strip .hash-active so the matrix's
  // click wiring skips them, and set a tooltip explaining why.
  // Returns the count of cells marked.
  function markReservedCells(root) {
    if (!root || typeof root.querySelectorAll !== 'function') return 0;
    var cells = root.querySelectorAll('[data-hex]');
    var n = 0;
    for (var i = 0; i < cells.length; i++) {
      var td = cells[i];
      var hex = (typeof td.getAttribute === 'function')
        ? td.getAttribute('data-hex')
        : (td.dataset && td.dataset.hex);
      if (!isReservedPrefix(hex)) continue;
      if (td.classList && typeof td.classList.add === 'function') {
        td.classList.add(RESERVED_CLASS);
        td.classList.remove('hash-active');
      }
      if (typeof td.setAttribute === 'function') {
        td.setAttribute('aria-disabled', 'true');
        td.setAttribute('title', RESERVED_TITLE);
      }
      n++;
    }
    return n;
  }

  var api = {
    RESERVED_FIRST_BYTES: RESERVED_FIRST_BYTES.slice(),
    RESERVED_CLASS: RESERVED_CLASS,
    RESERVED_NOTE: RESERVED_NOTE,
    RESERVED_TITLE: RESERVED_TITLE,
    isReservedPrefix: isReservedPrefix,
    filterReserved: filterReserved,
    reservedCount: reservedCount,
    markReservedCells: markReservedCells,
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  if (root) root.PrefixReserved = api;
})(typeof window !== 'undefined' ? window : globalThis);
