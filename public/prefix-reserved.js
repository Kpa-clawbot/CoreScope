/* === CoreScope — prefix-reserved.js (#1473 stub for RED commit) === */
'use strict';

// STUB — replaced in the green commit. Lets the tests reach assertions.
(function (root) {
  var api = {
    RESERVED_FIRST_BYTES: [],
    RESERVED_CLASS: 'prefix-reserved',
    RESERVED_NOTE: '',
    isReservedPrefix: function () { return false; },
    filterReserved: function (prefixes) { return Array.prototype.slice.call(prefixes); },
    reservedCount: function () { return 0; },
    markReservedCells: function () { return 0; },
  };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  if (root) root.PrefixReserved = api;
})(typeof window !== 'undefined' ? window : globalThis);
