// Geofilter draft save/load/download helpers.
// Exposes GeofilterDraft global with: saveDraft, loadDraft, clearDraft, buildConfigSnippet, downloadConfig
(function () {
  'use strict';
  var STORAGE_KEY = 'geofilter-draft';

  function saveDraft(polygon, bufferKm) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ polygon: polygon, bufferKm: bufferKm }));
  }

  function loadDraft() {
    var raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    try { return JSON.parse(raw); } catch (e) { return null; }
  }

  function clearDraft() {
    localStorage.removeItem(STORAGE_KEY);
  }

  function buildConfigSnippet(polygon, bufferKm) {
    return JSON.stringify({ geo_filter: { bufferKm: bufferKm, polygon: polygon } }, null, 2);
  }

  function downloadConfig(polygon, bufferKm) {
    var snippet = buildConfigSnippet(polygon, bufferKm);
    var blob = new Blob([snippet], { type: 'application/json' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = 'geofilter-config-snippet.json';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }

  // Export
  (typeof window !== 'undefined' ? window : this).GeofilterDraft = {
    saveDraft: saveDraft,
    loadDraft: loadDraft,
    clearDraft: clearDraft,
    buildConfigSnippet: buildConfigSnippet,
    downloadConfig: downloadConfig
  };
})();
