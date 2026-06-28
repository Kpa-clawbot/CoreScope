---
name: pr-merge-gate
description: Run the mandatory three-axis merge-readiness check on a GitHub PR per AGENTS.md rule 20. Verifies git mergeable state, CI green, and review-thread resolution in a single structured pass. Returns PASS/FAIL with concrete failing axis cited. Use when about to claim a PR is "merge-ready", "ready to merge", "approved", or "MERGE READY". Triggers: "is #N ready to merge", "rule-20 check on #N", "verify #N can merge", "is this approved". NOT for: code review (use pr-polish), conflict resolution (use rebase-or-force), adversarial review (use pr-polish with personas).
---

# pr-merge-gate

## When to invoke
- Before saying "MERGE READY" to the user about any PR
- After a pr-polish subagent claims "approved" — verify their claim against the gate
- When the user asks "can we merge #N"

## What it does
Runs the three-axis check from AGENTS.md rule 20:

### Axis 1 — git mergeable
```bash
gh pr view <N> --repo <owner>/<repo> --json mergeable,mergeStateStatus
```
PASS only if `mergeable == "MERGEABLE"` AND `mergeStateStatus == "CLEAN"` (or `BEHIND` is acceptable if rebase trivial).
FAIL if `CONFLICTING`, `DIRTY`, `UNKNOWN` for >1 minute (re-poll once before failing).

### Axis 2 — CI green
```bash
gh pr checks <N> --repo <owner>/<repo>
```
PASS if all required checks pass (skipped is OK).
FAIL if any FAILURE or PENDING (latter = not ready, retry later).

### Axis 3 — review threads resolved
```bash
gh api /repos/<owner>/<repo>/issues/<N>/comments --jq '.[] | "@\(.user.login): \(.body[:200])"'
gh api /repos/<owner>/<repo>/pulls/<N>/comments --jq '.[] | "@\(.user.login): \(.body[:200])"'
gh api /repos/<owner>/<repo>/pulls/<N>/reviews --jq '.[] | "@\(.user.login) \(.state)"'
```
Then BLOCKER detection on the comment text:
```bash
grep -ciE "BLOCKER|MAJOR|^- \[ \]|TODO|FIXME|must.fix|needs.work" <comment-bodies>
```
PASS only if:
- No `CHANGES_REQUESTED` review state without follow-up resolution comment
- No unaddressed BLOCKER/MAJOR in inline comments
- No reviewer push-back without author response

## Output format
Structured verdict (always all 4 axes, even if first fails):

```
## PR #<N> — pr-merge-gate verdict: PASS | FAIL

| Axis | State | Details |
|---|---|---|
| 1. Git mergeable | PASS/FAIL | <mergeable>/<mergeStateStatus> |
| 2. CI green | PASS/FAIL | <pass-count>/<total>, failing: <list> |
| 3. Review threads | PASS/FAIL | <BLOCKER count>, <MAJOR count>, CHANGES_REQUESTED: <state> |
| 4. Test quality (Kent Beck gate) | PASS/FAIL | <cite review comment URL or "no Kent Beck pass on record"> |

If FAIL: cite the specific failing item(s) with the exact comment URL or check URL.
If PASS: declare merge-ready.
```

### Axis 4 — Test quality (Kent Beck gate)
Check whether the PR has a passing Kent Beck gate review on record:
```bash
gh api /repos/<owner>/<repo>/pulls/<N>/reviews --jq '.[] | select(.body | test("Kent Beck Gate: PASS|Kent Beck Gate.*PASS")) | "@\(.user.login) \(.state) \(.html_url)"'
```
Also check PR comments:
```bash
gh api /repos/<owner>/<repo>/issues/<N>/comments --jq '.[] | select(.body | test("Kent Beck Gate.*PASS|Kent Beck Gate: PASS")) | .html_url'
```
PASS if a Kent Beck Gate PASS exists AND no subsequent NEEDS-WORK verdict overrides it.
FAIL if no Kent Beck pass on record OR if the most recent Kent Beck review is NEEDS-WORK/CHANGES_REQUESTED.

**Additionally, verify TDD commit history (red→green pattern):**
```bash
git log origin/master..origin/<branch> --reverse --oneline --name-only
```
Confirm: first non-merge commit's files are ALL test files (matching `*_test.go`, `test-*.js`, `tests/`, `*_test.py`, `*.test.ts`, `*.spec.*`). If not, check PR body for exemption justification:
```bash
gh pr view <N> --repo <owner>/<repo> --json body -q .body | grep -iE 'pure refactor|config change|net-new UI|pure docs|no test files modified|tests/ files diff: no changes'
```
PASS if red→green pattern confirmed OR valid exemption keyword found in PR body.
FAIL if neither red→green pattern nor exemption justification exists.

## Rules of engagement
- **NEVER claim merge-ready without running this gate.** Rule 20 is a Rule 5 violation if you claim merge-ready from incomplete state.
- **Read the actual review body**, not just the headline state. Subagent summaries lie by omission (Rule 23).
- **`UNKNOWN` mergeable state**: poll once after 60s. If still UNKNOWN, FAIL (likely race condition or external check still computing).
- **`SKIPPED` checks are OK** (workflow conditional skips on docs-only changes, etc).
- **`PENDING` checks are FAIL.** Wait for them to complete; don't preemptively claim ready.
- **For PRs from forks**: axis 1 still works; axis 2 may be missing some checks (CI doesn't run on fork PRs sometimes). Note this in the verdict.

## Anti-patterns this prevents
1. "MERGEABLE = ready" — git-clean isn't reviewer-clean
2. "subagent said APPROVED" — header lies, body might have BLOCKERs
3. "CI green = ready" — open BLOCKER comments still kill it
4. "CHANGES_REQUESTED but the changes were made" — verify the requestor's resolution, not just the existence of follow-up commits

## Pattern that earned this skill
PR #926 was called "MERGE-READY" by a push-to-merge subagent because gh said `MERGEABLE`. Two BLOCKERs sat in review threads from an external reviewer + the prior bot review, unaddressed. The maintainer caught it and flagged that open issues from #926 had not been addressed, making "mergeable" extremely misleading. Required PR #970 to actually fix. This skill runs the check the subagent skipped.
