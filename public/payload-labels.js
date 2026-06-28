/* payload-labels.js — canonical MeshCore payload-type label map.
 *
 * Single source of truth for human-readable labels of firmware payload type
 * enums. Surfaces that previously hand-rolled their own vocabularies
 * (packets.js typeMap, packet-filter.js FW_PAYLOAD_TYPES/TYPE_ALIASES,
 * live.js legend) consume this map so the same payload reads identically
 * everywhere the operator sees it.
 *
 * Keyed by the firmware enum name; values carry:
 *   short  — compact label used in dropdowns, table cells, legend titles
 *   long   — descriptive label used in tooltips and legend sub-text
 *   enumId — numeric firmware payload_type value (the wire byte)
 *
 * Refs #1799.
 */
(function () {
  'use strict';

  var PAYLOAD_LABELS = {
    REQ:        { short: 'Request',     long: 'Data request',                enumId: 0 },
    RESPONSE:   { short: 'Response',    long: 'Data response',               enumId: 1 },
    TXT_MSG:    { short: 'Direct Msg',  long: 'Direct message',              enumId: 2 },
    ACK:        { short: 'ACK',         long: 'Acknowledgment',              enumId: 3 },
    ADVERT:     { short: 'Advert',      long: 'Node advertisement',          enumId: 4 },
    GRP_TXT:    { short: 'Channel Msg', long: 'Group text',                  enumId: 5 },
    GRP_DATA:   { short: 'Group Data',  long: 'Group datagram',              enumId: 6 },
    ANON_REQ:   { short: 'Anon Req',    long: 'Anonymous request',           enumId: 7 },
    PATH:       { short: 'Path',        long: 'Path discovery',              enumId: 8 },
    TRACE:      { short: 'Trace',       long: 'Route trace',                 enumId: 9 },
    MULTIPART:  { short: 'Multipart',   long: 'Multi-fragment payload',      enumId: 10 },
    CONTROL:    { short: 'Control',     long: 'Control plane',               enumId: 11 },
    RAW_CUSTOM: { short: 'Raw Custom',  long: 'Application-defined payload', enumId: 15 }
  };

  // Reverse lookup: numeric enumId → entry. Lazily built from PAYLOAD_LABELS.
  var BY_ID = (function () {
    var m = {};
    for (var k in PAYLOAD_LABELS) {
      if (Object.prototype.hasOwnProperty.call(PAYLOAD_LABELS, k)) {
        m[PAYLOAD_LABELS[k].enumId] = Object.assign({ enumName: k }, PAYLOAD_LABELS[k]);
      }
    }
    return m;
  })();

  // Numeric id → firmware enum name. Mirrors what packet-filter.js used to
  // hand-roll as FW_PAYLOAD_TYPES.
  var FW_PAYLOAD_TYPES = (function () {
    var m = {};
    for (var id in BY_ID) m[id] = BY_ID[id].enumName;
    return m;
  })();

  // Numeric id → short prose label. Replaces packets.js typeMap/TYPE_NAMES.
  var SHORT_BY_ID = (function () {
    var m = {};
    for (var id in BY_ID) m[id] = BY_ID[id].short;
    return m;
  })();

  // User-input alias map (lowercased short label & legacy aliases) → enum.
  // Replaces packet-filter.js TYPE_ALIASES while staying backward-compatible
  // with the legacy free-text inputs operators have memorised.
  var TYPE_ALIASES = {
    'request': 'REQ',
    'response': 'RESPONSE',
    'direct msg': 'TXT_MSG',
    'dm': 'TXT_MSG',
    'ack': 'ACK',
    'advert': 'ADVERT',
    'channel msg': 'GRP_TXT',
    'channel': 'GRP_TXT',
    'group data': 'GRP_DATA',
    'anon req': 'ANON_REQ',
    'path': 'PATH',
    'trace': 'TRACE',
    'multipart': 'MULTIPART',
    'control': 'CONTROL',
    'raw': 'RAW_CUSTOM',
    'custom': 'RAW_CUSTOM',
    'raw custom': 'RAW_CUSTOM'
  };

  var api = {
    PAYLOAD_LABELS: PAYLOAD_LABELS,
    BY_ID: BY_ID,
    FW_PAYLOAD_TYPES: FW_PAYLOAD_TYPES,
    SHORT_BY_ID: SHORT_BY_ID,
    TYPE_ALIASES: TYPE_ALIASES,
    shortById: function (id) { return SHORT_BY_ID[id]; },
    longById: function (id) { return (BY_ID[id] && BY_ID[id].long) || ''; },
    enumNameById: function (id) { return FW_PAYLOAD_TYPES[id]; }
  };

  if (typeof window !== 'undefined') {
    // Backwards-friendly globals so legacy code that reads
    // `window.PayloadLabels.GRP_DATA.short` works.
    window.PayloadLabels = PAYLOAD_LABELS;
    window.PayloadLabelsApi = api;
  }
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  }
})();
