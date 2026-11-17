/* Repeater Planner — "Coverage" mode.
 *
 * Self-contained module mounted by the planner page:
 *   var inst = MeshPlannerCoverage.mount(ctx);   // -> { unmount(), setSite({lat, lon, node}) }
 * The planner routes map clicks and node picks to setSite. One
 * GET /api/planner/viewshed job returns a byte grid holding both layers
 * (users | repeaters << 4); it is painted into an L.imageOverlay, and a layer
 * switch or theme change repaints it without a new request.
 */
(function () {
  'use strict';

  var DEFAULT_CFG = {
    defaultRangeKm: 30, rangesKm: [10, 15, 20, 30, 40, 50, 60],
    newAntennaM: 10, existingAntennaM: 5, userReach: { d50Km: 2.7, slope: 0.3 }
  };
  // Same bounds as the server (planner_api.go plannerMinAntennaM / plannerMaxAntennaM).
  var HEIGHT_MIN = 1, HEIGHT_MAX = 100;
  var LAYERS = { users: 5, repeaters: 3 };          // highest class code per layer
  // Lowest painted code. Users class 1 (< 10%) and Repeaters class 1 (blocked)
  // are left transparent: they would otherwise tint the whole range disc.
  var LAYER_MIN = { users: 2, repeaters: 2 };
  // Repeaters classes from the first-Fresnel clearance, same bounds as the
  // server (planner_viewshed.go repeaterClass / viewshedNearClearance).
  var NEAR_PCT = -20;
  var REP_CLASSES = ['', 'Blocked by terrain', 'Slightly blocked (up to 20% of the Fresnel zone)', 'Line of sight'];   // codes 0..3
  var REP_NOTE = 'Checked against real links: line of sight predicted the links heard up to about 20 km; beyond that it is not enough on its own.';
  var USER_CLASSES = ['≥ 75%', '50–75%', '25–50%', '10–25%'];   // codes 5..2
  var HEIGHT_NOTE = 'Height mainly changes reach to other repeaters; user reach is measured and barely depends on height.';
  var OUTDATED_NOTE = 'Outdated, press Compute coverage.';
  var DISCLAIMER = 'An estimate from a terrain model (SRTM ~90 m) and a measured reach curve, not a survey.';
  var PANE = 'mc-pl-cov';
  // The last result URL the map was fitted to, across mounts: coming back to
  // the tab with the same inputs keeps the user's pan and zoom.
  var lastFitUrl = null;

  /* ---------- pure helpers ---------- */

  function num(v) {
    if (v === null || v === undefined || v === '') return NaN;
    return Number(v);
  }
  function cfgOf(cfg) {
    var o = {}, k;
    for (k in DEFAULT_CFG) o[k] = DEFAULT_CFG[k];
    if (cfg) for (k in cfg) if (cfg[k] !== undefined && cfg[k] !== null) o[k] = cfg[k];
    return o;
  }
  function fmt4(v) { return Number(v).toFixed(4); }

  function decodeCells(b64) {
    var bin = atob(b64 || '');
    var out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }
  // L.imageOverlay stretches its image linearly in Web Mercator y, while the
  // grid's rows are equal latitude bands (north first). For each of rows
  // canvas rows spaced evenly in Mercator between s and n, the grid row whose
  // band holds the canvas row's centre latitude.
  function mercY(lat) { return Math.log(Math.tan(Math.PI / 4 + lat * Math.PI / 360)); }
  function mercatorRowMap(s, n, rows) {
    var yN = mercY(n), yS = mercY(s), out = new Array(rows);
    for (var j = 0; j < rows; j++) {
      var lat = (2 * Math.atan(Math.exp(yN + (j + 0.5) / rows * (yS - yN))) - Math.PI / 2) * 180 / Math.PI;
      out[j] = Math.min(rows - 1, Math.max(0, Math.floor((n - lat) / (n - s) * rows)));
    }
    return out;
  }
  function cellCode(b, layer) { return layer === 'repeaters' ? (b >> 4) & 15 : b & 15; }
  function colorVarFor(layer, code) {
    if (!(code >= LAYER_MIN[layer] && code <= LAYERS[layer])) return null;
    return '--pl-cov-' + (layer === 'repeaters' ? 'rep' : 'user') + '-' + code;
  }

  function parseSite(str) {
    if (typeof str !== 'string') return null;
    var p = str.split(',');
    if (p.length !== 2) return null;
    var lat = num(p[0]), lon = num(p[1]);
    if (!isFinite(lat) || !isFinite(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180) return null;
    return { lat: lat, lon: lon };
  }
  function parseHeight(v, def) {
    var n = num(v);
    return isFinite(n) && n >= HEIGHT_MIN && n <= HEIGHT_MAX ? n : def;
  }
  // Normalises hash strings (or numbers) into the module state.
  function parseCoverageParams(params, cfg) {
    var c = cfgOf(cfg), p = params || {};
    var range = num(p.range);
    return {
      site: parseSite(p.site),
      h: parseHeight(p.h, c.newAntennaM),
      rx: parseHeight(p.rx, c.existingAntennaM),
      range: (c.rangesKm || []).indexOf(range) >= 0 ? range : c.defaultRangeKm,
      layer: p.layer === 'repeaters' ? 'repeaters' : 'users'
    };
  }
  function siteString(site) { return site ? fmt4(site.lat) + ',' + fmt4(site.lon) : null; }
  function stateToHash(st) {
    return { site: siteString(st.site), h: String(st.h), rx: String(st.rx), range: String(st.range), layer: st.layer };
  }
  function buildViewshedUrl(st) {
    if (!st || !st.site) return null;
    return '/api/planner/viewshed?site=' + siteString(st.site) + '&h=' + st.h + '&rx=' + st.rx + '&range=' + st.range;
  }

  // From the link's clearance_pct, which the server rounds to 0.1%: within
  // 0.05% of a bound a link can land one class off the raw-clearance grid
  // (a cell centre is never exactly at the repeater anyway). List swatch,
  // line and dot all use this, so they always agree with each other.
  function linkClass(k) {
    if (!k || k.error || k.clearance_pct == null || !isFinite(Number(k.clearance_pct))) return 0;
    var c = Number(k.clearance_pct);
    return c >= 0 ? 3 : c >= NEAR_PCT ? 2 : 1;
  }

  /* ---------- HTML builders ---------- */

  function controlsHtml(c) {
    var ranges = (c.rangesKm || []).map(function (r) {
      return '<option value="' + Number(r) + '">' + Number(r) + ' km</option>';
    }).join('');
    function height(f, label) {
      return '<label class="mc-pa-field">' + label + '<input type="number" data-f="' + f + '" min="' + HEIGHT_MIN + '" max="' + HEIGHT_MAX + '" step="1" inputmode="numeric"></label>';
    }
    return '' +
      '<div class="mc-pa mc-pl-cov">' +
      '<p class="mc-pl-lede">See what a repeater at one spot would reach.</p>' +
      '<div class="mc-pa-row">' +
        '<div class="mc-pa-label">Site</div>' +
        '<p class="mc-pa-hint" data-f="siteText" aria-live="polite"></p>' +
      '</div>' +
      height('h', 'Antenna (m)') +
      '<p class="mc-pa-hint">' + HEIGHT_NOTE + '</p>' +
      '<label class="mc-pa-field">Range<select data-f="range">' + ranges + '</select></label>' +
      '<fieldset class="mc-pa-fieldset"><legend>Layer</legend>' +
        '<label class="mc-pa-check"><input type="radio" name="mc-pl-cov-layer" data-f="layer" value="users"> Users (handheld at 1.5 m)</label>' +
        '<label class="mc-pa-check"><input type="radio" name="mc-pl-cov-layer" data-f="layer" value="repeaters"> Repeaters</label>' +
      '</fieldset>' +
      '<div data-f="rxWrap">' + height('rx', 'Repeaters at (m)') + '</div>' +
      '<div class="mc-pl-cov-actions">' +
        '<button type="button" class="btn mc-pa-primary" data-act="run">Compute coverage</button>' +
        '<div data-f="progress"></div>' +
        '<div data-f="outdated"></div>' +
      '</div>' +
      '<div data-f="error"></div>' +
      '<div data-f="legend"></div>' +
      '<div data-f="results"></div>' +
      '<p class="mc-pa-disclaimer">' + DISCLAIMER + '</p>' +
      '</div>';
  }

  function swatch(layer, code) {
    return '<span class="mc-pl-cov-sw mc-pl-cov-sw-' + (layer === 'repeaters' ? 'rep' : 'user') + '-' + code + '" aria-hidden="true"></span>';
  }
  function legendHtml(layer, st, cfg) {
    var items = [], i;
    var intro;
    if (layer === 'repeaters') {
      intro = 'Line of sight to another repeater at ' + Number(st.rx) + ' m:';
      for (i = LAYERS.repeaters; i >= LAYER_MIN.repeaters; i--) items.push('<li>' + swatch(layer, i) + REP_CLASSES[i] + '</li>');
    } else {
      var d50 = Number(cfgOf(cfg).userReach && cfgOf(cfg).userReach.d50Km);
      intro = 'Chance that a handheld at 1.5 m hears the site' +
        (isFinite(d50) && d50 > 0 ? ' (measured reach: 50% at ' + d50 + ' km; terrain removes deep shadow)' : '') + ':';
      for (i = 5; i >= LAYER_MIN.users; i--) items.push('<li>' + swatch(layer, i) + USER_CLASSES[5 - i] + '</li>');
    }
    return '<p class="mc-pa-hint">' + intro + '</p><ul class="mc-pl-cov-legend" aria-label="Map legend">' + items.join('') + '</ul>' +
      (layer === 'repeaters' ? '<p class="mc-pa-hint">' + REP_NOTE + '</p>' : '');
  }

  function linksHtml(d, esc) {
    var links = (d && d.links) || [];
    var out = '';
    if (!links.length) return '<p class="mc-pa-hint">No known repeaters within range.</p>';
    out += '<p class="mc-pa-label">Links to known repeaters</p><ul class="mc-pl-cov-links">';
    links.forEach(function (l) {
      var k = l.link || {};
      var cls = linkClass(k);
      var verdict = cls ? REP_CLASSES[cls] : 'No terrain data';
      out += '<li>' + (cls ? swatch('repeaters', cls) : '') +
        '<span>' + esc(l.name || String(l.pubkey || '').slice(0, 8) || 'Unnamed repeater') + ' · ' +
        Number(k.distance_km).toFixed(1) + ' km · ' + esc(verdict) + '</span></li>';
    });
    out += '</ul>';
    if (d.truncated) out += '<p class="mc-pa-note">More repeaters in range than the planner considers; the farthest were left out.</p>';
    return out;
  }

  /* ---------- mount ---------- */

  function mount(ctx) {
    var cfg = cfgOf(ctx.cfg);
    var map = ctx.map, panel = ctx.panel, esc = ctx.esc;
    if (map.getPane && !map.getPane(PANE)) {
      // Under the node pane (390) and the overlay pane (400), so lines and nodes stay on top.
      var pane = map.createPane(PANE);
      if (pane && pane.style) { pane.style.zIndex = '350'; pane.style.pointerEvents = 'none'; }
    }
    var root = L.layerGroup().addTo(ctx.layer);
    var rasterLayer = L.layerGroup().addTo(root);
    var linkLayer = L.layerGroup().addTo(root);
    var siteLayer = L.layerGroup().addTo(root);
    var state = parseCoverageParams(ctx.params, cfg);
    var siteName = null;
    var data = null, cells = null, ranUrl = null, abort = null, runId = 0, dead = false, outdated = false;
    var themeObs = null;

    panel.innerHTML = controlsHtml(cfg);
    function f(name) { return panel.querySelector('[data-f="' + name + '"]'); }

    function syncControls() {
      f('h').value = state.h;
      f('rx').value = state.rx;
      f('range').value = String(state.range);
      var radios = panel.querySelectorAll('[data-f="layer"]');
      for (var i = 0; i < radios.length; i++) radios[i].checked = radios[i].value === state.layer;
      f('rxWrap').hidden = state.layer !== 'repeaters';
      f('siteText').textContent = state.site
        ? (siteName ? siteName + ' · ' : '') + siteString(state.site)
        : 'Click the map or a node to place the site.';
      f('legend').innerHTML = legendHtml(state.layer, state, cfg);
      panel.querySelector('[data-act="run"]').disabled = !state.site;
      f('outdated').innerHTML = outdated ? '<p class="mc-pa-note">' + OUTDATED_NOTE + '</p>' : '';
    }
    function pushHash() { ctx.setParams(stateToHash(state)); }

    /* --- map --- */
    // The site the shown result was computed for (the server rounds it to
    // 0.001°), or where the user placed it while there is no current result.
    function shownSite() {
      var s = data && !outdated && data.params && data.params.site;
      return s && isFinite(s.lat) && isFinite(s.lon) ? s : state.site;
    }
    function drawSite() {
      siteLayer.clearLayers();
      var s = shownSite();
      if (!s) return;
      L.circleMarker([s.lat, s.lon], { radius: 8, className: 'mc-pl-cov-site', interactive: false }).addTo(siteLayer);
    }
    function readColors(layer) {
      var cs = getComputedStyle(document.documentElement);
      var out = [null];
      for (var code = 1; code <= LAYERS[layer]; code++) out.push(code < LAYER_MIN[layer] ? null : (cs.getPropertyValue(colorVarFor(layer, code)).trim() || null));
      return { colors: out, opacity: parseFloat(cs.getPropertyValue('--pl-cov-opacity')),
        dimmed: parseFloat(cs.getPropertyValue('--pl-cov-opacity-outdated')) };
    }
    // CSS colour string → RGBA bytes, through the canvas so any CSS colour syntax works.
    function rgbaOf(g, color) {
      g.clearRect(0, 0, 1, 1);
      g.fillStyle = color;
      g.fillRect(0, 0, 1, 1);
      return g.getImageData(0, 0, 1, 1).data;
    }
    function dim(theme) {
      if (!outdated) return isFinite(theme.opacity) ? theme.opacity : 0.5;
      return isFinite(theme.dimmed) ? theme.dimmed : 0.2;
    }
    function paint() {
      rasterLayer.clearLayers();
      if (!data || !cells) return;
      var rows = data.rows, cols = data.cols, b = data.bbox;
      if (!(rows > 0 && cols > 0) || cells.length < rows * cols || !b || b.length !== 4) return;
      var theme = readColors(state.layer);
      var canvas = document.createElement('canvas');
      canvas.width = cols;
      canvas.height = rows;
      var g = canvas.getContext('2d');
      var rgba = theme.colors.map(function (c) { return c ? Array.prototype.slice.call(rgbaOf(g, c)) : null; });
      g.clearRect(0, 0, 1, 1);
      var img = g.createImageData(cols, rows), px = img.data;
      var srcRow = mercatorRowMap(b[0], b[2], rows);
      for (var j = 0; j < rows; j++) {
        for (var k = 0; k < cols; k++) {
          var c = rgba[cellCode(cells[srcRow[j] * cols + k], state.layer)];
          if (!c) continue;
          var o = (j * cols + k) * 4;
          px[o] = c[0]; px[o + 1] = c[1]; px[o + 2] = c[2]; px[o + 3] = c[3];
        }
      }
      g.putImageData(img, 0, 0);
      L.imageOverlay(canvas.toDataURL(), [[b[0], b[1]], [b[2], b[3]]], {
        opacity: dim(theme), interactive: false, pane: PANE, className: 'mc-pl-cov-overlay'
      }).addTo(rasterLayer);
    }
    function drawLinks() {
      linkLayer.clearLayers();
      var site = shownSite();
      if (!data || outdated || !site || state.layer !== 'repeaters') return;
      (data.links || []).forEach(function (l) {
        var k = l.link || {};
        var cls = linkClass(k);
        if (!cls) return;
        // Blocked links get only the red dot: a line per blocked repeater
        // buries the useful ones.
        if (cls > 1) {
          L.polyline([[site.lat, site.lon], [l.lat, l.lon]], {
            className: 'mc-pl-cov-link mc-pl-cov-link-' + cls, interactive: false
          }).addTo(linkLayer);
        }
        // Recolour the repeater's dot by its link class; not interactive, so
        // the planner's node tooltip and click underneath keep working.
        L.circleMarker([l.lat, l.lon], {
          radius: 6, weight: 2, className: 'mc-pl-cov-node mc-pl-cov-node-' + cls, interactive: false
        }).addTo(linkLayer);
      });
    }
    function renderResults() {
      var html = '';
      if (data) {
        html += ctx.warningsHtml ? ctx.warningsHtml(data.warnings) : '';
        if (state.layer === 'repeaters') html += linksHtml(data, esc);
      }
      var list = f('results');
      list.innerHTML = html;
      list.className = data && outdated ? 'is-outdated' : '';
    }
    function repaint() { paint(); drawSite(); drawLinks(); renderResults(); }

    /* --- run --- */
    function setProgress(st, frac) { f('progress').innerHTML = st === null ? '' : ctx.progressHtml(st, frac); }
    function showError(msg) { f('error').innerHTML = msg ? ctx.errorHtml(msg) : ''; }
    function errorText(err) {
      var msg = err && err.message;
      if (err && err.status === 400 && msg === 'out_of_area') return 'This site is too far from the known mesh for terrain data.';
      if (err && err.status === 400 && msg === 'too_many_tiles') return 'This range needs more terrain tiles than the server allows. Choose a shorter range.';
      return ctx.jobErrorText(err);
    }
    function cancelRun() {
      if (abort) { abort.abort(); abort = null; }
      runId++;
      setProgress(null);
    }
    function clearResult() {
      data = null; cells = null;
      rasterLayer.clearLayers();
      linkLayer.clearLayers();
      f('results').innerHTML = '';
    }
    function run(opts) {
      var url = buildViewshedUrl(state);
      cancelRun();
      clearResult();
      outdated = false;
      syncControls();
      showError('');
      if (!url) return;
      var myRun = ++runId;
      ranUrl = url;
      var ac = new AbortController();
      abort = ac;
      setProgress('queued', null);
      ctx.fetchJob(url, {
        signal: ac.signal,
        onProgress: function (st, pr) { if (myRun === runId && !dead) setProgress(st, pr); }
      }).then(function (d) {
        if (myRun !== runId || dead) return;
        abort = null;
        setProgress(null);
        data = d;
        cells = decodeCells(d.cells);
        outdated = buildViewshedUrl(state) !== ranUrl;
        syncControls();
        repaint();
        if (opts && opts.fit && url !== lastFitUrl && map.fitBounds && d.bbox) {
          lastFitUrl = url;
          map.fitBounds([[d.bbox[0], d.bbox[1]], [d.bbox[2], d.bbox[3]]], { animate: false });
        }
      }, function (err) {
        if (myRun !== runId || dead) return;
        if (err && err.name === 'AbortError') return;
        abort = null;
        setProgress(null);
        showError(errorText(err));
      });
    }

    // A changed input never fetches: it flags the shown result as outdated
    // (dimmed, link lines hidden) until the button starts the next job.
    // An in-flight run is kept: its result arrives flagged outdated if the inputs moved on.
    function markOutdated() {
      if (!data || outdated) return;
      outdated = true;
      syncControls();
      repaint();
    }
    function setSite(p) {
      if (dead || !p || !isFinite(p.lat) || !isFinite(p.lon)) return;
      state.site = { lat: Number(p.lat), lon: Number(p.lon) };
      siteName = p.node ? (p.node.name || String(p.node.pubkey || p.node.public_key || '').slice(0, 8)) : null;
      syncControls();
      drawSite();
      pushHash();
      markOutdated();
    }
    function setLayer(layer) {
      state.layer = layer === 'repeaters' ? 'repeaters' : 'users';
      syncControls();
      pushHash();
      repaint();
    }

    /* --- panel events --- */
    function onChange(e) {
      var t = e && e.target;
      var name = t && t.getAttribute ? t.getAttribute('data-f') : null;
      if (name === 'layer') { if (t.checked) setLayer(t.value); return; }
      if (name !== 'h' && name !== 'rx' && name !== 'range') return;
      var next = parseCoverageParams({ h: f('h').value, rx: f('rx').value, range: f('range').value }, cfg);
      state.h = next.h; state.rx = next.rx; state.range = next.range;
      syncControls();
      pushHash();
      markOutdated();
    }
    function onClick(e) {
      var btn = e && e.target && e.target.closest ? e.target.closest('[data-act]') : null;
      if (btn && btn.getAttribute('data-act') === 'run' && state.site) run();
    }
    function onTheme() { if (!dead) paint(); }

    panel.addEventListener('change', onChange);
    panel.addEventListener('click', onClick);
    // Same signals the planner map uses: data-theme flips dark/light,
    // theme-refresh follows a customizer colour change.
    if (typeof MutationObserver === 'function') {
      themeObs = new MutationObserver(onTheme);
      themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    }
    window.addEventListener('theme-refresh', onTheme);

    syncControls();
    drawSite();
    if (state.site) run({ fit: true });

    return {
      setSite: setSite,
      unmount: function () {
        if (dead) return;
        cancelRun();
        dead = true;
        panel.removeEventListener('change', onChange);
        panel.removeEventListener('click', onClick);
        if (themeObs) themeObs.disconnect();
        window.removeEventListener('theme-refresh', onTheme);
        ctx.layer.clearLayers();
        panel.innerHTML = '';
      }
    };
  }

  window.MeshPlannerCoverage = {
    mount: mount,
    _internals: {
      decodeCells: decodeCells,
      mercatorRowMap: mercatorRowMap,
      cellCode: cellCode,
      colorVarFor: colorVarFor,
      parseCoverageParams: parseCoverageParams,
      stateToHash: stateToHash,
      buildViewshedUrl: buildViewshedUrl,
      legendHtml: legendHtml,
      linksHtml: linksHtml,
      linkClass: linkClass
    }
  };
})();
