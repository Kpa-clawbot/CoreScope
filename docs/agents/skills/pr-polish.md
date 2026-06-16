---
name: pr-polish
description: "Rebase, polish, and adversarially review a GitHub PR to merge-ready using a PARALLEL review fan-out (not a sequential persona chain). Use when asked to polish, clean up, or finalize a PR — or with shorthand like '/pr-polish 371'. Triggers: 'polish PR', 'pr-polish', 'clean up PR', 'finalize PR', 'get PR ready to merge', 'review and fix PR'. Takes a PR number and optional repo (defaults to origin remote). NOT for: initial code review only (use github skill), creating new PRs, or PRs on repos without local clones."
---

# PR Polish (v2 — parallel fan-out)

Get a PR merge-ready with INDEPENDENT, PARALLEL quality assurance and a hard cap on rework rounds.

## What changed in v2 (read this first)

The old skill ran reviewers serially: adversarial → fix → expert → fix → kent-beck → fix → re-review each one again. Typical PR burned 8-10 subagents and produced rework loops where fix-N introduced issues caught by review-(N+1).

v2 runs reviewers in **parallel** on the same SHA, **consolidates** all findings into ONE fix subagent, and **verifies** fixes by parent grep (not by re-spawning every persona). Hard cap of 2 rounds. Typical PR: 5-6 subagents.

## Input

- **PR number** (required)
- **Repo** (optional) — `owner/repo`. Default: detect from `git remote get-url origin` in cwd.
- **Risk level** (optional) — user can say "high risk" / "schema migration" / "security" — bumps reviewer count.

## Architecture

```
Round 0 (sequential, 1 subagent):
  rebase + self-review + preflight + push

Round 1 review fan-out (parallel, 3-5 subagents in ONE turn):
  ├─ adversarial reviewer (no persona)
  ├─ expert persona A   (auto-selected, or user-specified)
  ├─ expert persona B   (only if high-risk PR)
  └─ kent-beck gate     (TDD + test quality)

Parent: collect all reviews, dedupe findings, build ONE consolidated fix list

Round 1 fix (1 subagent if needed):
  fix EVERY must-fix from EVERY reviewer in one branch push

Parent verify (no subagent unless ambiguous):
  grep the new diff for each reviewer's specific concern; if unclear, ONE
  fast verify subagent re-checks ONLY the ambiguous items (not full re-review)

Round 2 (only if round 1 fix missed items or introduced new ones):
  same shape — parallel reviewers (only the personas that flagged) + 1 fixer.

HARD CAP: 2 rounds. After round 2, escalate to user. Never auto-spin round 3.
```

Why this works:
- **Parallelism eliminates fix-induced regressions between reviewers.** All reviewers see the same SHA; you fix the union of findings once.
- **Parent grep > re-spawn.** "Did the fix add a nil check at line 47?" is a 1-line grep, not a 2K-token persona session.
- **Hard cap surfaces stuck PRs.** A PR that needs round 3 has a design problem, not a polish problem — the user decides.

## Spawn budget (target)

| PR shape | Round 0 | Round 1 reviewers | Fix | Verify | Total |
|---|---|---|---|---|---|
| Clean (no findings) | 1 | 3 (parallel) | 0 | 0 | **4** |
| Standard | 1 | 3 (parallel) | 1 | 0-1 | **5-6** |
| High-risk | 1 | 4 (parallel) | 1 | 0-1 | **6-7** |
| Round 2 needed | 1 | 3+2 | 2 | 0-1 | **7-9** (cap) |

If you're at 10 spawns on one PR, STOP. Something is wrong with the fix subagent or the reviewers are flagging churn. Escalate.

## Subagent labels (mandatory)

- Round 0:        `pr-<N>-rebase-selfrev`
- Round 1 reviewers (parallel):
  - `pr-<N>-r1-adversarial`
  - `pr-<N>-r1-expert-<name>`
  - `pr-<N>-r1-expert-<name2>` (high-risk only)
  - `pr-<N>-r1-kent-beck`
- Round 1 fix:    `pr-<N>-r1-fix-consolidated`
- Verify (rare):  `pr-<N>-r1-verify`
- Round 2:        `pr-<N>-r2-...` (same shape)

## Expert persona selection

Personas live at `<personas-dir>/` (the `personas/` mirror in this skill is legacy).

Pick ONE expert by what the diff touches. For high-risk PRs (schema migrations, auth, irreversible ops), pick TWO and run them in parallel.

| PR characteristics | Expert |
|---|---|
| New tables, schema, data model, persistence, startup/shutdown | **munger** |
| Hot paths, large data, memory, caching, eviction, ingest | **carmack** |
| Refactoring, abstractions, code reorganization, complexity | **torvalds** |
| Auth, network, input parsing, API endpoints, secrets | **djb** |
| Error handling, retries, timeouts, queues, degradation | **taleb** |
| State machines, concurrency, algorithms, correctness proofs | **dijkstra** |
| MeshCore protocol, packet semantics, firmware behavior | **meshcore** |
| Operator UX, deployability, real-world mesh ops | **mesh-operator** |
| Visualization, charts, data density | **tufte** |

When in doubt: **munger**. User override always wins ("with carmack" → carmack).

## Round 0: Rebase + Self-Review (sequential, 1 subagent)

Spawn label: `pr-<N>-rebase-selfrev`. timeout: 1800s.

Brief template:

```
First: read AGENTS.md in the repo root and follow all rules.
Read: ~/.openclaw/skills/pr-preflight/SKILL.md

## Mission
PR #<N> in <REPO>: rebase onto origin/master, run self-review, fix everything, push.

## Setup — Worktree (mandatory)
1. cd <REPO_PATH>
2. git fetch origin
3. BRANCH=$(gh pr view <N> --repo <REPO> --json headRefName -q .headRefName)
4. git worktree add ../pr-<N>-polish origin/$BRANCH -b pr-<N>-polish-work
5. cd ../pr-<N>-polish

## Phase 0: Pre-flight
- If branch is from a fork: STOP and report (we cannot push).
- gh pr view <N> --json mergeable -q .mergeable — if "CONFLICTING", resolve before anything else.

## Phase 1: Rebase
- git rebase origin/master (check master vs main)
- If >3 conflicting files OR conflicts need design intent: STOP, report, do NOT force-push a guess.
- git push origin HEAD:$BRANCH --force-with-lease (allowed on bot PRs in active rework — see AGENTS.md)

## Phase 2: Self-review + fix
1. bash ~/.openclaw/skills/pr-preflight/scripts/run-all.sh origin/master  (must exit 0 or 2 with documented warnings)
2. git diff origin/master...HEAD — read every file
3. Fix everything you find. No "minor/non-blocking" category. Watch tautological tests.
4. Run tests.
5. Re-run preflight.
6. Push fix commits as REGULAR commits (no force-push).

## Cleanup
cd <REPO_PATH> && git worktree remove ../pr-<N>-polish

## Hard rules
- DO NOT spawn sub-chains. Parent owns the chain.
- PII Preflight before every commit/PR-write (AGENTS.md).
- Force-with-lease ONLY on the rebase. Fix commits are regular pushes.

## Final reply format
- HEAD SHA after push: <sha>
- Files changed in self-review fixes: <list>
- Preflight final exit code: <0|2>
- "no issues found" if nothing to fix
```

Wait for round 0 to finish before round 1.

## Round 1: Parallel Review Fan-Out

**Spawn ALL reviewers in the SAME turn (one tool-call block).** Do not wait for one before spawning the next. They are independent and operate on the same SHA.

Standard PR: 3 reviewers (adversarial + 1 expert + kent-beck).
High-risk PR: 4 reviewers (adversarial + 2 experts + kent-beck).

### Reviewer brief template (used for adversarial + each expert)

For the adversarial reviewer, omit the persona injection. For experts, inject the full persona file at the top.

```
First: read AGENTS.md in the repo root and follow all rules.

<IF EXPERT: paste full contents of <personas-dir>/<name>.md here>
<IF EXPERT: end with "---" separator>

## Mission
Independent review of PR #<N> in <REPO>. You have NO prior context — review cold.

## Hard rule: do NOT checkout the branch locally.
Use `gh pr diff` only. Working tree may be contaminated.

## Steps
1. gh pr view <N> --repo <REPO>
2. gh pr diff <N> --repo <REPO>
3. For full file context: git fetch origin <branch> && git show origin/<branch>:<path>
4. Review the diff systematically.
5. Verify diff matches the PR description's claimed scope. If you see unrelated code, STOP — wrong diff.

## Severity Rules — TWO categories only
- **Must-fix**: anything in the diff that is improvable. Includes everything you'd otherwise call "minor", "nit", "non-blocking", "suggestion". Code smells, DRY, missing assertions, naming, dead code, tautological tests — ALL must-fix.
- **Out-of-scope**: cannot be fixed in this PR (pre-existing, architectural). File an issue.

There is NO non-blocking / informational category.

## Output
Post review as PR comment labeled "<LABEL>" where LABEL =
  - adversarial reviewer: "Independent review (round <R>)"
  - expert: "<EXPERT_NAME> Review (round <R>)"
  - kent-beck: "Kent Beck Gate (round <R>) — TDD + test quality"

Use `gh pr review --request-changes` if ANY must-fix items. Approve only if zero.

## Final reply to parent (mandatory format)
- Verdict: APPROVED | NEEDS-WORK
- Must-fix count: <N>
- Must-fix items: numbered list, each ≤ 1 line, citing file:line
- Out-of-scope items (if any): numbered list
- Comment URL: <url>
```

### Kent Beck gate (always parallel in round 1, never sequential)

Same template, plus inject `<personas-dir>/kent-beck.md`, plus the TDD-history check:

```
## Additional check: TDD red→green history
1. git log origin/master..origin/<BRANCH> --reverse --oneline --name-only
2. First non-merge commit MUST add ONLY test files (no production code).
3. gh run list --commit <FIRST_COMMIT_SHA> --json conclusion -q '.[0].conclusion' MUST be "failure".
4. Second commit adds production code, CI passes.
5. If exemption claimed (refactor/config/net-new-UI/docs), verify per AGENTS.md.
6. If no red commit AND no valid exemption: NEEDS-WORK with "TDD violation: no red commit".

## Six Questions (apply systematically)
a. Show me the test that fails when this change is reverted.
b. Smallest test that would have caught the original bug?
c. Could a wrong implementation pass this test?
d. What edge cases are NOT tested?
e. Is the test name describing behavior or implementation?
f. Test setup more complex than test? API is wrong.
```

## Parent: Consolidate findings

After all round-1 reviewers report, the PARENT (you) does this — **no subagent needed**:

1. Collect each reviewer's `Must-fix items` lists.
2. Dedupe (multiple reviewers often flag the same line).
3. Build one numbered list with reviewer attribution: `[adv]`, `[carmack]`, `[kent-beck]`.
4. If ALL reviewers approved with zero must-fix: skip to "Verify merge-ready" below.
5. Otherwise: spawn ONE consolidated fixer.

## Round 1 Fix: Consolidated (only if needed)

Label: `pr-<N>-r1-fix-consolidated`. timeout: 1800s.

Brief:

```
First: read AGENTS.md in the repo root and follow all rules.

## Mission
PR #<N> in <REPO>: address ALL must-fix findings from round 1 reviewers in ONE branch push.

## Setup — Worktree
1. cd <REPO_PATH>
2. git fetch origin
3. BRANCH=$(gh pr view <N> --repo <REPO> --json headRefName -q .headRefName)
4. git worktree add ../pr-<N>-fix-r1 origin/$BRANCH
5. cd ../pr-<N>-fix-r1

## Findings to address (consolidated by parent)
<PASTE THE NUMBERED LIST HERE — every must-fix from every reviewer, with [reviewer] tags>

## Task
1. Fix every numbered item. No skipping. If you believe an item is wrong, push back to parent in your final reply — DO NOT silently skip.
2. One commit per logical group of fixes (not one per item).
3. Run preflight + tests after fixes.
4. Push regular commits (no force-push).
5. Post ONE PR comment listing what was fixed, mapping each fix to the original finding number with ✅ and commit SHA.

## Cleanup
cd <REPO_PATH> && git worktree remove ../pr-<N>-fix-r1

## Hard rules
- DO NOT spawn sub-chains.
- DO NOT request another review — parent does that.
- PII Preflight before every commit/comment.
- One numbered item = at least one assertion of "fix landed at <file>:<line>" in your final reply.

## Final reply format
| # | Reviewer | Finding | Fix file:line | Commit SHA |
Plus: any items you pushed back on (with reasoning).
Plus: comment URL on the PR.
```

## Parent: Verify (cheap, usually no subagent)

For each numbered finding, the parent (you) does:

```bash
gh pr diff <N> --repo <REPO> > /tmp/pr-<N>-r1-final.diff
# For each finding, run a SPECIFIC grep that proves the fix landed.
# Example: "[carmack] preallocate slice at handlers.go:142"
grep -n "make(\[\]" /tmp/pr-<N>-r1-final.diff | grep handlers.go
```

If the grep is unambiguous: mark ✅, move on.

If unclear (the finding is semantic, not syntactic): spawn ONE `pr-<N>-r1-verify` subagent that re-checks ONLY the ambiguous items by reading `gh pr diff` — NOT a full re-review. Budget: 1 subagent, ≤ 5 minutes, ≤ 1500-token brief.

Verify subagent brief:

```
## Mission
Verify these specific items landed in PR #<N> after the round-1 fix. NOT a full review.

## Items
<numbered list of ambiguous items only>

## Hard rule
Read gh pr diff <N> --repo <REPO> only. Do not flag NEW issues — that's not your job.

## Final reply
| # | Item | Landed? (yes/no/partial) | Evidence (file:line in diff) |
```

## Round 2 (only if round 1 fixer didn't address everything)

Same shape as round 1, but **only re-spawn the personas that still have unresolved must-fixes**. If only Kent Beck still has an issue: parallel = 1 reviewer. Don't re-run Carmack for fun.

Label: `pr-<N>-r2-...`

**HARD CAP: round 2 is the last round.** If after round 2 the PR still has unresolved must-fixes:
1. Post a status comment on the PR listing remaining items.
2. Report to user with: "PR #<N> stuck after 2 rounds. Remaining: <list>. Recommend: <option A: defer / option B: redesign / option C: user override>."
3. STOP. Do NOT spawn round 3.

## Verify merge-ready (parent, in-turn)

Before declaring merge-ready, the parent runs ALL three in the same turn (rule 20):

```bash
gh pr view <N> --json mergeable,mergeStateStatus -q '{m:.mergeable, s:.mergeStateStatus}'
gh pr checks <N>
gh pr view <N> --json reviews -q '[.reviews[] | select(.state=="CHANGES_REQUESTED")]'
```

All three must show: mergeable=MERGEABLE/CLEAN, all checks SUCCESS, no unresolved CHANGES_REQUESTED.
If any axis fails: NOT merge-ready, name which axis, do NOT relay merge-ready to user.

## Communication budget

- PR comments by reviewers: ≤ 100 words per concern, one issue per bullet.
- PR description edits: ≤ 250 words.
- Status update to user after round 1: ≤ 6 lines.
- Final merge-ready report: ≤ 8 lines with the three-axis output inline.

## Hard rules

- **Never leak prior reviewer context between reviewers** — each gets ONLY PR# + repo + (if expert) the persona file.
- **Never checkout the branch locally for reviews** — `gh pr diff` only.
- **Always use git worktrees** for write operations.
- **Spawn round-1 reviewers in parallel** (one tool-call block). Sequential = bug.
- **One consolidated fixer per round.** Multiple fixers on the same branch race.
- **Cap at 2 rounds.** Escalate at round 3.
- **Parent verifies fixes by grep** before re-spawning anything.
- **Always label subagents** with the round number and role.
- **Read AGENTS.md** at the start of every subagent (auto in worker prompt).
- **PII Preflight** before every commit/PR-write on public repos.
- **No force-push** except round-0 rebase (force-with-lease).

## Brevity (artifacts)

- PR descriptions ≤ 250 words.
- PR/issue comments ≤ 100 words.
- Issue bodies ≤ 300 words.
- Chat replies ≤ 6 lines unless asked for detail.
- Lead with the answer. No throat-clearing. No marketing voice.

If a reply exceeds the limit, the first line must say why ("Long because: 14 commits to summarize"). Otherwise trim.
