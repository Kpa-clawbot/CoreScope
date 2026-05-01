/* hash-color.js — Deterministic HSL color from packet hash
 * IIFE attaching window.HashColor = { hashToHsl, hashToOutline }
 * Pure function: no DOM access, no state, works in Node vm.createContext sandbox.
 */
(function() {
  'use strict';

  /**
   * Derive a deterministic HSL color string from a hex hash.
   * Uses bytes 0-1 for hue, byte 2 for saturation, byte 3 for lightness.
   * Produces bright vivid fills; contrast is provided by a dark outline (hashToOutline).
   * @param {string|null|undefined} hashHex - Hex string (e.g. "a1b2c3d4...")
   * @param {string} theme - "light" or "dark"
   * @returns {string} CSS hsl() string
   */
  function hashToHsl(hashHex, theme) {
    if (!hashHex || hashHex.length < 8) {
      return 'hsl(0, 0%, 50%)';
    }

    var b0 = parseInt(hashHex.slice(0, 2), 16) || 0;
    var b1 = parseInt(hashHex.slice(2, 4), 16) || 0;
    var b2 = parseInt(hashHex.slice(4, 6), 16) || 0;
    var b3 = parseInt(hashHex.slice(6, 8), 16) || 0;

    // Hue: 0-360 from bytes 0-1 (16-bit)
    var hue = Math.round(((b0 << 8) | b1) / 65535 * 360);
    // Saturation: 55-95% from byte 2
    var S = 55 + Math.round(b2 / 255 * 40);
    // Lightness: vivid range per theme from byte 3
    // Light: 50-65%, Dark: 55-72%
    var L;
    if (theme === 'dark') {
      L = 55 + Math.round(b3 / 255 * 17);
    } else {
      L = 50 + Math.round(b3 / 255 * 15);
    }

    return 'hsl(' + hue + ', ' + S + '%, ' + L + '%)';
  }

  /**
   * Derive a dark outline color (same hue) for contrast against backgrounds.
   * @param {string|null|undefined} hashHex - Hex string
   * @param {string} theme - "light" or "dark"
   * @returns {string} CSS hsl() string
   */
  function hashToOutline(hashHex, theme) {
    if (!hashHex || hashHex.length < 8) {
      return 'hsl(0, 0%, 30%)';
    }

    var b0 = parseInt(hashHex.slice(0, 2), 16) || 0;
    var b1 = parseInt(hashHex.slice(2, 4), 16) || 0;
    var hue = Math.round(((b0 << 8) | b1) / 65535 * 360);

    // Dark outline: same hue, low lightness for contrast
    if (theme === 'dark') {
      return 'hsl(' + hue + ', 30%, 15%)';
    }
    return 'hsl(' + hue + ', 70%, 25%)';
  }

  // Export
  if (typeof window !== 'undefined') {
    window.HashColor = { hashToHsl: hashToHsl, hashToOutline: hashToOutline };
  } else if (typeof module !== 'undefined') {
    module.exports = { hashToHsl: hashToHsl, hashToOutline: hashToOutline };
  }
})();
