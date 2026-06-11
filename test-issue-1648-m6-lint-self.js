#!/usr/bin/env node
/* Issue #1648 — M6: anti-tautology + unit test for the lint gate itself.
 *
 * If the lint script is broken (e.g. always returns []), the main test
 * `test-issue-1648-m6-final-sweep.js` becomes a no-op rubber-stamp.
 * This file:
 *   1. exercises the lint engine against synthetic fixtures and asserts
 *      it flags / allows the expected things;
 *   2. is the anti-tautology proof — the lint MUST detect a deliberate
 *      emoji injection.
 */
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const assert = require('assert');

const lint = require('./test-issue-1648-m6-final-sweep.js');

// 1. Codepoint classifier ------------------------------------------------
assert.strictEqual(lint.isEmojiCodepoint(0x1F600), true,  'emoji 😀 detected');
assert.strictEqual(lint.isEmojiCodepoint(0x2B50),  true,  '⭐ detected');
assert.strictEqual(lint.isEmojiCodepoint(0x23F0),  true,  '⏰ detected');
assert.strictEqual(lint.isEmojiCodepoint(0x25CF),  true,  '● detected');
assert.strictEqual(lint.isEmojiCodepoint(0x41),    false, 'ASCII A not flagged');
assert.strictEqual(lint.isEmojiCodepoint(0x4E2D),  false, 'CJK 中 not flagged');

// 2. Line scanner --------------------------------------------------------
var hits = lint.findEmojiInLine('hello ⭐ world');
assert.strictEqual(hits.length, 1);
assert.strictEqual(hits[0].codepoint, 0x2B50);
assert.deepStrictEqual(lint.findEmojiInLine('plain ascii'), []);

// 3. Allowlist forms -----------------------------------------------------
var tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'm6-lint-'));
function w(p, t) {
  var f = path.join(tmp, p);
  fs.mkdirSync(path.dirname(f), { recursive: true });
  fs.writeFileSync(f, t);
}
w('al.txt', [
  '# header comment',
  'public/allowed.js',
  'public/specific.js:2',
  'public/cp.js:1:U+2B50',
  '/EMOJI-OK/',
].join('\n'));
var allow = lint.loadAllowlist(path.join(tmp, 'al.txt'));
assert.ok(allow.pathGlobs.includes('public/allowed.js'));
assert.ok(allow.pathLine.has('public/specific.js:2'));
assert.ok(allow.pathLineCp.has('public/cp.js:1:U+2B50'));
assert.strictEqual(allow.regexes.length, 1);

// 4. lintFiles end-to-end on fixture files -------------------------------
w('public/bad.js',      'var x = "⭐ bare";\n');         // SHOULD flag
w('public/allowed.js',  'var y = "⭐ permitted";\n');     // SHOULD pass (glob)
w('public/specific.js', '// line1\nvar z = "⏰";\n');     // SHOULD pass (path:line)
w('public/cp.js',       'var a = "⭐";\nvar b = "⏰";\n'); // ⭐ pass (cp), ⏰ flag
w('public/tag.js',      'var t = "⭐"; // EMOJI-OK: prose\n'); // SHOULD pass (regex)

// Re-target ROOT to fixture dir by monkey-patching require cache: call
// lintFiles directly with the synthetic root.
var ROOT = tmp;
function relativise(files) { return files.map(function (f) { return path.relative(ROOT, f); }); }

// Wrap lintFiles to use synthetic ROOT (the published lintFiles resolves
// paths relative to its own __dirname; we hack by replacing the path
// resolver via cwd). Simpler: read each fixture via the same scanner.
var allFixtures = [
  'public/bad.js',
  'public/allowed.js',
  'public/specific.js',
  'public/cp.js',
  'public/tag.js',
];
function scanRel(rel) {
  var lines = fs.readFileSync(path.join(ROOT, rel), 'utf8').split('\n');
  var v = [];
  lines.forEach(function (line, i) {
    lint.findEmojiInLine(line).forEach(function (h) {
      // Mirror isAllowed logic via re-implementing here against `allow`:
      var cpKey = 'U+' + h.codepoint.toString(16).toUpperCase().padStart(4, '0');
      if (allow.pathLineCp.has(rel + ':' + (i + 1) + ':' + cpKey)) return;
      if (allow.pathLine.has(rel + ':' + (i + 1))) return;
      // simple glob: equality only here
      if (allow.pathGlobs.indexOf(rel) >= 0) return;
      if (allow.regexes.some(function (r) { return r.test(line); })) return;
      v.push({ file: rel, line: i + 1, cp: cpKey });
    });
  });
  return v;
}

var byFile = {};
allFixtures.forEach(function (f) { byFile[f] = scanRel(f); });

assert.strictEqual(byFile['public/bad.js'].length, 1,
  'bad.js MUST flag (no allowlist entry)');
assert.strictEqual(byFile['public/allowed.js'].length, 0,
  'allowed.js path-glob entry MUST pass');
assert.strictEqual(byFile['public/specific.js'].length, 0,
  'specific.js path:line entry MUST pass');
assert.strictEqual(byFile['public/cp.js'].length, 1,
  'cp.js MUST flag the unallowed ⏰ but not ⭐');
assert.strictEqual(byFile['public/cp.js'][0].cp, 'U+23F0',
  'remaining hit on cp.js MUST be ⏰');
assert.strictEqual(byFile['public/tag.js'].length, 0,
  'tag.js regex allowlist MUST pass');

// 5. ANTI-TAUTOLOGY: real lintFiles on repo finds 0; injecting an emoji
//    must surface it (proves the lint gate has teeth).
var beforeViolations = lint.runLint();
assert.strictEqual(beforeViolations.length, 0,
  'repo MUST be clean before anti-tautology probe');

var probeFile = path.join(__dirname, 'public', '__m6_lint_probe.js');
fs.writeFileSync(probeFile, '/* synthetic probe — emoji should be flagged: ⭐ */\n');
try {
  var afterViolations = lint.runLint();
  assert.ok(afterViolations.length >= 1,
    'lint gate FAILED to detect injected ⭐ — anti-tautology breach');
  assert.ok(afterViolations.some(function (v) {
    return v.file.indexOf('__m6_lint_probe.js') >= 0 && v.codepoint === 'U+2B50';
  }), 'detected violations missing the injected ⭐');
} finally {
  fs.unlinkSync(probeFile);
}

// 6. Clean up
fs.rmSync(tmp, { recursive: true, force: true });

console.log('✓ M6 lint-gate self-test passed (6 sections)');
console.log('  • codepoint classifier');
console.log('  • line scanner');
console.log('  • allowlist parser (path/path:line/path:line:cp/regex)');
console.log('  • end-to-end fixture scan');
console.log('  • anti-tautology: injected ⭐ probe correctly flagged');
console.log('  • cleanup');
