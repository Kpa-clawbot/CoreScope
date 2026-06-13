/**
 * test-a11y-axe-1668-selftest.js
 *
 * Deterministic, browser-free unit test for the M5 axe gate's
 * allowlist parser and shape. Runs in <100ms on any Node host.
 *
 * The full axe browser run (test-a11y-axe-1668.js) executes in the
 * Playwright E2E block after the fixture server + chromium are up.
 * THIS file guards the gate's metadata: route list, theme list,
 * allowlist parser, and the "expires_at refuses suppression" policy.
 */

'use strict';

const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');

const mod = require('./test-a11y-axe-1668.js');

// ---- routes / themes --------------------------------------------------------
assert.ok(Array.isArray(mod.ROUTES), 'ROUTES must be an array');
assert.ok(mod.ROUTES.length >= 14, `ROUTES too small: ${mod.ROUTES.length}`);
assert.deepStrictEqual(mod.THEMES, ['dark', 'light'], 'THEMES must be [dark,light]');

// ---- M6: viewports + per-viewport rulesets ---------------------------------
assert.ok(Array.isArray(mod.VIEWPORTS), 'VIEWPORTS must be an array');
assert.strictEqual(mod.VIEWPORTS.length, 2, 'M6: VIEWPORTS must have desktop + mobile');
const vpDesktop = mod.VIEWPORTS.find(v => v.name === 'desktop');
const vpMobile  = mod.VIEWPORTS.find(v => v.name === 'mobile');
assert.ok(vpDesktop, 'VIEWPORTS missing desktop');
assert.ok(vpMobile,  'VIEWPORTS missing mobile');
assert.strictEqual(vpDesktop.w, 1200, 'desktop width must be 1200');
assert.strictEqual(vpDesktop.h, 900,  'desktop height must be 900');
assert.strictEqual(vpMobile.w,  375,  'mobile width must be 375');
assert.strictEqual(vpMobile.h,  812,  'mobile height must be 812');
assert.ok(Array.isArray(vpDesktop.rules) && vpDesktop.rules.length > 0, 'desktop.rules must be a non-empty array');
assert.ok(Array.isArray(vpMobile.rules)  && vpMobile.rules.length  > 0, 'mobile.rules must be a non-empty array');
// M6: every gated viewport MUST include color-contrast (mobile color-contrast is
// the M6 promise) and the new rules must be present on both.
for (const vp of mod.VIEWPORTS) {
  for (const required of ['color-contrast', 'image-alt', 'label',
                          'aria-required-attr', 'region']) {
    assert.ok(vp.rules.includes(required),
      `viewport ${vp.name} must include rule "${required}"`);
  }
}
// And both viewports' rule arrays must match the exported RULES_* constants
// (anti-drift: prevents someone hand-editing one but not the other).
assert.deepStrictEqual(vpDesktop.rules, mod.RULES_DESKTOP, 'desktop.rules drift vs RULES_DESKTOP');
assert.deepStrictEqual(vpMobile.rules,  mod.RULES_MOBILE,  'mobile.rules drift vs RULES_MOBILE');

// Spot-check key routes from the M1 audit baseline
for (const r of ['/', '/packets', '/nodes', '/live', '/map', '/analytics?tab=collisions', '/audio-lab']) {
  assert.ok(mod.ROUTES.includes(r), `ROUTES missing ${r}`);
}

// #1706: ROUTES must cover ALL analytics tabs registered in REGISTERED_ANALYTICS_TABS.
// The gate previously covered 7 of 14 analytics tabs (overview, rf, topology,
// channels, hashsizes, collisions, roles) — the other 7 plus prefix-tool could
// regress on contrast/aria without CI noticing. This assertion enforces full
// coverage so any new tab added to analytics.js forces a ROUTES entry too.
for (const tab of mod.REGISTERED_ANALYTICS_TABS) {
  const route = `/analytics?tab=${tab}`;
  assert.ok(
    mod.ROUTES.includes(route),
    `#1706: ROUTES missing analytics tab coverage for "${route}" — every REGISTERED_ANALYTICS_TABS entry must be gated`
  );
}

// ---- ROUTES reciprocity vs registered pages / analytics tabs ----------------
// Forces a build break if someone adds a route without covering it, OR removes
// a registered page/tab without dropping it from ROUTES (silent skip).
function basePageOf(route) {
  // '/' → '' (SPA default = packets, but '/' is fine as a registered alias)
  // '/analytics?tab=rf' → 'analytics'
  // '/audio-lab'        → 'audio-lab'
  const stripped = route.replace(/^\//, '').split('?')[0];
  return stripped;
}
function tabOf(route) {
  const q = route.split('?')[1];
  if (!q) return null;
  const m = q.match(/(?:^|&)tab=([^&]+)/);
  return m ? decodeURIComponent(m[1]) : null;
}
for (const r of mod.ROUTES) {
  const base = basePageOf(r);
  if (base === '') continue; // '/' = SPA default, no registerPage needed
  assert.ok(
    mod.REGISTERED_PAGES.includes(base),
    `ROUTES contains "${r}" but base page "${base}" is NOT in REGISTERED_PAGES — drop it or register it`
  );
  const tab = tabOf(r);
  if (tab !== null) {
    assert.ok(
      mod.REGISTERED_ANALYTICS_TABS.includes(tab),
      `ROUTES contains "${r}" but analytics tab "${tab}" is NOT in REGISTERED_ANALYTICS_TABS — drop it or add the case`
    );
  }
}

// Cross-check REGISTERED_* against actual source code so the constant cannot
// drift silently from `registerPage()` calls or analytics tab `case` arms.
const repoRoot = __dirname;
const publicDir = path.join(repoRoot, 'public');
if (fs.existsSync(publicDir)) {
  const files = fs.readdirSync(publicDir).filter(f => f.endsWith('.js'));
  const registeredFromSource = new Set();
  for (const f of files) {
    const src = fs.readFileSync(path.join(publicDir, f), 'utf8');
    const re = /registerPage\(\s*['"]([^'"]+)['"]/g;
    let m;
    while ((m = re.exec(src)) !== null) registeredFromSource.add(m[1]);
  }
  // REGISTERED_PAGES MUST be a subset of what the source actually registers.
  for (const p of mod.REGISTERED_PAGES) {
    assert.ok(
      registeredFromSource.has(p),
      `REGISTERED_PAGES has "${p}" but no registerPage('${p}', ...) call found in public/*.js`
    );
  }
  // Analytics tabs: grep `case 'X':` arms from analytics.js
  const analyticsSrc = fs.readFileSync(path.join(publicDir, 'analytics.js'), 'utf8');
  const tabsFromSource = new Set();
  const caseRe = /case\s+['"]([a-z][a-z0-9-]*)['"]\s*:/g;
  // Constrain to the dispatch block to avoid grabbing unrelated `case` strings
  // (sorting comparators, key events). Match against the rendering switch only.
  const dispatch = analyticsSrc.match(/switch\s*\(\s*[a-zA-Z_$][\w$]*\s*\)\s*\{[\s\S]*?\n\s*\}/);
  if (dispatch) {
    let m;
    while ((m = caseRe.exec(dispatch[0])) !== null) tabsFromSource.add(m[1]);
    for (const t of mod.REGISTERED_ANALYTICS_TABS) {
      assert.ok(
        tabsFromSource.has(t),
        `REGISTERED_ANALYTICS_TABS has "${t}" but no \`case '${t}':\` arm found in analytics.js dispatch`
      );
    }
  }
}

// ---- parser: empty + flow ---------------------------------------------------
// #1706 finding 4: parser now decorates the returned array with .topLevel
// and .entries properties (back-compat: still array-iterable). Check shape +
// length rather than deepStrictEqual to allow the metadata.
const emptyFlow = mod.parseAllowlistYaml('[]');
assert.ok(Array.isArray(emptyFlow) && emptyFlow.length === 0, 'empty flow list');
assert.deepStrictEqual(emptyFlow.topLevel, {}, 'empty flow has empty topLevel');
const emptyStr = mod.parseAllowlistYaml('');
assert.ok(Array.isArray(emptyStr) && emptyStr.length === 0, 'empty string');
const emptyComment = mod.parseAllowlistYaml('# only a comment\n');
assert.ok(Array.isArray(emptyComment) && emptyComment.length === 0, 'comment-only');

// ---- parser: block list with two entries ------------------------------------
const sample = `
- route: /analytics?tab=channels
  selector: ".some-stale"
  rule: color-contrast
  issue: 1234
  expires_at: 2099-01-01
- route: /packets
  selector: .badge-quirk
  rule: color-contrast
  issue: 5678
  expires_at: 2099-06-01
`;
const parsed = mod.parseAllowlistYaml(sample);
assert.strictEqual(parsed.length, 2, 'parsed two entries');
assert.strictEqual(parsed[0].route, '/analytics?tab=channels');
assert.strictEqual(parsed[0].issue, 1234);
assert.strictEqual(parsed[0].selector, '.some-stale');
assert.strictEqual(parsed[1].route, '/packets');
assert.strictEqual(parsed[1].issue, 5678);

// ---- parser: malformed YAML THROWS (must not silently return []) -----------
// Anti-tautology: reverting parseAllowlistYaml to return [] on parse error
// MUST cause this block to fail. Verified by manual revert during dev.
assert.throws(
  () => mod.parseAllowlistYaml('this is : not : valid : yaml : at : all\n  no list dash'),
  /cannot parse line/i,
  'malformed inline YAML must throw'
);
// And loadAllowlist must propagate the throw end-to-end via a tmpfile.
const tmpMalformed = path.join(os.tmpdir(), `a11y-malformed-${process.pid}.yaml`);
fs.writeFileSync(tmpMalformed, 'this :: is :: garbage\n!!! no\n');
const ALLOWLIST_PATH_ORIG = require.resolve('./test-a11y-axe-1668.js');
// We can't easily redirect ALLOWLIST_PATH without re-requiring; instead call
// parseAllowlistYaml directly on the tmpfile content for end-to-end coverage.
assert.throws(
  () => mod.parseAllowlistYaml(fs.readFileSync(tmpMalformed, 'utf8')),
  /a11y-allowlist\.yaml:/i,
  'malformed YAML from tmpfile must throw via parser'
);
fs.unlinkSync(tmpMalformed);

// ---- violationAllowed: strict equality (no substring) ----------------------
const al = [{ route: '/a', rule: 'color-contrast', selector: '.x' }];
assert.strictEqual(
  mod.violationAllowed('/a', 'color-contrast', { target: ['.x'] }, al),
  true,
  'exact match should suppress'
);
assert.strictEqual(
  mod.violationAllowed('/b', 'color-contrast', { target: ['.x'] }, al),
  false,
  'route mismatch must not suppress'
);
assert.strictEqual(
  mod.violationAllowed('/a', 'color-contrast', { target: ['.y'] }, al),
  false,
  'selector mismatch must not suppress'
);
assert.strictEqual(
  mod.violationAllowed('/a', 'image-alt', { target: ['.x'] }, al),
  false,
  'rule mismatch must not suppress'
);
// Substring rejection: `.btn` MUST NOT suppress `.btn-primary` violations.
const btnAllow = [{ route: '/p', rule: 'color-contrast', selector: '.btn' }];
assert.strictEqual(
  mod.violationAllowed('/p', 'color-contrast', { target: ['.btn-primary'] }, btnAllow),
  false,
  'substring leak: .btn must NOT match .btn-primary'
);
assert.strictEqual(
  mod.violationAllowed('/p', 'color-contrast', { target: ['div > .btn'] }, btnAllow),
  false,
  'substring leak: .btn must NOT match `div > .btn` (different selector string)'
);
assert.strictEqual(
  mod.violationAllowed('/p', 'color-contrast', { target: ['.btn'] }, btnAllow),
  true,
  'exact `.btn` still suppresses `.btn`'
);

// ---- filterAllowlist: boundary + expiry + missing-field --------------------
const TODAY = '2026-06-12';
// Tomorrow accepted
assert.deepStrictEqual(
  mod.filterAllowlist(
    [{ route: '/x', selector: '.s', rule: 'color-contrast', issue: 1, expires_at: '2026-06-13' }],
    TODAY
  ).length,
  1,
  'expires_at tomorrow must be accepted'
);
// Today (boundary) refused
assert.throws(
  () => mod.filterAllowlist(
    [{ route: '/x', selector: '.s', rule: 'color-contrast', issue: 1, expires_at: TODAY }],
    TODAY
  ),
  /REFUSED \(expired/,
  'expires_at == today must throw (boundary)'
);
// Past refused
assert.throws(
  () => mod.filterAllowlist(
    [{ route: '/x', selector: '.s', rule: 'color-contrast', issue: 1, expires_at: '2026-06-11' }],
    TODAY
  ),
  /REFUSED \(expired/,
  'expires_at in past must throw'
);
// Missing field refused
assert.throws(
  () => mod.filterAllowlist(
    [{ route: '/x', selector: '.s', rule: 'color-contrast', issue: 1 /* no expires_at */ }],
    TODAY
  ),
  /missing required field/,
  'missing required field must throw'
);
// Bad today shape refused
assert.throws(
  () => mod.filterAllowlist([], '06/12/2026'),
  /YYYY-MM-DD/,
  'today must be YYYY-MM-DD'
);

// ---- #1706 finding 2: analyticsTabOf helper --------------------------------
assert.strictEqual(mod.analyticsTabOf('/analytics?tab=subpaths'), 'subpaths');
assert.strictEqual(mod.analyticsTabOf('/analytics?tab=neighbor-graph&section=foo'), 'neighbor-graph');
assert.strictEqual(mod.analyticsTabOf('/analytics'), null);
assert.strictEqual(mod.analyticsTabOf('/packets'), null);
assert.strictEqual(mod.analyticsTabOf('/'), null);

// ---- #1706 finding 1: selector_pattern + count_max -------------------------
// Pattern entry requires count_max>0.
assert.throws(
  () => mod.filterAllowlist(
    [{ route: '/x', selector_pattern: '^\\.foo', rule: 'color-contrast', issue: 1, expires_at: '2099-01-01' }],
    '2026-06-12'
  ),
  /selector_pattern requires count_max/,
  'selector_pattern missing count_max must throw'
);
// selector + selector_pattern mutually exclusive.
assert.throws(
  () => mod.filterAllowlist(
    [{ route: '/x', selector: '.s', selector_pattern: '^\\.s', count_max: 5,
       rule: 'color-contrast', issue: 1, expires_at: '2099-01-01' }],
    '2026-06-12'
  ),
  /mutually exclusive/,
  'selector + selector_pattern together must throw'
);
// Invalid regex rejected.
assert.throws(
  () => mod.filterAllowlist(
    [{ route: '/x', selector_pattern: '[bad-regex(', count_max: 5,
       rule: 'color-contrast', issue: 1, expires_at: '2099-01-01' }],
    '2026-06-12'
  ),
  /invalid regex/,
  'invalid selector_pattern regex must throw'
);
// Pattern match: counts up, allowed until count_max, then overflow flagged.
const patAllow = mod.filterAllowlist(
  [{ route: '/x', selector_pattern: '^\\.row:nth-child\\(\\d+\\) \\.badge$',
     count_max: 2, rule: 'color-contrast', issue: 1, expires_at: '2099-01-01' }],
  '2026-06-12'
);
let m1 = mod.matchViolation('/x', 'color-contrast',
  { target: ['.row:nth-child(1) .badge'] }, patAllow);
assert.strictEqual(m1.allowed, true, 'pattern match #1 allowed');
assert.strictEqual(m1.overflow, false, 'pattern match #1 not overflow');
let m2 = mod.matchViolation('/x', 'color-contrast',
  { target: ['.row:nth-child(2) .badge'] }, patAllow);
assert.strictEqual(m2.allowed, true, 'pattern match #2 allowed');
assert.strictEqual(m2.overflow, false, 'pattern match #2 not overflow (== count_max)');
let m3 = mod.matchViolation('/x', 'color-contrast',
  { target: ['.row:nth-child(3) .badge'] }, patAllow);
assert.strictEqual(m3.allowed, true, 'pattern match #3 allowed');
assert.strictEqual(m3.overflow, true, 'pattern match #3 must overflow (> count_max)');
// Pattern that does not match returns allowed=false.
const m4 = mod.matchViolation('/x', 'color-contrast',
  { target: ['.unrelated'] }, patAllow);
assert.strictEqual(m4.allowed, false, 'non-matching target must not be allowed');

// ---- #1706 finding 4: stale-allowlist detection & strict_unused -----------
const staleAllow = mod.filterAllowlist(
  [
    { route: '/used', selector: '.s', rule: 'color-contrast', issue: 1, expires_at: '2099-01-01' },
    { route: '/dead', selector: '.never-matched', rule: 'color-contrast', issue: 2, expires_at: '2099-01-01' },
  ],
  '2026-06-12'
);
// Match the first only.
mod.matchViolation('/used', 'color-contrast', { target: ['.s'] }, staleAllow);
const warnReport = mod.reportStaleAllowlist(staleAllow, { strict_unused: false });
assert.strictEqual(warnReport.stale.length, 1, 'one stale entry');
assert.strictEqual(warnReport.stale[0].route, '/dead', 'stale entry must be /dead');
assert.strictEqual(warnReport.fail, false, 'WARN must not fail by default');
// Promote to strict.
const failReport = mod.reportStaleAllowlist(staleAllow, { strict_unused: true });
assert.strictEqual(failReport.fail, true, 'strict_unused=true must request FAIL');

// ---- #1706 finding 4: top-level YAML config parsed (strict_unused) -------
const cfgYaml = `
strict_unused: true
- route: /a
  selector: .s
  rule: color-contrast
  issue: 1
  expires_at: 2099-01-01
`;
const cfgParsed = mod.parseAllowlistYaml(cfgYaml);
assert.strictEqual(cfgParsed.length, 1, 'cfg+entry parsed length');
assert.strictEqual(cfgParsed.topLevel.strict_unused, true, 'top-level strict_unused parsed');

// ---- #1706 finding 6: expires_at hard-fails CI (defense-in-depth) --------
// Explicit "well before today" test using the date the finding cites.
assert.throws(
  () => mod.filterAllowlist(
    [{ route: '/x', selector: '.s', rule: 'color-contrast', issue: 1, expires_at: '2020-01-01' }],
    '2026-06-12'
  ),
  /REFUSED \(expired/,
  'expires_at 2020-01-01 must hard-fail loader'
);

// ---- repo allowlist file: shape sanity --------------------------------------
const allowPath = path.join(__dirname, 'tests', 'a11y-allowlist.yaml');
assert.ok(fs.existsSync(allowPath), `tests/a11y-allowlist.yaml missing at ${allowPath}`);
const entries = mod.loadAllowlist();
for (const e of entries) {
  assert.ok(e.route && e.selector && e.rule && e.issue && e.expires_at,
    `allowlist entry missing required field: ${JSON.stringify(e)}`);
}
console.log(`PASS: a11y-axe-1668 selftest — routes=${mod.ROUTES.length} themes=${mod.THEMES.length} allowlist=${entries.length}`);
