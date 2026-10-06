# Optional User Management, Sub-project B: Settings Sync, Design Spec

**Status:** approved in conversation 2026-10-06, written for review.
**Builds on:** sub-project A, `docs/specs/2026-10-06-user-management-design.md`
(accounts, sessions, `users.db`, `withUser`, CSRF). Roadmap entry "B. Settings sync".
**Branch:** `feat/user-settings-sync`, on top of `feat/user-management`.

## Problem

A gives a person an account, but nothing the account holds is useful to a regular
user yet. Everything a user sets up lives in the browser's `localStorage`: own nodes,
favorites, the customizer delta, saved filters and per-page view preferences. A new
browser, a phone or a cleared cache starts from zero (#895: "must manually claim and
favorite" on every device).

## Goals

1. A logged-in user's settings follow them: logging in on another device restores
   them, and later changes reach the other devices within about a minute (or when the
   user returns to the tab).
2. Nothing is lost: a node or favorite added on one device is never dropped by a sync
   from another, and a removal on one device is not undone by another.
3. With user management off, or when logged out, nothing changes: no requests, no
   interception, no UI.
4. Channel keys, channel labels and decrypted-message caches never leave the browser
   (#725). The admin API key never leaves the browser.

## Non-goals

- Realtime push between devices (WebSocket). "Within a minute or on tab focus" is enough.
- Syncing device-specific state: panel and column widths, collapsed panels, map
  positions, gesture hints, geofilter drafts.
- Server-enforced operator defaults or customizer restrictions (sub-project C).
- Sharing settings between users.

## Decisions (from the design conversation)

| # | Topic | Decision |
|---|---|---|
| 1 | Purpose | Both: restore on login (backup) and ongoing sync between devices. |
| 2 | Scope | Core (own nodes, favorites, customizer/theme, saved filters) plus per-page filter and view preferences (about 55 keys). Layout and map positions are excluded as device-specific. |
| 3 | Merge on login | First login with an empty profile uploads this device. Lists merge (nothing is ever lost). Scalar settings: the profile wins unless only this device changed it. |
| 4 | Logout | Ask: "Keep my settings on this device" (default) or "Remove my settings from this device". |
| 5 | Mechanism | One sync module intercepting `localStorage` writes for allowlisted keys (approach A). Existing modules stay unchanged. |

## Architecture

```
public/settings-sync.js   intercept allowlisted localStorage writes, merge, push/pull
        │  GET/PUT /api/account/settings  (withUser: session + CSRF)
cmd/server/settings_handlers.go   allowlist/denylist check, size cap, rate limit
        │
internal/users  (users.db)  table user_settings: one JSON document per user + revision
```

No analyzer-DB write path is added: the document lives in `users.db`, the server's
only write path (AGENTS.md read/write invariant).

### Storage (`internal/users`)

Schema migration v1 → v2 (the existing forward-only `migrations` list):

```
CREATE TABLE user_settings (
  user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  doc        TEXT    NOT NULL,
  revision   INTEGER NOT NULL,
  updated_at TEXT    NOT NULL
)
```

Store methods:
- `GetSettings(userID) (doc string, revision int64, err)`: revision 0 and an empty
  doc when no row exists.
- `PutSettings(userID, baseRevision int64, doc string) (newRevision int64, err)`:
  in one transaction, writes only when the stored revision equals `baseRevision`
  (0 = no row yet); otherwise returns `ErrSettingsConflict`. New revision = base + 1.
- `DeleteSettings(userID) error`.
- Account deletion removes the row (cascade, and `Store.Delete` covers it explicitly
  in a test). Disabling keeps it.

### Document

```json
{"v": 1, "keys": {"meshcore-favorites": "<raw localStorage string>", "...": "..."}}
```

Values are the raw `localStorage` strings. The server does not parse them, so adding
a key to the allowlist needs no server-side parsing code. Maximum serialized size
256 KiB.

### Allowlist (single source: the server)

The server owns the list. `GET /api/account/settings` returns it with the document, so
client and server cannot drift. Each entry has a kind:

- `set`: a JSON array merged per item. The identity of an item is given per key.
- `scalar`: any string, merged as a whole.

| Key | Kind (identity) |
|---|---|
| `meshcore-my-nodes` | set (`pubkey`) |
| `meshcore-favorites` | set (the string itself) |
| `corescope_saved_filters_v1` | set (`name`) |
| `cs-theme-overrides`, `meshcore-theme`, `meshcore-cb-preset`, `mc-dark-tile-provider`, `mc-light-tile-provider`, `meshcore-distance-unit`, `meshcore-heatmap-opacity`, `meshcore-live-heatmap-opacity`, `live-channel-colors` | scalar |
| Packets: `meshcore-observer-filter`, `meshcore-type-filter`, `meshcore-time-window`, `meshcore-hex-hashes`, `meshcore-full-names`, `meshcore-obs-sort`, `meshcore-packets-sort` | scalar |
| Tables: `meshcore-nodes-sort`, `meshcore-observers-sort`, `meshcore-scope-audit-sort`, `meshcore-channel-sort` | scalar |
| Nodes: `meshcore-nodes-last-heard`, `meshcore-nodes-status-filter`, `meshcore-nodes-silent-for` | scalar |
| Region/area: `meshcore-area-filter`, `meshcore-region-filter`, `mc-region-show-all-nodes`, `meshcore-hide-1byte-hops`, `channels-show-encrypted` | scalar |
| Map: `meshcore-map-clustering`, `meshcore-map-heatmap`, `meshcore-map-hash-labels`, `meshcore-map-multibyte-overlay`, `meshcore-map-scope-overlay`, `meshcore-map-status-filter`, `meshcore-map-scope-filter`, `meshcore-map-byte-filter`, `meshcore-map-region-filter`, `meshcore-map-geo-filter`, `meshcore-top-routes-axis`, `meshcore-top-routes-n` | scalar |
| Analytics: `subpath-hide-collisions`, `meshcore-repeater-scatter-x`, `meshcore-repeater-scatter-y`, `ng-min-score` | scalar |
| Home: `meshcore-user-level` | scalar |
| Live: `meshcore-live-heatmap`, `live-ghost-hops`, `live-realistic-propagation`, `live-favorites-only`, `live-multibyte-only`, `live-matrix-mode`, `live-matrix-rain`, `meshcore-color-packets-by-hash`, `live-node-filter`, `live-vcr-speed`, `live-audio-voice`, `live-audio-enabled`, `live-audio-bpm`, `live-audio-volume` | scalar |

The implementation plan re-checks this list against `public/` (keys built from
constants are easy to miss) and pins it with a test that fails when a listed key no
longer occurs in `public/`.

**Never synced, enforced by a hard denylist checked before the allowlist:**
`corescope_channel_keys`, `corescope_channel_labels`, `corescope_channel_cache`, any
`corescope_channel_*` key, and `meshcore-api-key`. A server test pins that these are
refused even if added to the allowlist.

**Deliberately not synced (device-specific or transient):** `panel-drag-*`,
`panel-corner-*`, sidebar and column widths, `*-col-widths`, `packets-visible-cols`,
`packets-known-cols`, collapsed and hidden panel flags, `live-fullscreen`,
`live-nav-pinned`, `map-view`, `live-map-view`, `rx-coverage-view`, `geofilter-draft`,
`meshcore-gesture-hints-*`, `meshcore-affinity-debug`, the legacy v1 customizer keys,
all `sessionStorage`.

### Server API

Both routes are registered only when user management is on, behind `withUser`.

- `GET /api/account/settings` → `200 {revision, doc, allowlist}`. `doc` is `null` at
  revision 0.
- `PUT /api/account/settings` body `{baseRevision, doc}`:
  - `200 {revision}` on success.
  - `409 {revision, doc}` when `baseRevision` is stale; the body carries the current
    document so the client can merge without another GET.
  - `400` when `doc` is not the documented shape or contains a key that is denylisted
    or not allowlisted.
  - `413` when the serialized `doc` exceeds 256 KiB.
  - `429` from a per-user limiter (60 per hour).
- `DELETE /api/account/settings` → `200`; removes the document.
- No audit row for settings reads and writes (not a security event, and each favorite
  click would flood the log). The server log carries `#id` and sizes only, never
  document content.
- OpenAPI entries under the `users` tag.

### Client sync module (`public/settings-sync.js`)

Loaded after `auth.js`. Inert unless `window.MC_USER_MGMT` is on and `CSAuth` reports
a logged-in user. It becomes active on login and inert again on logout.

**Interception.** While active, `localStorage.setItem` and `localStorage.removeItem`
are wrapped. A write to an allowlisted key marks the document dirty and schedules a
push 2 seconds later (debounced). Any other key passes straight through. Writes made
by the module itself while applying a remote document do not mark it dirty.

**Per-device baseline.** The module stores the last synced document and its revision
locally under `cs-settings-sync-base` and `cs-settings-sync-rev`. These two keys are
never synced.

**Three-way merge** of local state L, profile P and baseline B, per key:
- `set`: result = P ∪ (L − B) − (B − L), with items compared by the key's identity.
  An item added locally since the last sync is kept. An item removed locally since the
  last sync is removed. An item added on another device arrives. When an item exists in
  both with different content (same identity), the profile's version wins unless only
  the local one changed since B.
- `scalar`: if L ≠ B and P = B, the local value wins; otherwise the profile wins.
- A `set` value that is not valid JSON is merged as a `scalar` and logged as a console
  warning.
- **First login** (profile revision 0): the local values of all allowlisted keys form
  the first document.

**Pull** on login, on page load while logged in, when the tab becomes visible, every
60 seconds while visible, and on "Sync now". Pull, merge, write changed keys locally,
then push the merged document if it differs from P.

**Push** `PUT` with `baseRevision`. On 409, merge with the returned document and retry,
at most 3 times. On network or 5xx errors, keep the document dirty and retry with
backoff (2 s doubling to 5 min), on tab focus, or on "Sync now".

**Applying a remote change.** Changed keys are written to `localStorage` without
triggering a push. Theme and colour-blind preset are applied through their existing
listeners (a synthetic `storage` event for those keys). The current page is re-rendered
through the router, because most keys are read only at page init. A short toast says
"Settings updated from another device". When the user is mid-edit (a form with
unsaved input, or the geofilter editor open), the re-render waits until they leave the
page.

### Login, logout and UI

- **After login or activation:** pull and merge; re-render when anything changed. On
  the first login a toast says "Your settings are now saved to your account."
- **Logout** (header menu or account page) opens a dialog built on the app's existing
  dialog pattern:
  - "Keep my settings on this device" (default, focused).
  - "Remove my settings from this device": removes the allowlisted keys and the sync
    baseline. The profile keeps them.
  - Text: channel keys are never synced and stay on this device; they are not removed,
    because no copy exists anywhere else.
  - Pending changes are pushed before logging out.
- **Automatic logout** (expired session, disabled account, any 401): no dialog, local
  data stays, no further pushes.
- **Account page**, new "Settings sync" section: "Last synced <time>" or
  "Not synced: retrying", a "Sync now" button, a short list of what is and is not
  synced, and "Delete synced settings from my account" (confirmation dialog; local
  data stays; the next change starts a new document).
- Logged out or feature off: none of this renders.

## Error handling

| Situation | Behaviour |
|---|---|
| Network error or 5xx on push | Stay dirty; backoff retry; account page shows "Not synced: retrying". The app keeps working locally. |
| 409 on push | Merge with the returned document, retry up to 3 times, then treat as a network error. |
| 413 | Stop pushing; the account page names the largest keys. |
| 400 (denied key) | Should not happen with the server-provided allowlist; logged in the console, push stopped until reload. |
| 401 | Treated as automatic logout. |
| Invalid JSON in a local `set` key | Merged as scalar, console warning. |

## Security

- Server-side allowlist plus a hard denylist (#725); a tampered client cannot store
  channel keys or the API key in the profile.
- `withUser`: session cookie, Origin check and `X-CS-CSRF` on PUT and DELETE.
- Per-user rate limit on PUT.
- Values are the user's own strings, written back only into `localStorage`; no new
  rendering path is added, and the existing renderers already escape.
- No document content in the server log.

## Performance

One GET per page load, tab focus and minute while visible; one debounced PUT per burst
of changes. Interception costs one Set lookup per `setItem`. The server reads or
writes one row per request. The document is capped at 256 KiB.

## Testing

**Go.**
- `internal/users`: migration v1 → v2 on an existing database and on a fresh one;
  get and put with revisions (match, stale → conflict, first write); delete; the row
  goes with the account.
- `cmd/server`: GET/PUT/DELETE flow; 409 carries the current document; 413; 400 on a
  non-allowlisted key; denylist refuses `corescope_channel_*` and `meshcore-api-key`
  even when allowlisted; CSRF; rate limit; 401 without a session; feature off: routes
  absent and no `user_settings` table created; OpenAPI completeness.

**Frontend unit** (vm, the real module).
- The three-way merge as a table of cases: add and remove on each side, first login,
  scalar changed locally or remotely, same-identity item edited, invalid JSON.
- Interception: only allowlisted keys trigger a push; channel keys never do; applying
  a remote document does not trigger a push.
- Debounce, 409 retry and backoff.
- Logout dialog: keep versus remove; channel keys always stay.
- Feature off or logged out: no request and no interception.
- The allowlist test: every listed key still occurs in `public/`.

**Playwright** (e2etest build). Two browser contexts as two devices on one account:
device 1 adds a favorite and changes the packet time window; device 2 pulls and shows
both. Device 1 removes the favorite; device 2 does not keep it. Logout with "Remove":
synced keys are gone, a pre-set channel key is still there. Axe on the "Settings sync"
section and the logout dialog.

**Manual on staging:** a desktop and a phone on the same account.

## Open points for the implementation plan

- Which existing dialog component to build the logout dialog on (read `public/` for
  the app's modal pattern before choosing).
- How the router re-renders the current page without a full reload (read `app.js`).
- Detecting "mid-edit" for deferring a re-render: start with open account forms and
  the geofilter editor; extend only if a test or a user report shows a gap.
