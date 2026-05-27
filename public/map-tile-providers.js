/* map-tile-providers.js — Dark-tile provider registry & runtime switcher (#1420).
 *
 * Scope:
 *   - 4 providers: carto-dark (default), esri-darkgray-labels (base+ref),
 *     voyager-inverted, positron-inverted (CSS-filter variants).
 *   - MC_setDarkTileProvider(id) persists per-browser to localStorage and
 *     dispatches `mc-tile-provider-changed` so map.js / live.js can swap.
 *   - MC_getDarkTileProvider() resolves localStorage → server default →
 *     'carto-dark'.
 *   - MC_applyTileFilter() applies/clears the CSS filter on
 *     `.leaflet-tile-pane` based on current theme + selected provider.
 *
 * No new deps — URL-only providers. Light mode is unchanged.
 */
(function () {
  'use strict';

  var STORAGE_KEY  = 'mc-dark-tile-provider';
  var DEFAULT_ID   = 'carto-dark';
  var EVENT_NAME   = 'mc-tile-provider-changed';
  var INVERT_CSS   = 'invert(1) hue-rotate(180deg) brightness(0.9) contrast(1.05)';

  // Per-browser server-injected default. roles.js writes this from
  // /api/config/client (cfg.mapDarkTileProvider) before any consumer reads.
  var _serverDefault = null;

  // ── Registry ────────────────────────────────────────────────────────────
  var REGISTRY = {
    'carto-dark': {
      label: 'Carto Dark (default)',
      url: 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png',
      attribution: '© OpenStreetMap © CartoDB',
      invertFilter: null
    },
    'esri-darkgray-labels': {
      label: 'Esri Dark Gray + Labels',
      // Two-layer provider: base + reference (labels) overlay.
      baseUrl: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Base/MapServer/tile/{z}/{y}/{x}',
      refUrl:  'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Reference/MapServer/tile/{z}/{y}/{x}',
      attribution: 'Tiles © Esri — Esri, DeLorme, NAVTEQ',
      invertFilter: null
    },
    'voyager-inverted': {
      label: 'Carto Voyager (inverted)',
      url: 'https://{s}.basemaps.cartocdn.com/rastertiles/voyager/{z}/{x}/{y}{r}.png',
      attribution: '© OpenStreetMap © CartoDB',
      invertFilter: INVERT_CSS
    },
    'positron-inverted': {
      label: 'Carto Positron (inverted)',
      url: 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png',
      attribution: '© OpenStreetMap © CartoDB',
      invertFilter: INVERT_CSS
    }
  };

  function _hasId(id) {
    return typeof id === 'string' && Object.prototype.hasOwnProperty.call(REGISTRY, id);
  }

  function _isDark() {
    try {
      var attr = document.documentElement.getAttribute('data-theme');
      if (attr === 'dark') return true;
      if (attr === 'light') return false;
      return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
    } catch (_) { return false; }
  }

  function getActiveId() {
    try {
      var stored = window.localStorage && window.localStorage.getItem(STORAGE_KEY);
      if (_hasId(stored)) return stored;
    } catch (_) { /* localStorage may be disabled */ }
    if (_hasId(_serverDefault)) return _serverDefault;
    return DEFAULT_ID;
  }

  function setActive(id) {
    if (!_hasId(id)) return false;
    try {
      if (window.localStorage) window.localStorage.setItem(STORAGE_KEY, id);
    } catch (_) { /* swallow quota / disabled */ }
    var detail = { id: id, provider: REGISTRY[id] };
    try {
      var ev = (typeof CustomEvent === 'function')
        ? new CustomEvent(EVENT_NAME, { detail: detail })
        : { type: EVENT_NAME, detail: detail };
      window.dispatchEvent(ev);
    } catch (_) { /* dispatch optional */ }
    // Re-apply filter immediately so callers without a listener still see it.
    applyTileFilter();
    return true;
  }

  function setServerDefault(id) {
    if (_hasId(id)) _serverDefault = id;
  }

  function applyTileFilter() {
    var pane;
    try { pane = document.querySelector('.leaflet-tile-pane'); } catch (_) { pane = null; }
    if (!pane || !pane.style) return;
    if (!_isDark()) { pane.style.filter = ''; return; }
    var id = getActiveId();
    var p  = REGISTRY[id];
    pane.style.filter = (p && p.invertFilter) ? p.invertFilter : '';
  }

  // ── Public surface ──────────────────────────────────────────────────────
  window.MC_TILE_PROVIDERS              = REGISTRY;
  window.MC_DARK_TILE_DEFAULT           = DEFAULT_ID;
  window.MC_setDarkTileProvider         = setActive;
  window.MC_getDarkTileProvider         = getActiveId;
  window.MC_setServerDefaultTileProvider = setServerDefault;
  window.MC_applyTileFilter             = applyTileFilter;
})();
