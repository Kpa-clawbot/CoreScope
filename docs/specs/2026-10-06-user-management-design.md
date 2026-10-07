# Optional User Management (Sub-project A: Foundation), Design Spec

**Date:** 2026-10-06
**Status:** Approved (design), implemented on this branch.
**Scope:** sub-project **A** of a five-part track (see *Roadmap*). B–E get their own
spec → plan → implementation cycle and build on A.

---

## Problem

CoreScope has exactly one notion of identity: a single shared `apiKey`, sent as
`X-API-Key`, that gates a handful of operator endpoints (`requireAPIKey`,
`cmd/server/routes.go:420`). Everything a visitor configures lives in their own
browser's `localStorage` (≈80 keys: own nodes, favorites, customizer delta, filters,
UI state). That model is deliberate and stays the default, but it repeatedly blocks
features that need a *person*:

- **Own nodes and favorites across devices.** #895 ("must manually claim and favorite
  all the nodes again"), #1765/#1767 (My Repeaters shipped "frontend-only" because
  there is no server-side starred set), #106, #381, #665, #2100.
- **Per-user preferences vs operator defaults.** The customizer has three layers
  (#502, #288). #1508 hides admin tabs, but that is cosmetic: "everything is
  browser-local localStorage state … doesn't actually prevent state mutation."
- **Alerts to a person.** #775 (alerting epic, closed unstaffed), #663 (battery
  thresholds with no delivery), #664/#666, #730.
- **Operator workflows in the UI.** #331 was closed because "auth model, audit log,
  write-validation all need design BEFORE" a server-write UI. #819 deferred
  server-side geofilter save for lack of auth. #2092 (suggested hashtag channels with
  admin review) was declined upstream for now, pending a general user-management
  design ("so that it is generally usable across CoreScope rather than built for one
  feature").

There is no session, cookie, CSRF, rate-limit or mail code anywhere (`public/app.js:1769`:
"Login — removed, no auth yet").

## Goals

1. An **optional** user system, **off by default**. With it off, CoreScope behaves
   and responds exactly as today. That includes routes, API payloads, UI and files
   on disk.
2. Self-registration with **email + password**, activated by a link sent to that
   address; activated accounts get role `user`.
3. Roles `user` and `admin`. Admins can manually activate, disable, enable, delete,
   promote and demote users, and see the delivery status of the mails sent to them.
4. A foundation that B–E build on without redesign: sessions, roles, an audit log,
   a mailer.

## Non-goals (this spec)

- Syncing `localStorage` to the profile (sub-project B).
- Admin dashboard or extra statistics (C), approval flows (D), alerts (E).
- Requiring login to *view* the dashboard (#1835). The dashboard stays public for
  anonymous visitors; logging in only adds features. Out of scope for this track.
- Channel keys (PSKs). They never go to the server: "Server Must NEVER Handle User
  Channel Keys" (#725). Changing that needs a separate, explicit design decision.
- OAuth/SSO, 2FA, magic-link login. Possible later; the schema doesn't preclude them.

## Decisions

| # | Question | Decision |
|---|---|---|
| 1 | Login method | Email + password, activation by mailed link, password reset by mailed link. |
| 2 | First admin | `adminEmails` in config: an account activated with a listed address becomes `admin`. Config admins can't be demoted, disabled or deleted from the UI. |
| 3 | Existing `apiKey` | Keeps working. Every endpoint gated today accepts `X-API-Key` **or** an admin session. |
| 4 | Dashboard visibility | Always public. Login is additive. |
| 5 | Storage | A separate SQLite file `users.db`, opened read-write by the server only, only when the feature is on. The measurement DB stays read-only to the server. |
| 6 | Profile | Email (private) plus a display name (shown to others in later sub-projects). |
| 7 | Mail | Brevo transactional API by default, behind a `Mailer` interface. Delivery events are recorded and shown to admins. |

### Why a separate `users.db` (decision 5)

The server opens the measurement DB with `mode=ro` (`cmd/server/db.go:107`), and
`cmd/server/readonly_invariant_test.go` enforces it (#1283). Three options were
weighed:

- **Ingestor-owned tables fed through a file queue** (like `internal/prunequeue`, or
  #2092's design). This keeps the invariant literally, but login, activation and
  password changes become asynchronous, and logging in would depend on the ingestor
  running. Rejected.
- **A JSON file** (like `SaveGeoFilter` rewriting `config.json`). No concurrency
  control or indexes; it breaks down once B and E add per-user rows. Rejected.
- **A separate SQLite file owned by the server.** Auth is synchronous, users are
  isolated from measurement data, and backing up or resetting one doesn't touch the
  other. Chosen.

The invariant is restated rather than dropped: **the server never writes
measurement data**. `users.db`, written only through `internal/users`, is the single
exception, and the invariant test is extended to pin exactly that.

## Configuration

A new optional block. If the block is absent or `enabled` is false, the feature is
off.

```json
"userManagement": {
  "enabled": false,
  "dbPath": "data/users.db",
  "adminEmails": ["operator@example.org"],
  "publicBaseUrl": "https://corescope.example.org",
  "sessionDays": 30,
  "trustedProxies": [],
  "mail": {
    "provider": "brevo",
    "brevoApiKey": "",
    "fromEmail": "noreply@example.org",
    "fromName": "CoreScope",
    "webhookSecret": ""
  }
}
```

- `dbPath` defaults to `users.db` in the same directory as the analyzer database when
  empty.

- The shape follows the existing opt-in pattern: a pointer sub-struct with
  `Enabled bool`, plus a nil-safe accessor where nil means off (`ClientRxCoverage`,
  `cmd/server/config.go:170,268-277`).
- **Off:** no routes are registered (requests fall through to the SPA page, 200 HTML,
  like any unknown path, rather than a router 404), `users.db` is
  not opened or created, and `/api/config/client` does not contain the
  `userManagement` field at all, so its payload is byte-identical to today.
- **On:** startup fails with a clear error unless all of the following hold:
  - `publicBaseUrl` is an absolute `http(s)` URL.
  - `mail.fromEmail` is set.
  - A Brevo API key is available.
  - `dbPath` is writable.

  A half-working setup where nobody can activate is worse than refusing to start.
- `publicBaseUrl` builds every link in every mail. The request `Host` header is never
  used, so a forged host can't poison a reset link.
- Secrets can come from the environment instead of `config.json`:
  `CORESCOPE_BREVO_API_KEY` and `CORESCOPE_BREVO_WEBHOOK_SECRET`. The environment
  value wins.
- `trustedProxies`: CIDRs whose `X-Forwarded-For` is trusted for rate limiting. When
  it is empty, the TCP peer address is used, with the same rule as the `/ws` limiter
  (`clientIP` in `cmd/server/ws_limits.go`): a loopback or private peer means clients
  cannot be told apart, so per-IP limits are off (see *Security*).
- `adminEmails` are compared case-insensitively after trimming.
- `config.example.json` gets the block with a `_comment` in the file's existing
  style.

## Components

Each unit has one job and can be tested on its own.

| Unit | Responsibility | Depends on |
|---|---|---|
| `internal/users` | `users.db`: open, migrate, CRUD for users, sessions, tokens, audit log, mail log. Password hashing. No HTTP. | `modernc.org/sqlite`, `golang.org/x/crypto/argon2` |
| `internal/mailer` | `Mailer` interface `Send(ctx, Message) (messageID string, err error)`. `Events(ctx, messageID) ([]Event, error)` (pull). A Brevo implementation and an in-memory fake for tests. | `net/http` |
| `cmd/server/auth_*.go` | HTTP handlers, session middleware, role checks, CSRF, rate limiting, mail templates, the Brevo webhook. | `internal/users`, `internal/mailer` |
| `public/account.js` | Login, register, activate, forgot, reset and "my account" views. The header account control. | existing helpers |
| `public/admin-users.js` | The admin user table and actions. | existing helpers |

New dependency: `golang.org/x/crypto`, a Go-team module. No new frontend
dependencies and no build step (AGENTS.md).

## Data model (`users.db`)

The schema has its own `schema_version` and forward-only migrations, independent of
`internal/dbschema`. It is opened in WAL mode with `busy_timeout`.

- **`users`**:
  - `id` INTEGER PK
  - `email` TEXT UNIQUE (stored lowercase, trimmed)
  - `display_name` TEXT (2–32 chars after trim, not unique, no control or bidi
    characters)
  - `password_hash` TEXT (argon2id, PHC string with parameters, so they can be
    raised later)
  - `role` (`user`|`admin`), `status` (`pending`|`active`|`disabled`)
  - `created_at`, `activated_at`, `last_login_at`
  - `activated_by` (nullable admin user id; null means activated by link)
  - `email_bouncing` BOOLEAN, set on a hard bounce
- **`sessions`**:
  - `token_hash` PK (SHA-256 of a 256-bit random token; the raw token exists only in
    the cookie)
  - `user_id`, `csrf_token`
  - `created_at`, `expires_at`, `last_seen_at`
  - `user_agent` (truncated)
- **`tokens`**: one-time links.
  - `token_hash` PK, `user_id`
  - `purpose` (`activate`|`reset`|`email_change`), `new_email` (for `email_change`)
  - `expires_at`, `used_at`
  - Lifetimes: activate 48 h, reset 1 h, email change 24 h.
- **`audit_log`**:
  - `id`, `at`, `actor_user_id` (nullable; null means the system or the API key)
  - `action`, `target_user_id`, `detail` (JSON)
  - Rows outlive deleted users; they then show as "deleted user #N".
- **`mail_log`**:
  - `id`, `user_id` (nullable after delete), `to_email`
  - `purpose`, `provider_message_id`, `sent_at`
  - `last_event`, `last_event_at`, `last_reason`
  - It is pruned after 90 days.
- **`mail_events`**:
  - `mail_id`, `event`, `at`, `reason`
  - The full history behind `mail_log.last_event`, pruned with it.

## Flows

All request and response bodies are JSON. Error responses use the server's existing
error shape.

### Register and activate

1. `POST /api/auth/register {email, displayName, password}`. The server validates:
   - Email syntax, at most 254 chars.
   - Password: 10–128 chars, no composition rules (NIST 800-63B).
   - Display name: per the rule above.
2. A user is created with status `pending` and an activation mail is sent with
   `{publicBaseUrl}/#/account/activate?token=…`.
3. If the address is already registered, the response is **identical** to the
   success case ("check your mail"). This prevents account enumeration. What gets
   mailed depends on the account:
   - An active or disabled account gets a mail: "someone tried to register with your
     address; if it was you, log in or reset your password."
   - A still-pending account stores the newest password and display name and gets a
     fresh activation link instead (newest registration wins). A squatter therefore
     cannot activate the owner into the squatter's password.
4. `POST /api/auth/activate {token, password}`:
   - The token is checked without being consumed. The password must match the pending
     account's password. A wrong password answers 401, the link stays usable, and
     attempts are rate-limited per IP and per account.
   - Then, in one transaction, it consumes the token, sets the status to `active` and
     sets the role to `admin` if the address is in `adminEmails`, and starts a session.
     The transaction applies only while the account is still pending with the password
     hash that was checked; if a re-register (or an admin) changed it in between, it
     answers 409 "account changed, try again" and the link is not consumed.
5. Pending accounts with an expired token are pruned periodically.
6. The admin action "resend activation" issues a fresh token and invalidates the old
   one.

### Log in and log out

- `POST /api/auth/login {email, password}`. It succeeds only for `active` users.
  - Any failure (unknown address, wrong password, pending, disabled) returns the same
    401 message after a constant-cost hash comparison. For an unknown address the
    server compares against a dummy hash.
  - Success sets the cookie `cs_session`: `HttpOnly`, `SameSite=Lax`, `Path=/`.
    `Secure` is set when `publicBaseUrl` is https.
  - The session expires after `sessionDays`. It is extended (sliding) when
    `last_seen_at` is older than one day.
- `POST /api/auth/logout` (checked by `Origin` only, see *Security*) deletes the session row and clears the cookie.
- `GET /api/auth/me` returns `{id, email, displayName, role, csrfToken}`, or 401.

### Forgot and reset the password

- `POST /api/auth/forgot {email}`. The response is always the same. A mail is sent
  only if an `active` account exists.
- `POST /api/auth/reset {token, password}` sets the password, consumes the token and
  **ends all sessions** of that user. It also ends outstanding email-change links.

### My account

- `PATCH /api/account {displayName?}`.
- `POST /api/account/password {currentPassword, newPassword}`. It ends all other
  sessions and ends outstanding email-change and reset links.
- `POST /api/account/email {newEmail, currentPassword}`. A confirmation link goes to
  the new address, and the change applies only after the click. The request is
  rate-limited per user and per new address. The old address is always told that a
  change was requested, also when the new address is already taken (no enumeration).
- `GET /api/account/sessions` lists the sessions as device and last seen.
  `DELETE /api/account/sessions/{id}` revokes one.
- `DELETE /api/account {currentPassword}` deletes the account, its sessions and
  tokens. Mail-log rows keep only the hashed address. Config admins can delete their
  account; activating again with the same address makes them admin again. The last
  admin can't delete their own account (409); they promote someone else first.

### Admin user management

All of these require role `admin` and live under `/api/admin/users`.

- `GET /api/admin/users?status=&role=&q=` returns users with their last mail status
  (`last_event`, `last_reason`, `email_bouncing`).
- `GET /api/admin/users/{id}` returns the user, their sessions (count, last seen),
  their mail log with events, and the audit entries that concern them.
- `POST …/{id}/disable` works only for active accounts (for a pending one, delete or
  activate instead). It sets the status to `disabled`, ends all their sessions
  immediately and ends all their outstanding links. `POST …/{id}/enable` reverses it.
- `DELETE …/{id}` is a hard delete, the same as self-delete.
- `POST …/{id}/role {role}`. These guards apply:
  - A config admin (an account that is admin **and** listed in `adminEmails`) can't be
    demoted, disabled or deleted. A pending account on a listed address can be deleted.
  - A role change on a pending account answers 409.
  - The last admin can't be demoted, disabled or deleted.
  - An admin can't disable or delete themselves; they use "my account" for that.
- `POST …/{id}/resend-activation` works only for pending users.
- `POST …/{id}/activate` manually activates a pending user, for when mail keeps
  failing (bounces, spam filters, Brevo outage).
  - It sets the status to `active` and `activated_at`, applies the `adminEmails` role
    rule exactly like link activation, and invalidates any outstanding activation
    tokens.
  - The address is then **not verified** by the user. The user row records
    `activated_by` (the admin's id) and the audit log records
    `user.activate.manual`, so this stays visible in the admin views.
  - No session is created. The user logs in with the password they chose at
    registration, or uses "forgot password" once mail works.
- `POST …/{id}/mail/{mailId}/refresh` pulls the events for that message from the
  provider API and writes the audit row `user.mail.refresh`.
- Every action writes an `audit_log` row.

### Mail delivery status (Brevo feedback)

- On send, the provider's `messageId` is stored in `mail_log`.
- **Webhook:** `POST /api/mail/brevo/webhook`. Brevo is configured to call it for
  these transactional events: `delivered`, `opened`, `click`, `soft_bounce`,
  `hard_bounce`, `invalid_email`, `deferred`, `spam`, `blocked`, `error`.
  - Each event is appended to `mail_events` and updates `mail_log.last_event` and
    `last_reason`.
  - `hard_bounce` and `invalid_email` set `users.email_bouncing`. The account is not
    disabled automatically; the admin decides.
  - The endpoint is authenticated with `webhookSecret`. If it is unset, the endpoint
    is not registered.
  - Brevo's webhook object supports `"auth": {"type": "bearer", "token": …}`
    (verified 2026-10-06), so the webhook is created with bearer auth. The server
    checks `Authorization: Bearer <webhookSecret>` in constant time. The secret
    must be at least 16 characters.
  - Unknown message IDs are ignored with 200, so Brevo doesn't retry forever. Bodies
    are size-capped.
- **Pull fallback:** for instances Brevo can't reach, the admin "refresh" action
  calls Brevo's transactional events API for that `messageId`. The same ingestion
  code updates `mail_events`.
- **UI:** the admin table shows a status chip per user: sent, delivered, opened,
  bounced, blocked or spam, with the reason in a tooltip. The detail view shows the
  timeline. "Opened" carries a note: tracking pixels are auto-loaded by some clients
  (Apple Mail Privacy Protection) and blocked by others, so the value is indicative
  only.
- Users never see the mail log. The webhook payload is not stored raw; only the
  event, the time and the reason are kept.

### Existing API-key endpoints

`requireAPIKey` becomes `requireAdmin`, which accepts **either** a valid strong
`X-API-Key` (exactly today's checks, `routes.go:420-437`) **or** a session with role
`admin` that passes the CSRF check. When the feature is off, only the API-key path
exists and the behavior is unchanged. In the UI, the customizer's geofilter tab hides
its API-key field when an admin is logged in, and `perf.js` sends the CSRF token on
`POST /api/perf/reset`. That fixes `perf.js:347`, which never sent credentials, for
logged-in admins.

## Security (cross-cutting)

- **CSRF:** every state-changing request authenticated by cookie must carry:
  - an `Origin` header (or, without one, a `Referer`) whose origin equals
    `publicBaseUrl`'s origin, **and**
  - a header `X-CS-CSRF` equal to the session's `csrf_token`.

  The exception is logout: it is checked by `Origin` only, without `X-CS-CSRF`,
  because a forced logout is low impact.

  Requests authenticated by `X-API-Key` are exempt, because they carry no ambient
  credential. CORS stays as it is: no credentialed CORS, and
  `corsAllowedOrigins` doesn't widen auth.
- **Rate limits:** in-memory token buckets keyed by client IP and by normalized
  email.
  - Login: 10 per 15 min.
  - Register, forgot and resend: 5 per hour.
  - Webhook: generous and per-IP only.

  The client IP is taken from `X-Forwarded-For` only when the peer is in
  `trustedProxies`. A limit returns 429 with `Retry-After`.

  With `trustedProxies` empty, the rule of the `/ws` limiter applies
  (`clientIP`, `cmd/server/ws_limits.go`). A loopback or private TCP peer means IPs
  cannot be told apart, so per-IP limits are off. Per-address and per-account keys
  still apply, and a startup warning says so. The webhook limiter is per-IP only, so
  it is off too. The buckets are capped at 100000 keys. At the cap, new keys are
  refused until buckets refill.
- **Logs:** tokens, passwords, password hashes and cookies are never logged.
  Addresses are logged only in audit and mail tables, not in the server log.
  Mail-provider error texts are redacted (email addresses replaced) before logging.
- **Headers:** auth responses send `Cache-Control: no-store`.
- **Output:** all user-supplied strings (display name, email) are rendered through
  the existing escape helpers (`test-xss-escape-sinks.js`, `test-preflight-xss-gate.js`).
- **WebSocket:** unchanged and public. Sessions add nothing to `/ws` in A.

## Frontend

- **Feature detection:** `/api/config/client` gains `userManagement: {enabled: true}`
  only when the feature is on. If the field is absent, the frontend adds no UI and
  makes no extra requests. That makes "off" pixel-identical and keeps older servers
  safe.
- **Header control:** "Log in" at the right of the top nav. When logged in it shows
  the display name with a menu: My account, Users (admins only), Log out. On phones
  (<=768px) the top-bar control is hidden, so a conditional "Log in" / "My account"
  entry is in the bottom-nav More sheet and the nav drawer. Icons are Phosphor, as elsewhere
  (#1648).
- **Routes:**
  - `#/account/login`, `#/account/register`, `#/account/activate`, `#/account/forgot`,
    `#/account/reset`, `#/account/confirm-email`
  - `#/account`: profile, password, email, sessions, delete
  - `#/admin/users`: a table ordered newest first by the server (no table-sort), with
    status and role filters, search, a detail panel, and `confirm()` dialogs for
    destructive actions. The filters and the open detail are deep-linked
    (`#/admin/users?status=&role=&q=&id=`).
- **State:** on load, if the feature is on, the frontend calls `GET /api/auth/me`
  once. The result is kept in memory (`window.CS_USER`). A 401 from any authed call
  clears it and shows a "you were logged out" toast. The rest of the page keeps
  working anonymously.
- **Language and mails:** the UI is English, like the rest of CoreScope. Mails are
  English, sent as both HTML and text. The subject prefix and the sender
  name come from `userManagement.mail.fromName` (default "CoreScope"), not the
  branding config.
- **Accessibility:** the new views must pass the existing axe and contrast tests and
  the touch-target rules.

## Error handling

| Situation | Behavior |
|---|---|
| Brevo send fails at register | 503 "mail could not be sent, try again later". The user row is rolled back, so a retry works. The error is logged without the token. |
| Brevo send fails at forgot, resend or email change | 503. The token is invalidated. |
| `users.db` can't be opened or migrated | The server refuses to start, with the path and cause. Measurement data is untouched. |
| Expired session, or the user was disabled or deleted | 401. The UI clears its user state and shows a toast. |
| Expired or used token | 410 with a clear message and a "send a new link" action. |
| Webhook with bad auth | 401. Nothing is written. |
| Config admin removed from `adminEmails` | They keep the `admin` role they have, but lose the config protection. Startup logs this as a note. |

## Testing

- **`internal/users` (Go unit):**
  - Argon2id hash round-trip and parameter parsing.
  - The token lifecycle: expiry, single use, purpose mismatch.
  - Status and role transitions, and the last-admin and config-admin guards.
  - Migration from an empty file. Pruning.
- **`internal/mailer`:** the Brevo client against an `httptest` server: the request
  shape, the API-key header, error mapping, and events parsing. The fake mailer
  records messages for the HTTP tests.
- **HTTP (`cmd/server`, fake mailer):**
  - The full register → mail → activate → login → me → logout flow.
  - Identical enumeration responses for register and forgot.
  - Reset ends sessions. Disable ends sessions.
  - Manual activation: only for pending users, invalidates the activation token,
    records `activated_by` and an audit row, and applies the `adminEmails` rule.
  - Rate limits and 429, including `trustedProxies` handling.
  - CSRF rejection: wrong origin, missing header, wrong token.
  - The admin-only routes return 403 for `user` and 401 for anonymous requests.
  - Gated endpoints accept the API key or an admin session.
  - The webhook: auth, unknown IDs, and event ingestion setting `email_bouncing`.
- **"Off is unchanged":**
  - With the block absent or `enabled: false`: no new route is registered (the unit
    test's API-only router answers 404; the real server falls through to the SPA
    page) and no `users.db` file is created. `/api/config/client` has no
    `userManagement` key, and
    it is byte-identical between "block absent" and `enabled: false`.
  - All existing tests pass unchanged.
- **Read-only invariant:** `readonly_invariant_test.go` is extended. Write-capable
  opens are allowed only inside `internal/users`, and only for the configured
  `dbPath`. All existing prohibitions on the measurement DB stay.
- **Playwright E2E** (server started with the feature on, the fake mailer, and a
  test-only endpoint that returns the last mail's link, compiled in only under a
  test build tag):
  - Register, activate, log in and log out.
  - An admin disables a user, and that user's next action shows "logged out".
  - axe on the new views.
  - With the feature off: no login control.

## Documentation

- `config.example.json`: the block plus `_comment`.
- `docs/user-guide/accounts.md`:
  - For operators: enabling the feature, a Brevo free account (sender domain
    verification, API key, webhook setup), the first admin, and backing up
    `users.db`.
  - For users: registering, logging in, and deleting an account.
- `docs/api-spec.md` and OpenAPI (`cmd/server/openapi.go`): the new endpoints, a
  `cookieAuth` security scheme next to `apiKey`.
- `AGENTS.md`: restate the invariant as "the server never writes measurement data;
  `users.db` via `internal/users` is the single exception (user management, opt-in)".
- `README.md` and `AGENTS.md` say "all API endpoints are public, no auth required".
  Those lines describe the public live instance (analyzer.00id.net), which does not
  enable the feature, so they stay unchanged.

## Roadmap (after A)

Each item gets its own spec. The order can change.

- **B. Settings sync.** Profile-stored own nodes, favorites, the customizer delta and
  selected filters and UI state. Merge rules for "local vs profile" on login. Explicit
  exclusion of channel keys and decrypted-message caches (#725).
- **C. Admin dashboard and statistics.** User stats, the audit-log viewer, and
  admin-only views of perf, health and MQTT status. Server-enforced customizer tab
  restrictions (#1508).
- **D. Approval flows.** A generic proposal/review mechanism, with #2092 (suggested
  hashtag channels) as its first consumer, attributing proposals to display names.
- **E. Notifications.** Per-user subscriptions to node events (offline, battery,
  foreign advert; #775, #663, #730), delivered via the mailer.
