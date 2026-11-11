/* planner.js — Repeater planner page (#/tools/planner).
 *
 * A full-screen Leaflet map with a side panel and three modes, all state in
 * the hash (#/tools/planner?mode=chain|relay|area&…):
 *   chain  points clicked on the map (or snapped to known nodes); every leg
 *          gets a line-of-sight verdict from ONE GET /api/planner/profile per
 *          point-set change, analysed locally with MeshLOS, so antenna edits
 *          never refetch.
 *   relay  A and B; GET /api/planner/relay ranks known nodes and open terrain
 *          as a relay between them.
 *   area   delegated to planner-area.js (window.MeshPlannerArea.mount).
 *
 * Every /api/planner/* endpoint speaks one job protocol: 200 = result, 202 =
 * {state, progress, retry_after_s} (re-poll the SAME url), 4xx/5xx = {error}.
 * fetchJob() implements it once, for this page and the area module.
 *
 * The maths and the profile SVG live in los.js (window.MeshLOS); the profile
 * panel reuses route-los.css's mc-los-* look. Shows a "not enabled" page while
 * window.MC_PLANNER_CFG is null. Exposes window.MeshPlanner; registers page
 * 'planner'.
 */
(function () {
  'use strict';

  var SPRITE = '/icons/phosphor-sprite.svg';
  var BASE_HASH = '#/tools/planner';
  var MODES = ['chain', 'relay', 'area'];
  var MODE_LABELS = { chain: 'Chain', relay: 'Relay', area: 'Area' };
  var DEBOUNCE_MS = 400;        // point-set / relay-input change → one request
  var INPUT_DEBOUNCE_MS = 150;  // antenna typing in chain mode → local re-analysis
  var POLL_MIN_S = 1;
  var POLL_MAX_S = 5;
  var MAX_POLLS = 360;          // ≥ 6 min of 202s before giving up
  var MOBILE_MAX_W = 767;
  var LEG_CACHE_MAX = 200;
  var SNAP_PX = 14;             // a dragged point dropped this close to a node snaps to it
  var COORD_DP = 5;             // ~1 m: URL and cache-key precision
  var HEIGHT_MIN = 0;
  var HEIGHT_MAX = 100;
  var DATALIST_MAX = 1500;
  var FALLBACK_VIEW = { center: [37.6, -122.1], zoom: 9 };
  var FOOTER = 'Terrain: SRTM ~90 m (may include some trees and buildings) · line of sight only, not a link budget · survey before you build';
  var READOUT_HINT = 'Move along a profile to read the terrain at each point';
  var CFG_DEFAULTS = {
    defaultRangeKm: 30, rangesKm: [10, 15, 20, 30, 40, 50, 60], newAntennaM: 10, existingAntennaM: 5,
    frequencyMhz: 910.525, kFactor: 4 / 3, maxPoints: 16, maxLegKm: 100, maxAreaKm: 100, maxSites: 10,
    minClearance: 0.6
  };
  // Per-leg error codes from /api/planner/profile, plus 'request' for a leg
  // whose whole request failed and 'bad_data' for a profile los.js rejects.
  var LEG_ERRORS = {
    too_long: 'Too long for a terrain check',
    out_of_area: 'Too far from the mesh for terrain data',
    no_terrain: 'No terrain data for this leg',
    request: 'The terrain check failed',
    bad_data: 'The terrain profile was unusable'
  };
  var CACHEABLE_ERRORS = { too_long: true, out_of_area: true, no_terrain: true };
  var JOB_STATES = {
    queued: 'Waiting in the queue…',
    loading_terrain: 'Loading terrain…',
    computing: 'Computing…'
  };
  var ROLE_NAMES = { repeater: 'Repeater', room: 'Room server', companion: 'Companion', sensor: 'Sensor', observer: 'Observer' };
  var NUM_RE = /^-?\d{1,3}(\.\d+)?$/;

  // ─── Pure helpers (exported via _internals for tests) ───────────────────

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function hasOwn(o, k) { return Object.prototype.hasOwnProperty.call(o, k); }

  function round(x, dp) {
    var f = Math.pow(10, dp == null ? COORD_DP : dp);
    var v = Math.round(Number(x) * f) / f;
    return v === 0 ? 0 : v; // no "-0"
  }

  function fmtCoord(x) { return String(round(x)); }
  function fmtLatLon(p) { return fmtCoord(p.lat) + ', ' + fmtCoord(p.lon); }

  // "12.3 km"; '—' when unknown.
  function fmtKm(x) {
    var d = Number(x);
    if (x == null || x === '' || !isFinite(d)) return '—';
    var a = Math.abs(d);
    return (a < 10 ? d.toFixed(2) : a < 100 ? d.toFixed(1) : String(Math.round(d))) + ' km';
  }

  // x is already a percentage (clearance_pct = 100 × Fresnel clearance):
  // "62%", "−45%" (terrain crosses the line), ">999%"; '—' when unknown.
  function fmtPct(x) {
    if (x == null || x === '') return '—';
    var v = Number(x);
    if (isNaN(v)) return '—';
    if (v > 999) return '>999%';
    if (v < -999) return '<−999%';
    var r = Math.round(v);
    return (r < 0 ? '−' + Math.abs(r) : String(r)) + '%';
  }

  // Clearance for a person. The server's sentinels are not percentages:
  // >= 999 means nothing near the line, <= -100 means terrain is above it.
  function fmtClearance(x) {
    if (x == null || x === '') return '—';
    var v = Number(x);
    if (isNaN(v)) return '—';
    if (v >= 999) return 'full clearance';
    if (v <= -100) return 'terrain blocks the path';
    return fmtPct(v);
  }
  function isClearanceWord(x) {
    var v = Number(x);
    return x != null && x !== '' && !isNaN(v) && (v >= 999 || v <= -100);
  }
  // "F1 clearance 62%" or the plain phrase for the sentinels.
  function clearanceMeta(x) {
    return isClearanceWord(x) ? fmtClearance(x) : 'F1 clearance ' + fmtClearance(x);
  }

  function fmtM(x) {
    var v = Number(x);
    return x != null && x !== '' && isFinite(v) ? Math.round(v) + ' m' : '—';
  }

  function fmtH(h) { return String(round(h, 1)); }

  function validLatLon(lat, lon) {
    return isFinite(lat) && isFinite(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180;
  }

  // "lat,lon,lat,lon,…" → [{lat, lon}], rounded to COORD_DP. Pairs that are
  // not plain decimal degrees in range are dropped; a trailing odd value is
  // ignored. max caps the count.
  function parsePts(str, max) {
    var out = [];
    if (str == null) return out;
    var parts = String(str).split(',');
    for (var i = 0; i + 1 < parts.length; i += 2) {
      var a = parts[i].trim(), b = parts[i + 1].trim();
      if (!NUM_RE.test(a) || !NUM_RE.test(b)) continue;
      var lat = Number(a), lon = Number(b);
      if (!validLatLon(lat, lon)) continue;
      out.push({ lat: round(lat), lon: round(lon) });
      if (max && out.length >= max) break;
    }
    return out;
  }

  function serializePts(pts) {
    var out = [];
    (pts || []).forEach(function (p) {
      if (!p) return;
      var lat = Number(p.lat), lon = Number(p.lon);
      if (p.lat == null || p.lon == null || !validLatLon(lat, lon)) return;
      out.push(fmtCoord(lat) + ',' + fmtCoord(lon));
    });
    return out.join(',');
  }

  // Exactly one "lat,lon" pair, else null.
  function parsePoint(str) {
    if (str == null || String(str).split(',').length !== 2) return null;
    var p = parsePts(str);
    return p.length ? p[0] : null;
  }

  // Antenna height in metres, HEIGHT_MIN..HEIGHT_MAX, else null.
  function parseHeight(v) {
    if (v == null || String(v).trim() === '') return null;
    var s = String(v).trim();
    if (!/^\d+(\.\d+)?$|^\.\d+$/.test(s)) return null;
    var n = Number(s);
    if (!isFinite(n) || n < HEIGHT_MIN || n > HEIGHT_MAX) return null;
    return round(n, 1);
  }

  function parseHeights(str) {
    if (str == null || str === '') return [];
    return String(str).split(',').map(parseHeight);
  }

  function parseRange(v) {
    if (v == null || !/^\d{1,4}(\.\d+)?$/.test(String(v))) return null;
    var n = Number(v);
    return n > 0 && n <= 1000 ? n : null;
  }

  function decodePart(s) {
    try { return decodeURIComponent(String(s).replace(/\+/g, ' ')); } catch (e) { return String(s); }
  }

  // The query of "#/tools/planner?a=b&…", "?a=b" or "a=b" as {key: string}.
  // First occurrence of a key wins.
  function queryOf(hashOrQuery) {
    var h = String(hashOrQuery == null ? '' : hashOrQuery);
    var qi = h.indexOf('?');
    var q = qi >= 0 ? h.slice(qi + 1) : (h.charAt(0) === '#' ? '' : h);
    var out = {};
    q.split('&').forEach(function (kv) {
      if (!kv) return;
      var eq = kv.indexOf('=');
      var k = decodePart(eq < 0 ? kv : kv.slice(0, eq));
      if (!k || k === '__proto__' || k === 'constructor' || k === 'prototype' || hasOwn(out, k)) return;
      out[k] = eq < 0 ? '' : decodePart(kv.slice(eq + 1));
    });
    return out;
  }

  // {k: v} → "?mode=…&k=v" (mode first); commas and colons stay readable.
  function buildQuery(obj) {
    var keys = Object.keys(obj || {}).filter(function (k) { return obj[k] != null && obj[k] !== ''; });
    keys.sort(function (a, b) { return (a === 'mode' ? 0 : 1) - (b === 'mode' ? 0 : 1); });
    if (!keys.length) return '';
    return '?' + keys.map(function (k) {
      return encodeURIComponent(k) + '=' + encodeURIComponent(String(obj[k])).replace(/%2C/gi, ',').replace(/%3A/gi, ':');
    }).join('&');
  }

  function parseParams(hashOrQuery) {
    var q = queryOf(hashOrQuery);
    var sel = /^([eo])([1-9]\d?)$/.exec(q.sel || '');
    return {
      mode: MODES.indexOf(q.mode) >= 0 ? q.mode : 'chain',
      raw: q,
      chain: { pts: parsePts(q.pts), h: parseHeights(q.h) },
      relay: {
        a: parsePoint(q.a),
        b: parsePoint(q.b),
        ha: parseHeight(q.ha),
        hb: parseHeight(q.hb),
        hr: parseHeight(q.hr),
        range: parseRange(q.range),
        sel: sel ? { list: sel[1] === 'e' ? 'existing' : 'open', idx: Number(sel[2]) - 1 } : null
      }
    };
  }

  function coordKey(p) { return fmtCoord(p.lat) + ',' + fmtCoord(p.lon); }
  function legKey(a, b) { return coordKey(a) + '>' + coordKey(b); }

  function nodeLabel(n) {
    if (!n) return '';
    return n.name || (n.pubkey ? String(n.pubkey).slice(0, 8) : 'Unnamed node');
  }

  function roleName(role) {
    var r = String(role || '').toLowerCase();
    return ROLE_NAMES[r] || (r ? r.charAt(0).toUpperCase() + r.slice(1) : 'Node');
  }

  // Only known role words become class names (a role string is mesh data).
  function roleClass(role) {
    var r = String(role || '').toLowerCase();
    return hasOwn(ROLE_NAMES, r) ? r : 'other';
  }

  function pointName(p, i) {
    return p && p.node ? nodeLabel(p.node) : 'Point ' + (i + 1);
  }

  // n points → n−1 legs; leg i joins points i → i+1 with their antenna heights
  // (an intermediate point's mast serves both of its legs).
  function legsFromPoints(points) {
    var legs = [];
    if (!Array.isArray(points)) return legs;
    for (var i = 1; i < points.length; i++) {
      var a = points[i - 1], b = points[i];
      var ha = Number(a.h), hb = Number(b.h);
      legs.push({
        i: i - 1,
        a: { lat: a.lat, lon: a.lon, name: pointName(a, i - 1) },
        b: { lat: b.lat, lon: b.lon, name: pointName(b, i) },
        ha: isFinite(ha) ? ha : 0,
        hb: isFinite(hb) ? hb : 0,
        key: legKey(a, b)
      });
    }
    return legs;
  }

  function verdicts() { return (window.MeshLOS && window.MeshLOS.VERDICTS) || {}; }

  function errorReason(code) { return LEG_ERRORS[code] || LEG_ERRORS.request; }

  function verdictSummary(an) {
    var v = verdicts()[an.verdict];
    if (!v) return 'No verdict';
    if (an.verdict === 'blocked' && an.obstructionM > 0) {
      return v.label + ': terrain ' + Math.max(1, Math.round(an.obstructionM)) + ' m above the line of sight';
    }
    if (an.clearance == null || !isFinite(an.clearance)) return v.label + ': ' + v.detail;
    if (isClearanceWord(an.clearance * 100)) return v.label + ': ' + fmtClearance(an.clearance * 100);
    return v.label + ': ' + fmtClearance(an.clearance * 100) + ' of the first Fresnel zone clear';
  }

  // Chip for a MeshLOS.analyze() result (or anything with .verdict), an
  // error result ({error}) or null while loading → {tone, text, title, loading}.
  function chipFor(an) {
    if (!an) return { tone: 'muted', text: 'Checking…', title: 'Checking terrain', loading: true };
    if (an.error) return { tone: 'muted', text: 'n/a', title: errorReason(an.error), loading: false };
    var v = verdicts()[an.verdict];
    if (!v) return { tone: 'muted', text: 'n/a', title: 'No verdict', loading: false };
    return { tone: v.tone, text: v.label, title: verdictSummary(an), loading: false };
  }

  // A server Link as the subset of an analysis that chipFor/verdictSummary read.
  function linkAnalysis(link) {
    if (!link || typeof link !== 'object') return { error: 'request' };
    var c = Number(link.clearance_pct);
    return {
      verdict: String(link.verdict || ''),
      clearance: link.clearance_pct != null && isFinite(c) ? c / 100 : null,
      obstructionM: Number(link.obstruction_m),
      worstKm: Number(link.worst_km),
      distanceKm: Number(link.distance_km)
    };
  }

  function finiteArray(e) {
    if (!Array.isArray(e) || e.length < 2) return null;
    for (var i = 0; i < e.length; i++) if (typeof e[i] !== 'number' || !isFinite(e[i])) return null;
    return e;
  }

  // One leg of a /api/planner/profile body → {distanceKm, elevM} | {error, distanceKm}.
  function legResultFrom(l) {
    if (!l || typeof l !== 'object') return { error: 'request' };
    var d = Number(l.distance_km);
    var dist = l.distance_km != null && isFinite(d) ? d : null;
    if (l.error) return { error: hasOwn(CACHEABLE_ERRORS, String(l.error)) ? String(l.error) : 'request', distanceKm: dist };
    var e = finiteArray(l.elev_m);
    return e ? { distanceKm: dist, elevM: e } : { error: 'bad_data', distanceKm: dist };
  }

  // "lat, lon" → a free point; anything else matches a node name (exact, then
  // prefix, then substring; repeaters and rooms win ties). null when nothing fits.
  function resolveQuery(q, nodes) {
    var s = String(q == null ? '' : q).trim();
    if (!s) return null;
    var m = /^(-?\d{1,3}(?:\.\d+)?)\s*[,\s]\s*(-?\d{1,3}(?:\.\d+)?)$/.exec(s);
    if (m) {
      var lat = Number(m[1]), lon = Number(m[2]);
      return validLatLon(lat, lon) ? { lat: round(lat), lon: round(lon), node: null } : null;
    }
    var needle = s.toLowerCase(), best = null, bestScore = 0;
    (nodes || []).forEach(function (n) {
      if (!n || !n.name) return;
      var nm = String(n.name).toLowerCase();
      var sc = nm === needle ? 3 : nm.indexOf(needle) === 0 ? 2 : nm.indexOf(needle) > 0 ? 1 : 0;
      if (!sc) return;
      sc += n.major ? 0.5 : 0;
      if (sc > bestScore) { bestScore = sc; best = n; }
    });
    return best ? { lat: round(best.lat), lon: round(best.lon), node: best } : null;
  }

  // /api/nodes rows → plottable {pubkey, name, role, lat, lon, major}.
  function normNodes(list) {
    var out = [];
    (Array.isArray(list) ? list : []).forEach(function (n) {
      if (!n || n.lat == null || n.lon == null || n.lat === '' || n.lon === '') return;
      var lat = Number(n.lat), lon = Number(n.lon);
      if (!validLatLon(lat, lon) || (lat === 0 && lon === 0)) return;
      var role = String(n.role || '').toLowerCase();
      out.push({
        pubkey: String(n.public_key || n.pubkey || ''),
        name: n.name == null ? '' : String(n.name),
        role: role, lat: lat, lon: lon,
        major: role === 'repeater' || role === 'room'
      });
    });
    return out;
  }

  function parseRetryAfter(v) {
    if (v == null || v === '') return null;
    var s = String(v).trim();
    if (/^\d+$/.test(s)) return Number(s);
    var t = Date.parse(s);
    return isFinite(t) ? Math.max(0, Math.ceil((t - Date.now()) / 1000)) : null;
  }

  function clamp01(x) {
    var v = Number(x);
    return isFinite(v) ? Math.max(0, Math.min(1, v)) : 0;
  }

  // ─── Job protocol ───────────────────────────────────────────────────────

  function abortError() {
    var e = new Error('Aborted');
    e.name = 'AbortError';
    e.status = 0;
    return e;
  }

  function isAbort(err) { return !!err && err.name === 'AbortError'; }

  // GET url; on 202 call onProgress(state, progress) and re-poll the SAME url
  // after retry_after_s clamped to POLL_MIN_S..POLL_MAX_S. Resolves with the
  // 200 body. Rejects with Error{status, message, retryAfter} on 4xx/5xx
  // (message = the server's {error}), status 0 on a network failure, and an
  // AbortError (no further polls) when opts.signal aborts.
  function fetchJob(url, opts) {
    opts = opts || {};
    var signal = opts.signal || null;
    var onProgress = typeof opts.onProgress === 'function' ? opts.onProgress : null;
    return new Promise(function (resolve, reject) {
      var timer = null, polls = 0, settled = false;
      function done(fn, v) {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        if (signal && signal.removeEventListener) signal.removeEventListener('abort', onAbort);
        fn(v);
      }
      function onAbort() { done(reject, abortError()); }
      function fail(status, message, retryAfter) {
        var e = new Error(message || ('HTTP ' + status));
        e.status = status;
        e.retryAfter = retryAfter == null ? null : retryAfter;
        done(reject, e);
      }
      function poll() {
        if (settled) return;
        if (signal && signal.aborted) { onAbort(); return; }
        var p;
        try {
          p = fetch(url, { signal: signal || undefined, headers: { Accept: 'application/json' } });
        } catch (e) {
          p = Promise.reject(e);
        }
        Promise.resolve(p).then(function (r) {
          if (settled) return;
          var bodyP = r && typeof r.json === 'function'
            ? Promise.resolve().then(function () { return r.json(); }).catch(function () { return null; })
            : Promise.resolve(null);
          return bodyP.then(function (body) {
            if (settled) return;
            var status = Number(r.status);
            if (status === 202) {
              var st = body && typeof body === 'object' ? body : {};
              if (onProgress) {
                try { onProgress(String(st.state || 'queued'), clamp01(st.progress)); } catch (e) { /* caller bug */ }
              }
              if (++polls >= MAX_POLLS) { fail(504, 'The planner took too long to answer', null); return; }
              var s = Number(st.retry_after_s);
              if (!isFinite(s)) s = POLL_MIN_S;
              s = Math.min(POLL_MAX_S, Math.max(POLL_MIN_S, s));
              timer = setTimeout(poll, s * 1000);
              return;
            }
            if (status >= 200 && status < 300) {
              if (body == null) { fail(status, 'Unexpected response from the server', null); return; }
              done(resolve, body);
              return;
            }
            var ra = r.headers && typeof r.headers.get === 'function' ? parseRetryAfter(r.headers.get('Retry-After')) : null;
            fail(status, body && body.error ? String(body.error) : '', ra);
          });
        }).catch(function (e) {
          if (settled) return;
          if (isAbort(e) || (signal && signal.aborted)) { onAbort(); return; }
          fail(0, 'Network error', null);
        });
      }
      if (signal) {
        if (signal.aborted) { onAbort(); return; }
        if (signal.addEventListener) signal.addEventListener('abort', onAbort);
      }
      poll();
    });
  }

  function jobErrorText(err) {
    var s = err ? Number(err.status) : 0;
    var wait = err && err.retryAfter > 0 ? ' Try again in ' + err.retryAfter + ' s.' : ' Try again in a moment.';
    if (s === 400) return 'The planner can’t use this request' + (err.message && !/^HTTP /.test(err.message) ? ': ' + err.message : '.');
    if (s === 404) return 'The planner isn’t available on this server right now.';
    if (s === 429) return 'Too many planner requests from this connection.' + wait;
    if (s === 503) return 'Terrain data is busy or unavailable.' + wait;
    if (s === 504) return 'The planner took too long to answer.' + wait;
    if (s >= 500) return 'The planner hit a server error.' + wait;
    return 'Couldn’t reach the server. Check your connection and try again.';
  }

  function isRetryable(err) {
    var s = err ? Number(err.status) : 0;
    return s === 0 || s === 429 || s >= 500;
  }

  // ─── HTML builders (pure; every mesh-controlled string goes through esc) ─

  function icon(name) {
    return '<svg class="ph-icon" aria-hidden="true"><use href="' + SPRITE + '#' + name + '"/></svg>';
  }

  function nodeTooltipHtml(n) {
    return '<b>' + esc(nodeLabel(n)) + '</b> <span class="mc-pl-tip-role">' + esc(roleName(n.role)) + '</span>';
  }

  function chipClass(chip) {
    return 'mc-los-chip mc-los-tone-' + chip.tone + (chip.loading ? ' mc-los-loading' : '');
  }

  // A non-interactive verdict chip (inside a row button).
  function chipSpan(chip, prefix) {
    return '<span class="' + chipClass(chip) + ' mc-pl-chip-static" title="' + esc(prefix + ': ' + chip.title) + '">' +
      '<span class="sr-only">' + esc(prefix) + ': </span>' + esc(chip.text) + '</span>';
  }

  function legRowHtml(leg, st, open) {
    var chip = chipFor(st);
    var km = st && st.distanceKm != null && isFinite(st.distanceKm) ? st.distanceKm : legDistanceKm(leg);
    var label = leg.a.name + ' to ' + leg.b.name + ', ' + fmtKm(km) + '. ' + chip.title + '. Show terrain profile';
    return '<div class="mc-pl-leg-row" data-leg="' + leg.i + '">' +
      '<span class="mc-pl-leg-rail" aria-hidden="true"></span>' +
      '<span class="mc-pl-leg-km">' + esc(fmtKm(km)) + '</span>' +
      '<button type="button" class="' + chipClass(chip) + '" data-leg-open="' + leg.i + '" title="' + esc(chip.title) +
        '" aria-label="' + esc(label) + '" aria-haspopup="dialog" aria-expanded="' + (open ? 'true' : 'false') + '">' + esc(chip.text) + '</button>' +
      '</div>';
  }

  function pointRowHtml(p, i, legHtml) {
    var name = pointName(p, i);
    var sub = fmtLatLon(p) + (p.node ? ' · ' + roleName(p.node.role) : '');
    var id = 'mc-pl-h-' + i;
    return '<li class="mc-pl-pt" data-pt="' + i + '">' +
      '<div class="mc-pl-pt-row">' +
        '<span class="mc-pl-badge' + (p.node ? ' mc-pl-badge-snapped' : '') + '" aria-hidden="true">' + (i + 1) + '</span>' +
        '<div class="mc-pl-main">' +
          '<span class="mc-pl-name" title="' + esc(name) + '">' + esc(name) + '</span>' +
          '<span class="mc-pl-sub">' + esc(sub) + '</span>' +
        '</div>' +
        '<div class="mc-pl-ant">' +
          '<label for="' + id + '">Antenna<span class="sr-only"> height of ' + esc(name) + ', metres</span></label>' +
          '<span class="mc-pl-ant-in"><input id="' + id + '" type="number" inputmode="decimal" min="' + HEIGHT_MIN + '" max="' + HEIGHT_MAX +
            '" step="0.5" value="' + esc(fmtH(p.h)) + '" data-h="' + i + '" aria-describedby="mc-pl-chain-err"><span aria-hidden="true">m</span></span>' +
        '</div>' +
        '<button type="button" class="mc-pl-icon-btn" data-remove="' + i + '" aria-label="Remove ' + esc(name) + '">' + icon('ph-x') + '</button>' +
      '</div>' +
      (legHtml || '') +
      '</li>';
  }

  function endLabel(which, end) {
    return end && end.node ? nodeLabel(end.node) : 'Point ' + which.toUpperCase();
  }

  function endRowHtml(which, end, picking) {
    var W = which.toUpperCase();
    var name = end ? endLabel(which, end) : 'Not set';
    var sub = end ? fmtLatLon(end) + (end.node ? ' · ' + roleName(end.node.role) : '') : 'Click the map or a node';
    var btn = picking ? 'Picking…' : end ? 'Change' : 'Pick on map';
    return '<div class="mc-pl-end' + (picking ? ' mc-pl-picking' : '') + (end ? '' : ' mc-pl-end-unset') + '" data-end="' + which + '">' +
      '<span class="mc-pl-badge mc-pl-badge-end" aria-hidden="true">' + W + '</span>' +
      '<div class="mc-pl-main">' +
        '<span class="mc-pl-name" title="' + esc(name) + '">' + esc(name) + '</span>' +
        '<span class="mc-pl-sub">' + esc(sub) + '</span>' +
      '</div>' +
      '<button type="button" class="mc-pl-btn mc-pl-btn-small" data-pick="' + which + '" aria-pressed="' + (picking ? 'true' : 'false') +
        '" aria-label="' + esc((end ? 'Change ' : 'Pick ') + W + ' on the map') + '">' + btn + '</button>' +
      '</div>';
  }

  function optionName(opt, idx) {
    if (opt && opt.kind === 'existing') return opt.name || (opt.pubkey ? String(opt.pubkey).slice(0, 8) : 'Unnamed node');
    return 'New site ' + (idx + 1);
  }

  function optionRowHtml(opt, list, idx, selected) {
    var name = optionName(opt, idx);
    var ca = chipFor(linkAnalysis(opt.leg_a)), cb = chipFor(linkAnalysis(opt.leg_b));
    var la = opt.leg_a || {}, lb = opt.leg_b || {};
    var sub = (list === 'existing' ? '' : fmtM(opt.elev_m) + ' ground · ') + fmtM(opt.antenna_m) + ' mast · ' +
      fmtKm(Number(la.distance_km) + Number(lb.distance_km)) + ' total';
    var id = (list === 'existing' ? 'e' : 'o') + (idx + 1);
    return '<li><button type="button" class="mc-pl-opt' + (selected ? ' mc-pl-opt-on' : '') + '" data-opt="' + id + '" aria-pressed="' + (selected ? 'true' : 'false') + '">' +
      '<span class="mc-pl-badge mc-pl-badge-' + list + '" aria-hidden="true">' + (idx + 1) + '</span>' +
      '<span class="mc-pl-main">' +
        '<span class="mc-pl-name">' + esc(name) + '</span>' +
        '<span class="mc-pl-opt-legs">' +
          '<span class="mc-pl-opt-leg"><span class="mc-pl-opt-end" aria-hidden="true">A</span>' + chipSpan(ca, 'A to relay') +
            '<span class="mc-pl-km">' + esc(fmtKm(la.distance_km)) + '</span></span>' +
          '<span class="mc-pl-opt-leg"><span class="mc-pl-opt-end" aria-hidden="true">B</span>' + chipSpan(cb, 'Relay to B') +
            '<span class="mc-pl-km">' + esc(fmtKm(lb.distance_km)) + '</span></span>' +
        '</span>' +
        '<span class="mc-pl-sub">' + esc(sub) + '</span>' +
      '</span>' +
      '<span class="mc-pl-score" title="Worst first-Fresnel-zone clearance of the two legs"><b>' + esc(fmtClearance(opt.bottleneck_pct)) +
        '</b>' + (isClearanceWord(opt.bottleneck_pct) ? '' : '<span>F1 clearance</span>') + '</span>' +
      '</button></li>';
  }

  function directHtml(d, nameA, nameB) {
    var an = linkAnalysis(d), chip = chipFor(an);
    var note = an.verdict === 'clear' || an.verdict === 'clear60'
      ? 'A and B should reach each other directly; a relay adds margin.'
      : an.verdict === 'marginal'
        ? 'The direct link is marginal; a relay can make it dependable.'
        : 'Terrain blocks the direct link, so a relay is needed.';
    var obst = an.verdict === 'blocked' && an.obstructionM > 0
      ? ' Terrain rises ' + Math.max(1, Math.round(an.obstructionM)) + ' m above the line ' + fmtKm(an.worstKm) + ' from ' + nameA + '.'
      : '';
    return '<section class="mc-pl-card mc-pl-direct" aria-labelledby="mc-pl-direct-h">' +
      '<div class="mc-pl-card-head"><h3 id="mc-pl-direct-h">Direct link</h3>' + chipSpan(chip, 'Direct link') + '</div>' +
      '<p class="mc-pl-sub">' + esc(nameA) + ' → ' + esc(nameB) + ' · ' + esc(fmtKm(d && d.distance_km)) + ' · ' +
        esc(clearanceMeta(d && d.clearance_pct)) + '</p>' +
      '<p class="mc-pl-note">' + esc(note + obst) + '</p>' +
      (d && finiteArray(d.elev_m) ? '<button type="button" class="mc-pl-btn mc-pl-btn-small" data-direct-profile="1" aria-haspopup="dialog">View profile</button>' : '') +
      '</section>';
  }

  function progressHtml(frac, label) {
    var known = frac != null && isFinite(frac);
    var pct = known ? Math.round(clamp01(frac) * 100) : 0;
    return '<div class="mc-pl-progress">' +
      '<div class="mc-pl-progress-text"><span>' + esc(label) + '</span>' + (known ? '<span>' + pct + '%</span>' : '') + '</div>' +
      '<div class="mc-pl-bar' + (known ? '' : ' mc-pl-bar-indet') + '" role="progressbar" aria-label="' + esc(label) +
        '" aria-valuemin="0" aria-valuemax="100"' + (known ? ' aria-valuenow="' + pct + '"' : '') + '>' +
        '<span' + (known ? ' style="width:' + pct + '%"' : '') + '></span></div>' +
      '</div>';
  }

  function errorHtml(msg, retryWhich) {
    return '<div class="mc-pl-error" role="alert">' + icon('ph-warning') +
      '<div><p>' + esc(msg) + '</p>' +
      (retryWhich ? '<button type="button" class="mc-pl-btn mc-pl-btn-small" data-retry="' + retryWhich + '">Try again</button>' : '') +
      '</div></div>';
  }

  // One stacked profile: title "A → B", meta (distance, FSPL, F1 clearance,
  // verdict), obstruction note, chart (or its loading/error state), readout.
  function profileSectionHtml(leg, k) {
    var an = leg.an;
    var km = an ? an.distanceKm : leg.distanceKm;
    var items = ['<span title="Link length">' + esc(fmtKm(km)) + '</span>'];
    if (an) {
      items.push('<span title="Free-space path loss">FSPL ' + an.fsplDb.toFixed(1) + ' dB</span>');
      items.push('<span title="Worst clearance of the first Fresnel zone">' +
        esc(an.clearance === Infinity ? 'F1 clearance —' : clearanceMeta(an.clearance * 100)) + '</span>');
    }
    var pill;
    if (an) {
      var v = verdicts()[an.verdict] || { label: an.verdict, detail: '', tone: 'muted' };
      pill = '<span class="mc-los-pill mc-los-tone-' + v.tone + '" title="' + esc(v.detail) + '">' + esc(v.label) + '</span>';
    } else {
      pill = '<span class="mc-los-pill mc-los-tone-muted' + (leg.error ? '' : ' mc-los-loading') + '">' + (leg.error ? 'n/a' : 'Checking…') + '</span>';
    }
    var html = '<section class="mc-pl-prof" data-prof="' + k + '">' +
      '<h3 class="mc-pl-prof-title" title="' + esc(leg.a.name + ' → ' + leg.b.name) + '">' + esc(leg.a.name) + ' <span aria-hidden="true">→</span><span class="sr-only"> to </span> ' + esc(leg.b.name) + '</h3>' +
      '<p class="mc-los-meta">' + items.join('<span class="mc-los-sep" aria-hidden="true">·</span>') + pill + '</p>';
    if (an && an.obstructionM > 0) {
      html += '<p class="mc-los-obst">Terrain rises ' + Math.max(1, Math.round(an.obstructionM)) + ' m above the line of sight ' +
        esc(fmtKm(an.worstKm)) + ' from ' + esc(leg.a.name) + '.</p>';
    }
    if (an) {
      html += '<div class="mc-los-chart">' + window.MeshLOS.renderProfileSvg(an, { nameA: leg.a.name, nameB: leg.b.name }) + '</div>' +
        '<p class="mc-los-readout">' + READOUT_HINT + '</p>';
    } else if (leg.error) {
      html += '<div class="mc-los-chart mc-los-chart-err"><div class="mc-los-chart-state mc-los-chart-error" role="status">' +
        (leg.error === 'no_terrain'
          ? '<strong>' + esc(errorReason(leg.error)) + '</strong>'
          : '<strong>No terrain profile for this leg</strong><span>' + esc(errorReason(leg.error)) + '.</span>') +
        '</div></div>';
    } else {
      html += '<div class="mc-los-chart mc-los-chart-wait"><div class="mc-los-chart-state" role="status">Loading terrain profile…</div></div>';
    }
    return html + '</section>';
  }

  // Hover readout for sample i of a profile leg.
  function readoutHtml(leg, an, i) {
    var ts = i / (an.n - 1);
    var edge = i === 0 || i === an.n - 1;
    var c = (an.losM[i] - an.terrainM[i]) / an.fresnelM[i];
    return [
      '<b>' + esc(fmtKm(ts * an.distanceKm)) + '</b> from ' + esc(leg.a.name),
      'terrain <b>' + Math.round(an.terrainM[i]) + ' m</b>',
      'LOS <b>' + Math.round(an.losM[i]) + ' m</b>',
      'clearance <b>' + (edge ? '—' : esc(fmtClearance(c * 100))) + '</b>'
    ].map(function (s) { return '<span class="mc-los-ri">' + s + '</span>'; })
      .join('<span class="mc-los-sep" aria-hidden="true">·</span>');
  }

  function legDistanceKm(leg) {
    return window.MeshLOS ? window.MeshLOS.haversineKm(leg.a, leg.b) : null;
  }

  // ─── Leg cache (terrain per leg, LRU) ───────────────────────────────────

  var legCache = new Map();

  function cacheGet(key) {
    if (!legCache.has(key)) return null;
    var v = legCache.get(key);
    legCache.delete(key);
    legCache.set(key, v);
    return v;
  }

  function cachePut(key, v) {
    if (legCache.has(key)) legCache.delete(key);
    while (legCache.size >= LEG_CACHE_MAX) legCache.delete(legCache.keys().next().value);
    legCache.set(key, v);
  }

  function isCacheable(r) { return !!r && (!!r.elevM || hasOwn(CACHEABLE_ERRORS, r.error || '')); }

  // ─── Page state ─────────────────────────────────────────────────────────

  var S = null;      // state while mounted
  var pageGen = 0;   // bumped by init/destroy; late async work compares

  function normCfg(raw) {
    var c = {};
    Object.keys(CFG_DEFAULTS).forEach(function (k) {
      var v = raw ? raw[k] : undefined;
      if (k === 'rangesKm') {
        var arr = Array.isArray(v) ? v.map(Number).filter(function (x) { return x > 0 && isFinite(x); }) : [];
        c[k] = arr.length ? arr : CFG_DEFAULTS[k].slice();
        return;
      }
      var n = Number(v);
      c[k] = v != null && v !== '' && isFinite(n) && n > 0 ? n : CFG_DEFAULTS[k];
    });
    if (c.rangesKm.indexOf(c.defaultRangeKm) < 0) c.defaultRangeKm = c.rangesKm[0];
    c.maxPoints = Math.max(2, Math.floor(c.maxPoints));
    return c;
  }

  function defaultHeight(node) {
    return node ? S.cfg.existingAntennaM : S.cfg.newAntennaM;
  }

  function isMobile() { return window.innerWidth <= MOBILE_MAX_W; }

  function reducedMotion() {
    try { return window.matchMedia('(prefers-reduced-motion: reduce)').matches; } catch (e) { return false; }
  }

  function raf(fn) {
    return typeof window.requestAnimationFrame === 'function' ? window.requestAnimationFrame(fn) : setTimeout(fn, 16);
  }

  function caf(id) {
    if (typeof window.cancelAnimationFrame === 'function') window.cancelAnimationFrame(id);
    else clearTimeout(id);
  }

  function listen(target, type, fn, opts) {
    target.addEventListener(type, fn, opts);
    S.unlisten.push(function () { target.removeEventListener(type, fn, opts); });
  }

  // ─── Hash params ────────────────────────────────────────────────────────

  function isPlannerHash(h) { return /^#\/tools\/planner(\?|$)/.test(String(h || '')); }

  function params() { return queryOf(location.hash); }

  // Merge partial into the hash query (null / '' removes a key) with
  // replaceState: app.js re-inits pages on hashchange, so never location.hash=.
  function setParams(partial) {
    var h = location.hash || '';
    if (!isPlannerHash(h)) return false;
    var cur = queryOf(h);
    Object.keys(partial || {}).forEach(function (k) {
      if (k === '__proto__' || k === 'constructor' || k === 'prototype') return;
      var v = partial[k];
      if (v == null || v === '') delete cur[k];
      else cur[k] = String(v);
    });
    replaceHash(BASE_HASH + buildQuery(cur));
    return true;
  }

  function replaceHash(next) {
    try {
      if (next !== location.hash && window.history && history.replaceState) history.replaceState(history.state, '', next);
    } catch (e) { /* history unavailable */ }
  }

  // ─── Lifecycle ──────────────────────────────────────────────────────────

  function init(container) {
    destroy();
    var gen = ++pageGen;
    if (!container) return;
    var raw = window.MC_PLANNER_CFG;
    if (raw === undefined) {
      // /api/config/client has not answered yet.
      var ready = window.MeshConfigReady;
      if (ready && typeof ready.then === 'function') {
        container.innerHTML = '<div class="mc-pl-off"><p class="mc-pl-off-wait" role="status">Loading the planner…</p></div>';
        var settle = function (ok) {
          if (gen !== pageGen) return;
          var c = window.MC_PLANNER_CFG;
          if (c) mount(container, c);
          else renderUnavailable(container, c === undefined && !ok ? 'error' : 'disabled');
        };
        ready.then(function () { settle(true); }, function () { settle(false); });
        return;
      }
      raw = null;
    }
    if (!raw) { renderUnavailable(container, 'disabled'); return; }
    mount(container, raw);
  }

  function renderUnavailable(container, why) {
    var title = why === 'error' ? 'The planner couldn’t start' : 'The repeater planner is not enabled on this server';
    var detail = why === 'error'
      ? esc('The server configuration didn’t load. Check your connection and reload the page.')
      : why === 'leaflet'
        ? esc('The map library didn’t load. Reload the page to try again.')
        : 'Server operators can turn it on with <code>planner.enabled</code> in config.json.';
    if (why === 'leaflet') title = 'The planner couldn’t start';
    container.innerHTML =
      '<div class="mc-pl-off">' +
        '<div class="mc-pl-off-card">' +
          '<span class="mc-pl-off-icon">' + icon('ph-map-trifold') + '</span>' +
          '<h2>Repeater planner</h2>' +
          '<p class="mc-pl-off-lede">' + esc(title) + '.</p>' +
          '<p class="mc-pl-off-detail">' + detail + '</p>' +
          '<a class="mc-pl-btn" href="#/tools">Back to tools</a>' +
        '</div>' +
      '</div>';
  }

  function pageHtml(cfg) {
    var tabs = MODES.map(function (m, i) {
      return '<button type="button" role="tab" class="mc-pl-tab" id="mc-pl-tab-' + m + '" aria-controls="mc-pl-panel-' + m +
        '" aria-selected="' + (i === 0 ? 'true' : 'false') + '" tabindex="' + (i === 0 ? '0' : '-1') + '" data-mode="' + m + '">' +
        MODE_LABELS[m] + '</button>';
    }).join('');
    var ranges = cfg.rangesKm.map(function (r) {
      return '<option value="' + esc(r) + '">' + esc(r) + ' km</option>';
    }).join('');
    function heightField(id, key, label) {
      return '<div class="mc-pl-field"><label for="' + id + '">' + label + '</label>' +
        '<span class="mc-pl-ant-in"><input id="' + id + '" type="number" inputmode="decimal" min="' + HEIGHT_MIN + '" max="' + HEIGHT_MAX +
        '" step="0.5" data-relay-h="' + key + '" aria-describedby="mc-pl-relay-err"><span aria-hidden="true">m</span></span></div>';
    }
    return '<div class="mc-pl-page mc-pl-mode-chain">' +
      '<div class="mc-pl-map" role="region" aria-label="Planner map"></div>' +
      '<aside class="mc-pl-side" aria-labelledby="mc-pl-h">' +
        '<div class="mc-pl-side-head">' +
          '<div class="mc-pl-titlebar">' +
            '<h2 id="mc-pl-h">Repeater planner</h2>' +
            '<button type="button" class="mc-pl-icon-btn mc-pl-sheet-toggle" aria-expanded="true" aria-controls="mc-pl-side-body" aria-label="Hide planner panel">' +
              icon('ph-caret-down') + '</button>' +
          '</div>' +
          '<div class="mc-pl-tabs" role="tablist" aria-label="Planner mode">' + tabs + '</div>' +
        '</div>' +
        '<div class="mc-pl-side-body" id="mc-pl-side-body">' +
          '<p class="mc-pl-notice" hidden></p>' +
          '<section class="mc-pl-panel" role="tabpanel" id="mc-pl-panel-chain" aria-labelledby="mc-pl-tab-chain" tabindex="0">' +
            '<p class="mc-pl-lede">Check line of sight along a chain of points.</p>' +
            '<p class="mc-pl-hint">Click the map to add a point, or click a node to use its location. Drag a point to move it.</p>' +
            '<div class="mc-pl-status" data-status="chain"></div>' +
            '<div class="mc-pl-chain-list"></div>' +
            '<p class="mc-pl-field-err" id="mc-pl-chain-err" aria-live="polite"></p>' +
            findFormHtml('chain', 'Add a point', 'Add') +
            '<div class="mc-pl-actions" data-actions="chain"></div>' +
          '</section>' +
          '<section class="mc-pl-panel" role="tabpanel" id="mc-pl-panel-relay" aria-labelledby="mc-pl-tab-relay" tabindex="0" hidden>' +
            '<p class="mc-pl-lede">Find the best relay site between two points.</p>' +
            '<div class="mc-pl-ends"></div>' +
            '<p class="mc-pl-hint mc-pl-pick-hint" aria-live="polite"></p>' +
            findFormHtml('relay', 'Set A', 'Set') +
            '<div class="mc-pl-fields">' +
              heightField('mc-pl-ha', 'ha', 'Antenna A') +
              heightField('mc-pl-hb', 'hb', 'Antenna B') +
              heightField('mc-pl-hr', 'hr', 'Relay antenna') +
              '<div class="mc-pl-field"><label for="mc-pl-range">Radio range</label>' +
                '<select id="mc-pl-range" data-relay-range>' + ranges + '</select></div>' +
              '<p class="mc-pl-field-err" id="mc-pl-relay-err" aria-live="polite"></p>' +
            '</div>' +
            '<div class="mc-pl-status" data-status="relay"></div>' +
            '<div class="mc-pl-results"></div>' +
            '<div class="mc-pl-actions" data-actions="relay"></div>' +
          '</section>' +
          '<section class="mc-pl-panel mc-pl-panel-area" role="tabpanel" id="mc-pl-panel-area" aria-labelledby="mc-pl-tab-area" tabindex="0" hidden></section>' +
          '<p class="sr-only" role="status" data-live></p>' +
        '</div>' +
        '<datalist id="mc-pl-nodes"></datalist>' +
      '</aside>' +
    '</div>';
  }

  function findFormHtml(which, label, button) {
    var id = 'mc-pl-find-' + which;
    return '<form class="mc-pl-find" data-find="' + which + '" novalidate>' +
      '<label for="' + id + '" data-find-label>' + label + '</label>' +
      '<div class="mc-pl-find-row">' +
        '<input id="' + id + '" type="text" list="mc-pl-nodes" autocomplete="off" spellcheck="false" placeholder="Node name or lat, lon" aria-describedby="' + id + '-err">' +
        '<button type="submit" class="mc-pl-btn" data-find-btn>' + button + '</button>' +
      '</div>' +
      '<p class="mc-pl-field-err" id="' + id + '-err" aria-live="polite"></p>' +
      '</form>';
  }

  function mount(container, rawCfg) {
    if (typeof L === 'undefined' || !window.MeshLOS) { renderUnavailable(container, 'leaflet'); return; }
    var cfg = normCfg(rawCfg);
    var p = parseParams(location.hash);
    container.innerHTML = pageHtml(cfg);
    var root = container.querySelector('.mc-pl-page');
    S = {
      gen: pageGen, cfg: cfg, rawCfg: rawCfg, container: container, root: root,
      map: null, tiles: null, layers: {}, nodeRenderer: null,
      rawNodes: [], nodes: [], nodeByCoord: new Map(), nodesState: 'loading', nodesPromise: null,
      mode: null, modeParams: {}, userMoved: false, source: null,
      unlisten: [], themeObs: null, ro: null, roRaf: 0, sheetAuto: false,
      els: {
        map: root.querySelector('.mc-pl-map'),
        side: root.querySelector('.mc-pl-side'),
        sheetBtn: root.querySelector('.mc-pl-sheet-toggle'),
        tabs: root.querySelectorAll('.mc-pl-tab'),
        panels: {
          chain: root.querySelector('#mc-pl-panel-chain'),
          relay: root.querySelector('#mc-pl-panel-relay'),
          area: root.querySelector('#mc-pl-panel-area')
        },
        notice: root.querySelector('.mc-pl-notice'),
        live: root.querySelector('[data-live]'),
        datalist: root.querySelector('#mc-pl-nodes'),
        chainStatus: root.querySelector('[data-status="chain"]'),
        chainList: root.querySelector('.mc-pl-chain-list'),
        chainErr: root.querySelector('#mc-pl-chain-err'),
        chainActions: root.querySelector('[data-actions="chain"]'),
        ends: root.querySelector('.mc-pl-ends'),
        pickHint: root.querySelector('.mc-pl-pick-hint'),
        relayFind: root.querySelector('[data-find="relay"]'),
        relayStatus: root.querySelector('[data-status="relay"]'),
        results: root.querySelector('.mc-pl-results'),
        relayErr: root.querySelector('#mc-pl-relay-err'),
        relayActions: root.querySelector('[data-actions="relay"]'),
        range: root.querySelector('#mc-pl-range')
      },
      chain: {
        pts: [], legs: [], legViews: [], open: null, undo: null,
        status: null, ctrl: null, timer: 0, inputTimer: 0, liveState: '',
        ptLayer: null, legLayer: null
      },
      relay: {
        a: null, b: null, ha: cfg.newAntennaM, hb: cfg.newAntennaM, hr: cfg.newAntennaM, range: cfg.defaultRangeKm,
        pick: 'a', result: null, resultKey: null, status: null, sel: null, pendingSel: null,
        ctrl: null, ctrlKey: null, timer: 0, inputTimer: 0, liveState: '', haAuto: false, hbAuto: false
      },
      area: { handle: null, layer: null, waiting: false },
      panel: {
        el: null, legs: [], title: '', owner: null, nav: null, ctrl: null, opener: null,
        cursor: null, ptr: null, rafId: 0, layer: null, cursorMarker: null
      }
    };
    restoreChain(p.chain);
    restoreRelay(p.relay);
    createMap();
    bindUi();
    setMode(p.mode, { initial: true });
    initialView(p);
    loadNodes();
  }

  function destroy() {
    pageGen++;
    if (!S) return;
    var s = S;
    try {
      clearTimeout(s.chain.timer);
      clearTimeout(s.chain.inputTimer);
      clearTimeout(s.relay.timer);
      clearTimeout(s.relay.inputTimer);
      if (s.chain.ctrl) s.chain.ctrl.abort();
      if (s.relay.ctrl) s.relay.ctrl.abort();
      leaveArea();
      closePanel({ restoreFocus: false });
      s.unlisten.forEach(function (fn) { try { fn(); } catch (e) { /* already gone */ } });
      if (s.themeObs) s.themeObs.disconnect();
      if (s.ro) s.ro.disconnect();
      if (s.roRaf) caf(s.roRaf);
      if (s.map) { s.map.off(); s.map.remove(); }
    } catch (e) {
      console.warn('[planner] destroy:', e);
    }
    S = null;
  }

  // ─── Map ────────────────────────────────────────────────────────────────

  function isDark() {
    return typeof window.MC_isDarkTheme === 'function' ? !!window.MC_isDarkTheme() : false;
  }

  // Same tile handling as map.js: a theme-synced "auto" group the layer
  // control can swap out, resolved through map-tile-providers.js.
  function syncTiles() {
    if (!S || !S.tiles || typeof window.MC_resolveTile !== 'function') return;
    var dark = isDark();
    var r;
    try { r = window.MC_resolveTile(dark); } catch (e) { return; }
    var t = S.tiles;
    var opts = { attribution: r.attribution, maxZoom: r.maxZoom || 19 };
    if (!t.base) {
      t.base = L.tileLayer(r.url, opts).addTo(t.auto);
    } else {
      t.base.setUrl(r.url);
      if (t.base.options) { t.base.options.attribution = r.attribution; t.base.options.maxZoom = opts.maxZoom; }
    }
    if (dark && r.refUrl) {
      if (!t.ref) t.ref = L.tileLayer(r.refUrl, opts).addTo(t.auto);
      else t.ref.setUrl(r.refUrl);
    } else if (t.ref) {
      t.auto.removeLayer(t.ref);
      t.ref = null;
    }
    if (typeof window.MC_applyTileFilter === 'function') window.MC_applyTileFilter();
    if (S.map && S.map.attributionControl && S.map.attributionControl._update) {
      try { S.map.attributionControl._update(); } catch (e) { /* not ready */ }
    }
  }

  function createMap() {
    var map = L.map(S.els.map, { zoomControl: true, worldCopyJump: true });
    S.map = map;
    map.setView(FALLBACK_VIEW.center, FALLBACK_VIEW.zoom);
    S.tiles = { auto: L.layerGroup().addTo(map), base: null, ref: null };
    syncTiles();
    if (typeof window.MC_createLayerControl === 'function') {
      try { window.MC_createLayerControl(map, S.tiles.auto, 'topleft'); } catch (e) { console.warn('[planner] layer control:', e); }
    }
    if (typeof MutationObserver === 'function') {
      S.themeObs = new MutationObserver(function () { syncTiles(); });
      S.themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    }
    listen(window, 'mc-tile-provider-changed', function (e) {
      var dark = isDark();
      if (e && e.detail && e.detail.type && e.detail.type !== (dark ? 'dark' : 'light')) return;
      syncTiles();
    });

    // Nodes sit in their own pane under the overlay pane, so legs always draw on top.
    map.createPane('mc-pl-nodes');
    map.getPane('mc-pl-nodes').style.zIndex = '390';
    S.nodeRenderer = L.svg({ pane: 'mc-pl-nodes' });
    S.layers.nodes = L.layerGroup().addTo(map);
    S.layers.chain = L.layerGroup().addTo(map);
    S.chain.legLayer = L.layerGroup().addTo(S.layers.chain);
    S.chain.ptLayer = L.layerGroup().addTo(S.layers.chain);
    S.layers.relay = L.layerGroup().addTo(map);
    S.layers.panel = L.layerGroup().addTo(map);
    S.panel.layer = S.layers.panel;

    map.on('click', onMapClick);
    map.on('dragstart zoomstart', function () { if (S) S.userMoved = true; });

    if (typeof ResizeObserver === 'function') {
      S.ro = new ResizeObserver(function () {
        if (!S || S.roRaf) return;
        S.roRaf = raf(function () { if (!S) return; S.roRaf = 0; S.map.invalidateSize({ pan: false }); });
      });
      S.ro.observe(S.els.map);
    } else {
      setTimeout(function () { if (S && S.map === map) map.invalidateSize(); }, 100);
    }
  }

  // Points in the URL win; then the map page's last view; then the
  // operator's configured centre.
  function initialView(p) {
    var pts = p.mode === 'relay'
      ? [S.relay.a, S.relay.b].filter(Boolean)
      : p.mode === 'chain' ? S.chain.pts : [];
    if (pts.length) { fitPoints(pts, false); return; }
    var saved = null;
    try { saved = JSON.parse(localStorage.getItem('map-view') || 'null'); } catch (e) { saved = null; }
    if (saved && [saved.lat, saved.lng, saved.zoom].every(function (v) { return typeof v === 'number' && isFinite(v); })) {
      S.map.setView([saved.lat, L.latLng(saved.lat, saved.lng).wrap().lng], Math.max(2, Math.min(18, saved.zoom)));
      return;
    }
    var gen = pageGen;
    fetch('/api/config/map').then(function (r) { return r.ok ? r.json() : null; }).then(function (m) {
      if (gen !== pageGen || !S || S.userMoved || !m) return;
      if (Array.isArray(m.center) && m.center.length === 2 && validLatLon(Number(m.center[0]), Number(m.center[1]))) {
        S.map.setView([Number(m.center[0]), Number(m.center[1])], typeof m.zoom === 'number' ? m.zoom : FALLBACK_VIEW.zoom);
      }
    }).catch(function () { /* keep the fallback view */ });
  }

  function fitPoints(pts, animate) {
    if (!S || !pts.length) return;
    var lls = pts.map(function (p) { return [p.lat, p.lon]; });
    try {
      if (lls.length === 1) S.map.setView(lls[0], Math.max(S.map.getZoom(), 11), { animate: !!animate });
      else S.map.fitBounds(L.latLngBounds(lls), { padding: [48, 48], maxZoom: 14, animate: !!animate && !reducedMotion() });
    } catch (e) { /* map not sized yet */ }
  }

  function onMapClick(e) {
    if (!S || !e || !e.latlng) return;
    var ll = e.latlng.wrap();
    if (S.mode === 'chain') addChainPoint({ lat: ll.lat, lon: ll.lng, node: null });
    else if (S.mode === 'relay') {
      var which = S.relay.pick;
      if (which) setRelayEnd(which, { lat: ll.lat, lon: ll.lng, node: null });
    }
  }

  function onNodeClick(n) {
    if (!S) return;
    if (S.mode === 'chain') addChainPoint({ lat: n.lat, lon: n.lon, node: n });
    else if (S.mode === 'relay' && S.relay.pick) setRelayEnd(S.relay.pick, { lat: n.lat, lon: n.lon, node: n });
  }

  // Nearest node within SNAP_PX of a dropped marker, by screen distance.
  function nearestNode(latlng) {
    if (!S || !S.nodes.length) return null;
    var map = S.map;
    var p = map.latLngToContainerPoint(latlng);
    var bounds = map.getBounds();
    var best = null, bd = SNAP_PX * SNAP_PX;
    for (var i = 0; i < S.nodes.length; i++) {
      var n = S.nodes[i];
      if (!bounds.contains([n.lat, n.lon])) continue;
      var q = map.latLngToContainerPoint([n.lat, n.lon]);
      var d = (q.x - p.x) * (q.x - p.x) + (q.y - p.y) * (q.y - p.y);
      if (d <= bd) { bd = d; best = n; }
    }
    return best;
  }

  function stopEvent(e) {
    if (e && e.originalEvent && L.DomEvent) L.DomEvent.stopPropagation(e.originalEvent);
  }

  function pinIcon(text, cls) {
    return L.divIcon({
      className: 'mc-pl-pin-wrap',
      html: '<span class="mc-pl-pin ' + (cls || '') + '">' + esc(text) + '</span>',
      iconSize: [28, 28],
      iconAnchor: [14, 14]
    });
  }

  function labelMarker(mk, label) {
    var el = mk.getElement && mk.getElement();
    if (el) el.setAttribute('aria-label', label);
  }

  // ─── Known nodes ────────────────────────────────────────────────────────

  function loadNodes() {
    var gen = pageGen;
    var p;
    if (typeof api === 'function') {
      var ttl = (typeof CLIENT_TTL !== 'undefined' && CLIENT_TTL && CLIENT_TTL.nodeList) || 60000;
      p = api('/nodes?limit=2000', { ttl: ttl });
    } else {
      p = fetch('/api/nodes?limit=2000').then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      });
    }
    S.nodesPromise = Promise.resolve(p).then(function (data) {
      if (gen !== pageGen || !S) return;
      var list = data && Array.isArray(data.nodes) ? data.nodes : Array.isArray(data) ? data : [];
      S.rawNodes = list;
      S.nodes = normNodes(list);
      S.nodeByCoord = new Map();
      S.nodes.forEach(function (n) {
        var k = coordKey(n);
        if (!S.nodeByCoord.has(k) || (n.major && !S.nodeByCoord.get(k).major)) S.nodeByCoord.set(k, n);
      });
      S.nodesState = 'ready';
      drawNodes();
      fillDatalist();
      resnap();
    }).catch(function (e) {
      if (gen !== pageGen || !S) return;
      console.warn('[planner] nodes:', e);
      S.nodesState = 'error';
      showNotice('Known nodes didn’t load, so points can’t snap to them. You can still place points on the map.');
    });
  }

  function showNotice(msg) {
    var el = S.els.notice;
    el.textContent = msg;
    el.hidden = !msg;
  }

  function drawNodes() {
    var layer = S.layers.nodes;
    layer.clearLayers();
    // Minor nodes first so repeaters and rooms draw on top.
    var ordered = S.nodes.filter(function (n) { return !n.major; }).concat(S.nodes.filter(function (n) { return n.major; }));
    ordered.forEach(function (n) {
      var m = L.circleMarker([n.lat, n.lon], {
        renderer: S.nodeRenderer,
        radius: n.major ? 6 : 4,
        weight: n.major ? 2 : 1,
        className: 'mc-pl-node mc-pl-node-' + roleClass(n.role) + (n.major ? ' mc-pl-node-major' : ''),
        bubblingMouseEvents: false
      });
      m.bindTooltip(nodeTooltipHtml(n), { direction: 'top', offset: [0, -6], className: 'mc-pl-tip' });
      m.on('click', function (e) { stopEvent(e); onNodeClick(n); });
      layer.addLayer(m);
    });
  }

  function fillDatalist() {
    var dl = S.els.datalist;
    while (dl.firstChild) dl.removeChild(dl.firstChild);
    var seen = {}, names = [];
    S.nodes.forEach(function (n) {
      if (!n.name || hasOwn(seen, n.name)) return;
      seen[n.name] = true;
      names.push(n);
    });
    names.sort(function (a, b) { return (b.major - a.major) || a.name.localeCompare(b.name); });
    names.slice(0, DATALIST_MAX).forEach(function (n) {
      var o = document.createElement('option');
      o.value = n.name;
      o.label = roleName(n.role);
      dl.appendChild(o);
    });
  }

  // URL points carry coordinates only; a point sitting exactly on a known
  // node is that node again (name, default antenna if the URL had none).
  function resnap() {
    var changed = false;
    S.chain.pts.forEach(function (p) {
      var n = S.nodeByCoord.get(coordKey(p));
      if (n && !p.node) {
        p.node = n;
        if (p.hAuto) { p.h = S.cfg.existingAntennaM; p.hAuto = false; }
        changed = true;
      }
    });
    if (changed) {
      rebuildChainLegs();
      if (S.mode === 'chain') { renderChain(); drawChain(); writeChainUrl(); }
    }
    var r = S.relay, rc = false;
    ['a', 'b'].forEach(function (w) {
      var end = r[w];
      if (!end || end.node) return;
      var n = S.nodeByCoord.get(coordKey(end));
      if (!n) return;
      end.node = n;
      if (r['h' + w + 'Auto']) { r['h' + w] = S.cfg.existingAntennaM; r['h' + w + 'Auto'] = false; rc = true; }
    });
    if (S.mode === 'relay') {
      renderRelayEnds();
      if (rc) { syncRelayFields(); relayParamsChanged(); }
      else if (r.result) renderRelayResults();
    }
  }

  // ─── UI wiring ──────────────────────────────────────────────────────────

  function bindUi() {
    var root = S.root;
    listen(root, 'click', onRootClick);
    listen(root, 'input', onRootInput);
    listen(root, 'change', onRootChange);
    listen(root, 'submit', onFindSubmit);
    listen(root.querySelector('.mc-pl-tabs'), 'keydown', onTabKey);
    listen(document, 'keydown', onDocKey);
    S.els.range.value = String(S.relay.range);
    syncRelayFields();
  }

  function onRootClick(e) {
    var t = e.target && e.target.closest ? e.target.closest('button') : null;
    if (!t || !S.root.contains(t)) return;
    var ds = t.dataset || {};
    if (ds.mode) { setMode(ds.mode); return; }
    if (t === S.els.sheetBtn) { toggleSheet(); return; }
    if (ds.remove != null) { removeChainPoint(Number(ds.remove)); return; }
    if (ds.legOpen != null) { openChainLeg(Number(ds.legOpen), { focus: true }); return; }
    if (ds.retry === 'chain') { fetchChain(); return; }
    if (ds.retry === 'relay') { fetchRelay(); return; }
    if (ds.clear === 'chain') { clearChain(); return; }
    if (ds.undo === 'chain') { undoClearChain(); return; }
    if (ds.clear === 'relay') { clearRelay(); return; }
    if (ds.pick) { togglePick(ds.pick); return; }
    if (ds.opt) { selectRelayOption(ds.opt, { focus: true }); return; }
    if (ds.directProfile != null) { openDirectProfile(); return; }
  }

  function onRootInput(e) {
    var t = e.target;
    if (!t || !t.dataset) return;
    if (t.dataset.h != null) {
      clearTimeout(S.chain.inputTimer);
      S.chain.inputTimer = setTimeout(function () { applyChainHeight(t, false); }, INPUT_DEBOUNCE_MS);
    } else if (t.dataset.relayH) {
      clearTimeout(S.relay.inputTimer);
      S.relay.inputTimer = setTimeout(function () { applyRelayHeight(t, false); }, DEBOUNCE_MS);
    } else if (t.closest && t.closest('.mc-pl-find')) {
      var err = t.closest('.mc-pl-find').querySelector('.mc-pl-field-err');
      if (err) err.textContent = '';
      t.removeAttribute('aria-invalid');
    }
  }

  function onRootChange(e) {
    var t = e.target;
    if (!t || !t.dataset) return;
    if (t.dataset.h != null) { clearTimeout(S.chain.inputTimer); applyChainHeight(t, true); }
    else if (t.dataset.relayH) { clearTimeout(S.relay.inputTimer); applyRelayHeight(t, true); }
    else if (t.dataset.relayRange != null) {
      var v = Number(t.value);
      if (S.cfg.rangesKm.indexOf(v) >= 0 && v !== S.relay.range) { S.relay.range = v; relayParamsChanged(); }
    }
  }

  function onFindSubmit(e) {
    var form = e.target;
    if (!form || !form.dataset || !form.dataset.find) return;
    e.preventDefault();
    var input = form.querySelector('input');
    var err = form.querySelector('.mc-pl-field-err');
    var hit = resolveQuery(input.value, S.nodes);
    if (!hit) {
      input.setAttribute('aria-invalid', 'true');
      err.textContent = String(input.value).trim()
        ? 'No node matches that name. Try another name, or enter coordinates like 37.77, -122.42.'
        : 'Type a node name or coordinates like 37.77, -122.42.';
      return;
    }
    input.removeAttribute('aria-invalid');
    err.textContent = '';
    if (form.dataset.find === 'chain') {
      if (addChainPoint(hit)) { input.value = ''; fitPoints(S.chain.pts, true); }
    } else if (S.relay.pick) {
      var which = S.relay.pick;
      setRelayEnd(which, hit);
      input.value = '';
      fitPoints([S.relay.a, S.relay.b].filter(Boolean), true);
      // Both ends set hides the form; keep focus on the end just set.
      if (form.hidden) {
        var btn = S.els.ends.querySelector('[data-pick="' + which + '"]');
        if (btn) btn.focus();
      }
    }
  }

  function onTabKey(e) {
    var keys = { ArrowLeft: -1, ArrowRight: 1, Home: 'first', End: 'last' };
    if (!hasOwn(keys, e.key)) return;
    e.preventDefault();
    var i = MODES.indexOf(S.mode);
    var k = keys[e.key];
    var next = k === 'first' ? 0 : k === 'last' ? MODES.length - 1 : (i + k + MODES.length) % MODES.length;
    setMode(MODES[next]);
    S.els.tabs[next].focus();
  }

  function onDocKey(e) {
    if (e.key !== 'Escape' || !S) return;
    if (S.panel.el) { e.preventDefault(); closePanel({ restoreFocus: true }); return; }
    if (S.mode === 'relay' && S.relay.pick && S.relay.a && S.relay.b) { e.preventDefault(); togglePick(S.relay.pick); }
  }

  function announce(msg) {
    if (S && S.els.live) S.els.live.textContent = msg;
  }

  // ─── Modes ──────────────────────────────────────────────────────────────

  function setMode(mode, opts) {
    opts = opts || {};
    if (!S) return;
    if (MODES.indexOf(mode) < 0) mode = 'chain';
    var prev = S.mode;
    if (prev === mode) return;
    if (prev) {
      var keep = params();
      delete keep.mode;
      S.modeParams[prev] = keep;
      leaveMode(prev);
    }
    closePanel({ restoreFocus: false });
    S.mode = mode;
    if (opts.initial) setParams({ mode: mode });
    else replaceHash(BASE_HASH + buildQuery(mode === 'area' ? Object.assign({ mode: mode }, S.modeParams.area || {}) : { mode: mode }));
    MODES.forEach(function (m, i) {
      var on = m === mode;
      var tab = S.els.tabs[i];
      tab.setAttribute('aria-selected', on ? 'true' : 'false');
      tab.tabIndex = on ? 0 : -1;
      S.els.panels[m].hidden = !on;
      S.root.classList.toggle('mc-pl-mode-' + m, on);
    });
    enterMode(mode);
  }

  function leaveMode(mode) {
    // State stays in memory (switching back restores it); the map layers go.
    if (mode === 'chain') {
      S.chain.ptLayer.clearLayers();
      S.chain.legLayer.clearLayers();
      S.chain.legViews = [];
    } else if (mode === 'relay') {
      clearTimeout(S.relay.timer);
      clearTimeout(S.relay.inputTimer);
      if (S.relay.ctrl) { S.relay.ctrl.abort(); S.relay.ctrl = null; }
      if (S.relay.status && S.relay.status.kind !== 'error') S.relay.status = null;
      S.layers.relay.clearLayers();
    } else if (mode === 'area') {
      leaveArea();
    }
  }

  function enterMode(mode) {
    if (mode === 'chain') {
      writeChainUrl();
      renderChain();
      drawChain();
      scheduleChainFetch();
    } else if (mode === 'relay') {
      writeRelayUrl();
      renderRelay();
      drawRelay();
      scheduleRelayFetch();
    } else {
      enterArea();
    }
  }

  // ─── Chain mode ─────────────────────────────────────────────────────────

  function restoreChain(pc) {
    var max = S.cfg.maxPoints;
    S.chain.pts = pc.pts.slice(0, max).map(function (pt, i) {
      var h = pc.h[i];
      return { lat: pt.lat, lon: pt.lon, node: null, h: h != null ? h : S.cfg.newAntennaM, hAuto: h == null };
    });
    rebuildChainLegs();
  }

  function writeChainUrl() {
    if (!S || S.mode !== 'chain') return;
    var pts = S.chain.pts;
    setParams({
      pts: serializePts(pts) || null,
      h: pts.length ? pts.map(function (p) { return fmtH(p.h); }).join(',') : null
    });
  }

  function chainLegState(leg) {
    if (leg.analysis) return leg.analysis;
    if (leg.result && leg.result.error) return leg.result;
    var st = S.chain.status;
    if (!leg.result && st && st.kind === 'error') return { error: 'request' };
    return null;
  }

  function analyzeLeg(leg, result) {
    if (!result || !result.elevM) return null;
    return window.MeshLOS.analyze({
      distanceKm: result.distanceKm != null ? result.distanceKm : legDistanceKm(leg),
      elevM: result.elevM,
      antennaAM: leg.ha,
      antennaBM: leg.hb,
      freqMHz: S.cfg.frequencyMhz,
      kFactor: S.cfg.kFactor
    });
  }

  function attachResult(leg, res) {
    leg.result = res || null;
    leg.analysis = analyzeLeg(leg, leg.result);
    if (leg.result && leg.result.elevM && !leg.analysis) leg.result = { error: 'bad_data', distanceKm: leg.result.distanceKm };
  }

  function rebuildChainLegs() {
    S.chain.legs = legsFromPoints(S.chain.pts);
    S.chain.legs.forEach(function (leg) { attachResult(leg, cacheGet(leg.key)); });
  }

  function addChainPoint(pt) {
    var c = S.chain;
    if (c.pts.length >= S.cfg.maxPoints) {
      S.els.chainErr.textContent = 'You can place up to ' + S.cfg.maxPoints + ' points. Remove one to add another.';
      return false;
    }
    S.els.chainErr.textContent = '';
    c.undo = null;
    c.pts.push({ lat: round(pt.lat), lon: round(pt.lon), node: pt.node || null, h: defaultHeight(pt.node), hAuto: false });
    chainChanged();
    announce(pointName(c.pts[c.pts.length - 1], c.pts.length - 1) + ' added');
    return true;
  }

  function removeChainPoint(i) {
    var c = S.chain;
    if (!(i >= 0 && i < c.pts.length)) return;
    var name = pointName(c.pts[i], i);
    c.pts.splice(i, 1);
    c.undo = null;
    S.els.chainErr.textContent = '';
    chainChanged();
    announce(name + ' removed');
    // Keep keyboard focus in the list.
    var btn = S.els.chainList.querySelector('[data-remove="' + Math.min(i, c.pts.length - 1) + '"]');
    (btn || S.root.querySelector('#mc-pl-find-chain')).focus();
  }

  function clearChain() {
    var c = S.chain;
    if (!c.pts.length) return;
    c.undo = c.pts.slice();
    c.pts = [];
    chainChanged();
    var u = S.els.chainList.querySelector('[data-undo]');
    if (u) u.focus();
  }

  function undoClearChain() {
    var c = S.chain;
    if (!c.undo) return;
    c.pts = c.undo;
    c.undo = null;
    chainChanged();
    fitPoints(c.pts, true);
  }

  // A point moved onto (or off) a node takes that kind's default mast,
  // unless its height was changed by hand.
  function snapHeight(p, newNode) {
    if (p.h === defaultHeight(p.node)) p.h = defaultHeight(newNode);
    p.node = newNode;
  }

  function moveChainPoint(i, ll) {
    var p = S.chain.pts[i];
    if (!p) return;
    var snap = nearestNode(ll);
    var w = L.latLng(ll).wrap();
    p.lat = round(snap ? snap.lat : w.lat);
    p.lon = round(snap ? snap.lon : w.lng);
    snapHeight(p, snap || null);
    p.hAuto = false;
    chainChanged();
  }

  function applyChainHeight(input, commit) {
    var i = Number(input.dataset.h);
    var p = S.chain.pts[i];
    if (!p) return;
    var v = parseHeight(input.value);
    if (v == null) {
      if (commit || input.getAttribute('aria-invalid') === 'true') {
        input.setAttribute('aria-invalid', 'true');
        S.els.chainErr.textContent = 'Antenna height must be ' + HEIGHT_MIN + '–' + HEIGHT_MAX + ' m.';
      }
      return;
    }
    input.removeAttribute('aria-invalid');
    if (!S.root.querySelector('[data-h][aria-invalid="true"]')) S.els.chainErr.textContent = '';
    if (v === p.h) return;
    p.h = v;
    p.hAuto = false;
    // Antenna edits re-analyse the cached terrain; nothing is refetched.
    var keep = {};
    S.chain.legs.forEach(function (l) { keep[l.key] = l.result; });
    S.chain.legs = legsFromPoints(S.chain.pts);
    S.chain.legs.forEach(function (l) { attachResult(l, keep[l.key] || cacheGet(l.key)); });
    writeChainUrl();
    updateChainLegRows();
    drawChainLegs();
    refreshChainPanel();
  }

  function chainChanged() {
    writeChainUrl();
    rebuildChainLegs();
    renderChain();
    drawChain();
    scheduleChainFetch();
    refreshChainPanel();
  }

  // Keep an open chain profile in step with the chain, or close it.
  function refreshChainPanel() {
    var c = S.chain;
    if (S.panel.owner !== 'chain') return;
    var k = -1;
    c.legs.forEach(function (l, i) { if (k < 0 && l.key === S.panel.legKey) k = i; });
    if (k < 0) { closePanel({ restoreFocus: false }); return; }
    openChainLeg(k, { refresh: true });
  }

  function scheduleChainFetch() {
    var c = S.chain;
    clearTimeout(c.timer);
    if (c.ctrl) { c.ctrl.abort(); c.ctrl = null; }
    var missing = c.legs.some(function (l) { return !l.result; });
    if (!missing) { setChainStatus(null); return; }
    setChainStatus({ kind: 'pending' });
    c.timer = setTimeout(fetchChain, DEBOUNCE_MS);
  }

  // ONE request per point-set change: the sub-chain from the first leg
  // without terrain to the last one (cached legs in between ride along).
  function fetchChain() {
    var c = S.chain;
    clearTimeout(c.timer);
    if (c.ctrl) { c.ctrl.abort(); c.ctrl = null; }
    var first = -1, last = -1;
    c.legs.forEach(function (l, k) { if (!l.result) { if (first < 0) first = k; last = k; } });
    if (first < 0) { setChainStatus(null); return; }
    var pts = c.pts.slice(first, last + 2);
    var keys = c.legs.slice(first, last + 1).map(function (l) { return l.key; });
    var ctrl = typeof AbortController === 'function' ? new AbortController() : null;
    c.ctrl = ctrl;
    var mine = function () { return S && S.chain === c && c.ctrl === ctrl; };
    setChainStatus({ kind: 'pending' });
    fetchJob('/api/planner/profile?pts=' + serializePts(pts), {
      signal: ctrl ? ctrl.signal : null,
      onProgress: function (state, progress) {
        if (mine()) setChainStatus({ kind: 'progress', state: state, progress: progress });
      }
    }).then(function (body) {
      if (!mine()) return;
      c.ctrl = null;
      var legs = body && Array.isArray(body.legs) ? body.legs : null;
      if (!legs || legs.length !== keys.length) {
        var e = new Error('Unexpected response from the server');
        e.status = 502;
        throw e;
      }
      if (body.source) S.source = String(body.source);
      var fresh = {};
      keys.forEach(function (k, j) {
        var r = legResultFrom(legs[j]);
        fresh[k] = r;
        if (isCacheable(r)) cachePut(k, r);
      });
      c.legs.forEach(function (l) { if (!l.result && hasOwn(fresh, l.key)) attachResult(l, fresh[l.key]); });
      setChainStatus(null);
      updateChainLegRows();
      drawChainLegs();
      refreshChainPanel();
      var bad = c.legs.filter(function (l) { return l.analysis && l.analysis.verdict === 'blocked'; }).length;
      announce('Line of sight checked for ' + c.legs.length + (c.legs.length === 1 ? ' leg' : ' legs') + (bad ? ', ' + bad + ' blocked' : ''));
    }).catch(function (err) {
      if (isAbort(err) || !mine()) return;
      c.ctrl = null;
      setChainStatus({ kind: 'error', err: err });
      updateChainLegRows();
      drawChainLegs();
      refreshChainPanel();
    });
  }

  function setChainStatus(st) {
    var c = S.chain;
    c.status = st;
    var el = S.els.chainStatus;
    if (!st) { el.innerHTML = ''; c.liveState = ''; return; }
    if (st.kind === 'error') {
      el.innerHTML = errorHtml(jobErrorText(st.err), isRetryable(st.err) ? 'chain' : null);
      return;
    }
    var label = st.kind === 'progress' ? (JOB_STATES[st.state] || 'Working…') : 'Checking terrain…';
    el.innerHTML = progressHtml(st.kind === 'progress' ? st.progress : null, label);
    if (c.liveState !== label) { c.liveState = label; announce(label); }
  }

  function renderChain() {
    var c = S.chain, html;
    if (!c.pts.length) {
      html = c.undo
        ? '<div class="mc-pl-empty"><p>Cleared ' + c.undo.length + (c.undo.length === 1 ? ' point' : ' points') + '.</p>' +
          '<button type="button" class="mc-pl-btn mc-pl-btn-small" data-undo="chain">' + icon('ph-arrow-u-up-left') + ' Undo</button></div>'
        : '<div class="mc-pl-empty">' + icon('ph-map-pin') +
          '<p><strong>No points yet.</strong> Click the map to place the first point, or click a repeater to start from it.</p></div>';
    } else {
      html = '<ol class="mc-pl-pts" aria-label="Points">' + c.pts.map(function (p, i) {
        var leg = c.legs[i];
        return pointRowHtml(p, i, leg ? legRowHtml(leg, chainLegState(leg), c.open === i && !!S.panel.el) : '');
      }).join('') + '</ol>';
      if (c.pts.length === 1) html += '<p class="mc-pl-hint">Add a second point to check line of sight.</p>';
    }
    S.els.chainList.innerHTML = html;
    var full = c.pts.length >= S.cfg.maxPoints;
    S.els.chainActions.innerHTML =
      '<span class="mc-pl-count">' + c.pts.length + ' of ' + S.cfg.maxPoints + ' points</span>' +
      (c.pts.length ? '<button type="button" class="mc-pl-btn mc-pl-btn-quiet" data-clear="chain">' + icon('ph-trash') + ' Clear points</button>' : '');
    var btn = S.root.querySelector('[data-find="chain"] [data-find-btn]');
    if (btn) btn.disabled = full;
  }

  // Leg rows in place (keeps focus on a chip or an antenna input).
  function updateChainLegRows() {
    var c = S.chain;
    c.legs.forEach(function (leg, k) {
      var row = S.els.chainList.querySelector('[data-leg="' + k + '"]');
      if (!row) return;
      var st = chainLegState(leg), chip = chipFor(st);
      var km = st && st.distanceKm != null && isFinite(st.distanceKm) ? st.distanceKm : legDistanceKm(leg);
      row.querySelector('.mc-pl-leg-km').textContent = fmtKm(km);
      var b = row.querySelector('[data-leg-open]');
      b.className = chipClass(chip);
      b.textContent = chip.text;
      b.title = chip.title;
      b.setAttribute('aria-label', leg.a.name + ' to ' + leg.b.name + ', ' + fmtKm(km) + '. ' + chip.title + '. Show terrain profile');
      b.setAttribute('aria-expanded', c.open === k && S.panel.owner === 'chain' ? 'true' : 'false');
    });
  }

  function drawChain() {
    drawChainPoints();
    drawChainLegs();
  }

  function drawChainPoints() {
    var c = S.chain;
    c.ptLayer.clearLayers();
    c.pts.forEach(function (p, i) {
      var name = pointName(p, i);
      var mk = L.marker([p.lat, p.lon], {
        icon: pinIcon(String(i + 1), p.node ? 'mc-pl-pin-snapped' : ''),
        draggable: true, keyboard: true, riseOnHover: true, zIndexOffset: 500, autoPan: true
      });
      mk.bindTooltip(esc(name), { direction: 'top', offset: [0, -14], className: 'mc-pl-tip' });
      mk.on('drag', function (e) { onChainDrag(i, e.target.getLatLng()); });
      mk.on('dragend', function (e) { moveChainPoint(i, e.target.getLatLng()); });
      mk.on('click', stopEvent);
      mk.addTo(c.ptLayer);
      labelMarker(mk, name + ', point ' + (i + 1) + ' of ' + c.pts.length);
    });
  }

  // While dragging, the touching legs follow the marker without a request.
  function onChainDrag(i, ll) {
    var c = S.chain;
    [i - 1, i].forEach(function (k) {
      var v = c.legViews[k], leg = c.legs[k];
      if (!v || !leg) return;
      var a = k === i ? ll : [leg.a.lat, leg.a.lon];
      var b = k === i ? [leg.b.lat, leg.b.lon] : ll;
      v.line.setLatLngs([a, b]);
      v.hit.setLatLngs([a, b]);
      if (v.warn) { c.legLayer.removeLayer(v.warn); v.warn = null; }
      var el = v.line.getElement && v.line.getElement();
      if (el) el.setAttribute('class', 'mc-pl-leg mc-pl-tone-muted mc-pl-leg-pending');
    });
  }

  function drawChainLegs() {
    var c = S.chain;
    c.legLayer.clearLayers();
    c.legViews = c.legs.map(function (leg, k) {
      var st = chainLegState(leg), chip = chipFor(st);
      var lls = [[leg.a.lat, leg.a.lon], [leg.b.lat, leg.b.lon]];
      var line = L.polyline(lls, {
        className: 'mc-pl-leg mc-pl-tone-' + chip.tone + (chip.loading ? ' mc-pl-leg-pending' : ''),
        weight: 5, opacity: 1, interactive: false
      }).addTo(c.legLayer);
      var hit = L.polyline(lls, { className: 'mc-pl-leg-hit', weight: 20, opacity: 1, bubblingMouseEvents: false }).addTo(c.legLayer);
      var tip = leg.a.name + ' → ' + leg.b.name + ': ' + chip.text;
      hit.bindTooltip(esc(tip), { sticky: true, className: 'mc-pl-tip' });
      hit.on('click', function (e) { stopEvent(e); openChainLeg(k, { focus: true }); });
      var warn = null;
      if (st && st.worstT != null && (chip.tone === 'warn' || chip.tone === 'bad')) {
        warn = obstructionMarker(leg, st, chip.tone, function () { openChainLeg(k, { focus: true }); }).addTo(c.legLayer);
      }
      return { line: line, hit: hit, warn: warn };
    });
  }

  function obstructionMarker(leg, an, tone, onClick) {
    var ll = window.MeshLOS.intermediatePoint(leg.a, leg.b, an.worstT);
    var text = an.obstructionM > 0
      ? 'Terrain ' + Math.max(1, Math.round(an.obstructionM)) + ' m above line of sight, ' + fmtKm(an.worstKm) + ' from ' + leg.a.name
      : (isClearanceWord(an.clearance * 100) ? fmtClearance(an.clearance * 100) : 'First Fresnel zone ' + fmtClearance(an.clearance * 100) + ' clear');
    var mk = L.marker([ll.lat, ll.lon], {
      icon: L.divIcon({
        className: 'mc-los-obstruction mc-los-tone-' + tone,
        html: icon('ph-warning'),
        iconSize: [28, 28],
        iconAnchor: [14, 14]
      }),
      keyboard: true, riseOnHover: true, zIndexOffset: 400
    });
    mk.bindTooltip(esc(text), { direction: 'top', offset: [0, -16], className: 'mc-pl-tip' });
    mk.on('click', function (e) { stopEvent(e); onClick(); });
    mk.on('add', function () { labelMarker(mk, text + '. Show terrain profile'); });
    return mk;
  }

  function chainProfileLeg(leg) {
    var r = leg.result;
    return {
      a: leg.a, b: leg.b, ha: leg.ha, hb: leg.hb,
      elevM: r && r.elevM ? r.elevM : null,
      distanceKm: r && r.distanceKm != null ? r.distanceKm : null,
      error: r && r.error ? r.error : (!r && S.chain.status && S.chain.status.kind === 'error' ? 'request' : null)
    };
  }

  function openChainLeg(k, opts) {
    opts = opts || {};
    var c = S.chain, leg = c.legs[k];
    if (!leg) return;
    c.open = k;
    openProfiles([chainProfileLeg(leg)], 'Line of sight', {
      owner: 'chain', fetchMissing: false, refresh: opts.refresh, focus: opts.focus, legKey: leg.key,
      nav: c.legs.length > 1 ? { pos: k, count: c.legs.length, step: function (d) { openChainLeg(k + d, { focus: false }); } } : null
    });
    updateChainLegRows();
  }

  // ─── Relay mode ─────────────────────────────────────────────────────────

  function restoreRelay(pr) {
    var r = S.relay, cfg = S.cfg;
    r.a = pr.a ? { lat: pr.a.lat, lon: pr.a.lon, node: null } : null;
    r.b = pr.b ? { lat: pr.b.lat, lon: pr.b.lon, node: null } : null;
    r.ha = pr.ha != null ? pr.ha : cfg.newAntennaM;
    r.hb = pr.hb != null ? pr.hb : cfg.newAntennaM;
    r.haAuto = pr.ha == null;
    r.hbAuto = pr.hb == null;
    r.hr = pr.hr != null ? pr.hr : cfg.newAntennaM;
    r.range = pr.range != null && cfg.rangesKm.indexOf(pr.range) >= 0 ? pr.range : cfg.defaultRangeKm;
    r.pick = !r.a ? 'a' : !r.b ? 'b' : null;
    r.pendingSel = pr.sel;
  }

  function relayQuery() {
    var r = S.relay;
    if (!r.a || !r.b) return null;
    return 'a=' + fmtCoord(r.a.lat) + ',' + fmtCoord(r.a.lon) + '&b=' + fmtCoord(r.b.lat) + ',' + fmtCoord(r.b.lon) +
      '&ha=' + fmtH(r.ha) + '&hb=' + fmtH(r.hb) + '&hr=' + fmtH(r.hr) + '&range=' + r.range;
  }

  function selId(sel) { return sel ? (sel.list === 'existing' ? 'e' : 'o') + (sel.idx + 1) : null; }

  function writeRelayUrl() {
    if (!S || S.mode !== 'relay') return;
    var r = S.relay;
    setParams({
      a: r.a ? fmtCoord(r.a.lat) + ',' + fmtCoord(r.a.lon) : null,
      b: r.b ? fmtCoord(r.b.lat) + ',' + fmtCoord(r.b.lon) : null,
      ha: fmtH(r.ha), hb: fmtH(r.hb), hr: fmtH(r.hr), range: String(r.range),
      sel: selId(r.sel || r.pendingSel)
    });
  }

  function setRelayEnd(which, pt) {
    var r = S.relay;
    var old = r[which];
    var hk = 'h' + which;
    // A new end takes its kind's default mast unless the height was set by hand.
    if (r[hk + 'Auto'] || r[hk] === defaultHeight(old ? old.node : null)) r[hk] = defaultHeight(pt.node);
    r[hk + 'Auto'] = false;
    r[which] = { lat: round(pt.lat), lon: round(pt.lon), node: pt.node || null };
    r.pick = which === 'a' && !r.b ? 'b' : which === 'b' && !r.a ? 'a' : null;
    syncRelayFields();
    renderRelayEnds();
    announce((which === 'a' ? 'A' : 'B') + ' set to ' + endLabel(which, r[which]));
    relayParamsChanged();
  }

  // Pressing an end's button picks it; pressing it again cancels (back to
  // the first unset end, if any).
  function togglePick(which) {
    var r = S.relay;
    r.pick = r.pick === which && r[which] ? (!r.a ? 'a' : !r.b ? 'b' : null) : which;
    renderRelayEnds();
    var btn = S.els.ends.querySelector('[data-pick="' + which + '"]');
    if (btn) btn.focus();
  }

  function clearRelay() {
    var r = S.relay;
    clearTimeout(r.timer);
    if (r.ctrl) { r.ctrl.abort(); r.ctrl = null; }
    r.a = r.b = null;
    r.result = null; r.resultKey = null; r.status = null; r.sel = null; r.pendingSel = null;
    r.pick = 'a';
    closePanel({ restoreFocus: false });
    writeRelayUrl();
    renderRelay();
    drawRelay();
    var input = S.root.querySelector('#mc-pl-find-relay');
    if (input) input.focus();
  }

  function syncRelayFields() {
    var r = S.relay;
    ['ha', 'hb', 'hr'].forEach(function (k) {
      var inp = S.root.querySelector('[data-relay-h="' + k + '"]');
      if (inp && document.activeElement !== inp) { inp.value = fmtH(r[k]); inp.removeAttribute('aria-invalid'); }
    });
    if (S.els.range) S.els.range.value = String(r.range);
  }

  function applyRelayHeight(input, commit) {
    var k = input.dataset.relayH;
    var v = parseHeight(input.value);
    if (v == null) {
      if (commit || input.getAttribute('aria-invalid') === 'true') {
        input.setAttribute('aria-invalid', 'true');
        S.els.relayErr.textContent = 'Antenna height must be ' + HEIGHT_MIN + '–' + HEIGHT_MAX + ' m.';
      }
      return;
    }
    input.removeAttribute('aria-invalid');
    if (!S.root.querySelector('[data-relay-h][aria-invalid="true"]')) S.els.relayErr.textContent = '';
    if (v === S.relay[k]) return;
    S.relay[k] = v;
    if (k === 'ha' || k === 'hb') S.relay[k + 'Auto'] = false;
    relayParamsChanged();
  }

  // Any input the server ranks on changed: the old result no longer applies.
  function relayParamsChanged() {
    var r = S.relay;
    if (relayQuery() !== r.resultKey) {
      r.result = null;
      r.resultKey = null;
      r.sel = null;
      if (S.panel.owner === 'relay') closePanel({ restoreFocus: false });
    }
    writeRelayUrl();
    renderRelayResults();
    drawRelay();
    scheduleRelayFetch();
  }

  function scheduleRelayFetch() {
    var r = S.relay;
    clearTimeout(r.timer);
    var key = relayQuery();
    if (!key) {
      if (r.ctrl) { r.ctrl.abort(); r.ctrl = null; }
      setRelayStatus(null);
      return;
    }
    if (r.result && r.resultKey === key) return;
    if (r.ctrl && r.ctrlKey === key) return;
    if (r.ctrl) { r.ctrl.abort(); r.ctrl = null; }
    setRelayStatus({ kind: 'pending' });
    r.timer = setTimeout(fetchRelay, DEBOUNCE_MS);
  }

  function fetchRelay() {
    var r = S.relay;
    clearTimeout(r.timer);
    if (r.ctrl) { r.ctrl.abort(); r.ctrl = null; }
    var key = relayQuery();
    if (!key) return;
    var ctrl = typeof AbortController === 'function' ? new AbortController() : null;
    r.ctrl = ctrl;
    r.ctrlKey = key;
    var mine = function () { return S && S.relay === r && r.ctrl === ctrl; };
    setRelayStatus({ kind: 'pending' });
    fetchJob('/api/planner/relay?' + key, {
      signal: ctrl ? ctrl.signal : null,
      onProgress: function (state, progress) {
        if (mine()) setRelayStatus({ kind: 'progress', state: state, progress: progress });
      }
    }).then(function (body) {
      if (!mine()) return;
      r.ctrl = null;
      r.result = normRelay(body);
      r.resultKey = key;
      if (body && body.source) S.source = String(body.source);
      setRelayStatus(null);
      var sel = r.pendingSel;
      r.pendingSel = null;
      r.sel = null;
      renderRelayResults();
      drawRelay();
      var n = r.result.existing.length + r.result.open.length;
      announce(n ? n + ' relay option' + (n === 1 ? '' : 's') + ' found' : 'No relay within range gives a usable link');
      if (sel && optionAt(sel)) selectRelayOption(selId(sel), { focus: false });
      else writeRelayUrl();
    }).catch(function (err) {
      if (isAbort(err) || !mine()) return;
      r.ctrl = null;
      setRelayStatus({ kind: 'error', err: err });
      renderRelayResults();
    });
  }

  function normOption(o, kind) {
    if (!o || typeof o !== 'object') return null;
    var lat = Number(o.lat), lon = Number(o.lon);
    if (!validLatLon(lat, lon)) return null;
    return {
      kind: kind, pubkey: o.pubkey ? String(o.pubkey) : '', name: o.name ? String(o.name) : '',
      lat: lat, lon: lon, elev_m: Number(o.elev_m), antenna_m: Number(o.antenna_m),
      leg_a: o.leg_a || {}, leg_b: o.leg_b || {}, bottleneck_pct: Number(o.bottleneck_pct)
    };
  }

  function normRelay(body) {
    body = body && typeof body === 'object' ? body : {};
    function list(arr, kind) {
      return (Array.isArray(arr) ? arr : []).map(function (o) { return normOption(o, kind); }).filter(Boolean);
    }
    return {
      direct: body.direct && typeof body.direct === 'object' ? body.direct : null,
      existing: list(body.existing, 'existing'),
      open: list(body.open, 'open'),
      warnings: (Array.isArray(body.warnings) ? body.warnings : []).map(String)
    };
  }

  function optionAt(sel) {
    var res = S.relay.result;
    if (!res || !sel) return null;
    var arr = res[sel.list];
    return arr && sel.idx >= 0 && sel.idx < arr.length ? arr[sel.idx] : null;
  }

  function parseSelId(id) {
    var m = /^([eo])(\d+)$/.exec(String(id || ''));
    return m ? { list: m[1] === 'e' ? 'existing' : 'open', idx: Number(m[2]) - 1 } : null;
  }

  function setRelayStatus(st) {
    var r = S.relay;
    r.status = st;
    var el = S.els.relayStatus;
    if (!st) { el.innerHTML = ''; r.liveState = ''; return; }
    if (st.kind === 'error') {
      el.innerHTML = errorHtml(jobErrorText(st.err), isRetryable(st.err) ? 'relay' : null);
      return;
    }
    var label = st.kind === 'progress'
      ? (st.state === 'computing' ? 'Searching for relay sites…' : JOB_STATES[st.state] || 'Working…')
      : 'Starting the relay search…';
    el.innerHTML = progressHtml(st.kind === 'progress' ? st.progress : null, label);
    if (r.liveState !== label) { r.liveState = label; announce(label); }
  }

  function renderRelay() {
    syncRelayFields();
    renderRelayEnds();
    setRelayStatus(S.relay.status);
    renderRelayResults();
  }

  function renderRelayEnds() {
    var r = S.relay;
    S.els.ends.innerHTML = endRowHtml('a', r.a, r.pick === 'a') + endRowHtml('b', r.b, r.pick === 'b');
    var hint = r.pick
      ? 'Click the map or a node to set ' + r.pick.toUpperCase() + '.'
      : 'Drag A or B on the map to move them, or use Change.';
    S.els.pickHint.textContent = hint;
    var form = S.els.relayFind;
    form.hidden = !r.pick;
    if (r.pick) {
      form.querySelector('[data-find-label]').textContent = 'Set ' + r.pick.toUpperCase() + ' by name or coordinates';
      form.querySelector('[data-find-btn]').textContent = 'Set ' + r.pick.toUpperCase();
    }
    S.root.classList.toggle('mc-pl-picking-map', !!r.pick);
    S.els.relayActions.innerHTML = r.a || r.b
      ? '<button type="button" class="mc-pl-btn mc-pl-btn-quiet" data-clear="relay">' + icon('ph-trash') + ' Clear A and B</button>'
      : '';
  }

  function renderRelayResults() {
    var r = S.relay, res = r.result, el = S.els.results;
    if (!r.a || !r.b) {
      el.innerHTML = '<div class="mc-pl-empty">' + icon('ph-signpost') +
        '<p><strong>Set A and B</strong> to search for a relay site between them.</p></div>';
      return;
    }
    if (!res) { el.innerHTML = ''; return; }
    var nameA = endLabel('a', r.a), nameB = endLabel('b', r.b);
    var html = res.direct ? directHtml(res.direct, nameA, nameB) : '';
    html += optionListHtml('existing', 'Existing nodes', 'Known repeaters and rooms that could relay.', res.existing);
    html += optionListHtml('open', 'New sites', 'Open terrain where a new repeater would work best.', res.open);
    el.innerHTML = html + warningsHtml(res.warnings);
  }

  // Server notes (e.g. a missing terrain tile read as sea level): visible, not alarming.
  function warningsHtml(list) {
    if (!list || !list.length) return '';
    return '<ul class="mc-pl-warnings">' + list.map(function (w) {
      return '<li>' + icon('ph-warning') + '<span>' + esc(w) + '</span></li>';
    }).join('') + '</ul>';
  }

  function optionListHtml(list, title, sub, opts) {
    var sel = S.relay.sel;
    var id = 'mc-pl-opts-' + list;
    return '<section class="mc-pl-opts-sec" aria-labelledby="' + id + '">' +
      '<div class="mc-pl-card-head"><h3 id="' + id + '">' + title + '</h3><span class="mc-pl-count">' + opts.length + '</span></div>' +
      '<p class="mc-pl-sub">' + sub + '</p>' +
      (opts.length
        ? '<ol class="mc-pl-opts">' + opts.map(function (o, i) {
          return optionRowHtml(o, list, i, !!sel && sel.list === list && sel.idx === i);
        }).join('') + '</ol>'
        : '<p class="mc-pl-none">No relay within range gives a usable link.</p>') +
      '</section>';
  }

  function relayEndPoint(which) {
    var e = S.relay[which];
    return { lat: e.lat, lon: e.lon, name: endLabel(which, e) };
  }

  function selectRelayOption(id, opts) {
    opts = opts || {};
    var r = S.relay, sel = parseSelId(id), o = optionAt(sel);
    if (!o) return;
    r.sel = sel;
    writeRelayUrl();
    Array.prototype.forEach.call(S.els.results.querySelectorAll('[data-opt]'), function (b) {
      var on = b.getAttribute('data-opt') === id;
      b.setAttribute('aria-pressed', on ? 'true' : 'false');
      b.classList.toggle('mc-pl-opt-on', on);
    });
    drawRelay();
    var R = { lat: o.lat, lon: o.lon, name: optionName(o, sel.idx) };
    var la = o.leg_a || {}, lb = o.leg_b || {};
    var hr = isFinite(o.antenna_m) ? o.antenna_m : r.hr;
    openProfiles([
      { a: relayEndPoint('a'), b: R, ha: r.ha, hb: hr, elevM: la.elev_m, distanceKm: la.distance_km },
      { a: R, b: relayEndPoint('b'), ha: hr, hb: r.hb, elevM: lb.elev_m, distanceKm: lb.distance_km }
    ], 'Relay via ' + R.name, { owner: 'relay', focus: opts.focus, opener: opts.focus ? S.els.results.querySelector('[data-opt="' + id + '"]') : null });
  }

  function openDirectProfile() {
    var r = S.relay, d = r.result && r.result.direct;
    if (!d) return;
    openProfiles([{ a: relayEndPoint('a'), b: relayEndPoint('b'), ha: r.ha, hb: r.hb, elevM: d.elev_m, distanceKm: d.distance_km }],
      'Direct link', { owner: 'relay', focus: true });
  }

  function drawRelay() {
    var r = S.relay, layer = S.layers.relay, res = r.result;
    layer.clearLayers();
    if (S.mode !== 'relay') return;
    if (r.a && r.b) {
      var dChip = res && res.direct ? chipFor(linkAnalysis(res.direct)) : { tone: 'muted' };
      L.polyline([[r.a.lat, r.a.lon], [r.b.lat, r.b.lon]], {
        className: 'mc-pl-leg mc-pl-direct mc-pl-tone-' + dChip.tone, weight: 3, opacity: 1, interactive: false
      }).addTo(layer);
    }
    var selO = optionAt(r.sel);
    if (selO) {
      [[r.a, selO, selO.leg_a], [selO, r.b, selO.leg_b]].forEach(function (seg) {
        var chip = chipFor(linkAnalysis(seg[2]));
        L.polyline([[seg[0].lat, seg[0].lon], [seg[1].lat, seg[1].lon]], {
          className: 'mc-pl-leg mc-pl-tone-' + chip.tone, weight: 5, opacity: 1, interactive: false
        }).addTo(layer);
      });
    }
    if (res) {
      ['existing', 'open'].forEach(function (list) {
        res[list].forEach(function (o, i) {
          var on = !!r.sel && r.sel.list === list && r.sel.idx === i;
          var name = optionName(o, i);
          var mk = L.marker([o.lat, o.lon], {
            icon: L.divIcon({
              className: 'mc-pl-cand-wrap',
              html: '<span class="mc-pl-cand mc-pl-cand-' + list + (on ? ' mc-pl-cand-on' : '') + '">' + (i + 1) + '</span>',
              iconSize: [24, 24], iconAnchor: [12, 12]
            }),
            keyboard: true, riseOnHover: true, zIndexOffset: on ? 450 : 300
          });
          mk.bindTooltip(esc(name + ' · ' + clearanceMeta(o.bottleneck_pct)), { direction: 'top', offset: [0, -12], className: 'mc-pl-tip' });
          mk.on('click', function (e) { stopEvent(e); selectRelayOption((list === 'existing' ? 'e' : 'o') + (i + 1), { focus: true }); });
          mk.addTo(layer);
          labelMarker(mk, (list === 'existing' ? 'Existing node ' : 'New site ') + (i + 1) + ': ' + name);
        });
      });
    }
    ['a', 'b'].forEach(function (w) {
      var end = r[w];
      if (!end) return;
      var label = endLabel(w, end);
      var mk = L.marker([end.lat, end.lon], {
        icon: pinIcon(w.toUpperCase(), 'mc-pl-pin-end' + (end.node ? ' mc-pl-pin-snapped' : '')),
        draggable: true, keyboard: true, riseOnHover: true, zIndexOffset: 600, autoPan: true
      });
      mk.bindTooltip(esc(w.toUpperCase() + ': ' + label), { direction: 'top', offset: [0, -14], className: 'mc-pl-tip' });
      mk.on('click', stopEvent);
      mk.on('dragend', function (e) {
        var ll = e.target.getLatLng(), snap = nearestNode(ll), wl = L.latLng(ll).wrap();
        var pick = r.pick;
        setRelayEnd(w, snap ? { lat: snap.lat, lon: snap.lon, node: snap } : { lat: wl.lat, lon: wl.lng, node: null });
        r.pick = pick === w ? null : pick;
        renderRelayEnds();
      });
      mk.addTo(layer);
      labelMarker(mk, w.toUpperCase() + ': ' + label);
    });
  }

  // ─── Area mode (planner-area.js) ────────────────────────────────────────

  function enterArea() {
    var panel = S.els.panels.area;
    if (!window.MeshPlannerArea || typeof window.MeshPlannerArea.mount !== 'function') {
      panel.innerHTML = '<p class="mc-pl-loading-note" role="status">Area planning is loading…</p>';
      return;
    }
    if (S.nodesState === 'loading' && S.nodesPromise) {
      // The module gets the node list we fetched; wait for it.
      panel.innerHTML = '<p class="mc-pl-loading-note" role="status">Loading known nodes…</p>';
      if (S.area.waiting) return;
      S.area.waiting = true;
      var gen = pageGen;
      S.nodesPromise.then(function () {
        if (gen !== pageGen || !S) return;
        S.area.waiting = false;
        if (S.mode === 'area' && !S.area.handle) enterArea();
      });
      return;
    }
    panel.innerHTML = '';
    var layer = L.layerGroup().addTo(S.map);
    var h = null;
    try {
      h = window.MeshPlannerArea.mount({
        map: S.map, layer: layer, panel: panel,
        cfg: S.rawCfg, params: params(), setParams: setParams,
        fetchJob: fetchJob, showProfiles: showProfiles, esc: esc,
        nodes: S.rawNodes, fmtClearance: fmtClearance
      });
    } catch (e) {
      console.error('[planner] area mount:', e);
      layer.remove();
      panel.innerHTML = errorHtml('Area planning couldn’t start. Reload the page to try again.', null);
      return;
    }
    S.area.handle = h || null;
    S.area.layer = layer;
  }

  function leaveArea() {
    if (!S) return;
    var a = S.area;
    if (a.handle && typeof a.handle.unmount === 'function') {
      try { a.handle.unmount(); } catch (e) { console.warn('[planner] area unmount:', e); }
    }
    if (a.layer) { try { a.layer.clearLayers(); a.layer.remove(); } catch (e) { /* map gone */ } }
    a.handle = null;
    a.layer = null;
    if (S.els && S.els.panels.area) S.els.panels.area.innerHTML = '';
  }

  // ─── Profile panel ──────────────────────────────────────────────────────

  function normProfilePoint(p, fallbackName) {
    var lat = Number(p && p.lat), lon = Number(p && p.lon);
    if (!p || !validLatLon(lat, lon)) return null;
    return { lat: lat, lon: lon, name: p.name ? String(p.name) : fallbackName };
  }

  function normProfileLeg(l) {
    if (!l || typeof l !== 'object') return null;
    var a = normProfilePoint(l.a, 'A'), b = normProfilePoint(l.b, 'B');
    if (!a || !b) return null;
    var ha = Number(l.ha), hb = Number(l.hb), d = Number(l.distanceKm);
    var leg = {
      a: a, b: b,
      ha: l.ha != null && isFinite(ha) ? ha : S.cfg.newAntennaM,
      hb: l.hb != null && isFinite(hb) ? hb : S.cfg.newAntennaM,
      elevM: finiteArray(l.elevM),
      distanceKm: l.distanceKm != null && isFinite(d) ? d : null,
      error: l.error ? String(l.error) : null,
      an: null
    };
    if (leg.distanceKm == null) leg.distanceKm = legDistanceKm(leg);
    leg.key = legKey(a, b);
    analyzeProfileLeg(leg);
    return leg;
  }

  function analyzeProfileLeg(leg) {
    leg.an = null;
    if (!leg.elevM) return;
    leg.an = analyzeLeg(leg, { elevM: leg.elevM, distanceKm: leg.distanceKm });
    if (!leg.an) leg.error = 'bad_data';
  }

  // legs: [{a:{lat,lon,name}, b, ha, hb, elevM?, distanceKm?}]; title is
  // the panel heading. Legs without terrain are fetched from
  // /api/planner/profile (one request per contiguous run of legs).
  function showProfiles(legs, title) {
    openProfiles(legs, title, { owner: 'external', fetchMissing: true, focus: true });
  }

  function openProfiles(legs, title, opts) {
    if (!S || !S.map) return;
    opts = opts || {};
    var P = S.panel;
    var norm = (Array.isArray(legs) ? legs : []).map(normProfileLeg).filter(Boolean);
    if (!norm.length) return;
    if (P.ctrl) { P.ctrl.abort(); P.ctrl = null; }
    var wasOpen = !!P.el;
    if (!opts.refresh || !wasOpen) P.opener = opts.opener || (document.activeElement !== document.body ? document.activeElement : null);
    P.legs = norm;
    P.title = String(title || 'Line of sight');
    P.owner = opts.owner || 'external';
    P.legKey = opts.legKey || null;
    P.nav = opts.nav || null;
    if (!opts.refresh) P.cursor = null;
    ensurePanel();
    renderPanel();
    drawActiveLegs();
    if (!opts.refresh) {
      collapseSheetForPanel();
      fitLegs(P.legs);
      if (opts.focus !== false) {
        try { P.el.focus({ preventScroll: true }); } catch (e) { P.el.focus(); }
      }
    } else if (P.cursor) {
      setCursor(P.cursor.k, P.cursor.t);
    }
    if (opts.fetchMissing !== false) fetchPanelMissing();
  }

  function ensurePanel() {
    var P = S.panel;
    if (P.el && P.el.parentNode === S.els.map) return;
    var el = document.createElement('div');
    el.className = 'mc-los-panel mc-pl-profile';
    el.setAttribute('role', 'dialog');
    el.tabIndex = -1;
    el.innerHTML =
      '<div class="mc-los-head">' +
        '<h2 class="mc-los-title"></h2>' +
        '<div class="mc-los-nav">' +
          '<button type="button" class="mc-los-btn mc-los-prev" aria-label="Previous leg">' + icon('ph-caret-left') + '</button>' +
          '<span class="mc-los-pos"></span>' +
          '<button type="button" class="mc-los-btn mc-los-next" aria-label="Next leg">' + icon('ph-caret-right') + '</button>' +
          '<button type="button" class="mc-los-btn mc-los-close" aria-label="Close terrain profile">' + icon('ph-x') + '</button>' +
        '</div>' +
      '</div>' +
      '<div class="mc-pl-profs"></div>' +
      '<p class="mc-los-foot"></p>';
    el.querySelector('.mc-los-prev').addEventListener('click', function () { stepPanel(-1); });
    el.querySelector('.mc-los-next').addEventListener('click', function () { stepPanel(1); });
    el.querySelector('.mc-los-close').addEventListener('click', function () { closePanel({ restoreFocus: true }); });
    el.addEventListener('pointermove', onPanelPointer);
    el.addEventListener('pointerdown', onPanelPointer);
    el.addEventListener('pointerout', onPanelPointerOut);
    if (L.DomEvent) {
      L.DomEvent.disableClickPropagation(el);
      L.DomEvent.disableScrollPropagation(el);
    }
    S.els.map.appendChild(el);
    S.els.map.classList.add('mc-los-open');
    P.el = el;
  }

  function renderPanel() {
    var P = S.panel, el = P.el;
    var title = el.querySelector('.mc-los-title');
    title.textContent = P.title;
    title.title = P.title;
    el.setAttribute('aria-label', P.title + ': terrain profile');
    var nav = el.querySelector('.mc-los-nav');
    nav.classList.toggle('mc-los-single', !P.nav);
    if (P.nav) {
      el.querySelector('.mc-los-prev').disabled = P.nav.pos <= 0;
      el.querySelector('.mc-los-next').disabled = P.nav.pos >= P.nav.count - 1;
      el.querySelector('.mc-los-pos').textContent = 'Leg ' + (P.nav.pos + 1) + ' of ' + P.nav.count;
    }
    el.classList.toggle('mc-pl-profile-multi', P.legs.length > 1);
    var profs = el.querySelector('.mc-pl-profs');
    profs.classList.toggle('mc-pl-profs-grid', P.legs.length > 1);
    profs.innerHTML = P.legs.map(profileSectionHtml).join('');
    el.querySelector('.mc-los-foot').textContent = FOOTER;
  }

  function stepPanel(d) {
    var P = S.panel;
    if (!P.nav) return;
    var next = P.nav.pos + d;
    if (next < 0 || next >= P.nav.count) return;
    P.nav.step(d);
    var active = document.activeElement;
    if (active && active.disabled && P.el) P.el.focus({ preventScroll: true });
  }

  function closePanel(opts) {
    opts = opts || {};
    if (!S) return;
    var P = S.panel;
    if (P.ctrl) { P.ctrl.abort(); P.ctrl = null; }
    if (P.rafId) { caf(P.rafId); P.rafId = 0; }
    var wasOpen = !!P.el;
    var shown = [];
    P.legs.forEach(function (l) { shown.push(l.a, l.b); });
    if (P.el && P.el.parentNode) P.el.parentNode.removeChild(P.el);
    if (S.els && S.els.map) S.els.map.classList.remove('mc-los-open');
    P.el = null;
    if (P.layer) P.layer.clearLayers();
    P.cursorMarker = null;
    P.cursor = null;
    P.ptr = null;
    var owner = P.owner;
    P.owner = null;
    P.legs = [];
    if (owner === 'chain') {
      S.chain.open = null;
      if (S.els.chainList) updateChainLegRows();
    }
    if (!wasOpen) return;
    if (S.sheetAuto) {
      // The sheet folded for the panel comes back and shrinks the map from
      // the bottom; bring what the panel showed back into view.
      S.sheetAuto = false;
      setSheet(true);
      if (shown.length && S.map) { S.map.invalidateSize({ pan: false }); fitPoints(shown, false); }
    }
    var opener = P.opener;
    P.opener = null;
    if (opts.restoreFocus && opener && opener.isConnected && typeof opener.focus === 'function') {
      try { opener.focus({ preventScroll: true }); } catch (e) { opener.focus(); }
    }
  }

  function drawActiveLegs() {
    var P = S.panel;
    P.layer.clearLayers();
    P.cursorMarker = null;
    P.legs.forEach(function (leg) {
      L.polyline([[leg.a.lat, leg.a.lon], [leg.b.lat, leg.b.lon]], {
        className: 'mc-los-leg-active', weight: 4, opacity: 1, dashArray: '10 8', interactive: false
      }).addTo(P.layer);
    });
  }

  // Fit the legs into the map area the panel leaves free.
  function fitLegs(legs) {
    var map = S.map, P = S.panel;
    if (!map || !legs.length || !P.el) return;
    try {
      map.invalidateSize({ pan: false });
      var pts = [];
      legs.forEach(function (l) { pts.push([l.a.lat, l.a.lon], [l.b.lat, l.b.lon]); });
      var size = map.getSize();
      var ph = P.el.offsetHeight || 0;
      var pad = Math.max(0, Math.min(ph + 24, size.y - 120));
      var o = isMobile()
        ? { paddingTopLeft: [24, pad + 16], paddingBottomRight: [24, 24] }
        : { paddingTopLeft: [48, 48], paddingBottomRight: [48, pad + 40] };
      o.maxZoom = 14;
      o.animate = !reducedMotion();
      map.fitBounds(L.latLngBounds(pts), o);
    } catch (e) { console.warn('[planner] fit:', e); }
  }

  // Legs the caller gave no terrain for: cached ones apply at once, the rest
  // go to /api/planner/profile, one request per contiguous run.
  function fetchPanelMissing() {
    var P = S.panel;
    var need = [], hits = 0;
    P.legs.forEach(function (leg, k) {
      if (leg.elevM || leg.error) return;
      var hit = cacheGet(leg.key);
      if (hit) { applyPanelResult(k, hit); hits++; return; }
      need.push(k);
    });
    if (hits) renderPanelSections();
    if (!need.length) return;
    var runs = [], cur = null;
    need.forEach(function (k) {
      var leg = P.legs[k];
      var joins = cur && k === cur.ks[cur.ks.length - 1] + 1 && coordKey(P.legs[k - 1].b) === coordKey(leg.a) && cur.pts.length < S.cfg.maxPoints;
      if (!joins) { cur = { ks: [], pts: [leg.a] }; runs.push(cur); }
      cur.ks.push(k);
      cur.pts.push(leg.b);
    });
    var ctrl = typeof AbortController === 'function' ? new AbortController() : null;
    P.ctrl = ctrl;
    var legsRef = P.legs;
    runs.forEach(function (run) {
      fetchJob('/api/planner/profile?pts=' + serializePts(run.pts), { signal: ctrl ? ctrl.signal : null })
        .then(function (body) {
          if (!S || S.panel.legs !== legsRef) return;
          var arr = body && Array.isArray(body.legs) && body.legs.length === run.ks.length ? body.legs : null;
          if (body && body.source) S.source = String(body.source);
          run.ks.forEach(function (k, j) {
            var res = arr ? legResultFrom(arr[j]) : { error: 'request' };
            if (isCacheable(res)) cachePut(P.legs[k].key, res);
            applyPanelResult(k, res);
          });
          renderPanelSections();
        })
        .catch(function (err) {
          if (isAbort(err) || !S || S.panel.legs !== legsRef) return;
          run.ks.forEach(function (k) { applyPanelResult(k, { error: 'request' }); });
          renderPanelSections();
        });
    });
  }

  function applyPanelResult(k, res) {
    var leg = S.panel.legs[k];
    if (!leg) return;
    if (res.elevM) {
      leg.elevM = res.elevM;
      if (res.distanceKm != null) leg.distanceKm = res.distanceKm;
      analyzeProfileLeg(leg);
    } else {
      leg.error = res.error || 'request';
    }
  }

  function renderPanelSections() {
    var P = S.panel;
    if (!P.el) return;
    P.el.querySelector('.mc-pl-profs').innerHTML = P.legs.map(profileSectionHtml).join('');
    if (P.cursor) setCursor(P.cursor.k, P.cursor.t);
  }

  // ─── Profile ↔ map crosshair ────────────────────────────────────────────

  function onPanelPointer(e) {
    var hit = e.target && e.target.closest ? e.target.closest('.mc-los-hit') : null;
    if (!hit) return;
    var sec = hit.closest('[data-prof]');
    if (!sec) return;
    var P = S.panel;
    P.ptr = { k: Number(sec.getAttribute('data-prof')), x: e.clientX };
    if (!P.rafId) P.rafId = raf(drawCursorFromPointer);
  }

  function onPanelPointerOut(e) {
    // A touch ends with pointerout; keep the last reading on screen.
    if (e.pointerType === 'touch') return;
    var to = e.relatedTarget;
    if (to && to.closest && to.closest('.mc-los-hit')) return;
    if (!e.target || !e.target.closest || !e.target.closest('.mc-los-hit')) return;
    hideCursor();
  }

  function drawCursorFromPointer() {
    var P = S && S.panel;
    if (!P) return;
    P.rafId = 0;
    if (!P.ptr || !P.el) return;
    var svg = P.el.querySelector('[data-prof="' + P.ptr.k + '"] .mc-los-svg');
    if (!svg) return;
    var rect = svg.getBoundingClientRect();
    if (!rect.width) return;
    var x = (P.ptr.x - rect.left) / rect.width * window.MeshLOS.PLOT.W;
    setCursor(P.ptr.k, window.MeshLOS.tAtViewX(x));
  }

  // Snap to the nearest sample so the readout matches the plotted point.
  function setCursor(k, t) {
    var P = S.panel, leg = P.legs[k], an = leg && leg.an;
    if (!an || !P.el) return;
    var i = Math.round(t * (an.n - 1));
    var ts = i / (an.n - 1);
    P.cursor = { k: k, t: ts };
    Array.prototype.forEach.call(P.el.querySelectorAll('[data-prof]'), function (sec) {
      var mine = Number(sec.getAttribute('data-prof')) === k;
      var cur = sec.querySelector('.mc-los-cursor');
      var ro = sec.querySelector('.mc-los-readout');
      if (!mine) {
        if (cur) cur.setAttribute('visibility', 'hidden');
        if (ro) ro.textContent = READOUT_HINT;
        return;
      }
      if (cur) {
        var vx = String(Math.round(window.MeshLOS.viewXAtT(ts) * 100) / 100);
        cur.setAttribute('x1', vx);
        cur.setAttribute('x2', vx);
        cur.setAttribute('visibility', 'visible');
      }
      if (ro) ro.innerHTML = readoutHtml(leg, an, i);
    });
    var ll = window.MeshLOS.intermediatePoint(leg.a, leg.b, ts);
    if (!P.cursorMarker) {
      P.cursorMarker = L.marker([ll.lat, ll.lon], {
        icon: L.divIcon({ className: 'mc-los-cursor-marker', html: '', iconSize: [16, 16], iconAnchor: [8, 8] }),
        interactive: false, keyboard: false, zIndexOffset: 1000
      }).addTo(P.layer);
    } else {
      P.cursorMarker.setLatLng([ll.lat, ll.lon]);
    }
  }

  function hideCursor() {
    var P = S && S.panel;
    if (!P) return;
    P.cursor = null;
    P.ptr = null;
    if (P.rafId) { caf(P.rafId); P.rafId = 0; }
    if (P.el) {
      Array.prototype.forEach.call(P.el.querySelectorAll('.mc-los-cursor'), function (c) { c.setAttribute('visibility', 'hidden'); });
      Array.prototype.forEach.call(P.el.querySelectorAll('.mc-los-readout'), function (r) { r.textContent = READOUT_HINT; });
    }
    if (P.cursorMarker) { P.layer.removeLayer(P.cursorMarker); P.cursorMarker = null; }
  }

  // ─── Mobile bottom sheet ────────────────────────────────────────────────

  function setSheet(open) {
    var side = S.els.side, btn = S.els.sheetBtn;
    side.classList.toggle('mc-pl-collapsed', !open);
    btn.setAttribute('aria-expanded', open ? 'true' : 'false');
    btn.setAttribute('aria-label', open ? 'Hide planner panel' : 'Show planner panel');
  }

  function toggleSheet() {
    S.sheetAuto = false;
    setSheet(S.els.side.classList.contains('mc-pl-collapsed'));
  }

  // On a phone the open sheet leaves the map a strip; fold it while a profile shows.
  function collapseSheetForPanel() {
    if (!isMobile() || S.els.side.classList.contains('mc-pl-collapsed')) return;
    setSheet(false);
    S.sheetAuto = true;
  }

  // ─── Export ─────────────────────────────────────────────────────────────

  window.MeshPlanner = {
    fetchJob: fetchJob,
    showProfiles: showProfiles,
    esc: esc,
    fmtKm: fmtKm,
    fmtPct: fmtPct,
    fmtClearance: fmtClearance,
    params: params,
    setParams: setParams,
    _internals: {
      parsePts: parsePts,
      serializePts: serializePts,
      parseParams: parseParams,
      legsFromPoints: legsFromPoints,
      chipFor: chipFor,
      parsePoint: parsePoint,
      parseHeight: parseHeight,
      queryOf: queryOf,
      buildQuery: buildQuery,
      linkAnalysis: linkAnalysis,
      legResultFrom: legResultFrom,
      resolveQuery: resolveQuery,
      normNodes: normNodes,
      normCfg: normCfg,
      parseRetryAfter: parseRetryAfter,
      jobErrorText: jobErrorText,
      pointRowHtml: pointRowHtml,
      legRowHtml: legRowHtml,
      endRowHtml: endRowHtml,
      optionRowHtml: optionRowHtml,
      directHtml: directHtml,
      warningsHtml: warningsHtml,
      errorHtml: errorHtml,
      progressHtml: progressHtml,
      profileSectionHtml: profileSectionHtml,
      readoutHtml: readoutHtml,
      nodeTooltipHtml: nodeTooltipHtml,
      roleClass: roleClass,
      state: function () { return S; }
    }
  };
  if (typeof registerPage === 'function') registerPage('planner', { init: init, destroy: destroy });
})();
