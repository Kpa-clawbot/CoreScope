---
name: qa-suite
description: Run a structured QA test plan against a deployed app (staging, prod, or a PR build) before merging a PR or tagging a release. Triggers on phrases like "qa staging", "qa pr 806", "qa <tag>", "run the test plan", "test the release candidate", "qa the changes since <tag>". Spawns a qa-engineer subagent that walks the plan in `plans/<id>.md`, runs the bundled scripts where possible (API contract diff, heap snapshot, frontend smoke), drives the browser for visual checks, and reports findings as a comment on the target PR or as a fresh GitHub issue. NOT for: writing new test plans (do that manually in plans/), running CI (CI runs in GitHub Actions), or load testing.
---

# qa-suite

Run a project test plan and report results as a structured GitHub issue or PR comment.

## Inputs
- **Target** (required): one of `pr <NUMBER>`, `tag <NAME>`, `staging`, `prod`
- **Plan** (optional): plan id from `plans/`. Default selection rules:
  - `pr <NUMBER>` → if `plans/pr-<NUMBER>.md` exists use it, else use latest `plans/v*-rc.md`
  - `tag <NAME>` → `plans/<NAME>.md` (must exist)
  - `staging` / `prod` → latest `plans/v*-rc.md`

## Workflow
1. **Locate plans + scripts.** Check (in order):
   - `<repo>/qa/plans/` and `<repo>/qa/scripts/` (preferred — project-specific assets in-repo)
   - `plans/` and `scripts/` next to this SKILL.md (only the example template lives here)
2. Load the selected plan.
3. Resolve env URLs from project config (the parent agent reads them — do NOT hardcode here):
   - prod: `${PROD_URL}`
   - staging: `${STAGING_URL}`
   - pr / tag: prompt the user for which env the build is deployed to
4. Spawn ONE qa-engineer subagent (see `personas/qa-engineer.md`) with the plan path + env wired in. Ask it to:
   - Run any `auto:` scripts named in the plan (resolved from `<repo>/qa/scripts/` if present, else `scripts/` next to the skill)
   - Drive the `browser` tool for the frontend sections
   - Skip steps marked `mode=human` and list them in the final report as needs-human
5. The subagent posts a structured results comment:
   - On `pr <NUMBER>` target → comment on that PR
   - On `tag <NAME>` / `staging` / `prod` → file a fresh issue titled `QA: <plan-id> on <env>`

## Bundled scripts (examples — adapt per project)
- `scripts/api-contract-diff.sh` — example template for diffing JSON shape between two deployments and asserting required fields are present. The endpoint list at the top of the script is project-specific; copy and edit for your API. The 4-way error classification (`curl-failed` / `parse-empty` / `shape-diff` / required-field missing) is the reusable pattern.
- `scripts/pprof-snapshot.sh` — fetches `/debug/pprof/heap` from a Go service, optionally over SSH, summarizes top-15 inuse_space symbols. Reusable as-is for any Go service exposing pprof.
- `scripts/frontend-smoke.md` — guidance for the `browser` tool (which page → which assertion).

If your project isn't a Go HTTP service with a Chrome-class web frontend, write your own scripts in `scripts/` and reference them from your plan via `mode=auto: <name>`.

## Project layout convention

Per-project test plans and customized scripts live **in the project repo** under `qa/`:

```
<your-repo>/
└── qa/
    ├── README.md
    ├── plans/
    │   └── <release>.md
    └── scripts/
        └── <project-tuned scripts>
```

Only the **reusable engine, persona, and an example plan** live here in the skill. Don't customize them — copy what you need into your project's `qa/` directory.

## Plan format
Each plan is a markdown file in `qa/plans/` (or `plans/` for the example). See `plans/example-rc-plan.md` for the schema. Each step row:

```
| # | step | pass criteria | source | mode |
```

Where `mode ∈ {auto: <script>, browser, human, browser+auto}`:
- `auto: <script>` → run `scripts/<script>` and use exit code / output for pass/fail
- `browser` → use the browser tool: snapshot → assert UI element matches
- `human` → skip and list in needs-human (use for steps requiring restart, OS-level access, multi-day waits, subjective judgment)
- `browser+auto` → both

**Pass criteria must be quantified.** "Visually aligned" / "fast" / "no regression" are anti-patterns — use measurable assertions (offset values monotonically increase, response time ≤ 500 ms, key set differs by ≤ 0 entries).

## Plan: Test Data section
Every plan should include a `## Test data` section telling the qa-engineer how to pick concrete fixtures (sample IDs, pubkeys, URLs) at runtime. Record fixtures used in the final report so failures are reproducible.

## Iteration
After each run, the qa-engineer reports false positives / brittleness; the human edits the relevant `plans/<id>.md` to refine pass criteria, then reruns. Plan files are versioned in git.

## Hard rules
- **Treat the host repo as PUBLIC** unless you've confirmed otherwise — never include personal names, real IPs, API keys, hostnames, or internal handles in any posted comment, issue body, or commit. Refer to environments as "baseline" and "target" in posted artifacts.
- Never run write/destructive operations against **prod** (no deletes, no config writes, no restarts, no UI mutations).
- **Staging mutations are allowed only when the plan step explicitly authorizes them and includes a teardown** (e.g., "add channel X then remove X"). A plan step that mutates without teardown must be marked `mode=human`.
- Treat `mode=human` steps as truly skipped — do not approximate them.
- Time-box: kill any single curl/script after 60s; whole subagent run should fit in < 30 min.

## Brevity (with clarity)

Default to short. Long is the exception, justified by content. See [`../../personas/orchestrator.md`](../../personas/orchestrator.md#brevity-with-clarity) for the canonical rules.

**Limits for artifacts this skill produces**
- The qa-engineer's posted report follows the format in `personas/qa-engineer.md` — that format is the limit. Don't expand.
- One issue per QA run, not one per failing step.
- Failures listed in a single ❌ block per failure, not a paragraph each.
- Verdict: GO / NO-GO / GO-WITH-CAVEATS + one sentence rationale. Not three.
