/**
 * test-a11y-axe-1668.js — Milestone 5 of #1668
 *
 * axe-core CI gate. Loads every major CoreScope route in dark + light theme,
 * injects axe-core, runs the `color-contrast` rule, and asserts zero
 * violations (modulo `tests/a11y-allowlist.yaml`).
 *
 * Scope per M5 brief:
 *   - Rules:    color-contrast ONLY (M6 owns the expanded ruleset)
 *   - Themes:   dark + light
 *   - Viewport: 1200x900 desktop (mobile = M6)
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
const ROUTES = [
  '/',
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
  '/analytics?tab=airtime',
  '/audio-lab',
  '/customize',
  '/replay',
];

const THEMES = ['dark', 'light'];

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
  const entries = parseAllowlistYaml(raw);
  const today = new Date().toISOString().slice(0, 10);
  const valid = [];
  for (const e of entries) {
    if (!e.route || !e.selector || !e.rule || !e.issue || !e.expires_at) {
      console.warn(`[allowlist] REFUSED (missing required field): ${JSON.stringify(e)}`);
      continue;
    }
    if (String(e.expires_at) < today) {
      console.warn(`[allowlist] REFUSED (expired ${e.expires_at}, issue #${e.issue}): ${e.route} ${e.selector}`);
      continue;
    }
    valid.push(e);
  }
  return valid;
}

function violationAllowed(route, rule, node, allowlist) {
  // axe node.target is an array of selector arrays (per-frame). Match if any
  // listed selector starts-with or equals the allowlist selector.
  const targets = (node.target || []).flat ? node.target.flat() : [].concat(...(node.target || []));
  for (const entry of allowlist) {
    if (entry.route !== route) continue;
    if (entry.rule !== rule) continue;
    for (const t of targets) {
      if (typeof t !== 'string') continue;
      if (t === entry.selector || t.includes(entry.selector)) return true;
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

async function runRoute(page, route, theme, AxeBuilder) {
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

  const axe = new AxeBuilder({ page })
    .withRules(['color-contrast']);
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
  console.log(`a11y-axe-1668: routes=${routesToRun.length} themes=${THEMES.length} cells=${routesToRun.length * THEMES.length}`);

  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });

  const summary = []; // { route, theme, raw, suppressed, net }
  let totalNet = 0;

  try {
    for (const theme of THEMES) {
      // One context per theme — keeps the init-script localStorage stable.
      const context = await browser.newContext({ viewport: { width: 1200, height: 900 } });
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
          const result = await runRoute(page, route, theme, AxeBuilder);
          for (const v of result.violations) {
            if (v.id !== 'color-contrast') continue; // narrow safeguard
            for (const node of v.nodes) {
              raw++;
              if (violationAllowed(route, v.id, node, allowlist)) {
                suppressed++;
              } else {
                net++;
                violationsDetail.push({
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

        const cell = { route, theme, raw, suppressed, net };
        summary.push(cell);
        totalNet += net;
        const verdict = net === 0 ? '✅' : '❌';
        console.log(`  ${verdict} ${theme.padEnd(5)} ${route.padEnd(34)} raw=${raw} suppressed=${suppressed} net=${net}`);
        if (net > 0) {
          for (const d of violationsDetail) {
            console.log(`     - ${JSON.stringify(d).slice(0, 500)}`);
          }
          const safe = `${theme}_${route.replace(/[^a-z0-9]+/gi, '_')}`;
          const shot = path.join(SHOT_DIR, `${safe}.png`);
          try { await page.screenshot({ path: shot, fullPage: false }); } catch (_) {}
        }
        await page.close();
      }
      await context.close();
    }
  } finally {
    await browser.close();
  }

  console.log('');
  console.log(`a11y-axe-1668: SUMMARY net=${totalNet} cells=${summary.length}`);
  for (const c of summary) {
    if (c.net > 0) {
      console.log(`  FAIL ${c.theme} ${c.route} net=${c.net}`);
    }
  }
  if (totalNet > 0) {
    console.error(`\nFAIL: ${totalNet} color-contrast violation(s) above allowlist`);
    process.exit(1);
  }
  console.log(`\nPASS: zero color-contrast violations across ${summary.length} cells`);
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
  violationAllowed,
  ROUTES,
  THEMES,
};
