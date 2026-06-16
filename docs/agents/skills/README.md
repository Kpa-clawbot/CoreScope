# Skills Index

A "skill" is a markdown recipe an agent loads on demand for a specialized task. Each file in this directory is a copy of a `SKILL.md` from our local skills library, sanitized of operator-private info.

> **Note:** Some skills below are general-purpose / non-CoreScope (media tools, personal-project helpers). They're listed for completeness — they're available in the same skills library but won't fire on CoreScope work. Skills are agent-stack-agnostic in concept; the specific commands assume an OpenClaw-style runtime, translate as needed.

## Core CoreScope pipeline skills

| Skill | When to use |
|---|---|
| [fix-issue](fix-issue.md) | Fix a GitHub issue end-to-end: implement, open PR, wait for CI, hand off to pr-polish. The entry point for most contribution work. |
| [pr-polish](pr-polish.md) | Rebase, polish, and adversarially review a PR to merge-ready using a parallel persona fan-out. |
| [pr-preflight](pr-preflight.md) | Pre-PR-submission fail-fast gate (PII leaks, assertion-shaped tests, theming illusions, etc.). Runs in <60s. |
| [pr-merge-gate](pr-merge-gate.md) | Three-axis merge-readiness check (mergeable + CI green + reviews resolved) per the rules. |
| [ci-watcher](ci-watcher.md) | Lightweight watcher for long-running CI; flips parent on pass/fail. |
| [corescope-release](corescope-release.md) | End-to-end release cut: verify CI, finalize notes, tag, wait for publish, hand over upgrade commands. |
| [qa-suite](qa-suite.md) | Structured QA test-plan run against staging/prod/PR build before merge or release tag. |

## Triage / planning / discovery

| Skill | When to use |
|---|---|
| [bug-intake](bug-intake.md) | Diagnose a bug using expert personas — symptoms, root cause, severity. |
| [feature-intake](feature-intake.md) | Refine a vague feature request into a locked, implementable spec with milestones. |
| [debug-repro](debug-repro.md) | Reproduce bugs locally against fixture or staging before fixing. |
| [devops-fix](devops-fix.md) | Live operational fixes — SSH, docker, sqlite, log triage on staging or prod. |
| [triage-sweep](triage-sweep.md) | Parallel multi-lane sweep of an open issue backlog (stale-check, effort-sizing, dep map). |

## Code quality enforcement

| Skill | When to use |
|---|---|
| [go-style-enforcer](go-style-enforcer.md) | Enforce Google's Go Style Guide on Go diffs, with canonical rule URLs. |
| [kotlin-pr-gate](kotlin-pr-gate.md) | SOLID + XP + Google/JetBrains Kotlin best-practice gate on Kotlin diffs. |

## Subagent infrastructure

| Skill | When to use |
|---|---|
| [subagent-brief-template](subagent-brief-template.md) | The standard task-brief template — required reading before any sub-agent spawn. See [SUBAGENT-BRIEF-TEMPLATE.md](../SUBAGENT-BRIEF-TEMPLATE.md) at top level for the same content. |

## General-purpose / non-CoreScope (available in the library)

These are unrelated to CoreScope but ship in the same skills library. Listed for completeness:

| Skill | What it does |
|---|---|
| [instagram-reel](instagram-reel.md) | Download an Instagram reel/post by URL via yt-dlp. |
| [instagram-reels-coach](instagram-reels-coach.md) | Evidence-based advice for Instagram Reels strategy. |
| [photo-slideshow](photo-slideshow.md) | Build a photo slideshow video with music + transitions. |
| [project-planner](project-planner.md) | Spec/refine a personal project portfolio + budgeting tool. |
| [softball-scout](softball-scout.md) | Evaluate a softball player against NCSA recruiting benchmarks. |
| [srt-calibrate](srt-calibrate.md) | Sync/calibrate SRT subtitle timing using Whisper as reference. |
| [usenet-movie](usenet-movie.md) | End-to-end movie download via NZBgeek + SABnzbd + GDrive. |
| [video-subtitle](video-subtitle.md) | Add translated subtitles to video clips with optional logo overlay. |
