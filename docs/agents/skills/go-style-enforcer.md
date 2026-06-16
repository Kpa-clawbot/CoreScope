---
name: go-style-enforcer
description: "Enforce Google's Go Style Guide (style guide + decisions + best practices) on Go diffs. Use when reviewing Go PRs, auditing Go code, or before committing Go changes. Triggers: 'go style', 'enforce go style', 'audit Go file', 'check go style', 'godoc check', 'review go code'. Takes a file path, PR number, or pasted code. Cites the canonical Google rule URL for every finding. NOT for: code formatting (run `gofmt` and `goimports` instead) or non-Go languages."
---

# Go Style Enforcer

This skill enforces **Google's Go Style Guide** on Go diffs. Every finding cites the
specific canonical rule URL. The skill refuses to invent rules: if it is not in the
cached references, it is not enforced.

## What this skill enforces

- Google Go Style Guide: https://google.github.io/styleguide/go/
- Cached locally (source of truth — `grep` these directly):
  - `references/google-go-styleguide-index.md`
  - `references/google-go-styleguide-guide.md` (canonical + normative)
  - `references/google-go-styleguide-decisions.md` (normative)
  - `references/google-go-styleguide-best-practices.md` (idiomatic patterns)
- `references/INDEX.md` is the keyword → file:line map. Use `scripts/grep-rule.sh <kw>`
  for fast lookup before quoting any rule.

Severity convention used by this skill:

- **must-fix** — violates a rule in `guide.md` (canonical) or a "Bad:" example
  in `decisions.md`. PR is blocked.
- **suggest** — violates a recommendation in `best-practices.md`, or a
  consistency/clarity preference. PR may merge but author should consider it.

## Review procedure (every invocation)

1. **Get the diff.** For a PR: `gh pr diff <num>`. For a file: `cat <path>`.
   For pasted code: use as-is.
2. **Run `gofmt -d` / `goimports -d` mentally first.** If formatting is off,
   STOP and tell the operator to run `gofmt`/`goimports`. This skill does not
   fight `gofmt` (see Hard NO list).
3. **Classify each changed hunk** against the categorized rules below. Walk
   categories in order: Naming → Comments/Doc → Errors → Concurrency →
   Tests → Package Structure → Style Decisions → AI-slop antipatterns.
4. **For every flagged line**, verify the rule by grepping the cached
   reference (`scripts/grep-rule.sh <kw>` or `grep -n` on the file). If the
   rule cannot be confirmed in the source, **drop the finding**. No invented
   rules.
5. **Emit findings** in the [Output format](#output-format) below.
6. **Report severity counts** at the end: `N must-fix, M suggest`.
   If `N > 0`, recommend the PR is NOT merge-ready.

## Output format

For each finding:

```
[severity] <file>:<line>  <rule-name>
  rule: <canonical URL with anchor>
  quote: "<1-line excerpt from cached reference>"
  why:  <one sentence tied to the cited line>
  fix:  <concrete diff suggestion if obvious; otherwise omit>
```

Then a trailing summary:

```
Summary: <must-fix-count> must-fix, <suggest-count> suggest. Merge-ready: <yes|no>.
```

---

## Rule catalogue (cite the URL anchor — not just the page)

All anchors are on `https://google.github.io/styleguide/go/{page}`. Each rule lists
the anchor, a one-line statement, and a "where" pointer (use `scripts/grep-rule.sh`
to pull the full text). Examples reference real CoreScope-style cases (e.g. paths
like `cmd/server/store.go`) as **illustrative shapes only** — they are not literal
citations of any specific committed code.

### 1. Naming

1. **MixedCaps, never snake_case** — `MaxLength` not `MAX_LENGTH`; `maxLength`
   not `max_length`. → guide#mixed-caps
2. **No underscores in names** (3 exceptions: generated-only packages,
   `Test|Benchmark|Example` funcs in `*_test.go`, OS/cgo interop). →
   decisions#underscores
3. **Package names: lowercase, no underscores, no camelCase, concise.** →
   decisions#package-names
4. **Avoid `util`/`common`/`helper`/`model` package names** — uninformative,
   invites import renames. → decisions#package-names, best-practices#util-packages
5. **Receiver name = 1–2 letter abbreviation of type; consistent across all
   methods; never `this`/`self`/`me`/`_`.** → decisions#receiver-names
6. **Constants are `MixedCaps`, NOT `MAX_BUFFER` or `kMaxFoo` or `KMaxFoo`.**
   Name by role, not value (`MaxPacketSize` not `Twelve`). →
   decisions#constant-names
7. **Initialisms keep one case throughout**: `URL`/`url`, `ID`/`id`, `DB`/`db`,
   `XMLAPI`/`xmlAPI`; never `Url`/`Id`/`Db`/`XmlApi`. → decisions#initialisms
8. **No `Get` prefix on getters.** Prefer `Counts()` over `GetCounts()`.
   `Fetch`/`Compute`/`Load` OK when the call is expensive/remote. →
   decisions#getters, best-practices#naming-conventions
9. **Variable name length is proportional to scope** (short for tight loops,
   longer for file/package scope) and **inversely proportional to use
   count**. → decisions#variable-names
10. **Omit type-like words from variable names**: `users` not `userSlice`,
    `count` not `numUsers`/`usersInt`. → decisions#variable-names
11. **`r`/`w` for `io.Reader`/`io.Writer`/`http.Request`/`http.ResponseWriter`;
    `i`/`x`/`y` for loop indices; otherwise prefer real names.** →
    decisions#single-letter-variable-names
12. **No repetition: package vs. exported symbol**: `widget.New` not
    `widget.NewWidget`; `db.Load` not `db.LoadFromDatabase`. →
    decisions#repetitive-with-package
13. **No repetition: variable vs. type**: `var users int` not
    `var numUsers int`; `var primary *Project` not `var primaryProject
    *Project`. → decisions#repetitive-with-type
14. **No repetition: in context** — inside `func (db *DB) UserCount()` use
    `count`/`err`, not `userCount`/`dbLoadError`. → decisions#repetitive-in-context
15. **Local names of renamed imports follow package-name rules** (lowercase,
    no underscores). Proto imports use `pb` suffix; gRPC stubs use `grpc`
    suffix. → decisions#import-renaming, best-practices#import-protos
16. **Test-double naming**: package `<X>test`; type `Stub` (one) or
    `StubService`/`AlwaysCharges`/`AlwaysDeclines` when multiple. →
    best-practices#naming-doubles

### 2. Comments / Documentation

17. **Every exported top-level name has a doc comment** that begins with the
    name and is a full sentence. → decisions#doc-comments
18. **Package comment lives immediately above `package` clause, no blank line,
    one per package** (use `doc.go` for very long ones). →
    decisions#package-comments
19. **`main` package doc starts `Binary <name>` / `Command <name>` / `The
    <name> command`** — name matches BUILD target. → decisions#package-comments
20. **Comment sentences are capitalized and punctuated like English.**
    Inline field comments may be fragments. → decisions#comment-sentences
21. **Comments explain "why," not "what"** the code does. Redundant or
    code-restating comments are noise. → guide#clarity (and decisions#commentary)
22. **No long unwrapped comment paragraphs** — wrap around 80–100 cols, but
    don't break URLs. → decisions#comment-line-length
23. **Runnable examples (`func Example…`) live in `_test.go`**, not in
    production source. → decisions#examples
24. **Document non-default `context` behavior**: deadline assumptions,
    interruption semantics, returned-error-on-cancel if not `ctx.Err()`. →
    best-practices#documentation-contexts
25. **Document concurrency safety only when non-obvious** (read-only is
    assumed safe; mutators are assumed unsafe). →
    best-practices#documentation-concurrency
26. **Document cleanup obligations** (caller must `Close`, `cancel`, etc.).
    → best-practices#documentation-cleanup
27. **Document which errors are programmatically inspectable** (sentinel /
    type) and which are opaque. → best-practices#documentation-errors

### 3. Errors

28. **`error` is the last return value**; return `error` (interface), never
    a concrete pointer type like `*os.PathError` (typed-nil trap). →
    decisions#returning-errors
29. **Error strings: lowercase, no terminal punctuation** (unless starting
    with a proper noun / acronym / exported name). →
    decisions#error-strings
30. **Don't discard errors with `_`** — handle, return, or `log.Fatal`. If
    you must ignore, leave a comment explaining why. → decisions#handle-errors
31. **No in-band error sentinels** (`-1`, `""`, `nil`). Return `(T, bool)`
    or `(T, error)`. → decisions#in-band-errors
32. **Handle errors first, return early, no `else` after `return`/`panic`/
    `log.Fatal`.** → decisions#indent-error-flow
33. **Give errors structure when callers must distinguish them** — sentinel
    var (`errors.Is`) or typed struct (`errors.As`); never string-match
    `err.Error()`. → best-practices#error-structure
34. **Don't duplicate info already in the wrapped error** — `os.Open` puts
    the path in already; `fmt.Errorf("launch codes unavailable: %v", err)`
    not `"could not open settings.txt: %v"`. →
    best-practices#error-extra-info
35. **`%w` vs. `%v`**: `%w` only when callers are intended to
    `errors.Is`/`errors.As`; otherwise `%v`. Don't expose internal errors
    across system boundaries with `%w`. →
    best-practices#error-extra-info
36. **`%w` goes at the END of the format string** — `"…: %w"` — so the chain
    prints newest-to-oldest. Exception: sentinel errors may go at the
    START. → best-practices#wraps, best-practices#sentinel-error-placement
37. **Don't add an annotation whose only job is to say "failed"** — e.g.
    `fmt.Errorf("failed: %v", err)` — just return `err`. →
    best-practices#error-extra-info
38. **Don't log AND return the same error** (caller will log again =
    logspam). → best-practices#logging-errors
39. **Use `log.Exit` in `main` for init/config errors, not `log.Fatal`** —
    no stack trace, just a human-readable message. →
    best-practices#program-initialization

### 4. Concurrency

40. **`context.Context` is the first parameter, named `ctx`.** Functions that
    take a ctx should usually return an error. → decisions#contexts
41. **No `context.Context` in struct fields** (with rare documented exceptions
    for request-scoped types). → decisions#contexts
42. **Don't pass a `nil` Context.** Use `context.TODO()`. → decisions#contexts
43. **Don't define custom Context types or use non-Context APIs to pass
    request-scoped values.** → decisions#custom-contexts
44. **Start a goroutine only when its lifetime is clear and bounded**;
    callers must be able to stop it. → decisions#goroutine-lifetimes
45. **Don't call `t.Fatal` from a goroutine other than the test goroutine.**
    Use `t.Error` + return, or send on a channel. →
    best-practices#t-fatal-goroutines
46. **Shadowing trap with `ctx, cancel := context.WithTimeout(ctx, …)` inside
    an `if`**: outside the `if`, `ctx` is the original. Use `=` plus `var
    cancel func()`. → best-practices#shadowing
47. **Read-only operations are assumed safe for concurrent use; mutators
    aren't.** Document deviations only. → best-practices#documentation-concurrency

### 5. Tests

48. **Use `testing`. Don't bring in assertion libraries** (testify, gocheck,
    etc.) — they obscure intent and skip information in failures. →
    decisions#assertion-libraries
49. **Failure messages: identify the function, the input, and report `got`
    BEFORE `want`.** → decisions#identify-the-function, decisions#identify-the-input,
    decisions#got-before-want
50. **`t.Error` to keep going, `t.Fatal` only when continuation is
    impossible.** → best-practices#t-error-vs-t-fatal
51. **Table-driven tests use a struct slice with named fields**; subtest
    name in `name string`, run with `t.Run(tc.name, func(t *testing.T){…})`.
    → decisions#table-driven-tests, decisions#subtests
52. **Subtest names follow `MixedCaps` and are unique within the test.** →
    decisions#subtest-names
53. **Use `cmp.Diff` for non-trivial equality**, print the diff in the
    failure message. → decisions#equality-comparison-and-diffs,
    decisions#print-diffs
54. **Test the error semantics, not the error string** — `errors.Is`,
    `errors.As`, or a `wantErr bool`/`wantCode` field. Don't substring-match
    `err.Error()`. → decisions#test-error-semantics
55. **Use field names in struct literals inside table tests** (skip
    zero-value fields). → best-practices#test-field-names
56. **`Example…` functions belong in `*_test.go`** and double as runnable
    docs. → decisions#examples
57. **Helpers call `t.Helper()` first thing**, take `*testing.T` as the
    first param, and return values rather than fail-and-return. →
    best-practices#error-handling-in-test-helpers
58. **Leave test concerns to `Test…` functions** — don't put assertions in
    production code paths "for tests." → best-practices#leave-testing-to-test
59. **Black-box test packages use `<pkg>_test` suffix.** →
    decisions#tests-in-different-package

### 6. Package structure

60. **Don't make `init()` do nontrivial work**; flags parsed in `main`;
    don't log before flags parsed. → best-practices#program-checks-and-panics
61. **Don't blank-import (`import _`) outside `main` or `_test.go`** (limited
    exceptions: `embed`, `nogo`). → decisions#import-blank
62. **No dot-imports (`import .`).** → decisions#import-dot
63. **Import groups in order: stdlib / project & vendor / proto / blank.**
    → decisions#import-grouping
64. **Avoid mutable package-level state.** Globals are global coupling; if
    you must, isolate behind a "provide a default instance" API. →
    best-practices#global-state
65. **Avoid unnecessary interfaces** — interfaces in the package that
    *consumes* them, not the one that defines the concrete type. →
    best-practices#avoid-unnecessary-interfaces,
    best-practices#interface-ownership
66. **Don't split one package across many tiny files or jam a package into
    one giant file.** Files should be discoverable by topic. →
    best-practices#package-size

### 7. Style decisions (gofmt won't catch)

67. **Struct literals from OTHER packages MUST use field names** (positional
    creates hidden coupling to field order). → decisions#literal-field-names
68. **Closing brace on its own line, matching opening-brace indentation;
    trailing comma on the last element of a multi-line literal.** →
    decisions#literal-matching-braces
69. **Omit zero-value fields** from struct literals unless the zero is part
    of the test/intent. → decisions#zero-value-fields
70. **Prefer `nil` slice over `[]T{}` for the empty case** (declared with
    `var t []string`). → decisions#nil-slices
71. **Use `len(s) == 0`, not `s == nil`,** to test emptiness; don't expose
    a nil-vs-empty distinction in your API. → decisions#nil-slices
72. **Don't break a function signature across lines.** Refactor or extract
    a struct param instead. → decisions#function-formatting
73. **No inline arg comments** like `f(ctx, 42 /* port */)` — use an
    options struct or document on the function. → decisions#function-formatting,
    best-practices#option-structure
74. **No naked returns in medium/large functions.** Declare the return and
    return it explicitly. → decisions#named-result-parameters
75. **Don't name result params just to enable naked returns** or to avoid a
    one-line declaration. → decisions#named-result-parameters
76. **`switch` over `if`/`else if` chains; `break` is implicit; use
    `fallthrough` only when intentional.** → decisions#switch-and-break
77. **`Must…` constructors are for compile-time / package-init use; they
    panic on error, not for normal control flow.** → decisions#must-functions
78. **Pass values, not pointers, for small immutable types** (`time.Time`,
    small structs). Receiver type is a separate decision. →
    decisions#pass-values, decisions#receiver-type
79. **All methods of a type use the SAME receiver type** (`*T` or `T`),
    don't mix unless you have a documented reason. → decisions#receiver-type
80. **Type aliases (`type A = B`) are rarely correct.** Prefer a named
    type unless you're staging a migration. → decisions#type-aliases
81. **Use `%q` for user-typed/control-char-bearing strings in format
    output** (logs, errors). → decisions#use-%q
82. **Use `any`, not `interface{}`**, in new code (Go 1.18+). →
    decisions#use-any
83. **Use `crypto/rand` for any security-sensitive randomness; `math/rand`
    is not safe for tokens, IDs you don't want guessed, etc.** →
    decisions#crypto-rand
84. **Don't copy types containing locks/atomics/`sync.*`/`atomic.*` by
    value.** → decisions#copying
85. **`fmt.Sprintf` for formatted strings; `+` for trivial cases;
    `strings.Builder` when concatenating in a loop.** →
    best-practices#string-concatenation,
    best-practices#strings-builder
86. **Variadic option pattern (`type Option func(*config)`) is the
    preferred way to add optional behavior to constructors** — not 7-arg
    funcs. → best-practices#variadic-options, best-practices#option-structure
87. **Hint sizes when you know them**: `make([]T, 0, n)`,
    `make(map[K]V, n)`. → best-practices#size-hints
88. **Specify channel direction (`<-chan`, `chan<-`) at function signatures.**
    → best-practices#channel-direction

### 8. AI-slop antipatterns this skill specifically catches

These map LLM-emitted Go patterns ("AI slop") to the cited Google rule. They
are the highest-yield checks on PRs authored or co-authored by Copilot/Claude/GPT.

| Antipattern (what the bot emits) | Cited rule |
|---|---|
| `map[string]interface{}` for a payload that has a known shape | decisions#use-any (use `any`, but actually: model the type — best-practices#error-structure logic generalizes) |
| `interface{}` everywhere instead of `any` | decisions#use-any |
| `fmt.Errorf("failed to do X: %w", err)` when "failed" carries no info | best-practices#error-extra-info |
| Stacked `%w` wrappers at every layer ("excessive wrapping") with no `errors.Is` consumer | best-practices#wraps, best-practices#error-structure |
| `if x != nil { … }` defensive checks on values that cannot be nil per the type | guide#simplicity ("does not assume reader can't follow code") |
| `GetFoo()` / `SetFoo()` accessor pairs in idiomatic Go structs | decisions#getters |
| Single-letter receiver `s` on every type — even when types share scope | decisions#receiver-names (must be abbreviation of THIS type) |
| `type IFooService interface { … }` (Hungarian-style I-prefix) | decisions#initialisms (no Hungarian) + best-practices#avoid-unnecessary-interfaces |
| `func DoThing() (result *Thing, err error)` naked returns in 40-line funcs | decisions#named-result-parameters |
| `util/` or `helpers/` package with grab-bag funcs | best-practices#util-packages, decisions#package-names |
| `panic(err)` instead of returning | decisions#dont-panic |
| Test using `assert.Equal(t, want, got)` (testify) | decisions#assertion-libraries |
| Test failure message `"test failed"` with no `got`/`want`/inputs | decisions#useful-test-failures, decisions#got-before-want |
| `*context.Context` (pointer to interface) anywhere | decisions#contexts |
| `ctx, cancel := context.WithTimeout(ctx, …)` inside an `if` that needs the new ctx after the block | best-practices#shadowing |
| Inline arg comments: `New(ctx, 42 /* port */, true /* tls */)` | decisions#function-formatting, best-practices#option-structure |
| `MAX_FOO`, `kMaxFoo`, `IsURL` vs `IsUrl` mixed in one file | decisions#constant-names, decisions#initialisms |
| `import _ "some/lib"` in a library package | decisions#import-blank |

This list is the operationalization of the AGENTS.md rule (CoreScope issue
#1383) that the operator's tooling repeatedly emits these patterns. Each row
must cite the canonical URL in the actual review output — do not paraphrase
the rule, quote it from the cached reference.

---

## Hard NO list

Things this skill **must refuse to do**:

1. **Do not fight `gofmt` / `goimports`.** Line wrapping, brace placement
   that `gofmt` would change, import alphabetization within a group — these
   are tools' jobs. Tell the operator to run the tool.
2. **Do not invent rules.** If a finding's rule cannot be confirmed by
   `grep`ing one of the four cached references, drop it. "Common Go wisdom"
   that is not in Google's pages is out of scope.
3. **Do not enforce subjective style** the guide explicitly leaves to local
   consistency (e.g., `%s` vs. `%v` for error formatting — see
   guide#local-consistency).
4. **Do not police line length.** The guide explicitly says there is no
   fixed line length. → guide#line-length
5. **Do not write PII** into review comments — names, emails, internal IPs,
   tokens. Honor the workspace PII preflight.
6. **Do not auto-apply fixes.** Emit findings; the operator decides.
7. **Do not lecture on Effective Go content** beyond what these four pages
   cite. The guide builds on Effective Go but is the authoritative source
   here.
8. **Do not enforce performance "rules"** unless they are documented in
   the four cached pages. Google's style guide is intentionally light on
   perf prescriptions.

---

## How to cite a finding (mechanical recipe)

Given a candidate violation:

1. `bash scripts/grep-rule.sh <keyword>` — find the rule in cache.
2. Open the matching file at the reported line; copy a ≤120-char quote.
3. Construct the canonical URL as
   `https://google.github.io/styleguide/go/<page>#<anchor>` where
   `<page>` is `guide`/`decisions`/`best-practices` and `<anchor>` is the
   slug Google uses for the heading (lowercase, hyphenated, see existing
   URLs in `references/INDEX.md`).
4. Emit per [Output format](#output-format).

If you cannot construct the anchor with confidence, link to the page root
and quote the section heading verbatim in the `why` line — but prefer the
anchored URL.

---

## References

- `references/google-go-styleguide-index.md` — mirror of
  https://google.github.io/styleguide/go/
- `references/google-go-styleguide-guide.md` — canonical foundation
- `references/google-go-styleguide-decisions.md` — settled decisions
- `references/google-go-styleguide-best-practices.md` — idiomatic patterns
- `references/INDEX.md` — keyword → file:line lookup table
- `scripts/grep-rule.sh <kw>` — convenience grep across all four references
