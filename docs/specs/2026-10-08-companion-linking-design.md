# Companion Linking (User Management, Sub-project F), Design Spec

**Date:** 2026-10-08
**Status:** Approved (design). Not implemented.
**Scope:** sub-project **F** of the optional user-management track. Builds on A
(accounts, sessions), B (settings sync, `meshcore-my-nodes`) and E (notifications).
The client side lives in CoreDrive RX (`efiten/coredrive-rx`,
`docs/superpowers/specs/2026-10-08-corescope-login-design.md`).

---

## Problem

CoreDrive RX companions publish coverage under `meshcore/client/<pubkey>/packets`
with one shared broker account. CoreScope knows a companion only by that pubkey.
With user management on, a logged-in user wants:

1. **Ownership.** Every companion they drive with shows up under their "My nodes",
   so the existing watch and notification features apply to it.
2. **Attribution.** The coverage their companions collected is theirs: a
   "My coverage" view, and their linked companions listed on the account page.

Two things block this today:

- Nothing ties a pubkey to a user. `meshcore-my-nodes` is a free-form synced list;
  anyone can put any pubkey in it, and two users can both "own" the same one. That
  is fine for favourites but not for attributing data.
- RX is a separate app, often on another origin. Sessions are cookies with CSRF, and
  `corsMiddleware` deliberately allows no credentialed CORS (`cors.go:44`). Cookies
  across origins are also unreliable on iOS Safari, which is where people drive.

## Decisions

| # | Topic | Decision |
|---|---|---|
| 1 | Auth for RX | A **device token** (bearer), issued by email + password login. One code path for same-origin and cross-origin deployments. No cookies, no CSRF. |
| 2 | Token storage | A row in the existing `sessions` table with `kind = 'device'`, so it shows under **Devices** on the account page and is revoked the same way. |
| 3 | Token scope | Narrow and server-enforced: `/api/auth/me`, `/api/auth/logout`, `/api/account/companions*`, `/api/account/settings`. Everything else answers 403 to a bearer token. |
| 4 | Ownership proof | The companion signs a server challenge with its own Ed25519 identity key (MeshCore companion `CMD_SIGN_*`). No signature, no link. |
| 5 | Conflicts | The newest valid proof wins. Whoever holds the key owns the companion (devices change hands). The previous owner gets an audit row and a mail; the mail is a security notice, sent whatever the node notification settings are (not to an inactive account or a bouncing address). |
| 6 | Attribution | A read-time join `client_receptions.rx_pubkey → companion_links`. All coverage of a linked companion counts for its current owner, including rows from before the link. The ingestor and the RX payload contract do not change. |
| 7 | My nodes | Linking also adds the pubkey to the user's `meshcore-my-nodes`. Unlinking does **not** remove it: that list is the user's own choice. |
| 8 | Linked-only ingest | Opt-in admin setting `clientRxCoverage.requireLinkedCompanion`. When it is on, the ingestor drops every `meshcore/client/<pubkey>/…` message whose pubkey is not in `companion_links`. It is a filter, not a security boundary (see *Linked-only ingest*). |
| 9 | Per-user API keys | Out of scope. The token table gets a `kind` and `scopes` so a later `api_key` kind (named, shown once, own scopes, revoked separately) is a small addition. The RX device token is never reused for external access. |

## Non-goals

- Per-user publish credentials or broker ACLs (a possible later sub-project; it
  would change the RX payload contract).
- Per-user API keys for external tools (see decision 8).
- Showing other users who owns a companion. Links are private to their owner and
  to admins.

## Data model (`users.db`, schema v6)

```sql
ALTER TABLE sessions ADD COLUMN kind TEXT NOT NULL DEFAULT 'web'
  CHECK (kind IN ('web','device'));
ALTER TABLE sessions ADD COLUMN label TEXT NOT NULL DEFAULT '';   -- device name
ALTER TABLE sessions ADD COLUMN scopes TEXT NOT NULL DEFAULT '';  -- '' = full (web)

CREATE TABLE companion_links (
  pubkey       TEXT PRIMARY KEY,            -- 64 lowercase hex
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  linked_at    INTEGER NOT NULL
);
CREATE INDEX companion_links_user ON companion_links(user_id);

CREATE TABLE link_challenges (
  challenge_hash TEXT PRIMARY KEY,          -- SHA-256 of the raw challenge
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  pubkey         TEXT NOT NULL,
  expires_at     INTEGER NOT NULL
);
```

Device tokens reuse the session token format (256-bit random, stored as SHA-256).
`csrf_token` is filled but unused for `kind = 'device'`. Expiry is 90 days, sliding
on use, like web sessions but with its own constant. The janitor prunes expired
challenges with the other expired rows.

"Last seen" for a companion is not stored: the account page reads the newest
`client_receptions` row for that pubkey from the analyzer database.

## API

All routes exist only when `userManagement.enabled`, and answer 404 otherwise, like
the rest of A–E. Rate limits use the existing token buckets (per IP and per user).

### Device token

- `POST /api/auth/device-token {email, password, deviceName}`
  → `200 {token, expiresAt, user: {id, displayName}}`.
  The same constant-cost failure path, rate limits and login audit row as
  `/api/auth/login`. `deviceName` is trimmed, control characters are stripped, and
  it is capped at 64 characters.
- Authentication: `Authorization: Bearer <token>`. `withUser` accepts either a
  cookie session (with the CSRF check for writes) or a bearer token (no CSRF,
  scope check). A bearer token on a route outside its scope answers 403. The
  bearer header only counts on a request without the `cs_session` cookie: with
  the cookie, the cookie decides, so an auth proxy that adds its own
  `Authorization: Bearer` does not break browser logins. RX therefore sends its
  API calls with `credentials: 'omit'`.
- `POST /api/auth/logout` with a bearer token revokes that device row.
- `GET /api/account/sessions` returns `kind` and `label`. The **Devices** list shows
  a device token as "CoreDrive RX – <label>" with its last use, and the existing
  "log out" button revokes it.

### Companions

- `POST /api/account/companions/challenge {pubkey}` → `200 {challenge, expiresAt, host}`. `host` is the host of `userManagement.publicBaseUrl`; the client signs it as-is, so a client that reaches CoreScope under another name still signs what the server verifies.
  32 random bytes as hex, valid for 5 minutes, single use, bound to user and pubkey.
- `POST /api/account/companions {pubkey, challenge, signature, name}`:
  1. Look up the challenge by hash. Missing, expired, or bound to another user or
     pubkey → 410. It is consumed in every case.
  2. Verify `signature` (64 bytes hex, Ed25519) over the UTF-8 message
     `"corescope-link:" + host + ":" + challenge`, where `host` is the host of
     `userManagement.publicBaseUrl`. This uses `internal/sigvalidate`. Invalid → 400.
  3. Upsert `companion_links`. If the pubkey belonged to another user, write an
     audit row for both users, and mail the previous owner (a security notice:
     node notification settings do not apply; no mail to an inactive account or
     a bouncing address).
  4. Add `{pubkey, name, addedAt}` to the user's `meshcore-my-nodes`, unless it is
     already there. This is a server-side read-modify-write of the settings document
     that bumps its revision, so open web clients pick it up through the normal
     B conflict flow. If the document is at the size cap, the link still succeeds
     and the response says `myNodes: "full"`.
  5. `200 {pubkey, name, linkedAt, myNodes: "added" | "present" | "full"}`.
- `GET /api/account/companions` → `[{pubkey, name, linkedAt, lastSeenAt}]`.
- `DELETE /api/account/companions/{pubkey}` → 204. Leaves `meshcore-my-nodes` alone.
- Admin: `GET /api/admin/users/{id}` gains `companions`. The audit kinds are
  `companion_link`, `companion_unlink` and `companion_transfer`.

### Coverage attribution

- `GET /api/rx-coverage?mine=1` filters to the caller's linked companions. It needs a
  session; without one it answers 401. The existing response shape is unchanged.
- The coverage page shows a "My coverage" toggle only to logged-in users.

### Linked-only ingest

- Config: `clientRxCoverage.requireLinkedCompanion` (bool, default false). It only
  takes effect with `userManagement.enabled`. If user management is off, the server
  and the ingestor log a startup warning and ignore the setting.
- Ingestor: before any client handler runs (packets, rf, regions), it checks the
  topic pubkey against an in-memory set of linked pubkeys read from `users.db`.
  - The set refreshes every 60 s, which picks up unlinks.
  - On a miss, the ingestor re-reads the set at most once per 5 s before dropping.
    A companion that was just linked is therefore accepted within seconds, and
    an RX client can publish right after a successful link.
  - Dropped messages are counted per reason in the ingestor stats file
    (`client_unlinked_dropped`), never logged per message.
- `/api/config/client` gains `clientRxRequireLinkedCompanion: true` when the setting
  is in effect. The field is omitted otherwise. RX reads it to hold its queue
  instead of publishing data that would be dropped.
- **It is not a security boundary.** All RX clients share one broker account, so
  anyone with that password can publish under a linked pubkey. The setting keeps
  honest unlinked clients out. Real enforcement needs per-user publish credentials
  (the later "publish token" sub-project).
- Unlinking does not delete coverage that is already stored.

### CORS

For origins in `corsAllowedOrigins`, and only on the bearer-scoped routes above, the
preflight also allows `POST, PUT, DELETE` and the `Authorization` and `Content-Type`
headers. `Access-Control-Allow-Credentials` stays off. Same-origin needs nothing.

## Account page (`public/account.js`)

- **Devices:** device tokens render with their label and a device marker; nothing
  else changes.
- A new **Companions** section below Devices lists name, short pubkey, linked since
  and last seen, each with an **Unlink** button. When the list is empty, it explains
  that companions are linked by logging in from CoreDrive RX.

## Error handling

| Case | Answer |
|---|---|
| Feature off | 404 on every route above. |
| Wrong credentials | 401, the same message as `/api/auth/login`. |
| Bearer token outside its scope | 403. |
| Expired or revoked token | 401. RX clears the token. |
| Challenge missing, expired or used | 410. |
| Bad signature or malformed pubkey | 400. |
| Rate limited | 429 with `Retry-After`. |

Tokens, challenges and signatures are never logged.

## Testing

- `internal/users`: the v6 migration; the device-session kind, label and scopes;
  sliding expiry; the challenge lifecycle (expiry, single use, binding); the link
  upsert and transfer; cascade on user delete.
- `cmd/server`:
  - Device-token issue: success, the constant-cost failure, the rate limit, the audit row.
  - Bearer scope: allowed routes pass, others answer 403, and no CSRF is needed.
  - Linking with a real MeshCore key and signature fixture: success, wrong host in
    the message, reused challenge, a challenge bound to another pubkey.
  - Transfer between two users: the audit rows and the mail.
  - The `meshcore-my-nodes` merge: keeps existing items, skips duplicates, reports
    `full` at the cap.
  - `rx-coverage?mine=1`.
  - CORS headers only for allowlisted origins and only on scoped routes.
  - Everything answers 404 with the feature off.
- `cmd/ingestor`: with `requireLinkedCompanion` off, nothing changes. With it on:
  linked pubkeys pass, unlinked pubkeys are dropped and counted, a link made after
  the last refresh is accepted through the miss refresh, the 5 s cap holds under a
  flood of unknown pubkeys, an unlink is honoured after the periodic refresh, and
  user management off means the setting is ignored with a warning.
- `/api/config/client`: the field is present only when the setting is in effect.
- e2e: log in on the account page, see a seeded device token under Devices and a
  linked companion under Companions, then unlink it.

## Rollout

Opt-in through `userManagement.enabled`, like A–E. Deployments where RX runs on
another origin add that origin to `corsAllowedOrigins`. An RX build without account
support is unaffected, **except** under `requireLinkedCompanion`: then its data is
dropped. Turn that setting on only once the RX clients in use support linking.
