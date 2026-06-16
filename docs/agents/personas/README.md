# Personas Index

A "persona" is a review-only system prompt with a strong viewpoint. The `pr-polish` skill spawns several of these in **parallel** as round-1 reviewers — each gives an independent verdict so the polish step doesn't get talked out of a finding by the implementer's own context.

These prompts are inspired by their namesakes; they are not those people. Use them as review lenses.

## Selection guide

`pr-polish` picks personas based on what the diff touches. Defaults are:

| Persona | When to fire |
|---|---|
| [orchestrator](orchestrator.md) | Always (parent agent context — included for orchestrators studying the pattern). |
| [carmack](carmack.md) | Performance-sensitive code; data structures, allocations, hot paths. |
| [torvalds](torvalds.md) | Any non-trivial diff — simplicity & complexity-budget review. |
| [dijkstra](dijkstra.md) | Correctness-critical changes; concurrency, invariants, edge cases. |
| [djb](djb.md) | Anything touching parsing, network input, crypto, untrusted data. |
| [feynman](feynman.md) | First-principles bug investigation; "what is actually happening?" |
| [house](house.md) | Diagnostic work — bug reports where the symptom may not be the cause. |
| [munger](munger.md) | Invert: what would make this fail catastrophically? |
| [taleb](taleb.md) | Risk surface, fat tails, "never failed before" claims. |
| [tufte](tufte.md) | Any UI / data-visualization change. |
| [doshi](doshi.md) | Specs, roadmaps, PR scope — strategy review. |
| [spec-refiner](spec-refiner.md) | Feature-intake; takes a raw idea to a locked, implementable spec. |
| [meshcore](meshcore.md) | Anything touching MeshCore protocol parsing / firmware-shape assumptions. |
| [mesh-operator](mesh-operator.md) | Operator-facing UX, deploy ergonomics, alerting, what an operator at 3 a.m. needs. |

## How `pr-polish` uses them

Round 1 (parallel, single tool-call block):

1. Adversarial reviewer (always)
2. Expert personas chosen by file types (typically 2-3)
3. Kent Beck TDD-history check (always)

Each returns a verdict (BLOCKER / MAJOR / MINOR / merge-ready) with line-cited findings.

Round 2 fires only if round 1 surfaced must-fixes; the parent verifies fixes by grepping `gh pr diff` first, and only re-spawns a persona if the grep can't confirm. Hard cap: 2 rounds; round 3 escalates to the human.

## Translation to other agent stacks

Each persona is plain markdown. Drop the file into your agent as a system-prompt addition, then ask it to review a specific diff or PR. The "fan-out in parallel" pattern is what matters — running them sequentially in one chain dilutes the independence that makes the review valuable.
