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
  var LORA_MAX_MHZ = 1000;   // duct with fmin <= 1 GHz carries 868/915 MHz
  var UHF_MAX_MHZ = 3000;
  var BAND_868 = 868;
  var BAND_915 = 915;

  // Fill opacity per class: elevated ducts are lighter than surface-based.
  var OPACITY = { 2: 0.35, 3: 0.3, 4: 0.55 };
  var VAR_NAME = { lora: '--mc-duct-lora', uhf: '--mc-duct-uhf', micro: '--mc-duct-micro', super: '--mc-duct-super' };
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
          this._renderer = L.canvas({ padding: 0.5 });
          this._group = L.layerGroup();
          map.addLayer(this._group);
        } catch (_) { this._group = null; }
        this._addControls(map);
        this._renderLegend();
        this._load(gen);
        var self = this;
        this._timer = setInterval(function () { self._load(self._gen); }, REFRESH_MS);
        // Fills are read from CSS variables, so a theme switch needs a restyle.
        this._onTheme = function () { if (self._data) self._restyle(); };
        try { window.addEventListener('theme-changed', this._onTheme); } catch (_) {}
      },

      onRemove: function (map) {
        this._gen++;                       // in-flight results are ignored
        if (this._timer != null) { try { clearInterval(this._timer); } catch (_) {} this._timer = null; }
        if (this._abort) { try { this._abort.abort(); } catch (_) {} this._abort = null; }
        var m = map || this._map;
        if (this._onTheme) { try { window.removeEventListener('theme-changed', this._onTheme); } catch (_) {} this._onTheme = null; }
        if (this._slider && this._onSlide) {
          try { this._slider.removeEventListener('input', this._onSlide); } catch (_) {}
        }
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
          var timeCtl = new L.Control({ position: 'bottomright' });
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

          var legendCtl = new L.Control({ position: 'bottomright' });
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

      _renderLegend: function () {
        var box = this._legendEl;
        if (!box) return;
        box.textContent = '';
        box.appendChild(el('div', 'mc-duct-legend-title', 'Tropospheric ducting'));
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
          box.appendChild(el('div', 'mc-duct-legend-note', 'Lighter shade: elevated duct (not touching the ground)'));
          var meta = (d.modelLabel || d.model || '');
          if (d.attribution) meta += (meta ? '. ' : '') + d.attribution;
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

      _fail: function (state) {
        if (this._data) return;            // keep the last good data
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

      // One rectangle per cell that is ever a duct or super-refraction.
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
          for (var t = 0; t < cell.c.length; t++) if (cell.c[t] >= 2) { any = true; break; }
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
        var i = this._idx, colors = {};
        for (var b in VAR_NAME) colors[b] = cssVar(VAR_NAME[b]);
        for (var r = 0; r < this._rects.length; r++) {
          var rect = this._rects[r], cell = rect._mcCell;
          var cls = at(cell.c, i);
          var bin = ductBin(cls, at(cell.f, i));
          if (bin && colors[bin]) rect.setStyle({ fillColor: colors[bin], fillOpacity: OPACITY[cls] });
          else rect.setStyle({ fillOpacity: 0 });
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
