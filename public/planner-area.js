/* Repeater Planner — "Area" mode.
 *
 * Self-contained module mounted by the planner page:
 *   var inst = MeshPlannerArea.mount(ctx);   // -> { unmount(), onParams(params) }
 * The user picks an area on the map; GET /api/planner/coverage suggests where
 * new repeaters would best join the existing backbone. Everything drawn goes
 * into ctx.layer; everything in the side panel goes into ctx.panel.
 */
(function () {
  'use strict';

  var DEFAULT_CFG = {
    defaultRangeKm: 20, rangesKm: [5, 10, 20, 30, 50],
    newAntennaM: 10, existingAntennaM: 5, maxAreaKm: 100, maxSites: 3
  };
  var COMP_CLASSES = 8;
  var DISCLAIMER = 'Suggestions from a terrain model (SRTM ~90 m) and greedy search — not optimal and not a survey. Antenna heights of existing nodes are assumed.';

  /* ---------- pure helpers ---------- */

  function num(v) {
    if (v === null || v === undefined || v === '') return NaN;
    return Number(v);
  }
  // Same rules as MeshPlanner.fmtClearance (planner.js); used when ctx has none.
  function fmtClearanceLocal(x) {
    if (x === null || x === undefined || x === '') return '—';
    var v = Number(x);
    if (isNaN(v)) return '—';
    if (v >= 999) return 'full clearance';
    if (v <= -100) return 'terrain blocks the path';
    var r = Math.round(v);
    return (r < 0 ? '−' + Math.abs(r) : String(r)) + '%';
  }
  function clamp(v, lo, hi) { return Math.min(hi, Math.max(lo, v)); }
  function r3(v) { return Math.round(v * 1000) / 1000; }
  function cfgOf(cfg) {
    var o = {}, k;
    for (k in DEFAULT_CFG) o[k] = DEFAULT_CFG[k];
    if (cfg) for (k in cfg) if (cfg[k] !== undefined && cfg[k] !== null) o[k] = cfg[k];
    return o;
  }

  // Leaflet LatLngBounds (or anything with getSouth/...) -> {s,w,n,e}
  function bboxFromBounds(b) {
    return { s: r3(b.getSouth()), w: r3(b.getWest()), n: r3(b.getNorth()), e: r3(b.getEast()) };
  }
  function bboxToString(bb) { return [bb.s, bb.w, bb.n, bb.e].join(','); }
  function bboxFromString(str) {
    if (typeof str !== 'string') return null;
    var p = str.split(',');
    if (p.length !== 4) return null;
    var v = p.map(num);
    for (var i = 0; i < 4; i++) if (!isFinite(v[i])) return null;
    var bb = { s: v[0], w: v[1], n: v[2], e: v[3] };
    if (bb.s >= bb.n || bb.w >= bb.e) return null;
    if (bb.s < -90 || bb.n > 90 || bb.w < -180 || bb.e > 180) return null;
    return bb;
  }
  // Size in km, longitude scaled by cos(mid-latitude).
  // The server snaps the box OUTWARD to a 0.05° grid before checking its size
  // (cmd/server/planner_api.go plannerParseGridBox, same sphere radius), so
  // measure the snapped box here too, or a ~95-100 km box passes here and is
  // refused there.
  var GRID_PER_DEG = 20, GRID_EPS = 1e-9, KM_PER_DEG = 6371 * Math.PI / 180;
  function snapBox(bb) {
    var s = Math.floor(bb.s * GRID_PER_DEG + GRID_EPS), w = Math.floor(bb.w * GRID_PER_DEG + GRID_EPS);
    var n = Math.ceil(bb.n * GRID_PER_DEG - GRID_EPS), e = Math.ceil(bb.e * GRID_PER_DEG - GRID_EPS);
    if (n <= s) n = s + 1;
    if (e <= w) e = w + 1;
    return { s: s / GRID_PER_DEG, w: w / GRID_PER_DEG, n: n / GRID_PER_DEG, e: e / GRID_PER_DEG };
  }
  function bboxSizeKm(bb) {
    var midLat = (bb.s + bb.n) / 2;
    return {
      w: (bb.e - bb.w) * KM_PER_DEG * Math.cos(midLat * Math.PI / 180),
      h: (bb.n - bb.s) * KM_PER_DEG
    };
  }
  function areaTooLarge(bb, cfg) {
    var sz = bboxSizeKm(snapBox(bb));
    return Math.max(sz.w, sz.h) > cfgOf(cfg).maxAreaKm;
  }

  // Normalises loose input (hash strings or numbers) into a full params object.
  function parseAreaParams(params, cfg) {
    var c = cfgOf(cfg), p = params || {};
    var sites = Math.round(num(p.sites));
    var range = num(p.range);
    var hr = num(p.hr), he = num(p.he);
    var ranges = c.rangesKm || [];
    return {
      bbox: bboxFromString(p.bbox),
      sites: isFinite(sites) ? clamp(sites, 1, c.maxSites) : Math.min(3, c.maxSites),
      range: ranges.indexOf(range) >= 0 ? range : c.defaultRangeKm,
      hr: isFinite(hr) && hr > 0 ? clamp(hr, 1, 200) : c.newAntennaM,
      he: isFinite(he) && he > 0 ? clamp(he, 1, 200) : c.existingAntennaM,
      baseline: p.baseline === 'observed' ? 'observed' : 'predicted',
      stale: p.stale === '1' || p.stale === 1 || p.stale === true || p.stale === 'true',
      // View option only: never part of the coverage request.
      marginal: p.marginal === '1' || p.marginal === 1 || p.marginal === true || p.marginal === 'true'
    };
  }
  // Same normalisation, but input is already typed (bbox object) — used for the URL.
  function normState(st, cfg) {
    var s = st || {};
    var src = {
      bbox: typeof s.bbox === 'string' ? s.bbox : (s.bbox ? bboxToString(s.bbox) : ''),
      sites: s.sites, range: s.range, hr: s.hr, he: s.he, baseline: s.baseline, stale: s.stale, marginal: s.marginal
    };
    return parseAreaParams(src, cfg);
  }
  function buildCoverageUrl(params, cfg) {
    var c = cfg || (params && params.cfg);
    var p = normState(params, c);
    if (!p.bbox) return null;
    return '/api/planner/coverage?bbox=' + bboxToString(p.bbox) +
      '&sites=' + p.sites + '&range=' + p.range + '&hr=' + p.hr + '&he=' + p.he +
      '&baseline=' + p.baseline + '&stale=' + (p.stale ? 1 : 0);
  }
  function stateToHash(p) {
    return {
      bbox: p.bbox ? bboxToString(p.bbox) : null,
      sites: String(p.sites), range: String(p.range), hr: String(p.hr), he: String(p.he),
      baseline: p.baseline, stale: p.stale ? '1' : '0', marginal: p.marginal ? '1' : '0'
    };
  }
  function plural(n, one, many) { return n + ' ' + (n === 1 ? one : many); }

  function reasonText(site, nodes) {
    var g = (site && site.gain) || {};
    var joined = g.components_joined || 0, cuts = g.cut_vertices_removed || 0, reach = g.repeaters_reached || 0;
    var reason = site && site.reason;
    if (!(reason === 'joins' && joined) && !(reason === 'redundancy' && cuts) && !(reason === 'reach' && reach)) {
      reason = joined ? 'joins' : cuts ? 'redundancy' : reach ? 'reach' : null;
    }
    if (reason === 'joins') {
      var ids = {}, added = 0, i;
      (site.joins_components || []).forEach(function (id) { if (id !== 0) ids[id] = true; });
      for (i = 0; i < (nodes || []).length; i++) if (ids[nodes[i].component]) added++;
      var t = 'Joins ' + plural(joined, 'cluster', 'clusters');
      return added > 0 ? t + ' (adds ' + plural(added, 'repeater', 'repeaters') + ' to the main backbone)' : t;
    }
    if (reason === 'redundancy') return 'Removes ' + plural(cuts, 'single point of failure', 'single points of failure');
    if (reason === 'reach') return 'Reaches ' + plural(reach, 'more repeater', 'more repeaters');
    return 'Adds link coverage in this area';
  }

  function summaryRows(b, f) {
    b = b || {}; f = f || {};
    var rows = [
      { label: 'Clusters', before: b.components, after: f.components },
      { label: 'Largest cluster', before: b.largest_component, after: f.largest_component },
      { label: 'Isolated repeaters', before: b.isolated, after: f.isolated },
      { label: 'Single points of failure', before: b.cut_vertices, after: f.cut_vertices }
    ];
    if ((b.stale_excluded || 0) > 0 || (f.stale_excluded || 0) > 0) {
      rows.push({ label: 'Stale excluded', before: b.stale_excluded || 0, after: f.stale_excluded || 0 });
    }
    return rows;
  }

  function nodeLabel(n) { return (n && (n.name || (n.pubkey ? String(n.pubkey).slice(0, 8) : ''))) || 'Unnamed repeater'; }

  function stateLabel(s) {
    s = String(s || '').toLowerCase();
    if (/queue|pend/.test(s)) return 'Queued';
    if (/terrain|load|fetch/.test(s)) return 'Loading terrain';
    return 'Computing';
  }

  function errorText(err) {
    var st = err && err.status, msg = (err && (err.code || err.message)) || '';
    if (st === 400 && /no_nodes_in_area/.test(msg + ' ' + (err.message || ''))) return 'No known repeaters in this area';
    if (st === 429 || st === 503) {
      var s = Math.ceil(Number(err.retryAfter) || 10);
      return 'Busy, try again in ' + s + ' s';
    }
    if (st === 400) return 'The planner rejected these settings: ' + (err.message || 'bad request');
    return 'Could not compute suggestions' + (err && err.message ? ': ' + err.message : '');
  }

  // Resolves a site's links to {name,lat,lon,hb,label} peers.
  function siteLegs(site, nodes, sites, he) {
    var legs = [];
    (site.links || []).forEach(function (l) {
      var peer = null;
      if (l.node >= 0 && nodes[l.node]) {
        var n = nodes[l.node];
        peer = { lat: n.lat, lon: n.lon, name: nodeLabel(n), hb: he };
      } else if (l.node === -1) {
        for (var i = 0; i < sites.length; i++) if (sites[i].step === l.peer_step) {
          peer = { lat: sites[i].lat, lon: sites[i].lon, name: 'New site #' + sites[i].step, hb: sites[i].antenna_m };
        }
      }
      if (peer) legs.push({ link: l, peer: peer });
    });
    return legs;
  }

  /* ---------- HTML builders (escape through the injected esc) ---------- */

  function controlsHtml(c) {
    var ranges = (c.rangesKm || []).map(function (r) {
      return '<option value="' + Number(r) + '">' + Number(r) + ' km</option>';
    }).join('');
    return '' +
      '<div class="mc-pa">' +
      '<div class="mc-pa-row">' +
        '<div class="mc-pa-label" id="mc-pa-area-label">Area</div>' +
        '<div class="mc-pa-area-actions">' +
          '<button type="button" class="btn mc-pa-btn" data-act="useview">Use map view</button>' +
          '<button type="button" class="btn mc-pa-btn" data-act="draw" aria-pressed="false">Draw area</button>' +
        '</div>' +
        '<p class="mc-pa-hint" data-f="areaText" aria-live="polite"></p>' +
      '</div>' +
      '<div class="mc-pa-grid">' +
        '<label class="mc-pa-field">Suggested sites<input type="number" data-f="sites" min="1" max="' + Number(c.maxSites) + '" step="1" inputmode="numeric"></label>' +
        '<label class="mc-pa-field">Range<select data-f="range">' + ranges + '</select></label>' +
        '<label class="mc-pa-field">New repeater antenna (m)<input type="number" data-f="hr" min="1" max="200" step="1" inputmode="numeric"></label>' +
        '<label class="mc-pa-field">Existing antenna (m)<input type="number" data-f="he" min="1" max="200" step="1" inputmode="numeric"></label>' +
      '</div>' +
      '<fieldset class="mc-pa-fieldset"><legend>Baseline</legend>' +
        '<label class="mc-pa-check"><input type="radio" name="mc-pa-baseline" data-f="baseline" value="predicted"> Observed + predicted links</label>' +
        '<label class="mc-pa-check"><input type="radio" name="mc-pa-baseline" data-f="baseline" value="observed"> Observed links only</label>' +
      '</fieldset>' +
      '<label class="mc-pa-check"><input type="checkbox" data-f="stale"> Include stale repeaters</label>' +
      '<button type="button" class="btn mc-pa-primary" data-act="run">Find sites</button>' +
      '<div class="mc-pa-progress-wrap" data-f="progress" hidden>' +
        '<div class="mc-pa-progress-head"><span data-f="progressLabel" aria-live="polite">Queued</span>' +
        '<button type="button" class="btn-link mc-pa-cancel" data-act="cancel">Cancel</button></div>' +
        '<div class="mc-pa-progress" role="progressbar" aria-valuemin="0" aria-valuemax="100"><div class="mc-pa-progress-bar" data-f="progressBar"></div></div>' +
      '</div>' +
      '<div class="mc-pa-error" data-f="error" role="alert" hidden></div>' +
      '<div class="mc-pa-results" data-f="results"></div>' +
      '<p class="mc-pa-disclaimer">' + DISCLAIMER + '</p>' +
      '</div>';
  }

  function fmtN(v) { return v === null || v === undefined ? '—' : String(v); }

  function countMarginal(data) {
    var n = 0;
    (data.edges || []).forEach(function (e) { if (e.class === 'marginal') n++; });
    return n;
  }
  function legendItem(key, svg, label, hidden) {
    return '<li data-legend="' + key + '"' + (hidden ? ' hidden' : '') + '>' +
      '<svg class="mc-pa-lg-sw" viewBox="0 0 28 10" aria-hidden="true" focusable="false">' + svg + '</svg>' + label + '</li>';
  }
  // Swatches carry the same classes as the map paths, so they cannot drift.
  function legendHtml(showMarginal) {
    var line = function (cls) { return '<line class="' + cls + '" x1="2" y1="5" x2="26" y2="5"></line>'; };
    return '<ul class="mc-pa-legend" aria-label="Map legend">' +
      legendItem('observed', line('mc-pa-edge mc-pa-edge-observed'), 'Observed link', false) +
      legendItem('solid', line('mc-pa-edge mc-pa-edge-solid'), 'Predicted link', false) +
      legendItem('marginal', line('mc-pa-edge mc-pa-edge-marginal'), 'Marginal link', !showMarginal) +
      legendItem('site', line('mc-pa-link mc-pa-link-solid'), 'New site link', false) +
      legendItem('stale', '<circle class="mc-pa-node mc-pa-comp-7 mc-pa-stale" cx="14" cy="5" r="4"></circle>', 'Stale repeater', false) +
      '</ul>';
  }

  function resultsHtml(data, hp, esc, fmtC) {
    var out = '', nodes = data.nodes || [], sites = data.sites || [];
    var he = hp && hp.he;
    fmtC = fmtC || fmtClearanceLocal;

    var banner;
    if (data.status === 'no_gain') banner = ['mc-pa-banner-neutral', 'No site in this area improves the backbone at these settings'];
    else if (data.status === 'connected') banner = ['mc-pa-banner-info', 'Backbone is already connected — sites below add redundancy'];
    else banner = ['mc-pa-banner-good', 'Suggested sites improve the backbone'];
    out += '<div class="mc-pa-banner ' + banner[0] + '" role="status">' + banner[1] + '</div>';

    var nMarg = countMarginal(data);
    out += '<div class="mc-pa-mapopts">' +
      '<label class="mc-pa-check"><input type="checkbox" data-f="marginal"' + (hp && hp.marginal ? ' checked' : '') + '> Show marginal links' +
      (nMarg ? ' <span class="mc-pa-count">(' + nMarg + ')</span>' : '') + '</label>' +
      legendHtml(!!(hp && hp.marginal)) + '</div>';

    out += '<table class="mc-pa-summary"><caption class="sr-only">Backbone before and after</caption><thead><tr><th scope="col"></th><th scope="col">Before</th><th scope="col">After</th></tr></thead><tbody>';
    summaryRows(data.baseline, data.final).forEach(function (r) {
      var better = r.after !== r.before && r.label !== 'Largest cluster' && r.label !== 'Stale excluded' ? (r.after < r.before) : (r.label === 'Largest cluster' && r.after > r.before);
      out += '<tr><th scope="row">' + r.label + '</th><td>' + fmtN(r.before) + '</td><td class="' + (better ? 'mc-pa-better' : '') + '">' + fmtN(r.after) + '</td></tr>';
    });
    out += '</tbody></table>';

    if (data.truncated) out += '<p class="mc-pa-note">Area has more repeaters than the planner considers; the nearest 300 were used.</p>';
    if (data.warnings && data.warnings.length) {
      out += '<ul class="mc-pa-warnings">' + data.warnings.map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('') + '</ul>';
    }

    if (sites.length) {
      out += '<ol class="mc-pa-sites">';
      sites.forEach(function (s) {
        out += '<li class="mc-pa-card" data-step="' + Number(s.step) + '" tabindex="0">' +
          '<div class="mc-pa-card-head"><span class="mc-pa-badge" aria-hidden="true">' + Number(s.step) + '</span>' +
          '<span class="mc-pa-card-title">#' + Number(s.step) + ' · ' + Math.round(s.elev_m) + ' m elevation · ' + Math.round(s.antenna_m) + ' m mast</span></div>' +
          '<p class="mc-pa-reason">' + esc(reasonText(s, nodes)) + '</p>';
        var legs = siteLegs(s, nodes, sites, he);
        if (legs.length) {
          out += '<ul class="mc-pa-links">';
          legs.forEach(function (lg) {
            out += '<li>→ ' + esc(lg.peer.name) + ' · ' + Number(lg.link.distance_km).toFixed(1) + ' km · ' +
              esc(fmtC(lg.link.clearance_pct)) +
              (Number(lg.link.clearance_pct) >= 999 || Number(lg.link.clearance_pct) <= -100 ? '' : ' clearance') + ' (' + esc(lg.link.class) + ')</li>';
          });
          out += '</ul>';
        }
        out += '<div class="mc-pa-card-actions">' +
          '<button type="button" class="btn mc-pa-btn" data-act="zoom" data-step="' + Number(s.step) + '">Zoom</button>' +
          '<button type="button" class="btn mc-pa-btn" data-act="profiles" data-step="' + Number(s.step) + '"' + (legs.length ? '' : ' disabled') + '>Show profiles</button>' +
          '</div></li>';
      });
      out += '</ol>';
    }
    return out;
  }

  /* ---------- mount ---------- */

  function mount(ctx) {
    var cfg = cfgOf(ctx.cfg);
    var fmtC = typeof ctx.fmtClearance === 'function' ? ctx.fmtClearance : fmtClearanceLocal;
    var map = ctx.map, panel = ctx.panel, esc = ctx.esc;
    var root = L.layerGroup().addTo(ctx.layer);       // my own sub-group
    var areaLayer = L.layerGroup().addTo(root);
    var edgeLayer = L.layerGroup().addTo(root);       // redrawn alone when marginal links toggle
    var resultLayer = L.layerGroup().addTo(root);
    var state = parseAreaParams(ctx.params, cfg);
    var data = null, siteLayers = {}, abort = null, runId = 0;
    var drawing = null;                               // {start, rect, pid, tapStart, ...}
    var dead = false;
    function stateKey(p) { return buildCoverageUrl(p, cfg) || 'none'; }

    panel.innerHTML = controlsHtml(cfg);
    function f(name) { return panel.querySelector('[data-f="' + name + '"]'); }

    /* --- controls <-> state --- */
    function syncControls() {
      f('sites').value = state.sites;
      f('range').value = String(state.range);
      f('hr').value = state.hr;
      f('he').value = state.he;
      f('stale').checked = state.stale;
      var radios = panel.querySelectorAll ? panel.querySelectorAll('[data-f="baseline"]') : [];
      for (var i = 0; i < radios.length; i++) radios[i].checked = radios[i].value === state.baseline;
      updateAreaInfo();
    }
    function updateAreaInfo() {
      var txt = f('areaText'), btn = panel.querySelector('[data-act="run"]');
      if (!state.bbox) {
        txt.textContent = 'No area chosen. Use the current map view or draw a rectangle.';
        txt.classList.remove('mc-pa-bad');
        btn.disabled = true;
        return;
      }
      var sz = bboxSizeKm(snapBox(state.bbox));
      var size = Math.round(sz.w) + ' × ' + Math.round(sz.h) + ' km';
      if (areaTooLarge(state.bbox, cfg)) {
        txt.textContent = 'Area is ' + size + '; each side must be ' + cfg.maxAreaKm + ' km or less. Zoom in or draw a smaller area.';
        txt.classList.add('mc-pa-bad');
        btn.disabled = true;
      } else {
        txt.textContent = 'Area: ' + size;
        txt.classList.remove('mc-pa-bad');
        btn.disabled = false;
      }
    }
    function readControls() {
      var next = parseAreaParams({
        bbox: state.bbox ? bboxToString(state.bbox) : '',
        sites: f('sites').value, range: f('range').value, hr: f('hr').value, he: f('he').value,
        baseline: state.baseline, stale: false
      }, cfg);
      next.marginal = state.marginal;
      var checked = panel.querySelector('[data-f="baseline"]:checked');
      next.baseline = checked && checked.value === 'observed' ? 'observed' : 'predicted';
      next.stale = !!f('stale').checked;
      next.bbox = state.bbox;
      return next;
    }
    function pushHash() { ctx.setParams(stateToHash(state)); }
    function setBbox(bb) {
      state.bbox = bb;
      drawAreaRect();
      updateAreaInfo();
      pushHash();
    }

    /* --- map drawing --- */
    function drawAreaRect() {
      areaLayer.clearLayers();
      if (!state.bbox) return;
      L.rectangle([[state.bbox.s, state.bbox.w], [state.bbox.n, state.bbox.e]],
        { className: 'mc-pa-area', interactive: false, fill: true }).addTo(areaLayer);
    }
    function clearResults() {
      resultLayer.clearLayers();
      edgeLayer.clearLayers();
      siteLayers = {};
      data = null;
      f('results').innerHTML = '';
    }

    // Edge layer only: marginal links are hidden unless asked for. No refetch.
    function renderEdges(d) {
      edgeLayer.clearLayers();
      var nodes = d.nodes || [];
      (d.edges || []).forEach(function (e) {
        if (e.class === 'marginal' && !state.marginal) return;
        var a = nodes[e.a], b = nodes[e.b];
        if (!a || !b) return;
        L.polyline([[a.lat, a.lon], [b.lat, b.lon]], { className: 'mc-pa-edge mc-pa-edge-' + e.class, interactive: false }).addTo(edgeLayer);
      });
    }
    function syncMarginalUi() {
      var cb = panel.querySelector('[data-f="marginal"]');
      if (cb) cb.checked = !!state.marginal;
      var row = panel.querySelector('[data-legend="marginal"]');
      if (row) row.hidden = !state.marginal;
    }
    // Frame the area and every suggested site; keep clear of the side panel
    // when it overlaps the map (it is a sibling today, so usually no padding).
    function fitResult(d) {
      var pts = [];
      if (state.bbox) pts.push([state.bbox.s, state.bbox.w], [state.bbox.n, state.bbox.e]);
      (d.sites || []).forEach(function (s) { pts.push([s.lat, s.lon]); });
      if (!pts.length || !map.fitBounds) return;
      var pad = { paddingTopLeft: [24, 24], paddingBottomRight: [24, 24], animate: false };
      try {
        var side = panel.closest && panel.closest('.mc-pl-side');
        var mc = map.getContainer && map.getContainer();
        if (side && mc && side.getBoundingClientRect && mc.getBoundingClientRect) {
          var sr = side.getBoundingClientRect(), mr = mc.getBoundingClientRect();
          var mobile = typeof window !== 'undefined' && window.innerWidth < 768;
          if (mobile) {
            if (sr.top < mr.bottom && sr.bottom > mr.top) pad.paddingBottomRight[1] += mr.bottom - Math.max(sr.top, mr.top);
          } else if (sr.left < mr.right && sr.right > mr.left) {
            pad.paddingBottomRight[0] += mr.right - Math.max(sr.left, mr.left);
          }
        }
      } catch (e) { /* padding is best effort */ }
      map.fitBounds(pts, pad);
    }

    function renderMap(d) {
      resultLayer.clearLayers();
      siteLayers = {};
      var nodes = d.nodes || [];
      renderEdges(d);
      nodes.forEach(function (n) {
        var cls = 'mc-pa-node mc-pa-comp-' + (Math.abs(n.component || 0) % COMP_CLASSES) +
          (n.stale ? ' mc-pa-stale' : '') + (n.in_area === false ? ' mc-pa-out' : '');
        var m = L.circleMarker([n.lat, n.lon], { radius: n.in_area === false ? 4 : 6, className: cls, weight: 2 });
        m.bindTooltip(esc(nodeLabel(n)) + (n.stale ? ' (stale)' : ''));
        m.addTo(resultLayer);
      });
      (d.sites || []).forEach(function (s) {
        var lines = [];
        siteLegs(s, nodes, d.sites, 0).forEach(function (lg) {
          lines.push(L.polyline([[s.lat, s.lon], [lg.peer.lat, lg.peer.lon]],
            { className: 'mc-pa-link mc-pa-link-' + (lg.link.class === 'marginal' ? 'marginal' : 'solid'), interactive: false }).addTo(resultLayer));
        });
        var icon = L.divIcon({ className: 'mc-pa-site', html: '<span>' + Number(s.step) + '</span>', iconSize: [30, 30], iconAnchor: [15, 15] });
        var mk = L.marker([s.lat, s.lon], { icon: icon, keyboard: false, title: 'Suggested site ' + Number(s.step) }).addTo(resultLayer);
        mk.bindTooltip('Suggested site #' + Number(s.step));
        mk.on('mouseover', function () { highlight(s.step, true); });
        mk.on('mouseout', function () { highlight(s.step, false); });
        siteLayers[s.step] = { marker: mk, lines: lines };
      });
    }
    function toggleCls(layer, on) {
      var el = layer && layer.getElement && layer.getElement();
      if (el && el.classList) el.classList[on ? 'add' : 'remove']('mc-pa-hl');
    }
    function highlight(step, on) {
      var sl = siteLayers[step];
      if (!sl) return;
      toggleCls(sl.marker, on);
      sl.lines.forEach(function (l) { toggleCls(l, on); });
      if (on && sl.marker.setZIndexOffset) sl.marker.setZIndexOffset(1000);
      else if (sl.marker.setZIndexOffset) sl.marker.setZIndexOffset(0);
    }

    /* --- run --- */
    function setBusy(label, pct) {
      var w = f('progress');
      w.hidden = false;
      f('progressLabel').textContent = label;
      var bar = f('progressBar');
      if (pct === null || pct === undefined || !isFinite(pct)) {
        bar.classList.add('mc-pa-indeterminate');
        bar.style.width = '';
      } else {
        bar.classList.remove('mc-pa-indeterminate');
        bar.style.width = clamp(pct, 0, 100) + '%';
        var pb = panel.querySelector('[role="progressbar"]');
        if (pb && pb.setAttribute) pb.setAttribute('aria-valuenow', String(Math.round(clamp(pct, 0, 100))));
      }
    }
    function setIdle() { f('progress').hidden = true; }
    function showError(msg) { var e = f('error'); e.textContent = msg; e.hidden = !msg; }

    function cancelRun() {
      if (abort) { abort.abort(); abort = null; }
      runId++;
      setIdle();
    }

    function run(opts) {
      var url = buildCoverageUrl(state, cfg);
      if (!url || areaTooLarge(state.bbox, cfg)) return;
      cancelRun();
      var myRun = ++runId;
      var ac = typeof AbortController !== 'undefined' ? new AbortController() : { signal: undefined, abort: function () {} };
      abort = ac;
      showError('');
      clearResults();
      setBusy('Queued', null);
      if (opts && opts.fit && state.bbox) {
        map.fitBounds([[state.bbox.s, state.bbox.w], [state.bbox.n, state.bbox.e]], { animate: false });
      }
      var snapshot = { he: state.he };
      ctx.fetchJob(url, {
        signal: ac.signal,
        onProgress: function (st, pr) {
          if (myRun !== runId) return;
          var pct = pr === null || pr === undefined ? null : Number(pr);
          if (pct !== null && isFinite(pct) && pct <= 1) pct *= 100;
          setBusy(stateLabel(st), pct);
        }
      }).then(function (d) {
        if (myRun !== runId || dead) return;
        abort = null;
        setIdle();
        data = d;
        renderMap(d);
        f('results').innerHTML = resultsHtml(d, { he: snapshot.he, marginal: state.marginal }, esc, fmtC);
        fitResult(d);
      }, function (err) {
        if (myRun !== runId || dead) return;
        if (err && err.name === 'AbortError') return;
        abort = null;
        setIdle();
        showError(errorText(err));
      });
    }

    /* --- draw-area mode (Pointer Events: mouse, touch and pen) --- */
    var TAP_PX = 6;                                   // movement under this is a tap, not a drag
    var DRAW_HINT = 'Drag, or tap two corners, to set the area. Esc cancels.';
    function pointLatLng(e) { return map.mouseEventToLatLng(e); }
    function resetGesture() {
      if (!drawing) return;
      if (drawing.rect) drawAreaRect();               // drop the rubber band, show the previous area
      drawing.start = null; drawing.rect = null; drawing.pid = null; drawing.tapStart = false;
    }
    function releaseCapture(e) {
      var c = map.getContainer && map.getContainer();
      try { if (c && c.releasePointerCapture) c.releasePointerCapture(e.pointerId); } catch (x) { /* already released */ }
    }
    function finishDraw(e) {
      var b = L.latLngBounds(drawing.start, pointLatLng(e));
      stopDraw();
      if (Math.abs(b.getNorth() - b.getSouth()) < 0.005 || Math.abs(b.getEast() - b.getWest()) < 0.005) {
        drawAreaRect();           // too small: ignore, restore previous
        return;
      }
      setBbox(bboxFromBounds(b));
    }
    function onPtrDown(e) {
      if (!drawing) return;
      if (e.isPrimary === false) { resetGesture(); return; }       // second finger (pinch): cancel cleanly
      if (e.pointerType === 'mouse' && e.button !== 0) return;
      if (drawing.pid !== null) return;
      var c = drawing.container;
      try { if (c.setPointerCapture) c.setPointerCapture(e.pointerId); } catch (x) { /* synthetic or lost pointer */ }
      drawing.pid = e.pointerId;
      drawing.px = e.clientX; drawing.py = e.clientY;
      if (!drawing.rect) {
        var ll = pointLatLng(e);
        drawing.start = ll;
        drawing.rect = L.rectangle([ll, ll], { className: 'mc-pa-area mc-pa-drawing', interactive: false }).addTo(areaLayer);
      }
    }
    function onPtrMove(e) {
      if (!drawing || !drawing.rect || e.isPrimary === false) return;
      if (drawing.pid === e.pointerId || (drawing.tapStart && drawing.pid === null)) {
        drawing.rect.setBounds(L.latLngBounds(drawing.start, pointLatLng(e)));
      }
    }
    function onPtrUp(e) {
      if (!drawing || drawing.pid === null || drawing.pid !== e.pointerId) return;
      releaseCapture(e);
      var moved = Math.max(Math.abs(e.clientX - drawing.px), Math.abs(e.clientY - drawing.py)) >= TAP_PX;
      drawing.pid = null;
      if (drawing.tapStart || moved) { finishDraw(e); return; }
      drawing.tapStart = true;                         // first tap: corner set, wait for the second
    }
    function onPtrCancel(e) {
      if (!drawing || drawing.pid !== e.pointerId) return;
      releaseCapture(e);
      resetGesture();
    }
    function onKey(e) {
      if (e.key === 'Escape' && drawing) { stopDraw(); drawAreaRect(); }
    }
    var PTR_EVENTS = [['pointerdown', onPtrDown], ['pointermove', onPtrMove], ['pointerup', onPtrUp], ['pointercancel', onPtrCancel]];
    function startDraw() {
      if (drawing) { stopDraw(); drawAreaRect(); return; }
      var c = map.getContainer();
      drawing = { start: null, rect: null, pid: null, tapStart: false, px: 0, py: 0, container: c,
        wasDragging: map.dragging.enabled ? !!map.dragging.enabled() : true };
      if (drawing.wasDragging) map.dragging.disable();
      if (c.classList) c.classList.add('mc-pa-drawing-map');
      PTR_EVENTS.forEach(function (p) { c.addEventListener(p[0], p[1]); });
      document.addEventListener('keydown', onKey);
      f('areaText').textContent = DRAW_HINT;
      var b = panel.querySelector('[data-act="draw"]');
      b.setAttribute('aria-pressed', 'true'); b.textContent = 'Cancel drawing';
    }
    function stopDraw() {
      if (!drawing) return;
      var d = drawing;
      drawing = null;
      PTR_EVENTS.forEach(function (p) { d.container.removeEventListener(p[0], p[1]); });
      document.removeEventListener('keydown', onKey);
      if (d.wasDragging) map.dragging.enable();
      if (d.container.classList) d.container.classList.remove('mc-pa-drawing-map');
      if (!dead) updateAreaInfo();
      var b = panel.querySelector('[data-act="draw"]');
      if (b) { b.setAttribute('aria-pressed', 'false'); b.textContent = 'Draw area'; }
    }

    /* --- panel events --- */
    function stepOf(target) {
      var el = target && target.closest ? target.closest('[data-step]') : null;
      return el ? Number(el.getAttribute('data-step')) : null;
    }
    function siteByStep(step) {
      var ss = (data && data.sites) || [];
      for (var i = 0; i < ss.length; i++) if (ss[i].step === step) return ss[i];
      return null;
    }
    function onClick(e) {
      var btn = e.target && e.target.closest ? e.target.closest('[data-act]') : null;
      if (!btn || btn.disabled) return;
      var act = btn.getAttribute('data-act');
      if (act === 'useview') { stopDraw(); setBbox(bboxFromBounds(map.getBounds())); }
      else if (act === 'draw') startDraw();
      else if (act === 'run') { state = readControls(); pushHash(); run(); }
      else if (act === 'cancel') cancelRun();
      else if (act === 'zoom' || act === 'profiles') {
        var s = siteByStep(Number(btn.getAttribute('data-step')));
        if (!s) return;
        var legs = siteLegs(s, data.nodes || [], data.sites || [], state.he);
        if (act === 'zoom') {
          var pts = [[s.lat, s.lon]].concat(legs.map(function (l) { return [l.peer.lat, l.peer.lon]; }));
          map.fitBounds(pts, { padding: [40, 40], maxZoom: 12 });
        } else {
          ctx.showProfiles(legs.map(function (lg) {
            return { a: { lat: s.lat, lon: s.lon, name: 'New site #' + s.step }, b: { lat: lg.peer.lat, lon: lg.peer.lon, name: lg.peer.name }, ha: s.antenna_m, hb: lg.peer.hb };
          }), 'Site #' + s.step + ' links');
        }
      }
    }
    function setMarginal(on) {
      state.marginal = !!on;
      if (data) renderEdges(data);
      syncMarginalUi();
      pushHash();
    }
    function onChange(e) {
      var t = e && e.target;
      if (t && t.getAttribute && t.getAttribute('data-f') === 'marginal') { setMarginal(t.checked); return; }
      state = readControls();
      updateAreaInfo();
      pushHash();
    }
    function onOver(e) { var st = stepOf(e.target); if (st !== null) highlight(st, true); }
    function onOut(e) { var st = stepOf(e.target); if (st !== null) highlight(st, false); }

    panel.addEventListener('click', onClick);
    panel.addEventListener('change', onChange);
    panel.addEventListener('mouseover', onOver);
    panel.addEventListener('mouseout', onOut);
    panel.addEventListener('focusin', onOver);
    panel.addEventListener('focusout', onOut);

    /* --- hash params --- */
    function applyParams(params, fit) {
      var next = parseAreaParams(params, cfg);
      if (stateKey(next) === stateKey(state)) { // echo of our own hash write, or a view-only change
        if (next.marginal !== state.marginal) {
          state.marginal = next.marginal;
          if (data) renderEdges(data);
          syncMarginalUi();
        }
        return;
      }
      state = next;
      syncControls();
      drawAreaRect();
      if (state.bbox) { run({ fit: fit }); }
      else { cancelRun(); clearResults(); }
    }

    syncControls();
    drawAreaRect();
    if (state.bbox) run({ fit: true });

    return {
      onParams: function (params) { if (!dead) applyParams(params || {}, true); },
      unmount: function () {
        if (dead) return;
        dead = true;
        cancelRun();
        stopDraw();
        panel.removeEventListener('click', onClick);
        panel.removeEventListener('change', onChange);
        panel.removeEventListener('mouseover', onOver);
        panel.removeEventListener('mouseout', onOut);
        panel.removeEventListener('focusin', onOver);
        panel.removeEventListener('focusout', onOut);
        ctx.layer.clearLayers();
        panel.innerHTML = '';
      }
    };
  }

  window.MeshPlannerArea = {
    mount: mount,
    _internals: {
      bboxFromBounds: bboxFromBounds,
      bboxSizeKm: bboxSizeKm,
      areaTooLarge: areaTooLarge,
      snapBox: snapBox,
      buildCoverageUrl: buildCoverageUrl,
      reasonText: reasonText,
      summaryRows: summaryRows,
      parseAreaParams: parseAreaParams,
      resultsHtml: resultsHtml,
      errorText: errorText,
      fmtClearance: fmtClearanceLocal,
      legendHtml: legendHtml
    }
  };
})();
