/* Estimated positions for repeaters known only from their reported name
   (/api/nodes/reported, cmd/server/reported_nodes.go).

   These repeaters never sent an advert, so they have no position of their own.
   The server estimates one from where mobile companions heard them directly.
   Everything here exists to keep that estimate from ever being read as a real
   node position: a hollow dashed marker with "≈", a dashed uncertainty circle,
   and a popup that says what the position is and is not. */
(function () {
  'use strict';

  // escapeHtml is app.js's; formatRadius and coverageHref are shared with the
  // nodes page (reported-nodes.js). All are looked up at call time.
  function formatRadius(m) { return window.ReportedNodes.formatRadius(m); }

  // withEstimate keeps only the nodes the server could place.
  function withEstimate(nodes) {
    return (nodes || []).filter(function (n) {
      return n && n.estimate && isFinite(n.estimate.lat) && isFinite(n.estimate.lon);
    });
  }

  function coverageHref(n) {
    return window.ReportedNodes.coverageHref(n.estimate.lat, n.estimate.lon);
  }

  function iconHtml() {
    return '<svg width="32" height="32" viewBox="0 0 26 26" aria-hidden="true">' +
      '<circle cx="13" cy="13" r="10" class="mc-est-ring"/>' +
      '<text x="13" y="17.5" text-anchor="middle" class="mc-est-glyph">≈</text></svg>';
  }

  function popupHtml(n, method) {
    var e = n.estimate;
    var name = n.name ? escapeHtml(n.name) : escapeHtml(n.pubkey.slice(0, 8));
    var minSpots = method && method.min_spots ? method.min_spots : 3;
    return '<div class="mc-est-popup">' +
      '<div class="mc-est-badge">Estimated position</div>' +
      '<div class="mc-est-name">' + name + '</div>' +
      '<div class="mc-est-pk mono">' + escapeHtml(n.pubkey.slice(0, 16)) + '…</div>' +
      '<p class="mc-est-what">No advert position. This repeater only told a mobile companion its name, ' +
      'so its location is estimated from where companions heard it directly: within about <strong>' +
      escapeHtml(formatRadius(e.radius_m)) + '</strong> of this point.</p>' +
      '<dl class="mc-est-facts">' +
      '<dt>Heard</dt><dd>' + escapeHtml(n.receptions) + ' times at ' + escapeHtml(n.spots) + ' spots (estimate needs ' + escapeHtml(minSpots) + ')</dd>' +
      (n.first_heard ? '<dt>First / last</dt><dd>' + escapeHtml(String(n.first_heard).slice(0, 10)) + ' / ' + escapeHtml(String(n.last_heard).slice(0, 10)) + '</dd>' : '') +
      '<dt>Name reported</dt><dd>' + escapeHtml(String(n.name_reported_at).slice(0, 10)) + ' by ' + escapeHtml(n.reporters) + (n.reporters === 1 ? ' companion' : ' companions') + '</dd>' +
      '</dl>' +
      '<p class="mc-est-caveat">A centroid of where companions drove, not a measured position: a repeater heard from one side only is placed towards that side.</p>' +
      '<a href="' + coverageHref(n) + '">Show receptions on the coverage map</a>' +
      '</div>';
  }

  // addTo draws every estimate onto layer: the uncertainty circle first, so the
  // marker stays on top and clickable. L is Leaflet.
  function addTo(layer, L, nodes, method) {
    var shown = withEstimate(nodes);
    shown.forEach(function (n) {
      var ll = [n.estimate.lat, n.estimate.lon];
      L.circle(ll, { radius: n.estimate.radius_m, className: 'mc-est-area', interactive: false }).addTo(layer);
      var marker = L.marker(ll, {
        icon: L.divIcon({ html: iconHtml(), className: 'mc-est-marker', iconSize: [32, 32], iconAnchor: [16, 16], popupAnchor: [0, -15] }),
        title: (n.name || n.pubkey.slice(0, 8)) + ' (estimated position)',
        alt: 'Estimated position of ' + (n.name || n.pubkey.slice(0, 8)),
        keyboard: true,
      });
      marker.bindPopup(popupHtml(n, method), { maxWidth: 300 });
      marker.addTo(layer);
    });
    return shown.length;
  }

  window.MapEstimated = { withEstimate: withEstimate, popupHtml: popupHtml, iconHtml: iconHtml, coverageHref: coverageHref, addTo: addTo };
})();
