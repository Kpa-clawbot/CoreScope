/* === MeshCore Analyzer — region-filter.js (shared region filter component) === */
'use strict';

(function () {
  var LS_KEY = 'meshcore-region-filter';
  var _regions = {};       // { code: label }
  var _selected = null;    // Set of selected region codes, null = all
  var _listeners = [];
  var _loaded = false;

  function loadFromStorage() {
    try {
      var stored = JSON.parse(localStorage.getItem(LS_KEY));
      if (Array.isArray(stored) && stored.length > 0) return new Set(stored);
    } catch (e) { /* ignore */ }
    return null; // null = all selected
  }

  function saveToStorage() {
    if (!_selected) {
      localStorage.removeItem(LS_KEY);
    } else {
      localStorage.setItem(LS_KEY, JSON.stringify(Array.from(_selected)));
    }
  }

  _selected = loadFromStorage();

  /** Fetch regions from server */
  async function fetchRegions() {
    if (_loaded) return _regions;
    try {
      var data = await fetch('/api/config/regions').then(function (r) { return r.json(); });
      _regions = data || {};
      _loaded = true;
      // If stored selection has codes no longer valid, clean up
      if (_selected) {
        var codes = Object.keys(_regions);
        var cleaned = new Set();
        _selected.forEach(function (c) { if (codes.includes(c)) cleaned.add(c); });
        _selected = cleaned.size > 0 ? cleaned : null;
        saveToStorage();
      }
    } catch (e) {
      _regions = {};
    }
    return _regions;
  }

  /** Get selected regions as array, or null if all */
  function getSelected() {
    if (!_selected || _selected.size === 0) return null;
    return Array.from(_selected);
  }

  /** Get region query param string for API calls: "SJC,SFO" or empty */
  function getRegionParam() {
    var sel = getSelected();
    return sel ? sel.join(',') : '';
  }

  /** Build query string fragment: "&region=SJC,SFO" or "" */
  function regionQueryString() {
    var p = getRegionParam();
    return p ? '&region=' + encodeURIComponent(p) : '';
  }

  /** Render the filter bar into a container element */
  function render(container) {
    var codes = Object.keys(_regions);
    if (codes.length < 2) {
      container.innerHTML = '';
      container.style.display = 'none';
      return;
    }
    container.style.display = '';
    var allSelected = !_selected;
    var html = '<div class="region-filter-bar">';
    html += '<button class="region-pill' + (allSelected ? ' region-pill-active' : '') + '" data-region="__all__">All</button>';
    codes.forEach(function (code) {
      var label = _regions[code] || code;
      var active = allSelected || (_selected && _selected.has(code));
      html += '<button class="region-pill' + (active ? ' region-pill-active' : '') + '" data-region="' + code + '">' + label + '</button>';
    });
    html += '</div>';
    container.innerHTML = html;

    container.onclick = function (e) {
      var btn = e.target.closest('[data-region]');
      if (!btn) return;
      var region = btn.dataset.region;
      if (region === '__all__') {
        _selected = null;
      } else {
        if (!_selected) {
          // Switch from "all" to just this one
          _selected = new Set([region]);
        } else if (_selected.has(region)) {
          _selected.delete(region);
          if (_selected.size === 0) _selected = null; // back to all
        } else {
          _selected.add(region);
        }
        // If all individually selected, switch to "all" mode
        if (_selected && _selected.size === codes.length) _selected = null;
      }
      saveToStorage();
      render(container);
      _listeners.forEach(function (fn) { fn(getSelected()); });
    };
  }

  /** Subscribe to selection changes. Callback receives selected array or null */
  function onChange(fn) {
    _listeners.push(fn);
    return fn;
  }

  /** Unsubscribe */
  function offChange(fn) {
    _listeners = _listeners.filter(function (f) { return f !== fn; });
  }

  /** Initialize filter in a container, fetch regions, render, return promise */
  async function initFilter(container) {
    await fetchRegions();
    render(container);
  }

  // Expose globally
  window.RegionFilter = {
    init: initFilter,
    render: render,
    getSelected: getSelected,
    getRegionParam: getRegionParam,
    regionQueryString: regionQueryString,
    onChange: onChange,
    offChange: offChange,
    fetchRegions: fetchRegions
  };
})();
