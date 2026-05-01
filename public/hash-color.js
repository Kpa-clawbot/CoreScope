/* hash-color.js — Deterministic HSL color from packet hash
 * IIFE attaching window.HashColor = { hashToHsl }
 * Pure function: no DOM access, no state, works in Node vm.createContext sandbox.
 */
(function() {
  'use strict';

  /**
   * Derive a deterministic HSL color string from a hex hash.
   * @param {string|null|undefined} hashHex - Hex string (e.g. "a1b2c3...")
   * @param {string} theme - "light" or "dark"
   * @returns {string} CSS hsl() string
   */
  function hashToHsl(hashHex, theme) {
    if (!hashHex || hashHex.length < 4) {
      return 'hsl(0, 0%, 50%)';
    }

    // First 2 bytes → hue (0-360)
    var b0 = parseInt(hashHex.slice(0, 2), 16) || 0;
    var b1 = parseInt(hashHex.slice(2, 4), 16) || 0;
    var hue = Math.round(((b0 << 8) | b1) / 65535 * 360);

    var S = 70;
    var L;

    if (theme === 'dark') {
      L = 65;
    } else {
      // Light theme: base L ensures WCAG ≥3.0 contrast against --content-bg (#f4f5f7, style.css:32)
      // Green/cyan zone (hue 45-195) needs lower L due to high perceptual luminance
      if (hue >= 45 && hue <= 195) {
        L = 30;
      } else {
        L = 38;
      }
    }

    return 'hsl(' + hue + ', ' + S + '%, ' + L + '%)';
  }

  // Export
  if (typeof window !== 'undefined') {
    window.HashColor = { hashToHsl: hashToHsl };
  } else if (typeof module !== 'undefined') {
    module.exports = { hashToHsl: hashToHsl };
  }
})();
