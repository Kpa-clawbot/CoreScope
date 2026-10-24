/* #1268 — LoRa RF parameters per node, from /api/rf-params (the MeshCore
 * map's view of each node's radio settings), rendered in the node detail
 * views of nodes.js. Pure helpers; no fetching here. */
window.RfParams = (function () {
  'use strict';
  // i18n.js defines tr(); sandboxes that load this file alone get English.
  var tr = globalThis.tr || function (s, p) { return p ? s.replace(/\{(\w+)\}/g, function (m, k) { return Object.prototype.hasOwnProperty.call(p, k) ? String(p[k]) : m; }) : s; };

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
    if (h < 1) return tr('under 1h ago');
    if (h < 24) return tr('{time} ago', { time: h + 'h' });
    return tr('{time} ago', { time: Math.floor(h / 24) + 'd' });
  }

  function settingText(s) {
    return Number(s.freq).toFixed(3) + ' MHz · SF' + s.sf + ' · BW ' + s.bw + ' kHz';
  }

  function deviationTitle(dev) {
    return tr('{agree} of {peers} nearby nodes on this band use {setting}', { agree: dev.agree, peers: dev.peers, setting: settingText(dev.majority) });
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
    return ' <span class="badge-lora-diff" title="' + esc(tr('LoRa settings differ: {detail}', { detail: deviationTitle(dev) })) + '">LoRa ' + esc(parts.join(' ')) + '</span>';
  }

  function detailHtml(p, presets, nowMs, dev) {
    if (!p) {
      return '<span class="text-muted" title="' + esc(tr('Not on the MeshCore map, or no advert there within the configured age')) + '">' + esc(tr('Not on the MeshCore map')) + '</span>';
    }
    var q = preset(p, presets);
    var when = Date.parse(p.last_advert);
    return Number(p.freq).toFixed(3) + ' MHz · SF' + p.sf + ' · BW ' + p.bw + ' kHz · CR 4/' + p.cr +
      (q ? ' <span class="badge-region">' + esc(q.title) + '</span>' : '') +
      (isNaN(when) ? '' : ' <span class="text-muted" title="' + esc(tr('Advert received by the MeshCore map')) + '">(' + esc(tr('map advert {time}', { time: ago(nowMs - when) })) + ')</span>') +
      (dev ? '<div class="lora-deviation" data-testid="lora-deviation">' + esc(tr('Differs from {agree} of {peers} nearby nodes on this band, which use {setting}', { agree: dev.agree, peers: dev.peers, setting: settingText(dev.majority) })) + '</div>' : '');
  }

  return { detailHtml: detailHtml, deviationBadge: deviationBadge };
})();
