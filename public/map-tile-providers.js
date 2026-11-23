/* map-tile-providers.js — Registry of map tile providers & runtime switcher (#1165/#1420).
 *
 * Scope:
 *   - Multiple providers: Carto (default), OSM, Stamen, Esri.
 *   - Carto requires an API key since 2026-08. `tiles.providers.carto.key` is
 *     appended as `?key=`; without it Carto returns watermarked tiles.
 *   - MC_setDarkTileProvider(id) / MC_setLightTileProvider(id) persist per-browser
 *     to localStorage and dispatch `mc-tile-provider-changed`.
 *   - Resolves localStorage → server default → 'carto-dark' / 'carto-light'.
 *   - MC_applyTileFilter(pane?) applies/clears the CSS filter on the given
 *     tile pane (default: the first `.leaflet-tile-pane`) based on current
 *     theme + selected provider.
 *   - MC_createLayerControl() builds a Leaflet control mapping providers.
 *   - MC_buildTileLayer() / MC_attachAutoTiles(): the shared basemap layer
 *     builder and theme-synced "Auto" group (raster, vector via map-vector.js;
 *     overlays incl. hillshade via map-hillshade.js; see CONTRACT).
 *
 * ── CONTRACT (#2428 map providers) ──────────────────────────────────────────
 * Additive only: every existing id, config key, default and public function
 * keeps its current behaviour. Carto stays the shipped default.
 *
 * Provider entry (BASE_STYLES, custom entries, window.MC_TILE_PROVIDERS[id]):
 *   provider      string   config group under map.tiles.providers (enable switch)
 *   label         string   picker text (plain text; escaped before display)
 *   type          'light' | 'dark' | 'any'   which theme picker lists it.
 *                 'any' (new) = listed under both, never colour-inverted
 *                 (satellite). getActiveId/getActiveLightId/setActive/
 *                 MC_resolveTile accept an 'any' entry for either theme.
 *   category      'street' | 'topo' | 'satellite'   (new, optional; default
 *                 'street') groups the picker.
 *   format        'raster' | 'vector'   (new, optional; default 'raster')
 *   url           string | () => string   raster: XYZ template; vector: style
 *                 JSON URL (MapLibre style spec)
 *   refUrl        string?  raster labels tileset stacked on the base (Esri)
 *   invertFilter  string?  CSS filter applied in dark mode (unchanged)
 *   attribution   string   plain text for new/custom entries (escaped)
 *   maxZoom       number   (default 19)
 *   maxNativeZoom number?  (new) highest zoom the server has; Leaflet
 *                 upsamples above it up to maxZoom
 *   subdomains    string?  (new) for {s}; default 'abc'
 *   optIn         boolean  (new) true = off unless the group's config says
 *                 enabled:true. Every provider added for #2428 is optIn, so
 *                 an upgrade changes nothing until the operator turns a group
 *                 on (licences and volunteer servers are the operator's call);
 *                 existing groups keep their current opt-in/opt-out rules.
 *
 * Overlay entry (new; window.MC_TILE_OVERLAYS[id]), stacked above any base:
 *   provider, label, attribution, maxZoom, maxNativeZoom, subdomains, optIn
 *   kind     'raster' | 'hillshade'
 *   url      raster: XYZ template; hillshade: Terrarium-encoded elevation PNG
 *            template (elevation m = R*256 + G + B/256 - 32768)
 *   opacity  number 0-1 (default 1; hillshade default 0.35)
 *   zIndex   number (default 250, above tiles 200, below overlayPane 400)
 *   Overlays are off until the viewer ticks them; the ticked set persists in
 *   localStorage 'mc-tile-overlays' (JSON array of ids).
 *
 * MC_resolveTile(isDark) descriptor gains: format, category, maxNativeZoom,
 *   subdomains (existing fields unchanged).
 *
 * New public functions (all synchronous unless noted):
 *   MC_buildTileLayer(desc, opts?) -> L.Layer
 *       desc: a provider entry or an MC_resolveTile descriptor. Raster ->
 *       L.tileLayer (grouped with refUrl when present). Vector -> an
 *       L.LayerGroup that mounts L.maplibreGL once MC_loadMapLibre()
 *       resolves; on load failure or no WebGL it mounts the raster entry
 *       opts.fallbackId (default: the theme's shipped Carto id) instead.
 *   MC_buildOverlayLayer(overlay) -> L.Layer   raster or hillshade overlay.
 *   MC_createHillshadeLayer({url, opacity, maxNativeZoom, ...}) -> L.GridLayer
 *       canvas tiles shaded client-side from Terrarium elevation tiles.
 *   MC_loadMapLibre() -> Promise   injects the vendored maplibre-gl scripts and
 *       CSS once; resolves with window.maplibregl. Only vector layers call it.
 *   MC_attachAutoTiles(map, {controlPosition?}) -> { group, sync, destroy }
 *       The one theme-synced basemap helper every map surface uses: owns the
 *       "Auto" layer group, resolves via MC_resolveTile(MC_isDarkTheme()),
 *       re-syncs on 'mc-tile-provider-changed' and data-theme changes, and
 *       adds MC_createLayerControl when controlPosition is given.
 *
 * Operator config, map.tiles:
 *   providers.<group>.enabled   unchanged; new groups: eox, osmfr,
 *                               openfreemap, versatiles, hillshade (all
 *                               optIn: on only with enabled:true)
 *   providers.hillshade.url     elevation tiles; default AWS Open Data
 *                               terrarium (the planner's terrain bucket)
 *   custom: [ { id, label, type, category, format, url, attribution,
 *               maxZoom, maxNativeZoom, subdomains, overlay } ]
 *     id      /^[a-z0-9][a-z0-9-]{0,31}$/; registered as 'custom-<id>'
 *     url     'https://' or 'http://' absolute, or a same-site path starting
 *             with a single '/'. Anything else rejects the entry.
 *     overlay true registers a raster overlay instead of a base provider.
 *     At most 20 entries; invalid entries are skipped with one console.warn.
 *   darkDefault / lightDefault may name any registered id, incl. custom-*.
 *
 * Security requirements (threat model, #2428):
 *   S1 attribution of custom/overlay entries is plain text, HTML-escaped ONCE
 *      at registration (MC_initTileRegistry) and stored escaped, so every
 *      consumer handing it to Leaflet stays safe unchanged.
 *   S2 picker labels are set with textContent, never innerHTML; overlays are
 *      handed to L.control.layers keyed by id and relabelled the same way.
 *   S3 one _validTileUrl(s): ^https?:// with an authority free of '@', '\',
 *      whitespace and control chars, OR a path matching ^/(?![/\\]). Only the
 *      placeholders {z} {x} {y} {s} {r} {-y} are allowed. Custom fields are
 *      copied from a whitelist with type/range checks (label string <= 64
 *      chars; maxZoom/maxNativeZoom finite, clamped 0-24; subdomains
 *      ^[a-z0-9]{1,8}$; enums checked); never Object.assign the config.
 *      Rejections console.warn once quoting the entry id only (URLs may
 *      carry keys).
 *   S4 MapLibre: build maplibregl.Map options explicitly (style,
 *      transformRequest, attributionControl:false); never setRTLTextPlugin;
 *      transformRequest refuses non-http(s)/relative URLs and http: on an
 *      https page. Attribution shows only through Leaflet (S1).
 *   S5 MapLibre 5.24.0 (CSP build) + @maplibre/maplibre-gl-leaflet 0.1.4,
 *      byte-identical npm copies in public/vendor/<name>-<version>/.
 *      MC_loadMapLibre uses constant paths, sets integrity (sha384) on the
 *      injected tags, calls maplibregl.setWorkerUrl on the CSP worker, caches
 *      its promise, and rejects after 15 s -> raster fallback.
 *   S6 hillshade: img.crossOrigin='anonymous' before src; getImageData in
 *      try/catch (SecurityError -> blank tile, warn once, layer marked
 *      failed); refuse images that are not 256x256 or 512x512.
 *   S7 localStorage 'mc-tile-overlays' and provider picks are untrusted:
 *      parse in try/catch, keep only registered ids. WebGL missing or
 *      context lost -> fallbackId raster.
 *   Everything under map.tiles is public (served to anonymous visitors).
 *
 * Sprint 3 additions (gray basemaps, overlays, vector):
 *   Entry field `filter` (string, built-ins only, never from config): a CSS
 *     filter applied to the tile pane in BOTH themes whenever this entry is
 *     shown (e.g. 'grayscale(1)'). It replaces invertFilter for that entry:
 *     MC_resolveTile returns it as desc.invertFilter (so every consumer that
 *     applies desc.invertFilter keeps working), MC_applyTileFilter and the
 *     picker's explicit-layer handler apply it regardless of theme.
 *   Entry field `style: 'gray'` (optional tag) marks gray basemaps; the
 *     picker shows them inside their category after the plain entries of
 *     the same type. No new `type` value.
 *   New groups, all optIn: openfreemap (vector), versatiles (vector),
 *     and the raster gray pair lives in osmfr.
 *   Split across files (each sets window globals; loaded after this file):
 *     map-hillshade.js  MC_createHillshadeLayer(opts) -> L.GridLayer
 *         opts: { url, opacity=0.35, maxZoom=19, maxNativeZoom=15,
 *                 attribution, zIndex=250 }
 *         also window.MC_hillshadeShade(elev: Float32Array, w, h, zoom, lat,
 *         out: Uint8ClampedArray) — the pure shading kernel (testable).
 *     map-vector.js     MC_loadMapLibre() -> Promise<maplibregl>
 *         MC_buildVectorLayer(desc, { fallback: () => L.Layer }) ->
 *         L.LayerGroup. Mounts L.maplibreGL({style: desc.url, ...S4}) into
 *         the group when the library loads; on load failure, 15 s timeout,
 *         no WebGL or context loss it adds fallback() to the group instead
 *         (and removes the GL layer). desc.attribution is shown by Leaflet.
 *     This file: MC_buildTileLayer calls MC_buildVectorLayer for format
 *     'vector' when it exists (else the raster fallback, as today);
 *     MC_buildOverlayLayer calls MC_createHillshadeLayer for kind
 *     'hillshade' when it exists (else returns null and the overlay is not
 *     listed).
 *
 * Fix-up additions (review findings, #2428):
 *   MC_buildTileLayer(desc, { raster: true }) never loads MapLibre: a vector
 *     desc builds its raster fallback directly. Every static/secondary map
 *     (node detail/reach/RX coverage via _applyTilesToNodeMap, analytics,
 *     home mini-map, customizer previews) passes { raster: true }; only the
 *     full map pages (map, live, planner via MC_attachAutoTiles) render
 *     vector maps.
 *   Static maps that are re-created on re-render keep a reference and
 *     remove() the previous Leaflet map first (no orphaned maps).
 */
(function () {
  'use strict';

  var STORAGE_KEY  = 'mc-dark-tile-provider';
  var STORAGE_KEY_LIGHT = 'mc-light-tile-provider';

  var DEFAULT_ID   = 'carto-dark';
  var DEFAULT_ID_LIGHT = 'carto-light';
  var EVENT_NAME   = 'mc-tile-provider-changed';
  // A map pinned in a layer picker (any theme), or absent for Auto. Untrusted:
  // honoured only while it names a registered entry.
  var PIN_KEY      = 'mc-tile-pinned';
  var _pinMemo     = null;   // last value written, for when localStorage throws
  var _pinUnsaved  = false;  // the last write failed: localStorage holds an older pin
  
  var _serverDefault = null;
  var _serverDefaultLight = null;
  
  var INVERT_CSS   = 'invert(1) hue-rotate(180deg) brightness(0.9) contrast(1.05)';

  var _cfg = null;
  var _DEFAULT_MAX_ZOOM = 19; // contract default maxZoom (also used by MC_resolveTile)

  var _getCartoBase = function() { return (_cfg && _cfg.providers && _cfg.providers.carto && _cfg.providers.carto.domain) ? 'https://{s}.' + _cfg.providers.carto.domain + '.cartocdn.com' : 'https://{s}.basemaps.cartocdn.com'; };
  // Carto started requiring an API key in 2026-08: unauthenticated tiles come back
  // stamped "API KEY REQUIRED". The key is a query param on the same host, so it is
  // appended to every Carto style URL. Free keys: https://carto.com/basemaps/apikey
  // NOTE: like the OSM/Stamen tokens above, this reaches the browser - restrict it
  // by domain in the Carto dashboard.
  var _getCartoKey = function() { return (_cfg && _cfg.providers && _cfg.providers.carto && _cfg.providers.carto.key) ? '?key=' + encodeURIComponent(_cfg.providers.carto.key) : ''; };
  var _getStamenUrl = function() { return 'https://tiles.stadiamaps.com/tiles/stamen_toner_lite/{z}/{x}/{y}{r}.png' + ((_cfg && _cfg.providers && _cfg.providers.stamen && _cfg.providers.stamen.token) ? '?api_key=' + encodeURIComponent(_cfg.providers.stamen.token) : ''); };
  var _getOsmUrl = function() {
    if (_cfg && _cfg.providers && _cfg.providers.osm && _cfg.providers.osm.provider && _cfg.providers.osm.token) {
      var prov = _cfg.providers.osm.provider.toLowerCase();
      var key = encodeURIComponent(_cfg.providers.osm.token);
      if (prov === 'thunderforest') return 'https://{s}.tile.thunderforest.com/osm-carto/{z}/{x}/{y}.png?apikey=' + key;
      if (prov === 'maptiler') return 'https://api.maptiler.com/maps/streets/{z}/{x}/{y}.png?key=' + key;
      if (prov === 'mapbox') return 'https://api.mapbox.com/styles/v1/mapbox/streets-v11/tiles/256/{z}/{x}/{y}@2x?access_token=' + key;
    }
    return 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png';
  };

  // Keyless vector built-ins (optIn). Attribution is plain text (S1).
  var _VECTOR_ATTRIBUTION = {
    openfreemap: 'OpenFreeMap © OpenMapTiles Data from OpenStreetMap',
    versatiles: '© OpenStreetMap contributors, VersaTiles'
  };
  function _vector(group, label, type, style, url) {
    var e = { provider: group, optIn: true, label: label, category: 'street', type: type, format: 'vector',
      invertFilter: null, url: url, attribution: _VECTOR_ATTRIBUTION[group], maxZoom: 19 };
    if (style) e.style = style;
    return e;
  }
  function _versatiles(name) { return 'https://tiles.versatiles.org/assets/styles/' + name + '/style.json'; }

  var BASE_STYLES = {
    'carto-dark': { provider: 'carto', style: 'gray', label: 'Carto Dark', url: function() { return _getCartoBase() + '/dark_all/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: null, type: 'dark', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'carto-light': { provider: 'carto', style: 'gray', label: 'Carto Positron', url: function() { return _getCartoBase() + '/light_all/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: null, type: 'light', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'carto-voyager': { provider: 'carto', label: 'Carto Voyager', url: function() { return _getCartoBase() + '/rastertiles/voyager/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: null, type: 'light', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'carto-voyager-dark': { provider: 'carto', label: 'Carto Voyager (inverted)', url: function() { return _getCartoBase() + '/rastertiles/voyager/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: INVERT_CSS, type: 'dark', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'positron-dark': { provider: 'carto', style: 'gray', label: 'Carto Positron (inverted)', url: function() { return _getCartoBase() + '/light_all/{z}/{x}/{y}{r}.png' + _getCartoKey(); }, invertFilter: INVERT_CSS, type: 'dark', attribution: '© OpenStreetMap © CartoDB', maxZoom: 19 },
    'osm-standard': { provider: 'osm', label: 'OSM Standard', url: _getOsmUrl, invertFilter: null, type: 'light', attribution: '© OpenStreetMap contributors, Maps © Mapbox/Thunderforest/MapTiler', maxZoom: 18 },
    'osm-dark': { provider: 'osm', label: 'OSM Standard (inverted)', url: _getOsmUrl, invertFilter: INVERT_CSS, type: 'dark', attribution: '© OpenStreetMap contributors, Maps © Mapbox/Thunderforest/MapTiler', maxZoom: 18 },
    'opentopomap': { provider: 'opentopomap', label: 'OpenTopoMap', url: function() { return 'https://{s}.tile.opentopomap.org/{z}/{x}/{y}.png'; }, invertFilter: null, type: 'light', category: 'topo', attribution: '© OpenStreetMap contributors, SRTM, © OpenTopoMap (CC-BY-SA)', maxZoom: 17 },
    'stamen-toner-lite': { provider: 'stamen', label: 'Stamen Toner Lite', url: _getStamenUrl, invertFilter: null, type: 'light', attribution: '© Stadia Maps © Stamen Design © OpenStreetMap', maxZoom: 20 },
    'stamen-toner-dark': { provider: 'stamen', label: 'Stamen Toner Lite (inverted)', url: _getStamenUrl, invertFilter: INVERT_CSS, type: 'dark', attribution: '© Stadia Maps © Stamen Design © OpenStreetMap', maxZoom: 20 },
    'usgs-topo': { provider: 'usgs', label: 'USGS Topographic', url: function() { return 'https://basemap.nationalmap.gov/arcgis/rest/services/USGSTopo/MapServer/tile/{z}/{y}/{x}'; }, invertFilter: null, type: 'light', category: 'topo', attribution: 'U.S. Geological Survey, National Geospatial Program', maxZoom: 16 },
    'usgs-imagery': { provider: 'usgs', label: 'USGS Imagery', url: function() { return 'https://basemap.nationalmap.gov/arcgis/rest/services/USGSImageryTopo/MapServer/tile/{z}/{y}/{x}'; }, invertFilter: null, type: 'light', category: 'satellite', attribution: 'U.S. Geological Survey, National Geospatial Program', maxZoom: 16 },
    'esri-darkgray-labels': { provider: 'esri', style: 'gray', label: 'Esri Dark Gray Canvas', url: function() { return 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Base/MapServer/tile/{z}/{y}/{x}'; }, refUrl: 'https://server.arcgisonline.com/ArcGIS/rest/services/Canvas/World_Dark_Gray_Reference/MapServer/tile/{z}/{y}/{x}', invertFilter: null, type: 'dark', attribution: 'Tiles © Esri', maxZoom: 19 },

    // ── #2428 keyless built-ins: optIn (off unless the group says enabled:true) ──
    // Their attribution is plain text stored HTML-escaped (S1), hence '&amp;'.
    // maxNativeZoom is the deepest level with real detail, measured against EOX
    // (z+1 tiles compared with their upsampled parent): Sentinel-2 is 10 m
    // imagery, so deeper EOX levels are resampled and Leaflet upsamples instead.
    'eox-s2-2025': { provider: 'eox', optIn: true, label: 'Satellite 2025 (EOX Sentinel-2)', category: 'satellite', type: 'any', invertFilter: null,
      url: 'https://tiles.maps.eox.at/wmts/1.0.0/s2cloudless-2025_3857/default/g/{z}/{y}/{x}.jpg',
      attribution: 'Sentinel-2 cloudless 2025 - s2maps.eu by EOX IT Services GmbH (Contains modified Copernicus Sentinel data 2025), CC BY-NC-SA 4.0',
      maxZoom: 19, maxNativeZoom: 15 },
    'eox-s2-2016': { provider: 'eox', optIn: true, label: 'Satellite 2016 (EOX Sentinel-2)', category: 'satellite', type: 'any', invertFilter: null,
      url: 'https://tiles.maps.eox.at/wmts/1.0.0/s2cloudless_3857/default/g/{z}/{y}/{x}.jpg',
      attribution: 'Sentinel-2 cloudless - s2maps.eu by EOX IT Services GmbH (Contains modified Copernicus Sentinel data 2016 &amp; 2017), CC BY 4.0',
      maxZoom: 19, maxNativeZoom: 14 },
    'eox-terrain-light': { provider: 'eox', optIn: true, label: 'Terrain Light (EOX)', category: 'topo', type: 'light', invertFilter: null,
      url: 'https://tiles.maps.eox.at/wmts/1.0.0/terrain-light_3857/default/g/{z}/{y}/{x}.jpg',
      attribution: 'Terrain Light: Data © OpenStreetMap contributors and others, Rendering © EOX',
      maxZoom: 19, maxNativeZoom: 15 },
    'osmfr-hot': { provider: 'osmfr', optIn: true, label: 'Humanitarian (OSM France)', category: 'street', type: 'light', invertFilter: null,
      url: 'https://{s}.tile.openstreetmap.fr/hot/{z}/{x}/{y}.png', subdomains: 'abc',
      attribution: '© OpenStreetMap contributors, style by Humanitarian OpenStreetMap Team, hosted by OpenStreetMap France',
      maxZoom: 19 },
    'osmfr-cyclosm': { provider: 'osmfr', optIn: true, label: 'CyclOSM (topographic)', category: 'topo', type: 'light', invertFilter: null,
      url: 'https://{s}.tile-cyclosm.openstreetmap.fr/cyclosm/{z}/{x}/{y}.png', subdomains: 'abc',
      attribution: 'CyclOSM, map data © OpenStreetMap contributors',
      maxZoom: 19 },

    // ── Sprint 3: gray rasters (OSM France tiles + a CSS `filter`, both themes) ──
    'osmfr-gray': { provider: 'osmfr', optIn: true, label: 'OSM Gray', category: 'street', type: 'light', style: 'gray', invertFilter: null,
      filter: 'grayscale(1) contrast(0.92) brightness(1.04)',
      url: 'https://{s}.tile.openstreetmap.fr/osmfr/{z}/{x}/{y}.png', subdomains: 'abc',
      attribution: '© OpenStreetMap contributors, tiles by OpenStreetMap France',
      maxZoom: 19 },
    'osmfr-gray-dark': { provider: 'osmfr', optIn: true, label: 'OSM Gray Dark', category: 'street', type: 'dark', style: 'gray', invertFilter: null,
      filter: 'grayscale(1) invert(1) contrast(0.9) brightness(0.95)',
      url: 'https://{s}.tile.openstreetmap.fr/osmfr/{z}/{x}/{y}.png', subdomains: 'abc',
      attribution: '© OpenStreetMap contributors, tiles by OpenStreetMap France',
      maxZoom: 19 },

    // ── Sprint 3: vector basemaps (url = MapLibre style JSON; see map-vector.js) ──
    'openfreemap-liberty': _vector('openfreemap', 'OpenFreeMap Liberty', 'light', null, 'https://tiles.openfreemap.org/styles/liberty'),
    'openfreemap-bright': _vector('openfreemap', 'OpenFreeMap Bright', 'light', null, 'https://tiles.openfreemap.org/styles/bright'),
    'openfreemap-positron': _vector('openfreemap', 'OpenFreeMap Positron', 'light', 'gray', 'https://tiles.openfreemap.org/styles/positron'),
    'openfreemap-dark': _vector('openfreemap', 'OpenFreeMap Dark', 'dark', 'gray', 'https://tiles.openfreemap.org/styles/dark'),
    'openfreemap-fiord': _vector('openfreemap', 'OpenFreeMap Fiord', 'dark', null, 'https://tiles.openfreemap.org/styles/fiord'),
    'versatiles-colorful': _vector('versatiles', 'VersaTiles Colorful', 'light', null, _versatiles('colorful')),
    'versatiles-muted': _vector('versatiles', 'VersaTiles Muted', 'light', null, _versatiles('muted')),
    'versatiles-gray': _vector('versatiles', 'VersaTiles Gray', 'light', 'gray', _versatiles('gray')),
    'versatiles-colorful-dark': _vector('versatiles', 'VersaTiles Colorful Dark', 'dark', null, _versatiles('colorful-dark')),
    'versatiles-gray-dark': _vector('versatiles', 'VersaTiles Gray Dark', 'dark', 'gray', _versatiles('gray-dark'))
  };

  var REGISTRY = {};

  // ── Overlays (CONTRACT "Overlay entry"), stacked above whichever base ──
  var OVERLAY_Z = 250;                 // pane z-index: above tiles (200), below overlayPane (400)
  var OVERLAY_PANE = 'mcOverlayPane';  // own pane, so the tile pane's invert/gray filter never reaches it
  var OVERLAYS_KEY = 'mc-tile-overlays';
  var OVERLAYS_EVENT = 'mc-tile-overlays-changed';
  var OVERLAYS_RAW_MAX = 4096;         // S7: a longer stored value is ignored unparsed
  var HILLSHADE_URL = 'https://s3.amazonaws.com/elevation-tiles-prod/terrarium/{z}/{x}/{y}.png';
  function _eoxOverlay(layer) { return 'https://tiles.maps.eox.at/wmts/1.0.0/' + layer + '/default/g/{z}/{y}/{x}.png'; }
  // EOX label overlays: transparent PNGs, served up to z14 (z15 is a 404).
  // overlay_bright draws light text with a dark halo (for imagery);
  // overlay draws dark text with a light halo (for light basemaps).
  var OVERLAY_STYLES = {
    'eox-labels': { provider: 'eox', optIn: true, kind: 'raster', label: 'Place labels (EOX)',
      url: _eoxOverlay('overlay_bright_3857'),
      attribution: 'Overlay: Data © OpenStreetMap contributors, Rendering © EOX',
      maxZoom: 19, maxNativeZoom: 14, opacity: 1, zIndex: OVERLAY_Z },
    'eox-labels-dark': { provider: 'eox', optIn: true, kind: 'raster', label: 'Place labels, dark text (EOX)',
      url: _eoxOverlay('overlay_3857'),
      attribution: 'Overlay: Data © OpenStreetMap contributors, Rendering © EOX',
      maxZoom: 19, maxNativeZoom: 14, opacity: 1, zIndex: OVERLAY_Z }
  };
  var OVERLAYS = {};
  var _customOverlays = {}; // the last registration's custom overlay entries
  var _hillshade = null;    // the last hillshade entry built

  function _hasId(id) {
    return typeof id === 'string' && Object.prototype.hasOwnProperty.call(REGISTRY, id);
  }

  // ── Operator custom entries (map.tiles.custom; contract + S1/S3) ──────────
  // map.tiles is served to anonymous visitors, so every value is validated
  // here and nothing reaches the registry unchecked.

  var CUSTOM_MAX = 20;
  var CUSTOM_ID_RE = /^[a-z0-9][a-z0-9-]{0,31}$/;
  var SUBDOMAINS_RE = /^[a-z0-9]{1,8}$/;
  var URL_MAX_LEN = 2048;
  var LABEL_MAX = 64;
  var ATTRIBUTION_MAX = 200;
  var ZOOM_MAX = 24;
  var PLACEHOLDERS = { '{z}': 1, '{x}': 1, '{y}': 1, '{s}': 1, '{r}': 1, '{-y}': 1 };
  var TYPES = { light: 1, dark: 1, any: 1 };
  var CATEGORIES = { street: 1, topo: 1, satellite: 1 };
  var FORMATS = { raster: 1, vector: 1 };

  // S3: the one URL gate. Absolute http(s) with a clean authority (no '@'
  // userinfo, no '\', no whitespace/control chars), or a same-site path that
  // cannot become protocol-relative ('//host', '/\host'). Whitespace and
  // control chars are refused anywhere in the URL, and every {...} must be a
  // Leaflet placeholder we support. Only {s} may appear in the authority:
  // Leaflet fills {r} with '@2x' (a{r}b.example -> userinfo 'a' at host
  // '2xb.example'), and {z}/{x}/{y}/{-y} would put tile numbers in the host.
  function _validTileUrl(s) {
    if (typeof s !== 'string' || !s || s.length > URL_MAX_LEN) return false;
    if (/[\s\u0000-\u001f\u007f-\u009f]/.test(s)) return false;
    var abs = /^https?:\/\/([^\/?#]*)/.exec(s);
    if (abs) {
      if (!abs[1] || /[@\\]/.test(abs[1])) return false;
      if (/[{}]/.test(abs[1].replace(/\{s\}/g, ''))) return false;
    } else if (!/^\/(?![\/\\])/.test(s)) {
      return false;
    }
    var rest = s.replace(/\{[^{}]*\}/g, function (m) { return _inSet(PLACEHOLDERS, m) ? '' : '\u0000'; });
    return !/[{}\u0000]/.test(rest);
  }

  function _hasXYZ(v) {
    return v.indexOf('{z}') >= 0 && v.indexOf('{x}') >= 0 && (v.indexOf('{y}') >= 0 || v.indexOf('{-y}') >= 0);
  }

  function _escapeHtml(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  // Own properties only: a config entry never lends us inherited values.
  function _own(o, k) {
    return Object.prototype.hasOwnProperty.call(o, k) ? o[k] : undefined;
  }

  // Set membership that 'constructor' or '__proto__' can't satisfy.
  function _inSet(set, v) {
    return typeof v === 'string' && Object.prototype.hasOwnProperty.call(set, v);
  }

  // Bounded: one entry per distinct message, and messages come from at most
  // CUSTOM_MAX entries per config. Re-registration (config arriving async)
  // does not repeat a warning.
  var _warned = {};
  var _warnedCount = 0;
  function _warnOnce(msg) {
    if (_warned[msg] || _warnedCount >= 64) return;
    _warned[msg] = true;
    _warnedCount++;
    try { console.warn('[tiles] ' + msg); } catch (_) {}
  }

  function _zoom(v) {
    if (typeof v !== 'number' || !isFinite(v)) return null;
    return Math.min(ZOOM_MAX, Math.max(0, v));
  }

  // One config entry -> a registry entry, or a string saying why it was
  // rejected. Fields are copied from a whitelist; absent (undefined/null)
  // optional fields take their defaults, present-but-invalid ones reject.
  function _customEntry(c, id) {
    var v;
    var e = { provider: 'custom', label: id, type: 'light', category: 'street', format: 'raster',
      url: null, attribution: '', maxZoom: _DEFAULT_MAX_ZOOM, invertFilter: null };

    v = _own(c, 'label');
    if (v != null) {
      if (typeof v !== 'string' || v.length > LABEL_MAX) return 'label must be text of at most ' + LABEL_MAX + ' characters';
      if (v.trim()) e.label = v;
    }
    v = _own(c, 'type');
    if (v != null) { if (!_inSet(TYPES, v)) return "type must be 'light', 'dark' or 'any'"; e.type = v; }
    v = _own(c, 'category');
    if (v != null) { if (!_inSet(CATEGORIES, v)) return "category must be 'street', 'topo' or 'satellite'"; e.category = v; }
    v = _own(c, 'format');
    if (v != null) { if (!_inSet(FORMATS, v)) return "format must be 'raster' or 'vector'"; e.format = v; }

    v = _own(c, 'url');
    if (!_validTileUrl(v)) return "url must be an https://, http:// or '/' URL using only {z} {x} {y} {s} {r} {-y}";
    if (e.format === 'raster' && !_hasXYZ(v)) return 'a raster url needs {z}, {x} and {y}';
    e.url = v;

    v = _own(c, 'attribution');
    if (v != null) {
      if (typeof v !== 'string' || v.length > ATTRIBUTION_MAX) return 'attribution must be text of at most ' + ATTRIBUTION_MAX + ' characters';
      e.attribution = _escapeHtml(v); // S1: escaped once, stored escaped
    }
    v = _own(c, 'maxZoom');
    if (v != null) { if (_zoom(v) === null) return 'maxZoom must be a number'; e.maxZoom = _zoom(v); }
    v = _own(c, 'maxNativeZoom');
    if (v != null) {
      if (_zoom(v) === null) return 'maxNativeZoom must be a number';
      e.maxNativeZoom = Math.floor(_zoom(v)); // Leaflet builds tile URLs from it: keep it whole
    }
    v = _own(c, 'subdomains');
    if (v != null) {
      if (typeof v !== 'string' || !SUBDOMAINS_RE.test(v)) return 'subdomains must be 1-8 characters of a-z and 0-9';
      e.subdomains = v;
    }
    return e;
  }

  // A validated custom entry -> a raster overlay entry (fields already checked).
  function _customOverlay(e) {
    var o = { provider: 'custom', kind: 'raster', label: e.label, url: e.url, attribution: e.attribution,
      maxZoom: e.maxZoom, opacity: 1, zIndex: OVERLAY_Z };
    if (e.maxNativeZoom != null) o.maxNativeZoom = e.maxNativeZoom;
    if (e.subdomains) o.subdomains = e.subdomains;
    return o;
  }

  // Adds the valid map.tiles.custom entries as 'custom-<id>': base providers
  // to `reg`, overlay:true entries to `overlays`.
  function _registerCustom(list, reg, overlays) {
    if (list == null) return;
    if (!Array.isArray(list)) { _warnOnce('map.tiles.custom is not a list; ignored'); return; }
    if (list.length > CUSTOM_MAX) {
      _warnOnce('map.tiles.custom has ' + list.length + ' entries; only the first ' + CUSTOM_MAX + ' are used');
    }
    var n = Math.min(list.length, CUSTOM_MAX);
    for (var i = 0; i < n; i++) {
      var c = list[i];
      var where = 'map.tiles.custom[' + i + ']';
      if (!c || typeof c !== 'object' || Array.isArray(c)) { _warnOnce(where + ' skipped: not an object'); continue; }
      var id = _own(c, 'id');
      // An invalid id is never quoted: it is not known to be harmless text.
      if (typeof id !== 'string' || !CUSTOM_ID_RE.test(id)) {
        _warnOnce(where + ' skipped: id must be 1-32 characters of a-z, 0-9 and -, starting with a letter or digit');
        continue;
      }
      var key = 'custom-' + id;
      var overlay = _own(c, 'overlay');
      if (overlay != null && typeof overlay !== 'boolean') { _warnOnce('custom tile "' + id + '" skipped: overlay must be true or false'); continue; }
      // One 'custom-<id>' namespace across base providers and overlays.
      if (Object.prototype.hasOwnProperty.call(reg, key) || Object.prototype.hasOwnProperty.call(overlays, key)) {
        _warnOnce('custom tile "' + id + '" skipped: duplicate id'); continue;
      }
      var e = _customEntry(c, id);
      if (typeof e === 'string') { _warnOnce('custom tile "' + id + '" skipped: ' + e); continue; }
      if (overlay === true) {
        if (e.format !== 'raster') { _warnOnce('custom tile "' + id + '" skipped: an overlay must be a raster url'); continue; }
        e = _customOverlay(e);
        overlays[key] = Object.freeze ? Object.freeze(e) : e;
        continue;
      }
      reg[key] = Object.freeze ? Object.freeze(e) : e;
    }
  }

  // An optIn group is on only when map.tiles.providers.<group>.enabled === true.
  function _groupOn(group) {
    var p = _cfg && _cfg.providers;
    var g = (p && typeof p === 'object') ? _own(p, group) : null;
    return !!(g && typeof g === 'object' && g.enabled === true);
  }

  // map-hillshade.js fills in only {z}, {x} and {y}: a hillshade url must use
  // exactly those, each once ({s}, {r}, {-y} would fetch a literal and draw
  // a silently blank layer).
  function _hillshadeUrlOk(u) {
    if (!_validTileUrl(u)) return false;
    var ph = u.match(/\{[^{}]*\}/g) || [];
    return ph.length === 3 && ph.slice().sort().join('') === '{x}{y}{z}';
  }

  // The hillshade overlay: providers.hillshade.url when it passes the S3 gate
  // and _hillshadeUrlOk, else the AWS Open Data Terrarium tiles.
  function _hillshadeEntry() {
    var p = _cfg && _cfg.providers;
    var g = (p && typeof p === 'object') ? _own(p, 'hillshade') : null;
    var u = (g && typeof g === 'object') ? _own(g, 'url') : null;
    var url = HILLSHADE_URL;
    if (u != null) {
      if (_hillshadeUrlOk(u)) url = u;
      else _warnOnce('map.tiles.providers.hillshade.url rejected; using the default elevation tiles');
    }
    // Same url -> same entry object, so maps keep their hillshade layer when
    // the registry is rebuilt (config arriving async) instead of redrawing it.
    if (_hillshade && _hillshade.url === url) return _hillshade;
    _hillshade = { provider: 'hillshade', optIn: true, kind: 'hillshade', label: 'Hillshade (terrain)', url: url,
      attribution: 'Terrain: Mapzen/AWS Open Data (SRTM, USGS and others)',
      maxZoom: _DEFAULT_MAX_ZOOM, maxNativeZoom: 15, opacity: 0.35, zIndex: OVERLAY_Z };
    return _hillshade;
  }

  // Rebuilds window.MC_TILE_OVERLAYS: hillshade (only while map-hillshade.js
  // has defined MC_createHillshadeLayer), the EOX labels, then custom ones.
  function _buildOverlays() {
    var o = {};
    if (typeof window.MC_createHillshadeLayer === 'function' && _groupOn('hillshade')) o.hillshade = _hillshadeEntry();
    for (var k in OVERLAY_STYLES) {
      if (_groupOn(OVERLAY_STYLES[k].provider)) o[k] = OVERLAY_STYLES[k];
    }
    for (k in _customOverlays) o[k] = _customOverlays[k];
    OVERLAYS = o;
    window.MC_TILE_OVERLAYS = o;
  }

  // map-hillshade.js loads after this file, so the registry built at parse
  // time has no hillshade; every picker build / overlay sync re-checks.
  function _ensureHillshade() {
    if (!_hasOverlay('hillshade') && typeof window.MC_createHillshadeLayer === 'function' && _groupOn('hillshade')) _buildOverlays();
  }

  function _hasOverlay(id) {
    return typeof id === 'string' && Object.prototype.hasOwnProperty.call(OVERLAYS, id);
  }

  window.MC_initTileRegistry = function(fromAsync) {
    _cfg = (typeof window !== 'undefined' && window.MC_MAP_CFG && window.MC_MAP_CFG.tiles) ? window.MC_MAP_CFG.tiles : null;

    var HAS_CARTO = !_cfg || !_cfg.providers || !_cfg.providers.carto || _cfg.providers.carto.enabled !== false;
    var HAS_OSM = _cfg && _cfg.providers && _cfg.providers.osm && _cfg.providers.osm.enabled;
    var HAS_OPENTOPOMAP = _cfg && _cfg.providers && _cfg.providers.opentopomap && _cfg.providers.opentopomap.enabled;
    var HAS_USGS = _cfg && _cfg.providers && _cfg.providers.usgs && _cfg.providers.usgs.enabled;
    var HAS_STAMEN = _cfg && _cfg.providers && _cfg.providers.stamen && _cfg.providers.stamen.enabled && !!_cfg.providers.stamen.token;
    var HAS_ESRI = true; // Kept for backwards compatibility

    REGISTRY = {};
    for (var key in BASE_STYLES) {
      var style = BASE_STYLES[key];
      if (style.optIn) { if (_groupOn(style.provider)) REGISTRY[key] = style; continue; }
      if (style.provider === 'carto' && HAS_CARTO) REGISTRY[key] = style;
      if (style.provider === 'osm' && HAS_OSM) REGISTRY[key] = style;
      if (style.provider === 'opentopomap' && HAS_OPENTOPOMAP) REGISTRY[key] = style;
      if (style.provider === 'stamen' && HAS_STAMEN) REGISTRY[key] = style;
      if (style.provider === 'usgs' && HAS_USGS) REGISTRY[key] = style;
      if (style.provider === 'esri' && HAS_ESRI) REGISTRY[key] = style;
    }
    // Custom entries are always enabled when valid; registered before the
    // defaults below so darkDefault/lightDefault may name a custom-* id.
    _customOverlays = {};
    if (_cfg) _registerCustom(_own(_cfg, 'custom'), REGISTRY, _customOverlays);
    _buildOverlays();

    // Keep the public reference in sync with the newly rebuilt REGISTRY
    window.MC_TILE_PROVIDERS = REGISTRY;

    if (_cfg && _cfg.darkDefault && _hasId(_cfg.darkDefault)) _serverDefault = _cfg.darkDefault;
    if (_cfg && _cfg.lightDefault && _hasId(_cfg.lightDefault)) _serverDefaultLight = _cfg.lightDefault;

    // When called after async config loads, notify maps to re-sync tiles
    if (fromAsync) {
      try {
        var ev = (typeof CustomEvent === 'function')
          ? new CustomEvent('mc-tile-provider-changed', { detail: { fromConfig: true } })
          : { type: 'mc-tile-provider-changed', detail: { fromConfig: true } };
        window.dispatchEvent(ev);
      } catch (_) {}
    }
  };

  // Exposed for unit tests of the S3 URL gate.
  window.MC__validTileUrl = _validTileUrl;

  // Run once immediately with whatever config is available at parse time
  window.MC_initTileRegistry();

  function _isDark() {
    try {
      var attr = document.documentElement.getAttribute('data-theme');
      if (attr === 'dark') return true;
      if (attr === 'light') return false;
      return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
    } catch (_) { return false; }
  }

  // An entry fits a theme when it was made for that theme or for both ('any',
  // e.g. satellite imagery, which is never colour-inverted).
  function _fitsTheme(p, theme) {
    return !!p && (p.type === theme || p.type === 'any');
  }

  function getActiveId() {
    try {
      var stored = window.localStorage && window.localStorage.getItem(STORAGE_KEY);
      if (_hasId(stored) && _fitsTheme(REGISTRY[stored], 'dark')) return stored;
    } catch (_) {}
    if (_cfg && _cfg.darkDefault && _hasId(_cfg.darkDefault)) return _cfg.darkDefault;
    if (_hasId(_serverDefault)) return _serverDefault;
    return DEFAULT_ID;
  }

  function getActiveLightId() {
    try {
      var stored = window.localStorage && window.localStorage.getItem(STORAGE_KEY_LIGHT);
      if (_hasId(stored) && _fitsTheme(REGISTRY[stored], 'light')) return stored;
    } catch (_) {}
    if (_cfg && _cfg.lightDefault && _hasId(_cfg.lightDefault)) return _cfg.lightDefault;
    if (_hasId(_serverDefaultLight)) return _serverDefaultLight;
    return DEFAULT_ID_LIGHT;
  }
  
  function setActive(id, type) {
    if (!_hasId(id)) return false;
    if (type !== 'light' && type !== 'dark') return false;
    if (!_fitsTheme(REGISTRY[id], type)) return false;
    var skey = type === 'light' ? STORAGE_KEY_LIGHT : STORAGE_KEY;
    try {
      if (window.localStorage) window.localStorage.setItem(skey, id);
    } catch (_) { }
    var detail = { id: id, provider: REGISTRY[id], type: type };
    try {
      var ev = (typeof CustomEvent === 'function')
        ? new CustomEvent(EVENT_NAME, { detail: detail })
        : { type: EVENT_NAME, detail: detail };
      window.dispatchEvent(ev);
    } catch (_) { }
    applyTileFilter();
    return true;
  }


  // The pinned map: the stored id while it is registered, else null (Auto).
  function getPinnedId() {
    var v = _pinMemo;
    if (!_pinUnsaved) {
      try { if (window.localStorage) v = window.localStorage.getItem(PIN_KEY); } catch (_) {}
    }
    return _hasId(v) ? v : null;
  }

  // Pin `id` (a registered entry, any theme), or clear the pin with null.
  // Every map follows: the event makes MC_attachAutoTiles re-sync.
  function setPinned(id) {
    if (id !== null && !_hasId(id)) return false;
    _pinMemo = id;
    _pinUnsaved = false;
    try {
      if (window.localStorage) {
        if (id === null) window.localStorage.removeItem(PIN_KEY);
        else window.localStorage.setItem(PIN_KEY, id);
      }
    } catch (_) { _pinUnsaved = true; } // e.g. quota full: never fall back to the stored, older pin
    var detail = { pinned: id };
    try {
      var ev = (typeof CustomEvent === 'function')
        ? new CustomEvent(EVENT_NAME, { detail: detail })
        : { type: EVENT_NAME, detail: detail };
      window.dispatchEvent(ev);
    } catch (_) { }
    return true;
  }

  // The entry a map without an explicit layer shows for a theme: the pin
  // when it suits the theme (an 'any' entry, e.g. satellite, always does),
  // else that theme's choice. A map with a picker ignores the pin: there
  // the pin is the explicit layer, and Auto always means the theme's map.
  function _themeId(isDark, ignorePin) {
    var pin = ignorePin ? null : getPinnedId();
    if (pin && _fitsTheme(REGISTRY[pin], isDark ? 'dark' : 'light')) return pin;
    return isDark ? getActiveId() : getActiveLightId();
  }

  function setServerDefault(id) { if (_hasId(id)) _serverDefault = id; }
  function setServerDefaultLight(id) { if (_hasId(id)) _serverDefaultLight = id; }

  
  function _isDarkEffective() {
    return _isDark();
  }

  // `pane` (optional): a specific map's tile pane. Without it the first
  // `.leaflet-tile-pane` in the document is used (the original behaviour);
  // MC_attachAutoTiles passes its own map's pane so several maps on one page
  // each get their filter.
  function applyTileFilter(pane) {
    if (!pane || !pane.style || typeof pane.getAttribute !== 'function') {
      try { pane = document.querySelector('.leaflet-tile-pane'); } catch (_) { pane = null; }
    }
    if (!pane || !pane.style) return;
    // Contract: if an explicit layer was selected via the map control, it locks
    // the CSS filter state. We bypass our auto-theme logic here so we don't accidentally
    // invert an explicitly chosen Light map while the app theme is Dark.
    if (pane.getAttribute('data-explicit-layer') === 'true') {
      return;
    }

    var isDark = _isDarkEffective();
    var picker = typeof pane.getAttribute === 'function' && pane.getAttribute('data-mc-picker') === 'true';
    pane.style.filter = _paneFilter(REGISTRY[_themeId(isDark, picker)], isDark);
  }

  // The tile-pane CSS filter for an entry: its `filter` in both themes, else
  // its invertFilter in dark mode (never for 'any'), else none.
  function _paneFilter(p, isDark) {
    if (!p) return '';
    if (p.filter) return p.filter;
    return (isDark && p.type !== 'any' && p.invertFilter) ? p.invertFilter : '';
  }


  // ── Public surface ──────────────────────────────────────────────────────
  
  window.MC_DARK_TILE_DEFAULT           = DEFAULT_ID;
  window.MC_TILE_PROVIDERS              = REGISTRY; // initial ref; MC_initTileRegistry keeps this in sync
  window.MC_setDarkTileProvider         = function(id) { return setActive(id, 'dark'); };
  window.MC_setLightTileProvider        = function(id) { return setActive(id, 'light'); };
  window.MC_getDarkTileProvider         = getActiveId;
  window.MC_getLightTileProvider        = getActiveLightId;
  window.MC_getPinnedTileProvider       = getPinnedId;
  window.MC_setPinnedTileProvider       = setPinned;
  window.MC_setServerDefaultTileProvider = setServerDefault;
  window.MC_setServerDefaultLightTileProvider = setServerDefaultLight;
  window.MC_applyTileFilter             = applyTileFilter;
  // Theme truth used by every map surface (data-theme attr, else OS preference).
  // roles.js helpers consult this at call time instead of re-deriving it, so a
  // theme flip and a pixel-perfect tile pick can't disagree about "dark".
  window.MC_isDarkTheme                 = _isDark;

  /**
   * Resolve a registry id to a concrete tile-URL string, invoking function-typed
   * `url` builders so callers never hand L.tileLayer a function (#1614). Returns
   * `fallback` when the id isn't in the active registry (e.g. provider disabled).
   * Lets non-map pages request a specific style without duplicating the URL.
   */
  window.MC_tileUrlById = function (id, fallback) {
    var p = _hasId(id) ? REGISTRY[id] : null;
    var u = p && (p.url || p.baseUrl);
    if (!u) return fallback;
    return (typeof u === 'function') ? u() : u;
  };

  // ── Shared tile resolution (single source of truth) ──────────────────────
  //
  // Before this block every surface resolved its own basemap: map.js/_resolveTileUrl
  // and live.js/_liveResolveTile honoured the light provider pick, while roles.js'
  // getTileUrl() light branch returned the bare TILE_LIGHT constant and every other
  // consumer (node detail inset, node reach, rx-coverage, analytics minimap) went
  // through that asymmetric path — wrong basemap in light mode, keyless Carto tiles
  // when config raced page load, and provider extras (attribution, maxZoom,
  // refUrl, invertFilter) dropped on the floor. All of them now come here.

  // Bare-constant fallbacks for the sandboxed/no-registry case (roles.js is the
  // normal home of TILE_DARK/TILE_LIGHT and loads before this file; keep the
  // literals in sync with roles.js if either ever changes).
  var _FALLBACK_TILE_DARK  = 'https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png';
  var _FALLBACK_TILE_LIGHT = 'https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png';
  var _ATTR_CARTO  = '© OpenStreetMap © CartoDB';
  var _ATTR_OSM    = '© OpenStreetMap contributors';

  function _constantTileUrl(dark) {
    // Prefer the roles.js-scalar when present (it carries explicit legacy
    // overrides and keyed defaults after MeshConfigReady resolves).
    try {
      if (dark && typeof TILE_DARK === 'string' && TILE_DARK) return TILE_DARK;
      if (!dark && typeof TILE_LIGHT === 'string' && TILE_LIGHT) return TILE_LIGHT;
    } catch (_e) { /* not loaded — use the literals */ }
    return dark ? _FALLBACK_TILE_DARK : _FALLBACK_TILE_LIGHT;
  }

  /**
   * Resolve the effective tile provider for a theme into a plain descriptor:
   *   { type, id, url, attribution, maxZoom, refUrl, invertFilter, override,
   *     format, category, maxNativeZoom, subdomains }
   * `type` is always the theme asked for ('dark' | 'light'), also when the
   * entry is an 'any' one. format defaults to 'raster', category to 'street',
   * maxNativeZoom and subdomains to null.
   * `url` is always a resolved string — provider `url`/`baseUrl` builders are
   * invoked here so callers never hand L.tileLayer a function (#1614).
   * Resolution happens at call time (never cached): MC_initTileRegistry(true)
   * reassigns the registry when async config arrives, and localStorage picks
   * can change at any moment.
   *
   * Precedence:
   *   1. Explicit operator override (cfg.tiles.dark|light or
   *      map.tiles.darkUrl|lightUrl — tracked by roles.js in
   *      window.MC_LEGACY_TILE_OVERRIDE; beats everything).
   *   2. User/operator registry pick for the theme (MC_getDarkTileProvider /
   *      MC_getLightTileProvider — localStorage, then server default, then the
   *      shipped carto defaults).
   *   3. The TILE_DARK/TILE_LIGHT constants (pre-config load or a registry
   *      the config has emptied out).
   */
  // opts.ignorePin: resolve the theme's map even when a map is pinned (the
  // Auto layer of a map with a picker).
  window.MC_resolveTile = function (isDark, opts) {
    isDark = !!isDark;
    var ignorePin = !!(opts && opts.ignorePin);
    var desc = {
      type: isDark ? 'dark' : 'light',
      id: null,
      url: null,
      attribution: _ATTR_CARTO,
      maxZoom: _DEFAULT_MAX_ZOOM,
      refUrl: null,
      invertFilter: null,
      override: false,
      format: 'raster',
      category: 'street',
      maxNativeZoom: null,
      subdomains: null
    };
    // 1. Explicit operator override beats user picks
    try {
      var ov = window.MC_LEGACY_TILE_OVERRIDE;
      if (ov && ov[desc.type]) {
        desc.url = _constantTileUrl(isDark);
        desc.attribution = _ATTR_OSM;
        desc.override = true;
        return desc;
      }
    } catch (_e) { /* fall through to the registry */ }
    // 2. Registry pick for this theme (honours localStorage + server defaults).
    // Reads the PUBLIC getters/registry ref, not the private internals: they're
    // the stable contract (window.MC_TILE_PROVIDERS is kept in sync by
    // MC_initTileRegistry after every rebuild) and they let test sandboxes
    // inject mocks the same way the #1614 harness does.
    var id = null;
    var reg0 = null;
    try { reg0 = window.MC_TILE_PROVIDERS || REGISTRY; } catch (_e) { reg0 = REGISTRY; }
    // 2a. A map pinned in a picker, when it suits this theme.
    if (!ignorePin) try {
      var pin = (typeof window.MC_getPinnedTileProvider === 'function') ? window.MC_getPinnedTileProvider() : null;
      if (pin && Object.prototype.hasOwnProperty.call(reg0, pin) && _fitsTheme(reg0[pin], desc.type)) id = pin;
    } catch (_e) { id = null; }
    if (!id) try {
      if (isDark && typeof window.MC_getDarkTileProvider === 'function') id = window.MC_getDarkTileProvider();
      else if (!isDark && typeof window.MC_getLightTileProvider === 'function') id = window.MC_getLightTileProvider();
    } catch (_e) { id = null; }
    var reg = null;
    try { reg = window.MC_TILE_PROVIDERS || null; } catch (_e) { reg = null; }
    if (!reg) reg = REGISTRY;
    var _pick = function (pid) {
      return (pid && Object.prototype.hasOwnProperty.call(reg, pid)) ? reg[pid] : null;
    };
    var p = _pick(id);
    var pickedId = p ? id : null;
    // The stored/default id may reference a provider the running config has
    // disabled; keep the old map.js/live.js behaviour of falling back to the
    // shipped default entry before giving up on the registry.
    var defId = isDark ? DEFAULT_ID : DEFAULT_ID_LIGHT;
    if (!p) { p = _pick(defId); pickedId = p ? defId : null; }
    // Registry is authoritative on types; an 'any' entry fits both themes.
    if (p && p.type && p.type !== desc.type && p.type !== 'any') { p = null; pickedId = null; }
    if (!p) { // 3. Bare constants (roles.js scalars or shipped literals)
      desc.url = _constantTileUrl(isDark);
      return desc;
    }
    var u = p.url || p.baseUrl;
    desc.id = pickedId; // the entry the URL actually came from
    desc.url = (typeof u === 'function') ? u() : u;
    desc.attribution = p.attribution || _ATTR_CARTO;
    desc.maxZoom = p.maxZoom || _DEFAULT_MAX_ZOOM;
    desc.refUrl = p.refUrl || null;
    // A `filter` entry is filtered in both themes; 'any' is never inverted.
    desc.invertFilter = p.filter || ((p.type === 'any') ? null : (p.invertFilter || null));
    desc.format = p.format || 'raster';
    desc.category = p.category || 'street';
    desc.maxNativeZoom = (p.maxNativeZoom != null) ? p.maxNativeZoom : null;
    desc.subdomains = p.subdomains || null;
    return desc;
  };


  // ── Shared basemap layer builder ─────────────────────────────────────────

  var _vectorWarned = false;

  // Leaflet options for one raster entry/descriptor. maxNativeZoom and
  // subdomains are only passed when set, so Leaflet keeps its own defaults.
  function _rasterOpts(desc) {
    var o = { attribution: desc.attribution || '', maxZoom: desc.maxZoom || _DEFAULT_MAX_ZOOM };
    if (typeof desc.maxNativeZoom === 'number' && isFinite(desc.maxNativeZoom)) o.maxNativeZoom = desc.maxNativeZoom;
    if (desc.subdomains) o.subdomains = desc.subdomains;
    return o;
  }

  /**
   * Build the Leaflet layer for a provider entry or an MC_resolveTile
   * descriptor. Raster: an L.tileLayer, grouped with its refUrl labels layer
   * in an L.layerGroup when the entry has one (Esri). Function-typed url
   * builders are invoked here (#1614).
   *
   * Vector entries go to MC_buildVectorLayer (map-vector.js), handing it a
   * factory for the raster entry opts.fallbackId (default: the theme's
   * shipped Carto id) that it mounts when MapLibre can't run. Without
   * map-vector.js they warn once and build that raster entry directly.
   * opts.raster (static/secondary maps) builds that raster entry directly
   * and never loads MapLibre.
   *
   * @param {Object} desc - registry entry or resolved descriptor
   * @param {{fallbackId?: string, raster?: boolean}} [opts]
   * @returns {L.Layer}
   */
  window.MC_buildTileLayer = function (desc, opts) {
    opts = opts || {};
    if (desc && desc.format === 'vector') {
      var fallback = function () { return _buildRaster(_withMapZoom(_vectorFallback(desc, opts.fallbackId), desc)); };
      if (opts.raster) return fallback();
      if (typeof window.MC_buildVectorLayer === 'function') {
        try { return window.MC_buildVectorLayer(desc, { fallback: fallback }); } catch (_) { /* raster below */ }
      } else if (!_vectorWarned) {
        _vectorWarned = true;
        try { console.warn('[tiles] vector basemap support is not loaded; using a raster fallback'); } catch (_) {}
      }
      return fallback();
    }
    return _buildRaster(desc);
  };

  // The raster entry a vector one falls back to. A descriptor's type is the
  // theme it was resolved for; an 'any' entry follows the current theme.
  function _vectorFallback(desc, fallbackId) {
    var dark = desc.type === 'dark' || (desc.type === 'any' && _isDark());
    var defFallback = dark ? DEFAULT_ID : DEFAULT_ID_LIGHT;
    var fid = fallbackId || defFallback;
    var fb = (_hasId(fid) && REGISTRY[fid]) || BASE_STYLES[fid];
    // Never recurse into another vector entry: the shipped Carto raster is the floor.
    if (!fb || fb.format === 'vector') fb = (_hasId(defFallback) && REGISTRY[defFallback]) || BASE_STYLES[defFallback];
    return fb;
  }

  // A raster XYZ url for a descriptor: its own url, or, for a vector entry
  // (a MapLibre style JSON, which L.tileLayer can't use), the raster it falls
  // back to. For callers that only want a tile template (getTileUrl).
  window.MC_rasterTileUrl = function (desc) {
    if (!desc) return null;
    if (desc.format !== 'vector') return desc.url;
    var fb = _vectorFallback(desc);
    var u = fb && (fb.url || fb.baseUrl);
    return (typeof u === 'function') ? u() : (u || null);
  };

  // The raster standing in for a vector entry keeps that entry's maxZoom, so
  // the map's zoom limit doesn't change when the fallback appears; past the
  // raster's own last zoom it upsamples instead of requesting missing tiles.
  function _withMapZoom(fb, desc) {
    var max = desc && desc.maxZoom;
    if (typeof max !== 'number' || !isFinite(max)) return fb;
    var own = (typeof fb.maxNativeZoom === 'number') ? fb.maxNativeZoom : (fb.maxZoom || _DEFAULT_MAX_ZOOM);
    var out = {};
    Object.keys(fb).forEach(function (k) { out[k] = fb[k]; });
    out.maxZoom = max;
    out.maxNativeZoom = Math.min(own, max);
    return out;
  }

  // A vector layer that fell back for a transient reason while MapLibre has
  // loaded since (map-vector.js decides): rebuilding it now mounts GL.
  function _vectorStale(layer) {
    if (typeof window.MC_vectorLayerStale !== 'function') return false;
    try { return !!window.MC_vectorLayerStale(layer); } catch (_) { return false; }
  }

  function _buildRaster(desc) {
    var u = desc.url || desc.baseUrl;
    var url = (typeof u === 'function') ? u() : u;
    var lopts = _rasterOpts(desc);
    var layer = L.tileLayer(url, lopts);
    if (desc.refUrl && typeof L.layerGroup === 'function') {
      layer = L.layerGroup([layer, L.tileLayer(desc.refUrl, _rasterOpts(desc))]);
    }
    return layer;
  }

  /**
   * Build the Leaflet layer for an overlay entry (window.MC_TILE_OVERLAYS[id]).
   * Raster -> L.tileLayer; hillshade -> MC_createHillshadeLayer (map-hillshade.js).
   * opts.pane puts the layer in that map pane (MC_attachAutoTiles passes its
   * map's overlay pane, which the tile pane's CSS filter does not reach).
   *
   * @param {Object} overlay
   * @param {{pane?: string}} [opts]
   * @returns {L.Layer|null} null when the overlay can't be built here
   */
  window.MC_buildOverlayLayer = function (overlay, opts) {
    opts = opts || {};
    if (!overlay || typeof overlay.url !== 'string') return null;
    var o = {
      attribution: overlay.attribution || '',
      maxZoom: overlay.maxZoom || _DEFAULT_MAX_ZOOM,
      opacity: (typeof overlay.opacity === 'number') ? overlay.opacity : 1,
      zIndex: (typeof overlay.zIndex === 'number') ? overlay.zIndex : OVERLAY_Z
    };
    if (typeof overlay.maxNativeZoom === 'number' && isFinite(overlay.maxNativeZoom)) o.maxNativeZoom = overlay.maxNativeZoom;
    var layer = null;
    try {
      if (overlay.kind === 'hillshade') {
        if (typeof window.MC_createHillshadeLayer !== 'function') return null;
        o.url = overlay.url;
        layer = window.MC_createHillshadeLayer(o);
      } else if (overlay.kind === 'raster') {
        if (overlay.subdomains) o.subdomains = overlay.subdomains;
        if (opts.pane) o.pane = opts.pane;
        layer = L.tileLayer(overlay.url, o);
      }
    } catch (_) { layer = null; }
    // The hillshade factory's options don't name a pane; Leaflet reads
    // options.pane when the layer is added, so set it on the instance.
    if (layer && opts.pane && layer.options) layer.options.pane = opts.pane;
    return layer || null;
  };


  // ── Picker grouping ─────────────────────────────────────────────────────

  var PICKER_GROUPS = [
    { category: 'street',    heading: 'Street' },
    { category: 'topo',      heading: 'Topographic' },
    { category: 'satellite', heading: 'Satellite' }
  ];
  var PICKER_TYPES = ['light', 'dark', 'any'];
  var PICKER_AUTO_KEY = '__auto__';
  var PICKER_AUTO_TITLE = 'Follows the app\'s current light/dark theme';

  // Entries of an unknown category list under Street (the contract default).
  function _categoryOf(p) {
    return (p && _inSet(CATEGORIES, p.category)) ? p.category : 'street';
  }

  // Registry ids in picker order: group, then type, then plain entries before
  // style:'gray' ones, then registry order. Entries of any other type are not
  // listed (as before).
  function _pickerOrder() {
    var ids = Object.keys(REGISTRY);
    var out = [];
    PICKER_GROUPS.forEach(function (g) {
      PICKER_TYPES.forEach(function (t) {
        [false, true].forEach(function (gray) {
          ids.forEach(function (id) {
            var p = REGISTRY[id];
            if (p && p.type === t && _categoryOf(p) === g.category && (p.style === 'gray') === gray) out.push(id);
          });
        });
      });
    });
    return out;
  }

  function _headingOf(category) {
    for (var i = 0; i < PICKER_GROUPS.length; i++) {
      if (PICKER_GROUPS[i].category === category) return PICKER_GROUPS[i].heading;
    }
    return category;
  }

  // DOM surgery on a freshly rendered L.control.layers base list. Leaflet is
  // handed raw ids as names (so e.name is the exact id); here each label gets
  // data-tile-id and its plain-text label (S2: textContent, never innerHTML),
  // and each category's labels move into a <fieldset> whose <legend> names
  // the group, so screen readers announce it before the group's radios. A
  // separator splits a group's light entries from its dark ones. Auto stays
  // first, outside any group.
  function _decorateControl(control) {
    try {
      var labels = control.getContainer().querySelectorAll('.leaflet-control-layers-base label');
      var group = null, groupCat = null, sawLight = false, sepDone = false;
      for (var i = 0; i < labels.length; i++) {
        var labelEl = labels[i];
        var spans = labelEl.querySelectorAll('span');
        if (spans.length === 0) continue;

        // Target the last span to avoid nuking Leaflet's radio <input>
        var span = spans[spans.length - 1];
        var id = span.textContent.trim();
        labelEl.setAttribute('data-tile-id', id);

        if (id === PICKER_AUTO_KEY) {
          span.textContent = ' ';
          var auto = document.createElement('span');
          auto.title = PICKER_AUTO_TITLE;
          auto.textContent = 'Auto';
          span.appendChild(auto);
          continue;
        }
        var p = REGISTRY[id];
        if (!p) continue;
        span.textContent = ' ' + (p.label || id);

        var cat = _categoryOf(p);
        if (cat !== groupCat) {
          group = document.createElement('fieldset');
          group.className = 'mc-tile-group';
          var legend = document.createElement('legend');
          legend.className = 'mc-tile-group-heading';
          legend.textContent = _headingOf(cat);
          group.appendChild(legend);
          labelEl.parentNode.insertBefore(group, labelEl);
          groupCat = cat;
          sawLight = false;
          sepDone = false;
        }
        if (p.type === 'dark' && sawLight && !sepDone) {
          var sep = document.createElement('div');
          sep.className = 'leaflet-control-layers-separator';
          group.appendChild(sep);
          sepDone = true;
        }
        group.appendChild(labelEl);
        if (p.type === 'light') sawLight = true;
      }
    } catch (_) {}
    _decorateOverlays(control);
  }

  // The overlay checkboxes get the same treatment: data-tile-overlay-id,
  // plain-text label (S2), and one 'Overlays' fieldset styled like the
  // category groups. Leaflet names them by id (control._mcOverlayIds).
  function _decorateOverlays(control) {
    var ids = control._mcOverlayIds;
    if (!ids) return;
    try {
      var labels = control.getContainer().querySelectorAll('.leaflet-control-layers-overlays label');
      var group = null;
      for (var i = 0; i < labels.length; i++) {
        var labelEl = labels[i];
        var spans = labelEl.querySelectorAll('span');
        if (spans.length === 0) continue;
        var span = spans[spans.length - 1];
        var id = span.textContent.trim();
        if (!_inSet(ids, id) || !_hasOverlay(id)) continue;
        labelEl.setAttribute('data-tile-overlay-id', id);
        span.textContent = ' ' + (OVERLAYS[id].label || id);
        if (!group) {
          group = document.createElement('fieldset');
          group.className = 'mc-tile-group mc-tile-overlays';
          var legend = document.createElement('legend');
          legend.className = 'mc-tile-group-heading';
          legend.textContent = 'Overlays';
          group.appendChild(legend);
          labelEl.parentNode.insertBefore(group, labelEl);
        }
        group.appendChild(labelEl);
      }
    } catch (_) {}
  }


  /**
   * Build and attach a Leaflet layer control listing "Auto (follows theme)"
   * at the top, then every enabled provider as an individual selectable base
   * layer. Selecting Auto delegates to the existing theme-synced tile group;
   * selecting a specific provider overrides it.
   *
   * @param {L.Map} map - The Leaflet map instance
   * @param {L.LayerGroup} autoLayerGroup - the theme-synced group MC_attachAutoTiles owns
   */
  // Overlays are listed only on pickers MC_attachAutoTiles builds (they need
  // its per-map overlay set); this public form lists base layers only.
  window.MC_createLayerControl = function(map, autoLayerGroup, position) {
    var lc = _createLayerControl(map, autoLayerGroup, position, null);
    return lc ? lc.control() : null;
  };

  // Px covered at the bottom of a viewport vh tall by the rects that start
  // below the list's top and overlap its columns (0 when none do). Pure, so
  // the map's own bottom corners and a page's bottomInset share one rule.
  function _coveredBelow(listRect, rects, vh) {
    var top = vh;
    (rects || []).forEach(function (r) {
      if (!r || !r.height || !r.width) return;
      if (r.top <= listRect.top || r.right <= listRect.left || r.left >= listRect.right) return;
      top = Math.min(top, r.top);
    });
    return Math.max(0, vh - top);
  }

  // Rects of the elements that are on screen: skips display:none,
  // visibility:hidden and faded-out (opacity 0) panels, whose boxes keep a
  // size while hidden.
  function _shownRects(els) {
    var out = [];
    Array.prototype.forEach.call(els || [], function (el) {
      if (!el || typeof el.getBoundingClientRect !== 'function') return;
      try {
        var cs = typeof window.getComputedStyle === 'function' ? window.getComputedStyle(el) : null;
        if (cs && (cs.display === 'none' || cs.visibility === 'hidden' || +cs.opacity === 0)) return;
      } catch (_) {}
      out.push(el.getBoundingClientRect());
    });
    return out;
  }

  // Shortest list height kept when the visible room is smaller still, so a
  // very short viewport gets a small scrolling list rather than none.
  var LAYER_LIST_MIN_HEIGHT = 60;

  // Leaflet sizes the expanded list from the whole map's height, but the
  // Live map runs under the nav, the replay bar and the legend, so on a
  // short screen the end of the list was off screen or hidden, with no
  // scrollbar. Fit it to what is visible: the viewport, the map's bottom
  // edge, the map's own bottom-corner controls (attribution, scale) in the
  // list's columns, and bottomInset(listRect) (px covered at the bottom of
  // the viewport, from the page). Keeps the scroll position across a re-fit.
  function _fitLayerList(control, map, bottomInset) {
    var section = control && control._section;
    var container = control && control._container;
    if (!section || !container || !L.DomUtil.hasClass(container, 'leaflet-control-layers-expanded')) return;
    var scrollTop = section.scrollTop || 0;
    section.style.height = '';
    L.DomUtil.removeClass(section, 'leaflet-control-layers-scrollbar');
    var vh = window.innerHeight;
    var listRect = section.getBoundingClientRect();
    var limit = vh;
    try { limit = Math.min(limit, map.getContainer().getBoundingClientRect().bottom); } catch (_) {}
    var corners = map._controlCorners || {};
    var cornerEls = [];
    ['bottomleft', 'bottomright'].forEach(function (k) {
      if (corners[k] && corners[k].children) Array.prototype.push.apply(cornerEls, corners[k].children);
    });
    limit = Math.min(limit, vh - _coveredBelow(listRect, _shownRects(cornerEls), vh));
    var inset = 0;
    if (typeof bottomInset === 'function') { try { inset = +bottomInset(listRect) || 0; } catch (_) {} }
    limit = Math.min(limit, vh - inset);
    var room = Math.max(LAYER_LIST_MIN_HEIGHT, Math.floor(limit - listRect.top - 12));
    if (section.scrollHeight > room) {
      L.DomUtil.addClass(section, 'leaflet-control-layers-scrollbar');
      section.style.height = room + 'px';
      section.scrollTop = scrollTop;
    }
  }

  // Implementation of MC_createLayerControl. Returns { control(), detach() } so
  // MC_attachAutoTiles can tear the control down with its map; the public
  // function keeps returning just the L.control.layers instance.
  function _createLayerControl(map, autoLayerGroup, position, overlaySet, bottomInset) {
    if (typeof L === 'undefined') return null;
    
    // Set a default position just in case it isn't provided
    var controlPosition = position || 'topleft';

    var AUTO_KEY   = PICKER_AUTO_KEY;
    var _control   = null;  // current L.control.layers instance
    var _layerMap  = {};    // provider-id → L.tileLayer
    var _isAuto    = true;  // true when "Auto" is the active selection
    var _explicitId = null; // the entry the user picked while not Auto

    // Restore the auto tile group; the event makes MC_attachAutoTiles re-sync it
    function _activateAuto() {
      _isAuto = true;
      _explicitId = null;
      try { 
        var pane = map.getPane('tilePane');
        if (pane) pane.removeAttribute('data-explicit-layer');
      } catch (_) {}
      try { if (autoLayerGroup && !map.hasLayer(autoLayerGroup)) map.addLayer(autoLayerGroup); } catch (_) {}
      
      try {
        var ev = (typeof CustomEvent === 'function')
          ? new CustomEvent('mc-tile-provider-changed', { detail: { auto: true } })
          : { type: 'mc-tile-provider-changed', detail: { auto: true } };
        window.dispatchEvent(ev);
      } catch (_) {}
      
      if (typeof applyTileFilter === 'function') applyTileFilter();
    }

    function _buildControl() {
      // Tear down existing control + explicit tile layers
      if (_control) { try { _control.remove(); } catch (_) {} _control = null; map._mcLayerControl = null; }
      Object.keys(_layerMap).forEach(function (id) {
        try { map.removeLayer(_layerMap[id]); } catch (_) {}
      });
      _layerMap = {};
      // Remove stale baselayerchange listeners by cloning off event (Leaflet re-adds on new control)
      if (map._mcBaselayerchangeHandler) {
        try { map.off('baselayerchange', map._mcBaselayerchangeHandler); } catch (_) {}
      }
      _offOverlayHandler();

      // Grouped by category (Street, Topographic, Satellite); within a group
      // light entries first, then dark, then theme-neutral ('any') ones.
      var orderedIds = _pickerOrder();

      function _makeLayer(id) {
        var p   = REGISTRY[id];
        // Shared builder: also stacks a two-layer provider's (Esri) labels
        // reference tileset on its base, so an explicit pick keeps place names.
        // A vector entry is an empty group until first picked: building it
        // starts the MapLibre download, which only a vector pick should cost.
        var isVector = p.format === 'vector';
        var layer = isVector ? L.layerGroup() : window.MC_buildTileLayer(p);
        var inner = null;

        // Every explicit layer enforces its own filter and locks the pane
        layer.on('add', function () {
          // Re-picked after a transient fallback (MapLibre timed out, then
          // arrived): build it again instead of keeping the raster.
          if (isVector && inner && _vectorStale(inner)) {
            layer.removeLayer(inner);
            inner = null;
          }
          if (isVector && !inner) {
            inner = window.MC_buildTileLayer(p);
            layer.addLayer(inner);
          }
          var pane = map.getPane('tilePane');
          if (pane) {
            pane.setAttribute('data-explicit-layer', 'true');
            // Clears it if null; an 'any' entry is never inverted; a `filter`
            // entry gets its filter whatever the theme.
            pane.style.filter = p.filter || ((p.type === 'any') ? '' : (p.invertFilter || ''));
          }
        });

        _layerMap[id] = layer;
        return layer;
      }

      // "Auto" entry is backed by the theme-synced autoLayerGroup
      var baseMaps = {};
      baseMaps[AUTO_KEY] = autoLayerGroup;

      orderedIds.forEach(function (id) { baseMaps[id] = _makeLayer(id); });

      // Use the dynamic controlPosition variable instead of a hardcoded string
      // Overlay checkboxes, keyed by id (S2: relabelled as text below).
      var overlayMaps = overlaySet ? overlaySet.layers() : null;
      var overlayIds = overlayMaps ? Object.keys(overlayMaps) : [];
      if (!overlayIds.length) overlayMaps = null;

      _control = L.control.layers(baseMaps, overlayMaps, { position: controlPosition }).addTo(map);
      // The overlay set defers its sync while this control handles a click.
      map._mcLayerControl = _control;
      if (overlayMaps) {
        _control._mcOverlayIds = {};
        overlayIds.forEach(function (id) { _control._mcOverlayIds[id] = true; });
      }

      _decorateControl(_control);
      // Leaflet re-renders the list from raw ids whenever a listed layer is
      // added or removed other than by a click; re-apply the labels and groups.
      if (typeof _control._update === 'function') {
        var leafletUpdate = _control._update;
        _control._update = function () {
          var r = leafletUpdate.apply(this, arguments);
          _decorateControl(this);
          _fitLayerList(this, map, bottomInset);
          return r;
        };
      }
      if (typeof _control.expand === 'function') {
        var leafletExpand = _control.expand;
        _control.expand = function () {
          var r = leafletExpand.apply(this, arguments);
          _fitLayerList(this, map, bottomInset);
          return r;
        };
      }
      _offFitHandler();
      map._mcLayerFitHandler = function () { _fitLayerList(_control, map, bottomInset); };
      map.on('resize', map._mcLayerFitHandler);

      // The pinned map, saved by a pick here or on another page, wins; a
      // pin that is gone (or none) means Auto.
      var pinned = getPinnedId();
      _isAuto = !pinned;
      _explicitId = pinned;

      // Decide initial active layer
      if (_isAuto) {
        // Ensure autoLayerGroup is on the map; explicit layers are off
        _activateAuto();
      } else {
        // Re-select the explicit pick (whatever its type), if still registered
        var prevLayer = _explicitId ? _layerMap[_explicitId] : null;
        if (prevLayer) {
          map.addLayer(prevLayer);
          try { if (autoLayerGroup && map.hasLayer(autoLayerGroup)) map.removeLayer(autoLayerGroup); } catch (_) {}
        } else {
          _activateAuto(); // fallback
        }
      }

      map._mcBaselayerchangeHandler = function (e) {
        if (e.name === AUTO_KEY) {
          setPinned(null); // before _activateAuto, whose sync resolves the theme map
          _activateAuto();
          return;
        }

        // Explicit provider selected — the name parameter is exactly our registry ID
        var selectedId = e.name;
        if (!REGISTRY[selectedId]) return;

        _isAuto = false;
        _explicitId = selectedId;
        setPinned(selectedId); // saved: this and every other map follow it
        try { 
          var pane = map.getPane('tilePane');
          // Contract: mark the pane as explicit so applyTileFilter skips auto-theme CSS filters
          if (pane) pane.setAttribute('data-explicit-layer', 'true');
        } catch (_) {}

        // Hide autoLayerGroup while an explicit layer is active
        try { if (autoLayerGroup && map.hasLayer(autoLayerGroup)) map.removeLayer(autoLayerGroup); } catch (_) {}

        // CSS invert filter is handled by the layer's own add/remove events above
      };

      // Now attach the correctly assigned handler
      map.on('baselayerchange', map._mcBaselayerchangeHandler);

      // A ticked/unticked overlay becomes the shared preference; every map's
      // overlay set mirrors it through OVERLAYS_EVENT. The set's own adds and
      // removes (restore, mirror, destroy) are not user choices: skipped.
      if (overlayMaps) {
        map._mcOverlayHandler = function (e) {
          var ids = _control && _control._mcOverlayIds;
          if (!e || !ids || overlaySet.busy() || !_inSet(ids, e.name)) return;
          window.MC_setTileOverlay(e.name, e.type === 'overlayadd');
        };
        map.on('overlayadd', map._mcOverlayHandler);
        map.on('overlayremove', map._mcOverlayHandler);
      }

      return _control;
    }

    _buildControl();

    // Re-build when server config arrives async so newly-enabled providers appear
    function _onConfig(e) {
      if (e && e.detail && e.detail.fromConfig) _buildControl();
    }
    window.addEventListener(EVENT_NAME, _onConfig);

    // When the site theme flips, MC_attachAutoTiles (which owns autoLayerGroup)
    // re-syncs the Auto tile on its own data-theme observer and on
    // mc-tile-provider-changed.
    // Nothing extra needed here — autoLayerGroup is managed externally.

    function _offOverlayHandler() {
      if (!map._mcOverlayHandler) return;
      try { map.off('overlayadd', map._mcOverlayHandler); } catch (_) {}
      try { map.off('overlayremove', map._mcOverlayHandler); } catch (_) {}
      map._mcOverlayHandler = null;
    }

    function _offFitHandler() {
      if (!map._mcLayerFitHandler) return;
      try { map.off('resize', map._mcLayerFitHandler); } catch (_) {}
      map._mcLayerFitHandler = null;
    }

    function _detach() {
      try { window.removeEventListener(EVENT_NAME, _onConfig); } catch (_) {}
      _offFitHandler();
      if (map._mcBaselayerchangeHandler) {
        try { map.off('baselayerchange', map._mcBaselayerchangeHandler); } catch (_) {}
        map._mcBaselayerchangeHandler = null;
      }
      _offOverlayHandler();
      if (_control) { try { _control.remove(); } catch (_) {} _control = null; map._mcLayerControl = null; }
      Object.keys(_layerMap).forEach(function (id) {
        try { map.removeLayer(_layerMap[id]); } catch (_) {}
      });
      _layerMap = {};
      try {
        var pane = map.getPane('tilePane');
        if (pane) pane.removeAttribute('data-explicit-layer');
      } catch (_) {}
    }

    return { control: function () { return _control; }, detach: _detach };
  }


  // ── Overlay preference + per-map overlay set ────────────────────────────
  // The ticked overlays are a per-browser preference shared by every map
  // (like the provider pick), so they are not put in the URL hash.

  var _overlaysMemo = null;      // last value written, for when localStorage throws
  var _overlaysUnsaved = false;  // the last write failed: _overlaysMemo is newer than storage

  function _rawOverlays() {
    // A failed write (quota) leaves an older value in storage that getItem
    // still returns; the page's own copy wins until a write succeeds.
    if (_overlaysUnsaved) return _overlaysMemo;
    try {
      if (window.localStorage) return window.localStorage.getItem(OVERLAYS_KEY);
    } catch (_) { /* storage blocked: use the in-page copy */ }
    return _overlaysMemo;
  }

  // S7: the stored value is untrusted. Parsed in try/catch; only registered
  // ids are kept, each once, at most one per registered overlay.
  function _readOverlays() {
    var raw = _rawOverlays();
    if (typeof raw !== 'string' || !raw || raw.length > OVERLAYS_RAW_MAX) return [];
    var arr;
    try { arr = JSON.parse(raw); } catch (_) { return []; }
    if (!Array.isArray(arr)) return [];
    var max = Object.keys(OVERLAYS).length;
    var out = [];
    for (var i = 0; i < arr.length && out.length < max; i++) {
      if (_hasOverlay(arr[i]) && out.indexOf(arr[i]) < 0) out.push(arr[i]);
    }
    return out;
  }

  function _dispatch(name, detail) {
    try {
      var ev = (typeof CustomEvent === 'function')
        ? new CustomEvent(name, { detail: detail })
        : { type: name, detail: detail };
      window.dispatchEvent(ev);
    } catch (_) { /* dispatch optional */ }
  }

  /** @returns {string[]} ids of the ticked overlays that are registered now */
  window.MC_getTileOverlays = function () {
    _ensureHillshade();
    return _readOverlays();
  };

  /**
   * Tick or untick an overlay for every map in this browser. Persists to
   * localStorage 'mc-tile-overlays' and dispatches 'mc-tile-overlays-changed'
   * when the set changes.
   * @returns {boolean} false when id is not a registered overlay
   */
  window.MC_setTileOverlay = function (id, on) {
    _ensureHillshade();
    if (!_hasOverlay(id)) return false;
    on = !!on;
    var cur = _readOverlays();
    var at = cur.indexOf(id);
    if (on === (at >= 0)) return true;
    if (on) cur.push(id); else cur.splice(at, 1);
    _overlaysMemo = JSON.stringify(cur);
    try {
      if (window.localStorage) window.localStorage.setItem(OVERLAYS_KEY, _overlaysMemo);
      _overlaysUnsaved = false;
    } catch (_) { _overlaysUnsaved = true; }
    _dispatch(OVERLAYS_EVENT, { id: id, on: on });
    return true;
  };

  // The map pane overlays live in: a sibling of the tile pane (so its CSS
  // filter doesn't reach them), above it and below vector/marker panes.
  // Created once per map; null when the map can't make panes.
  function _overlayPane(map) {
    try {
      if (typeof map.getPane === 'function' && map.getPane(OVERLAY_PANE)) return OVERLAY_PANE;
      if (typeof map.createPane !== 'function') return null;
      var el = map.createPane(OVERLAY_PANE);
      if (el && el.style) {
        el.style.zIndex = String(OVERLAY_Z);
        el.style.pointerEvents = 'none';
      }
      return OVERLAY_PANE;
    } catch (_) { return null; }
  }

  // One map's overlay layers, kept in line with the shared preference.
  // Layers are built once per registry entry and reused by the picker.
  function _overlaySet(map) {
    var built = {};   // id -> { entry, layer }
    var busy = false; // true while this set adds/removes layers itself
    var pane = _overlayPane(map);

    function drop(id) {
      try { map.removeLayer(built[id].layer); } catch (_) {}
      delete built[id];
    }

    function layerOf(id) {
      var e = OVERLAYS[id];
      if (built[id] && built[id].entry !== e) drop(id); // registry rebuilt (config arrived)
      if (!built[id]) {
        var l = window.MC_buildOverlayLayer(e, { pane: pane });
        if (!l) return null;
        built[id] = { entry: e, layer: l };
      }
      return built[id].layer;
    }

    // { id: layer } for every overlay that can be built, in registry order.
    function layers() {
      _ensureHillshade();
      var out = {};
      Object.keys(OVERLAYS).forEach(function (id) {
        var l = layerOf(id);
        if (l) out[id] = l;
      });
      return out;
    }

    // A sync while this map's layer control handles a click is deferred to
    // the next tick. Leaflet applies a click by walking every checkbox, and
    // skips redrawing the list for layer changes it sees while doing so: a
    // layer added mid-click (say, another tab's pick that is already in
    // localStorage before its 'storage' event arrives) would be removed
    // again by a later unchecked box, or leave a stale box that the next
    // click removes, writing the removal back to every tab.
    var deferred = null;
    function sync() {
      var c = map._mcLayerControl;
      if (c && c._handlingClick) {
        if (!deferred) deferred = setTimeout(function () { deferred = null; sync(); }, 0);
        return;
      }
      _ensureHillshade();
      var want = {};
      _readOverlays().forEach(function (id) { want[id] = true; });
      busy = true;
      try {
        Object.keys(built).forEach(function (id) {
          if (!_hasOverlay(id) || built[id].entry !== OVERLAYS[id]) { drop(id); return; }
          if (!want[id] && map.hasLayer(built[id].layer)) map.removeLayer(built[id].layer);
        });
        Object.keys(want).forEach(function (id) {
          var l = layerOf(id);
          if (l && !map.hasLayer(l)) map.addLayer(l);
        });
      } catch (_) { /* map torn down */ }
      busy = false;
    }

    function onStorage(e) { if (e && e.key === OVERLAYS_KEY) sync(); }

    try {
      window.addEventListener(OVERLAYS_EVENT, sync);
      window.addEventListener('storage', onStorage);
    } catch (_) {}

    function destroy() {
      if (deferred) { clearTimeout(deferred); deferred = null; }
      try {
        window.removeEventListener(OVERLAYS_EVENT, sync);
        window.removeEventListener('storage', onStorage);
      } catch (_) {}
      busy = true;
      Object.keys(built).forEach(drop);
      busy = false;
    }

    return { layers: layers, sync: sync, destroy: destroy, busy: function () { return busy; } };
  }


  // ── Theme-synced basemap for one map ────────────────────────────────────

  // Fields of a descriptor that need a new Leaflet layer when they change.
  // Anything else (url, attribution, id) is updated in place, which keeps the
  // old tiles on screen until the new ones load (no flash). maxZoom rebuilds:
  // Leaflet reads it into the map's zoom limits only on layeradd/layerremove,
  // so an in-place edit leaves the map capped at (or past) the old value.
  function _needsRebuild(a, b) {
    return a.format !== b.format || a.refUrl !== b.refUrl || a.maxZoom !== b.maxZoom ||
      a.maxNativeZoom !== b.maxNativeZoom || String(a.subdomains) !== String(b.subdomains);
  }

  function _sameTile(a, b) {
    return !_needsRebuild(a, b) && a.id === b.id && a.url === b.url && a.attribution === b.attribution;
  }

  // Swap a tile layer's attribution text. Leaflet's attribution control counts
  // the text it saw on layeradd, so a layer already on the map has its old text
  // removed and the new one added; otherwise layeradd picks the new one up.
  function _setAttribution(map, layer, text) {
    if (!layer.options || layer.options.attribution === text) return;
    var ac = map.attributionControl;
    var shown = false;
    try { shown = !!(ac && map.hasLayer(layer)); } catch (_) { shown = false; }
    if (shown && ac.removeAttribution) { try { ac.removeAttribution(layer.options.attribution); } catch (_) {} }
    layer.options.attribution = text;
    if (shown && ac.addAttribution) { try { ac.addAttribution(text); } catch (_) {} }
  }

  /**
   * Px covered at the bottom of the viewport by the shown elements that sit
   * below the picker's list and overlap its columns. For a page's
   * MC_attachAutoTiles bottomInset: MC_layerListInset(list, els).
   * @param {DOMRect} listRect
   * @param {ArrayLike<Element>} els
   * @param {number} [vh] viewport height (default window.innerHeight)
   * @returns {number}
   */
  window.MC_layerListInset = function (listRect, els, vh) {
    return _coveredBelow(listRect, _shownRects(els), vh == null ? window.innerHeight : vh);
  };
  /** Pure form of MC_layerListInset, over rects already measured. */
  window.MC_coveredBelow = _coveredBelow;

  /**
   * The theme-synced "Auto" basemap for one Leaflet map. Owns an L.layerGroup
   * on the map holding the layer MC_buildTileLayer made from
   * MC_resolveTile(MC_isDarkTheme()), and re-syncs it on
   * 'mc-tile-provider-changed' and on documentElement data-theme changes.
   * Every call keeps its own state, so several maps on one page are
   * independent; the config arriving async re-syncs through the same event.
   *
   * @param {L.Map} map
   * @param {{controlPosition?: string, bottomInset?: function(DOMRect): number}} [opts]
   *   - controlPosition adds the MC_createLayerControl provider picker at that
   *   corner; bottomInset(listRect) returns the px covered at the bottom of
   *   the viewport in the list's columns (the Live replay bar and legend),
   *   which the picker's list keeps clear of.
   * @returns {{group: L.LayerGroup, sync: function(), destroy: function()}}
   */
  window.MC_attachAutoTiles = function (map, opts) {
    opts = opts || {};
    var group = L.layerGroup().addTo(map);
    var cur = null;       // descriptor the current layer was built from
    var layer = null;     // the layer inside group
    var destroyed = false;
    var observer = null;
    var lc = null;
    // Overlays come with the picker: surfaces without one (node maps,
    // analytics, home, customizer) show none.
    var overlays = opts.controlPosition ? _overlaySet(map) : null;
    // Marks this map's tile pane as having a picker: its Auto filter (like
    // its Auto layer) ignores the pin.
    if (opts.controlPosition) {
      try { var tp = map.getPane && map.getPane('tilePane'); if (tp && tp.setAttribute) tp.setAttribute('data-mc-picker', 'true'); } catch (_) {}
    }

    function themeIsDark() {
      return typeof window.MC_isDarkTheme === 'function' ? !!window.MC_isDarkTheme() : _isDark();
    }

    function tilesOf(l) {
      return (l && typeof l.getLayers === 'function') ? l.getLayers() : [l];
    }

    function sync() {
      if (destroyed) return;
      var r;
      // On a map with a picker the Auto group is always the theme's map: the
      // pin is shown as the explicit layer, never loaded into the hidden group.
      try { r = window.MC_resolveTile(themeIsDark(), opts.controlPosition ? { ignorePin: true } : undefined); } catch (_) { return; }
      if (!r || !r.url) return;
      // A vector entry is built as a raster fallback, so its url can't be
      // swapped in place. A vector layer stuck on a transient fallback is
      // rebuilt on the next sync once MapLibre is ready.
      if (!layer || _needsRebuild(cur, r) || (r.format !== 'raster' && !_sameTile(cur, r)) || _vectorStale(layer)) {
        var next = window.MC_buildTileLayer(r);
        if (layer) { try { group.removeLayer(layer); } catch (_) {} }
        layer = next;
        group.addLayer(layer);
      } else if (!_sameTile(cur, r)) {
        var tiles = tilesOf(layer);
        // refUrl is unchanged here, so only the base layer's url can differ.
        if (tiles[0] && typeof tiles[0].setUrl === 'function') tiles[0].setUrl(r.url);
        tiles.forEach(function (t) {
          if (t && t.options) _setAttribution(map, t, r.attribution);
        });
      }
      cur = r;
      // Applied on every sync: the filter can change while the tiles don't
      // (an 'any' entry kept across a theme flip).
      var pane = null;
      try { pane = map.getPane && map.getPane('tilePane'); } catch (_) { pane = null; }
      if (typeof window.MC_applyTileFilter === 'function') window.MC_applyTileFilter(pane || undefined);
      if (map.attributionControl && map.attributionControl._update) {
        try { map.attributionControl._update(); } catch (_) { /* map not ready */ }
      }
    }

    function onProviderChanged(e) {
      // Async config can add/remove/replace overlays (the picker rebuilds too).
      if (overlays && e && e.detail && e.detail.fromConfig) overlays.sync();
      // A pick for the other theme doesn't change these tiles.
      var type = e && e.detail && e.detail.type;
      if (type && type !== (themeIsDark() ? 'dark' : 'light')) return;
      sync();
    }

    sync();
    try { window.addEventListener(EVENT_NAME, onProviderChanged); } catch (_) {}
    if (typeof MutationObserver === 'function') {
      try {
        observer = new MutationObserver(function () { sync(); });
        observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
      } catch (_) { observer = null; }
    }
    if (overlays) overlays.sync(); // restore the ticked overlays
    if (opts.controlPosition) lc = _createLayerControl(map, group, opts.controlPosition, overlays, opts.bottomInset);

    function destroy() {
      if (destroyed) return;
      destroyed = true;
      try { window.removeEventListener(EVENT_NAME, onProviderChanged); } catch (_) {}
      if (observer) { try { observer.disconnect(); } catch (_) {} observer = null; }
      if (lc) { lc.detach(); lc = null; }
      if (overlays) { overlays.destroy(); overlays = null; }
      try { map.removeLayer(group); } catch (_) {}
      layer = null;
      cur = null;
    }

    return { group: group, sync: sync, destroy: destroy };
  };


  // ── Cross-tab sync ──────────────────────────────────────────────────────
  // If another tab in the same browser changes the provider, mirror the
  // dispatch + filter-apply here so live map.js / live.js swap tiles too.
  try {
    window.addEventListener('storage', function (e) {
      if (!e || (e.key !== STORAGE_KEY && e.key !== STORAGE_KEY_LIGHT)) return;
      if (!_hasId(e.newValue)) return;
      var detail = { id: e.newValue, provider: REGISTRY[e.newValue], crossTab: true };
      try {
        var ev = (typeof CustomEvent === 'function')
          ? new CustomEvent(EVENT_NAME, { detail: detail })
          : { type: EVENT_NAME, detail: detail };
        window.dispatchEvent(ev);
      } catch (_) { /* dispatch optional */ }
      applyTileFilter();
    });
  } catch (_) { /* addEventListener may not exist in some envs */ }
})();
