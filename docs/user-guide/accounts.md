# Accounts (optional user management)

CoreScope runs without accounts by default: every page is public and settings live in
each visitor's browser. Operators can turn on **accounts**. The dashboard stays public;
logging in only adds things.

- Visitors can register with an email address, a display name and a password, and
  activate the account through a link mailed to that address.
- **Admins** get an admin area (account menu, *Admin*) with an overview, the user list
  (activate, disable, delete, promote) and the audit log, and can use the operator
  actions (geofilter save, prune, backup, perf reset) without the API key.

With the feature off (the default) the account routes are not registered: requests to
`/api/auth/*`, `/api/account/*` and `/api/admin/*` fall through to the normal page,
and no login or account control appears in the interface.

## For operators

### 1. Get a Brevo account

1. Create a free account at brevo.com. The free plan's daily volume is far more than
   account mails need.
2. Verify the sender address or domain you will send from (Brevo: *Senders, Domains &
   Dedicated IPs*). Mail from an unverified sender is rejected or lands in spam.
3. Create an API key (*SMTP & API, API Keys*).

#### Or: use your own Postal server

If you run [Postal](https://docs.postalserver.io), CoreScope can send through its HTTP
API instead of Brevo:

1. In Postal, add and verify the domain you will send from on your mail server.
2. Create an **API** credential for that mail server (*Credentials, Add credential*,
   type API).
3. In `config.json`, set `mail.provider` to `"postal"` and `mail.postalBaseUrl` to the
   address of your Postal web/API host, e.g. `https://postal.example.org`. It must be
   https, unless Postal runs on the same machine (`http://localhost:5000`).
4. Provide the credential's key as `mail.postalApiKey` or, better, the environment
   variable `CORESCOPE_POSTAL_API_KEY`, which wins over the config value. CoreScope
   sends it in the `X-Server-API-Key` header.

```json
"mail": {
  "provider": "postal",
  "postalBaseUrl": "https://postal.example.org",
  "postalApiKey": "<key of the API credential, or leave out and use the env variable>",
  "fromEmail": "noreply@example.org",
  "fromName": "My CoreScope"
}
```

Everything else below applies to both providers, except where it names one.

### 2. Configure

Add to `config.json` (see `config.example.json` for every key):

```json
"userManagement": {
  "enabled": true,
  "adminEmails": ["you@example.org"],
  "publicBaseUrl": "https://corescope.example.org",
  "mail": { "fromEmail": "noreply@example.org", "fromName": "My CoreScope" }
}
```

Provide the API key as `mail.brevoApiKey` or, better, the environment variable
`CORESCOPE_BREVO_API_KEY`. Restart the server. It refuses to start if the mail setup is
incomplete, and the log says what is missing.

| Key | Meaning |
|---|---|
| `adminEmails` | Addresses that become admin on activation. They cannot be demoted, disabled or deleted from the UI. Remove an address here first. |
| `publicBaseUrl` | The address visitors use. Every mail link is built from it, never from the request. It must match the browser origin, because state-changing requests from another origin are refused. |
| `dbPath` | Where accounts are stored. Default: `users.db` next to the analyzer database. |
| `sessionDays` | Login lifetime, extended while in use. Default 30, maximum 365. |
| `trustedProxies` | CIDRs of your reverse proxy, so the per-IP login limits see real client IPs. Without it, behind a proxy every client shares the proxy's IP for the per-IP limits (they are switched off when that IP is loopback or private). Per-address and per-account limits apply either way. |
| `mail.provider` | `brevo` (default) or `postal`. |
| `mail.brevoApiKey` | Brevo only: the API key. `CORESCOPE_BREVO_API_KEY` wins over it. Required with Brevo. |
| `mail.postalBaseUrl` | Postal only: your Postal host, https (http only for localhost). Required with Postal. |
| `mail.postalApiKey` | Postal only: the key of the server's API credential. `CORESCOPE_POSTAL_API_KEY` wins over it. Required with Postal. |
| `mail.webhookSecret` | Enables delivery status (below). At least 16 characters. With Postal, only letters, digits, `.`, `_` and `-`. |

### 3. The first admin

Register at `#/account/register` with an address from `adminEmails`, click the activation
link and enter the password you chose. You are now admin and can promote others in
**Admin, Users**.

### 4. Delivery status (optional)

To see per user whether mails were delivered, bounced, blocked or marked as spam:

1. Set `mail.webhookSecret` (16+ random characters), or `CORESCOPE_BREVO_WEBHOOK_SECRET`.
   The webhook route exists only when a secret is set.
2. In Brevo, create a **transactional** webhook to
   `https://<publicBaseUrl>/api/mail/brevo/webhook` with these events: delivered,
   opened, clicked, soft bounce, hard bounce, invalid email, deferred, spam, blocked,
   error. Use **bearer** authentication with the same secret.

With Postal:

1. Set `mail.webhookSecret` (16+ random letters and digits), or
   `CORESCOPE_POSTAL_WEBHOOK_SECRET`.
2. In Postal, add a webhook (*Webhooks, Add webhook*) for your mail server. Postal
   cannot send a custom header, so the secret goes into the URL as Basic auth:
   `https://postal:<secret>@<host of publicBaseUrl>/api/mail/postal/webhook`. The user
   name (`postal` here) is not checked. Choose these events: MessageSent,
   MessageDelayed, MessageDeliveryFailed, MessageHeld, MessageBounced, and if you use
   Postal's open and click tracking, MessageLoaded and MessageLinkClicked. Other
   events are accepted and ignored. Postal's "Sent" means the recipient's server
   accepted the mail and is shown as delivered.

   A failed delivery marks the address as bouncing only when the recipient's server
   rejected the mail permanently (a 5xx SMTP reply). Postal also fails mail for
   reasons on its own side: its outbound spam threshold, too many attempts after
   temporary failures, a removed message or a deleted domain. Those, and bounce
   messages (which can be delay notices or auto-replies), are shown as *error* with
   Postal's explanation, and the address is not flagged.

If the provider cannot reach your instance, use **Refresh status** in a user's details
instead: CoreScope then asks the provider directly. Mails sent before you switched
provider cannot be refreshed. "Opened" is indicative only. Some mail apps load
tracking pixels automatically, and others block them.

### When mail fails

- In **Admin, Users**, *Resend mail* sends a fresh activation link to a pending account.
- *Activate* activates a pending account by hand. The address is then unverified; the
  row shows "manual" and the audit log records who did it.

### The admin area

*Admin* in the account menu (on phones: the account page) opens three tabs (four with channel proposals on):

- **Overview**: a *Needs attention* list when something applies (pending accounts older
  than 24 hours, addresses whose mail bounces, accounts with 5 or more failed logins in
  24 hours, MQTT sources that are disconnected or silent for 10 minutes), user figures
  (accounts, registrations, active users, logins, mail of the last 7 days) and the system
  status (version, uptime, MQTT sources, observers). It refreshes every minute while the
  tab is visible. Each item links to the matching user list or audit entries.
  With node notifications on, the Users card also shows the notification mails of the
  last 24 hours against `maxMailsPerDay`, and how many nodes are watched by how many
  users (counts only; watch lists are private).
- **Users**: the user list. *Bouncing mail only* shows addresses whose mail bounces.
- **Audit**: every recorded action, newest first, filtered by action, period or user.
  Successful and failed logins are recorded without IP address and deleted after 90
  days; a failed login for an address that has no account is not recorded. Other
  actions are kept.
- **Proposals** (only with channel proposals on): proposed hashtag channels by status,
  with *Approve*, *Reject* and *Revoke* and an optional note that the proposer sees.
  Approving asks first, because the channel becomes readable for every visitor.

Old `#/admin/users` links still work and open the Users tab.

### Channel proposals (optional)

Hashtag channels are public by construction: the key is derived from the name, so
anyone who knows the name can read the channel. CoreScope decrypts only the hashtag
channels in `hashChannels`. With channel proposals on, logged-in users propose a
hashtag channel from *Channels, Add channel, Propose for everyone*, and admins decide
on the *Proposals* tab.

```json
"userManagement": {
  "channelProposals": { "enabled": true, "maxPending": 100, "maxApproved": 128, "perUserPerDay": 5 }
}
```

- An approved channel is decrypted by the ingestor from its next refresh (once a
  minute, no restart) and listed for every visitor on the Channels page, also before
  it has traffic and regardless of the region filter. Messages received before the
  approval stay encrypted.
- Revoking stops decryption of new messages from the next refresh; stored messages
  stay. A channel that is also in `hashChannels` or `channelKeys` stays decrypted.
- A name in `hashChannels`, or a `channelKeys` name written with its leading `#`, cannot
  be proposed: the answer is "this channel is already decrypted on this instance". The
  comparison is case-sensitive.
- A rejected name cannot be proposed again until 90 days after the decision; a revoked
  one can be proposed again at once, and the new proposer then replaces the old one (the
  audit log keeps both). Rejected and revoked proposals are deleted 90 days after the
  decision.
- `maxApproved` bounds the decryption work: every approved key is tried on every group
  message. `perUserPerDay` and `maxPending` limit proposals (HTTP 429).
- The ingestor reads `users.db` read-only, from `userManagement.dbPath` or next to the
  analyzer database. Set `dbPath` explicitly when the server and the ingestor are not
  given the same analyzer database path (for example `DB_PATH` set for one of them).
  Both log the absolute path they use at startup (server: `[users] user management
  enabled: db=...`, ingestor: `[proposals] reading approved channels from ...`).
- Names: at most 31 bytes including the `#` (MeshCore stores 32 with the terminator),
  no invisible or control characters (blank fillers such as U+3164 and spaces other than
  the plain space count as invisible), case-sensitive, not Public. Emoji work, except
  subdivision flags (England, Scotland, Wales), which use invisible tag characters.
  Names that need the zero-width non-joiner (U+200C), as some Persian and Urdu
  spellings do, are refused as well.

### Node notifications (optional)

With node notifications on, logged-in users choose nodes to watch and get a mail when a
watched node changes state. Admins can also watch the instance.

```json
"userManagement": {
  "notifications": { "enabled": true, "intervalMinutes": 5, "perUserPerDay": 20, "maxMailsPerDay": 100, "maxWatchesPerUser": 50 }
}
```

- Events: a watched node goes offline or comes back, using the node page's thresholds:
  offline once it has not been heard for the `healthThresholds` silent hours of its role
  (heard is the latest of its last advert, the packet store's newest packet involving
  it, and for repeaters and rooms its last relay hop). A watched node also reports a
  battery below `batteryThresholds.lowMv`, and recovers at `lowMv + 100` mV. Admins can
  add a new foreign node (once per node) and an observer going offline
  (`observerStaleMinutes`) or back (`observerOnlineMinutes`).
- A check runs every `intervalMinutes` (the first one an interval after startup, and only
  after the startup load). All changes for one user in one check go into one
  mail. The first check of a newly watched node stores its state without a mail; states
  are kept in `users.db`, so a restart does not mail again.
- While ingest is stale (the newest packet in the packet store is older than 30 minutes,
  for example when the MQTT broker or the ingestor is down), the offline checks for
  nodes and observers pause: their states stay as they were and nothing is mailed for
  them. Battery and foreign-node checks go on. The server log says when the pause
  starts and ends. After the feed comes back, offline reports wait one silent window
  (the role's silent hours for nodes, `observerStaleMinutes` for observers): until then
  older evidence counts as heard at the moment the feed came back, so the outage itself
  mails nobody. A node that died during the outage is reported up to one window late.
  The server keeps this grace in memory: a restart after the recovery and inside that
  window loses it, and so does a server that starts after an outage that already ended
  before its first check (for example a reboot of the whole host after a long outage).
- Limits: `perUserPerDay` mails per user and `maxMailsPerDay` in total, both over a
  rolling 24 hours. A change over a limit, for an account that is not active, for a
  bouncing address or while the user has notifications off is recorded and never mailed
  later. `maxWatchesPerUser` bounds each watch list.
- `maxMailsPerDay` counts notification mail only; activation, password-reset and
  address-change mail are not counted and are not limited by it. They share the mail
  provider's daily quota, though (Brevo's free tier allows 300 a day for the whole
  account; a Postal server has whatever send limit you set on it), so keep
  `maxMailsPerDay` well below that quota. The default 100 leaves 200 a day for account
  mail and for any other sender on the same provider account.
- Every mail carries a one-click unsubscribe link and `List-Unsubscribe` headers; the
  link turns notification mails off for that account and nothing else. The account page
  turns them back on.
- Watch lists are private: admins see counts on the overview, not lists.

#### External node alerts (optional)

A job outside CoreScope can publish alerts about nodes, for example coverage sectors
where a repeater is never heard by passing cars (#2177). With `externalAlerts` set,
users can choose the event *A watched node gets a <label> alert or it clears*
(`node.external`, off by default):

```json
"notifications": {
  "enabled": true,
  "externalAlerts": { "url": "http://coverage-job/alerts.json", "maxAgeHours": 48, "label": "Coverage" }
}
```

The feed is JSON, at most 1 MB, fetched once per check while someone has the event on:

```json
{ "generatedAt": "2026-10-11T01:35:12Z",
  "alerts": [ { "pubkey": "<64 hex>", "key": "SE", "text": "never heard to the south-east", "url": "https://example.org/coverage" } ] }
```

- `key` (at most 64 characters, not `*`) tells several alerts on one node apart; `text`
  (cut at 200 characters) goes into the mail; `url` (optional, http or https) is the
  mail line's link, otherwise the node page. Malformed alerts are skipped and logged.
- A new alert on a watched node mails `<name>: <label>: <text>`; an alert that is gone
  from a fresh feed mails `<name>: <label> resolved (<key>)`. The first check after a
  node is watched stores its current alerts without a mail.
- A feed that cannot be fetched, does not decode or whose `generatedAt` is older than
  `maxAgeHours` (default 48) changes nothing: no alert is mailed and none is resolved.
  The server log says when the feed stops and starts being usable.
- The server only reads the feed; the states live in `users.db` like the other events.

### Companion linking (CoreDrive RX)

With user management on, a user logs in from the CoreDrive RX app with their email and password.
RX then holds a **device token** instead of a cookie: it shows under *My account, Devices* as
"CoreDrive RX – <device name>", lasts 90 days from its last use, and *Log out* there revokes it.
The token is limited to the account routes RX needs (`/api/auth/me`, `/api/auth/logout`,
`/api/account/settings`, `/api/account/companions*`); anything else answers 403.

RX then links the companion it is connected to: the companion signs a server challenge with its
own key, so only whoever holds the device can link it. The newest proof wins: a companion that
changes hands moves to the new owner, and the previous owner gets an audit entry and a mail (a
security notice: it goes out whatever their notification settings are). A linked companion's
coverage counts as the owner's (*My coverage* on the coverage page). Links are private to their owner and to admins
(*Admin, user detail* lists them).

- **RX on another origin** (for example a hosted RX build): add that origin to
  `corsAllowedOrigins`. For listed origins, and only on the device-token routes above, the server
  then also allows `POST, PUT, DELETE` and the `Authorization` and `Content-Type` headers.
  `Access-Control-Allow-Credentials` stays off; RX sends the token as a bearer header, not a
  cookie. Same-origin RX needs no CORS setting.
- A bearer header only counts on a request without the session cookie: with the cookie, the
  cookie decides. So an auth proxy in front of CoreScope that adds its own
  `Authorization: Bearer` does not break browser logins, and RX sends its calls without cookies.
- The signed message contains the host of `publicBaseUrl`, so RX must log in on that address.
- **Linked companions only:** `clientRxCoverage.requireLinkedCompanion` makes the ingestor drop
  client data from companions nobody linked. It is a filter, not a security boundary, and it drops
  everything from RX builds without account support: upgrade the RX clients first. See
  [Client RX Coverage, Linked companions only](../client-rx-coverage.md#linked-companions-only-optional).

### Backups

`users.db` holds password hashes and addresses. The server keeps its own snapshots of
it: at startup when the newest snapshot is older than 24 hours (or there is none), then
about every 24 hours (the check runs hourly). A snapshot is a complete copy named `users-<YYYYMMDD-HHMMSS>.db` (UTC),
readable by the server's user only, in `backups/` next to `users.db`. After each new
snapshot the oldest ones beyond `keep` are deleted, never the one just written.
Temporary files of an interrupted snapshot (`users-<YYYYMMDD-HHMMSS>.db.tmp`) are
deleted once they are older than 24 hours; other files in that directory are never
touched. A failed snapshot is logged (`[users] backup failed: ...`) and the next run
tries again.

The server creates the default directory, or a `backup.dir` that does not exist yet,
with mode 0700. An existing `backup.dir` is not tightened: make it readable by the
server's user only yourself (`chmod 700 <dir>`).

A deleted account, including one the user deleted themselves, stays in the snapshots
for up to `keep` days (one snapshot a day), and in every downloaded copy until you
delete that copy.

```json
"userManagement": {
  "backup": { "enabled": true, "dir": "", "keep": 7 }
}
```

| Key | Meaning |
|---|---|
| `backup.enabled` | Default `true`, also when the block is absent. `false` turns the snapshots off. |
| `backup.dir` | Where snapshots go. Empty: `backups/` next to `users.db`. A relative path is relative to the server's working directory. |
| `backup.keep` | How many snapshots are kept. Default 7, also for 0 or less. |

Snapshots on the same disk are lost with that disk. To keep a copy elsewhere, log in as
an admin and open `/api/admin/users-backup` in the same browser: it downloads a fresh
snapshot (`corescope-users-<YYYYMMDD-HHMMSS>.db`) and records it in the audit log
(`user.backup`). Store the download encrypted: it holds every password hash and address.
The analyzer database has its own backup route, `GET /api/backup`.

**What a restore brings back.** `users.db` returns to the moment of the snapshot:

- accounts deleted after it, including accounts users deleted themselves;
- old passwords of users who changed them since;
- sessions that were logged out or revoked since (valid again until they expire, for
  anyone who still holds the cookie);
- activation, reset and email change links used since (valid again until they expire).

The audit log is replaced too, so note the deletions before you overwrite it.

**Restore** (not automated). The commands use the `sqlite3` command-line tool; replace
`2026-10-08 12:00:00` with the snapshot's time from its file name (UTC).

1. Stop the server.
2. List the accounts deleted since the snapshot, from the current `users.db`:

   ```sh
   sqlite3 users.db "SELECT target_user_id, action, datetime(at, 'unixepoch') FROM audit_log
     WHERE action IN ('user.delete', 'user.delete.self')
       AND at >= CAST(strftime('%s', '2026-10-08 12:00:00') AS INTEGER);"
   ```

3. Copy the snapshot over `users.db`, and delete `users.db-wal` and `users.db-shm` if
   they exist.
4. Still with the server stopped, disable the accounts from step 2 (replace `12, 34`
   with their ids) and clear all sessions and links, so everyone logs in again:

   ```sh
   sqlite3 users.db "UPDATE users SET status = 'disabled' WHERE id IN (12, 34);
     DELETE FROM sessions; DELETE FROM tokens;"
   ```

5. Start the server.
6. Delete the accounts from step 2 again in the admin area. Deleting there, not in
   `sqlite3`, also replaces the addresses in the mail history with a hash and records
   the deletion in the audit log.

A snapshot made by a newer CoreScope version is refused at startup ("database schema
version N is newer than this binary supports"): run that version or newer. Deleting
`users.db` removes all accounts and nothing else.

## For users

- **Register:** *Log in, Create an account*, then click the link in the mail within 48
  hours and enter your password to finish. Registering a pending (not yet activated)
  address again replaces the password and sends a new link. Registering an address that
  is already activated changes nothing; its owner gets a notice mail instead.
- **Forgot password:** *Log in, Forgot password?* The link works once, for one hour, and
  logs out all your devices.
- **My account:** change your display name, password or address, see your logged-in
  devices, or delete your account. A new address is confirmed from a link sent to it, and
  your old address gets a notice. Changing your password logs out your other devices.
- **Download my data:** *My account, My data, Download my data* saves one JSON file with
  your profile (including when and by whom the account was activated), logged-in devices,
  synced settings, proposals, notification settings and watched nodes, and the history of
  your account and of the mail sent to you (including the address each mail went to).
  An address change you have not confirmed yet is included as `pendingEmail`. Password
  hashes and login, link and unsubscribe tokens are not in it, and neither is the
  server's internal bookkeeping of which notifications it already sent. Other accounts
  in your history appear as a number only. Each download is recorded in the audit log
  (`user.export`).
- **Settings sync:** while you are logged in, your settings follow you: your nodes,
  favorites, theme and customizer settings, saved packet filters, and the filter, sort
  and view choices of each page. Log in on another browser or phone and they are
  restored; later changes reach your other devices within about a minute, or when you
  return to the tab. A node or favorite added on one device is never dropped by another,
  and one you removed stays removed. Saved packet filters are stored in your account as
  you typed them.
- **Propose a channel** (when the operator turned channel proposals on): *Channels, Add
  channel*, type the hashtag name, *Propose for everyone*. *My account, My proposals*
  shows the status and the admin's note. Approved channels are readable for every
  visitor of the instance.
- **Node notifications** (when the operator turned them on): *Notify me* on a node's
  page watches it; *My account, Notifications* lists your watched nodes, turns mails on
  or off, chooses the events, and *Watch my nodes* copies your synced My nodes. You get
  at most one mail per check, and every mail has a link that turns the mails off.
- **Companions:** log in from the CoreDrive RX app while it is connected to your
  companion, and it is linked to your account: it shows under *My account, Companions*
  (name, short key, linked since, last seen). It is not added to My nodes: a companion is
  not a node to monitor. The app shows
  under *Devices* as "CoreDrive RX – <device name>"; *Log out* there signs the app out.
  *Unlink* removes the link; the coverage it already collected stays on the map. On the coverage page, *My coverage* shows only what your linked companions heard.
- **Never synced:** channel keys and decrypted messages, the API key, panel and column
  sizes, collapsed panels and map positions. They stay in the browser where you set them.
- **Logging out** asks whether to keep your settings on this device (the default) or
  remove them; your account keeps its copy either way, and channel keys are never
  removed. An automatic logout (expired session) keeps everything on the device. On a
  shared computer choose *Remove my settings from this device*: settings kept on the
  device are added to the account of the next person who logs in there.
- **My account, Settings sync** shows when your settings were last synced, has *Sync now*,
  and *Delete synced settings from my account*, which removes the account's copy only.
  The settings on your devices stay, and your next change starts a new copy.

Admins can see your display name and email address in the Users list, and when you
logged in or someone failed to log in to your account (kept 90 days, no IP address).
