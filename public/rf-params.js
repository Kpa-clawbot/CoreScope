/* #1268 — LoRa RF parameters per node, from /api/rf-params (the MeshCore
 * map's view of each node's radio settings), rendered in the node detail
 * views of nodes.js. Pure helpers; no fetching here. */
window.RfParams = (function () {
  'use strict';

  function esc(s) {
    // All five OWASP characters, like app.js escapeHtml: radioText (#1869)
    // renders observer-published strings.
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function preset(p, presets) {
    if (!p || !presets) return null;
    for (var i = 0; i < presets.length; i++) {
      var q = presets[i];
      if (Math.abs(q.freq - p.freq) < 0.001 && q.sf === p.sf && Math.abs(q.bw - p.bw) < 0.01 && q.cr === p.cr) return q;
    }
    return null;
  }

  function ago(ms) {
    var h = Math.floor(ms / 3600000);
    if (h < 1) return 'under 1h ago';
    if (h < 24) return h + 'h ago';
    return Math.floor(h / 24) + 'd ago';
  }

  function settingText(s) {
    return Number(s.freq).toFixed(3) + ' MHz · SF' + s.sf + ' · BW ' + s.bw + ' kHz';
  }

  function deviationTitle(dev) {
    return dev.agree + ' of ' + dev.peers + ' nearby nodes on this band use ' + settingText(dev.majority);
  }

  // deviationBadge marks a node whose frequency, SF or bandwidth differs
  // from most same-band nodes around it (dev from /api/rf-params
  // deviations); the badge names the node's own differing values.
  function deviationBadge(p, dev) {
    if (!p || !dev) return '';
    var parts = (dev.fields || []).map(function (f) {
      if (f === 'freq') return Number(p.freq).toFixed(3);
      if (f === 'sf') return 'SF' + p.sf;
      return 'BW' + p.bw;
    });
    return ' <span class="badge-lora-diff" title="' + esc('LoRa settings differ: ' + deviationTitle(dev)) + '">LoRa ' + esc(parts.join(' ')) + '</span>';
  }

  function detailHtml(p, presets, nowMs, dev) {
    if (!p) {
      return '<span class="text-muted" title="Not on the MeshCore map, or no advert there within the configured age">Not on the MeshCore map</span>';
    }
    var q = preset(p, presets);
    var when = Date.parse(p.last_advert);
    return Number(p.freq).toFixed(3) + ' MHz · SF' + p.sf + ' · BW ' + p.bw + ' kHz · CR 4/' + p.cr +
      (q ? ' <span class="badge-region">' + esc(q.title) + '</span>' : '') +
      (isNaN(when) ? '' : ' <span class="text-muted" title="Advert received by the MeshCore map">(map advert ' + ago(nowMs - when) + ')</span>') +
      (dev ? '<div class="lora-deviation" data-testid="lora-deviation">Differs from ' + esc(dev.agree + ' of ' + dev.peers + ' nearby nodes on this band, which use ' + settingText(dev.majority)) + '</div>' : '');
  }

  // radioText renders the "MHz,kHz,SF,CR" string an observer reports
  // (#1869: the setting a packet was heard on). Unparseable input comes back
  // escaped as it is.
  function radioText(radio) {
    var p = String(radio || '').split(',');
    var num = /^\s*\d+(\.\d+)?\s*$/;
    if (p.length !== 4 || !p.every(function (x) { return num.test(x); })) return esc(radio || '');
    return esc(Number(p[0]).toFixed(3) + ' MHz · SF' + p[2].trim() + ' · BW ' + p[1].trim() + ' kHz · CR 4/' + p[3].trim());
  }

  return { detailHtml: detailHtml, deviationBadge: deviationBadge, radioText: radioText };
})();
