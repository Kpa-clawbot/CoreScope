/**
 * test-a11y-axe-routes-coverage.js — Gate for #1706
 *
 * Asserts that EVERY analytics tab declared in `public/analytics.js`
 * (via `<button class="tab-btn" data-tab="...">`) is covered by the
 * axe-core CI gate's ROUTES list as `/analytics?tab=<tab>`.
 *
 * The existing selftest checks REGISTERED_ANALYTICS_TABS ⊆ analytics.js
 * dispatch arms, but does NOT enforce that ROUTES exercises every tab.
 * That gap let 7+ tabs ship un-gated (issue #1706). This file closes it:
 * any future `data-tab=` button added without a matching ROUTES entry
 * fails CI on this assertion, blocking silent coverage drift.
 *
 * Runs in <100ms — no browser, no playwright, pure source grep.
 */

'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');

const mod = require('./test-a11y-axe-1668.js');

const analyticsSrc = fs.readFileSync(
  path.join(__dirname, 'public', 'analytics.js'),
  'utf8'
);

// Scrape every `<button class="...tab-btn..." data-tab="X">` from the
// analytics tab bar template literal.
const TAB_RE = /<button[^>]*\bclass="[^"]*\btab-btn\b[^"]*"[^>]*\bdata-tab="([^"]+)"/g;
const declaredTabs = new Set();
let m;
while ((m = TAB_RE.exec(analyticsSrc)) !== null) {
  declaredTabs.add(m[1]);
}

assert.ok(
  declaredTabs.size > 0,
  'expected at least one data-tab="..." button in public/analytics.js — regex broken?'
);

// Build the set of analytics tabs ROUTES exercises.
const routeTabs = new Set();
for (const r of mod.ROUTES) {
  const q = r.split('?')[1];
  if (!q) continue;
  const tm = q.match(/(?:^|&)tab=([^&]+)/);
  if (tm) routeTabs.add(decodeURIComponent(tm[1]));
}

// Every declared tab MUST appear as a ROUTES entry.
const missing = [...declaredTabs].filter(t => !routeTabs.has(t)).sort();
assert.deepStrictEqual(
  missing,
  [],
  `axe ROUTES missing analytics tabs (issue #1706): ${missing.join(', ')}\n` +
  `declared in public/analytics.js: ${[...declaredTabs].sort().join(', ')}\n` +
  `covered by ROUTES:                ${[...routeTabs].sort().join(', ')}`
);

// And every analytics route MUST correspond to a real declared tab (no typos).
const stale = [...routeTabs].filter(t => !declaredTabs.has(t)).sort();
assert.deepStrictEqual(
  stale,
  [],
  `axe ROUTES references analytics tabs not in public/analytics.js: ${stale.join(', ')}`
);

console.log(
  `PASS: a11y-axe routes coverage — declared=${declaredTabs.size} ` +
  `covered=${routeTabs.size} (all analytics tabs gated)`
);
