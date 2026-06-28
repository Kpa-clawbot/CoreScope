# WORKFLOW.md — How CoreScope Work Gets Merged

This is the end-to-end pipeline maintainers use. It is **agent-agnostic**: the same shape works under Claude Code, Codex, Cursor, Aider, Continue, or OpenClaw. Tooling differs; the *discipline* does not.

---

## 1. The Pipeline

```
issue ─▶ fix-issue ─▶ CI watch ─▶ pr-polish (parallel fan-out) ─▶ pr-merge-gate ─▶ merge
                       │                │                              │
                       └─ auto-retry    └─ optional gap-fix             └─ human approves
```

Each arrow is a **separate subagent invocation with a fresh context**. Do not collapse the chain into a single "do everything" agent — context bloat and skipped discipline are the failure modes.

### Stages
- **fix-issue** — implement the change. Writes a failing test first (see TDD.md), makes it green, opens a PR. Skill: `skills/fix-issue.md`.
- **CI watch** — long-running watcher that polls CI checks and reports the flip from pending → pass/fail. Skill: `skills/ci-watcher.md`.
- **pr-polish** — adversarial + expert + Kent-Beck personas reviewing the PR **in parallel** in one fan-out. Findings get triaged into BLOCKER / MAJOR / MINOR. Skill: `skills/pr-polish.md`.
- **gap-fix** (optional, one subagent, one round) — if pr-polish surfaces fixable issues, ONE subagent addresses *all* of them at once. Hard cap: 2 polish rounds total.
- **pr-merge-gate** — final three-axis check before merge: git mergeable, CI green, review threads resolved. Skill: `skills/pr-merge-gate.md`.
- **merge** — human approves. Agents do not auto-merge unless explicitly told.

### Why parallel polish?
Sequential persona chains balloon context and serialize wall-time for no gain. Spawn adversarial + expert(s) + kent-beck in the **same tool-call block**, collect findings, dedupe, fix once.

---

## 2. TDD: red → green is mandatory

See `TDD.md` for the full rules. Summary:

1. Write a failing test. Commit it. The commit must **build** and **fail on an assertion** (not a compile error).
2. Write the smallest production code to make it green. Commit it.
3. Optional refactor, stay green.

The PR commit history must show red→green. Reviewers (and the `pr-merge-gate` skill) verify this. Exemptions: pure refactors, config-only, net-new UI surfaces, pure docs — each requires explicit justification in the PR body.

---

## 3. Planning rules — plan-then-go

- Present a plan with milestones **before** implementing anything non-trivial. Wait for sign-off ("go", "ship it", "proceed", an explicit batch instruction).
- Acknowledgment ("good idea", "interesting") is **not** permission.
- Once a batch is signed off, batch language ("do the rest", "next wave") = EXECUTE, not "should I start?".

---

## 4. PII Preflight (MANDATORY before any public-repo write)

CoreScope is a public repo. Every `git commit`, `gh pr create/edit`, `gh issue create/comment`, `gh pr comment/review`, ANY `gh api` write **must** be preceded by a PII grep.

### Sanitized example grep

```bash
grep -nEi 'YOUR_NAME|YOUR_HANDLE|YOUR_PHONE|RFC1918_PRIVATE_IPS|PROD_VM_IPS|/your/home/|api[_-]?key|YOUR_API_KEY_PATTERN' <file-or-diff>
```

**Customize the pattern with your own leak vectors**, e.g.:
- Real names + handles (yours, teammates, operators)
- Phone numbers
- Internal IPs (RFC1918 subnets you use, prod/staging VM IPs)
- Internal hostnames
- API key prefixes / fragments
- Your home directory path (e.g. `/home/alice/`, `/Users/alice/`)
- Workspace-relative paths (e.g. `/var/agent-workspace/...`)

### Workflow
- **Commits**: grep `git diff --cached` AND any new files BEFORE `git commit`.
- **PR/issue bodies**: write body to a tmp file, grep it, THEN `gh ... -F tmpfile`. Never `--body` inline without grepping first.
- **Comments**: same — tmp file, grep, then `-F`.
- **Hits are a HARD STOP**. Fix, re-grep, then send.
- If unsure whether something is PII: ask, don't ship.
- No exceptions for "small" edits or "just a comment".

**Never commit**: real names, phones, IPs, API keys, internal hostnames, or absolute workspace paths under your home/root dir.

---

## 5. Subagent spawn discipline

The pipeline is multi-agent. Keep the roles clean:

- **Parent owns the chain.** Parent dispatches fix → CI watch → polish → gap-fix → merge-gate, one stage at a time, verifying each handoff.
- **Workers DO NOT spawn sub-chains.** If a worker thinks it needs another agent, it stops and reports back with what it needs and why. Parent decides.
- **Every subagent brief follows the template** (`SUBAGENT-BRIEF-TEMPLATE.md`). Briefs missing Mission / Setup / Hard rules / What NOT to do / Final reply format are a discipline failure.
- **Generous timeouts**: 1800s (30 min) minimum on implementation work, 2700s (45 min) for XL. Short timeouts cause mid-implementation kills and duplicate token burn.
- **Parent verifies worker GH writes.** After a worker posts a GH comment, parent fetches the URL and confirms.

---

## 6. Force-push rules

- **Banned**: master, shared branches, branches racing to merge.
- **Allowed (preferred)** with `--force-with-lease`: your own bot-authored PR branch during active rework.
- Never force-push a branch someone else is reviewing without coordinating.

---

## 7. Config documentation rule

Any PR that adds or modifies a config field MUST:

1. Update `config.example.json` with the new field + a sensible default.
2. Add/update the matching `_comment_` field explaining the behavior.
3. Show nested fields in context.

Operators discover config via the example file. Failure to update it blocks merge.

---

## 8. Git worktrees for parallel work

Use worktrees so multiple agents can work without colliding on the main checkout:

```bash
cd <repo>
git fetch origin
git worktree add _wt-<branch> -b <branch> origin/master
cd _wt-<branch>
# work, commit, push
```

When done:
```bash
cd <repo>
git worktree remove _wt-<branch>
```

Each worktree gets its own branch. Subagents are pointed at a worktree path in their brief.

---

## 9. Verifying merge-readiness — the three-axis rule

"Merge-ready" requires ALL THREE in the same turn, each backed by a tool call:

1. **git mergeable** — `gh pr view <N> --json mergeable,statusCheckRollup`
2. **CI green** — `gh pr checks <N>`
3. **Review threads resolved** — `gh pr view <N> --comments` AND `grep -c 'BLOCKER\|MAJOR'` on the review output. ≥1 = NOT merge-ready.

Bulk-merge instructions ("merge what's green") require this per PR. CI green ≠ review-clean. mergeable=MERGEABLE ≠ review-clean.

---

## 10. Agent-agnostic translation table

| Concept here | Claude Code | Codex / Aider | Cursor | OpenClaw |
|---|---|---|---|---|
| Subagent | sub-task / spawn | new chat or `--task` | new composer thread | `sessions_spawn` |
| Skill | system-prompt snippet | system message | `.cursorrules` snippet | skill in `skills/` |
| Persona | role system prompt | role system prompt | persona prompt | persona in `pr-polish/personas/` |
| Worktree | identical (`git worktree`) | identical | identical | identical |
| PII preflight | shell tool + grep | shell + grep | terminal + grep | exec + grep |

The shape is the same. The buttons differ.
