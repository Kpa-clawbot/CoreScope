'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// CoreDrive RX app link on #/rx-coverage (clientRxCoverage.appUrl, sent as
// clientRxAppUrl by /api/config/client): a QR code of exactly that URL plus
// the link, and no panel at all without it. Loads the real
// public/rx-coverage.js and the vendored public/vendor/qrcode.js in a vm.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(REPO_ROOT, 'public', 'rx-coverage.js'), 'utf8');
const qrSrc = fs.readFileSync(path.join(REPO_ROOT, 'public', 'vendor', 'qrcode.js'), 'utf8');
function escapeHtml(s) {
  if (s == null) return '';
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
const sandbox = {
  window: {},
  document: { getElementById: function () { return null; } },
  registerPage: function () {},
  console: { warn: function () {} },
  Promise: Promise,
  setTimeout: function () {}, clearTimeout: function () {},
  escapeHtml: escapeHtml,
  L: {}, getComputedStyle: function () { return { getPropertyValue: function () { return ''; } }; },
};
vm.createContext(sandbox);
vm.runInContext(qrSrc, sandbox);
vm.runInContext(code, sandbox);
const t = sandbox.window.CSRxCoverage._test;
const qrcode = sandbox.qrcode;
assert.strictEqual(typeof qrcode, 'function', 'vendor qrcode.js must define qrcode()');

// No URL: no panel.
assert.strictEqual(t.appPanelHtml('', qrcode), '');
assert.strictEqual(t.appPanelHtml(undefined, qrcode), '');

// A URL: QR code of exactly that URL, the link, the instruction line.
const url = 'https://rx.example.org/beta/';
const html = t.appPanelHtml(url, qrcode);
const want = qrcode(0, 'M'); want.addData(url); want.make();
assert.ok(html.indexOf(want.createSvgTag(3, 12)) !== -1, 'panel must hold the QR code of exactly the URL');
assert.ok(html.indexOf('<a class="rx-app-url" href="' + url + '" target="_blank" rel="noopener">' + url + '</a>') !== -1, html);
assert.ok(html.indexOf('Settings &gt; Deployment') !== -1, html);
assert.ok(/id="rxApp"/.test(html), html);

// The URL is escaped in the attribute and the text.
const evil = 'https://rx.example.org/"><img src=x>';
const eh = t.appPanelHtml(evil, qrcode);
assert.ok(eh.indexOf('<img') === -1 && eh.indexOf('&quot;&gt;&lt;img src=x&gt;') !== -1, eh);

// Without the vendor script the link still shows, without a QR code.
const noQr = t.appPanelHtml(url, undefined);
assert.ok(noQr.indexOf('rx-app-qr') === -1 && noQr.indexOf('href="' + url + '"') !== -1, noQr);

// Wiring: the page renders the panel from the config global and the vendor qrcode.
assert.ok(code.indexOf('appPanelHtml(window.MC_CLIENT_RX_APP_URL, window.qrcode)') !== -1, 'pageHtml must render the app panel');
const roles = fs.readFileSync(path.join(REPO_ROOT, 'public', 'roles.js'), 'utf8');
assert.ok(roles.indexOf("window.MC_CLIENT_RX_APP_URL = typeof cfg.clientRxAppUrl === 'string' ? cfg.clientRxAppUrl : '';") !== -1, 'roles.js must read clientRxAppUrl');

console.log('test-rx-coverage-app-qr: OK');
