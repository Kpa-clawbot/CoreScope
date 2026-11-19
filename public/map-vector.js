/* map-vector.js — vector (MapLibre GL) basemaps inside Leaflet (#2428).
 *
 * Uses Leaflet internals (map._addZoomLimit/_removeZoomLimit, _loaded,
 * _transitionEnd, layer._map) and the binding's _glMap: recheck them on any
 * Leaflet or maplibre-gl-leaflet upgrade.
 *
 *   MC_loadMapLibre() -> Promise<maplibregl>
 *     Injects, once, MapLibre's stylesheet, its CSP build and the Leaflet
 *     binding from vendor/ (constant paths + SRI), and points MapLibre at its
 *     vendored worker (once). Never runs at page load: only when a vector map
 *     is picked. The promise is cached and rejects only on a script error
 *     event, never on time; after a failure one later call retries, replacing
 *     only the tags that errored. Pending tags are never removed or doubled.
 *
 *   MC_buildVectorLayer(desc, opts) -> L.LayerGroup
 *     desc = { url, attribution, maxZoom, type }  (url: MapLibre style JSON)
 *     opts.fallback() -> raster L.Layer
 *     Returns a group at once; the GL layer is mounted into it once the
 *     library is loaded and the group is on a map. No WebGL, a failed load,
 *     no library within LOAD_TIMEOUT_MS of this layer's build (a per-layer
 *     timer; the load goes on for later layers), a style that errors before
 *     its first load/idle, or a lost GL context swap the GL layer for
 *     opts.fallback(), once. group.mcFallbackReason names why (else null).
 *     The group registers its maxZoom/minZoom with the map on add and
 *     unregisters on remove (as a GridLayer does), so the map has a zoom
 *     limit before, while and after GL is shown.
 *
 *   MC_vectorLayerStale(layer) -> boolean
 *     True when layer fell back for a transient reason (load-timeout,
 *     load-failed) and MapLibre has loaded since; callers rebuild it then.
 *
 * Security: MapLibre's RTL text plugin is never loaded; its attribution control
 * stays off (it renders HTML from remote style sources); every URL MapLibre
 * requests passes transformRequest, which refuses anything but https (or http
 * on an http page), and refuses this page's origin (cookie-bearing) unless the
 * style itself is served from it. No config value reaches a script or link URL.
 */
(function () {
  'use strict';

  // ── VENDOR INTEGRITY TABLE ──────────────────────────────────────────────
  // Paths are relative to the page. tests/unit/test-vendored-libs.js parses
  // the path/integrity pairs between these markers and checks each against
  // the file on disk. The worker cannot carry SRI in the browser (new Worker
  // has no integrity option); it is listed so the test still pins its bytes.
  // VENDOR-INTEGRITY-BEGIN
  var VENDOR = {
    css:      { path: 'vendor/maplibre-gl-5.24.0/maplibre-gl.css', integrity: 'sha384-uTttxo/aOKbdE5RlD/SPzSDoDmNvGlUYPjONi2MN/b7c9HPSvW07OIuyP7uL6jxK' },
    maplibre: { path: 'vendor/maplibre-gl-5.24.0/maplibre-gl-csp.js', integrity: 'sha384-+lDFpEYaS5ofD0qW/5dA+duOMWMSGs+utCY+wx9H/lS1i0WZ0bQH31giSmqYsxqR' },
    worker:   { path: 'vendor/maplibre-gl-5.24.0/maplibre-gl-csp-worker.js', integrity: 'sha384-wBzwLUk46fiEnNOOlHXeylfQrL/SzyNqSD0gYdQJskF9cbBaNcRDYn6ZmSBW4l0z' },
    binding:  { path: 'vendor/maplibre-gl-leaflet-0.1.4/leaflet-maplibre-gl.js', integrity: 'sha384-tXYNKOHx4T02jMP7YYCtBxPIv1B5gaA5mcVPBzqMp6d7VzWzxJgI2aWF/nJLrQdS' },
  };
  // VENDOR-INTEGRITY-END

  var LOAD_TIMEOUT_MS = 15000;
  var TAG_ATTR = 'data-mc-maplibre';

  // ── Loader ──────────────────────────────────────────────────────────────
  // A script tag cannot be cancelled: once inserted it downloads and runs
  // even if removed. So each script is inserted at most once per page and
  // left alone while pending; only a tag whose error event fired (it never
  // ran) is replaced, on the one retry. The shared promise rejects only on
  // such an error; a slow load is a per-layer timeout (buildVectorLayer),
  // so a library that arrives late still serves later layers.
  //
  // The binding captures window.maplibregl when it executes. It is only
  // usable if it ran after MapLibre: boundLib records what it captured.
  var loadPromise = null;
  var loadFailures = 0;
  var libTag = null;      // the MapLibre <script>, or null
  var bindingTag = null;  // the binding <script>, or null
  var boundLib = null;    // window.maplibregl when the binding last ran
  var workerLib = null;   // the maplibregl setWorkerUrl was called on
  var attempt = null;     // { resolve, reject } of the in-flight load

  function libraryReady() {
    var lib = window.maplibregl;
    return !!(lib && typeof lib.setWorkerUrl === 'function' && boundLib === lib &&
      window.L && typeof window.L.maplibreGL === 'function');
  }

  function finishLoad() {
    var lib = window.maplibregl;
    if (workerLib !== lib) {
      lib.setWorkerUrl(new URL(VENDOR.worker.path, document.baseURI).href);
      workerLib = lib;
    }
    return lib;
  }

  function settle(err) {
    var a = attempt;
    if (!a) return;
    attempt = null;
    if (err) { a.reject(err); return; }
    try { a.resolve(finishLoad()); } catch (e) { a.reject(e); }
  }

  function makeTag(tag, entry) {
    var el = document.createElement(tag);
    el.setAttribute(TAG_ATTR, '');
    el.integrity = entry.integrity;
    el.crossOrigin = 'anonymous';
    if (tag === 'link') {
      el.rel = 'stylesheet';
      el.href = entry.path;
    } else {
      // Dynamic scripts default to async; async=false keeps the two in
      // insertion order while both download in parallel.
      el.async = false;
      el.src = entry.path;
    }
    return el;
  }

  function head() {
    return document.head || document.getElementsByTagName('head')[0];
  }

  // Replaces a dead (errored) tag; a pending or loaded one is never removed.
  function injectScript(entry, old) {
    if (old && old.mcState === 'error' && old.parentNode) old.parentNode.removeChild(old);
    var el = makeTag('script', entry);
    el.mcState = 'pending';
    head().appendChild(el);
    return el;
  }

  function injectLib() {
    var el = injectScript(VENDOR.maplibre, libTag);
    libTag = el;
    el.onload = function () {
      el.mcState = window.maplibregl ? 'loaded' : 'error';
      if (el.mcState === 'error') settle(new Error('MapLibre loaded without its expected globals'));
    };
    el.onerror = function () {
      el.mcState = 'error';
      settle(new Error('Failed to load ' + VENDOR.maplibre.path));
    };
  }

  function injectBinding() {
    var el = injectScript(VENDOR.binding, bindingTag);
    bindingTag = el;
    el.onload = function () {
      el.mcState = 'loaded';
      if (el !== bindingTag) return;
      boundLib = window.maplibregl || null;
      if (libraryReady()) { settle(); return; }
      // Ran ahead of a MapLibre still pending (queued after this binding on
      // a retry): run the binding again, queued behind it.
      if (!boundLib && libTag && libTag.mcState === 'pending') { injectBinding(); return; }
      settle(new Error('MapLibre loaded without its expected globals'));
    };
    el.onerror = function () {
      el.mcState = 'error';
      if (el === bindingTag) settle(new Error('Failed to load ' + VENDOR.binding.path));
    };
  }

  function startLoad() {
    return new Promise(function (resolve, reject) {
      attempt = { resolve: resolve, reject: reject };
      if (libraryReady()) { settle(); return; }
      // The stylesheet only styles MapLibre's own elements; a failure is not fatal.
      if (!document.querySelector('link[' + TAG_ATTR + ']')) head().appendChild(makeTag('link', VENDOR.css));
      if (!window.maplibregl && (!libTag || libTag.mcState === 'error')) injectLib();
      // A pending binding is reused (its onload re-queues it if it runs
      // ahead of MapLibre); one that errored or ran without MapLibre is
      // replaced, queued behind MapLibre.
      if (!bindingTag || bindingTag.mcState === 'error' ||
          (bindingTag.mcState === 'loaded' && (!boundLib || boundLib !== window.maplibregl))) {
        injectBinding();
      }
    });
  }

  function loadMapLibre() {
    if (loadPromise) return loadPromise;
    loadPromise = startLoad().catch(function (err) {
      loadFailures++;
      if (loadFailures < 2) loadPromise = null; // allow one later retry
      throw err;
    });
    return loadPromise;
  }

  // ── Request policy (S4) ─────────────────────────────────────────────────
  function resolveUrl(url) {
    try { return new URL(String(url), document.baseURI); } catch (e) { return null; }
  }

  function isAllowedUrl(url) {
    var u = resolveUrl(url);
    if (!u) return false;
    if (u.protocol === 'https:') return true;
    return u.protocol === 'http:' && window.location.protocol === 'http:';
  }

  // MapLibre 5.24 passes style-supplied URLs (sprite, glyphs, sources,
  // tiles) to transformRequest as written: nothing is resolved against the
  // style URL, so a relative one is fetched against this page. Requests to
  // this origin carry its cookies, so a style from another host may not
  // reach it; a style hosted on this origin may.
  //
  // MapLibre 5 awaits transformRequest inside each resource load and turns a
  // rejection into that resource's error event, so a rejected promise cancels
  // the request before any network access. (A falsy return makes MapLibre use
  // the original URL; { url: '' } would fetch the page; a synchronous throw
  // for the style fires its error inside the Map constructor, before any
  // listener can see it.)
  function makeTransformRequest(styleUrl) {
    var style = resolveUrl(styleUrl);
    var styleSameOrigin = !!style && style.origin === window.location.origin;
    return function transformRequest(url) {
      if (!isAllowedUrl(url)) {
        return Promise.reject(new Error('Blocked map resource URL (scheme not allowed): ' + String(url).slice(0, 64)));
      }
      if (!styleSameOrigin && resolveUrl(url).origin === window.location.origin) {
        return Promise.reject(new Error('Blocked map resource URL (page origin from a cross-origin style): ' + String(url).slice(0, 64)));
      }
      return { url: url };
    };
  }

  // The binding's _transitionEnd (also run by _resize) defers its work to a
  // frame that dereferences this._map and this._glMap, both null once the
  // layer is removed. Defer through our own frame, check the layer is still
  // mounted, then run the original with Leaflet's frame helper made
  // synchronous so its body runs inside this checked frame. Its other
  // handlers run synchronously from Leaflet events (unbound on removal) or,
  // like the throttled _update, already return when _map is null.
  function guardTransitionEnd(layer) {
    var original = layer._transitionEnd;
    if (typeof original !== 'function') return;
    layer._transitionEnd = function (e) {
      L.Util.requestAnimFrame(function () {
        if (!this._map || !this._glMap) return;
        var util = L.Util;
        var raf = util.requestAnimFrame;
        util.requestAnimFrame = function (fn, ctx) { fn.call(ctx); return 0; };
        try { original.call(this, e); } finally { util.requestAnimFrame = raf; }
      }, this);
    };
  }

  // ── WebGL probe ─────────────────────────────────────────────────────────
  var webglAvailable = null;
  function hasWebGL() {
    if (webglAvailable !== null) return webglAvailable;
    try {
      var canvas = document.createElement('canvas');
      var gl = canvas.getContext('webgl2') || canvas.getContext('webgl');
      webglAvailable = !!gl;
      // Hand the probe's context back now: browsers cap live contexts (~16).
      var lose = gl && gl.getExtension('WEBGL_lose_context');
      if (lose) lose.loseContext();
    } catch (e) {
      webglAvailable = false;
    }
    return webglAvailable;
  }

  // ── Layer ───────────────────────────────────────────────────────────────
  var warned = {};
  function warnOnce(reason, err) {
    if (warned[reason]) return;
    warned[reason] = true;
    console.warn('[map-vector] vector basemap unavailable (' + reason + '); using the raster fallback', err || '');
  }

  // ── Zoom limits ─────────────────────────────────────────────────────────
  // Leaflet folds a layer's min/maxZoom into the map's limits only for a
  // GridLayer (its beforeAdd/onRemove call map._addZoomLimit/_removeZoomLimit);
  // neither a layer group nor the MapLibre binding (a plain L.Layer) does. A
  // map showing only a vector basemap would have no maxZoom, and
  // markercluster's onAdd throws without one. The group registers itself the
  // same way a GridLayer does: in beforeAdd, which Map.addLayer runs
  // synchronously (before the map is even loaded, before MapLibre arrives),
  // and in onRemove. These are private Map methods, stable in the vendored
  // Leaflet 1.9.4 and the ones Leaflet's own layers use; the alternatives
  // (map.options.maxZoom, owned by the page, or a dummy GridLayer with its
  // own pane container) are not symmetric or cost more.
  function zoomOptions(desc) {
    var o = {};
    var max = Number(desc.maxZoom);
    var min = Number(desc.minZoom);
    if (desc.maxZoom != null && isFinite(max) && max > 0) o.maxZoom = max;
    if (desc.minZoom != null && isFinite(min) && min >= 0) o.minZoom = min;
    return o;
  }

  function registerZoomLimit(group) {
    var leafletOnRemove = group.onRemove;
    group.beforeAdd = function (map) {
      if (typeof map._addZoomLimit !== 'function') return;
      map._addZoomLimit(group);
      // Map.removeLayer skips onRemove while the map is not loaded yet.
      if (!map._loaded) {
        map.whenReady(function () { if (!map.hasLayer(group)) map._removeZoomLimit(group); });
      }
    };
    group.onRemove = function (map) {
      if (typeof leafletOnRemove === 'function') leafletOnRemove.call(group, map);
      if (typeof map._removeZoomLimit === 'function') map._removeZoomLimit(group);
    };
  }

  // A fallback for these reasons may not happen again on a later build:
  // the library was slow or a script failed, not the browser or the style.
  var TRANSIENT_FALLBACK = { 'load-timeout': true, 'load-failed': true };

  // True when `layer` is a vector group that fell back for a transient
  // reason and MapLibre has loaded since: a fresh build would mount GL. The
  // caller decides when to rebuild (a re-pick, a sync); the layer never
  // rebuilds itself.
  function vectorLayerStale(layer) {
    return !!(layer && TRANSIENT_FALLBACK[layer.mcFallbackReason] === true &&
      libraryReady() && workerLib === window.maplibregl);
  }

  function buildVectorLayer(desc, opts) {
    desc = desc || {};
    var fallback = opts && typeof opts.fallback === 'function' ? opts.fallback : null;
    var styleUrl = typeof desc.url === 'function' ? desc.url() : desc.url;
    var attribution = typeof desc.attribution === 'string' ? desc.attribution : '';
    var group = L.layerGroup([], zoomOptions(desc));
    registerZoomLimit(group);
    // Why this layer shows its raster fallback (null while it doesn't).
    group.mcFallbackReason = null;
    var onMap = false;
    var ready = false;
    var fellBack = false;
    var glLayer = null;
    var timer = null;

    // Leaflet's attribution control reads this when the group is added.
    group.getAttribution = function () { return fellBack ? '' : attribution; };

    function useFallback(reason, err) {
      if (fellBack) return;
      warnOnce(reason, err);
      group.mcFallbackReason = reason;
      var map = group._map;
      if (map && map.attributionControl && attribution) map.attributionControl.removeAttribution(attribution);
      fellBack = true;
      if (glLayer) {
        var gl = glLayer;
        glLayer = null;
        try { group.removeLayer(gl); } catch (e) { /* already torn down */ }
      }
      var raster = fallback ? fallback() : null;
      if (raster) group.addLayer(raster);
    }

    // Wired on every add: the binding builds a fresh MapLibre map each time
    // the layer joins a map, and its onRemove calls map.remove(), which
    // releases the WebGL context.
    function wire(layer) {
      var glMap = layer.getMaplibreMap();
      if (!glMap) return;
      var firstRender = false;
      function rendered() { firstRender = true; }
      glMap.once('load', rendered);
      glMap.once('idle', rendered);
      glMap.on('error', function (e) {
        if (!firstRender && layer === glLayer) useFallback('style-error', e && e.error);
      });
      glMap.on('webglcontextlost', function () {
        if (layer === glLayer) useFallback('context-lost');
      });
    }

    function mount() {
      if (fellBack || glLayer || !ready || !onMap) return;
      var layer = L.maplibreGL({
        style: styleUrl,
        attributionControl: false,
        interactive: false,
        transformRequest: makeTransformRequest(styleUrl),
      });
      // Before the add: onAdd binds this._transitionEnd by reference.
      guardTransitionEnd(layer);
      glLayer = layer;
      layer.on('add', function () { wire(layer); });
      try {
        group.addLayer(layer);
      } catch (err) {
        // MapLibre's constructor threw (no GL context after all). The
        // binding's onRemove assumes a MapLibre map; give it an inert one.
        if (!layer.getMaplibreMap()) layer._glMap = { remove: function () {} };
        useFallback('init-failed', err);
      }
    }

    group.on('add', function () { onMap = true; mount(); });
    group.on('remove', function () { onMap = false; });

    if (typeof styleUrl !== 'string' || !styleUrl || !isAllowedUrl(styleUrl)) {
      useFallback('style-url-refused');
    } else if (!hasWebGL()) {
      useFallback('webgl-unavailable');
    } else {
      // Each layer waits LOAD_TIMEOUT_MS on its own; the shared load goes
      // on, so a later layer can still use a library that arrives late.
      timer = setTimeout(function () {
        useFallback('load-timeout', new Error('MapLibre did not load within ' + (LOAD_TIMEOUT_MS / 1000) + ' s'));
      }, LOAD_TIMEOUT_MS);
      loadMapLibre().then(function () {
        clearTimeout(timer);
        ready = true;
        mount();
      }, function (err) {
        clearTimeout(timer);
        useFallback('load-failed', err);
      });
    }
    return group;
  }

  window.MC_loadMapLibre = loadMapLibre;
  window.MC_buildVectorLayer = buildVectorLayer;
  window.MC_vectorLayerStale = vectorLayerStale;
})();
