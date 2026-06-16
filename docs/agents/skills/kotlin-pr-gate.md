---
name: kotlin-pr-gate
description: "Mandatory pre-merge quality gate for any PR touching Kotlin code. Enforces SOLID principles, Extreme Programming (XP) practices (TDD, simple design, refactor mercilessly, collective ownership, continuous integration), and Google/JetBrains Kotlin best practices. Runs in <90s via scripted checks plus an adversarial review checklist for the polish agent. Loaded by pr-polish and pr-preflight skills BEFORE a Kotlin-touching PR is marked ready or merged. Triggers: 'kotlin pr gate', 'kotlin review', 'enforce SOLID on kotlin', 'XP gate', 'kotlin best practices'. NOT for: non-Kotlin code, formatting only (run ktlint/detekt), or post-merge audits."
---

# Kotlin PR Gate

Adversarial gate for Kotlin PRs. Runs scripted checks for the mechanical violations and provides a structured checklist for the reviewer (human or polish agent) for the principle-shaped violations that grep cannot catch.

## When to run

- **pr-polish (Subagent 1):** AFTER rebase, BEFORE pushing fixes — gate the diff.
- **pr-polish (Subagent 3, fresh-context polish):** as part of the cold-read review. Cite findings against the checklist sections below in the PR comment.
- **fix-issue / spec-reconcile (Subagent E):** verify no hard-gate failures before merge.
- **Manual:** `bash ~/.openclaw/skills/kotlin-pr-gate/scripts/run-all.sh [BASE]` from the worktree (default `origin/master`).

## Inputs

- Worktree at HEAD of the feature branch with at least one `*.kt` or `*.kts` file in the diff
- `BASE` ref (default `origin/master`)
- Optional: `ktlint`, `detekt` available on PATH (auto-detected; gate degrades gracefully if absent)

## Hard gates (BLOCK merge)

| # | Check | Script | What it catches |
|---|-------|--------|-----------------|
| 1 | Test added with non-trivial production change | `scripts/check-test-coexists.sh` | XP TDD — production-only diff (no test added/changed) for non-pure-refactor PRs |
| 2 | Red commit on the branch | `scripts/check-red-commit-kotlin.sh` | TDD red→green visible in `git log`; red commit must compile and fail on an assertion (mirrors `pr-preflight` rule but Kotlin-aware) |
| 3 | No `!!` on non-test code | `scripts/check-bang-bang.sh` | Null-safety violation (Kotlin idiom #1) outside `src/test/`, `src/androidTest/` |
| 4 | No `runBlocking` in production | `scripts/check-runblocking.sh` | Concurrency anti-pattern outside `src/test/`, `main()`, top-level scripts |
| 5 | No `GlobalScope` | `scripts/check-globalscope.sh` | Structured concurrency violation (memory leaks, untraceable failures) |
| 6 | No `Thread.sleep` in coroutines | `scripts/check-thread-sleep.sh` | Blocks the dispatcher; use `delay()` |
| 7 | No mutable `var` in `data class` | `scripts/check-mutable-data-class.sh` | Breaks `equals`/`hashCode`, breaks Set/Map semantics |
| 8 | No catch-all exceptions | `scripts/check-catch-all.sh` | `catch (e: Exception)` or `catch (e: Throwable)` without re-throw/log — swallows bugs |
| 9 | No `println` in production | `scripts/check-println.sh` | Replace with proper logger (Timber/SLF4J/Logcat depending on target) |
| 10 | `Result<T>` not returned from suspend fn without handling | `scripts/check-result-unhandled.sh` | Smell: returns `Result<T>` but callers `.getOrNull()` and discard — pick one error model |
| 11 | Lint clean | `scripts/check-ktlint.sh` | `ktlint --format=plain --code-style=intellij` exits 0 |
| 12 | Detekt clean (if config present) | `scripts/check-detekt.sh` | Repo's detekt config passes (does not invent rules) |

## Warnings (log; require ack in PR body under `## Kotlin gate overrides`)

| # | Check | Script | What it catches |
|---|-------|--------|-----------------|
| W1 | `Any` in public API | `scripts/check-any-public.sh` | Type-erasure — usually a sign of missing generic |
| W2 | Object > 200 lines | `scripts/check-large-objects.sh` | God objects (SRP violation candidate) |
| W3 | Function > 30 lines | `scripts/check-large-functions.sh` | XP simple-design / refactor signal |
| W4 | More than 4 params on a function | `scripts/check-param-count.sh` | "Long Parameter List" smell — extract Parameter Object |
| W5 | Mutable shared state at top level | `scripts/check-toplevel-mutable.sh` | Global mutable state — hostile to testability |
| W6 | New abstract class without justification | `scripts/check-abstract-without-impl.sh` | YAGNI/XP — prefer concrete + interface when truly needed |

## Run

```bash
bash ~/.openclaw/skills/kotlin-pr-gate/scripts/run-all.sh origin/master
```

Exit 0 = clean. Exit 1 = hard-gate failure (BLOCK merge). Exit 2 = warnings only (must ack in PR body).

## Override format (PR body)

```
## Kotlin gate overrides
- check-bang-bang: justified — `!!` on a constant we just literal-defined two lines above; refactoring to ?.let{} hurts readability for zero safety win.
- W3: justified — single 47-line render function; splitting yields one-shot helpers that are harder to follow than the linear function.
```

If you cannot justify in one sentence, the override is invalid. Refactor instead.

## Principle review checklist (adversarial polish agent)

After scripts pass, the polish agent (or human reviewer) MUST score the diff against the principle catalog below and post findings as a PR comment. Each section: cite file:line for hits, link to the reference doc, mark BLOCKER / MAJOR / MINOR.

### SOLID
- **S — Single Responsibility:** does each class/function have ONE reason to change? See [`references/solid.md`](references/solid.md) §S. Look for: classes with `,` in their description, functions that do "X and Y", suffixes like `Helper`/`Manager`/`Util` masking multiple concerns.
- **O — Open/Closed:** can behavior be extended without modifying existing code? Look for: `when (type)` switches that grow per feature, hard-coded type checks (`is X`) where polymorphism would suffice.
- **L — Liskov Substitution:** can every subtype be substituted for its base type without surprises? Look for: overrides that throw `UnsupportedOperationException`, narrower preconditions, stricter return types violating contracts.
- **I — Interface Segregation:** are clients forced to depend on methods they don't use? Look for: large interfaces with multiple unrelated method clusters, implementors with most methods stubbed/empty.
- **D — Dependency Inversion:** do high-level modules depend on abstractions (not concretions)? Look for: direct construction of infrastructure inside business logic (`val db = Room.databaseBuilder(...)` deep in a ViewModel), `import android.util.Log` in pure-Kotlin modules, missing constructor injection.

See [`references/solid.md`](references/solid.md) for examples, before/after diffs, and Kotlin-specific patterns.

### Extreme Programming (XP)
- **TDD:** test commit precedes production commit (script gate #1, #2). Tests assert behavior, not implementation. See [`references/xp.md`](references/xp.md) §TDD.
- **Simple Design (4 rules, Beck):** (1) passes tests, (2) reveals intent, (3) no duplication, (4) fewest elements. The diff should remove or hold steady on lines/abstractions/files where possible.
- **Refactor Mercilessly:** spotted duplication or a smell while implementing? Fix it in the same PR (small enough to review). Don't leave `TODO: refactor later`.
- **YAGNI:** any new abstraction (interface, sealed class, factory, builder) must have ≥2 current concrete callers OR a written justification in the PR body. No speculative generality.
- **Continuous Integration:** the PR is small (target <400 LOC diff excluding tests/generated), rebased on master, CI green.
- **Collective Ownership:** code matches existing style/idioms in the touched module. No personal stylistic flourishes that diverge from the surrounding code.
- **Pair / Mob proxy:** the polish agent IS the pair partner — every BLOCKER finding from the gate counts as the pair would have stopped this from being committed.

See [`references/xp.md`](references/xp.md) for the full 12 practices and how each gates a PR.

### Kotlin best practices (Google/JetBrains/Effective Kotlin)
- **Immutability first:** prefer `val` over `var`, `List` over `MutableList` at the boundary, `data class` for value semantics. See [`references/kotlin-best-practices.md`](references/kotlin-best-practices.md) §Immutability.
- **Null safety:** no `!!` outside tests (gate #3). Prefer `?.let`, `?:`, `requireNotNull(x) { "msg" }`, or hoisting to non-null at boundary.
- **Coroutines:** structured concurrency only (gates #4–6). `suspend` for IO, `Flow` for streams, `CoroutineScope` injected (not constructed).
- **Sealed classes for state:** model exhaustive state with `sealed interface` + `when` (compiler-checked). No string/int "enum" magic.
- **Extension functions:** scope tightly. Public extension on `Any` or on common types from other modules = anti-pattern. Prefer `internal` or module-local.
- **Inline functions:** only when measurable; not by default. `inline` + lambda is the right tool for higher-order functions you want zero-allocation.
- **Receiver-aware DSLs (`@DslMarker`):** when building DSL-style APIs, mark scopes to prevent receiver leaking.
- **Visibility:** default to `internal` for module APIs. `public` is opt-in, not default. See [`references/kotlin-best-practices.md`](references/kotlin-best-practices.md) §Visibility.
- **Equality / hashCode:** never override on mutable fields. Use `data class` and keep its primary constructor fields immutable.
- **Companion object as namespace, not as god object:** factory methods OK; pile of unrelated utilities NOT OK.

See [`references/kotlin-best-practices.md`](references/kotlin-best-practices.md) for the full rule set with examples and citations to Google Android Kotlin Style Guide + JetBrains Coding Conventions + Effective Kotlin (Marcin Moskała).

## Output (mandatory)

The polish agent must post a single PR comment with this exact structure:

```
## Kotlin PR Gate

**Scripted checks:** PASS | FAIL (N hard / M warnings)
**Principle review:** N BLOCKER / N MAJOR / N MINOR

### SOLID
- [S/O/L/I/D]: <finding cite file:line> — <one-line> (BLOCKER|MAJOR|MINOR)

### XP
- <practice>: <finding> (BLOCKER|MAJOR|MINOR)

### Kotlin idioms
- <rule>: <finding cite file:line> (BLOCKER|MAJOR|MINOR)

### Overrides accepted (from PR body)
- <check>: <one-line justification>

### Verdict
MERGE-READY | NEEDS-FIX (BLOCKER count > 0) | WARN (MAJOR count > 0, owner decision)
```

If no Kotlin files are in the diff, post: `## Kotlin PR Gate\nNo Kotlin files in diff — gate skipped.`

## Integration

- **pr-polish:** read this skill at the start of Subagent 1 Phase 2 (self-review). Re-run on any new push. Subagent 3 uses the principle checklist for the cold-read review.
- **fix-issue:** the implementation subagent runs `scripts/run-all.sh` after committing, before `gh pr create`. Hard-gate failures must be fixed; warnings noted in PR body.
- **garage-inventory / any Kotlin project:** add a one-line reference to this skill in the project's `AGENTS.md` so workers load it before opening a Kotlin PR.

## Performance budget

- Total scripted checks: <90s on a 5000-LOC diff.
- Per check: <10s. If exceeded, demote to CI.
- Principle review: human/agent time, not scripted; target <15 minutes for a typical PR.

## Notes

- Scripts use POSIX bash + grep + git. ktlint/detekt invoked only if present on PATH.
- Scripts diff against `BASE`, not whole repo — fast on monorepos.
- This gate is project-agnostic for Kotlin; project-specific extra rules live in the project's own AGENTS.md, not here.
- Override format is the ONLY mechanism to bypass a hard gate. No "I'll fix it next PR" allowed.
