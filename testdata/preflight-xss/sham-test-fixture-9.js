// sham-test-fixture-9.js — SHAM coverage test. Mentions bad-9-shamtest-fixture.js basename
// and the audit markers ONLY INSIDE COMMENTS. Old test_covers() rubber-stamps
// this. New test_covers() must reject it because the markers don't appear
// in executable code.
//
// References: bad-9-shamtest-fixture.js
// Markers (in comments only):
//   ' onfocus=alert(1)
//   onerror=alert(1)
'use strict';
console.log('sham test — does nothing');
process.exit(0);
