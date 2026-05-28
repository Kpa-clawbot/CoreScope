/* cb-presets.js — Colorblind preset registry & runtime switcher (#1361).
 *
 * MVP scope:
 *   - 5 presets: default (Wong 2011), deut (IBM 5-class), prot (IBM 5-class
 *     with high-luminance amber anchor), trit (Tol muted, blue/yellow-safe),
 *     achromat (pure luminance ramp).
 *   - applyPreset(id) sets body[data-cb-preset], writes --mc-role-* and
 *     --mc-mb-* CSS vars on documentElement, persists to localStorage.
 *   - initFromStorage() re-applies on reload.
 *   - storage event listener syncs across tabs.
 *   - WCAG 2.2 SC 1.4.3 / 1.4.11 contrast helper for validation.
 *
 * Stretch (Brettel/Vienot SVG simulation overlay, "Reset to default Wong"
 * button) is intentionally NOT implemented here — separate follow-up.
 *
 * Palette sources cited in PR body.
 */
(function () {
  'use strict';

  var STORAGE_KEY = 'meshcore-cb-preset';
  var DATA_ATTR   = 'data-cb-preset';

  // ── Palettes ────────────────────────────────────────────────────────────
  // Each preset declares colors for the 5 roles + the 3 multi-byte status
  // colors. role keys mirror --mc-role-{repeater|companion|room|sensor|observer}.
  // mb keys mirror --mc-mb-{confirmed|suspected|unknown}.
  var PRESETS = [
    {
      id: 'default',
      label: 'Default (Wong 2011)',
      description: 'Wong\'s 8-class colorblind-safe palette — the project default.',
      roleColors: {
        repeater:  '#D55E00', // vermillion
        companion: '#56B4E9', // sky blue
        room:      '#009E73', // bluish-green
        sensor:    '#F0E442', // yellow
        observer:  '#CC79A7'  // reddish-purple
      },
      // #1407 — per-role text colors paired with each bg for WCAG 1.4.3 AA
      // (≥4.5:1). Wong defaults all pass with dark text; explicit so the
      // CSS-var pipeline is uniform across presets.
      roleText: {
        repeater: '#1a1a1a', companion: '#1a1a1a', room: '#1a1a1a',
        sensor: '#1a1a1a', observer: '#1a1a1a'
      },
      mb: {
        confirmed: '#56F0A0',
        suspected: '#FFD966',
        unknown:   '#FF8888'
      }
    ,
      routeRamp: ['#440154', '#3b528b', '#21918c', '#5ec962', '#fde725']
    },
    {
      id: 'deut',
      label: 'Deuteranopia-tuned',
      description: 'IBM 5-class palette — anchors shifted away from red/green collision.',
      // IBM Design Language colorblind-safe: blue / purple / magenta / orange / amber.
      roleColors: {
        repeater:  '#FE6100', // orange (high-luminance anchor for repeater)
        companion: '#648FFF', // blue
        room:      '#785EF0', // purple
        sensor:    '#FFB000', // amber
        observer:  '#DC267F'  // magenta
      },
      // #1407 — IBM 5-class: room (#785EF0) and observer (#DC267F) fail AA
      // with #1a1a1a (3.86 / 3.83). Flip to white where needed.
      roleText: {
        repeater: '#1a1a1a', companion: '#1a1a1a', room: '#ffffff',
        sensor: '#1a1a1a', observer: '#ffffff'
      },
      mb: {
        confirmed: '#648FFF',
        suspected: '#FFB000',
        unknown:   '#DC267F'
      }
    ,
      routeRamp: ['#0d0887', '#7e03a8', '#cc4778', '#f89540', '#f0f921']
    },
    {
      id: 'prot',
      label: 'Protanopia-tuned',
      description: 'IBM 5-class with amber-shifted repeater anchor (protan-safe luminance).',
      roleColors: {
        repeater:  '#FFB000', // amber — higher luminance than orange for protans
        companion: '#648FFF',
        room:      '#785EF0',
        sensor:    '#FE6100',
        observer:  '#DC267F'
      },
      // Same as deut for room/observer.
      roleText: {
        repeater: '#1a1a1a', companion: '#1a1a1a', room: '#ffffff',
        sensor: '#1a1a1a', observer: '#ffffff'
      },
      mb: {
        confirmed: '#648FFF',
        suspected: '#FFB000',
        unknown:   '#DC267F'
      }
    ,
      routeRamp: ['#0d0887', '#7e03a8', '#cc4778', '#f89540', '#f0f921']
    },
    {
      id: 'trit',
      label: 'Tritanopia-tuned',
      description: 'Tol muted palette — avoids blue/yellow confusion zone.',
      // Paul Tol muted (B/Y-safe): red / teal / green / purple / sand.
      roleColors: {
        repeater:  '#CC6677', // rose
        companion: '#117733', // green
        room:      '#882255', // wine
        sensor:    '#DDCC77', // sand (replaces pure yellow)
        observer:  '#AA4499'  // purple
      },
      // #1407 — Tol muted has 3 darker anchors that fail with dark text:
      //   companion #117733 vs #1a1a1a = 3.71:1 → use white text
      //   room #882255 vs #1a1a1a = 2.41:1 → use white text
      //   observer #AA4499 vs #1a1a1a = 4.00:1 → use white text
      // The 2 lighter anchors (rose, sand) keep dark text.
      roleText: {
        repeater: '#1a1a1a',  // #CC6677 vs #1a1a1a = 5.73:1 ✓
        companion: '#ffffff', // #117733 vs #fff = 5.66:1 ✓
        room:      '#ffffff', // #882255 vs #fff = 8.71:1 ✓
        sensor:    '#1a1a1a', // #DDCC77 vs #1a1a1a = 12.98:1 ✓
        observer:  '#ffffff'  // #AA4499 vs #fff = 5.25:1 ✓
      },
      mb: {
        confirmed: '#117733',
        suspected: '#DDCC77',
        unknown:   '#CC6677'
      }
    ,
      routeRamp: ['#440154', '#3b528b', '#21918c', '#5ec962', '#fde725']
    },
    {
      id: 'achromat',
      label: 'Achromatopsia (monochrome)',
      description: 'Pure luminance ramp — relies on shape/letter/glyph carriers from #1356/#1357.',
      roleColors: {
        repeater:  '#333333', // L=20%
        companion: '#595959', // L=35%
        room:      '#808080', // L=50%
        sensor:    '#b3b3b3', // L=70%
        observer:  '#e6e6e6'  // L=90%
      },
      // #1407 — original bug: pill text locked to #1a1a1a → 3 of 5 fail AA.
      // Fix: white text on the 2 darkest grays, dark text on the 2 lightest,
      // pure black for L=50 mid-gray (neither #1a1a1a nor #fff clears 4.5
      // there — black yields 5.32:1).
      //   repeater  #333 vs #fff = 12.63:1 ✓
      //   companion #595959 vs #fff = 7.00:1 ✓
      //   room      #808080 vs #000 = 5.32:1 ✓  (vs #1a1a1a = 4.41 ✗ / #fff = 3.95 ✗)
      //   sensor    #b3b3b3 vs #1a1a1a = 8.30:1 ✓
      //   observer  #e6e6e6 vs #1a1a1a = 13.94:1 ✓
      roleText: {
        repeater:  '#ffffff',
        companion: '#ffffff',
        room:      '#000000',
        sensor:    '#1a1a1a',
        observer:  '#1a1a1a'
      },
      mb: {
        confirmed: '#b3b3b3',
        suspected: '#808080',
        unknown:   '#595959'
      }
    ,
      routeRamp: ['#222222', '#555555', '#888888', '#bbbbbb', '#eeeeee']
    }
  ];

  // ── WCAG helpers ────────────────────────────────────────────────────────
  function _hexToRgb(hex) {
    if (!hex || hex[0] !== '#' || hex.length !== 7) return null;
    return {
      r: parseInt(hex.slice(1, 3), 16),
      g: parseInt(hex.slice(3, 5), 16),
      b: parseInt(hex.slice(5, 7), 16)
    };
  }
  function _channelLin(c) {
    var s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  }
  function relativeLuminance(hex) {
    var rgb = _hexToRgb(hex);
    if (!rgb) return 0;
    return 0.2126 * _channelLin(rgb.r) + 0.7152 * _channelLin(rgb.g) + 0.0722 * _channelLin(rgb.b);
  }
  function contrast(fg, bg) {
    var L1 = relativeLuminance(fg);
    var L2 = relativeLuminance(bg);
    var hi = Math.max(L1, L2);
    var lo = Math.min(L1, L2);
    return (hi + 0.05) / (lo + 0.05);
  }
  // Canonical map tile backgrounds for validation (Carto Positron / Dark Matter)
  var TILE_LIGHT = '#f2efe9';
  var TILE_DARK  = '#1a1a1a';

  /**
   * Validate a preset against WCAG 2.2 SC 1.4.11 (3:1 for non-text UI).
   * Returns an array of { role, color, vsLight, vsDark, passLight, passDark }.
   */
  function validatePreset(presetId) {
    var p = PRESETS.filter(function (x) { return x.id === presetId; })[0];
    if (!p) return [];
    var out = [];
    Object.keys(p.roleColors).forEach(function (role) {
      var c = p.roleColors[role];
      var vL = contrast(c, TILE_LIGHT);
      var vD = contrast(c, TILE_DARK);
      out.push({
        role: role,
        color: c,
        vsLight: vL,
        vsDark: vD,
        passLight: vL >= 3.0,
        passDark:  vD >= 3.0
      });
    });
    return out;
  }

  // ── Runtime application ────────────────────────────────────────────────
  function _byId(id) {
    for (var i = 0; i < PRESETS.length; i++) if (PRESETS[i].id === id) return PRESETS[i];
    return null;
  }

  function applyPreset(id, opts) {
    opts = opts || {};
    var p = _byId(id);
    if (!p) return false;
    if (typeof document !== 'undefined' && document.body) {
      document.body.setAttribute(DATA_ATTR, p.id);
    }
    if (typeof document !== 'undefined' && document.documentElement) {
      var style = document.documentElement.style;
      Object.keys(p.roleColors).forEach(function (role) {
        style.setProperty('--mc-role-' + role, p.roleColors[role]);
      });
      // #1407 — per-role text-color CSS vars so .mc-pill / badges can pick
      // a foreground that meets WCAG 1.4.3 AA against the role bg.
      var rt = p.roleText || {};
      ['repeater', 'companion', 'room', 'sensor', 'observer'].forEach(function (role) {
        style.setProperty('--mc-role-' + role + '-text', rt[role] || '#1a1a1a');
      });
      Object.keys(p.mb).forEach(function (k) {
        style.setProperty('--mc-mb-' + k, p.mb[k]);
      });
      // #1418 — route-view sequence ramp (5 stops). route-view.js reads
      // --mc-rt-ramp-0..4 instead of hardcoded viridis/magma so a CB preset
      // changes the route edge colors live. Achromat uses a luminance ramp.
      var rr = p.routeRamp || ['#440154','#3b528b','#21918c','#5ec962','#fde725'];
      for (var ri = 0; ri < 5; ri++) {
        style.setProperty('--mc-rt-ramp-' + ri, rr[ri] || rr[rr.length - 1]);
      }
      // #1407 — ROLE_COLORS / ROLE_STYLE are now live getters in roles.js
      // that read --mc-role-* directly, so no explicit sync is needed. The
      // pre-#1407 code path kept them in sync as a workaround for the static
      // literal bug; with the getter it's a no-op and removed.
    }
    if (!opts.skipPersist) {
      try { if (typeof localStorage !== 'undefined') localStorage.setItem(STORAGE_KEY, p.id); } catch (e) {}
    }
    if (typeof window !== 'undefined' && typeof window.dispatchEvent === 'function' && typeof window.CustomEvent === 'function') {
      try { window.dispatchEvent(new window.CustomEvent('cb-preset-changed', { detail: { id: p.id } })); } catch (e) {}
    }
    return true;
  }

  function currentPreset() {
    try {
      if (typeof localStorage !== 'undefined') {
        var v = localStorage.getItem(STORAGE_KEY);
        if (v && _byId(v)) return v;
      }
    } catch (e) {}
    // #1446 — return null when no preset is stored. Previously this returned
    // 'default' unconditionally, which forced body[data-cb-preset="default"]
    // on every cold boot and trapped --mc-role-* in the Wong palette via the
    // matching style.css rule. The CB preset is now an end-user opt-in:
    // absent attribute = "no preset", role colors flow from server config.
    return null;
  }

  function clearPreset() {
    try { if (typeof localStorage !== 'undefined') localStorage.removeItem(STORAGE_KEY); } catch (e) {}
    if (typeof document !== 'undefined' && document.body && document.body.removeAttribute) {
      document.body.removeAttribute(DATA_ATTR);
    }
    // Strip preset-written CSS vars from documentElement so the cascade
    // re-falls through :root defaults (or server config, which the
    // customizer pipeline re-applies via the cb-preset-changed listener).
    if (typeof document !== 'undefined' && document.documentElement && document.documentElement.style) {
      var style = document.documentElement.style;
      ['repeater', 'companion', 'room', 'sensor', 'observer'].forEach(function (role) {
        style.removeProperty('--mc-role-' + role);
        style.removeProperty('--mc-role-' + role + '-text');
      });
      ['confirmed', 'suspected', 'unknown'].forEach(function (k) {
        style.removeProperty('--mc-mb-' + k);
      });
      for (var ri = 0; ri < 5; ri++) style.removeProperty('--mc-rt-ramp-' + ri);
    }
    if (typeof window !== 'undefined' && typeof window.dispatchEvent === 'function' && typeof window.CustomEvent === 'function') {
      try { window.dispatchEvent(new window.CustomEvent('cb-preset-changed', { detail: { id: null } })); } catch (e) {}
    }
    return true;
  }

  function initFromStorage() {
    var id = currentPreset();
    // #1446 — only apply when a preset is actually stored. No stored preset
    // means "no preset active" (the new default), not "fall back to Wong".
    if (id) applyPreset(id, { skipPersist: true });
  }

  // Cross-tab sync via storage event.
  function _onStorage(ev) {
    if (!ev || ev.key !== STORAGE_KEY) return;
    var id = ev.newValue;
    if (!id || !_byId(id)) return;
    applyPreset(id, { skipPersist: true });
  }
  if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
    window.addEventListener('storage', _onStorage);
  }

  // Auto-init on module load (so reload re-applies the saved preset before
  // first paint, modulo script ordering — cb-presets.js loads before app.js).
  try { initFromStorage(); } catch (e) {}

  // Export
  var api = {
    list: PRESETS,
    applyPreset: applyPreset,
    clearPreset: clearPreset,
    currentPreset: currentPreset,
    initFromStorage: initFromStorage,
    validatePreset: validatePreset,
    wcag: {
      relativeLuminance: relativeLuminance,
      contrast: contrast,
      TILE_LIGHT: TILE_LIGHT,
      TILE_DARK: TILE_DARK
    },
    STORAGE_KEY: STORAGE_KEY
  };
  if (typeof window !== 'undefined') window.MeshCorePresets = api;
  if (typeof module !== 'undefined') module.exports = api;
})();
