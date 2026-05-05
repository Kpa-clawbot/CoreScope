/**
 * Tests for public/channel-qr.js — the QR generation/scanning module
 * for the channel UX redesign (#1034, PR #2 of 3).
 *
 * Pure-JS assertions only: covers buildUrl, parseChannelUrl. The DOM
 * (generate) and camera (scan) paths are exercised by Playwright E2E
 * elsewhere in the redesign series.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');

let passed = 0;
let failed = 0;

function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function loadChannelQR() {
  const sandbox = {
    window: {}, console, Date, JSON, parseInt, Math, String, Number,
    Object, Array, RegExp, Error, Promise, setTimeout, encodeURIComponent,
    decodeURIComponent, URL, URLSearchParams,
  };
  sandbox.window = sandbox;
  sandbox.self = sandbox;
  vm.createContext(sandbox);

  const src = fs.readFileSync(path.join(__dirname, 'public/channel-qr.js'), 'utf8');
  vm.runInContext(src, sandbox);
  return sandbox.window.ChannelQR;
}

console.log('── ChannelQR — URL helpers ──');
const ChannelQR = loadChannelQR();

assert(ChannelQR && typeof ChannelQR.buildUrl === 'function',
  'ChannelQR.buildUrl is exported');
assert(typeof ChannelQR.parseChannelUrl === 'function',
  'ChannelQR.parseChannelUrl is exported');
assert(typeof ChannelQR.generate === 'function',
  'ChannelQR.generate is exported');
assert(typeof ChannelQR.scan === 'function',
  'ChannelQR.scan is exported');

// --- buildUrl ---
const SECRET = '8b3387e1c4be1bbf09c1a4cd5c0fa5a3';
const url1 = ChannelQR.buildUrl('Public', SECRET);
assert(url1 === 'meshcore://channel/add?name=Public&secret=' + SECRET,
  'buildUrl produces canonical URL for plain name');

const url2 = ChannelQR.buildUrl('My Channel & Stuff', SECRET);
assert(url2 === 'meshcore://channel/add?name=My%20Channel%20%26%20Stuff&secret=' + SECRET,
  'buildUrl URL-encodes spaces and ampersands in name');

// --- parseChannelUrl ---
const p1 = ChannelQR.parseChannelUrl(url1);
assert(p1 && p1.name === 'Public' && p1.secret === SECRET,
  'parseChannelUrl extracts name + secret from canonical URL');

const p2 = ChannelQR.parseChannelUrl(url2);
assert(p2 && p2.name === 'My Channel & Stuff' && p2.secret === SECRET,
  'parseChannelUrl URL-decodes name correctly');

assert(ChannelQR.parseChannelUrl(null) === null, 'parseChannelUrl(null) → null');
assert(ChannelQR.parseChannelUrl('') === null, 'parseChannelUrl("") → null');
assert(ChannelQR.parseChannelUrl('https://example.com') === null,
  'parseChannelUrl rejects non-meshcore scheme');
assert(ChannelQR.parseChannelUrl('meshcore://channel/add?name=Foo') === null,
  'parseChannelUrl rejects URL missing secret');
assert(ChannelQR.parseChannelUrl('meshcore://channel/add?secret=' + SECRET) === null,
  'parseChannelUrl rejects URL missing name');
assert(ChannelQR.parseChannelUrl('meshcore://other/add?name=Foo&secret=' + SECRET) === null,
  'parseChannelUrl rejects wrong host/path');
assert(ChannelQR.parseChannelUrl('meshcore://channel/add?name=Foo&secret=zz') === null,
  'parseChannelUrl rejects non-hex secret');
assert(ChannelQR.parseChannelUrl('meshcore://channel/add?name=Foo&secret=' + SECRET.slice(0, 30)) === null,
  'parseChannelUrl rejects short secret (must be 32 hex chars)');

console.log('');
console.log(`  ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
