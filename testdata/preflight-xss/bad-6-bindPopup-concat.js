// bad-6-bindPopup-concat.js — Leaflet bindPopup with string-concat
// node-controlled name. Old script only catches backtick form.
/* eslint-disable */
function render(marker, observer) {
  marker.bindPopup('Name: ' + observer);
}
