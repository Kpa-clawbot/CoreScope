---
name: subagent-brief-template
description: Standard template for subagent task briefs. Required reading before ANY sessions_spawn call.
triggers:
  - "spawn subagent"
  - "before sessions_spawn"
  - "task brief for"
---

# subagent-brief-template — Standard Task Brief Structure

## Purpose
Ensures every subagent spawn has a complete, well-structured brief that prevents common failure modes. Read this before EVERY `sessions_spawn` call.

## Required sections (in order)

```markdown
## Mission
<One paragraph: what you're doing + why it matters>

## Setup
1. AGENTS.md is auto-loaded (worker rules)
2. Read skill: `~/.openclaw/skills/<relevant>/SKILL.md`
3. Read persona (if applicable): `<workspace>/personas/<name>.md`
4. Read task-specific files: <list paths>

## Skills available to you
- <skill-name>: `~/.openclaw/skills/<name>/SKILL.md` — <when to use>
- ...

## Task
<The actual work, with specific details>

## Hard rules
- <task-specific constraints>
- DO NOT spawn sub-chains. Parent owns chain dispatch.
- PII Preflight applies to all public-repo writes.

## What NOT to do
- <common failure mode 1>
- <common failure mode 2>
- <common failure mode 3>

## Final reply format
<Exactly what you want back — structure it>
```

## Example briefs

### Implementation task
```markdown
## Mission
Fix issue #999 — map markers disappear when observer goes inactive. Users see empty maps.

## Setup
1. AGENTS.md auto-loaded
2. Read: `~/.openclaw/skills/fix-issue/SKILL.md`
3. Read: `<workspace>/<repo>/AGENTS.md`
4. Worktree: `<workspace>/<repo>/_wt-fix-999`

## Skills available to you
- fix-issue: `~/.openclaw/skills/fix-issue/SKILL.md`
- debug-repro: `~/.openclaw/skills/debug-repro/SKILL.md`

## Task
1. Reproduce locally: `sqlite3 test-fixtures/e2e-fixture.db "SELECT COUNT(*) FROM observers WHERE inactive = 0"`
2. Identify why inactive observers are excluded from map query
3. Fix the SQL query in cmd/server/handlers.go
4. Test: start server with fixture DB, curl /api/nodes, verify markers present
5. Open PR

## Hard rules
- DO NOT spawn sub-chains.
- Reproduce before fixing (rule 19).
- One commit per logical change.

## What NOT to do
- Push to CI without local repro
- Modify the test fixture to make tests pass
- Open multiple PRs for the same fix

## Final reply format
- PR number + URL
- Repro command + before/after output
- Files changed (list)
```

### Review task
```markdown
## Mission
Polish PR #888 — rebase, self-review, adversarial review, get merge-ready.

## Setup
1. AGENTS.md auto-loaded
2. Read: `~/.openclaw/skills/pr-polish/SKILL.md`
3. Personas at: `<workspace>/personas/`

## Skills available to you
- pr-polish: `~/.openclaw/skills/pr-polish/SKILL.md`

## Task
Run the full pr-polish chain on PR #888. Do NOT skip any step.

## Hard rules
- DO NOT spawn sub-chains.
- Force-with-lease allowed on this branch.
- Full chain required: self-review → adversarial → expert → final.

## What NOT to do
- Skip the adversarial review step
- Claim merge-ready without all 3 checks passing
- Force-push master

## Final reply format
- Review file path
- BLOCKER/MAJOR count
- Merge-ready verdict (yes/no + reasons)
- Comment URL if posted
```

### Triage task
```markdown
## Mission
Triage 10 open issues in the backlog — categorize, prioritize, identify quick wins.

## Setup
1. AGENTS.md auto-loaded
2. Read: `~/.openclaw/skills/triage-sweep/SKILL.md`

## Skills available to you
- triage-sweep: `~/.openclaw/skills/triage-sweep/SKILL.md`
- bug-intake: `~/.openclaw/skills/bug-intake/SKILL.md`

## Task
Process issues #100-#110. For each: read, categorize (bug/feature/question), assess severity, tag.

## Hard rules
- DO NOT spawn sub-chains.
- DO NOT close issues without explicit user approval.
- Read the full issue + comments before categorizing.

## What NOT to do
- Categorize from title alone
- Close issues
- Start implementing fixes (just triage)

## Final reply format
| Issue | Category | Severity | Quick win? | Notes |
|---|---|---|---|---|
```

## What NOT to do (meta)
- Spawn without reading this template first
- Omit "What NOT to do" section (it prevents the most common failures)
- Use generic briefs ("fix this issue") without specific commands/paths
- Forget `runTimeoutSeconds` on the spawn call
