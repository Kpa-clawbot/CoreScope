// good-3b-chained-cause.js — chained error.cause.message must not flag.
/* eslint-disable */
function run(el, error) {
  el.innerHTML = `<div>${error.cause.message}</div>`;
}
function run2(el, parseError) {
  el.innerHTML = `<div>${parseError.message}</div>`;
}
function run3(el, myErr) {
  el.innerHTML = `<div>${myErr.cause.stack}</div>`;
}
