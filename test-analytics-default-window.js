#!/usr/bin/env node
'use strict';

const assert = require('assert');
const fs = require('fs');
const path = require('path');

const analyticsJs = fs.readFileSync(path.join(__dirname, 'public', 'analytics.js'), 'utf8');

assert.ok(
  analyticsJs.includes('<option value="24h" selected>Last 24 hours</option>'),
  'analytics time-window picker should default to Last 24 hours'
);
assert.ok(
  analyticsJs.includes("twInit.value = urlWindow || '24h';"),
  'analytics init should use 24h when no URL window is supplied'
);

console.log('Analytics default window tests passed!');
