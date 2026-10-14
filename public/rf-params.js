/* #1268 — LoRa RF parameters per node, from /api/rf-params (the MeshCore
 * map's view of each node's radio settings), rendered in the node detail
 * views of nodes.js. Pure helpers; no fetching here. */
window.RfParams = (function () {
  'use strict';

  function esc(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
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

  function detailHtml(p, presets, nowMs) {
    if (!p) {
      return '<span class="text-muted" title="Not on the MeshCore map, or no advert there within the configured age">Not on the MeshCore map</span>';
    }
    var q = preset(p, presets);
    var when = Date.parse(p.last_advert);
    return Number(p.freq).toFixed(3) + ' MHz · SF' + p.sf + ' · BW ' + p.bw + ' kHz · CR 4/' + p.cr +
      (q ? ' <span class="badge-region">' + esc(q.title) + '</span>' : '') +
      (isNaN(when) ? '' : ' <span class="text-muted" title="Advert received by the MeshCore map">(map advert ' + ago(nowMs - when) + ')</span>');
  }

  return { detailHtml: detailHtml };
})();
