/* Canary file for TDD red commit on issue #1342.
 * Asserts the eslint no-undef gate actually fails on an undefined variable.
 * Removed in the green commit. */
'use strict';
undefinedCanaryVar.shouldFailLint();
