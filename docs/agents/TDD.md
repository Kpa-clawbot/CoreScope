# TDD — Mandatory Cycle

You DO NOT write production code without a failing test first. This is non-negotiable.

## The cycle

1. **Write a test that demonstrates the bug or the new behavior. Commit it. CI must FAIL on this commit** — that's the proof the test gates the change.
2. **Write the smallest production code that makes the test pass. Commit it. CI must GREEN.**
3. (Optional) **Refactor for clarity, keeping CI green.**

## Red commit quality bar

- MUST compile/build successfully (no missing imports, no undefined symbols).
- MUST run the test to completion (not crash before reaching assertions).
- MUST fail on an **assertion** ("expected X, got Y") — NOT a build/import error.
- If the function doesn't exist yet, add a minimal stub (return zero/nil/empty) so the test executes and fails on the assertion.
- A compile error is NOT a valid red commit — it proves nothing about behavior gating.

PR commit history must show: red commit → green commit. Reviewers verify this; the merge-gate (axis 4) checks it programmatically.

## Exemptions (require explicit justification in PR body)

- **Pure refactors**: existing tests MUST remain byte-unchanged (renames OK, behavior changes NOT) AND green. PR body must cite: "tests/ files diff: no changes" OR per-altered-test justification.
- **Config changes**: existing tests MUST stay green AND unaltered. PR body must cite: "no test files modified" + "CI green without test edits."
- **Net-new UI surfaces** (no prior assertions to break): test must land in the SAME PR but doesn't need to be the FIRST commit. Bug fixes on EXISTING UI still require red-then-green (E2E-DOM-grep tests are valid).
- **Pure docs / pure comments**: no test required. Kent Beck gate still runs and rubber-stamps with "no behavior change" justification.

## Rationale

Tests written after the fact are advocacy, not validation. A test that doesn't fail when reverted is not a test — it's a tautology. TDD forces the API to be testable; if you can't write the failing test, the design is wrong, fix the design first.

## What blocks merge

- No red commit on the branch (when not exempt).
- Test commit doesn't actually fail when reverted (Kent Beck verifies).
- Tests added in same commit as fix (no red→green visible).
- Test mirrors the implementation rather than asserting behavior (tautology).
- Refactor PR with altered test files lacking explicit justification.
