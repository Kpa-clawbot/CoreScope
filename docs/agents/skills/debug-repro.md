---
name: debug-repro
description: Reproduce bugs locally against fixture/staging before fixing. Ensures repro commands are concrete and tested.
triggers:
  - "reproduce bug"
  - "why is X broken"
  - "repro this"
  - "debug N"
  - "what's failing in N"
---

# debug-repro — Local Bug Reproduction

## Purpose
Reproduce reported bugs LOCALLY against fixture data, synthesized data, or live read-only staging before attempting any fix. Eliminates guess-and-check CI cycles.

## Scope
- Local reproduction using project fixtures (`test-fixtures/` in repo)
- SQLite queries against fixture/staging DBs
- curl against locally-running server
- Read-only SSH to staging (connection details in MEMORY.md)

## Process
1. Identify the EXACT failing assertion/behavior
2. Find the fixture/DB/state the test uses
3. Write the literal repro command (sqlite3/curl/node)
4. Run it locally — observe actual error
5. Only THEN form a hypothesis
6. Fix → re-run repro → confirm fixed → push

## Hard rules
- Every task brief for a debugging subagent MUST include the literal repro command
- Never push a fix without local repro confirming the fix works
- Read-only on staging — no writes without explicit user permission
- Reference connection details from MEMORY.md, not hardcoded here

## What NOT to do
- Push hypothesis to CI without local repro ("push-and-pray")
- Read code and guess without running anything
- Skip fixture identification ("just use the default DB")
- Claim "can't reproduce" without trying the exact input the failing test uses

## Output format
```
## Reproduction
- Command: `<exact command>`
- Result: <what happened>
- Root cause: <1 sentence>

## Fix
- File: <path>
- Change: <description>
- Verification: `<re-run command>` → <expected output>
```

## History
This skill exists because of the 6-PR map-markers cascade (PRs #956→#961). See LESSONS.md "Sequential guess-and-check via CI."
