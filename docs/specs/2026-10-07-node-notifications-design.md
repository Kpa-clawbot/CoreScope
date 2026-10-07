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

## Amendments from the implementation plan

The plan (`docs/plans/2026-10-07-node-notifications.md`, untracked) decided these points
where the spec was silent or did not match the code; all 18 were accepted as written,
except where a ruling below changes one.

1. "Per day" is a rolling 24 hours counted from `mail_log`, like the proposal limit.
2. Offline uses the node page's three timestamps: `nodes.last_seen`, the packet store's
   newest packet involving the node, and for repeaters and rooms `last_relayed`.
3. No evaluation runs while the packet store is still loading after a restart; the first
   one runs one interval after startup.
4. `foreign.new` keeps a per-admin baseline row (subject `*`): the first evaluation after
   opting in stores the current foreign nodes without mailing.
5. Without a previous state, a battery between `lowMv` and `lowMv + 100` and an observer
   between `observerOnlineMinutes` and `observerStaleMinutes` start as good.
6. A watched node missing from the analyzer database is offline (one change) and not
   evaluated for battery; the watch stays.
7. States are written before mails are sent; a failed state write sends nothing and the
   next check tries again; a failed send is not retried.
8. States exist only for chosen events; leaving an event out deletes its states. Admin
   events are evaluated only while the account is an admin.
9. Preferences rows are created lazily (GET or PUT of the notification state, a watch
   add, `watch-my-nodes`) with notifications on and the two node events. Changed by
   ruling R1: users without a row are evaluated too.
10. The unsubscribe GET only redirects to `#/account/unsubscribe?token=`; the POST takes
    the token from the query string without a session or Origin check, answers 200 also
    when notifications were already off, and 410 for an unknown token. The token is
    stored raw, never rotates and only turns notifications off.
11. `watch-my-nodes` answers `{added, already, skipped, account}`.
12. The watch routes answer the full notification state; watching an already watched
    node and unwatching an unwatched one answer 200.
13. An admin event chosen by a non-admin answers 403; an unknown event answers 400.
14. Pubkeys are 64 hex characters, lowercased on input; anything else answers 400.
15. Mail: one subject line with the number of changes, one linked line per change, a
    "Manage notifications" button and a footer with the one-click unsubscribe link;
    every existing mail renders byte-identical.
16. The admin overview's Users card shows mails in the last 24 hours against
    `maxMailsPerDay`, watched nodes and watching users; the audit filter gains
    `notify.*`.
17. The e2etest build gains `GET /__e2e/unsubscribe-link?email=` so Playwright can follow
    an unsubscribe link without waiting for a real state change.
18. Foreign `told` rows are never pruned; they are bounded by the foreign nodes the
    instance has seen.

### Rulings during the implementation

- R1. Users without a preferences row are evaluated with the default preferences
  (enabled, node events). Users with notifications off keep being evaluated: their
  states are written and nothing is mailed, so turning notifications on again never
  mails old changes.
- R2. Inactive and bouncing accounts are skipped at send time, not in the evaluator;
  their states are still written, so the skipped changes are never mailed later.
- R3. Offline takes the later of `last_seen` and the packet store's last heard (more
  lenient than `roles.js` `last_heard || last_seen`); in a rare case the mail says
  online while the node page says stale.
- R4. Admin events of a user who is no longer an admin: their states are deleted, like
  opting out, so a re-promotion starts with a fresh baseline and no burst of mails. A
  demoted and re-promoted admin misses the foreign nodes seen in between.
- R5. The server lowercases pubkeys at the route boundary before storing a watch (PUT
  and DELETE of a watch, `watch-my-nodes`); `nodes.public_key` is lowercase.
- R6. The Notify-me toggle waits for the first `/api/auth/me` round trip before it
  decides whether a user is logged in; a failed state GET hides the toggle silently.
- R7. `DB.NotifyNodes` reuses the existing SQL placeholder helper; `internal/users`
  gets one placeholder helper shared by the new and the two existing inline copies.
- R8. Test and tooling details without behaviour change: the limit test advances the
  clock before writing the blocking `mail_log` row; the frontend unit test reads
  elements through the test document; the toggle computes its state on its own line
  before the HTML sink (XSS gate); gofmt runs only on touched files.

### Amendments from the final review

- F1 (I1). "Packet store loaded" (amendment 3) means the whole startup load: the hot
  window and the background fill (`PacketStore.StartupLoadDone`), on top of the
  `/api/healthz` readiness. Readiness alone can come while the newest packets are still
  loading, which would mail "offline" and then "back online" for a node whose recent
  packets were not loaded yet.
- F2 (I2). Node and observer names in a notification mail pass one sanitiser
  (`mailSafeText`): control characters (CR, LF and tab included) and bidi overrides and
  isolates (U+202A-U+202E, U+2066-U+2069) become a space, runs of spaces collapse and
  the ends are trimmed. It applies to the plain-text and the HTML part; the HTML part is
  still escaped. A name that is empty afterwards falls back to the key prefix or the
  observer id.
- F3 (I3). The default `maxMailsPerDay` is 100, not 300 (decision 7 and the Config
  example). It counts notification mail only (`mail_log` purpose `notify`); activation,
  reset and address-change mail share the provider's daily quota (Brevo free tier: 300
  for the whole account) and would fail at the provider on a day notifications used all
  of it. 100 leaves room for that mail and for other senders on the same account.
- F4 (I4). While ingest is stale (the newest transmission in the packet store, read in
  O(1) from the tail of `PacketStore.packets`, is older than 30 minutes, or the store is
  empty), `node.offline` and `observer.offline` are not compared: no state is written or
  changed and nothing is mailed for them. `node.battery` and `foreign.new` go on. The
  evaluator stays pure (`notifyInput.IngestStale`); the notifier logs
  `[notify] ingest stale since <time>; offline checks paused` once per stale period and
  `[notify] ingest fresh again; offline checks resumed` when it ends. Without this, an
  MQTT or ingestor outage longer than a role's silent window mailed every watcher
  "offline" and later "back online".
- F5 (I5). Every tick that evaluates logs one line:
  `[notify] tick: users=N changes=M mails=K took=Xms lock=Yms`, where `lock` is how long
  `PacketStore.LastHeardMap` held the store's read lock (timed inside the locked section
  and returned with the map). The lock time at live scale was an estimate; staging and
  live now report the measured value every interval. Code changes follow only if it is
  high.
