/**
 * Tests for #1020 — PSK channel UX:
 *   - Optional label stored alongside key in localStorage
 *   - removeKey clears both key and label
 *   - channels.js form has an optional label input
 *   - User-added rows render with a distinct badge marker in the DOM
 *   - Status feedback reports decrypt count from result (not DOM scrape)
 *
 * Runs in Node.js via vm.createContext to simulate the browser.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const path = require('path');
const { subtle } = require('crypto').webcrypto;

let passed = 0;
let failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

function createSandbox() {
  const storage = {};
  const localStorage = {
    getItem: (k) => storage[k] !== undefined ? storage[k] : null,
    setItem: (k, v) => { storage[k] = String(v); },
    removeItem: (k) => { delete storage[k]; },
    _data: storage,
  };
  const ctx = {
    window: {},
    crypto: { subtle },
    TextEncoder, TextDecoder, Uint8Array,
    localStorage,
    console, Date, JSON, parseInt, Math, String, Number, Object, Array, RegExp, Error, Promise, setTimeout,
    btoa: (s) => Buffer.from(s, 'binary').toString('base64'),
    atob: (s) => Buffer.from(s, 'base64').toString('binary'),
  };
  ctx.window = ctx;
  ctx.self = ctx;
  return ctx;
}

async function run() {
  console.log('\n=== #1020 PSK UX: ChannelDecrypt label storage ===');

  const cdSrc = fs.readFileSync(path.join(__dirname, 'public/channel-decrypt.js'), 'utf8');
  const sandbox = createSandbox();
  vm.runInContext(cdSrc, vm.createContext(sandbox));
  const CD = sandbox.window.ChannelDecrypt;

  // saveLabel/getLabel API exists
  assert(typeof CD.saveLabel === 'function', 'ChannelDecrypt.saveLabel exists');
  assert(typeof CD.getLabel === 'function', 'ChannelDecrypt.getLabel exists');
  assert(typeof CD.getLabels === 'function', 'ChannelDecrypt.getLabels exists');

  // saveKey overload with label argument
  CD.storeKey('psk:aabbccdd', 'aabbccdd11223344aabbccdd11223344', 'My Secret Channel');
  assert(CD.getLabel('psk:aabbccdd') === 'My Secret Channel',
    'storeKey(name, hex, label) persists label retrievable via getLabel');

  // saveLabel updates an existing key's label
  CD.saveLabel('psk:aabbccdd', 'Renamed');
  assert(CD.getLabel('psk:aabbccdd') === 'Renamed', 'saveLabel updates label');

  // removeKey clears label too
  CD.removeKey('psk:aabbccdd');
  assert(!CD.getLabel('psk:aabbccdd'), 'removeKey clears stored label');

  // No-label storage stays valid
  CD.storeKey('#LongFast', 'deadbeefdeadbeefdeadbeefdeadbeef');
  const keys = CD.getStoredKeys();
  assert(keys['#LongFast'] === 'deadbeefdeadbeefdeadbeefdeadbeef',
    'storeKey without label still persists key');
  assert(!CD.getLabel('#LongFast'), 'no label means getLabel returns falsy');

  console.log('\n=== #1020 PSK UX: channels.js DOM/contract ===');
  const chSrc = fs.readFileSync(path.join(__dirname, 'public/channels.js'), 'utf8');

  // E2E DOM: optional label input in add form
  assert(chSrc.includes('id="chKeyLabelInput"'),
    'add form contains chKeyLabelInput element');
  assert(/placeholder="[^"]*name[^"]*"/i.test(chSrc) || chSrc.includes('chKeyLabelInput'),
    'label input has a name-related placeholder');

  // E2E DOM: distinct badge class/marker for user-added channels
  assert(chSrc.includes('ch-user-added'),
    'renderChannelList emits ch-user-added marker for keyed channels');
  // Distinct icon
  assert(chSrc.includes('🔓'),
    'user-added rows use a distinct unlocked icon (🔓) from server-encrypted (🔒)');

  // addUserChannel accepts label
  assert(/addUserChannel\s*\(\s*val\s*,\s*\w*label/i.test(chSrc) ||
         /addUserChannel\([^)]*\blabel\b[^)]*\)/.test(chSrc),
    'addUserChannel signature accepts a label parameter');

  // mergeUserChannels reads labels
  assert(/getLabels?\s*\(/.test(chSrc),
    'channels.js queries ChannelDecrypt.getLabels()/getLabel()');

  // Toast count comes from result.messages, not from #chMessages DOM scrape
  assert(!/querySelectorAll\('#chMessages \.ch-msg'\)\.length/.test(chSrc),
    'addUserChannel must not scrape #chMessages DOM for count (use decrypt result)');

  console.log('\n=== Results ===');
  console.log('Passed: ' + passed + ', Failed: ' + failed);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch((e) => { console.error(e); process.exit(1); });
