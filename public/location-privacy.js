/* === CoreScope — location-privacy.js (shared "approximate positions" helpers) ===
 *
 * When the server runs with locationPrivacy.enabled, every node position it
 * serves is approximate (within radiusM) and ADVERT packets that carry a
 * location are served without raw_hex. /api/config/client says so:
 *   "locationPrivacy": { "enabled": bool, "radiusM": number }
 * roles.js feeds that payload to fromClientConfig(); every helper here is a
 * no-op (returns '' / null) while the flag is off, so the UI is unchanged.
 */
(function (root) {
  'use strict';

  var state = { enabled: false, radiusM: 0 };

  var HIDDEN_BYTES_TEXT = 'Raw bytes hidden — this advert carries a location, and this instance hides exact positions.';

  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // Reads cfg.locationPrivacy; anything but enabled === true turns it off.
  function fromClientConfig(cfg) {
    var lp = cfg && typeof cfg === 'object' ? cfg.locationPrivacy : null;
    if (lp && lp.enabled === true) {
      var r = Number(lp.radiusM);
      state = { enabled: true, radiusM: isFinite(r) && r > 0 ? r : 0 };
    } else {
      state = { enabled: false, radiusM: 0 };
    }
    return { enabled: state.enabled, radiusM: state.radiusM };
  }

  function enabled() { return state.enabled; }
  function radiusM() { return state.radiusM; }

  // "within 1 km", "within 1.5 km", "within 500 m".
  function radiusLabel() {
    var r = state.radiusM;
    if (!(r > 0)) return 'within a short distance';
    if (r >= 1000) return 'within ' + String(Math.round(r / 100) / 10) + ' km';
    return 'within ' + Math.round(r) + ' m';
  }

  // Node positions are approximate. Used in node detail's location row.
  function approxLocationLabel() {
    return state.enabled ? 'Approximate location (' + radiusLabel() + ')' : '';
  }

  function legendText() {
    return state.enabled ? 'Positions approximate (' + radiusLabel() + ')' : '';
  }

  var NOTICE_TEXT = {
    positions: function () {
      return 'Node positions on this instance are approximate (' + radiusLabel() + '). Distances, line of sight and planning results are indicative.';
    },
    coverage: function () {
      return 'Coverage cells are coarsened for privacy, and node positions on this instance are approximate (' + radiusLabel() + ').';
    },
    export: function () {
      return 'Node positions on this instance are approximate (' + radiusLabel() + '), so exported positions are approximate too.';
    }
  };

  // Compact notice paragraph; '' while the flag is off. kind: positions | coverage | export.
  function approxNoteHtml(kind) {
    if (!state.enabled) return '';
    if (kind === 'location') return '<span class="lp-loc" data-lp-kind="location">' + esc(approxLocationLabel()) + '</span>';
    var fn = NOTICE_TEXT[kind] || NOTICE_TEXT.positions;
    return '<p class="lp-notice" role="note" data-lp-kind="' + esc(NOTICE_TEXT[kind] ? kind : 'positions') + '">' + esc(fn()) + '</p>';
  }

  // Placeholder for pages that can render before /api/config/client answers;
  // hydrate(root) fills it once the config is known. Empty and hidden when off.
  function slotHtml(kind) {
    return '<div class="lp-slot" data-lp-kind="' + esc(kind || 'positions') + '" hidden></div>';
  }

  function fillSlots(rootEl) {
    if (!rootEl || !rootEl.querySelectorAll) return;
    var slots = rootEl.querySelectorAll('.lp-slot');
    for (var i = 0; i < slots.length; i++) {
      var html = state.enabled ? approxNoteHtml(slots[i].getAttribute('data-lp-kind')) : '';
      slots[i].innerHTML = html;
      slots[i].hidden = !html;
    }
  }

  function whenReady() {
    return Promise.resolve(root.MeshConfigReady).then(function () {}, function () {});
  }

  function hydrate(rootEl) {
    fillSlots(rootEl);
    whenReady().then(function () { fillSlots(rootEl); });
  }

  // True for a packet whose raw bytes the server withheld (ADVERT with location).
  function rawHexHidden(pkt) {
    return !!pkt && (pkt.raw_hex == null || pkt.raw_hex === '') && pkt.location_hidden === true;
  }

  function hiddenBytesHtml() {
    return '<p class="lp-hidden-bytes" role="note">' + esc(HIDDEN_BYTES_TEXT) + '</p>';
  }

  // Adds the "Positions approximate" line to a Leaflet map's attribution.
  function addAttribution(map) {
    function add() {
      if (!state.enabled || !map || !map.attributionControl || map._lpAttribution) return;
      map._lpAttribution = true;
      map.attributionControl.addAttribution('<span class="lp-attr">' + esc(legendText()) + '</span>');
    }
    add();
    whenReady().then(add);
  }

  // A Leaflet map that was removed (page left) must not get a late circle.
  function mapAlive(map) {
    if (!map || map._loaded === false) return false;
    return !(map._container && map._container.isConnected === false);
  }

  function accentColor() {
    var cs = root.getComputedStyle && typeof document !== 'undefined' ? root.getComputedStyle(document.documentElement) : null;
    return (cs && cs.getPropertyValue('--accent').trim()) || 'CanvasText';
  }

  var circles = [];
  var themeWired = false;

  // Re-reads the theme colour for every live circle; drops those of removed maps.
  function restyle() {
    circles = circles.filter(function (c) { return mapAlive(c._lpMap); });
    if (!circles.length) return;
    var accent = accentColor();
    circles.forEach(function (c) { try { c.setStyle({ color: accent, fillColor: accent }); } catch (e) { /* map gone */ } });
  }

  // Same signals the node maps use: data-theme flips, and the OS scheme for Auto.
  function wireTheme() {
    if (themeWired) return;
    themeWired = true;
    try {
      if (typeof MutationObserver === 'function' && typeof document !== 'undefined') {
        new MutationObserver(restyle).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
      }
      if (root.matchMedia) {
        var mq = root.matchMedia('(prefers-color-scheme: dark)');
        if (mq && mq.addEventListener) mq.addEventListener('change', restyle);
      }
    } catch (e) { /* no theme tracking; the colour stays as drawn */ }
  }

  // Circle of the privacy radius around latlng ([lat, lon]); null when off.
  // Before /api/config/client has answered it draws once the flag is known.
  function drawUncertainty(map, latlng) {
    if (!state.enabled) {
      whenReady().then(function () { if (state.enabled) draw(map, latlng); });
      return null;
    }
    return draw(map, latlng);
  }

  function draw(map, latlng) {
    if (!state.radiusM || !latlng || !root.L || !root.L.circle || !mapAlive(map)) return null;
    if (map._lpCircle) return map._lpCircle;
    var accent = accentColor();
    try {
      var circle = root.L.circle(latlng, {
        radius: state.radiusM,
        color: accent,
        weight: 2,
        dashArray: '6 4',
        fillColor: accent,
        fillOpacity: 0.12,
        interactive: false,
        className: 'lp-uncertainty'
      });
      circle.addTo(map);
      circle._lpMap = map;
      map._lpCircle = circle;
      circles.push(circle);
      wireTheme();
      return circle;
    } catch (e) {
      return null; // the map was torn down between the check and the add
    }
  }

  root.LocationPrivacy = {
    fromClientConfig: fromClientConfig,
    enabled: enabled,
    radiusM: radiusM,
    radiusLabel: radiusLabel,
    approxLocationLabel: approxLocationLabel,
    legendText: legendText,
    approxNoteHtml: approxNoteHtml,
    slotHtml: slotHtml,
    hydrate: hydrate,
    rawHexHidden: rawHexHidden,
    hiddenBytesHtml: hiddenBytesHtml,
    addAttribution: addAttribution,
    drawUncertainty: drawUncertainty,
    _restyle: restyle
  };
  if (typeof module !== 'undefined' && module.exports) module.exports = root.LocationPrivacy;
})(typeof window !== 'undefined' ? window : globalThis);
