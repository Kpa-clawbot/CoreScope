---
name: softball-scout
description: Evaluate a softball player's recruiting viability against real NCSA/college benchmarks by position and division level. Use when asked to assess a player's stats, project their college level, identify gaps, or build a development plan. Takes stats (batting, fielding, measurables) and outputs a scouting report with division-level projections and prioritized development areas. Triggers on "scout report", "evaluate player", "what level can she play", "recruiting assessment", "where does she project", "scouting report". NOT for: swing mechanics analysis (use video), recruiting outreach/emails, or schedule management.
---

# softball-scout

Evaluate a softball player against real college recruiting benchmarks. Output honest, evidence-based scouting reports.

## When to use
- Player stats are available and someone asks "what level can she play?"
- Building or updating a recruiting profile
- Comparing a player's trajectory to college benchmarks
- Identifying development priorities

## Required inputs
At minimum: position, batting average, and 2-3 other stats. The more data, the better the report. Flag what's missing.

## Process

### 1. Load benchmarks
Read `references/ncsa-benchmarks.md` for position-specific standards by division.

### 2. Map player stats to benchmarks
For each available stat, determine which division level the player meets:
- ✅ D1 — meets or exceeds D1 standard
- ✅ D2 — meets D2 but not D1
- ✅ D3 — meets D3/NAIA but not D2
- ❌ Below D3 — doesn't meet minimum college standard
- ❓ Unknown — stat not available

### 3. Context-adjust the raw numbers
- **Competition level matters.** .300 against elite 16U > .400 against weak 12U. State the competition context.
- **Age matters.** A 14U player doesn't need to hit D1 numbers today. Project trajectory.
- **Stats ≠ tools.** Batting average doesn't measure bat speed. Fielding % doesn't measure range. Call out what stats CAN'T tell you.
- **BABIP regression.** If BABIP > .350, flag that batting average may regress. If BABIP < .280, flag that it may improve.
- **Fielding % for outfielders is misleading.** High-range outfielders accumulate more errors because they reach more balls. A low FPCT + high putouts/game may indicate great range, not bad defense. Always note this caveat for OF.

### 4. Identify gaps (be blunt)
- What benchmarks does the player NOT meet?
- What measurables are missing entirely?
- What would a skeptical college coach question?

### 5. Build development priorities (ranked)
Order by: impact on recruiting viability × feasibility of improvement. Don't list 10 things — pick the top 3-5 that matter most.

### 6. Project realistic college targets
- Be specific: name division levels, types of programs (academic vs. athletic powerhouse), and conference tiers.
- The 4.0 GPA / high-academic angle is a REAL recruiting lever — Ivy and high-academic D1/D3 have lower athletic thresholds. Factor this in.
- Don't say "D1" unless the numbers actually support it. False hope wastes time and money.

## Output format

**[PLAYER NAME] — Scouting Report**
**Position | Class | Team | Season**

**Stat Line:** (one-line slash line + key counting stats)

**Division Projection Table:** (stat → D1/D2/D3/below/unknown for each metric)

**What's Genuinely Good:** (2-4 strengths with evidence)

**What Needs Work:** (2-4 gaps with evidence)

**Missing Data:** (what we can't evaluate without measurables or video)

**Development Priorities:** (ranked 1-5)

**Realistic College Targets:** (division levels + types of programs)

## Hard rules
- Never inflate. If a number is average, say average.
- Always state what you're comparing to and where the benchmark comes from.
- If competition level is weak or mixed, discount the stats and say so.
- Distinguish between "is" (current level) and "could be" (projection with development).
- If you don't have enough data, say so instead of guessing.
- GPA is a recruiting tool. Always factor it into the target list.
