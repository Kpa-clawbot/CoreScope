/**
 * Analytics → Channels section integration with PSK decrypt UX.
 *
 * Bug: the analytics channels list shows nonsense names like "ch185" for
 *      every encrypted channel and ignores the user's locally-decrypted PSK
 *      channels (from ChannelDecrypt.getStoredKeys() + label store).
 *
 * Fix:
 *   1. Replace "chNNN" raw names with "🔒 Encrypted (0xNN)" when the channel
 *      is encrypted and the server only knows its hash byte.
 *   2. For channels matching a locally-stored PSK key, show the user's
 *      label / key-name instead of the hash-byte placeholder.
 *   3. Group rendering: My Channels → Network → Encrypted, each sorted by
 *      message count descending.
 *   4. Add a link from the Channels page to the Analytics page so users can
 *      jump to channel activity stats.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');

const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

// ── Set up a tiny browser-ish global so analytics.js loads cleanly ──────────
global.window = global;
global.document = {
  documentElement: {},
  createElement: () => ({ style: {}, addEventListener() {} }),
  addEventListener() {},
  removeEventListener() {},
  querySelector: () => null,
  querySelectorAll: () => [],
  getElementById: () => null,
};
global.localStorage = {
  _s: {},
  getItem(k) { return this._s[k] || null; },
  setItem(k, v) { this._s[k] = String(v); },
  removeItem(k) { delete this._s[k]; },
};
global.getComputedStyle = () => ({ getPropertyValue: () => '' });
global.registerPage = () => {};
global.api = async () => ({});
global.fetch = async () => ({ ok: true, json: async () => ({}) });
global.CLIENT_TTL = {};
global.RegionFilter = { getRegionParam: () => '' };
global.Storage = function () {};
global.timeAgo = () => '';
global.histogram = () => ({ svg: '' });

// Load analytics.js — it self-registers global helpers we test.
const analyticsSrc = fs.readFileSync(
  path.join(REPO_ROOT, 'public/analytics.js'),
  'utf8'
);
// Strip top-level `await` / module syntax — analytics.js is plain IIFE so it's
// fine to eval as-is.
// eslint-disable-next-line no-eval
eval(analyticsSrc); // sets window._analyticsDecorateChannels etc.

console.log('\n=== Analytics channels: decorate with PSK keys ===');

const decorate = global._analyticsDecorateChannels;
assert(typeof decorate === 'function',
  '_analyticsDecorateChannels exposed for testing');

// Server response sample — mix of cleartext, rainbow-known encrypted, raw "chNNN".
const sampleChannels = [
  { hash: 17,  name: 'public',           messages: 100, senders: 5,  encrypted: false },
  { hash: 217, name: '#test',            messages: 200, senders: 8,  encrypted: false },
  { hash: 185, name: 'ch185',            messages: 50,  senders: 0,  encrypted: true  },
  { hash: 64,  name: 'ch64',             messages: 300, senders: 0,  encrypted: true  },
  { hash: 30,  name: 'ch30',             messages: 75,  senders: 0,  encrypted: true  },
  { hash: 99,  name: '#earthquake',      messages: 10,  senders: 1,  encrypted: false },
  // Rainbow-table hit on an ENCRYPTED channel: server resolved a real name.
  { hash: 12,  name: 'public-meshcore',  messages: 40,  senders: 2,  encrypted: true  },
  // Encrypted channel with empty name — must not render an empty <strong>.
  { hash: 200, name: '',                 messages: 5,   senders: 0,  encrypted: true  },
];

// User has two PSK keys locally: one matches hash=185 (named "Levski"),
// one matches hash=30 (named "secret-room", with label "Garage").
const myKeyHashToName = { 185: 'Levski', 30: 'secret-room' };
const labels = { 'secret-room': 'Garage' };

const out = decorate(sampleChannels, myKeyHashToName, labels);
assert(Array.isArray(out), 'decorate returns an array');
assert(out.length === sampleChannels.length, 'decorate keeps every channel');

// Find by original hash (and optionally original name) for assertions.
// Decoration preserves c.name as-is and writes the user-facing string to
// c.displayName, so matching on c.name is unambiguous.
function find(hash, name) {
  return out.find(c => c.hash === hash && (name == null || c.name === name));
}

const mine185 = find(185, 'ch185');
assert(mine185 && mine185.displayName === 'Levski',
  'hash 185 + stored key → displayName = "Levski" (not "ch185")');
assert(mine185 && mine185.group === 'mine',
  'hash 185 grouped as "mine"');

const mine30 = find(30, 'ch30');
assert(mine30 && mine30.displayName === 'Garage',
  'hash 30 with stored key + label → displayName = "Garage" (label wins)');
assert(mine30 && mine30.group === 'mine', 'hash 30 grouped as "mine"');

const ch64 = find(64, 'ch64');
assert(ch64 && ch64.displayName === 'Encrypted (0x40)',
  'unknown encrypted ch64 → "Encrypted (0x40)" (no nonsense "ch64")');
assert(ch64 && ch64.group === 'encrypted', 'unknown encrypted grouped as "encrypted"');

const pub = find(17, 'public');
assert(pub && pub.displayName === 'public', 'cleartext public name preserved');
assert(pub && pub.group === 'network', 'cleartext public grouped as "network"');

const test = find(217, '#test');
assert(test && test.group === 'network', 'rainbow-known #test grouped as "network"');

// Rainbow-table hit on an ENCRYPTED channel — actually exercises the
// "encrypted but server has the real name" branch (was previously dead-untested).
const rainbow = find(12, 'public-meshcore');
assert(rainbow && rainbow.encrypted === true,
  'rainbow row preserves encrypted=true');
assert(rainbow && rainbow.displayName === 'public-meshcore',
  'rainbow-decoded encrypted row → displayName = real name');
assert(rainbow && rainbow.group === 'network',
  'rainbow-decoded encrypted row → group = "network"');

// Empty-name encrypted: must NOT leak through with displayName = ''.
const empty = find(200, '');
assert(empty && empty.displayName === 'Encrypted (0xC8)',
  'encrypted with empty name → render as opaque encrypted placeholder');
assert(empty && empty.group === 'encrypted',
  'encrypted with empty name → group = "encrypted"');

// No "chNNN" leaks into displayName for any row.
const leak = out.find(c => /^ch(\d+|\?)$/.test(c.displayName));
assert(!leak, 'no displayName matches the raw chNNN placeholder');

console.log('\n=== Grouped table render: order + sort ===');

const tbody = global._analyticsChannelTbodyHtml(out, 'messages', 'desc', {
  grouped: true,
});
assert(typeof tbody === 'string' && tbody.length > 0,
  'channelTbodyHtml accepts grouped option and returns html');

// Group headers must appear in order: My Channels, Network, Encrypted.
const iMine = tbody.indexOf('My Channels');
const iNet  = tbody.indexOf('Network');
const iEnc  = tbody.indexOf('Encrypted');
assert(iMine >= 0 && iNet > iMine && iEnc > iNet,
  'group headers render in order: My Channels → Network → Encrypted');

// Within "mine" section, hash=30 (75 msgs) > hash=185 (50 msgs).
const i30  = tbody.indexOf('Garage');
const i185 = tbody.indexOf('Levski');
assert(i30 > 0 && i185 > i30,
  'within "My Channels" sort by messages desc (Garage 75 before Levski 50)');

// Within "network" section, #test (200) > public (100) > #earthquake (10).
const iT = tbody.indexOf('#test');
const iP = tbody.indexOf('public');
const iE = tbody.indexOf('#earthquake');
assert(iT > 0 && iP > iT && iE > iP,
  'within "Network" sort by messages desc (#test → public → #earthquake)');

// Within "encrypted" section, ch64 (300 msgs) appears (only one entry).
assert(tbody.indexOf('0x40') > iEnc, 'encrypted section contains 0x40');

// The "Channels page links to Analytics" assertion that used to live here was
// removed: #1367 / PR #1376 ("channels page chat-app redesign — restore prod row
// layout, drop analytics chip") deliberately deleted the `#/analytics` chip from
// public/channels.js. The assertion could not pass again without reinstating a
// feature that was removed on purpose. It survived because nothing runs
// test-all.sh in CI (#1858).

// ── A local key the server already decrypts with ────────────────────────────
// Undecrypted packets on the byte of a channel the server decrypts are ones
// that channel's key did not open, so a local copy of that key must not name
// their bucket, however the key was saved (by #name, or as hex / QR, which
// stores it as "psk:<hex8>" with the name only as a label). Runs the real
// channel-decrypt.js; hash bytes come from it, and the server sends `hash`
// as a string.
async function localKeyChecks() {
  console.log('\n=== Local key the server already decrypts with ===');
  eval(fs.readFileSync(path.join(REPO_ROOT, 'public/channel-decrypt.js'), 'utf8'));
  const CD = global.ChannelDecrypt;
  const build = global._analyticsBuildHashKeyMap;
  assert(typeof build === 'function', '_analyticsBuildHashKeyMap exposed for testing');
  if (typeof build !== 'function') return;

  const PUBLIC_HEX = '8b3387e9c5cdea6ac9e5edbaa115cd72';
  const driveHex = CD.bytesToHex(await CD.deriveKey('#coredrive'));
  const privHex = 'a1b2c3d4e5f60718293a4b5c6d7e8f90';
  const hb = async (hex) => CD.computeChannelHash(CD.hexToBytes(hex));
  const bPub = await hb(PUBLIC_HEX), bDrive = await hb(driveHex), bPriv = await hb(privHex);
  const rows = [
    { hash: String(bPub),   name: 'Public',     messages: 3102, senders: 600, encrypted: false },
    { hash: String(bPub),   name: 'ch' + bPub,  messages: 153,  senders: 0,   encrypted: true  },
    { hash: String(bDrive), name: '#coredrive', messages: 1632, senders: 36,  encrypted: false },
    { hash: String(bDrive), name: 'ch' + bDrive, messages: 3349, senders: 0,  encrypted: true  },
    { hash: String(bPriv),  name: '#elsewhere', messages: 50,   senders: 4,   encrypted: false },
    { hash: String(bPriv),  name: 'ch' + bPriv, messages: 20,   senders: 0,   encrypted: true  },
  ];
  const hex2 = (b) => b.toString(16).toUpperCase().padStart(2, '0');
  const enc = (out, b) => out.find(c => c.hash === String(b) && c.encrypted);

  async function run(stored, labels) {
    localStorage.setItem('corescope_channel_keys', JSON.stringify(stored));
    return decorate(rows, await build(rows, CD), labels);
  }

  let out = await run({ '#coredrive': driveHex }, {});
  assert(enc(out, bDrive).displayName === 'Encrypted (0x' + hex2(bDrive) + ')' && enc(out, bDrive).group === 'encrypted',
    'key saved as #coredrive: the undecrypted bucket on its byte stays Encrypted');

  out = await run({ ['psk:' + driveHex.slice(0, 8)]: driveHex }, { ['psk:' + driveHex.slice(0, 8)]: '#coredrive' });
  assert(enc(out, bDrive).group === 'encrypted',
    'same key saved as psk:<hex8> with a label (QR): bucket stays Encrypted');

  out = await run({ ['psk:' + driveHex.slice(0, 8)]: driveHex }, {});
  assert(enc(out, bDrive).group === 'encrypted',
    'same key saved as psk:<hex8> without a label: bucket stays Encrypted');

  out = await run({ ['psk:' + PUBLIC_HEX.slice(0, 8)]: PUBLIC_HEX }, { ['psk:' + PUBLIC_HEX.slice(0, 8)]: 'Public' });
  assert(enc(out, bPub).group === 'encrypted',
    'the Public key saved locally: the undecrypted bucket on its byte stays Encrypted');

  out = await run({ 'private-room': privHex }, {});
  assert(enc(out, bPriv).displayName === 'private-room' && enc(out, bPriv).group === 'mine',
    'a private key the server does not hold still names the bucket next to another decrypted channel');

  out = await run({ '#coredrive': driveHex.toUpperCase() }, {});
  assert(enc(out, bDrive).group === 'encrypted',
    'key hex compared case-insensitively');

  // A second local key on the same byte as a server-decrypted one: dropping
  // the server's key must not drop this one, whatever order they were saved.
  let mateHex = null;
  for (let i = 0; i < 4096 && !mateHex; i++) {
    const h = i.toString(16).padStart(32, 'f');
    if (await hb(h) === bDrive) mateHex = h;
  }
  out = await run({ 'drive-mate': mateHex, '#coredrive': driveHex }, {});
  assert(enc(out, bDrive).displayName === 'drive-mate' && enc(out, bDrive).group === 'mine',
    'another local key on that byte still names the bucket when the server key was saved last');

  localStorage.removeItem('corescope_channel_keys');
}

localKeyChecks().catch(e => { failed++; console.error('  ✗ local key checks threw: ' + e.message); }).then(() => {
  console.log('\n' + (failed ? '✗ ' + failed + ' failed, ' : '') + passed + ' passed');
  process.exit(failed ? 1 : 0);
});
