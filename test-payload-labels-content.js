/* Unit test for public/payload-labels.js — pins long descriptions and the
 * abbreviation-style policy declared at the top of the file.
 * #1799 PR #1804 r1 items 2 (tufte2: long must describe, not echo) and
 * 5 (tufte5: uniform abbreviation policy + top-of-file comment).
 */
'use strict';
const fs = require('fs');
const vm = require('vm');

const src = fs.readFileSync('public/payload-labels.js', 'utf8');
const ctx = { window: {}, console };
vm.createContext(ctx);
vm.runInContext(src, ctx);
const PL = ctx.window.PayloadLabels;

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  \u2713 ' + name); }
  catch (e) { fail++; console.log('  \u2717 ' + name + ' — ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

// Item 2 — long must describe behaviour, not echo the short label.
const EXPECTED_LONG = {
  REQ:        'Encrypted data request to a remote node',
  RESPONSE:   'Encrypted data response from a remote node',
  TXT_MSG:    'Encrypted point-to-point text message',
  ACK:        'Acknowledgment of a prior message or request',
  ADVERT:     'Node identity / capability advertisement',
  GRP_TXT:    'Channel-scoped group text message',
  GRP_DATA:   'Channel-scoped group datagram (non-text payload)',
  ANON_REQ:   'Anonymous encrypted request via ephemeral key',
  PATH:       'Network path discovery / return-path advertisement',
  TRACE:      'Per-hop route trace with SNR samples',
  MULTIPART:  'Fragmented payload reassembled across multiple packets',
  CONTROL:    'Mesh control-plane signalling (e.g. zero-hop direct)',
  RAW_CUSTOM: 'Application-defined raw payload, no firmware envelope'
};

console.log('=== payload-labels content + policy ===');

test('every enum has the pinned long description (item 2)', () => {
  for (const name of Object.keys(EXPECTED_LONG)) {
    assert(PL[name], 'missing entry ' + name);
    assert(PL[name].long === EXPECTED_LONG[name],
      name + '.long: expected "' + EXPECTED_LONG[name] + '", got "' + PL[name].long + '"');
  }
});

test('no long tautologically echoes its short (item 2)', () => {
  for (const name of Object.keys(EXPECTED_LONG)) {
    const s = (PL[name].short || '').toLowerCase().replace(/\s+/g, ' ').trim();
    const l = (PL[name].long  || '').toLowerCase().replace(/\s+/g, ' ').trim();
    assert(l !== s, name + ': long equals short (' + s + ')');
    assert(l !== s + ' ' + s, name + ': long is just doubled short');
    // soft heuristic: long must be at least 2 words longer than short
    const shortWords = s.split(/\s+/).length;
    const longWords  = l.split(/\s+/).length;
    assert(longWords >= shortWords + 2,
      name + ': long ("' + l + '") not meaningfully longer than short ("' + s + '")');
  }
});

test('abbreviation policy comment is present at top of file (item 5)', () => {
  // Top-of-file comment must declare the uniform policy so future editors
  // know the rule before adding a new enum.
  const head = src.slice(0, 1500);
  assert(/abbreviation policy|ABBREVIATION POLICY|Abbreviation policy/.test(head),
    'top-of-file comment missing an "abbreviation policy" section');
});

test('every short respects the declared length cap (<=12 chars)', () => {
  for (const name of Object.keys(EXPECTED_LONG)) {
    assert(PL[name].short.length <= 12,
      name + '.short ("' + PL[name].short + '") exceeds 12-char cap');
  }
});

test('all shorts use Title Case (no all-caps except documented exceptions)', () => {
  // ACK is the documented exception (a wire-protocol acronym).
  const allowedAllCaps = new Set(['ACK']);
  for (const name of Object.keys(EXPECTED_LONG)) {
    const s = PL[name].short;
    if (allowedAllCaps.has(name)) continue;
    assert(s !== s.toUpperCase() || s.length === 0,
      name + '.short ("' + s + '") is all-caps and not in the documented exception set');
  }
});

console.log('=== ' + pass + ' passed, ' + fail + ' failed ===');
process.exit(fail === 0 ? 0 : 1);
