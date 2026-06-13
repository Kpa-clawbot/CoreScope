/* === CoreScope — mqtt-status-panel.js (#1043, mobile cards #1697) ===
 * Small panel that fetches /api/mqtt/status, renders a per-source row
 * with connection state + recent-packet color coding, and auto-refreshes
 * every 10s. Mounted by observers.js into a container element.
 *
 * Layout:
 *   - ≥641px wide viewport — 7-column desktop table (unchanged).
 *   - ≤640px wide viewport — stacked card-per-source (#1697); the
 *     desktop table overflowed at 375px and ran 'connected'/'never'
 *     together. The card layout reflows naturally on phones.
 *
 * The renderer reads window.innerWidth on every render and a debounced
 * resize listener re-renders the panel when the viewport flips across
 * the 640px breakpoint.
 *
 * Color-coding (both layouts):
 *   - green:  connected AND a packet seen in the last 5 minutes
 *   - yellow: connected but no recent packets (broker quiet or stalled)
 *   - red:    disconnected
 *
 * Exposed as window.MqttStatusPanel for testability and so the Observers
 * page can mount it without an import system.
 */
'use strict';

(function () {
  var REFRESH_MS = 10000;
  var RECENT_PACKET_MS = 5 * 60 * 1000;
  var MOBILE_MAX_PX = 640;          // #1697 — ≤640px gets card layout
  var RESIZE_DEBOUNCE_MS = 150;     // #1697 — resize flip debounce

  function fmtRelative(unixSec, now) {
    if (!unixSec) return 'never';
    var ms = (now || Date.now()) - unixSec * 1000;
    if (ms < 0) ms = 0;
    if (ms < 60000) return Math.floor(ms / 1000) + 's ago';
    if (ms < 3600000) return Math.floor(ms / 60000) + 'm ago';
    if (ms < 86400000) return Math.floor(ms / 3600000) + 'h ago';
    return Math.floor(ms / 86400000) + 'd ago';
  }

  // classifySource returns 'green' | 'yellow' | 'red' for a source row.
  // Exposed for unit testing.
  function classifySource(src, now) {
    if (!src || !src.connected) return 'red';
    var lastMs = (src.lastPacketUnix || 0) * 1000;
    var ageMs = (now || Date.now()) - lastMs;
    if (src.lastPacketUnix && ageMs <= RECENT_PACKET_MS) return 'green';
    return 'yellow';
  }

  // escapeHTML keeps masked-but-still-attacker-controllable broker strings
  // safe in innerHTML. The server already redacts passwords; this defends
  // against a hostname containing < or & breaking the panel.
  function escapeHTML(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function dotColor(state) {
    switch (state) {
      case 'green':  return 'var(--status-green)';
      case 'yellow': return 'var(--status-yellow)';
      default:       return 'var(--status-red)';
    }
  }

  function dotSpan(state) {
    return '<span class="mqtt-status-dot" aria-hidden="true" '
      + 'style="display:inline-block;width:10px;height:10px;border-radius:50%;'
      + 'background:' + dotColor(state) + ';margin-right:6px;flex:0 0 auto"></span>';
  }

  // renderTable — the original desktop 7-column layout (#1043). Kept
  // unchanged behavior; pulled into a helper so renderPanel can pick.
  function renderTable(sources, now) {
    var rows = sources.map(function (s) {
      var state = classifySource(s, now);
      return ''
        + '<tr data-source-name="' + escapeHTML(s.name) + '" data-state="' + state + '">'
        + '<td>' + dotSpan(state)
        +   '<strong>' + escapeHTML(s.name) + '</strong></td>'
        + '<td><code style="font-size:12px">' + escapeHTML(s.broker) + '</code></td>'
        + '<td>' + (s.connected ? 'connected' : 'disconnected') + '</td>'
        + '<td>' + fmtRelative(s.lastPacketUnix, now) + '</td>'
        + '<td style="text-align:right">' + (s.packetsLast5m || 0) + '</td>'
        + '<td style="text-align:right">' + (s.packetsTotal || 0) + '</td>'
        + '<td style="text-align:right">' + (s.disconnectCount || 0) + '</td>'
        + '</tr>';
    }).join('');
    return ''
      + '<table class="mqtt-status-table" style="width:100%;font-size:var(--fs-sm);border-collapse:collapse">'
      + '<thead><tr style="text-align:left">'
      +   '<th style="padding:4px 8px">Source</th>'
      +   '<th style="padding:4px 8px">Broker</th>'
      +   '<th style="padding:4px 8px">State</th>'
      +   '<th style="padding:4px 8px">Last packet</th>'
      +   '<th style="padding:4px 8px;text-align:right">5m</th>'
      +   '<th style="padding:4px 8px;text-align:right">Total</th>'
      +   '<th style="padding:4px 8px;text-align:right">Disc.</th>'
      + '</tr></thead>'
      + '<tbody>' + rows + '</tbody>'
      + '</table>';
  }

  // renderCards — mobile-first stacked card layout (#1697). One card per
  // source. Three rows per card: header (dot + name + state + last),
  // broker (full-width <code>), counters (5m / Total / Disc.).
  // All colors via M2 palette tokens; all type via M3 (--fs-sm /
  // --fw-medium). No inline hex, no fixed widths.
  function renderCards(sources, now) {
    return sources.map(function (s) {
      var state = classifySource(s, now);
      var stateLabel = s.connected ? 'connected' : 'disconnected';
      var stateColorVar = (state === 'green' || state === 'yellow')
        ? 'var(--status-green)' : 'var(--status-red)';
      return ''
        + '<div class="mqtt-status-card" data-source-name="' + escapeHTML(s.name) + '" '
        +   'data-state="' + state + '" '
        +   'style="display:flex;flex-direction:column;gap:4px;padding:8px 10px;'
        +   'margin:6px 0;border:1px solid var(--border);border-radius:6px;'
        +   'background:var(--card-bg);font-size:var(--fs-sm)">'

        // Row 1: dot + name (bold, larger) + state + last-packet, right-aligned
        + '<div style="display:flex;align-items:center;flex-wrap:wrap;gap:6px 10px">'
        +   dotSpan(state)
        +   '<strong style="font-size:calc(var(--fs-sm) + 1px);font-weight:var(--fw-medium);flex:1 1 auto;min-width:0;'
        +     'overflow:hidden;text-overflow:ellipsis;white-space:nowrap">'
        +     escapeHTML(s.name) + '</strong>'
        +   '<span data-state-label style="color:' + stateColorVar + ';font-weight:var(--fw-medium);white-space:nowrap">'
        +     stateLabel + '</span>'
        +   '<span data-last-packet class="text-muted" '
        +     'style="color:var(--text-muted);white-space:nowrap">'
        +     fmtRelative(s.lastPacketUnix, now) + '</span>'
        + '</div>'

        // Row 2: broker URL (masked by server) — full width, wraps on overflow
        + '<div><code style="font-size:12px;color:var(--text-muted);'
        +   'word-break:break-all">' + escapeHTML(s.broker) + '</code></div>'

        // Row 3: counters — label/value pairs, wrap on very narrow viewports
        + '<div style="display:flex;flex-wrap:wrap;gap:4px 14px;color:var(--text-muted);font-size:var(--fs-sm)">'
        +   '<span><span class="text-muted">5m:</span> '
        +     '<strong style="color:var(--text-primary, inherit);font-weight:var(--fw-medium)">'
        +     (s.packetsLast5m || 0) + '</strong></span>'
        +   '<span><span class="text-muted">Total:</span> '
        +     '<strong style="color:var(--text-primary, inherit);font-weight:var(--fw-medium)">'
        +     (s.packetsTotal || 0) + '</strong></span>'
        +   '<span><span class="text-muted">Disc:</span> '
        +     '<strong style="color:var(--text-primary, inherit);font-weight:var(--fw-medium)">'
        +     (s.disconnectCount || 0) + '</strong></span>'
        + '</div>'

        + '</div>';
    }).join('');
  }

  function getViewportWidth() {
    if (typeof window === 'undefined') return 1200;
    return window.innerWidth || 1200;
  }

  // Track active panel containers so the global resize listener can
  // re-render them when the viewport flips across the breakpoint.
  // The Map is keyed by container; value is the most recent payload.
  var _active = (typeof Map === 'function') ? new Map() : null;
  var _resizeBound = false;
  var _resizeTimer = null;
  var _lastBucket = null; // 'mobile' | 'desktop'

  function currentBucket() { return getViewportWidth() <= MOBILE_MAX_PX ? 'mobile' : 'desktop'; }

  function bindResizeOnce() {
    if (_resizeBound || typeof window === 'undefined' || !window.addEventListener) return;
    _resizeBound = true;
    _lastBucket = currentBucket();
    window.addEventListener('resize', function () {
      if (_resizeTimer) clearTimeout(_resizeTimer);
      _resizeTimer = setTimeout(function () {
        var b = currentBucket();
        if (b === _lastBucket) return; // only re-render on bucket flip
        _lastBucket = b;
        if (!_active) return;
        _active.forEach(function (payload, container) {
          renderPanel(container, payload, Date.now());
        });
      }, RESIZE_DEBOUNCE_MS);
    });
  }

  function renderPanel(container, payload, now) {
    if (!container) return;
    var sources = (payload && payload.sources) || [];

    // Remember the latest payload for resize-driven re-render.
    if (_active && payload) _active.set(container, payload);
    bindResizeOnce();

    if (sources.length === 0) {
      container.innerHTML = '<div class="mqtt-status-empty text-muted" '
        + 'style="padding:8px 0;font-size:var(--fs-sm);color:var(--text-muted)">'
        + 'No MQTT sources reported yet. The ingestor publishes status '
        + 'every second; if this persists check the ingestor logs.</div>';
      return;
    }

    var body = (getViewportWidth() <= MOBILE_MAX_PX)
      ? renderCards(sources, now)
      : renderTable(sources, now);

    container.innerHTML = ''
      + '<div class="mqtt-status-panel" style="margin:12px 0">'
      +   '<h3 style="margin:0 0 6px 0;font-size:14px;font-weight:var(--fw-medium)">MQTT sources</h3>'
      +   body
      + '</div>';
  }

  // mount attaches the panel into `container` and starts auto-refresh.
  // Returns a teardown function the caller can invoke on page unmount.
  // The optional `opts.fetchImpl` lets tests inject a fake fetch.
  function mount(container, opts) {
    opts = opts || {};
    var fetchImpl = opts.fetchImpl || (typeof window !== 'undefined' && window.fetch ? window.fetch.bind(window) : null);
    if (!fetchImpl) return function noop() {};
    var stopped = false;

    function tick() {
      if (stopped) return;
      Promise.resolve()
        .then(function () { return fetchImpl('/api/mqtt/status'); })
        .then(function (r) { return r && r.json ? r.json() : r; })
        .then(function (payload) {
          if (stopped) return;
          renderPanel(container, payload, Date.now());
        })
        .catch(function () { /* keep last-rendered state on transient failures */ });
    }

    tick();
    var timer = setInterval(tick, opts.intervalMs || REFRESH_MS);
    return function teardown() {
      stopped = true;
      clearInterval(timer);
      if (_active) _active.delete(container);
    };
  }

  var api = {
    mount: mount,
    renderPanel: renderPanel,
    renderTable: renderTable,
    renderCards: renderCards,
    classifySource: classifySource,
    fmtRelative: fmtRelative,
    REFRESH_MS: REFRESH_MS,
    RECENT_PACKET_MS: RECENT_PACKET_MS,
    MOBILE_MAX_PX: MOBILE_MAX_PX
  };

  if (typeof window !== 'undefined') window.MqttStatusPanel = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})();
