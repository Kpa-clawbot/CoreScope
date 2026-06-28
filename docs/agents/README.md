# CoreScope — Agent Contributor Onboarding

If you're a contributor to CoreScope using an AI coding agent (Claude Code, Codex, Cursor, Aider, OpenClaw, Continue, etc.), this directory is your onboarding pack.

The maintainers run their own agent-driven workflow against this repo. The docs here describe the **process**, **discipline**, and **reusable building blocks** (skills + personas) that make that workflow merge-clean. Most of it is agent-agnostic — TDD, PR polish, persona review, PII preflight, subagent briefs — and translates cleanly to whatever harness you use.

## Read in this order

1. **[WORKFLOW.md](./WORKFLOW.md)** — the end-to-end pipeline (fix-issue → CI watch → pr-polish → merge-gate → merge), planning rules, PII preflight, force-push rules, worktrees.
2. **[RULES.md](./RULES.md)** — 36 hard-won rules. Skim once, re-read when something feels off.
3. **[TDD.md](./TDD.md)** — red→green is mandatory. Exemptions listed.
4. **[SUBAGENT-BRIEF-TEMPLATE.md](./SUBAGENT-BRIEF-TEMPLATE.md)** — required template before spawning any subagent.
5. **[skills/](./skills/)** — task-specific playbooks (e.g., `fix-issue`, `pr-polish`, `pr-preflight`, `corescope-release`).
6. **[personas/](./personas/)** — adversarial / expert review voices used in pr-polish fan-out.

## How to use this with your own agent

- Point your agent at this directory. Tell it: "Treat `WORKFLOW.md` + `RULES.md` + `TDD.md` as standing instructions for this repo. Use the skills as task playbooks. Use the personas as parallel review voices on PRs."
- The skills are written in a tool-call style we use internally, but the **structure** (Inputs / Steps / Output / Failure modes) is portable. Adapt the tool invocations to your harness.
- The personas are role prompts — drop them in as system messages when you want a specific kind of review (Carmack for perf, Dijkstra for correctness, Torvalds for taste, etc.).

## Non-goals

- This is not a tutorial on CoreScope internals. For that see the repo `README.md` and `AGENTS.md`.
- This is not a harness-specific guide. We document concepts, not key bindings.
