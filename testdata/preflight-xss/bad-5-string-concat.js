// bad-5-string-concat.js — innerHTML = ident + '<b>'. Old script
// only matches when RHS begins with quote/backtick; concat slips.
/* eslint-disable */
function render(el, name) {
  el.innerHTML = name + '<b>extra</b>';
}
