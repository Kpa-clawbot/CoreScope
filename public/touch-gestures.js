/* public/touch-gestures.js — stub for #1062 red commit.
 * Minimal init counter so the test executes and fails on behavior assertions
 * (not on missing global). Real implementation lands in the green commit.
 */
(function () {
  'use strict';
  if (typeof window === 'undefined') return;
  if (typeof window.__touchGestures1062InitCount !== 'number') {
    window.__touchGestures1062InitCount = 0;
  }
  window.__touchGestures1062InitCount += 1;
  window.TouchGestures = window.TouchGestures || {
    dismissRowAction: function () {},
  };
})();
