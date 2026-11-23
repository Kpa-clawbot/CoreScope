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

#### Or: use your own SMTP server

CoreScope can also send through any SMTP server (your own Postfix, a mail host you
already use) with no HTTP API or webhook involved:

1. Make sure the server accepts mail from the address in `mail.fromEmail`.
2. In `config.json`, set `mail.provider` to `"smtp"` and `mail.smtpHost` to the server's
   name, e.g. `smtp.example.org`.
3. If the server requires a login, set `mail.smtpUser` and provide the password as
   `mail.smtpPassword` or, better, the environment variable `CORESCOPE_SMTP_PASSWORD`,
   which wins over the config value. User and password are set together or not at all.
   Leading and trailing whitespace of the password is removed. Only AUTH PLAIN is used.

```json
"mail": {
  "provider": "smtp",
  "smtpHost": "smtp.example.org",
  "smtpPort": 587,
  "smtpTls": "starttls",
  "smtpUser": "noreply@example.org",
  "smtpPassword": "<password, or leave out and use the env variable>",
  "fromEmail": "noreply@example.org",
  "fromName": "My CoreScope"
}
```

- `smtpTls` is `starttls` (default), `tls` (implicit TLS) or `none`, in any letter case.
  `none` is accepted only for the host `localhost`, `127.0.0.1` or `::1`.
- `smtpPort` defaults to 587 with `starttls`, 465 with `tls` and 25 with `none`. 0 or
  leaving it out means the default. Otherwise 1 to 65535.
- With `starttls` and `tls` the server certificate is always verified against
  `smtpHost`. There is no option to skip the check.
- `smtpHost` may be an IPv6 literal in brackets, e.g. `[2001:db8::25]`. Verification
  then needs a certificate that names that address.
- A line break in `mail.fromName` is a startup error with SMTP.
- An address with non-ASCII characters (e.g. `jürgen@example.org`) needs a server that
  supports SMTPUTF8. Without it the mail fails with "the recipient address is not ASCII
  and the server does not support SMTPUTF8" (or "sender" for `mail.fromEmail`).
- `mail.webhookSecret` in the config is a startup error with SMTP, and
  `CORESCOPE_BREVO_WEBHOOK_SECRET` is ignored.
- At startup CoreScope connects, negotiates TLS (unless `smtpTls` is `none`) and logs in
  (only when `smtpUser` is set), then quits without sending
  anything. The log shows `[mail] SMTP ready: smtp.example.org:587 (starttls)` or
  `[mail] SMTP check failed: <error>`. After a failed check the server keeps starting.
- There is **no delivery status** with SMTP: the server only tells CoreScope that it
  accepted the mail. A user's mail shows as "sent (no delivery status with SMTP)",
  *Refresh status* is hidden and the admin overview shows one *Sent* figure instead of
  delivered, bounced, blocked and so on. Bounces arrive in the mailbox of the sender
  address, and CoreScope never flags an address as bouncing.

Everything else below applies to every provider, except where it names one.

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
| `mail.provider` | `brevo` (default), `postal` or `smtp`. |
| `mail.brevoApiKey` | Brevo only: the API key. `CORESCOPE_BREVO_API_KEY` wins over it. Required with Brevo. |
| `mail.postalBaseUrl` | Postal only: your Postal host, https (http only for localhost). Required with Postal. |
| `mail.postalApiKey` | Postal only: the key of the server's API credential. `CORESCOPE_POSTAL_API_KEY` wins over it. Required with Postal. |
| `mail.smtpHost` | SMTP only: the server. Required with SMTP. |
| `mail.smtpPort` | SMTP only: default 587 (`starttls`), 465 (`tls`), 25 (`none`). |
| `mail.smtpTls` | SMTP only: `starttls` (default), `tls` or `none` (localhost only). |
| `mail.smtpUser` | SMTP only: login name. Set together with the password. |
| `mail.smtpPassword` | SMTP only: the password. `CORESCOPE_SMTP_PASSWORD` wins over it. |
| `mail.webhookSecret` | Enables delivery status (below). At least 16 characters. With Postal, only letters, digits, `.`, `_` and `-`. Not allowed with SMTP. |

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

### Send a test mail

To check the mail setup without registering an account, open **Admin, Overview**, find
the *System* card and press *Send test mail*. It works with every provider and sends a
fixed message only to your own address (the address of the logged-in admin, never one you
type). Each admin and each IP address can send 3 per 10 minutes, and a failed attempt
counts too. The counter is in memory, so a restart resets it. The attempt is recorded in
the audit log as `mail.test` (result `ok` or `failed`). A mail that was sent also appears
in the mail log with purpose `test`. With Brevo or Postal, once the webhook is set up or
*Refresh status* is pressed, the delivery status of the test mail shows in your own
entry's details under **Admin, Users**, and a bounce of it flags your own address like
any other mail.

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
  it has traffic and regardless of the region filter. Messages stored before the
  approval are decrypted too: the ingestor re-decodes the stored messages it could
  not decrypt with the new key (#2133), and the server picks them up without a
  restart.
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
  (heard is the latest of its last advert and the newest packet that can only be this
  node: its own packets, and relay hops whose prefix no other repeater shares. A 1-byte
  hop shared with other repeaters is not counted, so a silent repeater is not kept
  online by a namesake prefix; the node page shows such a repeater as *Uncertain*, and
  its offline mail says that only its shared prefix was still seen). A watched node also reports a
  battery below `batteryThresholds.lowMv`, and recovers at `lowMv + 100` mV. Admins can
  add a new foreign node (once per node) and an observer going offline
  (`observerStaleMinutes`) or back (`observerOnlineMinutes`).
- Adaptive offline threshold (optional, off by default): with
  `"adaptiveOffline": { "enabled": true, "floorMinutes": 30, "minObservers": 3 }` in
  `notifications`, a watched node's offline window is learned from its own traffic
  instead of the fixed silent hours. The server counts the node's certain packets per
  hour over the week before its last certain packet (so an outage does not lower the
  rate), takes the 10th percentile as the rate λ so quiet nights do not alert, and
  reports the node offline after 14/λ hours of silence: the point where silence at that
  rate has less than a one-in-a-million chance. The result is at least `floorMinutes`
  and at most the role's silent hours. A repeater heard about 35 times an hour is thus
  reported after 30 minutes instead of 72 hours. Empty stretches are sorted first: one of
  at most 6 hours that comes back on another day at the same hours is the node's normal
  silence, and the window is never shorter than the longest such stretch plus an hour;
  any other empty stretch is an outage and is left out of the learning, so one outage
  neither stretches the window for a week nor stops the node from being learned. Nodes
  with less than one certain packet an hour at the 10th percentile, under a day of
  history in memory, or whose 10th-percentile hour was heard by fewer than
  `minObservers` observers keep the fixed window: when one observer carries a node, that
  observer's outage would look like the node's. Silence is measured up to the newest
  packet in the store, so a stalling feed stops every node's clock instead of mailing
  the nodes with short windows. The learned windows are recomputed every 10 minutes.
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
`/api/account/settings`, `/api/account/companions*`); anything else answers 403. Other apps can
ask for other scopes instead (see *Repeater info, Mobile apps*).

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

### Repeater info

With user management on, a repeater's node page has a *Repeater info* section: antenna height,
antenna gain, status (active, maintenance, decommissioned), power (mains, battery, solar) and
free notes (500 characters). There is no separate switch: it is on whenever `userManagement` is
enabled.

- **Who may edit:** a repeater nobody claimed can be edited by any logged-in active user. Once
  it has claimers, only its active claimers can. Admins can always edit. An unchanged save
  writes nothing.
- **Public names:** the display names of the claimers, and of the user who made the last edit,
  are shown to every visitor. Email addresses never are. Claims of a disabled user are kept
  but hidden and do not count, so a repeater whose only claimer is disabled is open again.
- **Audit log:** edits are recorded as `repeater.edit` (with the old and new value of each
  changed field), claims as `repeater.claim` and `repeater.unclaim`.
- **My repeaters:** *My account, My repeaters* lists the repeaters you claimed, silent ones
  first: when each was last heard (by the same rule as the node pages), its saved status, who
  else maintains it, and an *Unclaim* button. A repeater silent longer than
  `retention.nodeDays`, which the analyzer moves to its inactive nodes, stays listed by name as
  silent with its last advert. A repeater the analyzer has no record of at all is listed as no
  longer in the database until you unclaim it. Deep link: `#/account?section=repeaters`.
- **Removing a claim:** *Admin, Users*, open the user, *Remove* next to the claim. Admins use
  this when a claimer left or claimed a repeater that is not theirs.
- **Mobile apps:** an app logs in at `POST /api/auth/device-token` like CoreDrive RX, but asks
  for the scopes it needs: `repeater:claim` to claim a repeater and remove its own claim,
  `repeater:edit` to edit details (still only where the account may edit, as above). Either
  scope also lets the app read the account's own *My repeaters* list
  (`GET /api/account/repeaters`). The token shows under *My account, Devices*, and its claims and edits are recorded with `via: device`.
  Admin claim removal stays in the browser. There is no separate switch. An admin's
  `repeater:edit` token can edit the details of every repeater, as an admin can in the browser,
  for as long as the token lives (90 days, renewed with use); log it out under *Devices* when the
  device is lost or changes hands.
- **Edits from two places:** each save of the details counts up a `revision`. The node page and
  an app both send the revision they started from, and if someone saved in between, CoreScope
  refuses the save and returns the current details instead of overwriting them. The node page
  keeps what you typed and shows the saved values under the form: *Save* again replaces them,
  *Cancel* keeps them.

### Observer MQTT logins (optional)

`userManagement.observerLogins` lets every user request a personal MQTT login for their observers,
generated by CoreScope and sent only by mail, in place of the shared broker login. A login may
publish only under the observers bound to its user; an observer is bound on its first publish
with that login. The broker (EMQX) checks each connect and publish with CoreScope over HTTP and
keeps handling the shared login and bridges itself, also while CoreScope is down. Users find it
under *My account, Observer MQTT login*, admins under *Admin, Observer logins*.

Configuration, the EMQX setup and the migration steps: [Personal MQTT logins for
observers](../observer-mqtt-logins.md).

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
- activation, reset and email change links used since (valid again until they expire);
- with observer logins on: logins revoked since (they authenticate again), the username and
  password from before a reissue (the reissued ones stop working), and the observer bindings
  of the snapshot. Check *Admin, Observer logins* after a restore and revoke or reissue
  where needed.

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
- **Repeater info:** on a repeater's page, *Repeater info* shows what the community knows
  about it. *Claim this repeater* says you operate it; after that only you (and the other
  claimers and admins) can edit it. An unclaimed repeater can be edited by any logged-in
  user. Your display name is public next to your claims and edits, your email address is
  not. *Unclaim* removes your claim.
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
