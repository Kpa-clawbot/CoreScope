/* Live radio filter (config liveRadioFilters).
 *
 * Narrows the /live feed to packets heard ONLY on one frequency, e.g. a second
 * mesh that multi-radio observers listen to next to the main channel. Each
 * observation's `radio` ("MHz,kHz,SF,CR") is resolved by the server from the
 * observer's radio history (#1869: status `radio`, plus `radios` +
 * `rx_radio_id` for multi-radio observers).
 *
 * Standalone: live.js only calls init(onChange) once and matches(packets)
 * where it already applies its other feed filters.
 */
(function () {
  'use strict';

  var FREQ_EPS = 0.0005; // CanonicalRadio rounds MHz to 3 dp
  var STORAGE_KEY = 'live-radio-only';

  // Frequency (MHz) of the chosen liveRadioFilters entry, or null for all.
  // A saved value only takes effect once the server still lists it (init).
  var selectedFreq = null;

  function sameFreq(a, b) { return Math.abs(a - b) < FREQ_EPS; }

  function readSaved() {
    try { return parseFloat(localStorage.getItem(STORAGE_KEY)) || null; } catch (e) { return null; }
  }
  function save(freq) {
    try {
      if (freq) localStorage.setItem(STORAGE_KEY, freq);
      else localStorage.removeItem(STORAGE_KEY);
    } catch (e) { /* storage unavailable */ }
  }

  /**
   * True when every observation in the group was heard on freqMHz. An
   * observation on another setting, or with no known setting, means the
   * packet also travelled elsewhere, so the group does not match.
   * No freqMHz → no filter. Pure; exposed for unit tests.
   */
  function groupOnlyOnFreq(packets, freqMHz) {
    if (freqMHz == null || !(freqMHz > 0)) return true;
    if (!packets || !packets.length) return false;
    for (var i = 0; i < packets.length; i++) {
      var radio = packets[i] && packets[i].radio;
      if (!radio) return false;
      var f = parseFloat(String(radio).split(',')[0]);
      if (!sameFreq(f, freqMHz)) return false;
    }
    return true;
  }

  /** Feed hook: does this packet group pass the current selection? */
  function matches(packets) {
    return groupOnlyOnFreq(packets, selectedFreq);
  }

  function esc(s) {
    if (typeof window.escapeHtml === 'function') return window.escapeHtml(s);
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  /**
   * Fill #liveRadioOnlySelect from /api/config/client liveRadioFilters and
   * call onChange() whenever the selection changes. Hidden when none are
   * configured. #/live?radioOnly=<label or MHz> picks an entry from a link.
   */
  function init(onChange) {
    var label = document.getElementById('liveRadioOnlyLabel');
    var select = document.getElementById('liveRadioOnlySelect');
    if (!label || !select) return;
    var ready = window.MeshConfigReady && typeof window.MeshConfigReady.then === 'function'
      ? window.MeshConfigReady : Promise.resolve();
    ready.then(function () {
      var filters = Array.isArray(window.MC_LIVE_RADIO_FILTERS) ? window.MC_LIVE_RADIO_FILTERS : [];
      var freq = readSaved();
      var params = typeof window.getHashParams === 'function' ? window.getHashParams() : null;
      var urlVal = params && params.get('radioOnly');
      if (urlVal) {
        var want = String(urlVal).trim().toLowerCase();
        var hit = filters.find(function (f) {
          return String(f.label).toLowerCase() === want || sameFreq(f.freqMHz, parseFloat(want));
        });
        if (hit) freq = hit.freqMHz;
      }
      if (freq != null && !filters.some(function (f) { return sameFreq(f.freqMHz, freq); })) freq = null;
      selectedFreq = freq;
      if (!filters.length) { label.style.display = 'none'; return; }

      select.innerHTML = '<option value="">All</option>' + filters.map(function (f) {
        var sel = selectedFreq != null && sameFreq(f.freqMHz, selectedFreq) ? ' selected' : '';
        return '<option value="' + esc(String(f.freqMHz)) + '"' + sel + '>' + esc(f.label) + ' only</option>';
      }).join('');
      label.style.display = '';
      select.addEventListener('change', function () {
        selectedFreq = parseFloat(select.value) || null;
        save(selectedFreq);
        if (typeof onChange === 'function') onChange();
      });
      if (selectedFreq != null && typeof onChange === 'function') onChange();
    }).catch(function () { label.style.display = 'none'; });
  }

  window.LiveRadioFilter = { init: init, matches: matches, groupOnlyOnFreq: groupOnlyOnFreq };
})();
