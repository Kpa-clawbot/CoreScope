/* === CoreScope — region-filter.js (shared region filter component) === */
'use strict';

(function () {
  var LS_KEY = 'meshcore-region-filter';
  var _regions = {};       // { code: label }
  var _selected = null;    // Set of selected region codes, null = all
  var _listeners = [];
  var _container = null;
  var _loaded = false;
  var _picks = [];         // configured quick picks: [{ name, description, regions: [code] }]

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

  /** Fetch the configured quick picks. A failure only means no quick picks. */
  async function fetchQuickPicks() {
    try {
      var data = await fetch('/api/config/region-quick-picks').then(function (r) { return r.json(); });
      _picks = (data && Array.isArray(data.quickPicks)) ? data.quickPicks : [];
    } catch (e) {
      _picks = [];
    }
  }

  /** Fetch regions (and quick picks) from server */
  async function fetchRegions() {
    if (_loaded) return _regions;
    var picksLoaded = fetchQuickPicks();
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
    await picksLoaded;
    return _regions;
  }

  function esc(v) {
    return String(v == null ? '' : v).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  /** Quick picks narrowed to the codes that have an observer today. A pick
   *  with none would select nothing, so it is not offered. */
  function presentPicks(codes) {
    var known = Object.create(null);
    codes.forEach(function (c) { known[c] = true; });
    var out = [];
    _picks.forEach(function (p) {
      var here = (p.regions || []).filter(function (c) { return known[c]; });
      if (here.length) out.push({ name: p.name, description: p.description || '', codes: here });
    });
    return out;
  }

  /** The pick whose codes are exactly the current selection, if any. */
  function activePick(picks) {
    if (!_selected || _selected.size === 0) return null;
    for (var i = 0; i < picks.length; i++) {
      var p = picks[i];
      if (p.codes.length !== _selected.size) continue;
      if (p.codes.every(function (c) { return _selected.has(c); })) return p;
    }
    return null;
  }

  function pickButtonsHtml(picks) {
    var active = activePick(picks);
    var html = '';
    picks.forEach(function (p, i) {
      var on = p === active;
      var title = (p.description ? p.description + ' ' : '') + 'Selects ' + p.codes.join(', ') + '.';
      html += '<button type="button" class="region-pill region-quick-pick' + (on ? ' region-pill-active' : '') +
        '" data-pick="' + i + '" aria-pressed="' + on + '" title="' + esc(title) + '">' + esc(p.name) + '</button>';
    });
    return html;
  }

  /** Quick picks as the first section of the region dropdown menu. */
  function menuPicksHtml(picks) {
    if (!picks.length) return '';
    return '<div class="region-quick-picks" role="group" aria-label="Quick picks">' +
      '<div class="region-quick-picks-label">Quick picks</div>' +
      '<div class="region-quick-picks-row">' + pickButtonsHtml(picks) + '</div></div>';
  }

  /** Quick picks inside the pill bar, ahead of the single regions. */
  function barPicksHtml(picks) {
    if (!picks.length) return '';
    return pickButtonsHtml(picks) + '<span class="region-quick-picks-divider" aria-hidden="true"></span>';
  }

  /** Tapping a pick selects its codes; tapping the active pick goes back to all. */
  // Like a click in the control, a pick saves, redraws and tells the page,
  // so the page re-queries straight away.
  function applyPick(picks, index) {
    var p = picks[index];
    if (!p) return;
    _selected = activePick(picks) === p ? null : new Set(p.codes);
    saveToStorage();
    if (_container) render(_container);
    _listeners.forEach(function (fn) { fn(getSelected()); });
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

  /** Handle a region toggle (shared logic for both pill and dropdown modes) */
  function toggleRegion(region, codes, container) {
    if (region === '__all__') {
      _selected = null;
    } else {
      if (!_selected) {
        _selected = new Set([region]);
      } else if (_selected.has(region)) {
        _selected.delete(region);
        if (_selected.size === 0) _selected = null;
      } else {
        _selected.add(region);
      }
      if (_selected && _selected.size === codes.length) _selected = null;
    }
    saveToStorage();
    render(container);
    _listeners.forEach(function (fn) { fn(getSelected()); });
  }

  /** Build summary label for dropdown trigger */
  function dropdownLabel(codes) {
    if (!_selected) return 'All Regions';
    var named = activePick(presentPicks(codes));
    if (named) return esc(named.name);
    var sel = Array.from(_selected);
    if (sel.length === 0) return 'All Regions';
    if (sel.length <= 2) return sel.join(', ');
    return sel.length + ' Regions';
  }

  /** Render pill bar mode (≤4 regions) */
  function renderPills(container, codes) {
    var allSelected = !_selected;
    var picks = presentPicks(codes);
    var html = '<div class="region-filter-bar" role="group" aria-label="Region filter">';
    html += '<span class="region-filter-label" id="region-filter-label">Region:</span>';
    html += barPicksHtml(picks);
    html += '<button class="region-pill' + (allSelected ? ' region-pill-active' : '') +
      '" data-region="__all__" role="checkbox" aria-checked="' + allSelected + '">All</button>';
    codes.forEach(function (code) {
      var label = _regions[code] || code;
      var active = allSelected || (_selected && _selected.has(code));
      html += '<button class="region-pill' + (active ? ' region-pill-active' : '') +
        '" data-region="' + code + '" role="checkbox" aria-checked="' + !!active + '">' + label + '</button>';
    });
    html += '</div>';
    container.innerHTML = html;

    container.onclick = function (e) {
      var pickBtn = e.target.closest('[data-pick]');
      if (pickBtn) { applyPick(picks, Number(pickBtn.dataset.pick)); return; }
      var btn = e.target.closest('[data-region]');
      if (!btn) return;
      toggleRegion(btn.dataset.region, codes, container);
    };
  }

  /** Render dropdown mode (>4 regions) */
  function renderDropdown(container, codes) {
    var allSelected = !_selected;
    var picks = presentPicks(codes);
    var html = '<div class="region-dropdown-wrap" role="group" aria-label="Region filter">';
    html += '<button class="region-dropdown-trigger" aria-haspopup="listbox" aria-expanded="false">' +
      dropdownLabel(codes) + ' ▾</button>';
    html += '<div class="region-dropdown-menu' + (picks.length ? ' has-quick-picks' : '') +
      '" role="listbox" aria-label="Select regions" hidden>';
    html += menuPicksHtml(picks);
    html += '<label class="region-dropdown-item"><input type="checkbox" data-region="__all__"' +
      (allSelected ? ' checked' : '') + '> <strong>All</strong></label>';
    codes.forEach(function (code) {
      var configLabel = _regions[code];
      var cityName = configLabel || (window.IATA_CITIES && window.IATA_CITIES[code]);
      var label = cityName ? (code + ' - ' + cityName) : code;
      var active = allSelected || (_selected && _selected.has(code));
      html += '<label class="region-dropdown-item"><input type="checkbox" data-region="' + code + '"' +
        (active ? ' checked' : '') + '> ' + label + '</label>';
    });
    html += '</div></div>';
    container.innerHTML = html;

    var trigger = container.querySelector('.region-dropdown-trigger');
    var menu = container.querySelector('.region-dropdown-menu');
    container.onclick = function (e) {
      var pickBtn = e.target.closest('[data-pick]');
      if (pickBtn) applyPick(picks, Number(pickBtn.dataset.pick));
    };

    trigger.onclick = function () {
      var open = !menu.hidden;
      menu.hidden = open;
      trigger.setAttribute('aria-expanded', String(!open));
    };

    menu.onchange = function (e) {
      var input = e.target;
      if (!input.dataset.region) return;
      toggleRegion(input.dataset.region, codes, container);
    };

    // Close on outside click
    function onDocClick(e) {
      if (!container.contains(e.target)) {
        menu.hidden = true;
        trigger.setAttribute('aria-expanded', 'false');
      }
    }
    document.addEventListener('click', onDocClick, true);
    container._regionCleanup = function () {
      document.removeEventListener('click', onDocClick, true);
    };
  }

  /** Render the filter bar into a container element */
  function render(container) {
    // Clean up previous outside-click listener if any
    if (container._regionCleanup) { container._regionCleanup(); container._regionCleanup = null; }

    var codes = Object.keys(_regions);
    if (codes.length < 2) {
      container.innerHTML = '';
      container.style.display = 'none';
      return;
    }
    container.style.display = '';

    if (codes.length > 4 || container._forceDropdown) {
      renderDropdown(container, codes);
    } else {
      renderPills(container, codes);
    }
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

  /** Initialize filter in a container, fetch regions, render, return promise.
   *  Options: { dropdown: true } to force dropdown mode regardless of region count */
  async function initFilter(container, opts) {
    _container = container;
    if (opts && opts.dropdown) container._forceDropdown = true;
    await fetchRegions();
    render(container);
  }

  /** Override selected regions (e.g. from URL param). Persists to localStorage and re-renders. */
  function setSelected(codesArray) {
    _selected = (codesArray && codesArray.length > 0) ? new Set(codesArray) : null;
    saveToStorage();
    if (_container) render(_container);
  }

  /**
   * #1108 — "Show all nodes (faded)" toggle.
   *
   * When a region is selected, the default behavior (showAll = false) is to
   * HIDE non-region nodes on the map: the operator is looking at a region for
   * a reason, and far-away nodes are visual noise. When the toggle is ON
   * (showAll = true), legacy behavior is restored — all nodes load, region
   * scoping only applies to packet feeds / metrics.
   *
   * State persists across reloads in localStorage. Default: false (hide).
   */
  var SHOW_ALL_KEY = 'mc-region-show-all-nodes';
  var _showAllListeners = [];
  function showAllGet() {
    try { return localStorage.getItem(SHOW_ALL_KEY) === 'true'; }
    catch (e) { return false; }
  }
  function showAllSet(v) {
    var bool = !!v;
    try {
      if (bool) localStorage.setItem(SHOW_ALL_KEY, 'true');
      else localStorage.removeItem(SHOW_ALL_KEY);
    } catch (e) { /* ignore */ }
    _showAllListeners.forEach(function (fn) { fn(bool); });
  }
  function showAllOnChange(fn) { _showAllListeners.push(fn); return fn; }
  function showAllOffChange(fn) {
    _showAllListeners = _showAllListeners.filter(function (f) { return f !== fn; });
  }

  /**
   * Build a node-list query fragment that respects the "show all nodes" toggle.
   * Returns "&region=SJC,SFO" only when a region is selected AND showAll is
   * OFF; otherwise empty string. Use this for /api/nodes? requests on map
   * surfaces where the operator expects the visible markers to follow the
   * region selector. Other surfaces (packets, metrics) should keep using
   * regionQueryString() which is unconditional.
   */
  function nodesRegionQueryString() {
    if (showAllGet()) return '';
    return regionQueryString();
  }

  // Expose globally
  window.RegionFilter = {
    init: initFilter,
    render: render,
    getSelected: getSelected,
    getRegionParam: getRegionParam,
    regionQueryString: regionQueryString,
    nodesRegionQueryString: nodesRegionQueryString,
    onChange: onChange,
    offChange: offChange,
    fetchRegions: fetchRegions,
    setSelected: setSelected
  };
  window.RegionShowAll = {
    get: showAllGet,
    set: showAllSet,
    onChange: showAllOnChange,
    offChange: showAllOffChange,
    STORAGE_KEY: SHOW_ALL_KEY
  };
})();
