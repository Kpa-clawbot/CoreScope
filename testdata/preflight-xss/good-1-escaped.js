// good-1-escaped.js — passes check-xss-sinks: escapeHtml wraps the field.
/* eslint-disable */
function escapeHtml(s) {
  if (s == null) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}
function render(el, name) {
  el.innerHTML = `<div class="node">${escapeHtml(name)}</div>`;
}
