# Personas

Personas are role prompts used in **parallel review fan-out** during pr-polish. Each one has a distinct voice and bias — perf, correctness, taste, simplicity, statistics, mesh-networking expertise, etc.

The pattern: when polishing a PR, spawn **adversarial + 1–2 expert personas + Kent Beck** simultaneously in one tool-call block. Each persona reviews the same diff independently, returns findings tagged BLOCKER / MAJOR / MINOR / NIT. Findings are deduped and addressed in a single follow-up commit.

Sequential persona chains are deprecated — they balloon context and serialize wall-time for no quality gain.

## Roster

### Adversarial / taste
- **[torvalds](./torvalds.md)** — taste, naming, structure; ruthless on bad abstractions.
- **[house](./house.md)** — diagnostic skeptic; "everybody lies"; hunts for the symptom the PR is *not* explaining.
- **[djb](./djb.md)** — minimalism, correctness, no surprises; security-paranoid.

### Engineering experts
- **[carmack](./carmack.md)** — performance, data layout, simplicity over cleverness.
- **[dijkstra](./dijkstra.md)** — correctness, invariants, proof-style reasoning.
- **[feynman](./feynman.md)** — first-principles explanation; "if you can't explain it simply, you don't understand it."

### Domain experts
- **[meshcore](./meshcore.md)** — MeshCore protocol expert; packet types, channel hashes, observer semantics.
- **[mesh-operator](./mesh-operator.md)** — operator perspective; what breaks at 3am, what config knobs are missing.

### Analysis / statistics / risk
- **[tufte](./tufte.md)** — data visualization; charts that mislead, ink/data ratio.
- **[taleb](./taleb.md)** — fragility, fat tails, hidden risk; "what happens at 10×?"
- **[munger](./munger.md)** — mental models, invert-always-invert, second-order effects.

### Process / spec
- **[orchestrator](./orchestrator.md)** — pipeline discipline; verifies handoffs, three-axis merge readiness.
- **[spec-refiner](./spec-refiner.md)** — turns vague asks into precise acceptance criteria.
- **[doshi](./doshi.md)** — UX / product discipline; user journey, edge cases users hit.

### Tests
The Kent-Beck persona lives inside the `pr-polish` skill rather than as a standalone file because it's tightly coupled to the TDD red→green verification (see `../TDD.md`). It always runs in the polish fan-out.

## Picking personas for a PR

| PR shape | Personas to fan out |
|---|---|
| Backend perf / data-path change | carmack + dijkstra + (kent-beck) |
| Protocol / packet parsing | meshcore + djb + (kent-beck) |
| Frontend / UI / chart | tufte + doshi + (kent-beck) |
| Ops / staging / deploy | mesh-operator + taleb + (kent-beck) |
| Refactor / structure | torvalds + dijkstra + (kent-beck) |
| Spec / requirements unclear | spec-refiner + house + (kent-beck) |
| Risk / failure-mode analysis | taleb + munger + house |

Always include an adversarial voice (torvalds/house/djb) and the Kent-Beck TDD check.
