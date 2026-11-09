#!/usr/bin/env node
/* The no-restricted-syntax rule in .eslintrc.json keeps every request to the
 * CoreScope server on CSApi (public/api-base.js), so the UI also works from a
 * static host or CDN. This lints snippets as a file in public/ with the
 * repository's own config and checks exactly which lines the rule reports:
 * the ways around CSApi must be reported, and CSApi itself, api(),
 * CSAuth.request and plain app links must not. public/api-base.js is the one
 * file exempt. It also applies the rule to the inline scripts and markup of
 * public/*.html, which `eslint public/*.js` never sees.
 *
 * Needs eslint (a devDependency), so it runs in the E2E job, which does
 * npm ci, rather than in test-all.sh.
 */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { ESLint } = require('eslint');

const ROOT = path.join(__dirname, '..', '..');
const RULE = 'no-restricted-syntax';

// [statement, reported]: one statement per line of the linted text.
const CASES = [
  ['fetch(u);', true],
  ['window.fetch(u);', true],
  ['globalThis.fetch(u);', true],
  ['var x = new XMLHttpRequest();', true],
  ["navigator.sendBeacon('/api/perf', 'x');", true],
  ["var es = new EventSource('/api/stream');", true],
  ["var ws = new WebSocket('ws://' + location.host + '/ws');", true],
  ["var h1 = '<a href=\"/api/export\">';", true],
  ["var h2 = '<a href=\\'/api/export\\'>';", true],
  ['var h3 = `<a href="/api/export/${u}">`;', true],
  ["var h4 = '<img src=\"/api/nodes/' + u + '/avatar\">';", true],
  ["var h5 = '<form action=\"/api/decode\">';", true],
  ["location.href = '/api/export?x=1';", true],
  ["a.href = '/api/export/' + u;", true],
  ['a.src = `/api/export/${u}`;', true],
  ["window.open('/api/export');", true],
  ["a.href = '/api/nodes/' + u + '/avatar';", true],
  ["a.href = '/api' + u;", true],
  ["var h9 = '<a href=\"/api' + u + '\">';", true],
  ["a.setAttribute('href', '/api/export');", true],
  ["a.setAttribute('src', '/api/nodes/' + u + '/avatar');", true],
  ["a.setAttribute('action', `/api/decode?x=${u}`);", true],
  ["location.assign('/api/export');", true],
  ["window.location.replace('/api/export/' + u);", true],
  ["window.location = '/api/export';", true],
  ["CSApi.fetch('/api/nodes');", false],
  ["CSApi.postJSON('/api/decode', {});", false],
  ["api('/nodes?limit=5');", false],
  ["CSAuth.request('GET', '/api/account');", false],
  ["var ws2 = new WebSocket(CSApi.wsUrl('/ws'));", false],
  ["a.href = CSApi.url('/api/export');", false],
  ['var h6 = `<a href="${CSApi.url(\'/api/export\')}">`;', false],
  ["var h7 = '<a href=\"#/nodes\">';", false],
  ["var h8 = '<img src=\"/icons/x.svg\">';", false],
  ["location.href = '#/nodes';", false],
  ["a.setAttribute('href', CSApi.url('/api/export'));", false],
  ["a.setAttribute('title', '/api/export');", false],
  ["location.assign('#/nodes');", false],
  ["var p = u.replace('/api/', '');", false],
  ["a.href = '/apiary';", false],
];
const HEADER = ['/* global api */', "var u = '/api/nodes', a = document.createElement('a');"];

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// A bare /api path in a URL attribute of a page's markup.
const API_ATTR = /\b(href|src|action|formaction)\s*=\s*["']?\/api(\/|["'\s>])/i;

(async () => {
  // Unused disable directives are reported too, so an exception cannot outlive
  // the call it excused.
  const eslint = new ESLint({ cwd: ROOT, reportUnusedDisableDirectives: 'error' });
  async function lint(text, file) {
    const [result] = await eslint.lintText(text, { filePath: path.join(ROOT, 'public', file) });
    const fatal = result.messages.find((m) => m.fatal);
    if (fatal) throw new Error(file + ': ' + fatal.message);
    return result.messages.filter((m) => m.ruleId === RULE || (m.ruleId === null && /eslint-disable/.test(m.message)));
  }

  console.log('Lint rule: network calls go through CSApi');
  const reported = new Set((await lint(HEADER.concat(CASES.map((c) => c[0])).join('\n') + '\n', 'zz-lint-probe.js'))
    .map((m) => m.line));
  CASES.forEach(([statement, bad], i) => {
    test((bad ? 'reports ' : 'allows ') + statement, () => {
      assert.strictEqual(reported.has(HEADER.length + 1 + i), bad);
    });
  });

  const exempt = await lint("fetch(u);\nvar ws = new WebSocket('ws://x/ws');\n", 'api-base.js');
  test('public/api-base.js is exempt', () => assert.deepStrictEqual(exempt.map((m) => m.line), []));

  // `eslint public/*.js` never sees the pages' inline scripts or markup. Lint
  // each inline script with the same rule, its lines kept, and check the
  // markup's own URL attributes.
  console.log('\nInline scripts and markup of public/*.html');
  const SCRIPT = /<script\b([^>]*)>([\s\S]*?)<\/script>/gi;
  const pages = fs.readdirSync(path.join(ROOT, 'public')).filter((f) => f.endsWith('.html')).sort();
  for (const page of pages) {
    const src = fs.readFileSync(path.join(ROOT, 'public', page), 'utf8');
    const problems = [];
    let m, n = 0;
    while ((m = SCRIPT.exec(src))) {
      if (/\bsrc\s*=/i.test(m[1])) continue;
      const before = src.slice(0, m.index + m[0].indexOf('>') + 1).split('\n').length - 1;
      const found = await lint('\n'.repeat(before) + m[2], page.replace(/\.html$/, '') + '-inline-' + (n++) + '.js');
      found.forEach((f) => problems.push(page + ':' + f.line + ' ' + f.message.split(':')[0]));
    }
    const markup = src.replace(SCRIPT, '');
    (markup.match(new RegExp(API_ATTR.source, 'gi')) || []).forEach((x) => problems.push(page + ' markup: ' + x));
    test(page + ': inline scripts and markup reach the server only through CSApi', () =>
      assert.deepStrictEqual(problems, []));
  }
  const probe = await lint('\n'.repeat(4) + "fetch('/api/x');\n// eslint-disable-next-line no-restricted-syntax\nvar ok = 1;\n", 'zz-page-probe.js');
  test('the page check reports at the page line, and a stale eslint-disable too', () =>
    assert.deepStrictEqual(probe.map((f) => f.line), [5, 6]));
  test('the markup check sees /api in href, src and action, and not elsewhere', () => {
    ['<a href="/api/export">', "<img src='/api/x'>", '<form action=/api/decode>', '<a href="/api">']
      .forEach((s) => assert.ok(API_ATTR.test(s), s));
    ['<a href="#/api/x">', '<code>/api/nodes</code>', '<a href="/apiary">'].forEach((s) => assert.ok(!API_ATTR.test(s), s));
  });

  console.log(`\nlint network calls: ${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
