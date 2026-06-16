# WORKFLOW — Agent-Driven CoreScope Contribution Pipeline

This is the loop we run for every change. Skip a step, you create rework.

## Pipeline at a glance

```
┌──────────┐   ┌────────┐   ┌────────────────────────┐   ┌──────────────┐   ┌───────┐
│ fix-issue├──▶│ CI     ├──▶│ pr-polish              ├──▶│ pr-merge-gate├──▶│ merge │
│ (impl)   │   │ watch  │   │ (parallel fan-out)     │   │ (3-axis)     │   │       │
└──────────┘   └────────┘   └────────────────────────┘   └──────────────┘   └───────┘
       ▲              ▲              ▲                          ▲
       │              │              │                          │
       │   each step is a SEPARATE subagent with fresh context  │
       │                                                        │
       └────── parent (orchestrator) owns the chain ────────────┘
```

Each box is a different sub-agent. **Do not** combine them ("implement + polish + merge in one shot"). Fresh context at each step is the whole point — it's how the polish step catches things the impl step rationalized away.

## Step 1 — Plan, then go

Before any tool call that mutates state:

1. Present a plan with milestones.
2. Wait for sign-off ("go", "ship it", "do it"). Acknowledgments ("good idea") are NOT permission.
3. Once sign-off lands, execute — don't re-summarize the plan. Batch language ("next wave", "do the rest") is execute-not-acknowledge.

Plans are for unfamiliar territory. For known/repeated work, just do it.

## Step 2 — Spawn `fix-issue` (one subagent per issue)

The implementation subagent gets a brief built from the [SUBAGENT-BRIEF-TEMPLATE](SUBAGENT-BRIEF-TEMPLATE.md). Mandatory sections: Mission, Setup, Hard rules, What NOT to do, Final reply format. For debugging tasks, include reproduction commands.

The worker:

- Creates a worktree: `git worktree add _wt-fix-<n> -b fix/<n> origin/master` (parallel work safe)
- Writes a **failing test first** (see [TDD.md](TDD.md))
- Commits that test (red) — CI must fail
- Writes the smallest production code to make it pass (green) — CI must pass
- Runs PII preflight on the staged diff (see below)
- Pushes the branch and opens a draft PR
- Returns: branch, commits (sha + subject), PR URL, validation evidence

**Worker discipline:**
- Workers do NOT spawn sub-chains. If they think they need to, they STOP and report up.
- One logical commit (or a small series). No mixing unrelated changes.
- `git add <file>` explicitly. Never `git add -A` or `git add .`.

## Step 3 — CI watch

If CI takes >5 min and you have other work, spawn a lightweight `ci-watcher` subagent: poll `gh pr checks <PR>` every 60s, report on flip. The parent then chains the next step automatically. Sitting on a green CI is a discipline failure.

## Step 4 — `pr-polish` (PARALLEL fan-out, NOT a chain)

The polish subagent rebases the PR on master, then fans out a **single tool-call block** that spawns the round-1 reviewers in parallel:

- An **adversarial** reviewer (looks for shipping-blockers in the diff)
- One or more **expert personas** (see [personas/](personas/)) chosen by the file types touched
- A **kent-beck** TDD-history check (validates red→green, no test-tampering)

Each reviewer returns a verdict: BLOCKER / MAJOR / MINOR / merge-ready, with line-cited findings.

**Hard caps:**
- 2 polish rounds per PR. If round 2 still has must-fixes, escalate to the human.
- Verify fixes by parent-grep on `gh pr diff` before re-spawning a reviewer. Re-running the same persona is the expensive last resort, not the default.

## Step 5 — `pr-merge-gate` (3-axis check)

Before declaring merge-ready or merging:

1. **git mergeable**: `gh pr view <N> --json mergeable,statusCheckRollup` shows `MERGEABLE`.
2. **CI green**: `gh pr checks <N>` shows all checks pass.
3. **Reviews resolved**: `gh pr view <N> --comments` shows zero unaddressed BLOCKER/MAJOR.

CI green ≠ review-clean. mergeable=MERGEABLE ≠ review-clean. All three axes, every time, in the same turn as the claim.

## Step 6 — Merge

Only the human merges (unless they've explicitly delegated it). "Fixes #X" / "Closes #X" auto-close the issue — only use those when ALL acceptance criteria are met. Partial fixes use "Partial fix for #X" and leave the issue open.

---

## TDD red→green (mandatory)

See [TDD.md](TDD.md). Summary:

1. Failing test commit (must fail on assertion, not build error). CI red.
2. Smallest production fix. CI green.
3. Optional refactor with tests still green.

Exemptions exist (pure refactor, config, net-new UI surface, pure docs) — each requires explicit justification in the PR body.

---

## PII Preflight (MANDATORY before every commit / `gh` write)

CoreScope is a **public** repo. Names, phone numbers, internal IPs, hostnames, API keys, and home-directory paths must never land in a commit, PR body, issue, or comment.

### The grep pattern

Customize this regex with your own PII patterns — names, handles, phone numbers, internal IPs, hostnames, key fragments, home directories — anything that should never leak to a public repo.

```bash
grep -nEi 'YOUR_NAME|YOUR_HANDLE|YOUR_PHONE|RFC1918_IPS|PROD_VM_IP|STAGING_VM_IP|/your/home/|api[_-]?key|YOUR_KEY_FRAGMENT' <file-or-piped-text>
```

### When to run it

- **Before every commit**: `git diff --cached | grep -nEi '...'`
- **Before every `gh pr create / edit`**: write the body to a tmp file, grep it, then `gh pr create -F tmpfile`. Never `--body` inline without grepping first.
- **Before every issue create / comment / review**: same — tmp file, grep, then send.

### What to do on a hit

Hits are a **HARD STOP**. Fix and re-grep. No exceptions for "small" edits or "just a comment."

If unsure whether something is PII, ASK. Do not ship.

---

## Force-push rules

- **Banned** on `master`, shared branches, and any branch about to be merged by someone else.
- **Allowed (preferred)** with `--force-with-lease` on your own bot-authored PRs in active rework.

## Config Documentation Rule

Any PR that adds or modifies a config field MUST:

1. Update `config.example.json` with the new field + default value.
2. Add or update the `_comment_` field explaining behavior.
3. If nested (e.g., per-source), show the new field in context.

Operators discover config via the example file. Skipping this blocks merge.

---

## Worktrees for parallel work

Standard recipe (always):

```bash
cd <repo-root>
git fetch origin
git worktree add _wt-<branch> -b <branch> origin/master
cd _wt-<branch>
```

Never work in the main checkout. Worktrees keep parallel subagents from stepping on each other.

When done:

```bash
cd <repo-root>
git worktree remove _wt-<branch>
git branch -D <branch>   # if local-only
```

---

## Subagent spawn discipline

**Parent (orchestrator):**
- Read [SUBAGENT-BRIEF-TEMPLATE.md](SUBAGENT-BRIEF-TEMPLATE.md) before every spawn. Briefs missing Mission / Setup / Hard rules / What NOT to do / Final reply format are a discipline failure.
- For pr-polish: round-1 reviewers in PARALLEL (same tool-call block).
- Implementation subagents get generous timeouts: 30 min minimum, 45 min for XL effort. Short timeouts cost more than long ones.
- Verify every state claim from a worker with your own tool call in the same turn. "Worker says it's done" is a hypothesis, not a fact.

**Worker:**
- Do NOT spawn sub-chains. If you think you need to, STOP and report up.
- Your final reply is your handoff. Include exact branch name, commit shas, PR URL, validation evidence, and any judgment calls you made.

---

## Mapping to your agent / stack

| Concept | Translation |
|---|---|
| OpenClaw `sessions_spawn` | Claude Code task / Codex sub-conversation / Aider's task split / your own fork-and-prompt |
| Skill `SKILL.md` | A markdown recipe you load into the agent's context for that task |
| Persona `.md` | A review-only system prompt you run as a fresh pass |
| `~/.openclaw/skills/...` | Wherever you keep your local recipes |
| `gh` CLI | Use it. Every agent stack supports `bash`. |

The pipeline shape (impl → CI → polish → gate → merge) is what matters. The tool wiring is local detail.
