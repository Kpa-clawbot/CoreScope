/* Every test under tests/unit and tests/e2e must have one explicit home; adding a file cannot skip CI silently. */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');

const assert = require('assert');
const fs = require('fs');
const path = require('path');

function checkInventory(files, groups) {
  const assigned = new Set();
  const available = new Set(files);
  for (const [group, spec] of Object.entries(groups)) {
    assert.ok(spec.reason && spec.reason.trim(), group + ' needs a prerequisite/reason');
    for (const file of spec.files) {
      assert.ok(available.has(file), group + ' lists a missing test: ' + file);
      assert.ok(!assigned.has(file), 'test is listed more than once: ' + file);
      assigned.add(file);
    }
  }
  const missing = files.filter(file => !assigned.has(file));
  assert.deepStrictEqual(missing, [], 'unclassified tests: ' + missing.join(', '));
}

// Exercise the guard itself: omissions, stale paths, duplicate classifications,
// and undocumented exclusions must fail, even when the rest of the list is valid.
const unit = files => ({ reason: 'Standalone Node checks', files });
assert.doesNotThrow(() => checkInventory(['test-a.js'], { unit: unit(['test-a.js']) }));
assert.throws(() => checkInventory(['test-a.js', 'test-new.js'], { unit: unit(['test-a.js']) }), /unclassified tests/);
assert.throws(() => checkInventory(['test-a.js'], { unit: unit(['test-old.js']) }), /missing test/);
assert.throws(() => checkInventory(['test-a.js'], { unit: unit(['test-a.js', 'test-a.js']) }), /more than once/);
assert.throws(() => checkInventory(['test-a.js'], { unit: unit(['test-a.js']), browser: unit(['test-a.js']) }), /more than once/);
assert.throws(() => checkInventory(['test-a.js'], { browser: { reason: '', files: ['test-a.js'] } }), /prerequisite\/reason/);

const runner = fs.readFileSync(path.join(REPO_ROOT, 'test-all.sh'), 'utf8');
const unitTests = Array.from(runner.matchAll(/^node (tests\/[^\s]+\.js)\s*$/gm), match => match[1]);
const groups = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'scripts', 'non-unit-tests.json'), 'utf8'));
assert.ok(!Object.prototype.hasOwnProperty.call(groups, 'unit'), 'unit tests belong only in test-all.sh');
const isTest = file => /^test-.*\.js$/.test(file);
const files = ['tests/unit', 'tests/e2e'].flatMap(dir =>
  fs.readdirSync(path.join(REPO_ROOT, dir)).filter(isTest).map(file => dir + '/' + file)).sort();
assert.deepStrictEqual(fs.readdirSync(REPO_ROOT).filter(isTest), [], 'tests belong in tests/unit or tests/e2e, not the repo root');
for (const file of unitTests) assert.ok(file.startsWith('tests/unit/'), 'test-all.sh runs a suite outside tests/unit: ' + file);
for (const spec of Object.values(groups)) for (const file of spec.files) assert.ok(file.startsWith('tests/e2e/'), 'non-unit suite outside tests/e2e: ' + file);
checkInventory(files, { unit: unit(unitTests), ...groups });

const pkg = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'package.json'), 'utf8'));
assert.strictEqual(pkg.scripts['test:unit'], 'sh test-all.sh', 'npm test:unit must use the authoritative runner');
assert.ok(pkg.scripts.test.includes('sh test-all.sh'), 'npm test must use the authoritative runner');
assert.ok(pkg.scripts['test:coverage'].includes('sh test-all.sh'), 'coverage must use the authoritative runner');
const workflow = fs.readFileSync(path.join(REPO_ROOT, '.github', 'workflows', 'deploy.yml'), 'utf8');
const unitStep = workflow.match(/- name: Run JS unit tests[^\n]*\n([\s\S]*?)(?=\n      - name:)/);
assert.ok(unitStep, 'CI must retain its JS unit-test step');
assert.match(unitStep[1], /\bsh test-all\.sh\b/, 'CI must use the authoritative runner');
assert.doesNotMatch(unitStep[1], /\bnode (tests\/unit\/)?test-/, 'CI must not keep a second unit-test list');
console.log('Test inventory: ' + files.length + ' suites classified; ' + unitTests.length + ' run by local and CI unit checks.');
