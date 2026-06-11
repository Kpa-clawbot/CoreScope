/* === CoreScope — roles.js (shared config module) === */
'use strict';

/*
 * Centralized roles, thresholds, tile URLs, and UI constants.
 * Loaded BEFORE all page scripts via index.html.
 * Defaults are set synchronously; server config overrides arrive via fetch.
 */

(function () {
  // ─── Role definitions ───
  // #1407 — Wong palette defaults that match the unscoped --mc-role-* CSS
  // vars in :root of style.css. These are FALLBACKS only — the live getter
  // below reads --mc-role-* from documentElement on every access, so any
  // preset switch (cb-presets.js) is reflected immediately without per-page
  // listener wiring. The legacy April palette (#dc2626 etc.) was the bug.
  var WONG_ROLE_DEFAULTS = {
    repeater:  '#D55E00',
    companion: '#56B4E9',
    room:      '#009E73',
    sensor:    '#F0E442',
    observer:  '#CC79A7',
    unknown:   '#6b7280'
  };
  var WONG_ROLE_TEXT_DEFAULTS = {
    repeater: '#1a1a1a', companion: '#1a1a1a', room: '#1a1a1a',
    sensor: '#1a1a1a', observer: '#1a1a1a', unknown: '#1a1a1a'
  };

  function _readCssVar(name, fallback) {
    try {
      if (typeof document === 'undefined' || !document.documentElement) return fallback;
      var v = '';
      if (typeof getComputedStyle === 'function') {
        v = getComputedStyle(document.documentElement).getPropertyValue(name);
      }
      if (!v && document.documentElement.style && typeof document.documentElement.style.getPropertyValue === 'function') {
        v = document.documentElement.style.getPropertyValue(name);
      }
      v = (v || '').trim();
      return v || fallback;
    } catch (e) { return fallback; }
  }

  // Server-config overrides go into this object; the getter prefers them
  // when present so backend-pushed role colors still win over CSS vars.
  var _roleOverrides = {};

  function _liveRoleColors() {
    var base = {};
    var roles = ['repeater', 'companion', 'room', 'sensor', 'observer'];
    for (var i = 0; i < roles.length; i++) {
      var k = roles[i];
      base[k] = _roleOverrides[k] || _readCssVar('--mc-role-' + k, WONG_ROLE_DEFAULTS[k]);
    }
    base.unknown = _roleOverrides.unknown || WONG_ROLE_DEFAULTS.unknown;
    // Wrap in a Proxy so per-key assignment by legacy callers (customizer:
    //   `window.ROLE_COLORS[key] = inp.value`) lands in _roleOverrides and
    //   is visible on the NEXT read. Without this, the mutation would be
    //   thrown away when the snapshot is GC'd. Falls back to a plain object
    //   in environments without Proxy (none we ship to, but cheap).
    if (typeof Proxy === 'function') {
      return new Proxy(base, {
        set: function (t, prop, value) {
          _roleOverrides[prop] = value;
          t[prop] = value;
          return true;
        }
      });
    }
    return base;
  }

  Object.defineProperty(window, 'ROLE_COLORS', {
    configurable: true,
    enumerable: true,
    get: function () { return _liveRoleColors(); },
    // Setter accepts per-key writes — older callers do
    //   `ROLE_COLORS.repeater = '#xxx'`
    // which on a getter-only object would silently no-op in strict mode.
    // We treat any whole-object assignment as an override merge so the
    // legacy customizer code path still works.
    set: function (v) {
      if (v && typeof v === 'object') {
        for (var k in v) if (Object.prototype.hasOwnProperty.call(v, k)) _roleOverrides[k] = v[k];
      }
    }
  });
  // Per-key writes via Proxy not portable enough — expose helper for callers
  // that want to override at runtime (customizer "node colors" path).
  // #1438: snapshot of the cb-preset (or initial) CSS-var value per role
  // so that clearing an override restores the preset, not nothing.
  // We write the override to BOTH documentElement and body inline styles
  // because cb-presets ships stylesheet rules of the form
  //   body[data-cb-preset="deut"] { --mc-role-X: #...; }
  // which beats inheritance from :root. Body inline beats both.
  var _presetCssSnapshot = {};
  function _styleTargets() {
    var t = [];
    try { if (document.documentElement && document.documentElement.style) t.push(document.documentElement.style); } catch (e) {}
    try { if (document.body && document.body.style) t.push(document.body.style); } catch (e) {}
    return t.filter(function (s) { return s && typeof s.setProperty === 'function'; });
  }
  window.setRoleColorOverride = function (role, hex) {
    if (!role) return;
    var targets = _styleTargets();
    var varName = '--mc-role-' + role;

    if (hex == null || hex === '') {
      // Clear override → restore prior CSS var values captured at
      // first-override time, so CSS-var consumers see the preset color
      // again (matches JS getter behavior, preserves #1412 contract).
      delete _roleOverrides[role];
      if (Object.prototype.hasOwnProperty.call(_presetCssSnapshot, role)) {
        var snap = _presetCssSnapshot[role] || {};
        targets.forEach(function (s, i) {
          var prior = snap[i];
          if (prior && prior.length) s.setProperty(varName, prior);
          else s.removeProperty(varName);
        });
        delete _presetCssSnapshot[role];
      } else {
        targets.forEach(function (s) { s.removeProperty(varName); });
      }
      return;
    }
    // Capture the current per-target CSS var values before overwriting,
    // but only on the first override for this role so repeated picks
    // don't lose the original preset value.
    if (!Object.prototype.hasOwnProperty.call(_presetCssSnapshot, role)) {
      _presetCssSnapshot[role] = targets.map(function (s) {
        return s.getPropertyValue ? (s.getPropertyValue(varName) || '').trim() : '';
      });
    }
    _roleOverrides[role] = hex;
    // #1438: drive the CSS var so CSS-var consumers (cluster pills,
    // route lines, all marker SVGs that use fill="var(--mc-role-X)")
    // pick up the operator's hex without a page reload. Writing to
    // body inline style is necessary because body[data-cb-preset="..."]
    // selectors beat :root inheritance.
    //
    // #1446: write with !important so the inline body declaration also
    // beats the body[data-cb-preset="X"] CSS rule on equal specificity.
    // Without !important, the cascade order picks the later-defined
    // stylesheet rule in some browser versions even though specificity
    // (1,0,1) matches the inline body style — operator pick visibly
    // loses to active preset (root cause of #1444).
    targets.forEach(function (s) {
      // documentElement gets the value without !important (used as the
      // canonical readout for the JS getter); body gets !important so it
      // wins the CSS cascade against body[data-cb-preset="X"].
      if (s === (document.body && document.body.style)) {
        s.setProperty(varName, hex, 'important');
      } else {
        s.setProperty(varName, hex);
      }
    });
  };
  // Back-compat: also export the writable override map so customize.js's
  // `window.ROLE_COLORS[key] = inp.value` style mutation works.
  // We intercept by replacing the getter target with a Proxy on access.
  Object.defineProperty(window, 'ROLE_COLORS_OVERRIDES', {
    value: _roleOverrides, writable: false, enumerable: false, configurable: false
  });

  window.TYPE_COLORS = {
    ADVERT: '#22c55e', GRP_TXT: '#3b82f6', GRP_DATA: '#8b5cf6', TXT_MSG: '#f59e0b', ACK: '#6b7280',
    REQUEST: '#a855f7', RESPONSE: '#06b6d4', TRACE: '#ec4899', PATH: '#14b8a6',
    ANON_REQ: '#f43f5e', MULTIPART: '#0d9488', CONTROL: '#b45309', RAW_CUSTOM: '#c026d3',
    UNKNOWN: '#6b7280'
  };

  // Badge CSS class name mapping
  const TYPE_BADGE_MAP = {
    ADVERT: 'advert', GRP_TXT: 'grp-txt', GRP_DATA: 'grp-data', TXT_MSG: 'txt-msg', ACK: 'ack',
    REQUEST: 'req', RESPONSE: 'response', TRACE: 'trace', PATH: 'path',
    ANON_REQ: 'anon-req', MULTIPART: 'multipart', CONTROL: 'control', RAW_CUSTOM: 'raw-custom',
    UNKNOWN: 'unknown'
  };

  // Generate badge CSS from TYPE_COLORS — single source of truth
  window.syncBadgeColors = function() {
    var el = document.getElementById('type-color-badges');
    if (!el) { el = document.createElement('style'); el.id = 'type-color-badges'; document.head.appendChild(el); }
    var css = '';
    for (var type in TYPE_BADGE_MAP) {
      var color = window.TYPE_COLORS[type];
      if (!color) continue;
      var cls = TYPE_BADGE_MAP[type];
      css += '.badge-' + cls + ' { background: ' + color + '20; color: ' + color + '; }\n';
    }
    el.textContent = css;
  };

  // Auto-sync on load
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', window.syncBadgeColors);
  } else {
    window.syncBadgeColors();
  }

  window.ROLE_LABELS = {
    repeater: 'Repeaters', companion: 'Companions', room: 'Room Servers',
    sensor: 'Sensors', observer: 'Observers'
  };

  // #1293 — Marker shape per role (WCAG 1.4.1 — shape, not only colour).
  // Single source of truth; ROLE_STYLE.shape is derived from this map.
  window.ROLE_SHAPES = {
    repeater:  'circle',
    companion: 'square',
    room:      'hexagon',
    sensor:    'triangle',
    observer:  'diamond'
  };

  // #1407 — ROLE_STYLE.color reads live (matches ROLE_COLORS getter).
  // The shape/radius/weight stay static. Stored overrides survive across
  // reads via the closure above.
  var _styleShapes = {
    repeater:  { shape: 'circle',   radius: 8, weight: 2 },
    companion: { shape: 'square',   radius: 8, weight: 2 },
    room:      { shape: 'hexagon',  radius: 9, weight: 2 },
    sensor:    { shape: 'triangle', radius: 8, weight: 2 },
    observer:  { shape: 'diamond',  radius: 9, weight: 2 }
  };
  function _buildRoleStyle() {
    var out = {};
    var live = _liveRoleColors();
    for (var role in _styleShapes) {
      var s = _styleShapes[role];
      out[role] = {
        color:  _roleOverrides[role] || live[role],
        shape:  s.shape,
        radius: s.radius,
        weight: s.weight
      };
    }
    return out;
  }
  Object.defineProperty(window, 'ROLE_STYLE', {
    configurable: true,
    enumerable: true,
    get: function () { return _buildRoleStyle(); },
    set: function (v) {
      // Legacy whole-object assignment: copy color overrides only.
      if (v && typeof v === 'object') {
        for (var k in v) if (v[k] && v[k].color) _roleOverrides[k] = v[k].color;
      }
    }
  });

  // Glyphs mirror the ROLE_SHAPES (used in tooltips, legends, lists).
  window.ROLE_EMOJI = {
    repeater: '●', companion: '■', room: '⬢', sensor: '▲', observer: '◆'
  };

  /**
   * #1293 — Shared SVG marker generator. Returns a self-contained
   * <svg>...</svg> string for the given role/colour/size, with white
   * stroke for contrast (works on both dark + light tiles). Used by:
   *   - public/live.js  → addNodeMarker (L.divIcon)
   *   - public/live.js  → role legend swatches
   *   - public/map.js   → makeMarkerIcon (legacy switch retained for
   *                       per-role overrides + observer star overlay)
   *
   * Reads ROLE_SHAPES for the role's geometry; falls back to circle.
   * Caller controls colour to allow theming overrides (matrix mode,
   * stale dim, etc.) without rebuilding the marker.
   */
  window.makeRoleMarkerSVG = function (role, color, size) {
    var shape = (window.ROLE_SHAPES && window.ROLE_SHAPES[role]) || 'circle';
    size = size || 16;
    var c = size / 2;
    // #1438: default fill resolves through the live CSS var so existing
    // mounted SVG markers recolor when cb-preset switches or the
    // operator picks a per-role override via the customizer. Callers
    // that need a fixed tint (matrix mode, stale dim) keep passing
    // their explicit colour.
    var fill = color || ('var(--mc-role-' + (role || 'companion') + ')');
    // #1488 — stroke routed through CSS vars so operators can dial
    // colour/width without code edits (customizer Colors → Marker Stroke).
    var strokeAttr = ' stroke="var(--mc-marker-stroke-color)" stroke-width="var(--mc-marker-stroke-width)" stroke-opacity="var(--mc-marker-stroke-opacity)"';
    var path;
    switch (shape) {
      case 'square':
        path = '<rect x="3" y="3" width="' + (size - 6) + '" height="' + (size - 6) +
               '" fill="' + fill + '"' + strokeAttr + '/>';
        break;
      case 'triangle':
        path = '<polygon points="' + c + ',2 ' + (size - 2) + ',' + (size - 2) +
               ' 2,' + (size - 2) + '" fill="' + fill + '"' + strokeAttr + '/>';
        break;
      case 'diamond':
        path = '<polygon points="' + c + ',2 ' + (size - 2) + ',' + c + ' ' +
               c + ',' + (size - 2) + ' 2,' + c +
               '" fill="' + fill + '"' + strokeAttr + '/>';
        break;
      case 'hexagon': {
        // Pointy-top hexagon centred at (c,c), inscribed radius ≈ c-1.5
        var r = c - 1.5;
        var pts = '';
        for (var i = 0; i < 6; i++) {
          var a = (i * 60 - 90) * Math.PI / 180;
          pts += (c + r * Math.cos(a)).toFixed(2) + ',' +
                 (c + r * Math.sin(a)).toFixed(2) + ' ';
        }
        path = '<polygon points="' + pts.trim() + '" fill="' + fill +
               '"' + strokeAttr + '/>';
        break;
      }
      case 'star': {
        var cx = c, cy = c, outer = c - 1, inner = outer * 0.4;
        var spts = '';
        for (var j = 0; j < 5; j++) {
          var aO = (j * 72 - 90) * Math.PI / 180;
          var aI = ((j * 72) + 36 - 90) * Math.PI / 180;
          spts += (cx + outer * Math.cos(aO)) + ',' + (cy + outer * Math.sin(aO)) + ' ';
          spts += (cx + inner * Math.cos(aI)) + ',' + (cy + inner * Math.sin(aI)) + ' ';
        }
        path = '<polygon points="' + spts.trim() + '" fill="' + fill +
               '"' + strokeAttr + '/>';
        break;
      }
      default: // circle
        path = '<circle cx="' + c + '" cy="' + c + '" r="' + (c - 2) +
               '" fill="' + fill + '"' + strokeAttr + '/>';
    }
    return '<svg width="' + size + '" height="' + size +
           '" viewBox="0 0 ' + size + ' ' + size +
           '" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">' + path + '</svg>';
  };

  window.ROLE_SORT = ['repeater', 'companion', 'room', 'sensor', 'observer'];

  // ─── Health thresholds (ms) ───
  window.HEALTH_THRESHOLDS = {
    infraDegradedMs: 86400000,   // 24h
    infraSilentMs:   259200000,  // 72h
    nodeDegradedMs:  3600000,    // 1h
    nodeSilentMs:    86400000    // 24h
  };

  // Helper: get degraded/silent thresholds for a role (backward compat)
  window.getHealthThresholds = function (role) {
    var isInfra = role === 'repeater' || role === 'room';
    return {
      degradedMs: isInfra ? HEALTH_THRESHOLDS.infraDegradedMs : HEALTH_THRESHOLDS.nodeDegradedMs,
      silentMs:   isInfra ? HEALTH_THRESHOLDS.infraSilentMs   : HEALTH_THRESHOLDS.nodeSilentMs
    };
  };

  // Simplified two-state helper: returns 'active' or 'stale'
  window.getNodeStatus = function (role, lastSeenMs) {
    var isInfra = role === 'repeater' || role === 'room';
    var staleMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;
    var age = typeof lastSeenMs === 'number' ? (Date.now() - lastSeenMs) : Infinity;
    return age < staleMs ? 'active' : 'stale';
  };

  // ─── Tile URLs ───
  window.TILE_DARK  = 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png';
  window.TILE_LIGHT = 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png';

  window.getTileUrl = function () {
    var isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' &&
       window.matchMedia('(prefers-color-scheme: dark)').matches);
    if (!isDark) return TILE_LIGHT;
    // #1461 followup: honor customizer's dark-tile-provider pick (#1420 / #1430)
    // when the registry is loaded. Falls back to TILE_DARK if absent.
    try {
      if (window.MC_getDarkTileProvider && window.MC_TILE_PROVIDERS) {
        var id = window.MC_getDarkTileProvider();
        var p = window.MC_TILE_PROVIDERS[id];
        if (p && (p.url || p.baseUrl)) {
          // #1614: providers added in #1533 (carto/osm/stamen) declare
          // `url` as a function for lazy config resolution. Invoke it so
          // we always return a string URL template; L.tileLayer otherwise
          // stringifies the function source and every tile request 404s.
          var u = p.url || p.baseUrl;
          return (typeof u === 'function') ? u() : u;
        }
      }
    } catch (_e) {}
    return TILE_DARK;
  };
  /* Helper: get the full provider object (for callers that also need the
   * invertFilter or refUrl/attribution). Returns null when no customizer
   * provider applies (light mode, or registry not loaded). */
  window.getActiveTileProvider = function () {
    var isDark = document.documentElement.getAttribute('data-theme') === 'dark' ||
      (document.documentElement.getAttribute('data-theme') !== 'light' &&
       window.matchMedia('(prefers-color-scheme: dark)').matches);
    if (!isDark) return null;
    try {
      if (window.MC_getDarkTileProvider && window.MC_TILE_PROVIDERS) {
        var id = window.MC_getDarkTileProvider();
        return window.MC_TILE_PROVIDERS[id] || null;
      }
    } catch (_e) {}
    return null;
  };

  // ─── SNR thresholds ───
  window.SNR_THRESHOLDS = { excellent: 6, good: 0 };

  // ─── Distance thresholds (km) ───
  window.DIST_THRESHOLDS = { local: 50, regional: 200 };

  // ─── MAX_HOP_DIST (degrees, ~200km ≈ 1.8°) ───
  window.MAX_HOP_DIST = 1.8;

  // ─── Result limits ───
  window.LIMITS = {
    topNodes: 15,
    topPairs: 12,
    topRingNodes: 8,
    topSenders: 10,
    topCollisionNodes: 10,
    recentReplay: 8,
    feedMax: 25
  };

  // ─── Performance thresholds ───
  window.PERF_SLOW_MS = 100;

  // ─── WebSocket reconnect delay (ms) ───
  window.WS_RECONNECT_MS = 3000;

  // ─── Propagation buffer (ms) for realistic mode ───
  window.PROPAGATION_BUFFER_MS = 5000;

  // ─── Cache invalidation debounce (ms) ───
  window.CACHE_INVALIDATE_MS = 5000;

  // #1574 — operator-configurable cap on /live map node fetch (overridden
  // from /api/config/client below). Default mirrors the historical
  // hardcoded literal in public/live.js.
  window.LIVE_MAP_MAX_NODES = 2000;

  // ─── External URLs ───
  window.EXTERNAL_URLS = {
    flasher: 'https://flasher.meshcore.io/'
  };

  // ─── Fetch server overrides ───
  window.MeshConfigReady = fetch('/api/config/client').then(function (r) { return r.json(); }).then(function (cfg) {
    if (cfg.roles) {
      if (cfg.roles.colors) {
        // #1407 — ROLE_COLORS is now a live getter; merge into the override map.
        for (var rk in cfg.roles.colors) _roleOverrides[rk] = cfg.roles.colors[rk];
      }
      if (cfg.roles.labels) Object.assign(ROLE_LABELS, cfg.roles.labels);
      if (cfg.roles.style) {
        // Same: merge color overrides only; shape/radius/weight come from _styleShapes.
        for (var sk in cfg.roles.style) {
          if (cfg.roles.style[sk] && cfg.roles.style[sk].color) _roleOverrides[sk] = cfg.roles.style[sk].color;
        }
      }
      if (cfg.roles.emoji) Object.assign(ROLE_EMOJI, cfg.roles.emoji);
      if (cfg.roles.sort) window.ROLE_SORT = cfg.roles.sort;
    }
    if (cfg.healthThresholds) Object.assign(HEALTH_THRESHOLDS, cfg.healthThresholds);
    if (cfg.map) {
      window.MC_MAP_CFG = cfg.map;
    } else {
      // Fallback for older configs
      window.MC_MAP_CFG = { tiles: { providers: {} } };
    }
    // Backward compat for older tile URL overrides
    if (cfg.tiles) {
      if (cfg.tiles.dark) window.TILE_DARK = cfg.tiles.dark;
      if (cfg.tiles.light) window.TILE_LIGHT = cfg.tiles.light;
    } else if (cfg.map && cfg.map.tiles) {
      if (cfg.map.tiles.darkUrl) window.TILE_DARK = cfg.map.tiles.darkUrl;
      if (cfg.map.tiles.lightUrl) window.TILE_LIGHT = cfg.map.tiles.lightUrl;
    }
    if (typeof window.MC_initTileRegistry === 'function') window.MC_initTileRegistry(true);
    if (cfg.snrThresholds) Object.assign(SNR_THRESHOLDS, cfg.snrThresholds);
    if (cfg.distThresholds) Object.assign(DIST_THRESHOLDS, cfg.distThresholds);
    if (cfg.maxHopDist != null) window.MAX_HOP_DIST = cfg.maxHopDist;
    if (cfg.limits) Object.assign(LIMITS, cfg.limits);
    if (cfg.perfSlowMs != null) window.PERF_SLOW_MS = cfg.perfSlowMs;
    if (cfg.wsReconnectMs != null) window.WS_RECONNECT_MS = cfg.wsReconnectMs;
    if (cfg.cacheInvalidateMs != null) window.CACHE_INVALIDATE_MS = cfg.cacheInvalidateMs;
    if (cfg.externalUrls) Object.assign(EXTERNAL_URLS, cfg.externalUrls);
    if (cfg.propagationBufferMs != null) window.PROPAGATION_BUFFER_MS = cfg.propagationBufferMs;
    // #1508 — expose operator-side customizer knobs to customize-v2.js.
    // Default to an empty disabledTabs list so the .indexOf() guard in
    // _renderTabs is a no-op when the field is absent.
    window.MC_CUSTOMIZER_CFG = (cfg.customizer && typeof cfg.customizer === 'object')
      ? { disabledTabs: Array.isArray(cfg.customizer.disabledTabs) ? cfg.customizer.disabledTabs : [] }
      : { disabledTabs: [] };
    // #1574 — operator-configurable cap on /live map node count.
    if (cfg.liveMapMaxNodes != null) window.LIVE_MAP_MAX_NODES = cfg.liveMapMaxNodes;
    // Sync ROLE_STYLE colors with ROLE_COLORS
    // #1407 — both are now live getters; no manual sync needed. Kept as no-op for clarity.
  }).catch(function () { /* use defaults */ });

  // ─── Built-in IATA airport code → city name mapping ───
  window.IATA_CITIES = {
    // United States
    'SEA': 'Seattle, WA',
    'SFO': 'San Francisco, CA',
    'PDX': 'Portland, OR',
    'LAX': 'Los Angeles, CA',
    'DEN': 'Denver, CO',
    'SLC': 'Salt Lake City, UT',
    'PHX': 'Phoenix, AZ',
    'DFW': 'Dallas, TX',
    'ATL': 'Atlanta, GA',
    'ORD': 'Chicago, IL',
    'JFK': 'New York, NY',
    'LGA': 'New York, NY',
    'BOS': 'Boston, MA',
    'MIA': 'Miami, FL',
    'FLL': 'Fort Lauderdale, FL',
    'IAH': 'Houston, TX',
    'HOU': 'Houston, TX',
    'MSP': 'Minneapolis, MN',
    'DTW': 'Detroit, MI',
    'CLT': 'Charlotte, NC',
    'EWR': 'Newark, NJ',
    'IAD': 'Washington, DC',
    'DCA': 'Washington, DC',
    'BWI': 'Baltimore, MD',
    'LAS': 'Las Vegas, NV',
    'MCO': 'Orlando, FL',
    'TPA': 'Tampa, FL',
    'BNA': 'Nashville, TN',
    'AUS': 'Austin, TX',
    'SAT': 'San Antonio, TX',
    'RDU': 'Raleigh, NC',
    'SAN': 'San Diego, CA',
    'OAK': 'Oakland, CA',
    'SJC': 'San Jose, CA',
    'SMF': 'Sacramento, CA',
    'PHL': 'Philadelphia, PA',
    'PIT': 'Pittsburgh, PA',
    'CLE': 'Cleveland, OH',
    'CMH': 'Columbus, OH',
    'CVG': 'Cincinnati, OH',
    'IND': 'Indianapolis, IN',
    'MCI': 'Kansas City, MO',
    'STL': 'St. Louis, MO',
    'MSY': 'New Orleans, LA',
    'MEM': 'Memphis, TN',
    'SDF': 'Louisville, KY',
    'JAX': 'Jacksonville, FL',
    'RIC': 'Richmond, VA',
    'ORF': 'Norfolk, VA',
    'BDL': 'Hartford, CT',
    'PVD': 'Providence, RI',
    'ABQ': 'Albuquerque, NM',
    'OKC': 'Oklahoma City, OK',
    'TUL': 'Tulsa, OK',
    'OMA': 'Omaha, NE',
    'BOI': 'Boise, ID',
    'GEG': 'Spokane, WA',
    'ANC': 'Anchorage, AK',
    'HNL': 'Honolulu, HI',
    'OGG': 'Maui, HI',
    'BUF': 'Buffalo, NY',
    'SYR': 'Syracuse, NY',
    'ROC': 'Rochester, NY',
    'ALB': 'Albany, NY',
    'BTV': 'Burlington, VT',
    'PWM': 'Portland, ME',
    'MKE': 'Milwaukee, WI',
    'DSM': 'Des Moines, IA',
    'LIT': 'Little Rock, AR',
    'BHM': 'Birmingham, AL',
    'CHS': 'Charleston, SC',
    'SAV': 'Savannah, GA',
    // Canada
    'YVR': 'Vancouver, BC',
    'YYZ': 'Toronto, ON',
    'YUL': 'Montreal, QC',
    'YOW': 'Ottawa, ON',
    'YYC': 'Calgary, AB',
    'YEG': 'Edmonton, AB',
    'YWG': 'Winnipeg, MB',
    'YHZ': 'Halifax, NS',
    'YQB': 'Quebec City, QC',
    // Europe
    'LHR': 'London, UK',
    'LGW': 'London, UK',
    'STN': 'London, UK',
    'CDG': 'Paris, FR',
    'ORY': 'Paris, FR',
    'FRA': 'Frankfurt, DE',
    'MUC': 'Munich, DE',
    'BER': 'Berlin, DE',
    'AMS': 'Amsterdam, NL',
    'MAD': 'Madrid, ES',
    'BCN': 'Barcelona, ES',
    'FCO': 'Rome, IT',
    'MXP': 'Milan, IT',
    'ZRH': 'Zurich, CH',
    'GVA': 'Geneva, CH',
    'VIE': 'Vienna, AT',
    'CPH': 'Copenhagen, DK',
    'ARN': 'Stockholm, SE',
    'OSL': 'Oslo, NO',
    'HEL': 'Helsinki, FI',
    'DUB': 'Dublin, IE',
    'LIS': 'Lisbon, PT',
    'ATH': 'Athens, GR',
    'IST': 'Istanbul, TR',
    'WAW': 'Warsaw, PL',
    'PRG': 'Prague, CZ',
    'BUD': 'Budapest, HU',
    'OTP': 'Bucharest, RO',
    'SOF': 'Sofia, BG',
    'ZAG': 'Zagreb, HR',
    'BEG': 'Belgrade, RS',
    'KBP': 'Kyiv, UA',
    'LED': 'St. Petersburg, RU',
    'SVO': 'Moscow, RU',
    'BRU': 'Brussels, BE',
    'EDI': 'Edinburgh, UK',
    'MAN': 'Manchester, UK',
    // Asia
    'NRT': 'Tokyo, JP',
    'HND': 'Tokyo, JP',
    'KIX': 'Osaka, JP',
    'ICN': 'Seoul, KR',
    'PEK': 'Beijing, CN',
    'PVG': 'Shanghai, CN',
    'HKG': 'Hong Kong',
    'TPE': 'Taipei, TW',
    'SIN': 'Singapore',
    'BKK': 'Bangkok, TH',
    'KUL': 'Kuala Lumpur, MY',
    'CGK': 'Jakarta, ID',
    'MNL': 'Manila, PH',
    'DEL': 'New Delhi, IN',
    'BOM': 'Mumbai, IN',
    'BLR': 'Bangalore, IN',
    'CCU': 'Kolkata, IN',
    'SGN': 'Ho Chi Minh City, VN',
    'HAN': 'Hanoi, VN',
    'DOH': 'Doha, QA',
    'DXB': 'Dubai, AE',
    'AUH': 'Abu Dhabi, AE',
    'TLV': 'Tel Aviv, IL',
    // Oceania
    'SYD': 'Sydney, AU',
    'MEL': 'Melbourne, AU',
    'BNE': 'Brisbane, AU',
    'PER': 'Perth, AU',
    'AKL': 'Auckland, NZ',
    'WLG': 'Wellington, NZ',
    'CHC': 'Christchurch, NZ',
    // South America
    'GRU': 'São Paulo, BR',
    'GIG': 'Rio de Janeiro, BR',
    'EZE': 'Buenos Aires, AR',
    'SCL': 'Santiago, CL',
    'BOG': 'Bogota, CO',
    'LIM': 'Lima, PE',
    'UIO': 'Quito, EC',
    'CCS': 'Caracas, VE',
    'MVD': 'Montevideo, UY',
    // Africa
    'JNB': 'Johannesburg, ZA',
    'CPT': 'Cape Town, ZA',
    'CAI': 'Cairo, EG',
    'NBO': 'Nairobi, KE',
    'ADD': 'Addis Ababa, ET',
    'CMN': 'Casablanca, MA',
    'LOS': 'Lagos, NG'
  };

  // Copy text to clipboard with fallback for Firefox and older browsers
  window.copyToClipboard = function(text, onSuccess, onFail) {
    function fallback() {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.left = '-9999px';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      try {
        var ok = document.execCommand('copy');
        document.body.removeChild(ta);
        if (ok && onSuccess) onSuccess();
        else if (!ok && onFail) onFail();
      } catch (e) {
        document.body.removeChild(ta);
        if (onFail) onFail();
      }
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(
        function() { if (onSuccess) onSuccess(); },
        function() { fallback(); }
      );
    } else {
      fallback();
    }
  };

  // Simple markdown → HTML (bold, italic, links, code, lists, line breaks)
  window.miniMarkdown = function(text) {
    if (!text) return '';
    var html = text
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
      .replace(/\*(.+?)\*/g, '<em>$1</em>')
      .replace(/`(.+?)`/g, '<code>$1</code>')
      .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener" style="color:var(--accent)">$1</a>')
      .replace(/^- (.+)/gm, '<li>$1</li>')
      .replace(/\n/g, '<br>');
    // Wrap consecutive <li> in <ul>
    html = html.replace(/((?:<li>.*?<\/li><br>?)+)/g, function(m) {
      return '<ul>' + m.replace(/<br>/g, '') + '</ul>';
    });
    return html;
  };

  // #690 — Clock Skew shared helpers
  var SKEW_SEVERITY_COLORS = {
    ok: 'var(--status-green)',
    warning: 'var(--status-yellow)',
    critical: 'var(--status-orange)',
    absurd: 'var(--status-purple)',
    bimodal_clock: 'var(--status-amber)',
    no_clock: 'var(--text-muted)'
  };
  var SKEW_SEVERITY_LABELS = {
    ok: 'OK', warning: 'Warning', critical: 'Critical', absurd: 'Absurd', bimodal_clock: 'Bimodal', no_clock: 'No Clock'
  };
  var SKEW_SEVERITY_ORDER = { no_clock: 0, bimodal_clock: 1, absurd: 2, critical: 3, warning: 4, ok: 5 };

  window.SKEW_SEVERITY_COLORS = SKEW_SEVERITY_COLORS;
  window.SKEW_SEVERITY_LABELS = SKEW_SEVERITY_LABELS;
  window.SKEW_SEVERITY_ORDER = SKEW_SEVERITY_ORDER;

  /** Format skew seconds into human-readable string like "+2m 34s" or "-15h 22m" */
  window.formatSkew = function(sec) {
    if (sec == null) return '—';
    var abs = Math.abs(sec);
    var sign = sec >= 0 ? '+' : '-';
    if (abs < 60) return sign + Math.round(abs) + 's';
    if (abs < 3600) return sign + Math.floor(abs / 60) + 'm ' + Math.round(abs % 60) + 's';
    if (abs < 86400) return sign + Math.floor(abs / 3600) + 'h ' + Math.round((abs % 3600) / 60) + 'm';
    return sign + Math.floor(abs / 86400) + 'd ' + Math.round((abs % 86400) / 3600) + 'h';
  };

  /** Format drift rate as "+X.Xs/day" or "—" if falsy */
  window.formatDrift = function(secPerDay) {
    if (!secPerDay) return '—';
    return (secPerDay >= 0 ? '+' : '') + secPerDay.toFixed(1) + ' s/day';
  };

  /** Pick the skew value that drives current-health UI: prefer the
   *  recent-window median (#789, current health) over the all-time median
   *  (poisoned by historical bad samples). Falls back gracefully if the
   *  field isn't present (older API responses). */
  window.currentSkewValue = function(cs) {
    if (!cs) return null;
    return cs.recentMedianSkewSec != null ? cs.recentMedianSkewSec : cs.medianSkewSec;
  };

  /** Render a clock skew badge HTML */
  window.renderSkewBadge = function(severity, skewSec, cs) {
    if (!severity) return '';
    var cls = 'skew-badge skew-badge--' + severity;
    if (severity === 'no_clock') {
      return '<span class="' + cls + '" title="Uninitialized RTC — no valid clock"><svg class="ph-icon" aria-hidden="true" focusable="false"><use href="/icons/phosphor-sprite.svg#ph-prohibit"/></svg> No Clock</span>';
    }
    if (severity === 'bimodal_clock' && cs) {
      var badPct = cs.goodFraction != null ? Math.round((1 - cs.goodFraction) * 100) : '?';
      var label = '⏰ ' + window.formatSkew(skewSec);
      return '<span class="' + cls + '" title="Clock skew: ' + window.formatSkew(skewSec) + ' (bimodal: ' + badPct + '% of recent adverts have nonsense timestamps)">' + label + '</span>';
    }
    var label = severity === 'ok' ? '⏰' : '⏰ ' + window.formatSkew(skewSec);
    return '<span class="' + cls + '" title="Clock skew: ' + window.formatSkew(skewSec) + ' (' + (SKEW_SEVERITY_LABELS[severity] || severity) + ')">' + label + '</span>';
  };

  /** Compute severity for an observer's clock offset (seconds). */
  window.observerSkewSeverity = function(offsetSec) {
    var abs = Math.abs(offsetSec);
    return abs >= 3600 ? 'critical' : abs >= 300 ? 'warning' : 'ok';
  };

  /** Render a skew sparkline SVG (inline, word-sized) */
  window.renderSkewSparkline = function(samples, w, h) {
    w = w || 120; h = h || 24;
    if (!samples || samples.length < 2) return '';
    var values = samples.map(function(s) { return s.skew; });
    var max = Math.max.apply(null, values.map(function(v) { return Math.abs(v); }).concat([1]));
    var pts = values.map(function(v, i) {
      var x = i * (w / Math.max(values.length - 1, 1));
      var y = h / 2 - (v / max) * (h / 2 - 2);
      return x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
    // Zero line
    var zeroY = h / 2;
    return '<svg viewBox="0 0 ' + w + ' ' + h + '" style="width:' + w + 'px;height:' + h + 'px" role="img" aria-label="Clock skew sparkline">' +
      '<title>Clock skew over time</title>' +
      '<line x1="0" y1="' + zeroY + '" x2="' + w + '" y2="' + zeroY + '" stroke="var(--border)" stroke-width="0.5" stroke-dasharray="2"/>' +
      '<polyline points="' + pts + '" fill="none" stroke="var(--accent)" stroke-width="1.5"/></svg>';
  };
})();
