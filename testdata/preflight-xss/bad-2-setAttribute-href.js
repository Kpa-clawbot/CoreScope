// bad-2-setAttribute-href.js — XSS fixture for check-xss-sinks.
// setAttribute('href', <interpolation>) accepts javascript: URIs.
// EXPECTED: flagged by check-xss-sinks.
/* eslint-disable */
function attach(a, hash) {
  a.setAttribute('href', `#/packets/${hash}`);
}
