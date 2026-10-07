# Optional User Management, Sub-project E: Node Notifications, Design Spec

**Status:** written 2026-10-07 while the operator was away. The decisions in the table
below were taken by Claude with its recommended option, as the operator asked; each one
is open for review and can be reversed.
**Builds on:** A (`docs/specs/2026-10-06-user-management-design.md`), B
(`docs/specs/2026-10-06-user-settings-sync-design.md`), C
(`docs/specs/2026-10-07-admin-dashboard-design.md`) and D
(`docs/specs/2026-10-07-channel-proposals-design.md`). Roadmap #2128, part E.
**Related issues:** #775 (alerting epic), #730 (foreign advert detection, implemented in
the ingestor as `foreignAdverts.mode`), #663 (battery thresholds).

## Problem

A sysop learns that their repeater went silent, or that its battery is running down, only
by opening CoreScope and looking. #775 asks for notifications on network events. With
accounts (A) there is now a person and a verified mail address to notify, and a mailer
with delivery status.

## Goals

1. A logged-in user picks nodes to watch and gets one mail when a watched node goes
   offline, comes back, or reports a low battery.
2. Admins can additionally watch the instance: a new foreign node appears, or an observer
   goes offline.
3. Mail volume stays bounded per user and per instance, and every mail carries a
   one-click unsubscribe.
4. Off by default. With the feature off, nothing changes.

## Non-goals

- Other channels (Discord, Telegram, push, outbound webhooks). The design keeps the event
  detection separate from delivery so a second channel can be added later.
- The topology, RF and anomaly alerts of #775 (mesh split, SNR degradation, noise
  spikes, clock drift). They need their own thresholds and validation.
- Notifications for visitors without an account.
- Per-user thresholds. The instance's `healthThresholds` and `batteryThresholds` apply.

## Decisions (taken by Claude, open for review)

| # | Topic | Decision | Alternative not taken |
|---|---|---|---|
| 1 | Who | Logged-in active users watch nodes; admins also get two instance-wide event types. | Admin-only notifications. |
| 2 | What to watch | An explicit watch list in `users.db`, filled from a "Notify me" toggle on the node page, with a one-click "Watch my nodes" that copies the synced `meshcore-my-nodes` list (B). | Watch every node in the synced "my nodes" list automatically. |
| 3 | Events | Node offline, node back online, node battery low (and recovered, in the same mail as other changes); admins: new foreign node, observer offline and back. | All of #775. |
| 4 | Detection | A server loop every 5 minutes compares current state with the last state stored per (user, subject, event) in `users.db`. | Event hooks inside packet ingest. |
| 5 | Restart behaviour | A subject's first evaluation stores the state without mailing, so a restart or a new watch never sends a burst. | Mail on the first evaluation. |
| 6 | Delivery | Mail only; all changes for one user in one evaluation go into one mail. | One mail per event. |
| 7 | Limits | Per user at most 20 notification mails per day; per instance at most 300 per day (Brevo's free tier); at most 50 watched nodes per user. Over a limit, the change is recorded and skipped, never queued. | A queue that sends later. |
| 8 | Unsubscribe | Each mail has a one-click unsubscribe link (and `List-Unsubscribe` + `List-Unsubscribe-Post` headers) that turns notifications off for that user; the account page turns them back on. | Link to the account page only. |

## Events

All thresholds are the instance's existing configuration, so a notification agrees with
what the node page shows.

| Event | Subject | Goes to "bad" when | Goes back to "good" when |
|---|---|---|---|
| `node.offline` | node pubkey | the node is silent: no traffic for `healthThresholds` silent hours for its role (infra 72 h, others 24 h); for repeaters and rooms the later of last heard and last relayed counts (#1598), as on the node page | the node is heard again within the silent window |
| `node.battery` | node pubkey | the latest advert telemetry `battery_mv` is below `batteryThresholds.lowMv` (3300) | `battery_mv` is at or above `lowMv + 100` (hysteresis against flapping around the threshold) |
| `foreign.new` (admin) | node pubkey | a node with `foreign_advert = 1` that this admin has not been told about | no "good" transition; one mail per node, ever |
| `observer.offline` (admin) | observer id | `last_seen` older than `healthThresholds.observerStaleMinutes` (1440) | `last_seen` within `observerOnlineMinutes` (60) |

A node without battery telemetry never triggers `node.battery`. A node that disappears
from the analyzer database (retention) is treated as offline once and then dropped from
evaluation; its watch stays until the user removes it.

## Storage (`internal/users`, schema v5)

```
CREATE TABLE notification_prefs (
  user_id      INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  enabled      INTEGER NOT NULL DEFAULT 1,
  events       TEXT NOT NULL DEFAULT '',    -- comma list of opted-in event types
  unsub_token  TEXT NOT NULL UNIQUE,        -- random, 32 bytes base64url
  updated_at   INTEGER NOT NULL
);
CREATE TABLE notification_watches (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  pubkey     TEXT NOT NULL,                 -- lowercase hex
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, pubkey)
);
CREATE TABLE notification_state (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  event      TEXT NOT NULL,
  subject    TEXT NOT NULL,
  state      TEXT NOT NULL,                 -- 'good' | 'bad' | 'told' (foreign.new)
  changed_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, event, subject)
);
```

Defaults on first use: node events opted in, admin events opted out (an admin turns them
on). State rows for subjects no longer watched are deleted when the watch is removed.
Mail counts for the limits come from `mail_log` (purpose `notify`).

## Config

```json
"userManagement": {
  "notifications": {
    "enabled": false,
    "intervalMinutes": 5,
    "perUserPerDay": 20,
    "maxMailsPerDay": 300,
    "maxWatchesPerUser": 50
  }
}
```

Values at or below 0 fall back to the defaults. `intervalMinutes` has a floor of 1.

## Server (`cmd/server`, only when user management and notifications are on)

- **Evaluator.** Started in `initUserManagement` next to the janitor, stopped through the
  same `stop` channel and wait group, with `recover()` so a panic logs and the loop
  continues on the next tick. Each tick:
  1. Read all watches, prefs and states from `users.db` (one query each).
  2. Read the analyzer data it needs on the read-only connection: one
     `SELECT public_key, role, last_seen, battery_mv, foreign_advert, name FROM nodes`
     limited to watched pubkeys and foreign nodes, the relay times from the existing
     in-memory relay metrics, and the active observers.
  3. Compute each (user, event, subject) state, compare, collect transitions per user.
  4. For users with transitions: skip when the user is not active, has
     `email_bouncing`, has notifications off, or is over a limit; otherwise send one mail.
  5. Write the new states in one transaction, also for skipped users (so a skipped change
     is not mailed later).
- **Routes** (withUser unless noted, all in OpenAPI under the `users` tag):
  - `GET /api/account/notifications`: prefs, watches (with node names), limits.
  - `PUT /api/account/notifications`: `{enabled, events}`; admin events refused for
    non-admins.
  - `PUT /api/account/notifications/watches/{pubkey}` and `DELETE ...`: add or remove a
    watch; 409 over `maxWatchesPerUser`, 400 for a malformed pubkey, 404 for a node not in
    the analyzer database.
  - `POST /api/account/notifications/watch-my-nodes`: copies the pubkeys from the
    user's synced `meshcore-my-nodes` (B) into the watch list, up to the limit; returns
    added and skipped counts.
  - `GET /api/notifications/unsubscribe?token=` (no session): shows a confirm page;
    `POST` with the token turns notifications off. The `List-Unsubscribe-Post` one-click
    POST goes to the same handler.
- **Mailer.** `mailer.Message` gains a `Headers map[string]string` field, passed to
  Brevo's `headers`. Only `List-Unsubscribe` and `List-Unsubscribe-Post` are set.
- **Mail content.** Subject `[<fromName>] <n> change(s) on your watched nodes`; one line
  per transition with node name, event, time, and a link to the node page; footer with
  the unsubscribe link and a link to the account page. Plain text and HTML through the
  existing `render` helper.
- **Audit.** `notify.unsubscribe` (via link) and `notify.prefs` (changes on the account
  page). Watch add/remove is not audited (noise).
- **Admin dashboard (C).** The Users card's mail buckets already count `notify` mail
  through `mail_log`; the Overview gains "notification mails today: n of maxMailsPerDay".

## Frontend

- **Node page** (side pane and full page): a "Notify me" toggle for logged-in users when
  the feature is on, showing the watch state; disabled with a hint at the limit.
- **Account page:** a "Notifications" section: on/off, event checkboxes (admin events
  only for admins), the watch list with remove buttons, "Watch my nodes", and the
  per-day limit.
- **Unsubscribe page:** `#/account/unsubscribe?token=` with one confirm button.
- Every rendered value goes through `escapeHtml`; colors through CSS variables; the
  account section deep-links as `#/account?section=notifications`.

## Security and privacy

- Watches are private: only the user sees their own list; admins see counts, not lists.
- The unsubscribe token is random, per user, unrelated to the session, and only turns
  notifications off (it cannot read or change anything else).
- No new personal data leaves `users.db`; mail goes only to verified, active addresses.

## Performance

One tick every 5 minutes: three small `users.db` queries, one analyzer query bounded by
the number of distinct watched pubkeys plus foreign nodes (indexed on `public_key`, and a
partial index exists for `foreign_advert`), and an in-memory comparison. No work is added
to packet ingest, WebSocket broadcast or any request path. With 100 users x 50 watches
the state table holds at most 5,000 rows per event type.

## Testing

- `internal/users`: migration v4 to v5; prefs defaults and token uniqueness; watches add,
  remove, limit; state upsert and cleanup on watch removal; cascade on user delete.
- `cmd/server` evaluator (pure function from inputs to transitions, plus the loop with a
  fake mailer and a fixed clock): first evaluation sends nothing; offline and back;
  relay-aware infra; battery hysteresis at `lowMv` and `lowMv + 100`; foreign node once
  per admin; observer offline and back; one mail per user per tick; skipped for inactive,
  bouncing, disabled, over per-user limit, over instance limit, and those skips not
  mailed later; restart (states survive).
- Routes: auth, CSRF, limits, 400/404/409, admin events refused for users, unsubscribe
  with a valid and an invalid token, feature off: routes absent and no loop started.
- Mailer: `Headers` reach the Brevo request body.
- Frontend unit (vm): toggle states, account section, escaping.
- Playwright (e2etest build): a user watches a node, the account page lists it, the
  unsubscribe link turns notifications off.
