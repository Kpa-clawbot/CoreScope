---
name: triage-sweep
description: Parallel multi-lane triage of an open issue backlog on a GitHub repo. Phase 1 stale-check (already-fixed / dup / stale / still-valid / ask-reporter) across N parallel subagent lanes; Phase 2 effort-sizing + dependency map for the survivors. Read-only on issues; writes findings to memory files for owner review. Use when the user says "triage backlog", "triage issues", "stale-check sweep", "size the backlog", or asks how to parallelize issue work. NOT for: single-issue investigation (use bug-intake), feature spec work (use feature-intake), or PR review (use pr-polish).
---

# triage-sweep

## When to invoke
- "triage the open issues"
- "stale-check sweep"
- "categorize and size the backlog"
- "what should we work on next"
- N>20 open issues and no owner sense of priority

## What it does
Two-phase parallel triage with explicit owner-approval gating between phases.

### Phase 1 — stale-check (parallel)
- Pull all open issues
- Round-robin assign to N lanes (default 8; cap by user concurrency budget)
- Each lane subagent:
  - For each issue: read body + comments, check master commits/PRs grep, dedup search
  - Classify: ✅ already-fixed | 🔄 dup-of-#X | 🪦 stale (>60d no activity) | 🟢 still-valid | ❓ ask-reporter
  - Cite PR # / merge SHA for "fixed" claims; verify via `gh pr view <PR> --json mergedAt,state`
  - Write findings to `lane-N.md` with consistent format
- Parent rolls up into `summary.md` with proposed actions
- **Owner reviews summary, approves which closes/dedups to execute**
- Parent runs the approved `gh issue close ... -c "..."` commands

### Phase 2 — categorize + size (single subagent)
- Read still-valid set from Phase 1
- For each: assign effort (XS/S/M/L/XL), priority sanity check, dependency identification, file-scope hint for parallelization
- Output `phase2-categorized.md` with full table + top-N wave-1 recommendations (high-impact, low-effort, file-disjoint)
- **Owner reviews + picks wave-1 set + concurrency cap**
- Parent batches wave-1 into 1-3-PR concurrent groups by file disjointness

## Required setup directory
Before spawning lanes, create:
```
<workspace>-<NAME>/memory/triage-YYYY-MM-DD/
├── README.md       (rules + output format spec)
├── lanes.md        (lane assignment table)
├── lane-1.md       (each lane writes here)
...
├── lane-N.md
├── summary.md      (parent rolls up)
└── phase2-categorized.md
```

## Lane subagent task brief template

```
## Triage Lane K — read-only stale-check sweep

You are 1 of N parallel triage subagents. Your lane covers these <count> issues:
**#A, #B, #C, ...**

## Setup
1. Read <workspace>-<NAME>/memory/triage-<DATE>/README.md for output format + rules.
2. Read <workspace>-<NAME>/AGENTS.md (rules 18, 19, 20, 22, 23).
3. Read repo AGENTS.md.

## Method (per issue)
1. `gh issue view <N> --repo <owner>/<repo> --comments`
2. `git log --grep "#<N>" origin/master --oneline | head -5`
3. `gh pr list --repo <owner>/<repo> --state merged --search "#<N>"`
4. Dedup search: `gh issue list --repo <owner>/<repo> --state open --search "<terms>"`
5. Classify per README format.

## Categories: ✅ already-fixed | 🔄 dup-of-#X | 🪦 stale | 🟢 still-valid | ❓ ask-reporter

## Hard rules
- READ-ONLY. NO `gh issue close`, NO `gh issue edit`, NO commits, NO pushes.
- Cite PR # for "already-fixed" + verify MERGED via `gh pr view <PR> --json mergedAt,state`.
- HIGH confidence ONLY if you literally read the fix code.
- PII grep before saving (HARD STOP on hit).
- DO NOT spawn sub-chains.

## Output
<workspace>-<NAME>/memory/triage-<DATE>/lane-K.md

## Final reply
Counts per category + confidence breakdown + path to lane-K.md + anything weird.
```

## Phase 2 subagent task brief template

```
## Phase 2 — categorize + size

Input: all still-valid issues from Phase 1 (read summary.md to find them).

For each issue, output:
- Effort: XS (<1h) | S (~3h) | M (~1d) | L (multi-day) | XL (epic)
- Priority sanity check vs current label
- Dependency: blocks what / blocked by what
- File-scope hint: which files / subsystems

## Hard rules
- READ-ONLY. No commits, no closes.
- Cite reasoning for each effort estimate.
- No invented file paths.
- PII grep before saving.

## Output
<workspace>-<NAME>/memory/triage-<DATE>/phase2-categorized.md

Structure: summary by effort, summary by recommended next-action, top-10 wave-1 candidates table, full table, surprises/mis-prioritizations.
```

## Owner-approval gates (mandatory, do not skip)

1. After Phase 1: present rollup. **Wait for owner to approve which closes to execute.**
2. After Phase 2: present wave-1 candidates. **Wait for owner to pick which to start + concurrency cap.**

Per `--auto-close` flag (if user opts in): execute closes immediately after Phase 1 without waiting. Default OFF.

## Known good defaults
- 8 lanes for ~67 issues (~9/lane)
- 3-5 concurrent impls for wave-1 to avoid CI throughput issues
- 60-day stale threshold

## Pattern that earned this skill
Today's session triaged 67 open issues in 8 parallel lanes, categorized survivors, and shipped 5 wave-1 PRs in one work block. The phase gating prevented over-aggressive auto-close while parallel lanes kept the slow part fast.
