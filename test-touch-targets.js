#!/usr/bin/env node
/* Issue #1060 — touch-targets CSS pass.
 * Verifies the "=== Touch Targets ===" section of public/style.css declares
 * 48px minimums for interactive controls, :active feedback rules, and
 * hover→tap conversion via @media (hover: hover).
 */
'use strict';

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const css = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');

// Extract the Touch Targets section (between its banner and the next === banner).
const startMarker = '/* === Touch Targets === */';
const startIdx = css.indexOf(startMarker);
assert.ok(startIdx >= 0, 'style.css must contain a "=== Touch Targets ===" section');
const afterStart = css.slice(startIdx + startMarker.length);
const nextSection = afterStart.search(/\/\*\s*===\s*[A-Za-z]/);
assert.ok(nextSection > 0, 'Touch Targets section must be followed by another === section');
const section = afterStart.slice(0, nextSection);

function has(re, msg) {
  assert.ok(re.test(section), `Touch Targets section missing: ${msg}\n--- section ---\n${section}\n---`);
}

// 48px minimums on the major interactive controls called out by the issue.
has(/\.btn[^{]*\{[^}]*min-height:\s*48px/s, '.btn min-height: 48px');
has(/\.btn[^{]*\{[^}]*min-width:\s*48px/s, '.btn min-width: 48px');
has(/\.btn-icon[^{]*\{[^}]*min-height:\s*48px/s, '.btn-icon min-height: 48px');
has(/\.nav-btn[^{]*\{[^}]*min-height:\s*48px/s, '.nav-btn min-height: 48px');
has(/\.ch-icon-btn[^{]*\{[^}]*min-height:\s*48px/s, '.ch-icon-btn min-height: 48px');
has(/\.ch-gear-btn[^{]*\{[^}]*min-height:\s*48px/s, '.ch-gear-btn min-height: 48px');
has(/\.panel-close-btn[^{]*\{[^}]*min-height:\s*48px/s, '.panel-close-btn min-height: 48px');
has(/\.mc-jump-btn[^{]*\{[^}]*min-height:\s*48px/s, '.mc-jump-btn min-height: 48px');
has(/button\.ch-item[^{]*\{[^}]*min-height:\s*48px/s, 'button.ch-item min-height: 48px');

// Visible :active states on buttons (color/background/transform shift).
has(/:active\s*\{/, 'at least one :active rule');
has(/\.btn:active/, '.btn:active');
has(/\.btn-icon:active/, '.btn-icon:active');
has(/\.nav-btn:active/, '.nav-btn:active');
has(/\.ch-icon-btn:active/, '.ch-icon-btn:active');

// Hover→tap conversion: hover-only feedback gated behind @media (hover: hover)
// so touch devices don't get stuck in the hover state.
has(/@media\s*\(hover:\s*hover\)/, '@media (hover: hover) wrapper for hover-only rules');

// Tap-to-reveal tooltip pattern: .sort-help / .has-tooltip needs :focus-within
// (or :focus) so tap shows the tooltip without requiring hover.
has(/\.sort-help[^{]*:focus(-within)?[^{]*\{/, '.sort-help focus/focus-within rule (tap-to-reveal tooltip)');
has(/\.sort-help[^{]*tabindex|sort-help\b[^{]*\{[^}]*outline/s, 'sort-help focusable styling');

console.log('test-touch-targets.js: OK');
