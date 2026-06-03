// bad-7-outerHTML.js — outerHTML sink, not covered by old script.
/* eslint-disable */
function render(el, name) {
  el.outerHTML = `<div>${name}</div>`;
}
