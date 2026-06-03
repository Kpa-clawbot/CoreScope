// bad-8-document-write.js — document.write sink.
/* eslint-disable */
function render(name) {
  document.write(`<h1>${name}</h1>`);
}
