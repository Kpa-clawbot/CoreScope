---
name: fix-issue
description: "Fix a GitHub issue end-to-end: implement the fix, open a PR, wait for CI, auto-fix CI failures (up to 2 attempts), then hand off to pr-polish for independent review. Use when asked to fix an issue by number. Triggers: 'fix issue 350', 'fix #350', 'work on issue 350', 'implement issue 350', 'fix-issue 350'. Takes an issue number and optional repo. Uses git worktrees for isolation so multiple fixes can run in parallel. NOT for: issues requiring design discussion first, or multi-repo changes."
---

# Fix Issue

End-to-end pipeline: read issue → implement fix → PR → CI → pr-polish.

## Input

- **Issue number** (required) — extract from user message
- **Repo** (optional) — `owner/repo` format. Default: detect from `git remote get-url origin` in cwd

## Pipeline Overview

```
Subagent 1: Implement fix (worktree-isolated)
    ↓
Wait for CI
    ↓
If CI fails: Subagent 2: Fix CI (max 2 attempts)
    ↓
Hand off to pr-polish skill (2-subagent review)
```

## Step 1: Spawn Fix Subagent

Spawn a subagent with this task structure (fill in issue number, repo, cwd):

```
First: read AGENTS.md in the repo root and follow all rules.

Fix issue #<NUMBER> in <REPO>.

## Setup — Use a Git Worktree
1. cd <REPO_ROOT>
2. git fetch origin
3. git worktree add ../fix-issue-<NUMBER> origin/master -b fix/issue-<NUMBER>
4. cd ../fix-issue-<NUMBER>
5. Do ALL work in this worktree — never touch the main checkout

## Implement
1. Read the issue: gh issue view <NUMBER> --repo <REPO>
2. Understand the codebase context — read relevant files
3. **Write a failing test FIRST** that reproduces the bug or demonstrates the new behavior. Commit ONLY the test file(s). Push. CI MUST FAIL on this commit — that's the proof.
4. Write the smallest production code that makes the test pass. Commit it separately. Push. CI must GREEN.
5. (Optional) Refactor for clarity, keeping CI green.
6. **Run preflight checklist BEFORE `gh pr create`:** `bash ~/.openclaw/skills/pr-preflight/scripts/run-all.sh origin/master`. Fix all hard-gate failures; document any warnings under `## Preflight overrides` in the PR body. (See `~/.openclaw/skills/pr-preflight/SKILL.md` for details.)
7. Create a PR with a thorough description. First line of PR body: "Red commit: <SHA> (CI run: <URL>)"
8. Reference the issue: "Fixes #<NUMBER>" in PR body

### Frontend UI fixes — MANDATORY E2E coverage
If the fix touches ANY frontend file (`public/*.js`, `public/*.css`, or HTML) AND the bug manifests
as a user-visible UX behavior (click/hover/navigation/rendering), the PR MUST include a
browser-level assertion that would have caught the original bug — NOT only unit tests.

- Look for an existing E2E harness first (e.g. `test-e2e-playwright.js`, `e2e/`, `tests/e2e/`,
  `playwright.config.*`, or whatever the repo uses). Add the assertion there.
- If no E2E harness exists, ADD a small one rather than skip. A 30-line Playwright fixture
  hitting a public staging URL is acceptable.
- The assertion must exercise the exact UX the issue describes. Example: if the bug is
  "clicking observation B doesn't update hex pane", the test must click obs B and assert
  the hex pane's text differs from obs A's.
- Passing JSDOM-style unit tests alone are INSUFFICIENT for UX bugs — they mock the DOM and
  routinely pass while the real UI is broken. Do not ship a frontend UX fix with only unit tests.
- In the PR body, include a one-line "E2E assertion added: `<file>:<line>`" note.

Rationale: unit tests check your abstractions; E2E tests check the thing the user actually
sees. Bugs in glue code between layers (state → render → DOM) only surface in E2E.

## Cleanup
After pushing and creating the PR:
1. cd <REPO_ROOT>
2. git worktree remove ../fix-issue-<NUMBER>

Report back: PR number, what was changed, test results.

IMPORTANT: Do NOT force-push. Push complete work the first time.
```

## Step 2: Wait for CI

After the fix subagent completes and reports the PR number:

1. Wait 30 seconds for CI to start
2. Check CI status: `gh pr checks <PR> --repo <REPO>`
3. If checks are still running, poll every 30 seconds (max 10 minutes)
4. If no CI configured, skip to Step 4

## Step 3: Fix CI Failures (Max 2 Attempts)

If CI fails, spawn a subagent:

```
First: read AGENTS.md in the repo root and follow all rules.

CI failed on PR #<PR> in <REPO>. Fix it.

## Setup
1. gh pr checkout <PR>
2. Read CI failure logs: gh pr checks <PR> --repo <REPO>
3. Get detailed logs for the failed check

## Rules
- ONLY fix issues caused by THIS PR's changes
- Do NOT fix pre-existing CI problems
- If the fix is complex or unclear after 2 attempts, document what's wrong in a PR comment and STOP
- Push fixes as regular commits (not amend/force-push)

## Cleanup
Report: what failed, what you fixed, or why you stopped.
```

If CI fails again after the fix, repeat ONCE more (attempt 2). After 2 failed fix attempts:
1. Post a PR comment documenting the CI failure and what was tried
2. Report to user that CI couldn't be auto-fixed
3. Do NOT proceed to pr-polish

## Step 4: Hand Off to pr-polish

If CI passes (or no CI configured), trigger the pr-polish skill on the PR number.

This means spawning the two pr-polish subagents sequentially:
1. Subagent (Author): rebase + self-review + fix
2. Subagent (Reviewer): independent adversarial review

See the pr-polish skill for exact task templates.

## Key Rules

- **Always use worktrees** — `git worktree add` for isolation. Multiple fix-issue runs must not conflict.
- **Always clean up worktrees** — remove on both success and failure paths.
- **CI fix attempts capped at 2** — document and stop, don't loop forever.
- **Never force-push** — regular commits only (except pr-polish rebase).
- **Read AGENTS.md** — every subagent must read it first.
- **Report progress** — tell the user what's happening at each stage.


## Brevity (with clarity)

Default to short. Long is the exception, justified by content. See [`personas/orchestrator.md`](../personas/orchestrator.md#brevity-with-clarity) for the canonical rules.

**Limits for artifacts this skill produces**
- PR descriptions: ≤ 250 words. One paragraph "what + why," then bullets.
- PR / issue comments: ≤ 100 words. One concern per comment.
- Issue bodies: ≤ 300 words. Problem, evidence, proposed action.
- Chat replies to the human: ≤ 6 lines unless asked for detail.

**Always**
- Lead with the answer; supporting detail follows only if asked.
- Tables for ≥ 3 items with shared shape — never repeat a label across bullets.
- Drop hedges, throat-clearing, re-narration ("Let me…", "First, I…").

**Never**
- Restate the question before answering.
- Marketing voice ("powerful," "comprehensive," "seamlessly").
- Multi-section summaries when one paragraph suffices.

If a reply exceeds the limit, the first line must explain why ("Long because: 14 commits to summarize"). Otherwise trim.
