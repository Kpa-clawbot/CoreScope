// good-4-tested.js — unescaped sink, covered by REAL render-and-grep test
// (markers in executable code, not just comments).
/* eslint-disable */
function render(el, name) {
  el.innerHTML = `<div>${name}</div>`;
}
module.exports = { render };
