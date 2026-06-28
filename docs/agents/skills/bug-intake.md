---
name: bug-intake
description: "Diagnose a bug using expert personas. Analyzes symptoms, identifies root causes, assesses severity. Use when a bug is reported or something isn't working. Triggers: 'diagnose bug 570', 'triage 570', 'investigate 570', 'bug intake 570', 'what's causing this', 'why is X broken'. All trigger words do the same thing. Takes a bug description, issue number, or error output. NOT for: known fixes (just fix it), feature requests, or code review (use pr-polish)."
---

# Bug Intake

Expert-persona-driven bug diagnosis and triage. Turns vague bug reports into actionable root cause analysis.

## Input

- **Bug description** — user's description of the problem, error output, logs, screenshots, or a GitHub issue number
- **Repo** (optional) — `owner/repo` format for context
- **Expert override** (optional) — "diagnose with house", "investigate with feynman"

## Persona Directory

Expert personas live in the pr-polish skill's personas directory:
`~/.openclaw/skills/pr-polish/personas/`

Bug intake primarily uses:
- `house.md` — Dr. House: differential diagnosis, symptom vs cause, "everybody lies"
- `feynman.md` — Richard Feynman: first principles, minimal reproduction, evidence-based

Also available for severity/impact assessment:
- `munger.md` — Charlie Munger: failure modes, second-order effects
- `taleb.md` — Nassim Taleb: fat tails, cascading failures, blast radius

## Expert Selection

If the user specifies an expert, use that one.

Otherwise, auto-select based on the bug:

| Bug characteristics | Expert | Why |
|---|---|---|
| Vague report, unclear symptoms, "it doesn't work" | **house** | Needs differential diagnosis |
| Conflicting evidence, multiple theories, recurring bug | **feynman** | Needs first-principles investigation |
| "How bad is this?" severity/impact assessment | **munger** | Inversion, second-order effects |
| Intermittent failures, load-dependent, timing issues | **taleb** | Fat tails, cascading failures |

Default: **house** first (diagnose), then **feynman** if House's diagnosis needs verification.

## Process

### Step 1: Gather Context

Before spawning an expert, collect what's available:
- Bug report text / issue body
- Error logs or output
- Recent changes (commits, deploys)
- Environment info (prod vs staging, data size, load)
- Any prior debugging attempts

If the user gave an issue number:
```bash
gh issue view <NUMBER> --repo <REPO> --comments
```

### Step 2: Expert Diagnosis

Spawn a subagent with the expert persona. **Read the persona file first** and inject its contents.

Label: `bug-<NUMBER>-diagnosis-<expert>`

```
First: read AGENTS.md in the repo root if it exists and follow all rules.

<INJECT FULL PERSONA FILE CONTENTS HERE>

---

Diagnose this bug in <REPO>.

## Bug Report
<INSERT BUG DESCRIPTION, LOGS, ERROR OUTPUT HERE>

## Context
<INSERT ANY CONTEXT: recent deploys, environment, data size, prior debugging>

## Your Task
Apply your diagnostic process (from your persona) systematically:
1. Analyze the symptoms as reported
2. Question assumptions — what's missing, what's assumed, what's unverified?
3. Generate a differential diagnosis — list ALL plausible root causes (minimum 3)
4. For each candidate cause: what evidence supports it? What evidence contradicts it? What test would confirm or eliminate it?
5. Rank by likelihood and provide your verdict
6. Recommend specific next steps (code to check, logs to examine, tests to run)

## Access
You have full access to the codebase. Use it:
- `git log --oneline -20` — recent changes
- Read relevant source files
- Search for patterns: `grep -rn "pattern" <dir>`
- Check git blame for suspect code

Post your diagnosis as a comment on the GitHub issue if one exists.
Be thorough. Be skeptical. Don't guess — investigate.
```

### Step 3: Verification (optional, if diagnosis is uncertain)

If House's diagnosis has low confidence or multiple equally likely causes, spawn Feynman to verify:

Label: `bug-<NUMBER>-verify-feynman`

Give Feynman the same bug report PLUS House's diagnosis. Ask Feynman to:
- Challenge House's conclusions
- Propose the minimal test that distinguishes between the remaining hypotheses
- Either confirm or redirect the investigation

### Step 4: Severity Assessment (optional, for high-impact bugs)

If the bug looks serious, spawn Munger or Taleb for impact analysis:

Label: `bug-<NUMBER>-severity-<expert>`

Ask them:
- What's the blast radius?
- What else could be affected that we haven't checked?
- Is this a one-off or a systemic issue?
- What's the worst case if we don't fix it immediately?

### Step 5: Report

Compile the expert findings into a triage report:

```
## Bug Triage: <title>

**Diagnosed by:** <expert persona>
**Classification:** Bug / Feature Request / Enhancement / Won't Fix
**Confidence:** High/Medium/Low
**Severity:** Critical/Major/Minor/Cosmetic (only if Classification = Bug)

### Root Cause (if bug)
<one paragraph explanation>

### Assessment (if not a bug)
<why this isn't a bug — what behavior is correct and what the reporter actually wants>

### Evidence
<what confirms this diagnosis/classification>

### Impact
<who/what is affected, blast radius>

### Recommended Action
- **Bug**: specific code changes or investigation steps
- **Feature Request**: relabel issue, outline what the feature would look like
- **Enhancement**: relabel issue, scope the improvement
- **Won't Fix**: explain why, close with comment

### Open Questions
<anything still uncertain>
```

The expert should also relabel the GitHub issue:
- Bug → keep `bug` label
- Feature Request → remove `bug`, add `type:feature`
- Enhancement → remove `bug`, add `type:chore` or `type:feature`
- Won't Fix → comment and close

Post this on the GitHub issue and report to the user.

## Key Rules

- **Never guess** — if you don't have enough info, say so and ask for more
- **Symptoms ≠ causes** — always dig deeper than what's reported
- **Read the code** — experts have full codebase access, use it
- **One expert at a time** — don't stack diagnoses, they'll confuse each other
- **Don't fix in this skill** — diagnose and triage only. Fixing is a separate step (use fix-issue skill)
- **Always label subagents** with descriptive names


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
