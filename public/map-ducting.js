/* map-ducting.js — tropospheric ducting map overlay.
 *
 * Draws the forecast refractivity gradient as a semi-transparent field of
 * colour bands (like the usual tropo-ducting forecast maps): transparent where
 * propagation is normal, then purple, blue, cyan, green, yellow, orange, red
 * and pink as the gradient gets stronger; each colour is one range of dN/dh. Cells where a duct can carry LoRa
 * (868/915 MHz) get a dashed outline. Data: GET /api/ducting (hourly
 * forecast, one array entry per `times` entry, see cmd/server/ducting_types.go).
 *
 *   window.MC_createDuctingLayer(opts) -> L.Layer
 *       Fetches /api/ducting when added to a map, refreshes every 30 min while
 *       on it (every minute until it has data), draws the field as one canvas
 *       image overlay, one invisible clickable rectangle per cell with data
 *       (popups, LoRa outline), a legend and a forecast-time slider.
 *       A layer removed mid-fetch never touches the map afterwards.
 *   window.MC_ductTimeIndex(times, now) -> index of the latest time <= now
 *   window.MC_ductFieldColor(g, stops)  -> [r, g, b, a] (band colour) for a dN/dh value
 *   window.MC_ductFieldStops()          -> stops read from the CSS variables
 *   window.MC_ductGrid(cells, step)     -> the cells' lattice (see ductGrid)
 *   window.MC_ductGridValues(grid, cells, i) -> Float32Array, NaN = no data
 *   window.MC_ductSample(grid, vals, lat, lon) -> { g, a }
 *   window.MC_ductFieldValues(grid, vals) -> { g, a } Float32Arrays, one entry
 *       per image pixel: what the field image is coloured from
 *   window.MC_ductRowLat(y, h, north, south)   -> latitude of image row y
 *
 * The field image:
 *   - The cells sit on a regular lattice: centre = floor(x/step)*step + step/2.
 *     The image covers the bounding box of every cell that has data at some
 *     hour (the cells' outer edges), so its bounds stay put while the slider
 *     moves. Several separate regions share one image; its long side is
 *     capped at MAX_SIDE px, so far-apart regions get fewer pixels per cell
 *     (the browser smooths the upscaling). PX_PER_CELL is high so the band
 *     edges stay smooth curves at the zoom a region is usually viewed at. Cells on both sides of the
 *     antimeridian are drawn where Leaflet puts them (lon -180..180); the
 *     image then spans the whole width, nothing is wrapped.
 *   - Rows are sampled in Web Mercator (row -> latitude by the inverse
 *     projection), so the field lines up with the basemap at every zoom.
 *   - The value is the cell's min dN/dh (g), interpolated between cell
 *     centres with Catmull-Rom bicubic over the 4x4 neighbourhood (lattice
 *     indices clamped at its border), so band edges are smooth curves rather
 *     than the straight segments bilinear gives. Next to a cell without data
 *     (any of the 16 missing) it falls back to bilinear over the cells that
 *     have data. Where a neighbour has no data the field fades to
 *     transparent by the cell's edge (coverage, bilinear), so there are no
 *     hard square edges. At the outer edge of the forecast area (the lattice
 *     slots that have data at some hour) the fade is twice as wide: it starts
 *     a full cell inside the edge (see areaSoft).
 *   - The area's outer boundary is drawn as thin dotted lines (ductEdges),
 *     one segment per run of cell edges, so users see where the forecast ends.
 *   - Colour: the flat colour of the band g falls in (no blending between
 *     stops), every band at the same field opacity, times the coverage.
 *   - Built only when the data, the forecast hour or the theme changes,
 *     O(image pixels): the interpolation is separable (per image column the
 *     lattice taps and weights are computed once per grid, per image row the
 *     vertical sums once per lattice column), so a pixel costs a handful of
 *     multiply-adds. Pan and zoom only move/scale the image.
 *
 * Colours come from the --mc-duct-s1..s8 CSS variables (hex or rgb()), the
 * field opacity from --mc-duct-alpha and the LoRa outline from
 * --mc-duct-lora and the dotted edge line from --mc-duct-edge, all read at
 * draw time.
 *   window.MC_ductEdges(grid)           -> [[[lat, lon], [lat, lon]], ...]
 */
(function () {
  'use strict';

  var API_URL = '/api/ducting';
  var REFRESH_MS = 30 * 60 * 1000;
  // While there is no data yet (server still preparing, or a failed fetch),
  // ask again every minute instead of waiting a full refresh period.
  var RETRY_MS = 60 * 1000;
  var LORA_MAX_MHZ = 1000;   // duct with fmin <= 1 GHz carries 868/915 MHz
  var BAND_868 = 868;
  var BAND_915 = 915;
  var TYPE_NAME = { 0: 'No data', 1: 'Normal', 2: 'Super-refraction', 3: 'Elevated duct', 4: 'Surface-based duct' };

  // Colour bands: --mc-duct-sN colours dN/dh (N/km) from STOP_G[N-1] down to
  // (not including) the next stop; the last one everything below it. Above
  // the first (-79, where super-refraction starts) is transparent; -157 is a
  // trapping layer (duct). Whole numbers: the colour table relies on it.
  var STOP_G = [-79, -100, -130, -157, -220, -300, -400, -600];
  var DEFAULT_ALPHA = 0.8;      // when --mc-duct-alpha is unset
  // Colour table: entry n is the colour of g = -n, and of every g in
  // (-n - 1, -n] (same band, the stops being whole numbers); the last entry
  // covers everything stronger.
  var LUT_SIZE = 1 - STOP_G[STOP_G.length - 1];
  var PX_PER_CELL = 48;
  var MAX_SIDE = 2048;          // image long side, px
  var MAX_GRID = 1 << 22;       // lattice slots (0.25 deg worldwide is ~1M)
  var MAX_LAT = 85.0511287798;  // Web Mercator limit
  var FIELD_PANE = 'mcDuctFieldPane';
  var FIELD_Z = '245';          // above tiles (200), below the tile overlays (250) and vectors (400)
  var LORA_VAR = '--mc-duct-lora';
  var ALPHA_VAR = '--mc-duct-alpha';
  var EDGE_VAR = '--mc-duct-edge';
  var EDGE_STYLE = { stroke: true, weight: 1, opacity: 0.7, dashArray: '1 5', interactive: false };
  var LORA_STYLE = { stroke: true, weight: 1.5, opacity: 0.95, dashArray: '4 3' };

  // Legend: where each band starts (0..1 along the bar; the last runs to the
  // end, transparent before the first). Spread out instead of linear in g,
  // so the labels have room. "Strong" sits right-aligned at the end.
  var BAR_POS = [0.19, 0.29, 0.44, 0.58, 0.69, 0.78, 0.86, 0.93];
  var BAR_LABELS = [[0, 'Normal'], [0.19, 'Super-refraction'], [0.58, 'Ducting'], [1, 'Strong', true]];

  var NOTE = 'Indicator only: model levels are ~200 m apart, so thinner ducts are not visible.';
  var MSG_PENDING = 'Ducting forecast is being prepared';
  var MSG_ERROR = 'Ducting forecast unavailable';

  // ── Pure helpers ──────────────────────────────────────────────────────────

  // Latest time <= now; the first when every time is in the future; -1 when
  // there are no times.
  function ductTimeIndex(times, nowSec) {
    if (!Array.isArray(times) || !times.length) return -1;
    var best = 0;
    for (var i = 0; i < times.length; i++) {
      if (times[i] <= nowSec) best = i; else break;
    }
    return best;
  }

  function fmtTime(sec) {
    try {
      return new Date(sec * 1000).toLocaleString([], { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    } catch (_) { return String(sec); }
  }

  function at(arr, i) { return (arr && arr[i] != null) ? Number(arr[i]) : 0; }

  function isLora(cell, i) {
    var f = at(cell.f, i);
    return at(cell.c, i) >= 3 && f > 0 && f <= LORA_MAX_MHZ;
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  // Popup body for cell `cell` at time index `i` (DOM nodes, textContent only).
  function popupContent(cell, i) {
    var cls = at(cell.c, i);
    var box = el('div', 'mc-duct-popup');
    box.appendChild(el('div', 'mc-duct-popup-title', TYPE_NAME[cls] || TYPE_NAME[0]));
    if (cls < 2) return box;
    var fmin = at(cell.f, i);
    var rows = [];
    if (cls >= 3) {
      rows.push('Base: ' + Math.round(at(cell.b, i)) + ' m above ground');
      rows.push('Top: ' + Math.round(at(cell.t, i)) + ' m above ground');
    }
    rows.push('Strength: ' + (at(cell.dm, i) / 10).toFixed(1) + ' M-units');
    rows.push('Lowest trapped frequency: ' + (fmin > 0 ? Math.round(fmin) + ' MHz' : 'none'));
    rows.push('Can carry 868 MHz: ' + (fmin > 0 && fmin <= BAND_868 ? 'yes' : 'no'));
    rows.push('Can carry 915 MHz: ' + (fmin > 0 && fmin <= BAND_915 ? 'yes' : 'no'));
    for (var r = 0; r < rows.length; r++) box.appendChild(el('div', null, rows[r]));
    return box;
  }

  // '' when the variable is undefined.
  function cssVar(name) {
    try {
      var v = getComputedStyle(document.documentElement).getPropertyValue(name);
      return v ? String(v).trim() : '';
    } catch (_) { return ''; }
  }

  // '#rgb', '#rrggbb', '#rrggbbaa', 'rgb(r, g, b)' or 'rgba(...)' -> [r, g, b]
  // (alpha ignored: the field has one opacity); null for anything else.
  function parseColor(s) {
    s = String(s || '').trim().toLowerCase();
    var m = /^#([0-9a-f]{3}|[0-9a-f]{6}|[0-9a-f]{8})$/.exec(s);
    if (m) {
      var h = m[1];
      if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
      return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
    }
    m = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/.exec(s);
    if (m) return [Math.round(+m[1]), Math.round(+m[2]), Math.round(+m[3])];
    return null;
  }

  // The colour stops from the CSS variables: [{ g, rgb }], strongest last.
  // A missing or unreadable variable drops its stop (nothing is invented).
  function fieldStops() {
    var out = [];
    for (var k = 0; k < STOP_G.length; k++) {
      var rgb = parseColor(cssVar('--mc-duct-s' + (k + 1)));
      if (rgb) out.push({ g: STOP_G[k], rgb: rgb });
    }
    return out;
  }

  function fieldAlpha() {
    var a = parseFloat(cssVar(ALPHA_VAR));
    return (isFinite(a) && a >= 0 && a <= 1) ? a : DEFAULT_ALPHA;
  }

  // Colour of dN/dh value g: [r, g, b, a], the flat colour of its band
  // (stops[k].g >= g > stops[k + 1].g -> stops[k]; at or below the last stop
  // -> the last), alpha 1; transparent above the first stop, for NaN or
  // without stops. No blending between stops, no opacity ramp.
  function fieldColor(g, stops) {
    if (!stops || !stops.length || !(g <= stops[0].g)) return [0, 0, 0, 0];
    for (var k = stops.length - 1; k > 0; k--) if (g <= stops[k].g) break;
    var c = stops[k].rgb;
    return [c[0], c[1], c[2], 1];
  }

  // RGBA table: entry n is the colour of g = -n, alpha scaled by `alpha`.
  function fieldLut(stops, alpha) {
    var lut = new Uint8ClampedArray(LUT_SIZE * 4);
    for (var n = 0; n < LUT_SIZE; n++) {
      var c = fieldColor(-n, stops);
      lut[n * 4] = c[0]; lut[n * 4 + 1] = c[1]; lut[n * 4 + 2] = c[2];
      lut[n * 4 + 3] = Math.round(c[3] * alpha * 255);
    }
    return lut;
  }

  function pct(f) { return Math.round(f * 1000) / 10 + '%'; }

  function mercY(lat) { return Math.log(Math.tan(Math.PI / 4 + lat * Math.PI / 360)); }

  // Latitude at y px from the top of an image h px tall spanning north..south
  // in Web Mercator (y may be fractional; 0 = north edge, h = south edge).
  function rowLat(y, h, north, south) {
    var yn = mercY(north), ys = mercY(south);
    var m = yn + (ys - yn) * (y / h);
    return (2 * Math.atan(Math.exp(m)) - Math.PI / 2) * 180 / Math.PI;
  }

  // Lattice of the cells that have data (class >= 1) at some hour:
  // { step, i0, j0, rows, cols, slot (cell index -> lattice slot, -1 = none),
  //   south, west, north, east (outer cell edges), w, h (image px) }, or null.
  function ductGrid(cells, step) {
    step = Number(step);
    if (!(step > 0) || !Array.isArray(cells)) return null;
    var slot = new Int32Array(cells.length).fill(-1);
    var ii = new Int32Array(cells.length), jj = new Int32Array(cells.length);
    var i0 = Infinity, i1 = -Infinity, j0 = Infinity, j1 = -Infinity, n = 0;
    for (var k = 0; k < cells.length; k++) {
      var cell = cells[k];
      if (!cell || !Array.isArray(cell.c) || !isFinite(cell.lat) || !isFinite(cell.lon)) continue;
      var any = false;
      for (var t = 0; t < cell.c.length; t++) if (cell.c[t] >= 1) { any = true; break; }
      if (!any) continue;
      var i = Math.round(cell.lat / step - 0.5), j = Math.round(cell.lon / step - 0.5);
      ii[k] = i; jj[k] = j; slot[k] = 0; n++;
      if (i < i0) i0 = i; if (i > i1) i1 = i;
      if (j < j0) j0 = j; if (j > j1) j1 = j;
    }
    if (!n) return null;
    var rows = i1 - i0 + 1, cols = j1 - j0 + 1;
    if (rows * cols > MAX_GRID) return null;
    var present = new Uint8Array(rows * cols);
    for (k = 0; k < cells.length; k++) {
      if (slot[k] !== 0) continue;
      slot[k] = (ii[k] - i0) * cols + (jj[k] - j0);
      present[slot[k]] = 1;
    }
    var g = { step: step, i0: i0, j0: j0, rows: rows, cols: cols, slot: slot, soft: areaSoft(present, rows, cols),
      south: Math.max(-MAX_LAT, i0 * step), north: Math.min(MAX_LAT, (i1 + 1) * step),
      west: j0 * step, east: (j1 + 1) * step };
    if (!(g.north > g.south)) return null;
    // Image size: PX_PER_CELL per cell across, the Mercator aspect down,
    // long side capped.
    var w = cols * PX_PER_CELL;
    var h = w * ((mercY(g.north) - mercY(g.south)) * 180 / Math.PI) / (g.east - g.west);
    var scale = Math.min(1, MAX_SIDE / Math.max(w, h));
    g.w = Math.max(1, Math.round(w * scale));
    g.h = Math.max(1, Math.round(h * scale));
    return g;
  }

  // Per lattice slot: 0 outside the forecast area, 1 on its outer edge (some
  // of the 8 neighbours is outside, or off the lattice), 2 inside. The
  // coverage (rowSums/evalAt) uses it to widen the fade at the outer edge only.
  function areaSoft(present, rows, cols) {
    var out = new Uint8Array(rows * cols);
    for (var r = 0; r < rows; r++) {
      for (var c = 0; c < cols; c++) {
        var s = r * cols + c;
        if (!present[s]) continue;
        var inner = r > 0 && r < rows - 1 && c > 0 && c < cols - 1;
        if (inner) {
          for (var d = -1; d <= 1 && inner; d++) {
            var o = s + d * cols;
            if (!present[o - 1] || !present[o] || !present[o + 1]) inner = false;
          }
        }
        out[s] = inner ? 2 : 1;
      }
    }
    return out;
  }

  // The outer boundary of the forecast area: the cell edges whose neighbour
  // cell is outside the area, merged into runs along each lattice row and
  // column: [[[lat, lon], [lat, lon]], ...]. A filled rectangle is 4 segments.
  // Across the antimeridian a cell at lon 180 and one at -180 are neighbours,
  // so there is no edge at the seam when both exist. O(lattice slots).
  function ductEdges(grid) {
    var out = [];
    if (!grid || !grid.soft) return out;
    var soft = grid.soft, rows = grid.rows, cols = grid.cols, step = grid.step;
    var clampLat = function (lat) { return Math.max(-MAX_LAT, Math.min(MAX_LAT, lat)); };
    var r, c, start, lat, lon;
    for (r = 0; r <= rows; r++) {
      lat = clampLat((grid.i0 + r) * step);
      start = -1;
      for (c = 0; c <= cols; c++) {
        var edge = c < cols && ((r > 0 && soft[(r - 1) * cols + c] > 0) !== (r < rows && soft[r * cols + c] > 0));
        if (edge && start < 0) start = c;
        if (!edge && start >= 0) {
          out.push([[lat, (grid.j0 + start) * step], [lat, (grid.j0 + c) * step]]);
          start = -1;
        }
      }
    }
    var seamCol = function (lon, east) {   // lattice column across the antimeridian, or -1
      var j = Math.round(east ? -180 / step : 180 / step - 1) - grid.j0;
      return (Math.abs(lon - (east ? 180 : -180)) < 1e-9 && j >= 0 && j < cols) ? j : -1;
    };
    for (c = 0; c <= cols; c++) {
      lon = (grid.j0 + c) * step;
      var mirror = c === cols ? seamCol(lon, true) : c === 0 ? seamCol(lon, false) : -1;
      start = -1;
      for (r = 0; r <= rows; r++) {
        var has = function (col) { return r < rows && col >= 0 && col < cols && soft[r * cols + col] > 0; };
        var left = c > 0 ? has(c - 1) : has(mirror);
        var right = c < cols ? has(c) : has(mirror);
        var vedge = r < rows && left !== right;
        if (vedge && start < 0) start = r;
        if (!vedge && start >= 0) {
          out.push([[clampLat((grid.i0 + start) * step), lon], [clampLat((grid.i0 + r) * step), lon]]);
          start = -1;
        }
      }
    }
    return out;
  }

  // g at time index i per lattice slot; NaN where there is no data (class 0).
  function ductGridValues(grid, cells, i) {
    var vals = new Float32Array(grid.rows * grid.cols).fill(NaN);
    for (var k = 0; k < cells.length; k++) {
      var s = grid.slot[k];
      if (s < 0 || at(cells[k].c, i) < 1) continue;
      var g = at(cells[k].g, i);
      vals[s] = isFinite(g) ? g : 0;
    }
    return vals;
  }

  // ── Interpolation ──
  // At lattice row v / column u (fractional, 0 = first centre):
  //   g: Catmull-Rom bicubic over the 4x4 slots around (v, u), indices
  //      clamped to the lattice; bilinear over the slots with data when any
  //      of the 16 has none (NaN propagates through the sums, which is how
  //      that is detected).
  //   a: coverage, from the bilinear weights of the slots with data: 1 where
  //      all four have data, falling to 0 by the edge of a cell whose
  //      neighbour has none; with `soft` (areaSoft) also scaled down towards
  //      the outer edge of the forecast area, so the fade there spans a full
  //      cell (1 at the second cell in, 0 at the edge).
  // Separable: colTaps holds the horizontal taps of a set of columns,
  // rowSums the vertical sums of one row per lattice column, rowEval combines
  // them. The renderer and sample() share all three, so they agree exactly.

  // Catmull-Rom weights of the 4 taps at t (0..1) into w[o..o+3]; t = 0 is
  // exactly [0, 1, 0, 0], so the field equals the data at the cell centres.
  function crWeights(t, w, o) {
    var t2 = t * t, t3 = t2 * t;
    w[o] = 0.5 * (2 * t2 - t3 - t);
    w[o + 1] = 0.5 * (3 * t3 - 5 * t2 + 2);
    w[o + 2] = 0.5 * (4 * t2 - 3 * t3 + t);
    w[o + 3] = 0.5 * (t3 - t2);
  }

  // Horizontal taps for lattice columns us[x]: bj = the interval between
  // lattice columns bj - 1 and bj (index into rowSums' padded arrays; -1 =
  // off the lattice), fu = the fraction across it.
  function colTaps(cols, us) {
    var n = us.length;
    var T = { bj: new Int32Array(n), fu: new Float64Array(n) };
    for (var x = 0; x < n; x++) {
      var u = us[x], j = Math.floor(u);
      T.bj[x] = (j >= -1 && j < cols) ? j + 1 : -1;
      T.fu[x] = u - j;
    }
    return T;
  }

  // Reusable per-row buffers for a lattice `cols` wide. P/S/Q are padded by
  // one column on each side (zeros), so bilinear taps never need a check.
  function rowBufs(cols) {
    return { ok: false, P: new Float64Array(cols + 2), S: new Float64Array(cols + 2), Q: new Float64Array(cols + 2),
      cub: new Float64Array(cols), C: new Float64Array((cols + 1) * 4), wv: new Float64Array(4) };
  }

  // Vertical sums of lattice row v into R, per lattice column c:
  //   P/S/Q[c + 1] = bilinear weight, weighted value and weighted soft of the
  //   two rows around v that have data; cub[c] = bicubic sum of the 4 rows
  //   (NaN when any has no data). Then, per interval b between columns b - 1
  //   and b, the horizontal Catmull-Rom polynomial through cub[b - 2..b + 1]
  //   (clamped): C[4b..4b+3] = c0..c3, g(t) = ((c3 t + c2) t + c1) t + c0.
  //   c0 is the column's own value, so g(0) is exact; c3 involves all four,
  //   so a missing one makes g NaN everywhere in the interval.
  //   R.ok = false when v is off the lattice.
  function rowSums(vals, soft, rows, cols, v, R) {
    var i = Math.floor(v), fv = v - i;
    R.ok = i >= -1 && i < rows;
    if (!R.ok) return R;
    var P = R.P, S = R.S, Q = R.Q, cub = R.cub, wv = R.wv, c;
    P.fill(0); S.fill(0); Q.fill(0);
    for (var di = 0; di < 2; di++) {
      var r = i + di;
      if (r < 0 || r >= rows) continue;
      var w = di ? fv : 1 - fv, base = r * cols;
      for (c = 0; c < cols; c++) {
        var x = vals[base + c];
        if (x !== x) continue;               // NaN: no data
        P[c + 1] += w; S[c + 1] += w * x;
        if (soft) Q[c + 1] += w * soft[base + c];
      }
    }
    crWeights(fv, wv, 0);
    var last = rows - 1;
    var b0 = Math.max(0, Math.min(last, i - 1)) * cols, b1 = Math.max(0, Math.min(last, i)) * cols;
    var b2 = Math.max(0, Math.min(last, i + 1)) * cols, b3 = Math.max(0, Math.min(last, i + 2)) * cols;
    var w0 = wv[0], w1 = wv[1], w2 = wv[2], w3 = wv[3];
    for (c = 0; c < cols; c++) cub[c] = w0 * vals[b0 + c] + w1 * vals[b1 + c] + w2 * vals[b2 + c] + w3 * vals[b3 + c];
    var C = R.C, lc = cols - 1;
    for (var b = 0; b <= cols; b++) {
      var j = b - 1;
      var p0 = cub[Math.max(0, Math.min(lc, j - 1))], p1 = cub[Math.max(0, Math.min(lc, j))];
      var p2 = cub[Math.max(0, Math.min(lc, j + 1))], p3 = cub[Math.max(0, Math.min(lc, j + 2))];
      C[4 * b] = p1;
      C[4 * b + 1] = 0.5 * (p2 - p0);
      C[4 * b + 2] = 0.5 * (2 * p0 - 5 * p1 + 4 * p2 - p3);
      C[4 * b + 3] = 0.5 * (3 * p1 - p0 - 3 * p2 + p3);
    }
    return R;
  }

  // Field along the row in R at every column of taps T: gs[x] = g (NaN = no
  // data), as[x] = coverage (scaled by the soft sums in Q when hasSoft).
  // The hot loop. It walks runs of columns in the same lattice interval, so
  // the interval's coefficients stay in locals and a pixel costs only the
  // polynomial; inside the forecast area (both columns full) the coverage
  // needs no smoothstep or division.
  function rowEval(R, T, hasSoft, gs, as) {
    var n = gs.length, none = NaN;         // local: a global lookup per pixel is slow in some engines
    if (!R.ok) { gs.fill(none); as.fill(0); return; }
    var bj = T.bj, fu = T.fu, P = R.P, S = R.S, Q = R.Q, C = R.C;
    var x = 0, end, f, e;
    while (x < n) {
      var b = bj[x];
      for (end = x + 1; end < n && bj[end] === b; end++);
      if (b < 0) { for (; x < end; x++) { gs[x] = none; as[x] = 0; } continue; }
      var p0 = P[b], p1 = P[b + 1], s0 = S[b], s1 = S[b + 1], q0 = hasSoft ? Q[b] : 0, q1 = hasSoft ? Q[b + 1] : 0;
      var o = 4 * b, c0 = C[o], c1 = C[o + 1], c2 = C[o + 2], c3 = C[o + 3];
      var cubic = c3 === c3;               // NaN when one of the 16 has no data
      if (p0 === 1 && p1 === 1) {          // full coverage on both sides
        for (; x < end; x++) {
          f = fu[x]; e = 1 - f;
          as[x] = hasSoft ? (e * q0 + f * q1) / 2 : 1;
          gs[x] = cubic ? ((c3 * f + c2) * f + c1) * f + c0 : e * s0 + f * s1;
        }
        continue;
      }
      for (; x < end; x++) {
        f = fu[x]; e = 1 - f;
        var p = e * p0 + f * p1;
        if (!(p > 0)) { gs[x] = none; as[x] = 0; continue; }
        var a = 2 * p - 1;
        a = a <= 0 ? 0 : a >= 1 ? 1 : a * a * (3 - 2 * a);
        as[x] = hasSoft ? a * (e * q0 + f * q1) / (2 * p) : a;
        gs[x] = cubic ? ((c3 * f + c2) * f + c1) * f + c0 : (e * s0 + f * s1) / p;
      }
    }
  }

  function sample(grid, vals, lat, lon) {
    var R = rowSums(vals, grid.soft, grid.rows, grid.cols, lat / grid.step - 0.5 - grid.i0, rowBufs(grid.cols));
    var T = colTaps(grid.cols, [lon / grid.step - 0.5 - grid.j0]);
    var gs = new Float64Array(1), as = new Float64Array(1);
    rowEval(R, T, !!grid.soft, gs, as);
    return { g: gs[0], a: as[0] };
  }

  // The image columns' taps: computed once per grid (its width never changes).
  function gridTaps(grid) {
    if (grid._taps) return grid._taps;
    var W = grid.w, us = new Float64Array(W);
    for (var x = 0; x < W; x++) us[x] = (grid.west + (grid.east - grid.west) * (x + 0.5) / W) / grid.step - 0.5 - grid.j0;
    return (grid._taps = colTaps(grid.cols, us));
  }

  // Runs fn(y, gs, as) for every image row y with that row's g and coverage
  // (buffers reused from row to row).
  function eachRow(grid, vals, fn) {
    var W = grid.w, H = grid.h, T = gridTaps(grid), R = rowBufs(grid.cols), soft = !!grid.soft;
    var gs = new Float64Array(W), as = new Float64Array(W);
    for (var y = 0; y < H; y++) {
      rowSums(vals, grid.soft, grid.rows, grid.cols, rowLat(y + 0.5, H, grid.north, grid.south) / grid.step - 0.5 - grid.i0, R);
      rowEval(R, T, soft, gs, as);
      fn(y, gs, as);
    }
  }

  // g and coverage per image pixel (row-major), as renderField sees them.
  function fieldValues(grid, vals) {
    var W = grid.w, out = { g: new Float32Array(W * grid.h), a: new Float32Array(W * grid.h) };
    eachRow(grid, vals, function (y, gs, as) { out.g.set(gs, y * W); out.a.set(as, y * W); });
    return out;
  }

  // Fills `out` (RGBA, grid.w x grid.h, all zero) with the field. O(pixels).
  function renderField(grid, vals, lut, out) {
    var W = grid.w, top = LUT_SIZE - 1;
    eachRow(grid, vals, function (y, gs, as) {
      for (var x = 0, o = y * W * 4; x < W; x++, o += 4) {
        var cov = as[x];
        if (!(cov > 0)) continue;
        var ng = -gs[x];
        if (!(ng >= 0)) continue;              // no colour, or NaN
        var n = ng > top ? top : ng | 0;       // floor
        var a = lut[n * 4 + 3];
        if (!a) continue;
        out[o] = lut[n * 4]; out[o + 1] = lut[n * 4 + 1]; out[o + 2] = lut[n * 4 + 2];
        out[o + 3] = a * cov;
      }
    });
    return out;
  }

  // ── Layer ─────────────────────────────────────────────────────────────────

  // Leaflet 1.9.4's canvas _updatePaths (run on every map move) redraws at
  // once but leaves an already queued animation-frame redraw in place, its id
  // dropped. If the overlay is switched off before that frame, removing the
  // renderer cannot cancel it, and the orphaned callback draws to the deleted
  // context ("reading 'clearRect'"). This renderer cancels the queued frame
  // before redrawing.
  var CanvasClass = null;
  function newRenderer() {
    if (!CanvasClass && L.Canvas && typeof L.Canvas.extend === 'function') {
      CanvasClass = L.Canvas.extend({
        _updatePaths: function () {
          if (!this._postponeUpdatePaths && this._redrawRequest) {
            L.Util.cancelAnimFrame(this._redrawRequest);
            this._redrawRequest = null;
          }
          L.Canvas.prototype._updatePaths.call(this);
        }
      });
    }
    return CanvasClass ? new CanvasClass({ padding: 0.5 }) : L.canvas({ padding: 0.5 });
  }

  // An image overlay whose image is a canvas: the field is drawn straight
  // into it (no data URL encode/decode), and Leaflet positions and scales it
  // like any image overlay. null when this Leaflet has no ImageOverlay.
  var FieldClass = null;
  function newField(canvas, bounds, pane) {
    if (!FieldClass && L.ImageOverlay && typeof L.ImageOverlay.extend === 'function') {
      FieldClass = L.ImageOverlay.extend({
        _initImage: function () {
          var img = this._image = this._url;
          L.DomUtil.addClass(img, 'leaflet-image-layer');
          if (this._zoomAnimated) L.DomUtil.addClass(img, 'leaflet-zoom-animated');
          if (this.options.className) L.DomUtil.addClass(img, this.options.className);
          img.onselectstart = L.Util.falseFn;
          img.onmousemove = L.Util.falseFn;
        }
      });
    }
    if (!FieldClass) return null;
    var opts = { interactive: false, className: 'mc-duct-field' };
    if (pane) opts.pane = pane;
    var f = new FieldClass(canvas, bounds, opts);
    f._mcField = true;
    return f;
  }

  // The field's own pane: above the tiles, below the tile overlays (labels,
  // hillshade), vectors and markers; never takes pointer events.
  function fieldPane(map) {
    try {
      if (map.getPane(FIELD_PANE)) return FIELD_PANE;
      var p = map.createPane(FIELD_PANE);
      p.style.zIndex = FIELD_Z;
      p.style.pointerEvents = 'none';
      return FIELD_PANE;
    } catch (_) { return null; }
  }

  function gridBounds(g) { return [[g.south, g.west], [g.north, g.east]]; }

  function makeLayerClass() {
    return L.Layer.extend({
      initialize: function (opts) {
        L.setOptions(this, opts || {});
        this._gen = 0;
        this._data = null;
        this._state = 'loading';   // loading | pending | error | ok
        this._timer = null;
        this._idx = -1;
        this._userPicked = false;
      },

      onAdd: function (map) {
        this._map = map;
        var gen = ++this._gen;
        this._rects = [];
        this._edge = null;
        this._edgeColor = '';
        this._grid = null;
        try {
          this._renderer = newRenderer();
          this._group = L.layerGroup();
          map.addLayer(this._group);
        } catch (_) { this._group = null; }
        this._pane = fieldPane(map);
        this._addControls(map);
        this._watchAttribution(map);
        this._renderLegend();
        this._load(gen);
        this._schedule(REFRESH_MS);
        var self = this;
        // Colours are read from CSS variables, so a theme switch needs a redraw.
        this._onTheme = function () { if (self._data) self._restyle(); };
        try { window.addEventListener('theme-changed', this._onTheme); } catch (_) {}
      },

      onRemove: function (map) {
        this._gen++;                       // in-flight results are ignored
        if (this._timer != null) { try { clearInterval(this._timer); } catch (_) {} this._timer = null; }
        this._timerMs = 0;
        if (this._abort) { try { this._abort.abort(); } catch (_) {} this._abort = null; }
        var m = map || this._map;
        if (this._onTheme) { try { window.removeEventListener('theme-changed', this._onTheme); } catch (_) {} this._onTheme = null; }
        if (this._slider && this._onSlide) {
          try { this._slider.removeEventListener('input', this._onSlide); } catch (_) {}
        }
        this._unwatchAttribution(m);
        if (this._legendCtl) { try { this._legendCtl.remove(); } catch (_) {} }
        if (this._timeCtl) { try { this._timeCtl.remove(); } catch (_) {} }
        if (this._group && m) { try { m.removeLayer(this._group); } catch (_) {} }
        if (this._field && m) { try { m.removeLayer(this._field); } catch (_) {} }
        if (this._renderer && m) { try { m.removeLayer(this._renderer); } catch (_) {} }
        this._map = this._group = this._renderer = this._field = this._canvas = this._grid = this._img = null;
        this._legendCtl = this._timeCtl = this._legendEl = this._statusEl = this._metaEl = null;
        this._slider = this._timeLabel = this._onSlide = null;
        this._rects = [];
        this._edge = null;
      },

      // ── controls ──
      _addControls: function (map) {
        var self = this;
        try {
          var timeCtl = new L.Control({ position: 'bottomleft' });
          timeCtl.onAdd = function () {
            var box = el('div', 'mc-duct-time');
            var label = el('label', 'mc-duct-time-label', 'Forecast time');
            label.setAttribute('for', 'mc-duct-time-input');
            var slider = el('input');
            slider.type = 'range';
            slider.id = 'mc-duct-time-input';
            slider.min = '0';
            slider.max = '0';
            slider.step = '1';
            slider.value = '0';
            slider.disabled = true;
            slider.setAttribute('aria-label', 'Ducting forecast time');
            self._onSlide = function () {
              var i = parseInt(slider.value, 10);
              if (isFinite(i)) { self._userPicked = true; self._setIndex(i); }
            };
            slider.addEventListener('input', self._onSlide);
            box.appendChild(label);
            box.appendChild(slider);
            self._timeLabel = label;
            self._slider = slider;
            if (L.DomEvent && L.DomEvent.disableClickPropagation) L.DomEvent.disableClickPropagation(box);
            return box;
          };
          this._timeCtl = timeCtl;
          timeCtl.addTo(map);

          var legendCtl = new L.Control({ position: 'bottomleft' });
          legendCtl.onAdd = function () {
            var box = el('div', 'mc-duct-legend');
            self._legendEl = box;
            if (L.DomEvent && L.DomEvent.disableClickPropagation) L.DomEvent.disableClickPropagation(box);
            return box;
          };
          this._legendCtl = legendCtl;
          legendCtl.addTo(map);
        } catch (_) { /* map already gone */ }
      },

      // The bottom-left corner shares the map's bottom edge with Leaflet's
      // attribution bar, which wraps to several lines on narrow screens.
      // Lift the corner by the bar's measured height whenever the two overlap
      // horizontally; re-measured on resize, layer changes and bar resizes.
      _watchAttribution: function (map) {
        var self = this;
        this._lift = function () { self._applyLift(); };
        try { map.on('resize layeradd layerremove', this._lift); } catch (_) {}
        try {
          var attr = this._attrEl();
          if (attr && typeof ResizeObserver === 'function') {
            this._ro = new ResizeObserver(this._lift);
            this._ro.observe(attr);
          }
        } catch (_) {}
        this._applyLift();
      },

      _unwatchAttribution: function (map) {
        if (this._ro) { try { this._ro.disconnect(); } catch (_) {} this._ro = null; }
        if (this._lift && map) { try { map.off('resize layeradd layerremove', this._lift); } catch (_) {} }
        if (this._corner) { try { this._corner.style.marginBottom = ''; } catch (_) {} }
        this._lift = this._corner = null;
      },

      _attrEl: function () {
        var ac = this._map && this._map.attributionControl;
        return (ac && typeof ac.getContainer === 'function') ? ac.getContainer() : null;
      },

      _applyLift: function () {
        var box = this._legendEl, attr = this._attrEl();
        var corner = box && box.parentNode;
        if (!corner || !corner.style) return;
        this._corner = corner;
        var h = 0;
        try {
          if (attr && attr.offsetHeight) {
            var a = attr.getBoundingClientRect(), c = corner.getBoundingClientRect();
            // Overlap only if the bar reaches left over the ducting controls.
            if (a.left < c.right) h = attr.offsetHeight;
          }
        } catch (_) { h = 0; }
        corner.style.marginBottom = h ? h + 'px' : '';
      },

      _renderLegend: function () {
        var box = this._legendEl;
        if (!box) return;
        var self = this;
        if (this._legendOpen == null) {
          // Compact by default on phones so the legend does not cover the map.
          var narrow = false;
          try { narrow = !!(window.matchMedia && window.matchMedia('(max-width: 600px)').matches); } catch (_) {}
          this._legendOpen = !narrow;
        }
        box.textContent = '';
        var head = el('button', 'mc-duct-legend-title', 'Tropospheric ducting');
        head.type = 'button';
        head.setAttribute('aria-expanded', this._legendOpen ? 'true' : 'false');
        head.addEventListener('click', function () { self._legendOpen = !self._legendOpen; self._renderLegend(); });
        box.appendChild(head);
        if (!this._legendOpen) return;
        var d = this._data;
        if (!d) {
          var st = el('div', 'mc-duct-legend-status', this._state === 'pending' ? MSG_PENDING : this._state === 'error' ? MSG_ERROR : 'Loading ducting forecast');
          st.setAttribute('role', 'status');
          box.appendChild(st);
        } else {
          box.appendChild(this._scale());
          var lrow = el('div', 'mc-duct-legend-row');
          lrow.appendChild(el('span', 'mc-duct-swatch mc-duct-swatch-lora'));
          lrow.appendChild(el('span', null, 'Dashed outline: a duct here can carry LoRa 868/915 MHz'));
          box.appendChild(lrow);
          var erow = el('div', 'mc-duct-legend-row');
          erow.appendChild(el('span', 'mc-duct-swatch mc-duct-swatch-edge'));
          erow.appendChild(el('span', null, 'Dotted line: edge of the forecast area (no forecast beyond it)'));
          box.appendChild(erow);
          var meta = d.attribution || d.modelLabel || d.model || '';
          if (meta) box.appendChild(el('div', 'mc-duct-legend-meta', meta));
          if (d.generatedAt) box.appendChild(el('div', 'mc-duct-legend-meta', 'Updated ' + fmtTime(d.generatedAt)));
        }
        box.appendChild(el('div', 'mc-duct-legend-note', NOTE));
      },

      // Bar of the same colour bands as the field (hard stops: each colour
      // starts where the previous ends), labels under it.
      _scale: function () {
        var wrap = el('div', 'mc-duct-scale');
        var bar = el('div', 'mc-duct-scale-bar');
        var parts = ['transparent 0%', 'transparent ' + pct(BAR_POS[0])];
        for (var k = 0; k < STOP_G.length; k++) {
          var c = 'var(--mc-duct-s' + (k + 1) + ') ';
          parts.push(c + pct(BAR_POS[k]), c + pct(k + 1 < BAR_POS.length ? BAR_POS[k + 1] : 1));
        }
        bar.style.background = 'linear-gradient(to right, ' + parts.join(', ') + ')';
        bar.setAttribute('aria-hidden', 'true');
        wrap.appendChild(bar);
        var labels = el('div', 'mc-duct-scale-labels');
        for (var i = 0; i < BAR_LABELS.length; i++) {
          var b = BAR_LABELS[i];
          var lab = el('span', 'mc-duct-scale-label' + (b[2] ? ' mc-duct-scale-label-end' : ''), b[1]);
          lab.style.left = pct(b[0]);
          labels.appendChild(lab);
        }
        wrap.appendChild(labels);
        wrap.appendChild(el('div', 'mc-duct-legend-caption', 'Refractivity gradient dN/dh (N/km): super-refraction from -79, ducting from -157'));
        return wrap;
      },

      // ── data ──
      _load: function (gen) {
        var self = this;
        if (gen !== this._gen) return;
        var opts = { headers: { Accept: 'application/json' } };
        if (typeof AbortController === 'function') {
          if (this._abort) { try { this._abort.abort(); } catch (_) {} }
          this._abort = new AbortController();
          opts.signal = this._abort.signal;
        }
        var p;
        try {
          p = CSApi.fetch(API_URL, opts).then(function (r) {
            if (!r || r.ok === false) throw new Error('http');
            return r.json();
          });
        } catch (_) { p = Promise.reject(new Error('fetch')); }
        p.then(function (d) {
          if (gen !== self._gen) return;
          self._apply(d);
        }, function () {
          if (gen !== self._gen) return;
          self._fail('error');
        });
      },

      // Re-arms the refresh interval when its period changes.
      _schedule: function (ms) {
        if (this._timer != null && this._timerMs === ms) return;
        if (this._timer != null) { try { clearInterval(this._timer); } catch (_) {} }
        var self = this;
        this._timerMs = ms;
        this._timer = setInterval(function () { self._load(self._gen); }, ms);
      },

      _fail: function (state) {
        if (this._data) return;            // keep the last good data
        this._schedule(RETRY_MS);
        this._state = state;
        this._renderLegend();
      },

      _apply: function (d) {
        if (!d || typeof d !== 'object') return this._fail('error');
        if (d.status === 'pending') return this._fail('pending');
        if (d.status !== 'ok' || !Array.isArray(d.cells) || !Array.isArray(d.times) || !d.times.length) return this._fail('error');
        var prevTime = (this._data && this._idx >= 0) ? this._data.times[this._idx] : null;
        this._data = d;
        this._state = 'ok';
        this._schedule(REFRESH_MS);
        this._build();
        var idx = ductTimeIndex(d.times, Date.now() / 1000);
        if (this._userPicked && prevTime != null) {
          var keep = d.times.indexOf(prevTime);
          if (keep >= 0) idx = keep;
        }
        if (this._slider) {
          this._slider.max = String(d.times.length - 1);
          this._slider.disabled = d.times.length < 2;
        }
        this._renderLegend();
        this._setIndex(idx);
      },

      // One invisible, clickable rectangle per cell that has data (class >= 1)
      // at some hour, and the lattice the field is drawn on.
      _build: function () {
        var d = this._data, group = this._group;
        if (!group) return;
        try { group.clearLayers(); } catch (_) {}
        this._rects = [];
        this._edge = null;
        this._grid = ductGrid(d.cells, Number(d.stepDeg) || 0.5);
        var h = (Number(d.stepDeg) || 0.5) / 2;
        for (var i = 0; i < d.cells.length; i++) {
          var cell = d.cells[i];
          if (!cell || !Array.isArray(cell.c)) continue;
          var any = false;
          for (var t = 0; t < cell.c.length; t++) if (cell.c[t] >= 1) { any = true; break; }
          if (!any) continue;
          var rect = L.rectangle([[cell.lat - h, cell.lon - h], [cell.lat + h, cell.lon + h]],
            { renderer: this._renderer, stroke: false, fill: true, fillOpacity: 0, interactive: true });
          rect._mcCell = cell;
          rect._mcLora = '';
          rect.bindPopup(this._popupFn(cell));
          group.addLayer(rect);
          this._rects.push(rect);
        }
        this._buildEdge(group);
      },

      // The forecast area's outline: one non-interactive multi-part polyline
      // (a part per merged run of boundary cell edges), on the shared canvas.
      _buildEdge: function (group) {
        var segs = ductEdges(this._grid);
        if (!segs.length || !L.polyline) return;
        var color = cssVar(EDGE_VAR);
        try {
          var line = L.polyline(segs, L.extend({ renderer: this._renderer, color: color || undefined }, EDGE_STYLE, { stroke: !!color }));
          line._mcEdge = true;
          group.addLayer(line);
          this._edge = line;
          this._edgeColor = color;
        } catch (_) { this._edge = null; }
      },

      _popupFn: function (cell) {
        var self = this;
        return function () { return popupContent(cell, self._idx); };
      },

      _setIndex: function (i) {
        var d = this._data;
        if (!d || i < 0 || i >= d.times.length) return;
        this._idx = i;
        if (this._slider) this._slider.value = String(i);
        if (this._timeLabel) this._timeLabel.textContent = 'Forecast time: ' + fmtTime(d.times[i]);
        this._restyle();
      },

      // Redraws the field image, the edge colour and the LoRa outlines for the current hour
      // and theme; the rectangles themselves are kept.
      _restyle: function () {
        this._drawField();
        var edgeColor = cssVar(EDGE_VAR);
        if (this._edge && edgeColor !== this._edgeColor) {   // theme change
          this._edgeColor = edgeColor;
          this._edge.setStyle(edgeColor ? { stroke: true, color: edgeColor } : { stroke: false });
        }
        var i = this._idx, lora = cssVar(LORA_VAR);
        for (var r = 0; r < this._rects.length; r++) {
          var rect = this._rects[r];
          var want = (lora && isLora(rect._mcCell, i)) ? lora : '';
          if (want === rect._mcLora) continue;   // only changed cells redraw
          rect._mcLora = want;
          var st = want ? L.extend({ color: want }, LORA_STYLE) : { stroke: false };
          st.fillOpacity = 0;
          rect.setStyle(st);
        }
      },

      _drawField: function () {
        var g = this._grid, map = this._map, d = this._data;
        if (!g || !map || !d) { this._dropField(); return; }
        var t0 = Date.now();
        var canvas = this._canvas;
        try {
          if (!canvas) canvas = this._canvas = document.createElement('canvas');
          if (canvas.width !== g.w) canvas.width = g.w;
          if (canvas.height !== g.h) canvas.height = g.h;
          var ctx = canvas.getContext('2d');
          // One pixel buffer per image size, cleared for each redraw.
          var img = this._img;
          if (!img || img.width !== g.w || img.height !== g.h) img = this._img = ctx.createImageData(g.w, g.h);
          else img.data.fill(0);
          renderField(g, ductGridValues(g, d.cells, this._idx), fieldLut(fieldStops(), fieldAlpha()), img.data);
          ctx.putImageData(img, 0, 0);
        } catch (_) { this._dropField(); return; }
        var bounds = gridBounds(g);
        if (!this._field) {
          this._field = newField(canvas, bounds, this._pane);
          if (this._field) { try { map.addLayer(this._field); } catch (_) { this._field = null; } }
        } else if (this._fieldKey !== String(bounds)) {
          try { this._field.setBounds(L.latLngBounds(bounds)); } catch (_) {}
        }
        this._fieldKey = String(bounds);
        this._fieldMs = Date.now() - t0;
      },

      _dropField: function () {
        if (this._field && this._map) { try { this._map.removeLayer(this._field); } catch (_) {} }
        this._field = null;
        this._fieldKey = '';
      }
    });
  }

  var LayerClass = null;
  window.MC_createDuctingLayer = function (opts) {
    if (typeof L === 'undefined' || !L.Layer) return null;
    if (!LayerClass) LayerClass = makeLayerClass();
    return new LayerClass(opts);
  };
  window.MC_ductTimeIndex = ductTimeIndex;
  window.MC_ductPopupContent = popupContent;
  window.MC_ductFieldColor = fieldColor;
  window.MC_ductFieldStops = fieldStops;
  window.MC_ductGrid = ductGrid;
  window.MC_ductEdges = ductEdges;
  window.MC_ductGridValues = ductGridValues;
  window.MC_ductSample = sample;
  window.MC_ductFieldValues = fieldValues;
  window.MC_ductRowLat = rowLat;
})();
