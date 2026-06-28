---
name: ci-watcher
description: Watch a GitHub PR's CI checks and notify the parent when status flips from pending to pass/fail. Use when CI takes >5 min and the parent has other work to do — prevents green CI from sitting unnoticed. Triggers - 'watch CI for PR 1234', 'monitor PR 1234 checks', 'notify when CI flips'. Takes a PR number and optional repo/timeout. Lightweight - single tool call loop, minimal token usage.
---

# CI Watcher

Spawn-and-forget subagent that polls a PR's CI checks until they reach a terminal state, then reports back.

## Input

- **PR number** (required)
- **Repo** (optional) — `owner/repo`. Default: detect from `git remote get-url origin` in cwd
- **Timeout minutes** (optional) — default 30, max 60
- **Poll interval seconds** (optional) — default 60

## Task

Spawn a subagent with this exact brief:

```
## Mission
Watch CI on PR #<NUMBER> in <REPO>. Report back the moment all checks reach a terminal state (pass/fail/cancelled/skipping) — or when the timeout hits.

## Setup
1. AGENTS.md auto-loaded (worker rules)
2. cwd: <REPO_LOCAL_PATH>

## Task
1. Poll loop: every <POLL_INTERVAL>s, run `gh pr checks <NUMBER> --repo <REPO>`
2. Parse the output. Terminal states: `pass`, `fail`, `cancelled`, `skipping`. Non-terminal: `pending`, `queued`, `in_progress`.
3. If any check is non-terminal: wait <POLL_INTERVAL>s using `sleep` or exec yieldMs, then re-poll
4. If ALL checks are terminal: STOP polling, report
5. Hard timeout: <TIMEOUT_MIN> minutes total — if reached without all terminal, report current state

## Hard rules
- DO NOT spawn sub-chains
- DO NOT modify any files
- DO NOT comment on the PR
- DO NOT take any action other than poll + report
- Use `exec` with `yieldMs` ≥ 60000 — never tight loop

## Final reply format
- PR number + URL
- Final state: GREEN / FAILED / TIMEOUT / MIXED
- Per-check status table (name, conclusion, duration)
- Failed check log URLs (if any)
- Total wait time
```

## Parent's Responsibility

After spawning the watcher, the parent should NOT poll the PR itself. The watcher's completion event will arrive as a user message; on receipt, the parent decides next action:

- **GREEN** → spawn pr-polish (or merge if already polished)
- **FAILED** → spawn fix-ci subagent
- **TIMEOUT** → check why (CI runner stuck? infrastructure issue?)
- **MIXED** (some pass, some fail) → fix the failures

## When NOT to use

- CI is already green (just spawn polish directly, no point watching)
- Short CI (<5 min) — just check inline
- No CI configured on the repo

## Brevity

Watcher's final reply: ≤ 8 lines including the per-check table. Lead with the verdict.
