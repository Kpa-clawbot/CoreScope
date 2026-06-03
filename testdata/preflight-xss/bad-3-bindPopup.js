// bad-3-bindPopup.js — XSS fixture for check-xss-sinks.
// Leaflet bindPopup with raw ${observer} interpolation.
// EXPECTED: flagged by check-xss-sinks.
/* eslint-disable */
function bindPopupForMarker(marker, observer) {
  marker.bindPopup(`<b>${observer}</b>`);
}
