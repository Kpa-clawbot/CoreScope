# Optional User Management, Sub-project A — Plan Index

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/specs/2026-10-06-user-management-design.md` (read it first; this plan does
not restate its rationale).

**Goal:** Optional, off-by-default accounts for CoreScope: email + password
registration with mailed activation, sessions, roles user/admin, admin user
management, Brevo mail with delivery status. With the feature off, nothing changes.

**Architecture:** Two new Go modules:
- `internal/users` owns a separate `users.db` and is the server's only write path.
- `internal/mailer` provides a `Mailer` interface with Brevo and fake implementations.

`cmd/server` gets an `authService`, built only when `userManagement.enabled` is true.
It registers the auth, account, admin and webhook routes. The existing API-key gate
becomes `requireAdmin` (API key **or** admin session). The frontend learns the feature
from `/api/config/client` and adds `auth.js`, `account.js` and `admin-users.js`.

**Tech stack:** Go 1.22 modules (`modernc.org/sqlite`, `golang.org/x/crypto/argon2`,
`gorilla/mux`), vanilla JS (no build step, no npm deps), Playwright E2E.

## Parts (execute in order)

| Part | File | Produces |
|---|---|---|
| A1 | `docs/plans/2026-10-06-user-management-a1-store-mailer.md` | `internal/users`, `internal/mailer`, fully unit-tested. No server change. |
| A2 | `docs/plans/2026-10-06-user-management-a2-server.md` | Config, `authService`, all HTTP endpoints, `requireAdmin`, webhook, OpenAPI. Go HTTP tests. |
| A3 | `docs/plans/2026-10-06-user-management-a3-frontend-ci-docs.md` | Frontend, E2E build tag + CI, Dockerfile, docs, AGENTS/README. |

Each part leaves the repo green (`go test` in every touched module, `sh test-all.sh`).

## Ground rules (apply to every task)

- **Branch:** `feat/user-management`. Commit after every task. Use author
  `efiten <erwin.fiten@gmail.com>`. End each message with
  `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- **No upstream actions from the work PC:** no PRs, issues or comments on
  Kpa-clawbot/CoreScope. Push only to `origin` (`efiten/CoreScope`) as gh account
  `efiten` (`gh auth switch -u efiten`, push, then switch back).
- **AGENTS.md rules:** no new `map[string]interface{}` in new code (typed structs; the
  existing `routeMeta`/OpenAPI maps are pre-existing), no npm deps, no build step, and
  user-facing strings through `escapeHtml`.
- **Go toolchain:** modules declare `go 1.22`. Do not let `go get` raise the `go`
  directive. If it does, pin a lower dependency version (see A1 Task 1).
- **Run Go tests per module** (`cd internal/users && go test ./...`). The repo has no
  `go.work`.
