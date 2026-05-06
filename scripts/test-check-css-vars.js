#!/usr/bin/env node
/*
 * scripts/test-check-css-vars.js — gates scripts/check-css-vars.js
 *
 * Confirms the JS/HTML extension (#1128 followup M3) catches an
 * undefined CSS variable reference embedded in a JS template literal.
 *
 * Strategy: write a tmp file under a tmp dir alongside one CSS file
 * (so the lint has a defined-vars source set) plus one JS fixture
 * containing `var(--definitely-undefined-xyz)`. Spawn the lint, assert
 * exit code 1, assert the offending var is named in stderr.
 */
'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'check-css-vars-test-'));
const lintPath = path.join(__dirname, 'check-css-vars.js');
let pass = 0, fail = 0;

function check(name, cond, info) {
  if (cond) { console.log('PASS', name); pass++; }
  else { console.error('FAIL', name, info || ''); fail++; }
}

try {
  // 1. Baseline: a CSS file that defines --foo, a JS file that uses it.
  fs.writeFileSync(path.join(tmp, 'site.css'), ':root { --foo: red; }\n.x { color: var(--foo); }\n');
  fs.writeFileSync(path.join(tmp, 'app.js'), 'const s = `<div style="color:var(--foo)">x</div>`;\n');
  let r = spawnSync('node', [lintPath, '--dir', tmp], { encoding: 'utf8' });
  check('clean tree exits 0', r.status === 0, r.stderr || r.stdout);

  // 2. Add a JS file with an undefined var; lint MUST exit 1 and name it.
  fs.writeFileSync(path.join(tmp, 'broken.js'),
    'const s = `<span style="background: var(--definitely-undefined-xyz)">x</span>`;\n');
  r = spawnSync('node', [lintPath, '--dir', tmp], { encoding: 'utf8' });
  check('JS-side undefined var → exit 1', r.status === 1, 'got status=' + r.status);
  check('error names the offending var',
    /--definitely-undefined-xyz/.test(r.stderr),
    'stderr=' + r.stderr);
  check('error names the offending file',
    /broken\.js/.test(r.stderr),
    'stderr=' + r.stderr);

  // 3. HTML inline style is also caught.
  fs.unlinkSync(path.join(tmp, 'broken.js'));
  fs.writeFileSync(path.join(tmp, 'page.html'),
    '<!doctype html><div style="color: var(--also-undefined-html)">x</div>\n');
  r = spawnSync('node', [lintPath, '--dir', tmp], { encoding: 'utf8' });
  check('HTML-side undefined var → exit 1', r.status === 1, 'got status=' + r.status);
  check('html error names offending var',
    /--also-undefined-html/.test(r.stderr), r.stderr);

  // 4. Fallback form is tolerated.
  fs.unlinkSync(path.join(tmp, 'page.html'));
  fs.writeFileSync(path.join(tmp, 'safe.js'),
    'const s = `<div style="color:var(--maybe-undef, red)">x</div>`;\n');
  r = spawnSync('node', [lintPath, '--dir', tmp], { encoding: 'utf8' });
  check('fallback var() form → exit 0', r.status === 0, r.stderr || r.stdout);
} finally {
  // Cleanup tmp dir.
  for (const f of fs.readdirSync(tmp)) fs.unlinkSync(path.join(tmp, f));
  fs.rmdirSync(tmp);
}

console.log('\n=== test-check-css-vars: ' + pass + ' pass, ' + fail + ' fail ===');
process.exit(fail > 0 ? 1 : 0);
