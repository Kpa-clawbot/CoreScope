/* map-ducting.js — tropospheric ducting map overlay.
 *
 * Shows, per grid cell, whether the atmosphere can bend or trap radio signals
 * and whether a duct can carry LoRa (868/915 MHz). Data: GET /api/ducting
 * (hourly forecast, one array entry per `times` entry, see
 * cmd/server/ducting_types.go).
 *
 *   window.MC_createDuctingLayer(opts) -> L.Layer
 *       Fetches /api/ducting when added to a map, refreshes every 30 min while
 *       on it, draws one canvas rectangle per cell that is ever a duct or
 *       super-refraction, and adds a legend and a forecast-time slider.
 *       A layer removed mid-fetch never touches the map afterwards.
 *   window.MC_ductBin(cls, fmin)        -> 'lora'|'uhf'|'micro'|'super'|null
 *   window.MC_ductTimeIndex(times, now) -> index of the latest time <= now
 *
 * Fill colours are read from the --mc-duct-* CSS variables at draw time.
 */
(function () {
  'use strict';

  var API_URL = '/api/ducting';
  var REFRESH_MS = 30 * 60 * 1000;
  // While there is no data yet (server still preparing, or a failed fetch),
  // ask again every minute instead of waiting a full refresh period.
  var RETRY_MS = 60 * 1000;
  var LORA_MAX_MHZ = 1000;   // duct with fmin <= 1 GHz carries 868/915 MHz
  var UHF_MAX_MHZ = 3000;
  var BAND_868 = 868;
  var BAND_915 = 915;

  // Fill opacity per class: elevated ducts are lighter than surface-based.
  var OPACITY = { 2: 0.35, 3: 0.3, 4: 0.55 };
  var VAR_NAME = { lora: '--mc-duct-lora', uhf: '--mc-duct-uhf', micro: '--mc-duct-micro', super: '--mc-duct-super' };
  var GRID_VAR = '--mc-duct-grid';
  var TYPE_NAME = { 0: 'No data', 1: 'Normal', 2: 'Super-refraction', 3: 'Elevated duct', 4: 'Surface-based duct' };

  var NOTE = 'Indicator only: model levels are ~200 m apart, so thinner ducts are not visible.';
  var MSG_PENDING = 'Ducting forecast is being prepared';
  var MSG_ERROR = 'Ducting forecast unavailable';

  var LEGEND_ROWS = [
    ['lora', 'Duct that can carry LoRa 868/915 MHz'],
    ['uhf', 'Duct for 1-3 GHz only'],
    ['micro', 'Duct for above 3 GHz only'],
    ['super', 'Super-refraction (extended range, no duct)']
  ];

  // ── Pure helpers ──────────────────────────────────────────────────────────

  function ductBin(cls, fmin) {
    if (cls === 2) return 'super';
    if (cls !== 3 && cls !== 4) return null;
    var f = Number(fmin);
    if (!isFinite(f) || f <= 0) return null;   // 0 = no duct
    if (f <= LORA_MAX_MHZ) return 'lora';
    if (f <= UHF_MAX_MHZ) return 'uhf';
    return 'micro';
  }

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

  // '' when the variable is undefined (the cell is then left undrawn).
  function cssVar(name) {
    try {
      var v = getComputedStyle(document.documentElement).getPropertyValue(name);
      return v ? String(v).trim() : '';
    } catch (_) { return ''; }
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
        try {
          this._renderer = newRenderer();
          this._group = L.layerGroup();
          map.addLayer(this._group);
        } catch (_) { this._group = null; }
        this._addControls(map);
        this._watchAttribution(map);
        this._renderLegend();
        this._load(gen);
        this._schedule(REFRESH_MS);
        var self = this;
        // Fills are read from CSS variables, so a theme switch needs a restyle.
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
        if (this._renderer && m) { try { m.removeLayer(this._renderer); } catch (_) {} }
        this._map = this._group = this._renderer = null;
        this._legendCtl = this._timeCtl = this._legendEl = this._statusEl = this._metaEl = null;
        this._slider = this._timeLabel = this._onSlide = null;
        this._rects = [];
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
          for (var i = 0; i < LEGEND_ROWS.length; i++) {
            var row = el('div', 'mc-duct-legend-row');
            row.appendChild(el('span', 'mc-duct-swatch mc-duct-swatch-' + LEGEND_ROWS[i][0]));
            row.appendChild(el('span', null, LEGEND_ROWS[i][1]));
            box.appendChild(row);
          }
          var nrow = el('div', 'mc-duct-legend-row');
          nrow.appendChild(el('span', 'mc-duct-swatch mc-duct-swatch-normal'));
          nrow.appendChild(el('span', null, 'Outline only: normal propagation'));
          box.appendChild(nrow);
          box.appendChild(el('div', 'mc-duct-legend-note', 'Lighter shade: elevated duct (not touching the ground)'));
          var meta = d.attribution || d.modelLabel || d.model || '';
          if (meta) box.appendChild(el('div', 'mc-duct-legend-meta', meta));
          if (d.generatedAt) box.appendChild(el('div', 'mc-duct-legend-meta', 'Updated ' + fmtTime(d.generatedAt)));
        }
        box.appendChild(el('div', 'mc-duct-legend-note', NOTE));
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

      // One rectangle per cell that has data (class >= 1) at some hour.
      _build: function () {
        var d = this._data, group = this._group;
        if (!group) return;
        try { group.clearLayers(); } catch (_) {}
        this._rects = [];
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
          rect.bindPopup(this._popupFn(cell));
          group.addLayer(rect);
          this._rects.push(rect);
        }
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

      _restyle: function () {
        var i = this._idx, colors = {}, grid = cssVar(GRID_VAR);
        for (var b in VAR_NAME) colors[b] = cssVar(VAR_NAME[b]);
        for (var r = 0; r < this._rects.length; r++) {
          var rect = this._rects[r], cell = rect._mcCell;
          var cls = at(cell.c, i);
          var bin = ductBin(cls, at(cell.f, i));
          // Every cell with data gets a faint outline, so "normal" differs from "no data".
          var st = (cls >= 1 && grid) ? { stroke: true, color: grid, weight: 1, opacity: 1 } : { stroke: false };
          if (bin && colors[bin]) { st.fillColor = colors[bin]; st.fillOpacity = OPACITY[cls]; }
          else st.fillOpacity = 0;
          rect.setStyle(st);
        }
      }
    });
  }

  var LayerClass = null;
  window.MC_createDuctingLayer = function (opts) {
    if (typeof L === 'undefined' || !L.Layer) return null;
    if (!LayerClass) LayerClass = makeLayerClass();
    return new LayerClass(opts);
  };
  window.MC_ductBin = ductBin;
  window.MC_ductTimeIndex = ductTimeIndex;
  window.MC_ductPopupContent = popupContent;
})();
