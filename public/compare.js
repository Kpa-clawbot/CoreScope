/* === CoreScope — compare.js === */
/* Observer packet comparison — Fixes #129 */
'use strict';

/**
 * Compare two sets of packet hashes using Set operations.
 * Returns { onlyA, onlyB, both } as arrays of hashes.
 * O(n) via Set lookups — no nested loops.
 */
function comparePacketSets(hashesA, hashesB) {
  var setA = hashesA instanceof Set ? hashesA : new Set(hashesA || []);
  var setB = hashesB instanceof Set ? hashesB : new Set(hashesB || []);
  var onlyA = [];
  var onlyB = [];
  var both = [];
  setA.forEach(function (h) {
    if (setB.has(h)) both.push(h);
    else onlyA.push(h);
  });
  setB.forEach(function (h) {
    if (!setA.has(h)) onlyB.push(h);
  });
  return { onlyA: onlyA, onlyB: onlyB, both: both };
}

/**
 * Filter packets by route type.
 * mode: 'all' | 'flood' | 'direct'
 * Flood = route_type 0 (TransportFlood) or 1 (Flood)
 * Direct = route_type 2 (Direct) or 3 (TransportDirect)
 */
function filterPacketsByRoute(packets, mode) {
  if (!packets || mode === 'all') return packets || [];
  if (mode === 'flood') {
    return packets.filter(function (p) { return p.route_type === 0 || p.route_type === 1; });
  }
  if (mode === 'direct') {
    return packets.filter(function (p) { return p.route_type === 2 || p.route_type === 3; });
  }
  return packets;
}

/**
 * Compute asymmetric overlap statistics between two observer packet sets.
 * Given a comparePacketSets() result, returns:
 *   - totalA / totalB: unique packet count for each observer
 *   - shared: packets seen by both
 *   - onlyA / onlyB: exclusive packet counts
 *   - aSeesOfB: percentage of B's packets that A also saw (rounded to 0.1%)
 *   - bSeesOfA: percentage of A's packets that B also saw (rounded to 0.1%)
 * Returns 0% (not NaN) when a denominator is zero.
 */
function computeOverlapStats(cmp) {
  var onlyA = (cmp && cmp.onlyA && cmp.onlyA.length) || 0;
  var onlyB = (cmp && cmp.onlyB && cmp.onlyB.length) || 0;
  var shared = (cmp && cmp.both && cmp.both.length) || 0;
  var totalA = onlyA + shared;
  var totalB = onlyB + shared;
  var aSeesOfB = totalB > 0 ? Math.round((shared / totalB) * 1000) / 10 : 0;
  var bSeesOfA = totalA > 0 ? Math.round((shared / totalA) * 1000) / 10 : 0;
  return {
    totalA: totalA,
    totalB: totalB,
    shared: shared,
    onlyA: onlyA,
    onlyB: onlyB,
    aSeesOfB: aSeesOfB,
    bSeesOfA: bSeesOfA,
  };
}

// Expose for testing
if (typeof window !== 'undefined') {
  window.comparePacketSets = comparePacketSets;
  window.filterPacketsByRoute = filterPacketsByRoute;
  window.computeOverlapStats = computeOverlapStats;
}

(function () {
  var PAYLOAD_LABELS = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
  var MAX_PACKETS = 10000;
  var observers = [];
  var selA = null;
  var selB = null;
  var comparisonResult = null;
  var packetsA = [];
  var packetsB = [];
  var currentView = 'summary';
  var routeFilter = 'all';

  function init(app, routeParam) {
    // Parse preselected observers from URL: #/compare?a=ID1&b=ID2
    var hashParams = location.hash.split('?')[1] || '';
    var params = new URLSearchParams(hashParams);
    selA = params.get('a') || null;
    selB = params.get('b') || null;
    comparisonResult = null;
    packetsA = [];
    packetsB = [];
    currentView = 'summary';
    routeFilter = 'all';

    app.innerHTML = '<div class="compare-page">' +
      '<div class="page-header">' +
        '<a href="#/observers" class="btn-icon" title="Back to Observers" aria-label="Back">\u2190</a>' +
        '<h2>\uD83D\uDD0D Observer Comparison</h2>' +
      '</div>' +
      '<nav data-role="compare-breadcrumbs" aria-label="Compare breadcrumbs" class="compare-breadcrumbs"></nav>' +
      '<div id="compareControls" class="compare-controls"><div class="text-center text-muted" style="padding:20px">Loading observers\u2026</div></div>' +
      '<div id="compareContent"></div>' +
    '</div>';

    // #209 — Keyboard accessibility for compare table rows
    app.addEventListener('keydown', function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      location.hash = row.dataset.value;
    });

    loadObservers();
  }

  function destroy() {
    observers = [];
    selA = null;
    selB = null;
    comparisonResult = null;
    packetsA = [];
    packetsB = [];
    routeFilter = 'all';
  }

  // #1646 round-2 — single shared "should we run a comparison?" predicate
  // used by every auto-run call site so guards cannot drift apart.
  // URL-prepopulated ?a=X&b=X (same observer in both slots) returns false.
  function isComparisonReady() {
    return !!(selA && selB && selA !== selB);
  }

  async function loadObservers() {
    try {
      var data = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = (data.observers || []).sort(function (a, b) {
        return (a.name || a.id).localeCompare(b.name || b.id);
      });
      renderControls();
      if (isComparisonReady()) runComparison();
    } catch (e) {
      document.getElementById('compareControls').innerHTML =
        '<div class="text-muted" style="padding:20px">Error loading observers: ' + escapeHtml(e.message) + '</div>';
    }
  }

  function renderControls() {
    var el = document.getElementById('compareControls');
    if (!el) return;

    var optionsHtml = '<option value="">Select observer\u2026</option>' +
      observers.map(function (o) {
        var label = escapeHtml(o.name || o.id);
        var region = o.iata ? ' (' + escapeHtml(o.iata) + ')' : '';
        return '<option value="' + escapeHtml(o.id) + '">' + label + region + '</option>';
      }).join('');

    el.innerHTML =
      '<div class="compare-selector">' +
        '<div class="compare-select-group">' +
          '<label for="compareObsA">Observer A</label>' +
          '<span class="compare-select-id" aria-hidden="true">A</span>' +
          '<select id="compareObsA" class="compare-select">' + optionsHtml + '</select>' +
        '</div>' +
        '<span class="compare-vs">vs</span>' +
        '<div class="compare-select-group">' +
          '<label for="compareObsB">Observer B</label>' +
          '<span class="compare-select-id" aria-hidden="true">B</span>' +
          '<select id="compareObsB" class="compare-select">' + optionsHtml + '</select>' +
        '</div>' +
        '<div class="compare-select-group">' +
          '<label for="compareRouteFilter">Packet Type</label>' +
          '<select id="compareRouteFilter" class="compare-select">' +
            '<option value="all">All packets</option>' +
            '<option value="flood">Flood only</option>' +
            '<option value="direct">Direct only</option>' +
          '</select>' +
        '</div>' +
      '</div>';

    var ddA = document.getElementById('compareObsA');
    var ddB = document.getElementById('compareObsB');

    if (selA) ddA.value = selA;
    if (selB) ddB.value = selB;

    var ddRoute = document.getElementById('compareRouteFilter');
    ddRoute.value = routeFilter;
    ddRoute.addEventListener('change', function () {
      routeFilter = ddRoute.value;
      if (comparisonResult) runComparison();
    });

    // #1646 — single source of truth for "should we run a comparison?".
    // Called from change handlers and from the initial pre-populated
    // path in loadObservers(). The picker collapse rule (.is-collapsed)
    // is the ONLY DOM hook for state — no parallel data-collapsed attr.
    function updateBtn() {
      selA = ddA.value || null;
      selB = ddB.value || null;
      var wrap = document.getElementById('compareControls');
      var ready = isComparisonReady();
      if (wrap) wrap.classList.toggle('is-collapsed', ready);
      renderBreadcrumbs();
    }
    function onChange() {
      updateBtn();
      // change events only fire when value actually changes, so any
      // ready transition that lands here came from a real user action.
      if (isComparisonReady()) runComparison();
    }
    ddA.addEventListener('change', onChange);
    ddB.addEventListener('change', onChange);
    updateBtn();
  }

  // #1640 — render breadcrumbs linking back to each observer's detail page.
  // Hidden when neither observer is picked; otherwise:
  //   "Observers › <A name> ⇆ <B name>"
  // The "&lrm;" entities keep punctuation LTR in RTL contexts.
  function renderBreadcrumbs() {
    var el = document.querySelector('[data-role="compare-breadcrumbs"]');
    if (!el) return;
    function linkFor(id) {
      if (!id) return null;
      var match = null;
      for (var i = 0; i < observers.length; i++) {
        if (String(observers[i].id) === String(id)) { match = observers[i]; break; }
      }
      var label = match ? (match.name || match.id) : id;
      return '<a href="#/observers/' + encodeURIComponent(id) + '">' + escapeHtml(label) + '</a>';
    }
    var parts = ['<a href="#/observers">Observers</a>'];
    var aLink = linkFor(selA);
    var bLink = linkFor(selB);
    if (aLink || bLink) {
      var pair = [];
      if (aLink) pair.push(aLink);
      if (bLink) pair.push(bLink);
      parts.push(pair.join(' <span aria-hidden="true">\u21C6</span> '));
    }
    el.innerHTML = parts.join(' <span aria-hidden="true">\u203A</span> ');
  }

  function sinceISO(hours) {
    return new Date(Date.now() - hours * 3600000).toISOString();
  }

  async function runComparison() {
    if (!selA || !selB || selA === selB) return;
    var content = document.getElementById('compareContent');
    if (!content) return;

    content.innerHTML = '<div class="text-center text-muted" style="padding:40px">Fetching packets\u2026</div>';

    // Update URL for shareability
    var base = '#/compare?a=' + encodeURIComponent(selA) + '&b=' + encodeURIComponent(selB);
    if (location.hash.split('?')[0] === '#/compare') {
      history.replaceState(null, '', base);
    }

    try {
      var since24h = sinceISO(24);
      var results = await Promise.all([
        api('/packets?observer=' + encodeURIComponent(selA) + '&limit=' + MAX_PACKETS + '&since=' + encodeURIComponent(since24h)),
        api('/packets?observer=' + encodeURIComponent(selB) + '&limit=' + MAX_PACKETS + '&since=' + encodeURIComponent(since24h))
      ]);

      packetsA = results[0].packets || [];
      packetsB = results[1].packets || [];

      // Apply flood/direct filter (#928)
      var filteredA = filterPacketsByRoute(packetsA, routeFilter);
      var filteredB = filterPacketsByRoute(packetsB, routeFilter);

      var hashesA = new Set(filteredA.map(function (p) { return p.hash; }));
      var hashesB = new Set(filteredB.map(function (p) { return p.hash; }));

      comparisonResult = comparePacketSets(hashesA, hashesB);

      // Build hash→packet lookups for detail rendering
      comparisonResult.packetMapA = new Map();
      comparisonResult.packetMapB = new Map();
      filteredA.forEach(function (p) { comparisonResult.packetMapA.set(p.hash, p); });
      filteredB.forEach(function (p) { comparisonResult.packetMapB.set(p.hash, p); });

      currentView = 'summary';
      renderComparison();
    } catch (e) {
      content.innerHTML = '<div class="text-muted" style="padding:40px">Error: ' + escapeHtml(e.message) + '</div>';
    }
  }

  function obsName(id) {
    for (var i = 0; i < observers.length; i++) {
      if (observers[i].id === id) return observers[i].name || id;
    }
    return id ? id.substring(0, 12) : 'Unknown';
  }

  function renderComparison() {
    var content = document.getElementById('compareContent');
    if (!content || !comparisonResult) return;

    var r = comparisonResult;
    var nameA = escapeHtml(obsName(selA));
    var nameB = escapeHtml(obsName(selB));
    var total = r.onlyA.length + r.onlyB.length + r.both.length;
    var pctBoth = total > 0 ? Math.round(r.both.length / total * 100) : 0;
    var pctA = total > 0 ? Math.round(r.onlyA.length / total * 100) : 0;
    var pctB = total > 0 ? Math.round(r.onlyB.length / total * 100) : 0;

    // Type breakdown for "both" packets
    var typeBreakdown = {};
    r.both.forEach(function (h) {
      var p = r.packetMapA.get(h) || r.packetMapB.get(h);
      if (p) {
        var t = p.payload_type;
        typeBreakdown[t] = (typeBreakdown[t] || 0) + 1;
      }
    });

    var typeHtml = Object.keys(typeBreakdown).map(function (t) {
      return '<span class="compare-type-badge">' +
        escapeHtml(PAYLOAD_LABELS[t] || 'Type ' + t) + ' <b>' + typeBreakdown[t] + '</b>' +
      '</span>';
    }).join('');

    var stats = computeOverlapStats(r);

    content.innerHTML =
      '<div class="compare-results">' +
        // Headline strip — A | shared | B above a single proportional bar.
        // All three cells lead with their percentage so the row reads in
        // one unit (Tufte: show data variation, not design variation).
        // #1646
        '<section class="compare-strip" aria-label="Packet overlap summary">' +
          '<div class="compare-strip-row">' +
            '<div class="compare-strip-side" data-view="onlyA" role="button" tabindex="0" aria-label="Show only ' + nameA + ' packets">' +
              '<div class="compare-strip-name">' + nameA + '</div>' +
              '<div class="compare-strip-side-pct">' + pctA + '<span class="compare-strip-side-pct-unit">%</span></div>' +
              '<div class="compare-strip-sub">' + r.onlyA.length.toLocaleString() + ' only here</div>' +
            '</div>' +
            '<div class="compare-strip-mid" data-view="both" role="button" tabindex="0" aria-label="Show shared packets">' +
              '<div class="compare-strip-mid-pct">' + pctBoth + '<span class="compare-strip-mid-pct-unit">%</span></div>' +
              '<div class="compare-strip-mid-count">' + r.both.length.toLocaleString() + '</div>' +
              '<div class="compare-strip-mid-label">of all unique</div>' +
            '</div>' +
            '<div class="compare-strip-side compare-strip-side-b" data-view="onlyB" role="button" tabindex="0" aria-label="Show only ' + nameB + ' packets">' +
              '<div class="compare-strip-name">' + nameB + '</div>' +
              '<div class="compare-strip-side-pct">' + pctB + '<span class="compare-strip-side-pct-unit">%</span></div>' +
              '<div class="compare-strip-sub">' + r.onlyB.length.toLocaleString() + ' only here</div>' +
            '</div>' +
          '</div>' +
          // Single shared-axis diff bar. Width is exact proportion.
          '<div class="compare-bar-container">' +
            '<div class="compare-bar" role="img"' +
                ' aria-label="' + nameA + ' only ' + pctA + '%, both ' + pctBoth + '%, ' + nameB + ' only ' + pctB + '%">' +
              (pctA > 0 ? '<div class="compare-bar-seg compare-bar-a" style="width:' + pctA + '%" title="Only ' + nameA + ': ' + r.onlyA.length + '"></div>' : '') +
              (pctBoth > 0 ? '<div class="compare-bar-seg compare-bar-both" style="width:' + pctBoth + '%" title="Both: ' + r.both.length + '"></div>' : '') +
              (pctB > 0 ? '<div class="compare-bar-seg compare-bar-b" style="width:' + pctB + '%" title="Only ' + nameB + ': ' + r.onlyB.length + '"></div>' : '') +
            '</div>' +
            '<div class="compare-bar-legend">' +
              '<span class="compare-legend-item"><span class="compare-dot compare-dot-a"></span> ' + nameA + ' only</span>' +
              '<span class="compare-legend-item"><span class="compare-dot compare-dot-both"></span> Both</span>' +
              '<span class="compare-legend-item"><span class="compare-dot compare-dot-b"></span> ' + nameB + ' only</span>' +
            '</div>' +
          '</div>' +
        '</section>' +

        // Asymmetric reach — two compact sentences instead of two big cards
        '<section class="compare-asym" aria-label="Directional reach">' +
          '<div class="compare-asym-line">' +
            '<span class="compare-asym-pct">' + stats.aSeesOfB.toFixed(1) + '%</span>' +
            nameA + ' saw <b>' + stats.shared.toLocaleString() + '</b> of ' + nameB +
            '\u2019s <b>' + stats.totalB.toLocaleString() + '</b> packets' +
          '</div>' +
          '<div class="compare-asym-line">' +
            '<span class="compare-asym-pct">' + stats.bSeesOfA.toFixed(1) + '%</span>' +
            nameB + ' saw <b>' + stats.shared.toLocaleString() + '</b> of ' + nameA +
            '\u2019s <b>' + stats.totalA.toLocaleString() + '</b> packets' +
          '</div>' +
        '</section>' +

        // Shared packet types — pills, mono-numeric
        (typeHtml ? '<section class="compare-type-summary" aria-label="Shared packet types">' +
            '<span class="compare-type-summary-label">Shared types</span>' + typeHtml +
          '</section>' : '') +

        // Detail tabs
        '<div class="compare-tabs" role="tablist">' +
          '<button class="tab-btn' + (currentView === 'summary' ? ' active' : '') + '" data-cview="summary" role="tab" aria-controls="compareDetail" aria-selected="' + (currentView === 'summary' ? 'true' : 'false') + '" tabindex="' + (currentView === 'summary' ? '0' : '-1') + '">Summary</button>' +
          '<button class="tab-btn' + (currentView === 'both' ? ' active' : '') + '" data-cview="both" role="tab" aria-controls="compareDetail" aria-selected="' + (currentView === 'both' ? 'true' : 'false') + '" tabindex="' + (currentView === 'both' ? '0' : '-1') + '">Both (' + r.both.length + ')</button>' +
          '<button class="tab-btn' + (currentView === 'onlyA' ? ' active' : '') + '" data-cview="onlyA" role="tab" aria-controls="compareDetail" aria-selected="' + (currentView === 'onlyA' ? 'true' : 'false') + '" tabindex="' + (currentView === 'onlyA' ? '0' : '-1') + '">Only ' + nameA + ' (' + r.onlyA.length + ')</button>' +
          '<button class="tab-btn' + (currentView === 'onlyB' ? ' active' : '') + '" data-cview="onlyB" role="tab" aria-controls="compareDetail" aria-selected="' + (currentView === 'onlyB' ? 'true' : 'false') + '" tabindex="' + (currentView === 'onlyB' ? '0' : '-1') + '">Only ' + nameB + ' (' + r.onlyB.length + ')</button>' +
        '</div>' +
        '<div id="compareDetail"></div>' +
      '</div>';

    // Sync the tablist's active/aria-selected/tabindex to currentView.
    function syncTabState() {
      content.querySelectorAll('.tab-btn').forEach(function (b) {
        var on = b.dataset.cview === currentView;
        b.classList.toggle('active', on);
        b.setAttribute('aria-selected', on ? 'true' : 'false');
        b.setAttribute('tabindex', on ? '0' : '-1');
      });
    }

    // Activate a [data-view] strip segment OR a [data-cview] tab.
    function activate(el) {
      if (!el) return;
      if (el.dataset.cview) {
        currentView = el.dataset.cview;
      } else if (el.dataset.view) {
        currentView = el.dataset.view;
      } else {
        return;
      }
      syncTabState();
      renderDetail();
    }

    // Bind tab clicks + strip clicks
    content.addEventListener('click', function handler(e) {
      var btn = e.target.closest('[data-cview]');
      if (btn) { activate(btn); return; }
      var seg = e.target.closest('[data-view]');
      if (seg) { activate(seg); }
    });

    // Keyboard activation for tabs (arrow nav) + strip segments (Enter/Space).
    content.addEventListener('keydown', function (e) {
      var tab = e.target.closest('[data-cview]');
      if (tab) {
        if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
          e.preventDefault();
          var tabs = Array.prototype.slice.call(content.querySelectorAll('.tab-btn'));
          var idx = tabs.indexOf(tab);
          if (idx < 0) return;
          var next = e.key === 'ArrowRight'
            ? tabs[(idx + 1) % tabs.length]
            : tabs[(idx - 1 + tabs.length) % tabs.length];
          activate(next);
          next.focus();
          return;
        }
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          activate(tab);
          return;
        }
      }
      var seg = e.target.closest('[data-view]');
      if (seg && (e.key === 'Enter' || e.key === ' ')) {
        e.preventDefault();
        activate(seg);
      }
    });

    renderDetail();
  }

  function renderDetail() {
    var el = document.getElementById('compareDetail');
    if (!el || !comparisonResult) return;
    var r = comparisonResult;
    var nameA = escapeHtml(obsName(selA));
    var nameB = escapeHtml(obsName(selB));

    if (currentView === 'summary') {
      // Textual summary — the headline strip + asym lines already cover
      // the quantitative story; this paragraph adds context and surfaces
      // edge cases (no shared packets, perfect overlap).
      var total = r.onlyA.length + r.onlyB.length + r.both.length;
      var overlap = total > 0 ? (r.both.length / total * 100).toFixed(1) : '0.0';
      el.innerHTML =
        '<div class="compare-summary-text">' +
          '<p>In the last 24 hours, <strong>' + nameA + '</strong> saw <strong>' +
            (r.onlyA.length + r.both.length).toLocaleString() + '</strong> unique packets ' +
          'and <strong>' + nameB + '</strong> saw <strong>' +
            (r.onlyB.length + r.both.length).toLocaleString() + '</strong> unique packets. ' +
          '<strong>' + r.both.length.toLocaleString() + '</strong> (' + overlap + '%) were seen by both observers.</p>' +
          (r.both.length === 0 && total > 0 ? '<p class="compare-warning">\u26A0\uFE0F These observers share no packets \u2014 they may be on different frequencies or too far apart.</p>' : '') +
          (r.onlyA.length === 0 && r.onlyB.length === 0 && r.both.length > 0 ? '<p class="compare-good">\u2705 Perfect overlap \u2014 both observers see the same packets.</p>' : '') +
        '</div>';
      return;
    }

    var hashes = r[currentView] || [];
    if (hashes.length === 0) {
      el.innerHTML = '<div class="text-muted" style="padding:20px">No packets in this category.</div>';
      return;
    }

    // Show up to 200 packets in the table
    var displayLimit = 200;
    var displayed = hashes.slice(0, displayLimit);
    var mapA = r.packetMapA;
    var mapB = r.packetMapB;

    el.innerHTML =
      (hashes.length > displayLimit ? '<div class="text-muted" style="margin-bottom:8px">Showing first ' + displayLimit + ' of ' + hashes.length.toLocaleString() + ' packets.</div>' : '') +
      '<div class="analytics-table-scroll"><table class="data-table compare-table">' +
        '<thead><tr>' +
          '<th scope="col">Hash</th><th scope="col">Time</th><th scope="col">Type</th><th scope="col">Observer</th>' +
        '</tr></thead>' +
        '<tbody>' + displayed.map(function (h) {
          var p = mapA.get(h) || mapB.get(h);
          if (!p) return '';
          var typeName = PAYLOAD_LABELS[p.payload_type] || 'Type ' + p.payload_type;
          var obsLabel = '';
          if (currentView === 'both') {
            obsLabel = nameA + ', ' + nameB;
          } else if (currentView === 'onlyA') {
            obsLabel = nameA;
          } else {
            obsLabel = nameB;
          }
          return '<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/packets/' + escapeHtml(h) + '" onclick="location.hash=\'#/packets/' + escapeHtml(h) + '\'">' +
            '<td class="mono" style="font-size:0.85em">' + escapeHtml(h.substring(0, 12)) + '</td>' +
            '<td>' + timeAgo(p.timestamp || p.first_seen) + '</td>' +
            '<td><span class="payload-badge badge-' + payloadTypeColor(p.payload_type) + '">' + escapeHtml(typeName) + '</span></td>' +
            '<td>' + obsLabel + '</td>' +
          '</tr>';
        }).join('') +
        '</tbody>' +
      '</table></div>';
  }

  registerPage('compare', { init: init, destroy: destroy });
})();
