/* === CoreScope — observers.js === */
'use strict';

// Issue #1478 — naive-clock ⚠️ chip.
// Exposed as a window global so the renderer (and a jsdom-style test) can
// call it without depending on the IIFE-scoped helpers below. Returns the
// chip HTML when the observer's clock is currently flagged naive, or "" when
// it's clean — never throws.
window.ObserversNaiveChip = {
  render: function (o) {
    if (!o || o.clock_naive !== true) return '';
    var sec = Number(o.clock_skew_seconds || 0);
    var absSec = Math.abs(sec);
    var magnitude;
    if (absSec >= 3600) {
      magnitude = (absSec / 3600).toFixed(absSec >= 36000 ? 0 : 1) + 'h';
    } else if (absSec >= 60) {
      magnitude = Math.round(absSec / 60) + 'm';
    } else {
      magnitude = absSec + 's';
    }
    var dir = sec < 0 ? 'behind' : 'ahead of';
    var count = Number(o.clock_skew_count_24h || 0);
    var tip = 'Observer\u2019s local clock is ' + magnitude + ' ' + dir
      + ' UTC — per-packet timing for this observer is being clamped to ingest time'
      + (count > 0 ? ' (' + count + ' events in last 24h)' : '')
      + '. Fix: set the host clock to UTC, or emit Z-suffixed/offset-aware timestamps.';
    return '<span class="obs-clock-naive-chip" title="' + tip.replace(/"/g, '&quot;')
      + '" aria-label="' + tip.replace(/"/g, '&quot;')
      + '" style="margin-left:6px;cursor:help">\u26A0\uFE0F</span>';
  },
};

// #1562 — Pure helper for computing + rendering the observers-page aggregate
// header. Split out as a window global so it's unit-testable in a vm sandbox
// without needing a DOM, AND so the render path has one obvious place to wire
// the "Last updated: X ago" freshness label.
//
// Why this exists (per #1562): the header used to derive Online/Stale/Offline
// counts inline from a possibly-stale cached /api/observers payload, with no
// UI signal that the data could be stale. After #1551 added Cache-Control:
// no-store on the server response, the in-memory client cache (api() ttl)
// could still serve old data. The "Last updated" label makes that visible;
// manual refresh now also bypasses the cache (bust: true).
window.ObserversSummary = (function () {
  // #1563 — Single source of truth: aggregate counts MUST come from the
  // same classifier used to render the per-row dots. Previously this helper
  // had its own hardcoded thresholds parallel to healthStatus(); operators
  // would see "5 Online" in the header but count 12 green rows by hand
  // (regression of #1562). We now delegate to window.observerHealthStatus
  // (exposed below by the IIFE) and map its returned .cls to the bucket.
  //
  // A default fallback classifier is kept for the case where this module
  // is loaded BEFORE observers.js wires up window.observerHealthStatus
  // (e.g. legacy test paths). It mirrors the canonical thresholds, but
  // production code paths always hit window.observerHealthStatus.
  function defaultClassify(lastSeen) {
    if (!lastSeen) return { cls: 'health-red' };
    var ago = Date.now() - new Date(lastSeen).getTime();
    var tolerance = 30000;
    if (ago < 600000 + tolerance) return { cls: 'health-green' };
    if (ago < 3600000 + tolerance) return { cls: 'health-yellow' };
    return { cls: 'health-red' };
  }

  function computeCounts(observers) {
    var online = 0, stale = 0, offline = 0;
    var list = Array.isArray(observers) ? observers : [];
    var classifier = (typeof window !== 'undefined' && typeof window.observerHealthStatus === 'function')
      ? window.observerHealthStatus
      : defaultClassify;
    for (var i = 0; i < list.length; i++) {
      var h = classifier(list[i] && list[i].last_seen) || { cls: 'health-red' };
      if (h.cls === 'health-green') online++;
      else if (h.cls === 'health-yellow') stale++;
      else offline++;
    }
    return { online: online, stale: stale, offline: offline, total: list.length };
  }

  // Renders the obs-summary block as a string. fetchedAt is a ms epoch
  // timestamp (or null/0 for "unknown" — first render before any successful
  // fetch). When fetchedAt is older than 60s, the timestamp gets the
  // obs-updated-stale class so operators see the data is going stale.
  function renderHeader(counts, fetchedAt) {
    var c = counts || { online: 0, stale: 0, offline: 0, total: 0 };
    var updatedHtml = '';
    if (fetchedAt) {
      var ageMs = Date.now() - fetchedAt;
      var staleCls = ageMs > 60000 ? ' obs-updated-stale' : '';
      var iso = new Date(fetchedAt).toISOString();
      updatedHtml = '<span class="obs-stat obs-updated' + staleCls
        + '" data-action="obs-refresh" role="button" tabindex="0"'
        + ' title="Click to force a fresh fetch (bypass client cache)"'
        + ' aria-label="Last updated ' + timeAgo(iso) + ' — click to refresh">'
        + 'Last updated: ' + timeAgo(iso) + '</span>';
    }
    return ''
      + '<div class="obs-summary">'
      +   '<span class="obs-stat"><span class="health-dot health-green">\u25CF</span> ' + c.online + ' Online</span>'
      +   '<span class="obs-stat"><span class="health-dot health-yellow">\u25B2</span> ' + c.stale + ' Stale</span>'
      +   '<span class="obs-stat"><span class="health-dot health-red">\u2715</span> ' + c.offline + ' Offline</span>'
      +   '<span class="obs-stat">\uD83D\uDCE1 ' + c.total + ' Total</span>'
      +   updatedHtml
      + '</div>';
  }

  return { computeCounts: computeCounts, renderHeader: renderHeader };
})();

(function () {
  let observers = [];
  let _fetchedAt = 0; // #1562: ms epoch when the current `observers` payload was received
  let _loadObserversReqId = 0; // #1563: monotonic id; resolutions older than the latest are discarded
  let obsSkewMap = {}; // observerID → {offsetSec, samples}
  let wsHandler = null;
  let refreshTimer = null;
  let regionChangeHandler = null;

  function init(app) {
    app.innerHTML = `
      <div class="observers-page">
        <div class="page-header">
          <h2>Observer Status</h2>
          <a href="#/compare" class="btn-icon" title="Compare observers" aria-label="Compare observers" style="text-decoration:none">🔍</a>
          <button class="btn-icon" data-action="obs-refresh" title="Refresh" aria-label="Refresh observers">🔄</button>
        </div>
        <div id="obsRegionFilter" class="region-filter-container"></div>
        <div id="obsContent"><div class="text-center text-muted" style="padding:40px">Loading…</div></div>
      </div>`;
    RegionFilter.init(document.getElementById('obsRegionFilter'));
    regionChangeHandler = RegionFilter.onChange(function () { render(); });
    loadObservers();
    // Event delegation for data-action buttons
    app.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-action]');
      if (btn && btn.dataset.action === 'obs-refresh') loadObservers({ bust: true });
      var row = e.target.closest('tr[data-action="navigate"]');
      if (row) {
        // #1056 AC#4: at narrow widths, open detail in slide-over instead of
        // navigating to a separate page.
        if (window.SlideOver && window.SlideOver.shouldUse()) {
          e.preventDefault();
          openObserverSlideOver(row.dataset.value);
          return;
        }
        location.hash = row.dataset.value;
      }
    });
    // #209 — Keyboard accessibility for observer rows
    app.addEventListener('keydown', function (e) {
      // #1562 — Last-updated pill (role=button) supports Enter/Space to
      // force a fresh fetch, matching click behavior on data-action="obs-refresh".
      var refreshBtn = e.target.closest('[data-action="obs-refresh"]');
      if (refreshBtn && (e.key === 'Enter' || e.key === ' ')) {
        e.preventDefault();
        loadObservers({ bust: true });
        return;
      }
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      if (window.SlideOver && window.SlideOver.shouldUse()) {
        openObserverSlideOver(row.dataset.value);
        return;
      }
      location.hash = row.dataset.value;
    });
    // Auto-refresh every 30s
    refreshTimer = setInterval(loadObservers, 30000);
    wsHandler = debouncedOnWS(function (msgs) {
      if (msgs.some(function (m) { return m.type === 'packet'; })) loadObservers();
    });
  }

  function destroy() {
    if (wsHandler) offWS(wsHandler);
    wsHandler = null;
    if (refreshTimer) clearInterval(refreshTimer);
    refreshTimer = null;
    if (regionChangeHandler) RegionFilter.offChange(regionChangeHandler);
    regionChangeHandler = null;
    observers = [];
    obsSkewMap = {};
  }

  async function loadObservers(opts) {
    var bust = !!(opts && opts.bust);
    // #1563 — in-flight guard: every call gets a monotonic id; when we
    // resolve, if a newer call has started, drop this result silently.
    // Prevents a slow auto-refresh from clobbering a fresh manual bust
    // (or vice versa) with stale data + a misleading "0s ago" pill.
    var myId = ++_loadObserversReqId;
    try {
      const [data, skewData] = await Promise.all([
        api('/observers', { ttl: CLIENT_TTL.observers, bust: bust }),
        api('/observers/clock-skew', { ttl: 30000 }).catch(function() { return []; })
      ]);
      if (myId !== _loadObserversReqId) return; // stale resolve, newer in-flight
      observers = data.observers || [];
      _fetchedAt = Date.now(); // #1562: stamp freshness for the header label
      obsSkewMap = {};
      (Array.isArray(skewData) ? skewData : []).forEach(function(s) {
        if (s && s.observerID) obsSkewMap[s.observerID] = s;
      });
      render();
    } catch (e) {
      if (myId !== _loadObserversReqId) return; // discard stale error too
      document.getElementById('obsContent').innerHTML =
        `<div class="text-muted" role="alert" aria-live="polite" style="padding:40px">Error loading observers: ${e.message}</div>`;
    }
  }

  // NOTE: Comparing server timestamps to Date.now() can skew if client/server
  // clocks differ. We add ±30s tolerance to thresholds to reduce false positives.
  function healthStatus(lastSeen) {
    if (!lastSeen) return { cls: 'health-red', label: 'Unknown' };
    const ago = Date.now() - new Date(lastSeen).getTime();
    const tolerance = 30000; // 30s tolerance for clock skew
    // Issue #1552 — thresholds are operator-configurable via config.json
    // healthThresholds.observerOnlineMinutes / observerStaleMinutes, surfaced
    // to the client through window.HEALTH_THRESHOLDS. Defaults are 60 min
    // Online / 1440 min (24h) Stale, matching node thresholds (#1552).
    const th = (typeof window !== 'undefined' && window.HEALTH_THRESHOLDS) || {};
    const onlineMs = th.observerOnlineMs || 3600000;
    const staleMs = th.observerStaleMs || 86400000;
    if (ago < onlineMs + tolerance) return { cls: 'health-green', label: 'Online' };
    if (ago < staleMs + tolerance) return { cls: 'health-yellow', label: 'Stale' };
    return { cls: 'health-red', label: 'Offline' };
  }
  // Issue #1552 — exposed for tests and external callers.
  // #1563 — Expose for ObserversSummary so aggregate counts and per-row dots
  // share ONE classifier (single source of truth). If anything reintroduces
  // parallel thresholds, the new ObserversSummary regression test breaks.
  window.observerHealthStatus = healthStatus;

  function packetBadge(o) {
    if (!o.last_packet_at) return '<span title="No packets ever observed">📡⚠ never</span>';
    const pktAgo = Date.now() - new Date(o.last_packet_at).getTime();
    const statusAgo = o.last_seen ? Date.now() - new Date(o.last_seen).getTime() : Infinity;
    const gap = pktAgo - statusAgo;
    if (gap > 600000) {
      return `<span title="Last packet ${timeAgo(o.last_packet_at)} — status is newer by ${Math.round(gap/60000)}min. Observer may be alive but not forwarding packets.">📡⚠ ${timeAgo(o.last_packet_at)}</span>`;
    }
    return timeAgo(o.last_packet_at);
  }

  function uptimeStr(firstSeen) {
    if (!firstSeen) return '—';
    const ms = Date.now() - new Date(firstSeen).getTime();
    const d = Math.floor(ms / 86400000);
    const h = Math.floor((ms % 86400000) / 3600000);
    if (d > 0) return `${d}d ${h}h`;
    const m = Math.floor((ms % 3600000) / 60000);
    return h > 0 ? `${h}h ${m}m` : `${m}m`;
  }

  function sparkBar(count, max) {
    if (max === 0) return `<span class="text-muted">0/hr</span>`;
    const pct = Math.min(100, Math.round((count / max) * 100));
    return `<span style="display:inline-flex;align-items:center;gap:6px;white-space:nowrap"><span style="display:inline-block;width:60px;height:12px;background:var(--border);border-radius:3px;overflow:hidden;vertical-align:middle"><span style="display:block;height:100%;width:${pct}%;background:linear-gradient(90deg,#3b82f6,#60a5fa);border-radius:3px"></span></span><span style="font-size:11px">${count}/hr</span></span>`;
  }

  function render() {
    const el = document.getElementById('obsContent');
    if (!el) return;

    // Apply region filter
    const selectedRegions = RegionFilter.getSelected();
    const filtered = selectedRegions
      ? observers.filter(o => o.iata && selectedRegions.includes(o.iata))
      : observers;

    if (filtered.length === 0) {
      el.innerHTML = '<div class="text-center text-muted" style="padding:40px">No observers found.</div>';
      return;
    }

    const maxPktsHr = Math.max(1, ...filtered.map(o => o.packetsLastHour || 0));

    // #1562 — Aggregate counts + "Last updated" freshness label come from the
    // pure ObserversSummary helper (unit-tested in test-issue-1562-*).
    const summaryCounts = window.ObserversSummary.computeCounts(filtered);
    const summaryHtml = window.ObserversSummary.renderHeader(summaryCounts, _fetchedAt);

    el.innerHTML = `
      ${summaryHtml}
      <div class="obs-table-scroll table-fluid-wrap"><table class="data-table obs-table" id="obsTable">
        <caption class="sr-only">Observer status and statistics</caption>
        <thead><tr>
          <th scope="col" data-priority="1">Status</th><th scope="col" data-priority="1">Name</th><th scope="col" data-priority="3">Region</th><th scope="col" data-priority="2">Last Status</th><th scope="col" data-priority="2">Last Packet</th>
          <th scope="col" data-priority="3">Packet Health</th><th scope="col" data-priority="4">Total Packets</th><th scope="col" data-priority="3">Packets/Hour</th><th scope="col" data-priority="4">Clock Offset</th><th scope="col" data-priority="4">Uptime</th>
        </tr></thead>
        <tbody>${filtered.map(o => {
          const h = healthStatus(o.last_seen);
          const shape = h.cls === 'health-green' ? '●' : h.cls === 'health-yellow' ? '▲' : '✕';
          return `<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/observers/${encodeURIComponent(o.id)}" onclick="location.hash='#/observers/${encodeURIComponent(o.id)}'">
            <td><span class="health-dot ${h.cls}" title="${h.label}">${shape}</span> ${h.label}</td>
            <td class="mono">${escapeHtml(o.name || o.id)}${window.ObserversNaiveChip.render(o)}${o.can_relay === false ? ' <span class="badge-listener" title="Firmware reported repeat:off — listener-only; excluded from path-hop disambiguator (issue #1290)">listener</span>' : (o.can_relay === true ? ' <span class="badge-repeater" title="Firmware reported repeat:on — eligible as a path hop">repeater</span>' : '')}</td>
            <td>${o.iata ? `<span class="badge-region">${o.iata}</span>` : '—'}</td>
            <td>${timeAgo(o.last_seen)}</td>
            <td>${o.last_packet_at ? timeAgo(o.last_packet_at) : '<span class="text-muted">—</span>'}</td>
            <td>${packetBadge(o)}</td>
            <td>${(o.packet_count || 0).toLocaleString()}</td>
            <td>${sparkBar(o.packetsLastHour || 0, maxPktsHr)}</td>
            <td>${(function() {
              var sk = obsSkewMap[o.id];
              if (!sk || sk.samples == null || sk.samples === 0) return '<span class="text-muted">—</span>';
              var sev = observerSkewSeverity(sk.offsetSec);
              return renderSkewBadge(sev, sk.offsetSec) + ' <span class="text-muted" title="Computed from ' + sk.samples + ' multi-observer packets. Positive = observer ahead of consensus.">(' + sk.samples + ')</span>';
            })()}</td>
            <td>${uptimeStr(o.first_seen)}</td>
          </tr>`;
        }).join('')}</tbody>
      </table></div>`;
    makeColumnsResizable('#obsTable', 'meshcore-obs-col-widths');
    // #1056: fluid columns + +N hidden pill
    if (window.TableResponsive) {
      var _obsTbl = document.getElementById('obsTable');
      if (_obsTbl) window.TableResponsive.register(_obsTbl);
    }
  }


  registerPage('observers', { init, destroy });

  // #1056 AC#4: row-detail slide-over (narrow viewports). Renders a compact
  // summary from the in-memory observer + a link to the full page.
  function openObserverSlideOver(hashHref) {
    if (!window.SlideOver) return;
    var m = String(hashHref || '').match(/#\/observers\/(.+)$/);
    if (!m) return;
    var id = decodeURIComponent(m[1]);
    var o = (observers || []).find(function (x) { return String(x.id) === id; });
    if (!o) return;
    var h = healthStatus(o.last_seen);
    var sk = obsSkewMap[o.id];
    var skewLine = (sk && sk.samples) ? renderSkewBadge(observerSkewSeverity(sk.offsetSec), sk.offsetSec) + ' (' + sk.samples + ' samples)' : '—';
    var pkts = sparkBar(o.packetsLastHour || 0, Math.max(1, o.packetsLastHour || 1));
    var content = window.SlideOver.open({ title: (o.name || o.id) });
    var naiveChipHTML = window.ObserversNaiveChip.render(o);
    content.innerHTML =
      (naiveChipHTML ? '<div style="margin-bottom:10px">' + naiveChipHTML + ' <span class="text-muted">Clock is naive — per-packet timing clamped to ingest time.</span></div>' : '') +
      '<dl class="slide-over-dl" style="margin:0;display:grid;grid-template-columns:auto 1fr;gap:6px 12px;font-size:13px">' +
        '<dt>Status</dt><dd><span class="health-dot ' + h.cls + '">●</span> ' + h.label + '</dd>' +
        '<dt>Region</dt><dd>' + (o.iata ? '<span class="badge-region">' + o.iata + '</span>' : '—') + '</dd>' +
        '<dt>Last status</dt><dd>' + timeAgo(o.last_seen) + '</dd>' +
        '<dt>Last packet</dt><dd>' + (o.last_packet_at ? timeAgo(o.last_packet_at) : '—') + '</dd>' +
        '<dt>Total packets</dt><dd>' + (o.packet_count || 0).toLocaleString() + '</dd>' +
        '<dt>Packets/hr</dt><dd>' + pkts + '</dd>' +
        '<dt>Clock offset</dt><dd>' + skewLine + '</dd>' +
        '<dt>Uptime</dt><dd>' + uptimeStr(o.first_seen) + '</dd>' +
      '</dl>' +
      '<p style="margin-top:14px"><a class="btn-primary" href="' + hashHref + '">Open full detail →</a></p>';
  }
})();
