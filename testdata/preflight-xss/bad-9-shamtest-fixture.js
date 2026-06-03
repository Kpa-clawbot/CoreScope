// bad-9-sham-test.js — unescaped sink relying on a sham companion test
// that only mentions the markers in COMMENTS.
/* eslint-disable */
function render(el, name) {
  el.innerHTML = `<div>${name}</div>`;
}
module.exports = { render };
