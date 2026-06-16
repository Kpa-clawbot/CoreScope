# Agent-Driven Contribution Onboarding

If you're a contributor working on CoreScope with an AI coding agent (Claude Code, Codex, Cursor, Aider, OpenClaw, or anything else that can run a shell and call tools), this directory is your onboarding pack.

The repo's existing `AGENTS.md` at the project root is the short-form worker rules file your agent will auto-load. Everything in here expands on that — the workflow, the hard-won rules, the skill recipes, and the review personas we run on every PR.

## Read in this order

1. **[WORKFLOW.md](WORKFLOW.md)** — the end-to-end pipeline (fix-issue → CI watch → pr-polish → merge-gate → merge), TDD red→green, planning, PII preflight, worktrees, subagent discipline.
2. **[RULES.md](RULES.md)** — 35 numbered rules. Hard-won lessons; sanitized.
3. **[TDD.md](TDD.md)** — the TDD-is-mandatory cycle, exemptions, what blocks merge.
4. **[SUBAGENT-BRIEF-TEMPLATE.md](SUBAGENT-BRIEF-TEMPLATE.md)** — the standard task-brief template for any sub-agent spawn.
5. **[skills/README.md](skills/README.md)** — index of every skill (specialized recipe). Browse on demand.
6. **[personas/README.md](personas/README.md)** — index of review personas used by the `pr-polish` parallel fan-out.

## Agent-agnostic translation

These docs were written inside an OpenClaw-based workflow, so commands like `sessions_spawn`, "skills", and tool names sometimes leak through. Translate as needed:

| Concept here | Your agent |
|---|---|
| "skill" / `SKILL.md` | a markdown recipe / system-prompt addition / slash-command |
| "subagent" / `sessions_spawn` | sub-task, fork, sub-conversation, child agent run |
| "persona" | a review system prompt run as a separate pass |
| "worktree" | `git worktree` (universal — use it) |
| "PII preflight" | a pre-commit grep — every agent that runs `git` can do this |

The workflow concepts (TDD red→green, parallel adversarial review, plan-then-go, PII preflight, subagent brief discipline) are tool-agnostic. The OpenClaw-specific tool names are not load-bearing — pick equivalent invocations on your stack.

## What you do NOT need to recreate

- Operator-private files (memory, identity, soul docs, runtime config)
- Specific cron jobs / heartbeats
- Internal hostnames, IPs, API keys

If you spot any of those leaking into a contribution, that's a PII preflight bug — open an issue.
