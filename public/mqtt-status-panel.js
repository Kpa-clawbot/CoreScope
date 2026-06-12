/* === CoreScope — mqtt-status-panel.js (#1043) ===
 * Small panel that fetches /api/mqtt/status, renders a per-source row
 * with connection state + recent-packet color coding, and auto-refreshes
 * every 10s. Mounted by observers.js into a container element.
 *
 * Color-coding:
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

  function renderPanel(container, payload, now) {
    if (!container) return;
    var sources = (payload && payload.sources) || [];
    if (sources.length === 0) {
      container.innerHTML = '<div class="mqtt-status-empty text-muted" '
        + 'style="padding:8px 0;font-size:13px">'
        + 'No MQTT sources reported yet. The ingestor publishes status '
        + 'every second; if this persists check the ingestor logs.</div>';
      return;
    }
    var rows = sources.map(function (s) {
      var state = classifySource(s, now);
      var dot;
      switch (state) {
        case 'green': dot = 'var(--status-green)'; break;
        case 'yellow': dot = 'var(--status-yellow)'; break;
        default: dot = 'var(--status-red)';
      }
      return ''
        + '<tr data-source-name="' + escapeHTML(s.name) + '" data-state="' + state + '">'
        + '<td><span class="mqtt-status-dot" aria-hidden="true" '
        +   'style="display:inline-block;width:10px;height:10px;border-radius:50%;'
        +   'background:' + dot + ';margin-right:6px"></span>'
        +   '<strong>' + escapeHTML(s.name) + '</strong></td>'
        + '<td><code style="font-size:12px">' + escapeHTML(s.broker) + '</code></td>'
        + '<td>' + (s.connected ? 'connected' : 'disconnected') + '</td>'
        + '<td>' + fmtRelative(s.lastPacketUnix, now) + '</td>'
        + '<td style="text-align:right">' + (s.packetsLast5m || 0) + '</td>'
        + '<td style="text-align:right">' + (s.packetsTotal || 0) + '</td>'
        + '<td style="text-align:right">' + (s.disconnectCount || 0) + '</td>'
        + '</tr>';
    }).join('');
    container.innerHTML = ''
      + '<div class="mqtt-status-panel" style="margin:12px 0">'
      + '<h3 style="margin:0 0 6px 0;font-size:14px">MQTT sources</h3>'
      + '<table class="mqtt-status-table" style="width:100%;font-size:13px;border-collapse:collapse">'
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
      + '</table></div>';
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
    };
  }

  var api = {
    mount: mount,
    renderPanel: renderPanel,
    classifySource: classifySource,
    fmtRelative: fmtRelative,
    REFRESH_MS: REFRESH_MS,
    RECENT_PACKET_MS: RECENT_PACKET_MS
  };

  if (typeof window !== 'undefined') window.MqttStatusPanel = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})();
