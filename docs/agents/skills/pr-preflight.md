---
name: pr-preflight
description: "Mandatory pre-PR-submission checklist that runs concrete fail-fast checks (grep/git/scripts) to catch the rework-prone failure classes seen on prior PRs (assertion-shaped tests, CSS-var theming illusions, LIKE-on-JSON attribution, sync migrations on large tables, branch-scope contamination, PII leaks). Runs in <60s. Loaded by fix-issue and pr-polish skills BEFORE any subagent invokes `gh pr create` or `gh pr ready`. Triggers: 'pr preflight', 'preflight checks', 'before opening PR', 'pre-submission gate'. NOT for: post-merge audits, CI watching, or initial code review."
---

# PR Preflight

Fast, scriptable gate that runs immediately before `gh pr create` (or `gh pr ready` on a draft). Each check is ONE command. Hard gates BLOCK submission until fixed or explicitly overridden in the PR body.

## When to run

- **fix-issue skill, Step 1:** AFTER the implementation subagent commits + pushes, BEFORE it runs `gh pr create`. (Add to the subagent's brief: "Run preflight checklist from `~/.openclaw/skills/pr-preflight/SKILL.md` before opening the PR.")
- **pr-polish skill, Subagent 1 (rebase + self-review):** AFTER rebase, BEFORE pushing fixes. Re-run if any new commits land.
- **Manual:** `bash ~/.openclaw/skills/pr-preflight/scripts/run-all.sh <BASE>` from the worktree (default BASE=`origin/master`).

## Inputs

- Worktree at HEAD of the feature branch
- `BASE` ref to diff against (default `origin/master`)
- Repo with `scripts/check-css-vars.js` and `cmd/server/db.go` (CoreScope-shaped — degrade gracefully if absent)

## Hard gates (BLOCK submission on fail)

| # | Check | Script | What it catches |
|---|-------|--------|-----------------|
| 1 | PII | `scripts/check-pii.sh` | Personal names, IPs, paths, API keys in diff (P-13) |
| 2 | Branch scope | `scripts/check-branch-clean.sh` | Worktree contamination — files touched outside declared scope (P-6) |
| 3 | Red commit | `scripts/check-red-commit.sh` | Test commits that don't actually fail when reverted (P-1) |
| 4 | CSS-var defined | `scripts/check-css-vars.sh` | `var(--X)` referenced but never defined (P-3) |
| 5 | CSS self-fallback | `scripts/check-css-self-fallback.sh` | Tautological `var(--X, var(--X, ...))` (P-2) |
| 6 | LIKE-on-JSON attribution | `scripts/check-decoded-json-like.sh` | `decoded_json LIKE '%X%'` for attribution (P-4) |
| 7 | Sync migration | `scripts/check-async-migration.sh` | Sync `ALTER`/backfill on >1K-row table (P-5) |
| 7b | Async-migration annotation | `scripts/check-async-migrations.sh` | New `CREATE INDEX`/`ALTER TABLE` in a migration file without `// PREFLIGHT: async=true reason="..."` annotation, `RunAsyncMigration` wrapper, or `PREFLIGHT-MIGRATION-SCALE:` PR-body opt-out (#791, #1483 regression class) |

## Warnings (log but allow; require ack in PR body if present)

| # | Check | Script | What it catches |
|---|-------|--------|-----------------|
| 8 | `<img>` SVG ratio | `scripts/check-img-svg-ratio.sh` | width/height attrs ≠ SVG viewBox aspect (P-9) |
| 9 | Themed `<img>` SVG | `scripts/check-img-themed-svg.sh` | `<img src="*.svg">` for assets that should theme (P-2 cousin) |
| 10 | Stale fixture coverage | `scripts/check-fixture-coverage.sh` | New `public/<subdir>/` not referenced in fixture build (P-7) |

## Run

```bash
bash ~/.openclaw/skills/pr-preflight/scripts/run-all.sh origin/master
```

Exit 0 = clean, exit 1 = hard-gate failure (do NOT open PR), exit 2 = warnings only (PR body must ack).

## Pass/fail criteria

- Hard gate fails → fix the underlying issue. Re-run. Do not proceed to `gh pr create`.
- Warning fails → either fix it OR add to PR body:
  ```
  ## Preflight overrides
  - check-img-svg-ratio: justified — wordmark intentionally letterboxed (3.08:1 vs 4:1)
  ```

## Frontend UX bug — extra mandatory clauses

If issue is labeled `area:frontend` or the diff touches `public/*.{js,css,html}`:
- PR body MUST contain: `Browser verified: <staging URL or screenshot path>` (catches P-8 — JSDOM-only "works fine" against operator-confirmed staging bug).
- PR body MUST contain: `E2E assertion added: <file>:<line>` (already required by fix-issue skill; preflight asserts the line exists).

## When to override (hard gate)

Document in the PR body under `## Preflight overrides` with this exact format:

```
## Preflight overrides
- <check-name>: <one-sentence justification>
- check-decoded-json-like: justified — full-text search on message body, not pubkey attribution; query is on `body` column not `decoded_json`
```

If a hard gate fires and you cannot justify it in one sentence, the override is invalid. STOP and ask.

## Why these checks (links to references)

- `references/patterns-found.md` — condensed pattern catalog from `pr-checklist-analysis.md`
- `references/tdd-discipline.md` — what makes a red commit genuine (P-1)
- `references/css-var-hygiene.md` — defined-vs-referenced + theming propagation (P-2, P-3)
- `references/like-on-json-attribution.md` — why substring search on JSON is a structural bug (P-4)
- `references/async-migration.md` — when sync `ALTER`/`UPDATE` is forbidden + the existing async pattern (P-5)
- `references/frontend-e2e.md` — JSDOM is insufficient for UX bugs (P-8)

## Integration with fix-issue / pr-polish

- **fix-issue:** read this skill at the end of Step 1 (the implementation subagent must run preflight before `gh pr create`). Add to the brief: `Before \`gh pr create\`, run: bash ~/.openclaw/skills/pr-preflight/scripts/run-all.sh origin/master — fix all hard-gate failures, document warnings in PR body.`
- **pr-polish:** read this skill at the start of Subagent 1 Phase 2 (Self-Review & Fix). Re-run after any push.

## Performance budget

Total runtime target: <60 seconds. Each individual check budget: <10 seconds. If a check exceeds budget, demote it from this gate to CI.

## Notes

- All scripts are POSIX-portable bash + grep + git + jq + node (only for `check-css-vars.sh` if `scripts/check-css-vars.js` exists in the target repo).
- Scripts degrade gracefully when target files are absent (e.g., non-CoreScope repos).
- Scripts read the diff against `BASE`, not the entire worktree — fast even on large repos.
