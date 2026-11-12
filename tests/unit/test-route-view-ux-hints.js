/**
 * Route-view UX hints: on-map paths badge, path-picker discoverability
 * (pulse, one-time toast, hover arrow), and "no GPS" vs "unresolved" counts.
 *
 * buildSidebar/buildLegend are private to their IIFEs and need Leaflet + a
 * DOM, so the count logic is lifted from the REAL source text of
 * public/route-view.js and public/route-render.js and evaluated in a vm;
 * the DOM wiring is covered by source assertions (plus the Playwright run).
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const REPO_ROOT = path.resolve(__dirname, '..', '..');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const read = f => fs.readFileSync(path.join(REPO_ROOT, 'public', f), 'utf8');
const js = read('route-view.js');
const render = read('route-render.js');
const css = read('route-view.css');

// Evaluate the real `var <name> = positions.filter(...)` line against positions.
function countFromSource(src, name, positions) {
  const m = src.match(new RegExp('var ' + name + ' = (positions\\.filter\\(.*?\\)\\.length);'));
  if (!m) return null;
  return vm.runInContext(m[1], vm.createContext({ positions }));
}

const positions = [
  { resolved: true },
  { resolved: false, gpsless: true },
  { resolved: false, gpsless: true },
  { resolved: false },
  { resolved: false, gpsless: false },
  { resolved: true },
];

console.log('\n=== header counts: no GPS vs unresolved ===');
{
  assert(countFromSource(js, 'gpslessCount', positions) === 2, 'route-view: 2 no-GPS hops');
  assert(countFromSource(js, 'unresolvedCount', positions) === 2, 'route-view: 2 unresolved hops (gpsless not double counted)');
  assert(countFromSource(render, 'gpslessCount', positions) === 2, 'route-render: 2 no-GPS hops');
  assert(/\(gpslessCount \? ' · ' \+ gpslessCount \+ ' no GPS' : ''\)/.test(js), 'header shows "N no GPS" only when > 0');
  assert(/\(unresolvedCount \? ' · ' \+ unresolvedCount \+ ' unresolved' : ''\)/.test(js), 'header shows "N unresolved" only when > 0');
  assert(/function buildLegend\(container, resolvedCount, totalCount, gpslessCount\)/.test(render), 'buildLegend takes gpslessCount');
  assert(/' hops resolved' \+\s*\(gpslessCount \? ' \(' \+ gpslessCount \+ ' no GPS\)' : ''\)/.test(render), 'legend reads "R of T hops resolved (G no GPS)"');
  assert(/buildLegend\(container, resolvedCount, total, gpslessCount\)/.test(render), 'buildLegend caller passes gpslessCount');
  assert(/p\.gpsless \? ', no GPS coordinates' : ', unresolved'/.test(render), 'marker aria label distinguishes no GPS from unresolved');
}

console.log('\n=== on-map paths badge ===');
{
  assert(/if \(multiPath\) \{\s*var mapBadge = document\.createElement\('button'\)/.test(js), 'badge is only created for multi-path routes');
  assert(/mapBadge\.type = 'button'/.test(js) && /className = 'mc-rt-map-badge'/.test(js), 'badge is a real button');
  assert(/<b>' \+ hopCount \+ '<\/b> hop/.test(js) && /' shown<\/span> · '/.test(js), 'badge reads "N hops shown"');
  assert(/' observers saw ' \+ uniquePathsCount \+ ' path'/.test(js), 'badge reads "X observers saw Y paths"');
  assert(js.includes('tap to see all') && js.includes('or select a path in the sidebar'), 'badge has the cta and the sidebar hint line');
  assert(/mapBadge\.setAttribute\(\s*'aria-label'/.test(js), 'badge has an aria-label');
  assert(/mapRef\.getContainer\(\)/.test(js) && /mapContainerEl\.appendChild\(mapBadge\)/.test(js), 'badge is appended to the map container');
  const click = js.match(/mapBadge\.addEventListener\('click', function \(\) \{([\s\S]*?)\n        \}\);/);
  assert(click && /mc-rt-path-active/.test(click[1]) && /restoreAllPaths\(\)/.test(click[1]) && /mapBadge\.remove\(\)/.test(click[1]),
    'click clears row state, restores all paths and removes the badge');
  const stale = js.indexOf("querySelector('.mc-rt-map-badge')");
  assert(stale > 0 && stale < js.indexOf("var mapBadge = document.createElement"), 'a stale badge is removed before the multi-path check');
  assert(/querySelectorAll\('\.mc-rt-map-badge'\)\.forEach\(function \(el\) \{ el\.remove\(\); \}\)/.test(js), 'teardown removes the badge');
  const td = js.slice(js.indexOf('function teardownIfNavigatedAway'));
  assert(td.indexOf('.mc-rt-map-badge') > 0 && td.indexOf('.mc-rt-map-badge') < td.indexOf('window.removeEventListener(\'hashchange\''), 'badge removal sits inside the navigate-away branch');
  assert(js.includes('sidebar._applyPathView') && js.includes('pathGroupKey(p.path, filterHops)'), 'existing deep-link / pathGroupKey code is intact');
}

console.log('\n=== picker discoverability ===');
{
  assert(/\(multiPath \? ' mc-rt-paths-pulse' : ''\)/.test(js), 'pulse class applies on every multi-path route');
  assert(/\.mc-rt-paths-pulse \{[^}]*animation: mc-rt-paths-pulse-kf 1\.4s ease-in-out 4;/.test(css), 'pulse runs 4 iterations');
  assert(/@media \(prefers-reduced-motion: reduce\) \{\s*\.mc-rt-paths-pulse \{ animation: none; \}/.test(css), 'pulse is disabled under prefers-reduced-motion');
  assert(/localStorage\.getItem\('mc-rt-path-hint-seen'\)/.test(js) && /localStorage\.setItem\('mc-rt-path-hint-seen', '1'\)/.test(js), 'toast is gated by mc-rt-path-hint-seen');
  assert(/try \{ showPathToastHint = !localStorage\.getItem\('mc-rt-path-hint-seen'\); \} catch/.test(js), 'storage read is in try/catch');
  const toast = js.slice(js.indexOf("sidebar.dataset.showPathToastHint === '1'"));
  const block = toast.slice(0, toast.indexOf('console.warn'));
  assert(/Tip: click any path in the list to see it on the map/.test(block), 'toast copy');
  assert(/role', 'status'/.test(block) && /aria-live', 'polite'/.test(block), 'toast has role=status and aria-live=polite');
  assert(/}, 6000\)/.test(block), 'toast removes itself after 6s');
  assert(/className = 'mc-rt-path-toast'/.test(block) && !/style\.cssText|\.style\./.test(block), 'toast is styled by class, no inline style');
  assert(!/#[0-9a-fA-F]{3,6}|--mc-bg-secondary|--mc-text-primary/.test(block), 'toast has no hardcoded colours or undefined vars');
  const toastCss = css.match(/\.mc-rt-path-toast \{[^}]*\}/)[0];
  assert(/var\(--surface-2/.test(toastCss) && /var\(--text,/.test(toastCss) && /var\(--border/.test(toastCss), 'toast CSS uses --surface-2/--text/--border');
  assert(/\.mc-rt-path-row::after \{[^}]*content: '\\2192'[^}]*opacity: 0/.test(css) &&
    /\.mc-rt-path-row:focus::after \{\s*opacity: 1/.test(css), 'row arrow is revealed on hover and focus');
  assert(/\.mc-rt-path-row:hover,\s*\.mc-rt-path-row:focus \{\s*background/.test(css), 'row has a :focus background');
  assert(/\.mc-rt-paths-header \{[^}]*color: var\(--text,/.test(css), 'picker instruction text uses --text');
}

console.log('\n=== no new hardcoded colours ===');
{
  // Hex colours that are NOT a var(--x, #fallback) fallback.
  const bare = s => (s.replace(/var\(--[a-z0-9-]+, *#[0-9a-fA-F]{3,6}\)/gi, '').match(/#[0-9a-fA-F]{3,6}/g) || []).length;
  const BASELINE_BARE_HEX = 8; // bare hex colours in master's route-view.css before this feature
  const after = bare(css);
  assert(after <= BASELINE_BARE_HEX, 'bare hex colours in route-view.css did not increase (' + BASELINE_BARE_HEX + ' -> ' + after + ')');
}

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed === 0 ? 0 : 1);
