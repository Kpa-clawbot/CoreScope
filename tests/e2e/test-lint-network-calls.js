#!/usr/bin/env node
/* The no-restricted-syntax rule in .eslintrc.json keeps every request to the
 * CoreScope server on CSApi (public/api-base.js), so the UI also works from a
 * static host or CDN. This lints snippets as a file in public/ with the
 * repository's own config and checks exactly which lines the rule reports:
 * the ways around CSApi must be reported, and CSApi itself, api(),
 * CSAuth.request and plain app links must not. public/api-base.js is the one
 * file exempt.
 *
 * Needs eslint (a devDependency), so it runs in the E2E job, which does
 * npm ci, rather than in test-all.sh.
 */
'use strict';
const assert = require('assert');
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
];
const HEADER = ['/* global api */', "var u = '/api/nodes', a = document.createElement('a');"];

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

(async () => {
  const eslint = new ESLint({ cwd: ROOT });
  async function lint(text, file) {
    const [result] = await eslint.lintText(text, { filePath: path.join(ROOT, 'public', file) });
    const fatal = result.messages.find((m) => m.fatal);
    if (fatal) throw new Error(file + ': ' + fatal.message);
    return result.messages.filter((m) => m.ruleId === RULE);
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

  console.log(`\nlint network calls: ${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
