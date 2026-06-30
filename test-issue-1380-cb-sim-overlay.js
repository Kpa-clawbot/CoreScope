/**
 * #1380 — Colorblind a11y stretch goal: Brettel/Vienot SVG simulation overlay.
 *
 * Deferred from #1361 / PR #1378. This test enforces the overlay wiring:
 *   - public/index.html contains inline SVG defs with filter ids
 *     cb-deut, cb-prot, cb-trit, cb-achromat (Brettel/Vienot 1997
 *     dichromatic matrices via <feColorMatrix>).
 *   - A CSS rule selects body[data-cb-sim="<class>"] and applies
 *     filter:url(#cb-<class>) to a top-level wrapper (body or #app).
 *   - public/customize-v2.js renders an "off/deut/prot/trit/achromat"
 *     radio selector marked with data-cv2-cb-sim and the change handler
 *     toggles body[data-cb-sim].
 *
 * Pure-string + vm.createContext assertions, mirrors test-issue-1361.
 * Persistence is intentionally NOT asserted (preview-only per spec).
 */
'use strict';

const fs   = require('fs');
const path = require('path');
const vm   = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  \u2713 ' + msg); }
  else      { failed++; console.error('  \u2717 ' + msg); }
}

const indexSrc  = fs.readFileSync(path.join(__dirname, 'public', 'index.html'), 'utf8');
const customSrc = fs.readFileSync(path.join(__dirname, 'public', 'customize-v2.js'), 'utf8');
const labelsSrc = fs.readFileSync(path.join(__dirname, 'public', 'payload-labels.js'), 'utf8');

console.log('\n=== #1380 A: index.html has inline SVG filters for the 4 sim classes ===');
['cb-deut', 'cb-prot', 'cb-trit', 'cb-achromat'].forEach(function (id) {
  var re = new RegExp('<filter[^>]*id=["\']' + id + '["\']', 'i');
  assert(re.test(indexSrc), 'index.html contains <filter id="' + id + '">');
});
assert(/<feColorMatrix[^>]*type=["']matrix["']/i.test(indexSrc),
  'index.html SVG defs use <feColorMatrix type="matrix"> (Brettel/Vienot 1997 form)');

console.log('\n=== #1380 B: CSS rule wires body[data-cb-sim] to filter:url(#cb-*) ===');
['deut', 'prot', 'trit', 'achromat'].forEach(function (cls) {
  var re = new RegExp('body\\[data-cb-sim=["\']' + cls + '["\']\\][^{]*\\{[^}]*filter:\\s*url\\(#cb-' + cls + '\\)', 'i');
  assert(re.test(indexSrc),
    'body[data-cb-sim="' + cls + '"] rule applies filter:url(#cb-' + cls + ')');
});

console.log('\n=== #1380 C: customize-v2.js renders the cv2-cb-sim radio group ===');
assert(/data-cv2-cb-sim/.test(customSrc),
  'customize-v2.js exposes a data-cv2-cb-sim hook for the sim radio group');
assert(/name=["']cv2-cb-sim["']/.test(customSrc),
  'customize-v2.js radio inputs use name="cv2-cb-sim"');
['', 'deut', 'prot', 'trit', 'achromat'].forEach(function (val) {
  var re = new RegExp('value=["\']' + val + '["\']', 'i');
  // The empty value is the "off" radio; assert it explicitly.
  if (val === '') {
    assert(/data-cv2-cb-sim[^>]*value=["']["']/.test(customSrc) ||
           /value=["']["'][^>]*data-cv2-cb-sim/.test(customSrc),
      'customize-v2.js radio has an "off" option (value="")');
  } else {
    assert(re.test(customSrc),
      'customize-v2.js radio has value="' + val + '"');
  }
});

console.log('\n=== #1380 D: change handler toggles body[data-cb-sim] attribute ===');
assert(/setAttribute\(\s*['"]data-cb-sim['"]/.test(customSrc),
  'customize-v2.js change handler calls setAttribute("data-cb-sim", ...)');
assert(/removeAttribute\(\s*['"]data-cb-sim['"]/.test(customSrc),
  'customize-v2.js change handler can clear body[data-cb-sim] when "off"');

console.log('\n=== #1380 E: vm.createContext — selecting deut applies the attribute ===');
// Build a minimal sandbox, eval customize-v2.js, drive the radio change.
function makeSandbox() {
  const stored = {};
  const ls = {
    getItem(k) { return Object.prototype.hasOwnProperty.call(stored, k) ? stored[k] : null; },
    setItem(k, v) { stored[k] = String(v); },
    removeItem(k) { delete stored[k]; },
  };
  const body = {
    _attrs: {},
    setAttribute(k, v) { this._attrs[k] = v; },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; },
    removeAttribute(k) { delete this._attrs[k]; },
    dataset: {},
  };
  const handlers = {};
  const sandbox = {
    window: null,
    document: {
      readyState: 'complete',
      body: body,
      documentElement: {
        style: { setProperty() {}, removeProperty() {}, getPropertyValue() { return ''; } },
        dataset: { theme: 'light' },
        getAttribute() { return 'light'; },
        setAttribute() {},
      },
      head: { appendChild() {} },
      getElementById() { return null; },
      createElement() {
        return {
          id: '', textContent: '', innerHTML: '', className: '',
          setAttribute() {}, appendChild() {}, style: {},
          addEventListener() {}, querySelectorAll() { return []; }, querySelector() { return null; },
        };
      },
      addEventListener(ev, cb) { (handlers[ev] = handlers[ev] || []).push(cb); },
      querySelectorAll() { return []; },
      querySelector() { return null; },
    },
    localStorage: ls,
    console: console,
    setTimeout(fn) { try { fn(); } catch (e) {} },
    clearTimeout() {},
    MutationObserver: class { observe() {} },
    HashChangeEvent: class {},
    CustomEvent: class { constructor(t, o) { this.type = t; this.detail = o && o.detail; } },
    Event: class { constructor(t) { this.type = t; } },
    getComputedStyle() { return { getPropertyValue() { return ''; } }; },
  };
  sandbox.window = {
    addEventListener(ev, cb) { (handlers[ev] = handlers[ev] || []).push(cb); },
    dispatchEvent(ev) { (handlers[ev.type] || []).forEach(function (cb) { try { cb(ev); } catch (_) {} }); return true; },
    localStorage: ls,
    SITE_CONFIG: {},
    location: { hash: '', pathname: '/' },
    CustomEvent: sandbox.CustomEvent,
    Event: sandbox.Event,
    matchMedia() { return { matches: false, addEventListener() {} }; },
  };
  sandbox.self = sandbox.window;
  return { sandbox, body, ls };
}

let envOK = false, env, exposed;
try {
  env = makeSandbox();
  vm.createContext(env.sandbox);
  vm.runInContext(labelsSrc, env.sandbox, { filename: 'payload-labels.js' });
  vm.runInContext(customSrc, env.sandbox, { filename: 'customize-v2.js' });
  exposed = env.sandbox.window._customizerV2;
  envOK = !!exposed;
} catch (e) {
  console.error('  ! customize-v2.js failed to load in vm sandbox: ' + e.message);
}
assert(envOK, 'customize-v2.js loads in vm sandbox and exposes window._customizerV2');

// Drive the handler directly — exposed for tests.
if (envOK && typeof exposed.applyCbSim === 'function') {
  exposed.applyCbSim('deut');
  assert(env.body.getAttribute('data-cb-sim') === 'deut',
    'applyCbSim("deut") sets body[data-cb-sim="deut"]');
  exposed.applyCbSim('');
  assert(env.body.getAttribute('data-cb-sim') === null,
    'applyCbSim("") clears body[data-cb-sim]');
} else {
  assert(false, 'customize-v2.js exposes applyCbSim() helper for test-driven toggling');
}

console.log('\n=== #1380 summary ===');
console.log('  passed: ' + passed + '   failed: ' + failed);
if (failed > 0) process.exit(1);
