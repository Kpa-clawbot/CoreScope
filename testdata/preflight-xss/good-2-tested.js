// good-2-tested.js — interpolates a node-controlled field unescaped,
// but the SAME PR adds test-good-2.js which DOM-greps the audit payload
// against this file. check-xss-sinks must therefore accept this sink.
/* eslint-disable */
function render(el, name) {
  el.innerHTML = `<div class="node">${name}</div>`;
}
