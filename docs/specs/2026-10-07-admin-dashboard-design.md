# Optional User Management, Sub-project C: Admin Dashboard, Design Spec

**Status:** approved in conversation 2026-10-07, written for review.
**Builds on:** sub-project A (`docs/specs/2026-10-06-user-management-design.md`) and B
(`docs/specs/2026-10-06-user-settings-sync-design.md`), both merged (#2129, #2130).
Roadmap: #2128, part C.

## Problem

An admin can manage users one by one in `#/admin/users`, but nothing shows the state of
the instance at a glance: how many people registered, who is stuck in activation, whose
mail bounces, whether someone is guessing a password, whether an MQTT source dropped.
The audit log exists (`internal/users/audit.go`) but can only be read per user, and
logins are not recorded in it.

## Goals

1. One admin area with an overview that answers "does anything need my attention?" and
   links straight to the user or audit entries behind each item.
2. A global, filterable audit log, including logins.
3. A compact system status (server, MQTT sources, observers) next to the user figures.
4. With user management off, nothing changes. Logged-out visitors and non-admins see no
   new UI, and the new endpoints refuse them.

## Non-goals

- Restricting existing pages (perf, MQTT status, others) to logged-in users or admins.
  This is a likely next step; this design keeps it possible (see "Prepared for page
  restriction") but does not build it.
- Changing the public perf, health and MQTT endpoints.
- Role-aware customizer tabs. `customizer.disabledTabs` (#1508, `config.go:213-218`)
  stays as it is.
- Statistics about analyzer data (packets, nodes): those pages exist already.

## Decisions (from the design conversation)

| # | Topic | Decision |
|---|---|---|
| 1 | Purpose | Operator overview first. Page restriction later, out of scope here. |
| 2 | Scope | User figures, global audit log (with logins), and system status, all three. |
| 3 | Login audit | Successful and failed logins, no IP address. Login rows are deleted after 90 days; other audit actions are kept as today. |
| 4 | Structure | One admin page with tabs: Overview, Users, Audit (`#/admin?tab=...`). User figures and audit through two new admin endpoints; system status composed in the browser from the existing endpoints. |

## Architecture

```
public/admin.js            tab shell for page 'admin' (#/admin?tab=overview|users|audit)
public/admin-overview.js   Overview tab: attention list, Users card, System card
public/admin-audit.js      Audit tab: filters, table, "Load more"
public/admin-users.js      existing user table, mounted as the Users tab
        │ GET /api/admin/stats, GET /api/admin/audit         (withAdmin)
        │ GET /api/health, /api/healthz, /api/mqtt/status, /api/observers (existing, public)
cmd/server/admin_stats_handlers.go, admin_audit_handlers.go
internal/users: Stats(now), AuditList(filter), PruneAudit(actions, maxAge), schema v3
```

`cmd/server` keeps writing only `users.db` through `internal/users` (AGENTS.md read/write
separation).

## Overview tab

### "Needs attention"

Shown only when at least one item applies. Each item links to a filtered tab.

| Item | Rule | Link |
|---|---|---|
| Stuck activations | pending accounts created more than 24 hours ago | Users, `status=pending` |
| Mail problems | users with `email_bouncing` set | Users, `bouncing=1` (a new Users filter) |
| Password guessing | accounts with 5 or more `user.login.failed` rows in the last 24 hours | Audit, `action=user.login.failed&user=<id>` |
| MQTT source down | a source in `/api/mqtt/status` that is not connected, or whose last message is older than 10 minutes | the existing MQTT status panel |

The 24-hour, 5-attempt and 10-minute thresholds are constants in this version (AGENTS.md
rule 8: they belong in the customizer or config later; noted for a follow-up).

### Users card (`GET /api/admin/stats`)

- Total accounts; by status (active, pending, disabled); number of admins.
- New registrations in the last 7 and 30 days, plus a per-day count for the last 30 days
  drawn as a small bar chart.
- Active users in the last 7 and 30 days: `last_login_at` or a session `last_seen_at`
  inside the window, counted once per user.
- Logins and failed logins in the last 24 hours (from the audit log).
- Mail sent in the last 7 days by final status: delivered, bounced (hard and soft),
  blocked, spam, pending (no event yet).

### System card (existing endpoints, read in the browser)

- Version, commit and uptime from `/api/health`; ready or warming up from `/api/healthz`.
- MQTT sources from `/api/mqtt/status`: connected or not, and the age of the last message.
- Observers online and total from `/api/observers`.
- Links to the existing Perf page and MQTT status panel.

### Refresh

On open, with a "Refresh" button, and every 60 seconds while the tab is visible.

## Audit tab

### Logging logins (`cmd/server/auth_handlers.go`, through the existing `a.audit`)

- `user.login`: a successful login; target is the user.
- `user.login.failed`: a failed login for an existing account; target is that account;
  detail `reason` is `wrong_password`, `pending` or `disabled`.
- A failed login for an unknown address writes no row, so typed addresses of
  non-users are never stored.
- Logout is not logged.
- The login response stays identical in all failure cases (A's enumeration rule); the
  audit write happens after the response is decided and its failure is logged, not
  returned.

### Retention

The hourly janitor (`auth_service.go`) calls `PruneAudit([]string{"user.login",
"user.login.failed"}, 90 days)`. Other actions are kept.

### Store (`internal/users`)

- Schema v3: `CREATE INDEX audit_at ON audit_log(at)`.
- `AuditList(f AuditFilter) ([]AuditEntry, error)`, where `AuditFilter` has `Actions
  []string`, `UserID *int64` (matches actor or target), `From`, `To *time.Time`,
  `BeforeID int64` (keyset: only rows with `id < BeforeID`) and `Limit int` (default 100,
  max 500). Newest first.
- `PruneAudit(actions []string, maxAge time.Duration) (int64, error)`.
- `Stats(now time.Time) (Stats, error)`: the Users card figures as one typed struct.

### API

- `GET /api/admin/stats` → the `Stats` struct (typed, no `map[string]interface{}`).
- `GET /api/admin/audit?action=&user=&from=&to=&before=&limit=` → `{entries:[...],
  next: <id or null>}`. Each entry: id, time, action, actor and target as `{id,
  displayName, email}` looked up in `users`, or `{id, deleted: true}` when the account is
  gone, and `detail`. `action` accepts one action or a group prefix ending in `.*`
  (`user.login.*`). Invalid parameters give 400.
- Both routes: `withAdmin`, registered only when user management is on, OpenAPI entries
  under the `users` tag.

### Tab

Filters (action or group, user, period), a table newest first, "Load more" using
`next`. Filters live in the URL (`#/admin?tab=audit&action=user.login.failed&user=12`).
A user name opens that user in the Users tab.

## Admin area and navigation

- Page `admin`, tabs Overview (default), Users, Audit at `#/admin?tab=...`. Users keeps
  its filters (`status`, `role`, `q`, `id`, plus the new `bouncing`) next to `tab=users`.
- `#/admin/users?...` keeps working: it rewrites to `#/admin?tab=users&...` with
  `history.replaceState`.
- Account menu: "Users" becomes "Admin" (Overview). The account page button "Manage
  users" becomes "Admin". On phones the existing account entry in the More sheet and
  drawer leads to the account page, which carries the button.
- Non-admins opening the URL see "Admins only"; the server refuses their requests.

### Prepared for page restriction

The admin area adds no access mechanism of its own: it relies on the existing server-side
role check (`withAdmin`) and the client's `CSAuth.isAdmin()`. A later "page X only for
logged-in users or admins" can then be one router rule plus endpoint enforcement, without
changing this dashboard.

## Error handling

- A card whose source fails shows "Could not load" with a retry button; the other cards
  keep working.
- 401 and 403 follow the existing paths (logged out, or "Admins only").
- Invalid filters in the URL are ignored, the same way the Users tab's `readHash`
  validation does it.

## Security

- Both endpoints are admin-only and read-only.
- Every value rendered in HTML goes through `escapeHtml`, including audit details.
- No addresses of non-users are stored; no new personal data in the server log.

## Performance

`Stats` is a handful of `COUNT` queries on `users.db` (rows per account, no analyzer
data). The audit list is keyset-paginated with an index on `at` and on actor/target
(existing). The System card reuses endpoints that are already cached server-side. No
analyzer hot path is touched.

## Testing

**Go.**
- `internal/users`: `AuditList` filters (action, group prefix, user as actor or target,
  period) and keyset pagination; `PruneAudit` removes only the listed actions older than
  the age; migration v2 → v3 on an existing database and a fresh one; `Stats` against a
  fixed dataset with every counter checked.
- `cmd/server`: `/api/admin/stats` and `/api/admin/audit` (filters, limit cap, deleted
  user, 400 on bad parameters, 403 for a non-admin, 401 without session); a successful
  login writes `user.login`, a wrong password writes `user.login.failed` with the reason,
  an unknown address writes nothing, and the login responses stay identical; feature
  off: routes absent; OpenAPI completeness.

**Frontend unit** (vm, real modules). Attention items derived from fixed data;
tab and filter state to URL and back; the `#/admin/users` rewrite; XSS payload in audit
details and user names rendered inert; a failing source shows its error only in its
own card.

**Playwright** (e2etest build). An admin opens the overview and sees the user figures and
a failed login in "Needs attention", follows it to the audit tab and sees the row; the
old `#/admin/users` link lands on the Users tab. Axe on all three tabs.

## Amendments from the implementation plan (approved 2026-10-07)

These rules override the sections above where they differ.

1. **Registrations** in the Users card are counted from `user.register` audit rows, not
   from `users.created_at`: the janitor prunes pending accounts after 48 hours, so the
   users table forgets them.
2. **Mail buckets** add a sixth bucket, `other`, for error and unknown events.
3. **Admins** counts active admins only.
4. **The attention rules are computed by the server** and returned in the stats
   (`stuckPending`, `bouncing`, `guessing`); the browser only adds the MQTT rule from
   `/api/mqtt/status`. A source that never delivered a message counts as down. The MQTT
   link goes to `#/observers`, where the MQTT status panel lives.
5. **Audit entries** use `at` for the time, like the existing per-user audit JSON.
6. **`/api/healthz` is read on open and on "Refresh" only**, not on the 60-second timer:
   it walks every packet under a read lock. The timer refreshes `/api/admin/stats`,
   `/api/health`, `/api/mqtt/status` and `/api/observers`.
7. **Timing.** Login audit rows (`user.login` and `user.login.failed`) are written
   asynchronously after the response is decided, best-effort, so neither the extra write
   nor a locked `users.db` changes response timing. A row can be lost if the process
   stops right after a login.
