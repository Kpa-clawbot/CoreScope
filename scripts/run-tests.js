#!/usr/bin/env node
'use strict';

const { spawnSync } = require('child_process');
const path = require('path');

const root = path.resolve(__dirname, '..');
const tests = [
  'test-packet-filter.js',
  'test-packet-filter-ux.js',
  'test-aging.js',
  'test-frontend-helpers.js',
  'test-hop-resolver-affinity.js',
  'test-live.js',
  'test-url-state.js',
  'test-perf-go-runtime.js',
  'test-channel-psk-ux.js',
  'test-channel-sidebar-layout.js',
  'test-channel-fluid-layout.js',
  'test-channel-modal-ux.js',
  'test-channel-decrypt-insecure-context.js',
  'test-channel-qr.js',
  'test-channel-qr-wiring.js',
  'test-channel-issue-1087.js',
  'test-analytics-channels-integration.js',
  'test-observers-headings.js',
];

console.log('========================================');
console.log('  CoreScope - Test Suite');
console.log('========================================\n');
console.log('-- Unit Tests --');

for (const test of tests) {
  const result = spawnSync(process.execPath, [test], {
    cwd: root,
    stdio: 'inherit',
    env: process.env,
  });
  if (result.error) {
    console.error(`Failed to run ${test}: ${result.error.message}`);
    process.exit(1);
  }
  if (result.status !== 0) {
    process.exit(result.status || 1);
  }
}

console.log('\n========================================');
console.log('  All tests passed');
console.log('========================================');
