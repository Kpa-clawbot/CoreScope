/* #661 — rolling 1 h TX airtime share per observer, shared by the RF Health
 * tiles (analytics.js) and the observer list (observers.js). The value and
 * the warning threshold come from /api/observers/metrics/summary
 * (tx_airtime_pct_1h, duty_cycle_warn_pct). */
window.ObserverAirtime = (function () {
  'use strict';
  var DEFAULT_WARN_PCT = 10;

  function warnPct(w) {
    return w > 0 ? w : DEFAULT_WARN_PCT;
  }

  function format(pct) {
    if (pct == null) return '—';
    var n = Number(pct);
    if (n > 0 && n < 0.1) return '<0.1%';
    return n.toFixed(1) + '%';
  }

  // Compared at the one decimal format() shows, so "10.0%" is never unflagged.
  function isOver(pct, w) {
    return pct != null && Math.round(Number(pct) * 10) / 10 >= warnPct(w);
  }

  function badge(pct, w) {
    if (pct == null) {
      return '<span class="text-muted" title="Under 30 minutes of airtime samples in the last hour, or none in the last 15 minutes">—</span>';
    }
    var over = isOver(pct, w);
    var title = 'TX airtime over the last hour. Duty-cycle warning at ' + warnPct(w) + '%';
    return '<span class="airtime-pct' + (over ? ' airtime-over' : '') + '" title="' + title + '">' +
      format(pct).replace('<', '&lt;') + '</span>';
  }

  return { format: format, isOver: isOver, badge: badge };
})();
