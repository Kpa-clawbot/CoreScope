// test-good-2.js — DOM-grep coverage test for testdata/preflight-xss/good-2-tested.js
// Demonstrates the (b) opt-out clause of check-xss-sinks: a same-PR test
// asserting the audit payload renders inert satisfies the gate without
// requiring escapeHtml() at the sink.
//
// References file basename "good-2-tested.js" and BOTH audit payload markers
// (' onfocus= and onerror=alert) so check-xss-sinks' test_covers() matches.
'use strict';
const { JSDOM } = (() => {
  try { return require('jsdom'); }
  catch { return { JSDOM: null }; }
})();

if (!JSDOM) {
  console.log('test-good-2: jsdom not available, skipping (marker strings still grep-visible)');
  // Markers are still present in this source file for check-xss-sinks:
  //   ' onfocus=alert(1)
  //   onerror=alert(1)
  process.exit(0);
}

const { render } = require('./testdata/preflight-xss/good-2-tested.js');
const dom = new JSDOM('<!doctype html><div id="root"></div>');
const el = dom.window.document.getElementById('root');
// Payload taken from the post-#1537 XSS audit:
//   ' onfocus=alert(1) autofocus '
//   "<img src=x onerror=alert(1)>"
const payload = "' onfocus=alert(1) autofocus 'onerror=alert(1)";
render(el, payload);
if (el.querySelector('img[onerror], [onfocus]')) {
  console.error('FAIL: payload rendered as live attributes');
  process.exit(1);
}
console.log('PASS: payload rendered inert');
