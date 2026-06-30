/* route-view-utils.js — pure helpers extracted from route-view.js (#1424).
 *
 * Pure refactor: zero behavior change. Loaded BEFORE route-view.js in any
 * HTML that needs the route view. The IIFE in route-view.js unpacks
 * window.MC_ROUTE_UTILS at the top.
 *
 * Helpers exported:
 *   escapeHtml(s)
 *   buildPacketContextBlock(pktCtx)
 *   buildSnrSparkline(snrTrend)
 *
 * `spiderFanFor` is NOT extracted — it consumes Leaflet LatLng / map types
 * (mapRef.latLngToLayerPoint, mk.getLatLng, mk.setLatLng, L.point) and is
 * not pure. Left in place inside route-view.js (see comment there).
 */
(function () {
  'use strict';

  function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c];
    });
  }

  // packet-context block (one stable layout, type chip + 3-5
  // facts). pktCtx shape:
  //   { type: 'ADVERT'|'TXT_MSG'|'GRP_TXT'|'TRACE'|other,
  //     decoded: <parsed JSON of decoded_json>,
  //     payloadType: <byte>,
  //     srcResolvedName, destResolvedName, observedHops, observationCount }
  function buildPacketContextBlock(pktCtx) {
    if (!pktCtx || !pktCtx.type) return '';
    var t = pktCtx.type;
    var d = pktCtx.decoded || {};
    // #1648 M3: payload-type taxonomy glyphs are inline Phosphor sprite refs
    // (was: 📡/🔀/✉/🔒/🔓/⌖/#/·). // EMOJI-OK: comment referencing prior glyphs
    var glyph, label, factsHtml = '';
    switch (t) {
      case 'ADVERT':
        glyph = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-broadcast"/></svg>'; label = 'ADVERT';
        var name = d.adName || d.name || (d.pubKey ? d.pubKey.slice(0, 8) : '?');
        var role = (d.flags && (d.flags.repeater ? 'repeater' : d.flags.room ? 'room' : d.flags.sensor ? 'sensor' : d.flags.chat ? 'companion' : 'unknown')) || 'unknown';
        // no fabricated fields. Battery isn't decoded into the advert
        // JSON — adverts carry lat/lon + name + flags, not battery. If a
        // future advert version exposes it, re-add then.
        var sigHtml = (d.signatureValid === true)
          ? '<span class="status-ok">' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-check"/></svg>' + '</span>'
          : (d.signatureValid === false
              ? '<span class="status-err">' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-x"/></svg>' + '</span>'
              : null);
        var line1 = '<b>' + escapeHtml(name) + '</b> · ' + escapeHtml(role);
        if (sigHtml) line1 += ' · sig ' + sigHtml;
        // Self-reported GPS if present
        if (d.lat != null && d.lon != null) {
          line1 += ' · ' + d.lat.toFixed(3) + ', ' + d.lon.toFixed(3);
        }
        var pkPrefix = d.pubKey ? d.pubKey.slice(0, 12) + '…' : '';
        factsHtml = '<div class="mc-rt-ctx-line">' + line1 + '</div>';
        if (pkPrefix) factsHtml += '<div class="mc-rt-ctx-line mc-rt-ctx-mono">' + escapeHtml(pkPrefix) + '</div>';
        break;
      case 'PATH':
        glyph = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-shuffle"/></svg>'; label = 'PATH';
        var psrc = pktCtx.srcResolvedName || (d.srcHash ? 'unknown (hash ' + d.srcHash + ')' : '?');
        var pdst = pktCtx.destResolvedName || (d.destHash ? 'unknown (hash ' + d.destHash + ')' : '?');
        factsHtml = '<div class="mc-rt-ctx-line"><b>' + escapeHtml(psrc) + '</b> <span class="mc-rt-ctx-arrow">→</span> <b>' + escapeHtml(pdst) + '</b></div>';
        break;
      case 'TXT_MSG':
      case 'REQ':
      case 'RESPONSE':
      case 'ANON_REQ':
        var typeGlyphNames = { 'TXT_MSG': 'ph-envelope', 'REQ': 'ph-lock', 'RESPONSE': 'ph-lock-open', 'ANON_REQ': 'ph-lock' };
        // PR #1804 r1 item 10: REQ label uses the canonical short
        // ('Request') instead of the legacy 'REQUEST' caps string.
        var typeLabels = { 'TXT_MSG': 'DM', 'REQ': 'Request', 'RESPONSE': 'Response', 'ANON_REQ': 'Anon Req' };
        glyph = (typeGlyphNames[t] ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#' + typeGlyphNames[t] + '"/></svg>' : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle"/></svg>');
        label = typeLabels[t] || t;
        var src = pktCtx.srcResolvedName || (d.srcHash ? 'unknown (hash ' + d.srcHash + ')' : (t === 'ANON_REQ' ? 'anon' : '?'));
        var dst = pktCtx.destResolvedName || (d.destHash ? 'unknown (hash ' + d.destHash + ')' : '?');
        factsHtml = '<div class="mc-rt-ctx-line"><b>' + escapeHtml(src) + '</b> <span class="mc-rt-ctx-arrow">→</span> <b>' + escapeHtml(dst) + '</b></div>';
        factsHtml += '<div class="mc-rt-ctx-line mc-rt-ctx-meta">' + '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg>' + ' encrypted</div>';
        break;
      case 'GRP_TXT':
      case 'CHAN':
        glyph = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-hash"/></svg>'; label = 'CHANNEL MSG';
        var chName = pktCtx.channelName || d.channel || (d.channelHashHex ? 'channel 0x' + d.channelHashHex : 'channel ?');
        var contentText = pktCtx.decryptedText || d.text || d.plainText || null;
        var decrypted = !!contentText || d.decryptionStatus === 'decrypted';
        var encStatus = decrypted
          ? '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock-open"/></svg>' + ' decrypted'
          : '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-lock"/></svg>' + ' no key';
        factsHtml = '<div class="mc-rt-ctx-line"><b>' + escapeHtml(chName) + '</b></div>';
        factsHtml += '<div class="mc-rt-ctx-line mc-rt-ctx-meta">' + encStatus + '</div>';
        if (contentText) {
          var preview = contentText.slice(0, 80);
          if (contentText.length > 80) preview += '…';
          factsHtml += '<div class="mc-rt-ctx-line mc-rt-ctx-quote">"' + escapeHtml(preview) + '"</div>';
        }
        var senderName = pktCtx.srcResolvedName || d.sender || (d.srcHash ? 'sender 0x' + d.srcHash : null);
        if (senderName) factsHtml += '<div class="mc-rt-ctx-line mc-rt-ctx-meta">from <b>' + escapeHtml(senderName) + '</b></div>';
        break;
      case 'TRACE':
        glyph = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-crosshair"/></svg>'; label = 'TRACE';
        var officialHops = (d.routeTaken && d.routeTaken.length) || (d.route && d.route.length) || null;
        var observed = (pktCtx.observedHops != null) ? pktCtx.observedHops : null;
        if (officialHops != null && observed != null) {
          factsHtml = '<div class="mc-rt-ctx-line">Official: <b>' + officialHops + '</b> hops · Observed: <b>' + observed + '</b></div>';
        } else if (officialHops != null) {
          factsHtml = '<div class="mc-rt-ctx-line">Official route: <b>' + officialHops + '</b> hops</div>';
        }
        if (pktCtx.issuedBy) factsHtml += '<div class="mc-rt-ctx-line mc-rt-ctx-meta">issued by <b>' + escapeHtml(pktCtx.issuedBy) + '</b></div>';
        break;
      default:
        glyph = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-circle"/></svg>'; label = (t || 'OTHER').toUpperCase();
        if (pktCtx.payloadSize != null) {
          factsHtml = '<div class="mc-rt-ctx-line mc-rt-ctx-meta">' + pktCtx.payloadSize + ' bytes</div>';
        }
        break;
    }
    return '<div class="mc-rt-ctx" data-type="' + escapeHtml(t) + '">' +
      '<div class="mc-rt-ctx-chip"><span class="mc-rt-ctx-glyph">' + glyph + '</span> ' + escapeHtml(label) + '</div>' +
      '<div class="mc-rt-ctx-facts">' + factsHtml + '</div>' +
      '</div>';
  }

  function buildSnrSparkline(snrTrend) {
    if (!snrTrend || !snrTrend.length) return '<span class="mc-rt-detail-na">no SNR data</span>';
    var pts = snrTrend.filter(function (p) { return p && p.snr != null; });
    if (!pts.length) return '<span class="mc-rt-detail-na">no SNR data</span>';
    var W = 200, H = 28;
    var snrs = pts.map(function (p) { return p.snr; });
    var minS = Math.min.apply(null, snrs), maxS = Math.max.apply(null, snrs);
    if (maxS === minS) { minS -= 1; maxS += 1; }
    // n<3 is not a sparkline — a 2-point polyline implies a
    // trend across time it can't represent. Show DOTS only (no connecting line)
    // when there are fewer than 3 observations.
    var showLine = pts.length >= 3;
    var poly = pts.map(function (p, i) {
      var x = (i / (pts.length - 1 || 1)) * W;
      var y = H - 2 - ((p.snr - minS) / (maxS - minS)) * (H - 4);
      return x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
    var svg = '<svg class="mc-rt-detail-spark" width="' + W + '" height="' + H + '" viewBox="0 0 ' + W + ' ' + H + '" aria-label="SNR across route observations">';
    if (showLine) {
      svg += '<polyline points="' + poly + '" fill="none" stroke="currentColor" stroke-width="1.2"/>';
    }
    // Dots always (data points themselves are the truth)
    pts.forEach(function (p, i) {
      var x = (i / (pts.length - 1 || 1)) * W;
      var y = H - 2 - ((p.snr - minS) / (maxS - minS)) * (H - 4);
      svg += '<circle cx="' + x.toFixed(1) + '" cy="' + y.toFixed(1) + '" r="2" fill="currentColor"/>';
    });
    svg += '</svg>';
    return svg +
      '<span class="mc-rt-detail-spark-meta">' + pts.length + ' obs · ' + minS.toFixed(1) + '..' + maxS.toFixed(1) + ' dB</span>';
  }

  window.MC_ROUTE_UTILS = {
    escapeHtml: escapeHtml,
    buildPacketContextBlock: buildPacketContextBlock,
    buildSnrSparkline: buildSnrSparkline
  };
})();
