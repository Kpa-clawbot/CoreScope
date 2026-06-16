# RULES — Hard-won lessons for agent-driven contribution

These 35 rules are the residue of every failure mode we've hit running AI agents against this codebase. Each one was earned. Read them before your agent ships its first PR. Sanitized of operator-specific names, hosts, and incident attributions — the substance is what carries.

These map onto the worker rules in the project root `AGENTS.md` and the workflow in [WORKFLOW.md](WORKFLOW.md).

---

1. **Acknowledgment is not permission.** "Good idea" / "interesting" are NOT "go."
2. **If you didn't check, say "I haven't checked yet."** No fabrication. Hedge words banned when substituting for a tool call.
3. **Check your own surfaces first.** Workspace files, sub-agents, prior messages — before attributing elsewhere.
4. **When challenged on accuracy, your first action is a tool call — not a sentence.**
5. **State your source and method for every fact.** "Read from path/file.cpp:42" beats "I think the protocol does X."
6. **No invented identifiers.** Issue numbers, PR numbers, commit SHAs, file paths, ports, IPs, package versions — if you didn't see it in a tool result this session, you don't have it.
7. **Compliance = WHAT + WHEN + VERIFICATION in one message.** "I'll fix it" without specifics = lie.
8. **When wrong, name the rule you broke.** Forces self-classification.
9. **No humor when criticized, when you erred, or when someone's frustrated.**
10. **Every deliverable gets written to a file in the same message it's created.** Include the path.
11. **"I'll remember" is banned.** Files, scheduled tasks, or a tracked commitments doc — or it didn't happen.
12. **Scheduled-task payloads contain instructions, not data.** Fetch live state at fire time.
13. **No partial completion claims.** 6/7 done = "1 of 7 incomplete," not "✅ all good."
14. **Negative findings are required.** "Checked X, nothing relevant" beats silence.
15. **No new work when a prior task is open.** Finish or escalate, then move on.
16. **Errors and lessons get written to a file before they get explained.**
17. **End every message with the next concrete action — or a question.**
18. **"Tests pass" is not "feature works."** For frontend changes: spin up the server, curl the actual route, grep rendered HTML. If you cannot stand up a server, say so explicitly.
19. **For any failing test, reproduce locally FIRST — not read-and-guess.** The cycle: identify exact input → reproduce failure locally → observe actual error → form hypothesis → fix → re-run → push.
20. **"Merge-ready" requires THREE checks:** (a) git mergeable, (b) CI green, (c) review threads resolved (no unaddressed BLOCKER/MAJOR).
21. **Force-push rules:** Banned for master/shared/racing-to-merge branches. Allowed (preferred) with `--force-with-lease` on your own bot PRs in active rework.
22. **Parent verifies subagent GH writes.** After a worker posts any GH comment, the worker returns the comment URL in its completion report and the parent re-fetches it.
23. **Read full review before relaying merge-readiness.** `grep -c 'BLOCKER\|MAJOR'` on the review file. ≥1 = not merge-ready.
24. **Never reload/restart the agent runtime while subagents are running.** Signal restarts kill children.
25. **Merge dependency PRs before rebasing dependents.**
26. **Subagent task briefs MUST include reproduction commands for debugging tasks.**
27. **Collapse follow-up work into ONE subagent brief per PR.** If polish surfaces docs gaps + missing E2E + a typo, all three go into a single follow-up subagent — not three. Multiple subagents touching the same branch race and waste tokens.
28. **The PR pipeline is auto-chained: fix → CI → polish → merge.** When CI goes green on a PR you opened, you spawn polish IMMEDIATELY without waiting for a user prompt. When polish reports merge-ready, you tell the user it's ready (you do not auto-merge unless they said so). Sitting on a green CI is a discipline failure.
29. **Verify every PR/issue state claim with a tool call in the same turn.** "PR is merge-ready" requires `gh pr view --json mergeable,statusCheckRollup` output in the same message. "CI green" requires `gh pr checks` output. No state claim without proof in the same turn.
30. **Batch/wave language = EXECUTE, not acknowledge.** "Go to next batch", "next wave", "do the rest", "merged both go to next batch" are PERMISSION + INSTRUCTION. Spawn the work in the same turn, do not reply with "should I start?" or a plan summary. Plans are for unfamiliar territory; batches are for executing known plans.
31. **CI watcher pattern for long-running PRs.** When a PR's CI takes >5 min and you have other work to do, spawn a lightweight watcher subagent: poll `gh pr checks <PR>` every 60s up to 30 min. When CI flips from pending to pass/fail, report immediately. Parent then chains polish on the green/fail signal.
32. **Subagent timeout defaults: 30 min minimum for ALL implementation work; 45 min for XL effort.** Short timeouts cause wasted work when subagents time out mid-implementation with nothing pushed. The cost of an extra 15 min of idle timeout is zero; the cost of a timeout mid-work is a full respawn + duplicate token burn.
33. **Never tag a `[skip ci]` commit for a release.** Tags trigger CI via the push event — but `[skip ci]` in the commit message suppresses the workflow. Always tag a real code commit (not a badge/coverage update). If HEAD is `[skip ci]`, find the most recent non-skip commit (`git log --oneline origin/master | grep -v '\[skip ci\]' | head -1`) and tag THAT.
34. **"Fixes #X" means ALL acceptance criteria met.** If a PR only addresses part of an issue, use "Partial fix for #X" in the body and DO NOT include "Fixes #X" or "Closes #X" — those auto-close the issue. Leave it open. List what's done and what's NOT done in the PR body. Only the human closes issues after verifying.
35. **NEVER merge a PR without reading its review comments in the same turn.** "Merge whatever is green and mergeable" / "merge what's ready" / any bulk-merge instruction REQUIRES `gh pr view <N> --comments` per PR in the same turn, with a one-line audit per PR: `PR #N: <reviewer> verdict=<merge-ready|BLOCKER count|MAJOR count>`. CI green ≠ review-clean. mergeable=MERGEABLE ≠ review-clean. Any `gh pr merge` not preceded by a `--comments` fetch in the same turn is a discipline failure.

---

## Red lines (always)

- Don't exfiltrate private data.
- Prefer trash/recycle over `rm -rf`.
- Never write a real human's name (operator, user, athlete, anyone) into a committed file. Use "the user" / "the operator" / a role label.
- When in doubt, ask.
