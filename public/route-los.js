/* route-los.js — radio line of sight (LOS) on the map packet-route view.
 *
 * Every leg between two placed hops gets a clear / marginal / blocked chip in
 * the route sidebar, from ONE terrain request per contiguous run of legs
 * (GET /api/elevation/profile; a 202 while the server loads terrain tiles is
 * re-polled, see fetchRequest). Blocked or marginal legs get an obstruction
 * marker on the map. A chip or marker opens a docked terrain-profile panel
 * whose chart drives a crosshair on the map; its antenna and frequency inputs
 * re-analyse every leg locally, with no refetch.
 *
 * The maths and the SVG live in los.js (window.MeshLOS). This file owns the
 * fetching, caching and UI. Inert while window.MC_LOS_CFG is null (the server
 * has LOS off). route-view.js calls update() whenever its rows change and
 * clear() on teardown. Exposes window.MeshRouteLOS.
 */
(function () {
  'use strict';

  var CACHE_MAX = 300;
  var DEBOUNCE_MS = 150;
  var POLL_MIN_S = 1;           // 202 → re-poll after retry_after_s, clamped to these
  var POLL_MAX_S = 10;
  var MAX_POLLS = 30;           // re-polls of one request before it reads as unavailable
  var DEFAULT_MAX_POINTS = 16;
  var MOBILE_MAX_W = 767;
  var SPRITE = '/icons/phosphor-sprite.svg';
  var LS_KEYS = { antA: 'mc-los-ant-a', antB: 'mc-los-ant-b', freq: 'mc-los-freq' };
  var LIMITS = { antA: [0, 300], antB: [0, 300], freq: [100, 6000] };
  var FIELD_ERRORS = {
    antA: 'Antenna A must be 0–300 m.',
    antB: 'Antenna B must be 0–300 m.',
    freq: 'Frequency must be 100–6000 MHz.'
  };
  // Per-leg error codes from the server, plus the client-side whole-request
  // failures (rate_limited = HTTP 429, upstream = anything else, including a
  // 202 that never finished).
  var ERROR_REASONS = {
    unknown_point: 'Node location unknown to the server',
    no_terrain: 'No terrain data for this leg',
    too_many_tiles: 'Route crosses too much terrain for one check',
    rate_limited: 'Terrain lookups are rate-limited; try again shortly',
    upstream: 'Terrain data unavailable'
  };
  // Errors worth a manual retry (never retried automatically).
  var RETRYABLE = { upstream: true, rate_limited: true };

  // Leg results keyed by both endpoints, direction-aware. Only terrain
  // (elev_m) and the deterministic too_long and no_terrain errors are cached:
  // any other error is retried by the next update().
  var cache = new Map();
  var inflight = new Map(); // key -> Promise<result>, so a refetch never doubles up

  var S = {
    gen: 0,           // bumped by every update()/clear(); stale callbacks compare
    map: null, sidebar: null, seq: null,
    legs: [],         // see buildLegs(); plus row, chip, result, analysis, marker
    layer: null,      // own L.layerGroup: obstruction markers, leg highlight, cursor
    settings: null,   // { antA, antB, freq }
    source: null,     // terrain attribution from the last response
    open: null,       // { idx, key } of the leg shown in the panel
    panel: null, legLine: null, cursorMarker: null,
    cursorT: null, ptr: null, rafId: 0, debounce: 0,
    pendingLos: null, // &los= row index waiting for its leg's data
    sheetCollapsed: false,
    cfgWaiting: false, cfgWaited: false, waitCtx: null,
    warned: false
  };

  // ─── Pure helpers (exported via _internals for tests) ───────────────────

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function fmtKm(km) {
    var d = Number(km);
    if (!isFinite(d)) return '?';
    return d < 10 ? d.toFixed(2) : d < 100 ? d.toFixed(1) : String(Math.round(d));
  }

  // Whole metres, never "0 m" for a real (positive) obstruction.
  function fmtObstruction(m) { return String(Math.max(1, Math.round(m))); }

  // Fresnel clearance ratio as text. Below zero the terrain is above the
  // line itself, where a percentage of the zone means nothing.
  function fmtClearance(c) {
    if (c == null || !isFinite(c)) return '—';
    if (c < 0) return 'blocked';
    if (c > 9.99) return '>999%';
    return Math.round(c * 100) + '%';
  }

  function isPlaced(p) {
    if (!p || p.resolved === false || p.gpsless) return false;
    if (p.lat == null || p.lon == null || p.lat === '' || p.lon === '') return false;
    var lat = Number(p.lat), lon = Number(p.lon);
    if (!isFinite(lat) || !isFinite(lon)) return false;
    if (lat === 0 && lon === 0) return false;
    return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180;
  }

  function nameOf(p) {
    return (p && (p.name || (p.pubkey ? String(p.pubkey).slice(0, 8) : ''))) || '?';
  }

  function cacheKey(a, b) {
    return Number(a.lat).toFixed(5) + ',' + Number(a.lon).toFixed(5) + '>' +
      Number(b.lat).toFixed(5) + ',' + Number(b.lon).toFixed(5);
  }

  // The server matches request points against its node table, so each point
  // keeps the coordinates exactly as the position object carries them.
  function legPoint(p) {
    return { lat: Number(p.lat), lon: Number(p.lon), rawLat: String(p.lat), rawLon: String(p.lon) };
  }

  // Leg at row index i joins seq[i-1] → seq[i] when both ends are placed.
  function buildLegs(seq) {
    var legs = [];
    if (!Array.isArray(seq)) return legs;
    for (var i = 1; i < seq.length; i++) {
      if (!isPlaced(seq[i - 1]) || !isPlaced(seq[i])) continue;
      var a = legPoint(seq[i - 1]), b = legPoint(seq[i]);
      legs.push({ idx: i, a: a, b: b, nameA: nameOf(seq[i - 1]), nameB: nameOf(seq[i]), key: cacheKey(a, b) });
    }
    return legs;
  }

  // Contiguous legs (consecutive row indices) share one request; a run longer
  // than maxPoints points is chunked, each chunk starting on the previous
  // chunk's last point. Returns [{ legs: [...], pts: [a, b, ...] }].
  function runsToRequests(legs, maxPoints) {
    var cap = Math.max(2, Math.floor(Number(maxPoints)) || 2);
    var out = [], cur = null;
    for (var i = 0; i < (legs || []).length; i++) {
      var leg = legs[i];
      var joins = cur && leg.idx === cur.legs[cur.legs.length - 1].idx + 1 && cur.pts.length < cap;
      if (!joins) {
        cur = { legs: [], pts: [leg.a] };
        out.push(cur);
      }
      cur.legs.push(leg);
      cur.pts.push(leg.b);
    }
    return out;
  }

  function requestUrl(req) {
    return '/api/elevation/profile?pts=' + req.pts.map(function (p) {
      return (p.rawLat != null ? p.rawLat : String(p.lat)) + ',' + (p.rawLon != null ? p.rawLon : String(p.lon));
    }).join(',');
  }

  function parseLosParam(hash) {
    var h = String(hash || '');
    var qi = h.indexOf('?');
    if (qi < 0) return null;
    var parts = h.slice(qi + 1).split('&');
    for (var i = 0; i < parts.length; i++) {
      var eq = parts[i].indexOf('=');
      if ((eq < 0 ? parts[i] : parts[i].slice(0, eq)) !== 'los') continue;
      var v = eq < 0 ? '' : parts[i].slice(eq + 1);
      if (!/^\d{1,4}$/.test(v)) return null;
      var n = parseInt(v, 10);
      return n >= 1 ? n : null;
    }
    return null;
  }

  // hash with los=<idx> set (idx == null removes it); other params untouched.
  function withLosParam(hash, idx) {
    var h = String(hash || '');
    var qi = h.indexOf('?');
    var base = qi < 0 ? h : h.slice(0, qi);
    var parts = qi < 0 ? [] : h.slice(qi + 1).split('&').filter(function (s) {
      return s && s !== 'los' && s.indexOf('los=') !== 0;
    });
    if (idx != null) parts.push('los=' + idx);
    return base + (parts.length ? '?' + parts.join('&') : '');
  }

  // One leg of a 200 body → { distanceKm, elevM } or { error, distanceKm }.
  function legResultFrom(l) {
    if (!l || typeof l !== 'object') return { error: 'upstream' };
    var d = Number(l.distance_km);
    var dist = l.distance_km != null && isFinite(d) ? d : null;
    if (l.error) {
      var code = String(l.error);
      return { error: code === 'too_long' || ERROR_REASONS[code] ? code : 'upstream', distanceKm: dist };
    }
    var e = l.elev_m;
    if (!Array.isArray(e) || e.length < 2) return { error: 'upstream', distanceKm: dist };
    for (var i = 0; i < e.length; i++) {
      if (typeof e[i] !== 'number' || !isFinite(e[i])) return { error: 'upstream', distanceKm: dist };
    }
    return { distanceKm: dist, elevM: e };
  }

  function isCacheable(r) { return !!r && (!!r.elevM || r.error === 'too_long' || r.error === 'no_terrain'); }

  function verdicts() { return window.MeshLOS.VERDICTS; }

  function errorReason(r) {
    if (r.error === 'too_long') {
      return 'Too far for a terrain check' + (r.distanceKm != null ? ' (' + fmtKm(r.distanceKm) + ' km)' : '');
    }
    return ERROR_REASONS[r.error] || ERROR_REASONS.upstream;
  }

  // Every leg drawn here is a hop of a real packet's resolved path, so the
  // packet was heard across it. A "blocked" model verdict therefore means
  // "terrain in the way of the assumed antennas", not "cannot work"; the
  // planner (which shares MeshLOS.VERDICTS) keeps the hard wording.
  var SOFT_BLOCKED = 'Terrain in path';
  var HEARD_ANYWAY = 'The packet was heard on this hop anyway: the antennas may be higher than assumed, or the hop may be mis-resolved.';

  function fmtAnt(m) { return String(Math.round(Number(m) * 10) / 10); }

  function antennaNote(an) {
    return fmtAnt(an.antennaAM) + ' m / ' + fmtAnt(an.antennaBM) + ' m antennas';
  }

  // r: a MeshLOS.analyze() result, an error result, or null while loading.
  function chipText(r) {
    if (!r) return 'LOS…';
    if (r.error) return 'LOS n/a';
    return r.verdict === 'blocked' ? SOFT_BLOCKED : r.verdict === 'marginal' ? 'LOS marginal' : 'LOS clear';
  }

  function chipTone(r) {
    if (!r || r.error) return 'muted';
    if (r.verdict === 'blocked') return 'warn';
    var v = verdicts()[r.verdict];
    return v ? v.tone : 'muted';
  }

  function verdictSummary(an) {
    var v = verdicts()[an.verdict];
    var ant = ' at ' + antennaNote(an);
    if (an.verdict === 'blocked') {
      var head = an.obstructionM > 0
        ? 'terrain ' + fmtObstruction(an.obstructionM) + ' m above the line' + ant
        : 'terrain in path' + ant;
      return head + '. ' + HEARD_ANYWAY;
    }
    if (an.clearance === Infinity) return v.label + ant;
    return v.label + ' — ' + Math.round(an.clearance * 100) + '% of first Fresnel zone' + ant;
  }

  function chipTitle(r, prevName) {
    var from = 'Line of sight from ' + (prevName || 'the previous hop') + ': ';
    if (!r) return from + 'checking terrain';
    if (r.error) return from + 'n/a — ' + errorReason(r);
    return from + verdictSummary(r);
  }

  // Map marker tooltip for a warn/bad leg.
  function markerText(an, nameA) {
    var ant = ' at ' + antennaNote(an);
    if (an.obstructionM > 0) {
      return 'Terrain ' + fmtObstruction(an.obstructionM) + ' m above line of sight' + ant + ', ' +
        fmtKm(an.worstKm) + ' km from ' + nameA + '. Heard on this hop anyway.';
    }
    return 'Fresnel zone ' + Math.round(an.clearance * 100) + '% clear' + ant;
  }

  function parseSetting(name, raw) {
    if (raw == null || String(raw).trim() === '') return null;
    var v = Number(raw);
    var lim = LIMITS[name];
    if (!lim || !isFinite(v) || v < lim[0] || v > lim[1]) return null;
    return v;
  }

  // ─── Config + settings ──────────────────────────────────────────────────

  function cfg() { return window.MC_LOS_CFG || null; }

  function enabled() {
    return !!cfg() && !!window.MeshLOS && typeof L !== 'undefined';
  }

  function kFactor() {
    var k = Number(cfg() && cfg().kFactor);
    return k > 0 && isFinite(k) ? k : 4 / 3;
  }

  function defaults() {
    var c = cfg() || {};
    var ant = parseSetting('antA', c.antennaHeightM);
    var freq = parseSetting('freq', c.frequencyMhz);
    return { antA: ant == null ? 5 : ant, antB: ant == null ? 5 : ant, freq: freq == null ? 910.525 : freq };
  }

  function lsGet(k) { try { return window.localStorage.getItem(k); } catch (e) { return null; } }
  function lsSet(k, v) { try { window.localStorage.setItem(k, v); } catch (e) { /* storage blocked */ } }
  function lsDel(k) { try { window.localStorage.removeItem(k); } catch (e) { /* storage blocked */ } }

  function loadSettings() {
    var d = defaults(), out = {};
    Object.keys(LS_KEYS).forEach(function (name) {
      var v = parseSetting(name, lsGet(LS_KEYS[name]));
      out[name] = v == null ? d[name] : v;
    });
    return out;
  }

  // ─── Cache + fetch ──────────────────────────────────────────────────────

  function cacheGet(key) {
    if (!cache.has(key)) return null;
    var v = cache.get(key);
    cache.delete(key); // LRU bump
    cache.set(key, v);
    return v;
  }

  function cachePut(key, v) {
    if (cache.has(key)) cache.delete(key);
    while (cache.size >= CACHE_MAX) cache.delete(cache.keys().next().value);
    cache.set(key, v);
  }

  // Seconds to wait before re-polling a 202 body.
  function pollDelayS(body) {
    var s = Number(body && body.retry_after_s);
    if (!isFinite(s)) s = POLL_MIN_S;
    return Math.min(POLL_MAX_S, Math.max(POLL_MIN_S, s));
  }

  // Whether the route on screen still shows a leg of req. Every update()
  // bumps S.gen; one for the same route keeps its legs, while a new route
  // or clear() drops them.
  function stillWanted(req, gen) {
    if (gen === S.gen) return true;
    for (var i = 0; i < S.legs.length; i++) {
      for (var j = 0; j < req.legs.length; j++) if (S.legs[i].key === req.legs[j].key) return true;
    }
    return false;
  }

  // One request; resolves (never rejects) to a result per leg. A 202 means
  // the server is still loading terrain tiles: re-poll the same URL after
  // pollDelayS, at most MAX_POLLS times (the chips stay loading). Polling
  // stops once the route no longer shows the request's legs (stillWanted);
  // a stopped or exhausted request reads as upstream, which is not cached.
  function fetchRequest(req) {
    var url = requestUrl(req);
    var gen = S.gen;
    var polls = 0;
    function failAll(code) { return req.legs.map(function () { return { error: code }; }); }
    function attempt() {
      var p;
      try {
        p = fetch(url, { headers: { Accept: 'application/json' } });
      } catch (e) {
        p = Promise.reject(e);
      }
      return Promise.resolve(p).then(function (r) {
        if (r.status === 429) return failAll('rate_limited');
        if (r.status === 202) {
          if (polls >= MAX_POLLS) return failAll('upstream');
          polls++;
          return Promise.resolve().then(function () { return r.json(); }).catch(function () { return null; })
            .then(function (st) {
              return new Promise(function (resolve) { setTimeout(resolve, pollDelayS(st) * 1000); });
            })
            .then(function () { return stillWanted(req, gen) ? attempt() : failAll('upstream'); });
        }
        if (!r.ok) return failAll('upstream');
        return r.json().then(function (body) {
          var legs = body && Array.isArray(body.legs) ? body.legs : null;
          if (!legs || legs.length !== req.legs.length) return failAll('upstream');
          if (body.source) S.source = String(body.source);
          return req.legs.map(function (leg, i) { return legResultFrom(legs[i]); });
        });
      }).catch(function () { return failAll('upstream'); });
    }
    return attempt();
  }

  // Start one fetch per run of legs not cached or already in flight.
  function fetchMissing(legs) {
    var seen = {}, need = [];
    legs.forEach(function (leg) {
      if (seen[leg.key] || cache.has(leg.key) || inflight.has(leg.key)) return;
      seen[leg.key] = true;
      need.push(leg);
    });
    var maxPts = Number(cfg() && cfg().maxPoints) || DEFAULT_MAX_POINTS;
    runsToRequests(need, maxPts).forEach(function (req) {
      var all = fetchRequest(req);
      req.legs.forEach(function (leg, i) {
        var one = all.then(function (results) {
          var res = results[i] || { error: 'upstream' };
          if (isCacheable(res)) cachePut(leg.key, res);
          inflight.delete(leg.key);
          return res;
        });
        inflight.set(leg.key, one);
      });
    });
  }

  // ─── Rows + chips ───────────────────────────────────────────────────────

  // Row element per seq index. Canonical rows carry data-hop-idx (their
  // position index; a 1-hop route renders the same position twice, pinned top
  // and bottom). Isolated-path rows carry none and are 1:1 in DOM order.
  function rowsFor(sidebar, seq) {
    var els = Array.prototype.slice.call(sidebar.querySelectorAll('.mc-rt-row'));
    var out = new Array(seq.length);
    var byData = els.length > 0 && els.every(function (el) { return el.getAttribute('data-hop-idx') != null; });
    if (byData) {
      els.forEach(function (el) {
        var k = parseInt(el.getAttribute('data-hop-idx'), 10);
        if (k >= 0 && k < seq.length && !out[k]) out[k] = el;
      });
      return out;
    }
    if (els.length !== seq.length) return null;
    els.forEach(function (el, i) { out[i] = el; });
    return out;
  }

  function stripChips(sidebar) {
    if (!sidebar || typeof sidebar.querySelectorAll !== 'function') return;
    Array.prototype.forEach.call(sidebar.querySelectorAll('.mc-los-chip'), function (el) {
      if (el.parentNode) el.parentNode.removeChild(el);
    });
    Array.prototype.forEach.call(sidebar.querySelectorAll('.mc-los-has-chip'), function (el) {
      el.classList.remove('mc-los-has-chip');
    });
  }

  function makeChip(leg) {
    var b = document.createElement('button');
    b.type = 'button';
    b.className = 'mc-los-chip';
    b.setAttribute('aria-haspopup', 'dialog');
    b.setAttribute('data-los-idx', String(leg.idx));
    b.addEventListener('click', function (e) {
      // The row's own click toggles its hop-detail panel.
      e.stopPropagation();
      e.preventDefault();
      openPanel(leg.idx, { focus: true });
    });
    b.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' || e.key === ' ') e.stopPropagation();
    });
    return b;
  }

  // The chip sits at the end of .mc-rt-name. The name text gets its own
  // ellipsis box so a long name shrinks instead of pushing the chip out.
  function placeChip(row, chip) {
    var nameEl = row && row.querySelector('.mc-rt-name');
    if (!nameEl) return false;
    if (!nameEl.querySelector('.mc-los-name-text')) {
      var first = nameEl.firstChild;
      if (first && first.nodeType === 3) {
        var span = document.createElement('span');
        span.className = 'mc-los-name-text';
        span.title = first.textContent;
        nameEl.insertBefore(span, first);
        span.appendChild(first);
      }
    }
    nameEl.classList.add('mc-los-has-chip');
    nameEl.appendChild(chip);
    return true;
  }

  function legState(leg) {
    if (leg.analysis) return leg.analysis;
    if (leg.result && leg.result.error) return leg.result;
    return null;
  }

  function renderChip(leg) {
    if (!leg.chip) return;
    var st = legState(leg);
    leg.chip.className = 'mc-los-chip mc-los-tone-' + chipTone(st) + (st ? '' : ' mc-los-loading');
    leg.chip.textContent = chipText(st);
    var title = chipTitle(st, leg.nameA);
    leg.chip.title = title;
    leg.chip.setAttribute('aria-label', title + '. Show terrain profile');
    leg.chip.setAttribute('aria-expanded', S.open && S.open.idx === leg.idx ? 'true' : 'false');
  }

  // ─── Map layer ──────────────────────────────────────────────────────────

  function ensureLayer(map) {
    if (S.layer && typeof map.hasLayer === 'function' && map.hasLayer(S.layer)) return S.layer;
    if (S.layer) { try { S.layer.remove(); } catch (e) { /* map gone */ } }
    S.layer = L.layerGroup().addTo(map);
    return S.layer;
  }

  function removeFromLayer(l) {
    if (!l) return;
    try { if (S.layer) S.layer.removeLayer(l); else l.remove(); } catch (e) { /* already gone */ }
  }

  function renderMarker(leg) {
    removeFromLayer(leg.marker);
    leg.marker = null;
    var an = leg.analysis;
    if (!an || an.worstT == null || !S.layer) return;
    var tone = chipTone(an);
    if (tone !== 'warn' && tone !== 'bad') return;
    var ll = window.MeshLOS.intermediatePoint(leg.a, leg.b, an.worstT);
    var text = markerText(an, leg.nameA);
    var icon = L.divIcon({
      className: 'mc-los-obstruction mc-los-tone-' + tone,
      html: '<svg class="ph-icon" aria-hidden="true"><use href="' + SPRITE + '#ph-warning"/></svg>',
      iconSize: [28, 28],
      iconAnchor: [14, 14]
    });
    var mk = L.marker([ll.lat, ll.lon], { icon: icon, keyboard: true, riseOnHover: true, zIndexOffset: 400 });
    mk.bindTooltip(esc(text), { direction: 'top', offset: [0, -16] });
    mk.on('click', function () { openPanel(leg.idx, { focus: true }); });
    mk.addTo(S.layer);
    var el = mk.getElement && mk.getElement();
    if (el) el.setAttribute('aria-label', text + '. Show terrain profile');
    leg.marker = mk;
  }

  function drawLegLine(leg) {
    removeFromLayer(S.legLine);
    S.legLine = null;
    if (!leg || !S.layer) return;
    S.legLine = L.polyline([[leg.a.lat, leg.a.lon], [leg.b.lat, leg.b.lon]], {
      className: 'mc-los-leg-active', weight: 4, opacity: 1, dashArray: '10 8', interactive: false
    }).addTo(S.layer);
  }

  // ─── Analysis ───────────────────────────────────────────────────────────

  function legKm(leg) { return window.MeshLOS.haversineKm(leg.a, leg.b); }

  function analyzeLeg(leg) {
    var r = leg.result;
    if (!r || !r.elevM) return null;
    return window.MeshLOS.analyze({
      distanceKm: r.distanceKm != null ? r.distanceKm : legKm(leg),
      elevM: r.elevM,
      antennaAM: S.settings.antA,
      antennaBM: S.settings.antB,
      freqMHz: S.settings.freq,
      kFactor: kFactor()
    });
  }

  function applyResult(leg, res) {
    leg.result = res;
    leg.analysis = analyzeLeg(leg);
    if (res && res.elevM && !leg.analysis) leg.result = { error: 'upstream', distanceKm: res.distanceKm };
    renderChip(leg);
    renderMarker(leg);
    if (S.open && S.open.idx === leg.idx) renderPanel();
  }

  function reanalyzeAll() {
    S.legs.forEach(function (leg) {
      if (!leg.result || !leg.result.elevM) return;
      leg.analysis = analyzeLeg(leg);
      renderChip(leg);
      renderMarker(leg);
    });
    if (S.open) renderPanel();
  }

  function legByIdx(idx) {
    for (var i = 0; i < S.legs.length; i++) if (S.legs[i].idx === idx) return S.legs[i];
    return null;
  }

  function openLeg() { return S.open ? legByIdx(S.open.idx) : null; }

  // ─── Deep link (&los=<row index>) ───────────────────────────────────────

  function isRouteHash(h) { return /^#\/map(\?|$)/.test(h) && /[?&](packet|route)=/.test(h); }

  // replaceState, never location.hash =: route-view tears down on hashchange.
  function writeLosParam(idx) {
    try {
      var h = location.hash || '';
      if (!isRouteHash(h)) return;
      var next = withLosParam(h, idx);
      if (next !== h && window.history && history.replaceState) history.replaceState(history.state, '', next);
    } catch (e) { /* history unavailable */ }
  }

  function raf(fn) {
    return typeof window.requestAnimationFrame === 'function' ? window.requestAnimationFrame(fn) : setTimeout(fn, 16);
  }

  // Deferred a frame so a deep-linked path isolation queued by render() wins.
  function schedulePending() {
    if (S.pendingLos == null) return;
    var gen = S.gen;
    raf(function () {
      if (gen !== S.gen || S.pendingLos == null) return;
      var leg = legByIdx(S.pendingLos);
      if (!leg) {
        if (S.legs.every(function (l) { return !!l.result; })) S.pendingLos = null;
        return;
      }
      if (!leg.result) return;
      openPanel(leg.idx, { focus: false });
    });
  }

  // ─── Panel ──────────────────────────────────────────────────────────────

  function icon(name) {
    return '<svg class="ph-icon" aria-hidden="true"><use href="' + SPRITE + '#' + name + '"/></svg>';
  }

  function field(name, label, min, max, step) {
    return '<div class="mc-los-field">' +
      '<label for="mc-los-in-' + name + '">' + label + '</label>' +
      '<input id="mc-los-in-' + name + '" type="number" inputmode="decimal" min="' + min + '" max="' + max +
        '" step="' + step + '" data-los-field="' + name + '" aria-describedby="mc-los-input-err">' +
      '</div>';
  }

  function buildPanel() {
    var el = document.createElement('div');
    el.className = 'mc-los-panel';
    el.setAttribute('role', 'dialog');
    el.setAttribute('aria-label', 'Line of sight');
    el.tabIndex = -1;
    el.innerHTML =
      '<div class="mc-los-head">' +
        '<h2 class="mc-los-title"></h2>' +
        '<div class="mc-los-nav">' +
          '<button type="button" class="mc-los-btn mc-los-prev" aria-label="Previous link">' + icon('ph-caret-left') + '</button>' +
          '<span class="mc-los-pos"></span>' +
          '<button type="button" class="mc-los-btn mc-los-next" aria-label="Next link">' + icon('ph-caret-right') + '</button>' +
          '<button type="button" class="mc-los-btn mc-los-close" aria-label="Close line of sight">' + icon('ph-x') + '</button>' +
        '</div>' +
      '</div>' +
      '<div class="mc-los-body"></div>' +
      '<div class="mc-los-controls">' +
        field('antA', 'Antenna A (m)', 0, 300, 0.5) +
        field('antB', 'Antenna B (m)', 0, 300, 0.5) +
        field('freq', 'Frequency (MHz)', 100, 6000, 'any') +
        '<div class="mc-los-field mc-los-k" title="Effective Earth-radius factor for refraction, set by the server">' +
          '<span class="mc-los-k-label">k-factor</span><span class="mc-los-k-val"></span>' +
        '</div>' +
        '<button type="button" class="mc-los-reset" title="Reset antenna heights and frequency to the server defaults">Reset</button>' +
        '<p class="mc-los-input-err" id="mc-los-input-err" aria-live="polite"></p>' +
      '</div>' +
      '<p class="mc-los-foot"></p>';

    el.querySelector('.mc-los-prev').addEventListener('click', function () { step(-1); });
    el.querySelector('.mc-los-next').addEventListener('click', function () { step(1); });
    el.querySelector('.mc-los-close').addEventListener('click', function () { closePanel({ restoreFocus: true }); });
    el.querySelector('.mc-los-reset').addEventListener('click', resetSettings);
    el.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') {
        e.stopPropagation();
        e.preventDefault();
        closePanel({ restoreFocus: true });
      }
    });
    Array.prototype.forEach.call(el.querySelectorAll('input[data-los-field]'), function (inp) {
      inp.value = String(S.settings[inp.getAttribute('data-los-field')]);
      inp.addEventListener('input', function () {
        clearTimeout(S.debounce);
        S.debounce = setTimeout(function () { applyInputs(false); }, DEBOUNCE_MS);
      });
      inp.addEventListener('change', function () { clearTimeout(S.debounce); applyInputs(true); });
    });
    el.querySelector('.mc-los-k-val').textContent = (Math.round(kFactor() * 100) / 100).toFixed(2);
    // Clicks, drags and wheel inside the panel must not pan or zoom the map.
    if (L.DomEvent) {
      L.DomEvent.disableClickPropagation(el);
      L.DomEvent.disableScrollPropagation(el);
    }
    return el;
  }

  function metaHtml(leg, an, st) {
    var km = an ? an.distanceKm : (leg.result && leg.result.distanceKm != null ? leg.result.distanceKm : legKm(leg));
    var items = [
      '<span title="Link length">' + fmtKm(km) + ' km</span>',
      '<span title="Bearing from ' + esc(leg.nameA) + '">' + (Math.round(window.MeshLOS.bearingDeg(leg.a, leg.b)) % 360) + '°</span>'
    ];
    if (an) {
      items.push('<span title="Free-space path loss">FSPL ' + an.fsplDb.toFixed(1) + ' dB</span>');
      items.push('<span title="Worst clearance of the first Fresnel zone">F1 clearance ' + fmtClearance(an.clearance) + '</span>');
    }
    var pill;
    if (an) {
      var v = verdicts()[an.verdict];
      var blk = an.verdict === 'blocked';
      pill = '<span class="mc-los-pill mc-los-tone-' + chipTone(an) + '" title="' +
        esc(blk ? v.detail + ' ' + HEARD_ANYWAY : v.detail) + '">' + esc(blk ? SOFT_BLOCKED : v.label) + '</span>';
    } else {
      pill = '<span class="mc-los-pill mc-los-tone-muted' + (st ? '' : ' mc-los-loading') + '">' + (st ? 'n/a' : 'Checking…') + '</span>';
    }
    return '<p class="mc-los-meta">' + items.join('<span class="mc-los-sep" aria-hidden="true">·</span>') + pill + '</p>';
  }

  function chartStateHtml(leg, st) {
    if (!st) {
      return '<div class="mc-los-chart-state" role="status">Loading terrain profile…</div>';
    }
    var retry = RETRYABLE[st.error]
      ? '<button type="button" class="mc-los-retry">Try again</button>' : '';
    return '<div class="mc-los-chart-state mc-los-chart-error" role="status">' +
      '<strong>No terrain profile for this link</strong>' +
      '<span>' + esc(errorReason(st)) + '.</span>' + retry +
      '</div>';
  }

  var READOUT_HINT = 'Move along the profile to read the terrain at each point';

  // Panel footer: the terrain attribution the server sent (body.source).
  function footText(source) {
    return 'Elevation: ' + (source || 'SRTM') + ' · terrain only, trees and buildings are not modelled';
  }

  // The panel body. Every node-controlled string (leg.nameA/nameB) goes
  // through esc() here or in MeshLOS.renderProfileSvg; test-route-los.js
  // renders hostile names through this and asserts they stay inert.
  function panelTitleHtml(leg) {
    return 'Line of sight <span aria-hidden="true">·</span> <span class="mc-los-ends">' +
      esc(leg.nameA) + ' → ' + esc(leg.nameB) + '</span>';
  }

  function panelBodyHtml(leg, an, st) {
    var body = metaHtml(leg, an, st);
    if (an && an.obstructionM > 0) {
      body += '<p class="mc-los-obst">Terrain rises ' + fmtObstruction(an.obstructionM) +
        ' m above the line of sight ' + fmtKm(an.worstKm) + ' km from ' + esc(leg.nameA) + '.</p>';
    }
    // Loading reserves the chart's full box; an error needs far less room.
    body += '<div class="mc-los-chart' + (an ? '' : st ? ' mc-los-chart-err' : ' mc-los-chart-wait') + '">' +
      (an ? window.MeshLOS.renderProfileSvg(an, { nameA: leg.nameA, nameB: leg.nameB }) : chartStateHtml(leg, st)) +
      '</div>';
    if (an) body += '<p class="mc-los-readout">' + READOUT_HINT + '</p>';
    return body;
  }

  function renderPanel() {
    var leg = openLeg();
    var p = S.panel;
    if (!leg || !p) return;
    var an = leg.analysis, st = legState(leg);
    var pos = S.legs.indexOf(leg);
    var title = p.querySelector('.mc-los-title');
    title.innerHTML = panelTitleHtml(leg);
    title.title = leg.nameA + ' → ' + leg.nameB;
    p.setAttribute('aria-label', 'Line of sight from ' + leg.nameA + ' to ' + leg.nameB);
    var many = S.legs.length > 1;
    p.querySelector('.mc-los-nav').classList.toggle('mc-los-single', !many);
    p.querySelector('.mc-los-prev').disabled = pos <= 0;
    p.querySelector('.mc-los-next').disabled = pos >= S.legs.length - 1;
    p.querySelector('.mc-los-pos').textContent = many ? 'Link ' + (pos + 1) + ' of ' + S.legs.length : '';

    p.querySelector('.mc-los-body').innerHTML = panelBodyHtml(leg, an, st);

    var retryBtn = p.querySelector('.mc-los-retry');
    if (retryBtn) retryBtn.addEventListener('click', retry);
    var hit = p.querySelector('.mc-los-hit');
    if (hit) {
      hit.addEventListener('pointermove', onPointer);
      hit.addEventListener('pointerdown', onPointer);
      hit.addEventListener('pointerleave', onPointerLeave);
    }
    p.querySelector('.mc-los-foot').textContent = footText(S.source);

    S.legs.forEach(function (l) {
      if (l.chip) l.chip.setAttribute('aria-expanded', l === leg ? 'true' : 'false');
    });
    drawLegLine(leg);
    if (an && S.cursorT != null) setCursorT(S.cursorT);
    else hideMapCursor();
  }

  function ensurePanel() {
    var container = S.map && S.map.getContainer ? S.map.getContainer() : null;
    if (!container) return null;
    if (S.panel && S.panel.parentNode === container) return S.panel;
    if (S.panel && S.panel.parentNode) S.panel.parentNode.removeChild(S.panel);
    S.panel = buildPanel();
    container.appendChild(S.panel);
    return S.panel;
  }

  function isMobile() { return window.innerWidth <= MOBILE_MAX_W; }

  // On a phone the route sidebar is a bottom sheet that, expanded, leaves the
  // map a strip. Collapse it (through its own handle) while the panel is open.
  function collapseSheet() {
    var sb = S.sidebar;
    if (!isMobile() || !sb || !sb.classList.contains('mc-rt-mobile-expanded')) return false;
    var handle = sb.querySelector('.mc-rt-mobile-handle');
    if (!handle) return false;
    handle.click();
    S.sheetCollapsed = true;
    return true;
  }

  function reducedMotion() {
    try { return window.matchMedia('(prefers-reduced-motion: reduce)').matches; } catch (e) { return false; }
  }

  // Fit the leg into the map area the panel leaves free.
  function fitLeg(leg) {
    var map = S.map;
    if (!map || !leg || !S.panel) return;
    // Leaflet drops a setView issued mid zoom-animation (e.g. the route's own
    // re-fit after the mobile sheet collapses); fit once that animation ends.
    if (map._animatingZoom && typeof map.once === 'function') {
      map.once('zoomend', function () { if (S.open && S.open.idx === leg.idx) fitLeg(leg); });
      return;
    }
    try {
      var size = map.getSize();
      var ph = S.panel.offsetHeight || 0;
      var pad = Math.max(0, Math.min(ph + 24, size.y - 120));
      var opts = isMobile()
        ? { paddingTopLeft: [24, pad + 16], paddingBottomRight: [24, 24] }
        : { paddingTopLeft: [48, 48], paddingBottomRight: [48, pad + 40] };
      opts.maxZoom = 14;
      opts.animate = !reducedMotion();
      map.fitBounds(L.latLngBounds([[leg.a.lat, leg.a.lon], [leg.b.lat, leg.b.lon]]), opts);
    } catch (e) { console.warn('[route-los] fit:', e); }
  }

  function openPanel(idx, opts) {
    opts = opts || {};
    var leg = legByIdx(idx);
    if (!leg) return;
    S.pendingLos = null;
    if (!ensurePanel()) return;
    S.open = { idx: leg.idx, key: leg.key };
    S.cursorT = null;
    S.map.getContainer().classList.add('mc-los-open');
    document.body.classList.add('mc-los-panel-open');
    renderPanel();
    writeLosParam(leg.idx);
    // The sheet's handle re-fits the whole route ~280 ms later; fit the leg after it.
    var waited = opts.focus && collapseSheet();
    if (waited) setTimeout(function () { if (S.open && S.open.idx === leg.idx) fitLeg(leg); }, 360);
    else fitLeg(leg);
    if (opts.focus) {
      try { S.panel.focus({ preventScroll: true }); } catch (e) { S.panel.focus(); }
    }
  }

  function step(dir) {
    var leg = openLeg();
    if (!leg) return;
    var next = S.legs[S.legs.indexOf(leg) + dir];
    if (!next) return;
    S.open = { idx: next.idx, key: next.key };
    S.cursorT = null;
    renderPanel();
    writeLosParam(next.idx);
    fitLeg(next);
    // Stepping onto the last link disables the button that had focus.
    var active = document.activeElement;
    if (active && active.disabled && S.panel) S.panel.focus({ preventScroll: true });
  }

  function closePanel(opts) {
    opts = opts || {};
    var leg = openLeg();
    var wasOpen = !!S.open;
    S.open = null;
    S.cursorT = null;
    cancelCursorFrame();
    removeFromLayer(S.legLine);
    removeFromLayer(S.cursorMarker);
    S.legLine = null;
    S.cursorMarker = null;
    if (S.panel && S.panel.parentNode) {
      if (S.panel.parentNode.classList) S.panel.parentNode.classList.remove('mc-los-open');
      S.panel.parentNode.removeChild(S.panel);
    }
    S.panel = null;
    if (document.body) document.body.classList.remove('mc-los-panel-open');
    S.legs.forEach(function (l) { if (l.chip) l.chip.setAttribute('aria-expanded', 'false'); });
    if (!wasOpen) return;
    if (opts.updateUrl !== false) writeLosParam(null);
    if (S.sheetCollapsed && opts.restoreFocus && S.sidebar && !S.sidebar.classList.contains('mc-rt-mobile-expanded')) {
      var handle = S.sidebar.querySelector('.mc-rt-mobile-handle');
      if (handle) handle.click();
    }
    S.sheetCollapsed = false;
    if (opts.restoreFocus && leg && leg.chip && leg.chip.isConnected) {
      try { leg.chip.focus({ preventScroll: false }); } catch (e) { leg.chip.focus(); }
    }
  }

  function retry() {
    if (!S.map || !S.sidebar || !S.seq) return;
    update({ map: S.map, sidebar: S.sidebar, seq: S.seq });
    if (S.panel) S.panel.focus({ preventScroll: true });
  }

  // ─── Settings inputs ────────────────────────────────────────────────────

  function applyInputs(showErrors) {
    if (!S.panel) return;
    var next = { antA: S.settings.antA, antB: S.settings.antB, freq: S.settings.freq };
    var errs = [];
    Array.prototype.forEach.call(S.panel.querySelectorAll('input[data-los-field]'), function (inp) {
      var name = inp.getAttribute('data-los-field');
      var v = parseSetting(name, inp.value);
      if (v == null) {
        // Silent while typing; flagged on change, then re-checked live.
        if (showErrors || inp.getAttribute('aria-invalid') === 'true') {
          inp.setAttribute('aria-invalid', 'true');
          errs.push(FIELD_ERRORS[name]);
        }
        return;
      }
      inp.removeAttribute('aria-invalid');
      next[name] = v;
    });
    S.panel.querySelector('.mc-los-input-err').textContent = errs.join(' ');
    var changed = next.antA !== S.settings.antA || next.antB !== S.settings.antB || next.freq !== S.settings.freq;
    if (!changed) return;
    S.settings = next;
    Object.keys(LS_KEYS).forEach(function (name) { lsSet(LS_KEYS[name], String(next[name])); });
    reanalyzeAll();
  }

  function resetSettings() {
    clearTimeout(S.debounce);
    Object.keys(LS_KEYS).forEach(function (name) { lsDel(LS_KEYS[name]); });
    S.settings = defaults();
    if (S.panel) {
      Array.prototype.forEach.call(S.panel.querySelectorAll('input[data-los-field]'), function (inp) {
        inp.value = String(S.settings[inp.getAttribute('data-los-field')]);
        inp.removeAttribute('aria-invalid');
      });
      S.panel.querySelector('.mc-los-input-err').textContent = '';
    }
    reanalyzeAll();
  }

  // ─── Chart ↔ map crosshair ──────────────────────────────────────────────

  function onPointer(e) {
    S.ptr = { x: e.clientX };
    if (!S.rafId) S.rafId = raf(drawCursorFromPointer);
  }

  function onPointerLeave(e) {
    // A touch ends with pointerleave; keep the last reading on screen.
    if (e.pointerType === 'touch') return;
    S.cursorT = null;
    cancelCursorFrame();
    hideChartCursor();
    hideMapCursor();
  }

  function cancelCursorFrame() {
    if (S.rafId) {
      if (typeof window.cancelAnimationFrame === 'function') window.cancelAnimationFrame(S.rafId);
      else clearTimeout(S.rafId);
    }
    S.rafId = 0;
    S.ptr = null;
  }

  function drawCursorFromPointer() {
    S.rafId = 0;
    var svg = S.panel && S.panel.querySelector('.mc-los-svg');
    if (!svg || !S.ptr) return;
    var rect = svg.getBoundingClientRect();
    if (!rect.width) return;
    var x = (S.ptr.x - rect.left) / rect.width * window.MeshLOS.PLOT.W;
    setCursorT(window.MeshLOS.tAtViewX(x));
  }

  // Hover readout for sample i of the open leg.
  function readoutHtml(leg, an, i) {
    var ts = i / (an.n - 1);
    var edge = i === 0 || i === an.n - 1;
    var clr = edge ? '—' : fmtClearance((an.losM[i] - an.terrainM[i]) / an.fresnelM[i]);
    return [
      '<b>' + fmtKm(ts * an.distanceKm) + ' km</b> from ' + esc(leg.nameA),
      'terrain <b>' + Math.round(an.terrainM[i]) + ' m</b>',
      'LOS <b>' + Math.round(an.losM[i]) + ' m</b>',
      'clearance <b>' + clr + '</b>'
    ].map(function (s) { return '<span class="mc-los-ri">' + s + '</span>'; })
      .join('<span class="mc-los-sep" aria-hidden="true">·</span>');
  }

  // Snap to the nearest sample so the readout matches the plotted point.
  function setCursorT(t) {
    var leg = openLeg();
    var an = leg && leg.analysis;
    if (!an || !S.panel) return;
    var i = Math.round(t * (an.n - 1));
    var ts = i / (an.n - 1);
    S.cursorT = ts;
    var cur = S.panel.querySelector('.mc-los-cursor');
    if (cur) {
      var vx = String(Math.round(window.MeshLOS.viewXAtT(ts) * 100) / 100);
      cur.setAttribute('x1', vx);
      cur.setAttribute('x2', vx);
      cur.setAttribute('visibility', 'visible');
    }
    var ro = S.panel.querySelector('.mc-los-readout');
    if (ro) ro.innerHTML = readoutHtml(leg, an, i);
    var ll = window.MeshLOS.intermediatePoint(leg.a, leg.b, ts);
    if (!S.cursorMarker) {
      S.cursorMarker = L.marker([ll.lat, ll.lon], {
        icon: L.divIcon({ className: 'mc-los-cursor-marker', html: '', iconSize: [16, 16], iconAnchor: [8, 8] }),
        interactive: false, keyboard: false, zIndexOffset: 1000
      });
    } else {
      S.cursorMarker.setLatLng([ll.lat, ll.lon]);
    }
    if (S.layer && !S.layer.hasLayer(S.cursorMarker)) S.layer.addLayer(S.cursorMarker);
  }

  function hideChartCursor() {
    if (!S.panel) return;
    var cur = S.panel.querySelector('.mc-los-cursor');
    if (cur) cur.setAttribute('visibility', 'hidden');
    var ro = S.panel.querySelector('.mc-los-readout');
    if (ro) ro.textContent = READOUT_HINT;
  }

  function hideMapCursor() {
    if (S.cursorMarker && S.layer && S.layer.hasLayer(S.cursorMarker)) S.layer.removeLayer(S.cursorMarker);
  }

  // ─── Lifecycle ──────────────────────────────────────────────────────────

  // Chips + markers of the current legs; the panel survives (see update).
  function resetLegs() {
    S.legs.forEach(function (leg) { removeFromLayer(leg.marker); leg.marker = null; });
    stripChips(S.sidebar);
    S.legs = [];
  }

  // MC_LOS_CFG is undefined (not null) until /api/config/client answers.
  // Wait on MeshConfigReady once; the latest ctx is retried when it settles.
  function waitForConfig(ctx) {
    if (window.MC_LOS_CFG !== undefined) return false;
    if (S.cfgWaiting) { S.waitCtx = ctx; return true; }
    var ready = window.MeshConfigReady;
    if (S.cfgWaited || !ready || typeof ready.then !== 'function') return false;
    clear();
    S.cfgWaiting = true;
    S.waitCtx = ctx;
    var done = function () {
      S.cfgWaiting = false;
      S.cfgWaited = true;
      var c = S.waitCtx;
      S.waitCtx = null;
      if (c) update(c);
    };
    ready.then(done, done);
    return true;
  }

  function update(ctx) {
    ctx = ctx || {};
    if (waitForConfig(ctx)) return;
    var map = ctx.map, sidebar = ctx.sidebar;
    var seq = Array.isArray(ctx.seq) ? ctx.seq : [];
    if (!enabled() || !map || !sidebar || typeof sidebar.querySelectorAll !== 'function') { clear(); return; }
    if (S.map && S.map !== map) clear();
    var fresh = sidebar !== S.sidebar;
    var keepKey = S.open ? S.open.key : null;
    resetLegs();
    var gen = ++S.gen;
    S.map = map;
    S.sidebar = sidebar;
    S.seq = seq;
    // Restored rows are innerHTML snapshots and may hold stale chips.
    stripChips(sidebar);
    if (!S.settings) S.settings = loadSettings();

    var rows = rowsFor(sidebar, seq);
    if (!rows) {
      if (!S.warned) {
        S.warned = true;
        console.warn('[route-los] sidebar rows do not match the route sequence; line of sight skipped');
      }
      clear();
      return;
    }
    if (!keepKey && (fresh || S.pendingLos != null)) S.pendingLos = parseLosParam(location.hash);

    var legs = buildLegs(seq);
    S.legs = legs;
    if (!legs.length) { closePanel({ restoreFocus: false }); return; }
    ensureLayer(map);
    legs.forEach(function (leg) {
      leg.row = rows[leg.idx];
      leg.chip = makeChip(leg);
      if (!placeChip(leg.row, leg.chip)) leg.chip = null;
    });

    // Keep an open panel on the same link if this route still has it.
    if (keepKey) {
      var same = null;
      legs.forEach(function (l) { if (!same && l.key === keepKey) same = l; });
      if (same) {
        S.open = { idx: same.idx, key: same.key };
        writeLosParam(same.idx);
      } else {
        closePanel({ restoreFocus: false });
      }
    }

    fetchMissing(legs);
    legs.forEach(function (leg) {
      var hit = cacheGet(leg.key);
      if (hit) { applyResult(leg, hit); return; }
      renderChip(leg);
      var p = inflight.get(leg.key);
      if (!p) return;
      p.then(function (res) {
        if (gen !== S.gen) return; // a newer update() or clear() owns the UI
        applyResult(leg, res);
        schedulePending();
      });
    });
    if (S.open) renderPanel();
    schedulePending();
  }

  function clear() {
    S.gen++;
    S.waitCtx = null;
    clearTimeout(S.debounce);
    closePanel({ updateUrl: false, restoreFocus: false });
    resetLegs();
    if (S.layer) {
      try { S.layer.clearLayers(); S.layer.remove(); } catch (e) { /* map already removed */ }
      S.layer = null;
    }
    S.map = null;
    S.sidebar = null;
    S.seq = null;
    S.pendingLos = null;
  }

  window.MeshRouteLOS = {
    update: update,
    clear: clear,
    _internals: {
      buildLegs: buildLegs,
      runsToRequests: runsToRequests,
      requestUrl: requestUrl,
      cacheKey: cacheKey,
      parseLosParam: parseLosParam,
      withLosParam: withLosParam,
      legResultFrom: legResultFrom,
      isCacheable: isCacheable,
      pollDelayS: pollDelayS,
      footText: footText,
      MAX_POLLS: MAX_POLLS,
      chipText: chipText,
      chipTitle: chipTitle,
      chipTone: chipTone,
      markerText: markerText,
      fmtClearance: fmtClearance,
      parseSetting: parseSetting,
      panelTitleHtml: panelTitleHtml,
      panelBodyHtml: panelBodyHtml,
      readoutHtml: readoutHtml,
      metaHtml: metaHtml,
      rowsFor: rowsFor,
      cache: cache,
      CACHE_MAX: CACHE_MAX,
      state: S
    }
  };
})();
