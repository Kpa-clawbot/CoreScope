# Skills

Skills are task-specific playbooks an agent can load when it gets a particular kind of request. Each file here is self-contained — name, description/triggers, inputs, steps, output format, and failure modes.

These are written in a structure we use internally, but the **shape** is portable: you can paste any of these into a Claude Code / Codex / Cursor / Aider / OpenClaw system prompt or rule file and the agent will follow it.

## Index

### Intake & triage
- **[bug-intake](./bug-intake.md)** — diagnose a bug with expert personas; identify root causes, severity.
- **[feature-intake](./feature-intake.md)** — refine a vague feature request into a locked, implementable spec.
- **[triage-sweep](./triage-sweep.md)** — bulk-categorize open issues across multiple lanes.

### Implementation
- **[fix-issue](./fix-issue.md)** — end-to-end issue fix: implement, open PR, watch CI, auto-fix, hand off to polish.
- **[debug-repro](./debug-repro.md)** — reproduce bugs locally against fixture/staging before fixing.

### PR pipeline
- **[ci-watcher](./ci-watcher.md)** — lightweight watcher that polls CI checks and notifies on flip.
- **[pr-preflight](./pr-preflight.md)** — pre-submission gate of fast fail-fast greps (run BEFORE `gh pr create`).
- **[pr-polish](./pr-polish.md)** — adversarial + expert + Kent-Beck review fan-out in parallel.
- **[pr-merge-gate](./pr-merge-gate.md)** — three-axis pre-merge check (mergeable + CI + reviews).

### Release & ops
- **[corescope-release](./corescope-release.md)** — release tag + deploy + verification flow.
- **[devops-fix](./devops-fix.md)** — live operational fixes on staging/prod (SSH, docker, sqlite, log triage).
- **[qa-suite](./qa-suite.md)** — full QA sweep against a running instance.

### Language-specific quality gates
- **[go-style-enforcer](./go-style-enforcer.md)** — Google Go style guide enforcement on Go diffs.

### Meta
- **[subagent-brief-template](./subagent-brief-template.md)** — the template every subagent brief must follow.

## Adapting these to your agent

- The skills assume a `read`/`edit`/`exec` style tool surface and a `gh` CLI. Substitute whatever your harness uses.
- Trigger phrases are suggestions; map them to your harness's slash-command or rule-matching system.
- Paths inside the skills use placeholders like `<workspace>`, `<repo>`, `<home>` — replace with your actual layout.
