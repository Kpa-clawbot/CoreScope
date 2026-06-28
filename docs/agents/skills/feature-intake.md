---
name: feature-intake
description: "Refine a feature request — from vague idea to locked, implementable spec with design decisions and crisp milestones. Handles ambiguity: asks clarifying questions, surfaces hidden assumptions, drives to concrete deliverables. Use when a feature is requested, an enhancement is proposed, or someone has a rough idea. Triggers: 'spec out 770', 'feature intake', 'design feature', 'spec this', 'refine this idea'. Takes a feature description, issue number, or user feedback. NOT for: bugs (use bug-intake), existing PRs (use pr-polish), or implementation (use fix-issue)."
---

# Feature Request Intake

Refines raw, vague, or half-formed feature requests into locked specs ready for implementation. Handles ambiguity — if the request is unclear, the skill drives clarification before speccing.

## Input

- **Feature description** — anything from "the graph should be better" to a detailed proposal
- **Repo** (optional) — `owner/repo` for codebase context
- **Expert overrides** (optional) — "spec with tufte", "review with operator"

## Handling Vague Requests

Most feature requests are vague. That's normal. The skill's first job is to **extract what the user actually needs** before writing any spec.

### Clarification Pattern
When the request is vague (e.g., "make the map better"):
1. **Invoke the project-specific domain expert** — they know the codebase, the users, and the domain. Have them generate 3-5 targeted questions that surface the real need.
2. **Present questions as a numbered list** to the owner — not essay questions, yes/no or short-answer.
3. **From the answers, identify the concrete features** hiding inside the vague request.
4. THEN proceed to spec.

Example:
```
User: "the neighbor graph needs work"

Domain expert questions:
1. Is it the visual layout, the data accuracy, or the interactivity?
2. What do you use the graph for today — what question are you trying to answer?
3. What's the most frustrating thing about it right now?
4. How many nodes does your mesh have? (performance context)
5. Do you need historical graph data or just current state?

Owner answers: "1. interactivity 2. finding bottlenecks 3. can't click anything 
               4. about 500 5. current state is fine for now"

→ Now we know: click-to-inspect, bottleneck detection, 500-node target. Spec THAT.
```

## Process

### Phase 1: Understand the Need
If the request is clear → skip to Phase 2.
If vague → invoke domain expert for clarifying questions → get answers → proceed.

### Phase 2: Initial Spec
Write the first-pass spec:
- Problem statement (what the user actually needs, not what they asked for)
- Proposed solution with milestones
- What NOT to build (YAGNI cuts)
- Data/API changes needed (if applicable)

### Phase 3: Expert Review
Select experts based on what the feature touches. The skill itself is **project-agnostic** — it selects from whatever personas are available:

| Feature touches... | Look for persona with... |
|-------------------|------------------------|
| UI/visualization | Data design, information hierarchy expertise |
| Protocol/domain logic | Project-specific domain knowledge |
| Performance | Data flow, rendering, hot path expertise |
| Security | Attack surface, trust model expertise |
| User workflow | Operator/user field experience |
| Architecture | Failure modes, inversion thinking |

**Always include:**
1. A **user/operator perspective** — features without user validation are vanity projects
2. A **codebase audit** — specs written without checking existing code waste time

The expert reviews the spec and raises questions/concerns. **Report back to the owner** — don't post to the issue until decisions are made.

### Phase 4: Owner Decisions
Present questions as a **numbered list** — one sentence each, answerable quickly:
```
1. Move feature X to milestone 2?
2. Cut feature Y entirely?
3. Default value: A or B?
```

Owner answers. **Immediately** lock decisions:
```markdown
## Design Decisions (Locked)
1. Feature X → M2. User says it's the primary use case.
2. Feature Y cut. Over-engineered for the actual workflow.
3. Default B + UI control with persistence.
```

### Phase 5: Codebase Reconciliation
If the project has a codebase, audit the spec against reality:
- What infrastructure already exists?
- What data is available? What's missing?
- What are the blockers per milestone?

Produce a **blocker table**:
```
| Feature | Exists? | Blocker? |
|---------|---------|----------|
| User data | In API ✅ | No |
| History | No table | Yes — backend work |
```

### Phase 6: Architecture Decisions
For complex features, present architecture questions as another numbered list.
Lock as architecture decisions.

### Phase 7: Final Spec
The issue now has:
- ✅ Locked milestones (ordered by user value, each ships independently)
- ✅ Design decisions (with rejected alternatives)
- ✅ Architecture decisions (with codebase evidence)
- ✅ Blocker table (if applicable)
- ✅ Scope cuts (what was explicitly NOT built)

**Only now is implementation allowed.**

## Expert Selection

The skill does NOT hardcode which personas to use. It selects based on:
1. What the feature touches (UI → visualization expert, security → security expert)
2. What personas are available in the project's persona directory
3. The owner can override with specific experts

**Every intake MUST include:**
- At least one **domain/user expert** (someone who represents the end user)
- At least one **codebase check** (someone who knows what already exists)

## Rules

- **Drive out ambiguity first.** A spec for the wrong thing is worse than no spec.
- **Never skip the user perspective.** Engineers build what's clever. Users use what's useful.
- **Never skip the codebase check.** Specs that ignore existing code produce surprised engineers.
- **Number every question.** Decision-makers are busy — make it easy.
- **Lock decisions immediately.** Open questions that linger get re-debated every session.
- **Milestones ship independently.** If M3 requires M2 requires M1, that's one milestone.
- **Record what was cut.** Prevents future sessions from re-proposing rejected ideas.
- **The skill is project-agnostic.** Domain knowledge comes from personas, not from this skill.


## Brevity (with clarity)

Default to short. Long is the exception, justified by content. See [`personas/orchestrator.md`](../personas/orchestrator.md#brevity-with-clarity) for the canonical rules.

**Limits for artifacts this skill produces**
- PR descriptions: ≤ 250 words. One paragraph "what + why," then bullets.
- PR / issue comments: ≤ 100 words. One concern per comment.
- Issue bodies: ≤ 300 words. Problem, evidence, proposed action.
- Chat replies to the human: ≤ 6 lines unless asked for detail.

**Always**
- Lead with the answer; supporting detail follows only if asked.
- Tables for ≥ 3 items with shared shape — never repeat a label across bullets.
- Drop hedges, throat-clearing, re-narration ("Let me…", "First, I…").

**Never**
- Restate the question before answering.
- Marketing voice ("powerful," "comprehensive," "seamlessly").
- Multi-section summaries when one paragraph suffices.

If a reply exceeds the limit, the first line must explain why ("Long because: 14 commits to summarize"). Otherwise trim.
