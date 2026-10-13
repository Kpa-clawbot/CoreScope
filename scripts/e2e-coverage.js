'use strict';
// Placeholder for #2203: the test in tests/unit/test-e2e-coverage.js defines
// the behaviour; nothing is collected yet.

function readAndReset() { return '{}'; }
function mergeInto() {}
function createCollector() {
  const store = {};
  const pass = x => x;
  return { store, collect: async () => {}, trackPage: pass, trackContext: pass, trackBrowser: pass, wrapLaunch() {} };
}
function writeCoverage() { return null; }

module.exports = { readAndReset, mergeInto, createCollector, writeCoverage };
