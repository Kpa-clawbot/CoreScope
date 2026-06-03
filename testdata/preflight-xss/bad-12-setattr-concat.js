// bad-12-setattr-concat.js — setAttribute href with string concat,
// no `$` in the value. Old regex requires `$`; concat slips.
/* eslint-disable */
function render(a, payload) {
  a.setAttribute('href', 'javascript:' + payload);
}
