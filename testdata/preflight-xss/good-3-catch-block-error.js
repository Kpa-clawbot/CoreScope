// good-3-catch-block-error.js — passes check-xss-sinks: exception
// .message access inside catch is NOT node-controlled.
// EXPECTED: clean.
/* eslint-disable */
function run(el) {
  try {
    JSON.parse('not json');
  } catch (e) {
    el.innerHTML = `<div class="err">${e.message}</div>`;
  }
}
