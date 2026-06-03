// bad-4-bare-ident.js — innerHTML = bare identifier, NO quote/backtick.
// Old script's quote-required regex misses this. New script must flag.
/* eslint-disable */
function render(el, name) {
  el.innerHTML = name;
}
