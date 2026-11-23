# Push Relay Contract (v1), Spec

**Date:** 2026-11-22
**Status:** Implemented in #2447. Reference relay: [anieto/nodescope-push-relay](https://github.com/anieto/nodescope-push-relay) (GPL-3.0), run by NodeScope at `https://push.nodescope.app`.
**Scope:** the HTTP contract between CoreScope and a push relay, and the account API a mobile app uses to link a device. Builds on node notifications (`2026-10-07-node-notifications-design.md`).

A change to anything in this document is a reviewed change in this repository. A relay implements this contract; CoreScope does not depend on any one relay.

---

## Why a relay

Mobile push goes through Apple (APNs) and Google (FCM), and both only accept pushes signed with the credentials of the app that receives them. Those credentials belong to the app's publisher, so a self-hosted analyzer cannot send to an app directly. A relay holds one app's credentials and forwards alerts from any analyzer that enrolled with it.

The split of responsibilities:

| CoreScope | Relay | App |
|---|---|---|
| Accounts, watches, preferences, when an alert fires | Device tokens, delivery through APNs/FCM, the visible alert text | Registers its token with its relay, links the device to an account on an analyzer |

CoreScope never sees a push token. The relay never sees an email address, account name, location or message. A relay reaches only the users of the app whose credentials it holds.

## Conventions

- HTTPS and JSON. CoreScope's requests are well under 8 KiB, the reference relay's limit. CoreScope sends no request to a relay over plain HTTP and does not follow redirects, so its sender credential stays on the configured origin.
- IDs are opaque, case-sensitive strings matching `^[A-Za-z0-9_-]{1,128}$`. Credentials are 32 random bytes as unpadded base64url (43 characters).
- Authenticated relay routes take `Authorization: Bearer <credential>`.
- Times are RFC 3339 in UTC.
- Errors are `{"error":{"code":"<code>"}}` with these statuses:

| Status | Meaning | Codes CoreScope can receive |
|---|---|---|
| 400 | Invalid request shape or values | `invalid_request`, `invalid_origin`, `invalid_link`, `invalid_delivery` |
| 401 | Missing or invalid credential | `invalid_credential` |
| 404 | Unknown resource, or one outside the caller's scope | `unknown_analyzer`, `unknown_proof`, `unknown_subscription`, `not_found` (route not served, for example self-enrollment turned off) |
| 409 | Conflicting replay | `analyzer_already_enrolled`, `proof_binding_conflict`, `delivery_conflict`, `sender_credential_already_used` |
| 410 | Known but expired or revoked | `analyzer_inactive`, `proof_expired`, `subscription_inactive`, `delivery_expired` |
| 422 | Origin verification failed | `origin_verification_failed` |
| 429 | Throttled or at a quota; may carry `Retry-After` (seconds) | `rate_limited`, `delivery_limit`, `subscription_limit`, `domain_enrollment_limit`, `analyzer_limit` |
| 503 | Temporary failure; may carry `Retry-After` | `service_unavailable` |

CoreScope acts on the status; codes are for logs and operators.

## Contract version

`contract_version` is `1`. The relay reports it in `GET /v1/analyzer` and CoreScope passes it to apps. A relay bumps it for any change CoreScope or an app must know about: a new route, a changed payload or status, or a new `kind`. Adding a `kind` is a version bump because the relay writes the alert text for each kind.

## Enrollment

Enrollment gives CoreScope an `analyzer_id` and a `sender_credential` for one relay. CoreScope keeps both in a private file next to `users.db` (`<users.db>.push-relay.json`, mode 0600), never in `config.json`.

### Self-service

The admin picks the relay on **Admin → Notifications → Mobile push**: a preset (NodeScope) or a custom HTTPS origin. CoreScope proves it controls its own public origin (`userManagement.publicBaseUrl`, HTTPS on port 443).

1. CoreScope generates a one-off Ed25519 key pair and a random sender credential. The credential is saved first (`<users.db>.push-relay-pending-<hash>.json`, named after the relay origin) so a retry reuses it.
2. CoreScope sends `POST /v1/analyzers/self-enroll`:
   ```json
   {"origin": "https://analyzer.example.org", "public_key": "<base64url Ed25519 public key>", "sender_credential": "<base64url 32 bytes>"}
   ```
3. The relay generates a 32-byte challenge and fetches `GET <origin>/api/push-relay/enrollment-proof?challenge=<base64url>` from the analyzer. It requires a public DNS hostname with public addresses only and a valid certificate, and rejects redirects. CoreScope answers only while this admin request is waiting for the relay's answer; otherwise the route answers 404. While it does answer, the body is:
   ```json
   {"signature": "<base64url Ed25519 signature>"}
   ```
   The signature covers the relay's own origin as well as the challenge, so a proof obtained by one relay does not verify at another:
   ```
   "corescope-push-relay-proof-v1" LF <relay origin> LF <decoded 32-byte challenge>
   ```
   `<relay origin>` is the origin CoreScope sent the enrollment to, in this form, with no trailing slash (for example `https://push.nodescope.app`):
   - `https://`;
   - a DNS hostname in lowercase ASCII, without a trailing dot (a host ending in more than one dot is rejected). An internationalized domain name is written in its A-label (`xn--`) form; any other host containing non-ASCII characters is rejected. IP literals (IPv4 or IPv6) are not valid relay origins;
   - `:<port>` in decimal without leading zeros, only when the port isn't 443. An empty port (`host:`) counts as none.

   The relay verifies against its own public origin in that form and rejects a signature over the bare challenge.
4. After verifying the signature, the relay answers `201 {"analyzer_id": "...", "sender_credential": "..."}`.
5. CoreScope confirms with `GET /v1/analyzer` (below) that the analyzer ID and origin match, then saves the enrollment and removes the pending file.

A retry with the same sender credential returns the same analyzer ID. An origin with an active enrollment and a different credential gets 409 `analyzer_already_enrolled`; revoke the old enrollment first. A relay may limit self-enrollment per registrable domain and in total (429).

### Manual

For a relay that issues one-time codes instead, or an analyzer the relay cannot reach:

1. The relay operator issues a code for the analyzer's origin (`POST /v1/operator/enrollment-codes`; operator-only, outside this contract).
2. `POST /v1/analyzers/enroll` with `{"code": "...", "request_id": "<stable id>"}` answers `201 {"analyzer_id", "sender_credential"}`.
3. The admin configures `userManagement.notifications.pushRelay` with `origin`, `analyzerId` and `platforms`, and sets `CORESCOPE_PUSH_RELAY_CREDENTIAL` in the environment.

### Status and revocation

| Request | Response |
|---|---|
| `GET /v1/analyzer` (sender credential) | `200 {"analyzer_id", "origin", "contract_version"}` |
| `DELETE /v1/analyzer` (sender credential) | `204`. Revokes the analyzer and all of its subscriptions at once. |

**Disconnect relay** in CoreScope pauses delivery, calls `DELETE /v1/analyzer`, and on 204 removes the local enrollment and device links. Until the relay confirms, the disconnect stays pending and delivery stays paused.

## Advertising push to apps

`GET /api/account/notifications` (signed-in session) includes, while an enrollment is active and not revoked:

```json
"pushRelay": {"relayOrigin": "https://push.nodescope.app", "analyzerId": "an_...", "contractVersion": 1, "platforms": ["ios", "android"]}
```

An app uses push only when `relayOrigin` is the relay it registers its own tokens with. It never registers a token with an origin taken from this response.

`platforms` comes from the relay preset, the `pushRelay.platforms` configuration, or defaults to iOS and Android for a custom relay.

## Device linking

The app first registers with its relay (outside this contract, see the relay's README): `POST /v1/installations` with its push token gives an installation ID and an installation credential, and `POST /v1/installations/{id}/link-proofs` with `{"analyzer_id"}` gives a one-time proof for this analyzer, valid for 5 minutes.

Then the app calls CoreScope with its account session and CSRF token:

| CoreScope route | Effect |
|---|---|
| `PUT /api/account/notifications/devices/{installationID}` with `{"proof"}` | Links or renews the caller's device. Answers `200 {"subscription_id", "status", "expires_at"}`. |
| `DELETE /api/account/notifications/devices/{installationID}` | Removes the caller's link and queues revocation at the relay. Answers 204, also when there was no link. |

To link, CoreScope stages a local binding with a random `binding_id`, then calls:

```
POST /v1/subscriptions          (sender credential)
{"installation_id": "...", "binding_id": "...", "proof": "..."}
→ 201 {"subscription_id": "...", "status": "active", "expires_at": "..."}
```

and activates the binding once the response is stored. Replaying the same binding and proof after an ambiguous failure returns the same subscription; a proof already used by a different binding gets 409 `proof_binding_conflict`. A subscription lasts 30 days; the app renews it by linking again with a fresh proof. CoreScope keeps at most 16 devices per account per analyzer, and one account per installation: linking from another account replaces the old link and revokes it.

Revocation is `DELETE /v1/subscriptions/{id}` with the sender credential, answering 204. CoreScope queues it when a device is unlinked and when a link is replaced.

Account export (`GET /api/account/export`) lists the account's links as `notifications.pushDevices` (`analyzerId`, `installationId`, `expiresAt`); binding IDs, proof hashes and subscription IDs stay out.

## Delivery

### When CoreScope sends

The notifier evaluates watches on its timer. Each offline or battery transition of a watched node becomes one job per linked device, written to `users.db` in the same transaction as the notification states, so an alert is never recorded without its deliveries or the other way round. Transitions are reported whatever the user's email setting; the email toggle and unsubscribe link only affect email.

| CoreScope event | Transition | `kind` |
|---|---|---|
| `node.offline` | not heard | `node.not_heard` |
| `node.offline` | heard again | `node.heard_again` |
| `node.battery` | low | `node.battery_low` |
| `node.battery` | recovered | `node.battery_recovered` |

Other notification events (observers, external alerts, new foreign nodes) stay email-only.

A worker drains the queue every 5 seconds. Before each send it checks again that the account is active, the node still watched, the event type still on and the link current; a job that no longer qualifies is dropped. Unlinking, unwatching or turning an event type off deletes pending jobs at once. **Pause delivery** drops queued jobs and skips new ones while keeping the links.

### The request

```
POST /v1/deliveries            (sender credential)
{
  "event_id": "<random, stable across retries>",
  "subscription_id": "sub_...",
  "kind": "node.not_heard",
  "node_pubkey": "<64 lowercase hex>",
  "node_name": "Repeater 1",
  "occurred_at": "2026-11-18T20:00:00Z",
  "expires_at": "2026-11-18T21:00:00Z"
}
```

- `node_name` is optional. CoreScope sends the node's name with control and bidirectional characters removed, as for mail subjects; the relay may shorten it. Without it, the relay uses generic wording.
- `expires_at` is after `occurred_at` and at most one hour later; `occurred_at` is not in the future. CoreScope sets it to one hour after the event.
- The request carries no alert text, title, URL, topic or project: the relay writes the alert for the `kind` and resolves the provider from the subscription.

The relay answers `202 {"delivery_id", "status"}` once the delivery is durably queued. The same `(analyzer, subscription, event_id)` again answers `200` with the original delivery; different contents for the same key answer 409 `delivery_conflict`.

### Retries

| Relay answer | CoreScope |
|---|---|
| 2xx | Done. |
| 429 or 5xx with `Retry-After` | Retries after that delay. |
| 429 or 5xx without it, or a network error | Retries with backoff from 5 seconds, doubling, at most 5 minutes. |
| Any other 4xx | Drops the job and logs it. |

A job is dropped once its retry would fall after `expires_at`. Revocation jobs follow the same rules.

## What the relay keeps

Per installation: the push provider, environment and token. Per subscription: the analyzer, installation, binding ID and expiry. Per delivery: the request above, until an hour after it expires. The reference relay encrypts its state at rest and logs no tokens, credentials or proofs. A relay operator documents its own retention.

## Out of scope

- The relay's app-facing routes (installation registration, token updates, link proofs) and the provider payload an app receives. They are between an app and its relay; see the reference relay's README.
- A `notifications` device-token scope for the device routes. They use the browser-style session and CSRF today; a token scope like the repeater scopes of #2429 is a possible follow-up.
- Other destinations. Email and the push relay share the notifier's destination interface (`cmd/server/notify_destination.go`); a further destination such as Pushover adds to it without changing this contract.
