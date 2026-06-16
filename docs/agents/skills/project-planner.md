---
name: project-planner
description: Help <contributor> design and spec her project portfolio management and budgeting tool. Use when she mentions "project planner", "the tool", "power apps project", "budget tool", or wants to work on the Visa project planning tool. Triggers on phrases like "let's work on the project", "project planner", "the budget tool", "power apps", "spec the tool".
---

# Project Planner Skill

Help <contributor> design a project portfolio management and budgeting tool for her Finance team at Visa.

## Context
- **Repo:** github.com/Kpa-clawbot/petia-project-planner (private)
- **Local clone:** /tmp/petia-project-planner
- **Personas:** `personas/spec-refiner.md` and `personas/doshi.md` in the repo

## The Tool
Users submit projects with background, risks, and financial info. Finance reviews and decides funding (full/partial/none). Reports export to Excel.

### Access Roles
- **User** — sees only their own projects
- **Manager** — sees their team's projects
- **Finance** — sees all projects, approves/declines funding

## Constraints

### Platform
- **Target platform:** Microsoft Power Apps (SharePoint backend likely)
- All designs and prototypes MUST respect Power Apps capabilities and limitations
- Do NOT design features that require technologies unavailable in Power Apps (3D libraries, custom JS frameworks, etc.)
- Research Power Apps limitations BEFORE designing any feature

### Data Security
- **ZERO Visa proprietary data.** No real financial figures, project names, employee names, or internal processes.
- Use only dummy/fictional data in all specs, mockups, and prototypes
- If <contributor> shares anything that looks like real Visa data, **immediately flag it** and ask her to substitute with dummy data
- Specs should describe data structures generically (e.g., "project budget field" not "FY26 Q3 marketing allocation")

### No Real Visa Data Checklist (run mentally before every commit)
- No real project names or codes
- No real employee names or org structure
- No real financial figures
- No real Visa processes or internal tool names

## Workflow

### Feature Request Process
1. Any new feature request → create a **GitHub issue** on the repo
2. Run the issue through **Doshi persona** — is this worth building? L/N/O classification?
3. Run through **Spec Refiner persona** — is the spec tight? Numbered decisions needed?
4. Run through **DJB persona** — any security concerns? Data exposure risks? Access control gaps?
5. Present to **<contributor> for final sign-off** — she is the decision maker, not the personas
5. Record all decisions in a numbered **ADR file** in `decisions/` (e.g., `002-role-model.md`)
6. Write the final spec as an `.md` file in `specs/`
7. Update the original GitHub issue with the final spec and link to the ADR
8. Commit and push after every significant update

### Design & Prototyping
- Screen designs and data models go in `design/`
- All mockups/prototypes must be buildable in Power Apps
- When unsure if Power Apps supports something, research first, document the finding
- Clickable prototypes can be hosted on **GitHub Pages** (github.io) for <contributor> to review
- **Prototype URLs MUST include a hard-to-guess random component** (e.g., `/petia-project-planner/preview-a7f3x9k2/`) to keep them private
- Never use predictable paths like `/demo/` or `/preview/`

### Code & Build Process
- All code changes go on a **feature branch**, never direct to main
- Open a **PR** against main for review
- Run PR through persona review (Spec Refiner, Tufte, DJB as applicable) **BEFORE asking for human approval**
- **<contributor> approves** code PRs (<contributor> can override if <contributor> unavailable)
- Merge to main only after approval
- No YOLO pushes to main
- **NO SHORTCUTS** — do not skip expert review even if it feels like it'll be fine
- When <contributor> says "let's build it at Visa," generate a complete handoff document:
  - Final specs (all `.md` files from `specs/`)
  - All ADRs
  - Data model
  - Screen descriptions
  - Step-by-step instructions for Copilot/Codex to rebuild inside Visa's Power Apps environment

## Key Rules
- **<contributor> is the product owner** — all final decisions are hers
- **Personas advise, <contributor> decides**
- **Every decision gets an ADR** — no undocumented decisions
- **Every feature gets an issue** — no skipping the process
- **Push to repo after every update** — the repo is the source of truth
- **When in doubt about data sensitivity, ask <contributor>**
