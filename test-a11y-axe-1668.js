/**
 * test-a11y-axe-1668.js — Milestones 5 + 6 of #1668
 *
 * axe-core CI gate. Loads every major CoreScope route in dark + light theme,
 * injects axe-core, runs the configured ruleset, and asserts zero
 * violations (modulo `tests/a11y-allowlist.yaml`).
 *
 * Scope:
 *   - M5: color-contrast on desktop dark+light at 1200x900.
 *   - M6: expanded ruleset (image-alt, label, aria-required-attr,
 *     aria-valid-attr, aria-valid-attr-value, landmark-one-main, region,
 *     button-name, link-name, document-title, html-has-lang, duplicate-id)
 *     applied across BOTH viewports, PLUS color-contrast at 375x812 mobile.
 *
 *   Themes:    dark + light
 *   Viewports: desktop 1200x900, mobile 375x812 (M6 adds mobile)
 *
 * Allowlist (`tests/a11y-allowlist.yaml`):
 *   Operator-flagged false-positives. Each entry MUST cite an issue # AND
 *   an expires_at date. Expired entries are refused (warning logged, full
 *   failure). Missing fields => refused.
 *
 * Usage:
 *   BASE_URL=http://localhost:13581 node test-a11y-axe-1668.js
 *
 * Env:
 *   BASE_URL          required (server to test against)
 *   CHROMIUM_PATH     optional (else playwright's bundled chromium)
 *   AXE_ROUTES_ONLY   optional comma list of routes to limit (debug)
 *   AXE_SCREENSHOT_DIR  where to write screenshots on failure (default /tmp/axe-1668)
 */

'use strict';

const fs = require('fs');
const path = require('path');
// Lazy-require playwright + @axe-core/playwright inside main() so the
// parser helpers below are unit-testable on hosts without those modules
// (e.g. CI lint passes, or the sanity self-test below).

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const ROUTES_FILTER = (process.env.AXE_ROUTES_ONLY || '').split(',').filter(Boolean);
const SHOT_DIR = process.env.AXE_SCREENSHOT_DIR || '/tmp/axe-1668';
const ALLOWLIST_PATH = path.join(__dirname, 'tests', 'a11y-allowlist.yaml');

// Routes: M1 audit baseline (already proven coverage).
// Hash routes — CoreScope is a SPA, server returns the same shell for any path.
//
// HARD INVARIANT: every entry below MUST resolve to a `registerPage()` page
// in `public/*.js` (or — for `/analytics?tab=X` — to a real `case 'X':` arm
// of the tab dispatch in `public/analytics.js`). The selftest enforces this
// via REGISTERED_PAGES / REGISTERED_ANALYTICS_TABS reciprocity so a removed
// route forces a build break instead of the gate silently skipping coverage.
const ROUTES = [
  '/',                              // SPA default → packets
  '/packets',
  '/nodes',
  '/channels',
  '/live',
  '/map',
  '/observers',
  '/compare',
  '/analytics?tab=overview',
  '/analytics?tab=rf',
  '/analytics?tab=topology',
  '/analytics?tab=channels',
  '/analytics?tab=hashsizes',
  '/analytics?tab=collisions',
  '/analytics?tab=roles',
  '/audio-lab',
];

// Source-of-truth for ROUTES reciprocity. Keep these in sync with the
// `registerPage(...)` calls under `public/` and the `case 'X':` arms in
// `public/analytics.js`. The selftest greps the source to confirm.
const REGISTERED_PAGES = [
  'home', 'packets', 'packet-detail', 'nodes', 'node-analytics', 'node-reach',
  'channels', 'live', 'map', 'observers', 'observer-detail', 'compare',
  'analytics', 'audio-lab', 'perf', 'traces', 'path-inspector', 'tools-landing',
];
const REGISTERED_ANALYTICS_TABS = [
  'overview', 'rf', 'topology', 'channels', 'hashsizes', 'collisions',
  'subpaths', 'nodes', 'distance', 'neighbor-graph', 'rf-health',
  'clock-health', 'roles', 'prefix-tool', 'scopes',
];

const THEMES = ['dark', 'light'];

// M6: ruleset per viewport. Both viewports share the expanded ruleset;
// color-contrast also runs on both (M5 baseline desktop + M6 mobile gate).
// All rules in these arrays MUST be 0 violations against the CI fixture
// (no allowlist seeding — same hard policy as M5).
const RULES_DESKTOP = [
  'color-contrast',
  'image-alt',
  'label',
  'aria-required-attr',
  'aria-valid-attr',
  'aria-valid-attr-value',
  'landmark-one-main',
  'region',
  'button-name',
  'link-name',
  'document-title',
  'html-has-lang',
  'duplicate-id',
];
const RULES_MOBILE = RULES_DESKTOP.slice(); // identical at M6; split arrays let
                                            // a future PR diverge cleanly.
const VIEWPORTS = [
  { name: 'desktop', w: 1200, h: 900, rules: RULES_DESKTOP },
  { name: 'mobile',  w: 375,  h: 812, rules: RULES_MOBILE  },
];

// ---- tiny YAML loader (flow `[]` or block list of `key: value` maps) -------
//
// Stays dependency-free — we only need to parse our own narrow schema.
// Supports:
//   - empty list ([])
//   - block list of inline `- key: value` items continued with `  key: value` lines
//   - quoted strings ('...' or "...")
//   - integers and YYYY-MM-DD dates
function parseAllowlistYaml(src) {
  // strip BOM, comments, normalize line endings
  const lines = src.replace(/^\uFEFF/, '').split(/\r?\n/)
    .map(l => l.replace(/(^|\s)#.*$/, '').replace(/\s+$/, ''))
    .filter(l => l.trim().length > 0);

  if (lines.length === 0) return [];
  if (lines.length === 1 && lines[0].trim() === '[]') return [];

  const entries = [];
  let current = null;
  for (const raw of lines) {
    const m = raw.match(/^(\s*)(-?)\s*([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$/);
    if (!m) {
      throw new Error(`a11y-allowlist.yaml: cannot parse line: ${raw}`);
    }
    const [, , dash, key, valRaw] = m;
    if (dash === '-') {
      if (current) entries.push(current);
      current = {};
    }
    if (!current) throw new Error(`a11y-allowlist.yaml: key "${key}" outside list item`);
    current[key] = coerce(valRaw.trim());
  }
  if (current) entries.push(current);
  return entries;
}

function coerce(v) {
  if (v === '' || v === '~' || v === 'null') return null;
  if (/^-?\d+$/.test(v)) return parseInt(v, 10);
  if (/^'.*'$/.test(v) || /^".*"$/.test(v)) return v.slice(1, -1);
  return v;
}

function loadAllowlist() {
  if (!fs.existsSync(ALLOWLIST_PATH)) return [];
  const raw = fs.readFileSync(ALLOWLIST_PATH, 'utf8');
  // parseAllowlistYaml THROWS on malformed input (don't silently swallow → []).
  // A swallowed parse error would let a typo-mangled allowlist suppress nothing
  // and ship a green gate that no longer reflects operator intent. Loud failure.
  const entries = parseAllowlistYaml(raw);
  const today = new Date().toISOString().slice(0, 10);
  return filterAllowlist(entries, today);
}

// Pure function: filter a parsed allowlist against `today` (YYYY-MM-DD string).
// THROWS on:
//   - any entry missing a required field (route/selector/rule/issue/expires_at)
//   - any entry whose expires_at <= today (today inclusive — boundary fails)
// Returning a soft-filtered subset would let stale suppressions persist; this
// matches PR policy that expired entries are refused as a HARD failure.
function filterAllowlist(entries, today) {
  if (!Array.isArray(entries)) throw new Error('filterAllowlist: entries must be an array');
  if (typeof today !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(today)) {
    throw new Error(`filterAllowlist: today must be YYYY-MM-DD, got ${today}`);
  }
  const valid = [];
  for (const e of entries) {
    if (!e || !e.route || !e.selector || !e.rule || !e.issue || !e.expires_at) {
      throw new Error(`a11y-allowlist.yaml: REFUSED (missing required field): ${JSON.stringify(e)}`);
    }
    if (String(e.expires_at) <= today) {
      throw new Error(`a11y-allowlist.yaml: REFUSED (expired ${e.expires_at} <= today ${today}, issue #${e.issue}): ${e.route} ${e.selector}`);
    }
    valid.push(e);
  }
  return valid;
}

function violationAllowed(route, rule, node, allowlist) {
  // axe node.target is an array of selector arrays (per-frame). Match if any
  // listed selector is STRICTLY EQUAL to the allowlist selector. We deliberately
  // do NOT substring-match: `.btn` must not suppress `.btn-primary` violations,
  // and `body` must not suppress every node in the document.
  const targets = (node.target || []).flat ? node.target.flat() : [].concat(...(node.target || []));
  for (const entry of allowlist) {
    if (entry.route !== route) continue;
    if (entry.rule !== rule) continue;
    for (const t of targets) {
      if (typeof t !== 'string') continue;
      if (t === entry.selector) return true;
    }
  }
  return false;
}

// ---------------------------------------------------------------------------

async function setTheme(page, theme) {
  // Seed localStorage BEFORE the SPA boots so the theme is correct on first paint.
  await page.addInitScript((t) => {
    try {
      localStorage.setItem('meshcore-theme', t);
      // Live page collapses controls by default; keep them visible
      // (matches test-e2e-playwright.js convention).
      localStorage.setItem('live-controls-expanded', 'true');
      // Default time window wide enough to render content.
      localStorage.setItem('meshcore-time-window', '525600');
    } catch (_) { /* ignore */ }
    // Set the attribute pre-paint to avoid a transient mismatch.
    try { document.documentElement.setAttribute('data-theme', t); } catch (_) {}
  }, theme);
}

async function runRoute(page, route, theme, rules, AxeBuilder) {
  const url = `${BASE}/#${route}`;
  await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 30000 });
  // Give the SPA a moment to render. We deliberately do NOT
  // wait for network idle because /live + /map keep sockets open.
  await page.waitForTimeout(1500);

  // Quick sanity: confirm body is visible and theme attr matches
  const themeAttr = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
  if (themeAttr !== theme) {
    // Try toggling explicitly if the SPA reset it (shouldn't happen, but be safe)
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme);
    await page.waitForTimeout(200);
  }

  const axe = new AxeBuilder({ page }).withRules(rules);
  const result = await axe.analyze();
  return result;
}

async function main() {
  const { chromium } = require('playwright');
  const { AxeBuilder } = require('@axe-core/playwright');
  if (!fs.existsSync(SHOT_DIR)) fs.mkdirSync(SHOT_DIR, { recursive: true });
  const allowlist = loadAllowlist();
  console.log(`a11y-axe-1668: BASE=${BASE} allowlist=${allowlist.length} entries`);

  const routesToRun = ROUTES_FILTER.length ? ROUTES.filter(r => ROUTES_FILTER.includes(r)) : ROUTES;
  console.log(`a11y-axe-1668: routes=${routesToRun.length} themes=${THEMES.length} viewports=${VIEWPORTS.length} cells=${routesToRun.length * THEMES.length * VIEWPORTS.length}`);

  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  const summary = []; // { vp, route, theme, raw, suppressed, net }
  let totalNet = 0;
  // Per-viewport tallies for the summary footer.
  const vpTotals = {};
  for (const vp of VIEWPORTS) vpTotals[vp.name] = { raw: 0, suppressed: 0, net: 0 };

  try {
    for (const vp of VIEWPORTS) {
      console.log(`\n--- viewport ${vp.name} ${vp.w}x${vp.h} rules=${vp.rules.length} ---`);
      for (const theme of THEMES) {
        // One context per (viewport, theme) — keeps init-script localStorage stable.
        const context = await browser.newContext({ viewport: { width: vp.w, height: vp.h } });
        await context.addInitScript((t) => {
          try {
            localStorage.setItem('meshcore-theme', t);
            localStorage.setItem('live-controls-expanded', 'true');
            localStorage.setItem('meshcore-time-window', '525600');
            document.documentElement.setAttribute('data-theme', t);
          } catch (_) {}
        }, theme);

        for (const route of routesToRun) {
          const page = await context.newPage();
          let raw = 0, suppressed = 0, net = 0;
          const violationsDetail = [];
          try {
            const result = await runRoute(page, route, theme, vp.rules, AxeBuilder);
            for (const v of result.violations) {
              if (!vp.rules.includes(v.id)) continue; // narrow safeguard
              for (const node of v.nodes) {
                raw++;
                if (violationAllowed(route, v.id, node, allowlist)) {
                  suppressed++;
                } else {
                  net++;
                  violationsDetail.push({
                    rule: v.id,
                    selector: node.target,
                    html: node.html && node.html.slice(0, 200),
                    message: node.failureSummary,
                  });
                }
              }
            }
          } catch (err) {
            // Probe errors should NOT silently pass — treat as a hard failure
            // so route regressions (server 500, hash route 404, JS crash) surface.
            net = 1;
            violationsDetail.push({ probeError: err.message });
          }

          const cell = { vp: vp.name, route, theme, raw, suppressed, net };
          summary.push(cell);
          totalNet += net;
          vpTotals[vp.name].raw += raw;
          vpTotals[vp.name].suppressed += suppressed;
          vpTotals[vp.name].net += net;
          const verdict = net === 0 ? '✅' : '❌';
          console.log(`  ${verdict} ${vp.name.padEnd(7)} ${theme.padEnd(5)} ${route.padEnd(34)} raw=${raw} suppressed=${suppressed} net=${net}`);
          if (net > 0) {
            for (const d of violationsDetail) {
              console.log(`     - ${JSON.stringify(d).slice(0, 500)}`);
            }
            const safe = `${vp.name}_${theme}_${route.replace(/[^a-z0-9]+/gi, '_')}`;
            const shot = path.join(SHOT_DIR, `${safe}.png`);
            try { await page.screenshot({ path: shot, fullPage: false }); } catch (_) {}
          }
          await page.close();
        }
        await context.close();
      }
    }
  } finally {
    await browser.close();
  }

  console.log('');
  console.log(`a11y-axe-1668: SUMMARY net=${totalNet} cells=${summary.length}`);
  for (const vp of VIEWPORTS) {
    const t = vpTotals[vp.name];
    console.log(`  viewport ${vp.name}: raw=${t.raw} suppressed=${t.suppressed} net=${t.net} (${vp.rules.length} rules)`);
  }
  for (const c of summary) {
    if (c.net > 0) {
      console.log(`  FAIL ${c.vp} ${c.theme} ${c.route} net=${c.net}`);
    }
  }
  if (totalNet > 0) {
    console.error(`\nFAIL: ${totalNet} a11y violation(s) above allowlist`);
    process.exit(1);
  }
  console.log(`\nPASS: zero violations across ${summary.length} cells (${VIEWPORTS.length} viewports × ${THEMES.length} themes × ${routesToRun.length} routes)`);
}

if (require.main === module) {
  main().catch((err) => {
    console.error('a11y-axe-1668 fatal:', err && err.stack || err);
    process.exit(2);
  });
}

// Allow consumers (e.g. a CI unit-test step) to import the parser helpers
// without launching a browser.
module.exports = {
  parseAllowlistYaml,
  loadAllowlist,
  filterAllowlist,
  violationAllowed,
  ROUTES,
  THEMES,
  VIEWPORTS,
  RULES_DESKTOP,
  RULES_MOBILE,
  REGISTERED_PAGES,
  REGISTERED_ANALYTICS_TABS,
};
