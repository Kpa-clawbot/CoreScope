// bad-11-comment-rubber-stamp.js — escapeHtml mentioned only in a comment.
// Old has_escape line-level rubber-stamps; new strips comments first.
/* eslint-disable */
function render(el, name) {
  el.innerHTML = `<div>${name}</div>`; // TODO: escapeHtml(name)
}
