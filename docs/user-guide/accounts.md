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
| `mail.webhookSecret` | Enables delivery status (below). At least 16 characters. |

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

If Brevo cannot reach your instance, use **Refresh status** in a user's details instead:
CoreScope then asks Brevo directly. "Opened" is indicative only. Some mail apps load
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
  the plain space count as invisible), case-sensitive, not Public. Emoji work.

### Backups

`users.db` holds password hashes and addresses. Back it up together with the analyzer
database, and protect it the same way. Deleting it removes all accounts and nothing else.

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
