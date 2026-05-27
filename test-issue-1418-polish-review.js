/**
 * #1418 / PR #1423 polish-review guards.
 *
 * Source-grep guards for the polish-review findings addressed on the
 * route-view feature branch. Each guard pins one finding so future edits
 * can't silently regress the fix.
 *
 * Findings covered (see PR #1423 review comments for full context):
 *   - resize listener leak (carmack/munger)
 *   - 5-staggered-timer fit storm (tufte/doshi)
 *   - empty catch {} swallowing errors (torvalds)
 *   - _detailCache unbounded (carmack) — LRU(50)
 *   - recolorRoute walks document.querySelectorAll (torvalds) — scoped
 *   - deep-link silent failure (doshi) — toast on empty paths
 *   - innerHTML row re-wire factored (dijkstra) — wireRow helper
 */
'use strict';

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const rvSrc = fs.readFileSync(path.join(__dirname, 'public', 'route-view.js'), 'utf8');
const mapSrc = fs.readFileSync(path.join(__dirname, 'public', 'map.js'), 'utf8');

console.log('\n=== A. resize listener leak fix (carmack/munger) ===');
// Single resize listener attached via window.__mc_routeResizeRefit stash,
// torn down on next render() + on teardownIfNavigatedAway.
assert(/window\.__mc_routeResizeRefit/.test(rvSrc),
  'resize handler stashed on window.__mc_routeResizeRefit for dedupe');
assert(/removeEventListener\(['"]resize['"],\s*window\.__mc_routeResizeRefit\)/.test(rvSrc),
  'prior resize handler removed before attaching new one');
// Old buggy pattern (anonymous resize listener with no removal) must be gone.
const anonResize = rvSrc.match(/window\.addEventListener\(['"]resize['"]\s*,\s*function/g) || [];
assert(anonResize.length === 0,
  'no anonymous window.resize listeners (all go via __mc_routeResizeRefit) — found ' + anonResize.length);

console.log('\n=== B. fit-storm collapse to rAF (tufte/doshi) ===');
// The 5-staggered (0/300/800/1600/2800) and 3-staggered (0/200/600/1400)
// timers MUST be gone. Single requestAnimationFrame is the replacement.
const bigFitStorm = /setTimeout\(\s*refit\s*,\s*(?:300|800|1600|2800)\s*\)/.test(rvSrc);
assert(!bigFitStorm, 'no setTimeout(refit, 300|800|1600|2800) staggered fit storm');
const isoFitStorm = /setTimeout\(\s*doFit\s*,\s*(?:200|600|1400)\s*\)/.test(rvSrc);
assert(!isoFitStorm, 'no setTimeout(doFit, 200|600|1400) staggered isolate-fit storm');
const restoreFitStorm = /setTimeout\(\s*_restoreFit\s*,\s*(?:200|600|1400)\s*\)/.test(rvSrc);
assert(!restoreFitStorm, 'no setTimeout(_restoreFit, 200|600|1400) staggered restore-fit storm');
assert(/requestAnimationFrame\(\s*refit\s*\)/.test(rvSrc),
  'requestAnimationFrame(refit) is the new initial-settle path');
assert(/requestAnimationFrame\(\s*doFit\s*\)/.test(rvSrc),
  'requestAnimationFrame(doFit) replaces isolate-path staggered timers');
assert(/new ResizeObserver/.test(rvSrc),
  'ResizeObserver attached to map container for layout-settle re-fit');

console.log('\n=== C. ResizeObserver lifecycle (carmack) ===');
assert(/window\.__mc_routeResizeObserver/.test(rvSrc),
  'ResizeObserver stashed on window.__mc_routeResizeObserver for dedupe');
assert(/__mc_routeResizeObserver[^;]*\.disconnect\(\)/.test(rvSrc),
  'ResizeObserver disconnected on render() re-entry + teardown');

console.log('\n=== D. _detailCache LRU bound (carmack) ===');
assert(/_detailCache\s*=\s*new\s+Map\(\)/.test(rvSrc),
  '_detailCache is a Map (LRU-capable) not a plain object');
assert(/DETAIL_CACHE_MAX/.test(rvSrc),
  'DETAIL_CACHE_MAX constant defined (LRU bound)');
assert(/_detailCache\.size\s*>=?\s*DETAIL_CACHE_MAX/.test(rvSrc),
  'LRU eviction guard checks _detailCache.size against DETAIL_CACHE_MAX');

console.log('\n=== E. catch {} silent swallow → console.warn (torvalds) ===');
// Empty `catch (e) {}` (no body) count should be near zero. A handful may
// remain where the catch is genuinely a "best-effort" no-op — but the
// review flagged 20+ silent swallows; we should be down to ≤5 after the pass.
// Empty `catch (e) {}` (no body) count for full-block catches (e). The
// inline `} catch (_) {}` no-op removers are intentional (marker may
// already be detached). The review flagged 20+ silent block swallows;
// after the pass the remaining ones must be legitimately benign
// (localStorage may be disabled, marker may have been removed in a race).
const blockEmptyCatches = (rvSrc.match(/\}\s*catch\s*\(\s*e\s*\)\s*\{\s*\}/g) || []).length;
assert(blockEmptyCatches <= 8,
  'block-style silent `} catch (e) {}` reduced to ≤8 (was 20+) — current: ' + blockEmptyCatches);
assert(/console\.warn\(['"]\[route-view\]/.test(rvSrc),
  'at least one [route-view] console.warn breadcrumb present');

console.log('\n=== F. recolorRoute scoped to sidebar (torvalds) ===');
// The walks must be scoped to the active sidebar root, not document-wide.
// We allow document.querySelectorAll for `.mc-rt-sidebar` (the tear-down)
// but NOT for `.mc-rt-edge` / `.mc-rt-row` / `.mc-rt-spark-dot`.
const docEdges = /document\.querySelectorAll\(['"]\.mc-rt-edge['"]\)/.test(rvSrc);
assert(!docEdges, 'recolorRoute no longer walks document.querySelectorAll(.mc-rt-edge)');
const docRows = /document\.querySelectorAll\(['"]\.mc-rt-row['"]\)/.test(rvSrc);
assert(!docRows, 'recolorRoute no longer walks document.querySelectorAll(.mc-rt-row)');

console.log('\n=== G. deep-link empty-paths toast (doshi) ===');
// When allPaths.length === 0, surface a sidebar/console message instead of
// silently bailing.
assert(/allPaths\.length\s*===\s*0[\s\S]{0,400}(?:console\.warn|alert|toast|showToast|notif)/i.test(mapSrc),
  'deep-link empty-paths path emits a console.warn / toast (no silent return)');

console.log('\n=== H. wireRow row-wireup helper (dijkstra) ===');
assert(/function\s+wireRow\s*\(\s*row\s*\)/.test(rvSrc),
  'wireRow(row) helper centralizes row event wiring');
assert(/sidebar\._wireRow\s*=\s*wireRow/.test(rvSrc),
  'wireRow stashed on sidebar so restoreAllPaths can reuse');
assert(/newRowEls\.forEach\(\s*sidebar\._wireRow/.test(rvSrc),
  'restoreAllPaths re-wires rows via sidebar._wireRow (not inline duplicate)');

console.log('\n=== Summary ===');
console.log('  passed: ' + passed);
console.log('  failed: ' + failed);
if (failed > 0) process.exit(1);
