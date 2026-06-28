/* payload-labels.js — canonical MeshCore payload-type label map.
 *
 * Single source of truth for human-readable labels of firmware payload type
 * enums. Surfaces that previously hand-rolled their own vocabularies
 * (packets.js typeMap, packet-filter.js FW_PAYLOAD_TYPES/TYPE_ALIASES,
 * live.js legend) consume this map so the same payload reads identically
 * everywhere the operator sees it.
 *
 * Keyed by the firmware enum name; values carry:
 *   enumName   — firmware enum name (mirrors the key; #1799 r1 item 9)
 *   short      — compact label used in dropdowns, table cells, legend titles
 *   long       — descriptive label used in tooltips and legend sub-text
 *   enumId     — numeric firmware payload_type value (the wire byte)
 *   legendNote — optional sub-string appended to legend text (#1799 r1 item 7)
 *
 * Refs #1799.
 */
(function () {
  'use strict';

  // PAYLOAD_LABELS — every entry carries enumName for shape uniformity with
  // BY_ID (#1799 r1 item 9).
  var PAYLOAD_LABELS = {
    REQ:        { enumName: 'REQ',        short: 'Request',     long: 'Data request',                enumId: 0  },
    RESPONSE:   { enumName: 'RESPONSE',   short: 'Response',    long: 'Data response',               enumId: 1  },
    TXT_MSG:    { enumName: 'TXT_MSG',    short: 'Direct Msg',  long: 'Direct message',              enumId: 2  },
    ACK:        { enumName: 'ACK',        short: 'ACK',         long: 'Acknowledgment',              enumId: 3,
                  legendNote: 'Other \u2014 Acknowledgment or unknown type' },
    ADVERT:     { enumName: 'ADVERT',     short: 'Advert',      long: 'Node advertisement',          enumId: 4  },
    GRP_TXT:    { enumName: 'GRP_TXT',    short: 'Channel Msg', long: 'Group text',                  enumId: 5  },
    GRP_DATA:   { enumName: 'GRP_DATA',   short: 'Group Data',  long: 'Group datagram',              enumId: 6  },
    ANON_REQ:   { enumName: 'ANON_REQ',   short: 'Anon Req',    long: 'Anonymous request',           enumId: 7  },
    PATH:       { enumName: 'PATH',       short: 'Path',        long: 'Path discovery',              enumId: 8  },
    TRACE:      { enumName: 'TRACE',      short: 'Trace',       long: 'Route trace',                 enumId: 9  },
    MULTIPART:  { enumName: 'MULTIPART',  short: 'Multipart',   long: 'Multi-fragment payload',      enumId: 10 },
    CONTROL:    { enumName: 'CONTROL',    short: 'Control',     long: 'Control plane',               enumId: 11 },
    RAW_CUSTOM: { enumName: 'RAW_CUSTOM', short: 'Raw Custom',  long: 'Application-defined payload', enumId: 15 }
  };

  // Legend display order (#1799 r1 item 5) — keeps Advert/GRP_TXT/TXT_MSG up
  // top per the historical Live legend layout.
  var ORDER = [
    'ADVERT', 'GRP_TXT', 'TXT_MSG', 'REQ', 'RESPONSE', 'TRACE', 'PATH',
    'ANON_REQ', 'GRP_DATA', 'MULTIPART', 'CONTROL', 'RAW_CUSTOM', 'ACK'
  ];

  // Reverse lookup: numeric enumId → entry. Built from PAYLOAD_LABELS.
  // Single `Object.entries` loop pattern across the module (#1799 r1 item 8).
  var BY_ID = {};
  Object.entries(PAYLOAD_LABELS).forEach(function (kv) {
    BY_ID[kv[1].enumId] = kv[1];
  });

  // Numeric id → firmware enum name. Mirrors what packet-filter.js used to
  // hand-roll as FW_PAYLOAD_TYPES.
  var FW_PAYLOAD_TYPES = {};
  Object.entries(BY_ID).forEach(function (kv) {
    FW_PAYLOAD_TYPES[kv[0]] = kv[1].enumName;
  });

  // Numeric id → short prose label. Replaces packets.js typeMap/TYPE_NAMES.
  var SHORT_BY_ID = {};
  Object.entries(BY_ID).forEach(function (kv) {
    SHORT_BY_ID[kv[0]] = kv[1].short;
  });

  // User-input alias map (lowercased short label & legacy aliases) → enum.
  // Replaces packet-filter.js TYPE_ALIASES while staying backward-compatible
  // with the legacy free-text inputs operators have memorised.
  //
  // NOTE: 'raw custom' is intentionally listed alongside 'raw'/'custom' so
  // the documented filter syntax mirrors the rendered short label
  // ("Raw Custom"). (#1799 r1 item 11.)
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

  // Public API — ONE global (#1799 r1 item 10). The map is exposed as
  // `LABELS` and legacy direct-access (`PayloadLabels.GRP_DATA.short`) still
  // works because every map entry hangs off the same object as a named
  // property below.
  var api = {
    LABELS: PAYLOAD_LABELS,
    ORDER: ORDER,
    BY_ID: BY_ID,
    FW_PAYLOAD_TYPES: FW_PAYLOAD_TYPES,
    SHORT_BY_ID: SHORT_BY_ID,
    TYPE_ALIASES: TYPE_ALIASES,
    shortById: function (id) { return SHORT_BY_ID[id]; },
    longById: function (id) { return (BY_ID[id] && BY_ID[id].long) || ''; },
    enumNameById: function (id) { return FW_PAYLOAD_TYPES[id]; }
  };
  // Make every enum entry directly addressable on the API object so the
  // legacy `window.PayloadLabels.GRP_DATA.short` shape keeps working without
  // a second global.
  Object.entries(PAYLOAD_LABELS).forEach(function (kv) {
    api[kv[0]] = kv[1];
  });

  if (typeof window !== 'undefined') {
    window.PayloadLabels = api;
  }
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  }
})();
