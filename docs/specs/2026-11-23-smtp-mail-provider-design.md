# SMTP as a mail provider, plus a test mail

Issue: #2463

## Why

Account mail (activation, password reset, address change, node notifications) can go
through Brevo or Postal only. An operator who already has a mail server, or a mailbox at a
provider that offers SMTP submission, has to sign up for Brevo or run Postal just for
CoreScope. A plain SMTP provider removes that step. Operators also have no way to check a
mail setup short of registering a test account, so a test mail comes with it.

## Goals

- `userManagement.mail.provider: "smtp"` sends every account mail through an SMTP
  submission server.
- A broken SMTP setup shows up at startup, not at the first registration.
- An admin can send a test mail from the admin page, for every provider, and sees the
  provider's or server's own error text when it fails.

## Non-goals

- Delivery status for SMTP. SMTP has no event API: bounces arrive as mail in the sender's
  mailbox. CoreScope does not read that mailbox, so an SMTP setup never flags an address
  as bouncing.
- DKIM signing. That is the mail server's job.
- Auth methods other than PLAIN (no CRAM-MD5, no OAuth2 / XOAUTH2).
- A connection pool or a send queue. Mail volume is a few hundred per day at most; one
  connection per mail is enough.

## Anti-goals

- No new Go dependency.
- No password or SMTP credential in any log line, error returned to the browser, or audit
  row.
- No plain-text login over an unencrypted connection to a remote host.

## Approaches

- **A. Go standard library (chosen).** `net/smtp` and `crypto/tls` for the connection, a
  small MIME builder of our own for `multipart/alternative` (text and HTML,
  quoted-printable) with our own `Message-ID`. No dependency. Costs a few hundred lines that
  we test ourselves; the risk is contained because every message body comes from CoreScope.
- B. A library such as `go-mail`. Shorter code, MIME and DKIM included, but a new
  dependency to justify upstream for features we do not need.
- C. A local `sendmail` binary. Not in the Docker image, and no error text to show in the
  test. Dropped.

## Design

### Config

```json
"mail": {
  "provider": "smtp",
  "smtpHost": "mail.example.org",
  "smtpPort": 587,
  "smtpTls": "starttls",
  "smtpUser": "corescope@example.org",
  "smtpPassword": "",
  "fromEmail": "noreply@example.org",
  "fromName": "CoreScope"
}
```

- `smtpHost` required with `smtp`.
- `smtpTls`: `starttls` (default), `tls` (implicit TLS, usually port 465) or `none`.
  `none` is allowed only when `smtpHost` is `localhost`, `127.0.0.1` or `::1`, the same rule
  as Postal's http base URL.
- `smtpPort`: default 587 for `starttls`, 465 for `tls`, 25 for `none`.
- `smtpUser` and `smtpPassword` are optional (a local relay may need no login). Set one,
  set both. `CORESCOPE_SMTP_PASSWORD` wins over `smtpPassword`.
- Certificates are verified against `smtpHost`. No option to skip verification.
- `webhookSecret` is rejected with `smtp` at startup: there is no webhook to secure, and a
  silently ignored secret would suggest delivery status that does not exist.

### Sending (`internal/mailer/smtp.go`)

- `NewSMTP(cfg)` returns a `Mailer`. `Send` opens one connection per mail with a 30 s
  overall deadline (and the request context), does STARTTLS or implicit TLS, `AUTH PLAIN`
  when a user is set, `MAIL FROM` = `fromEmail`, `RCPT TO`, `DATA`, `QUIT`.
- The message carries `From` (with `fromName`, RFC 2047 encoded when not ASCII), `To`,
  `Subject` (RFC 2047), `Date`, `Message-ID` (`<random@fromEmail domain>`),
  `MIME-Version`, the caller's extra `Headers` (for example `List-Unsubscribe`), and a
  `multipart/alternative` body with the text part first, both quoted-printable UTF-8.
- Header values are checked for CR and LF before use; a value containing one fails the
  send (no header injection).
- `Send` returns the `Message-ID` as the provider message id, so the mail log has an id to
  show.
- `Events` returns no events and no error.
- Errors carry the server's reply (`535 5.7.8 authentication failed`) but never the
  password.

### Startup check

- When the provider is `smtp`, the server dials, does TLS and AUTH, then `QUIT`, without
  sending. On failure it logs `[mail] SMTP check failed: <error>` and starts anyway, so a
  mail server that is down for a moment does not take CoreScope down. On success it logs
  one line naming host, port and TLS mode.
- Brevo and Postal keep their current startup behaviour (none). The test mail below is
  their check.

### Test mail

- `POST /api/admin/mail/test` (admin session, CSRF like the other admin POSTs) sends a
  short fixed test mail to the calling admin's own address, through the configured
  provider. The address is not a parameter, so the endpoint cannot be used to send mail to
  arbitrary addresses.
- Rate limit: 3 per admin per 10 minutes, with the existing `rateLimiter`.
- Answer: `{ok: true, provider, messageId}`, or `{ok: false, provider, error}` with the
  provider's or server's error text (502). An audit row `mail.test` records who sent it and
  whether it worked.
- The mail is recorded in the mail log like any other mail (tag `test`), so with Brevo or
  Postal its delivery status appears on the admin's own user page.
- UI: a **Send test mail** button in the System card of the admin Overview tab
  (`public/admin-overview.js`), with the result shown next to it.

### Delivery status in the UI

- With `smtp`, the user detail page shows the mail as `sent` and hides **Refresh status**.
  The admin stats count SMTP mail as sent only.

### Docs

- `docs/user-guide/accounts.md`: a section "Or: use your own SMTP server" next to the
  Postal one, the config table rows, the no-delivery-status limitation, and the test mail.
- `config.example.json`: the new keys and an updated `_comment_userManagement`.
- `docs/api-spec.md` and `cmd/server/openapi.go`: the new endpoint.
- `CHANGELOG.md`.

## Tests

- `internal/mailer/smtp_test.go` against an in-process SMTP server on a local listener
  (plain, STARTTLS and implicit TLS with a self-signed certificate trusted by the test):
  the conversation, AUTH PLAIN, the exact MIME output, RFC 2047 encoding, CR/LF rejection,
  a 535 reply surfacing as an error without the password, the deadline.
- `cmd/server/user_mgmt_config_test.go`: required fields, defaults per TLS mode, `none`
  only for localhost, user without password and the reverse, env wins, `webhookSecret`
  rejected.
- `cmd/server`: the test-mail handler with the Fake mailer (success, failure text, rate
  limit, non-admin 403, audit row, mail log row) and the startup check (logs, does not
  stop startup).
- Frontend unit test for the button state and the result text; one E2E step in the admin
  suite with the fake provider.

## Left to the builder

- Exact file split inside `internal/mailer` and `cmd/server`.
- Wording of log lines and UI text, within the rules above.
