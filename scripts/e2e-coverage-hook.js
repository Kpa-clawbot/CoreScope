'use strict';
// Preload for the E2E shards (#2203): `NODE_OPTIONS=--require ./scripts/e2e-coverage-hook.js`.
// Every Chromium the suite launches is tracked by scripts/e2e-coverage.js, and
// the summed coverage is written to .nyc_output/ when the process exits. A
// suite that never loads an instrumented page writes nothing.

const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright');
const { createCollector, writeCoverage } = require('./e2e-coverage');

const collector = createCollector();
collector.wrapLaunch(chromium);

process.on('exit', () => {
  writeCoverage(fs, path, collector.store, path.resolve(__dirname, '..', '.nyc_output'),
    path.basename(process.argv[1] || 'node', '.js'), process.pid);
});
