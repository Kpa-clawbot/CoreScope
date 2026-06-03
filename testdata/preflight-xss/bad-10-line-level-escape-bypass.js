// bad-10-line-level-escape-bypass.js — one interp escaped, another raw.
// Old line-level has_escape rubber-stamps because escapeHtml( appears on
// the line. New per-interp audit must flag the raw ${name}.
/* eslint-disable */
function escapeHtml(s) { return String(s); }
function render(el, role, name) {
  el.innerHTML = `<div>${escapeHtml(role)} ${name}</div>`;
}
