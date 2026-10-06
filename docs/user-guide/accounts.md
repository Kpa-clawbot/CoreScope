# Accounts (optional user management)

CoreScope runs without accounts by default: every page is public and settings live in
each visitor's browser. Operators can turn on **accounts**. The dashboard stays public;
logging in only adds things.

- Visitors can register with an email address, a display name and a password, and
  activate the account through a link mailed to that address.
- **Admins** manage users (activate, disable, delete, promote) and can use the operator
  actions (geofilter save, prune, backup, perf reset) without the API key.

With the feature off (the default) the account routes are not registered: requests to
`/api/auth/*`, `/api/account/*` and `/api/admin/users/*` fall through to the normal page,
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
**Users**.

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

- In **Users**, *Resend mail* sends a fresh activation link to a pending account.
- *Activate* activates a pending account by hand. The address is then unverified; the
  row shows "manual" and the audit log records who did it.

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

Admins can see your display name and email address in the Users list.
