#!/usr/bin/env node
// test-preflight-xss-gate.js — exercises scripts/check-xss-sinks.sh against
// the testdata/preflight-xss fixtures. Asserts the bad fixtures HARD-FAIL
// (exit 1) and the good fixtures pass (exit 0).
//
// This is the repo-side validation of the canonical pr-preflight gate
// documented at ~/.openclaw/skills/pr-preflight/scripts/check-xss-sinks.sh.
// The skill-side script enforces the gate at PR-creation time; this test
// guards against regressions in the local mirror at scripts/check-xss-sinks.sh.
//
// Each fixture line is a behavioral assertion:
//   bad-* MUST fail   — proves the gate catches the unescaped sink class.
//   good-* MUST pass  — proves the gate doesn't false-positive on escaped
//                        or test-covered sinks.
//
// Exit 1 on any assertion failure.

'use strict';
const { spawnSync } = require('child_process');
const path = require('path');
const fs = require('fs');

const SCRIPT = path.resolve(__dirname, 'scripts/check-xss-sinks.sh');
const FIXTURE_DIR = path.resolve(__dirname, 'testdata/preflight-xss');

if (!fs.existsSync(SCRIPT)) {
  console.error(`FAIL: ${SCRIPT} missing`);
  process.exit(1);
}
if (!fs.existsSync(FIXTURE_DIR)) {
  console.error(`FAIL: ${FIXTURE_DIR} missing`);
  process.exit(1);
}

const cases = [
  // bad fixtures: gate MUST flag (exit 1)
  { file: 'bad-1-template-literal.js', expect: 1, label: 'innerHTML template literal with ${name}' },
  { file: 'bad-2-setAttribute-href.js', expect: 1, label: "setAttribute('href', `…${hash}…`)" },
  { file: 'bad-3-bindPopup.js',         expect: 1, label: 'Leaflet bindPopup(`…${observer}…`)' },
  // good fixtures: gate MUST pass (exit 0)
  { file: 'good-1-escaped.js', expect: 0, label: 'escapeHtml(${name}) wrapper' },
  { file: 'good-2-tested.js',  expect: 0, label: 'unescaped but DOM-grep-tested in same PR' },
];

let failed = 0;
for (const c of cases) {
  const target = path.join(FIXTURE_DIR, c.file);
  if (!fs.existsSync(target)) {
    console.error(`FAIL: fixture missing: ${target}`);
    failed++;
    continue;
  }
  // Pass --pr-test-files so good-2-tested.js can see its companion test.
  const testFile = path.join(FIXTURE_DIR, 'test-good-2.js');
  const env = Object.assign({}, process.env, {
    PREFLIGHT_TEST_FILES: testFile,
  });
  const res = spawnSync('bash', [SCRIPT, '--file', target], {
    env,
    encoding: 'utf8',
  });
  const got = res.status;
  if (got !== c.expect) {
    console.error(`FAIL: ${c.file} (${c.label}) — expected exit ${c.expect}, got ${got}`);
    if (res.stdout) console.error('  stdout:', res.stdout.trim());
    if (res.stderr) console.error('  stderr:', res.stderr.trim());
    failed++;
  } else {
    console.log(`PASS: ${c.file} — ${c.label} (exit ${got})`);
  }
}

if (failed > 0) {
  console.error(`\n${failed} assertion(s) failed.`);
  process.exit(1);
}
console.log('\nAll preflight-xss-gate assertions passed.');
