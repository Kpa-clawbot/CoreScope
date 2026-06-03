// bad-1-template-literal.js — XSS fixture for check-xss-sinks.
// Unescaped ${name} (node-controlled) interpolated into innerHTML.
// EXPECTED: flagged by check-xss-sinks.
/* eslint-disable */
function render(el, name) {
  el.innerHTML = `<div class="node">${name}</div>`;
}
