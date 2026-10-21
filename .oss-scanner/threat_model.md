# CoreScope threat model

CoreScope collects MeshCore LoRa mesh packets from MQTT, decodes them, stores
them in SQLite and serves a public web analyzer (REST API, WebSocket feed and a
vanilla-JS frontend). Operators run it as a public website for their regional
mesh. Optional user accounts (`userManagement.enabled`) add registration,
login, admin roles, companion-device linking and email notifications.

## Where untrusted input enters

Treat all of these as attacker-controlled:

- **Radio packets.** Anyone with a LoRa radio in range of an observer can
  transmit arbitrary bytes. Observers forward them as hex in MQTT JSON. The
  bytes are parsed by `cmd/ingestor/decoder.go` and `cmd/server/decoder.go`
  (`DecodePacket`), `internal/packetpath`, `internal/channel` (AES-ECB channel
  decrypt) and `internal/sigvalidate` (ed25519 advert and message signatures).
  Node names, advert data and channel message text end up in the database and
  are rendered by the frontend.
- **MQTT messages.** `cmd/ingestor/main.go` `handleMessage` parses the topic
  and the JSON payload, including the `meshcore/client/{pubkey}/...` topics
  that mobile companions publish. A broker may accept publishes from many
  observers, so assume a malicious observer can publish any topic and payload.
- **Unauthenticated HTTP.** Every `GET /api/*` read endpoint, plus
  `POST /api/decode`, `POST /api/packets/observations` and
  `POST /api/paths/inspect` (`cmd/server/routes.go`). The SPA and static file
  handler in `cmd/server/main.go` (`spaHandler`, `isSafeStaticPath`).
- **WebSocket** `/ws` (`cmd/server/websocket.go`): unauthenticated, origin
  checked, per-IP limits in `ws_limits.go`.
- **User management** (`cmd/server/auth_*.go`, `internal/users`): register,
  activate, login, password reset, email change, device tokens, companion
  linking challenges, channel proposals, notification unsubscribe tokens, and
  the mail provider webhooks (`internal/mailer`).

Trusted: the operator, `config.json`, the host, the SQLite files on disk, and
the MQTT broker configuration itself. `cmd/decrypt` and `cmd/migrate` are
operator CLI tools that read local files.

## Security properties that matter most

- Malformed packets, topics or JSON must not crash, hang or exhaust the
  ingestor or server. One bad packet stopping ingest takes the whole site down.
- Attacker-controlled strings (node names, channel messages, advert fields,
  observer names) must never execute as script in the frontend. The shared
  escaper is `escapeHtml` in `public/app.js`; several files keep a local copy.
- Authentication and authorization: a normal user must not reach admin
  endpoints (`requireAdmin`, `withAdmin`), a device token (scope `rx`) must
  stay inside its allowed routes, sessions and CSRF checks
  (`cmd/server/auth_session.go`) must hold, and account flows (reset,
  activation, email change, companion linking) must not allow account
  takeover or enumeration beyond what the UI shows.
- Read/write separation: `cmd/server` opens the analyzer database read-only.
  Only `cmd/ingestor` writes it; `users.db` is the one database the server
  writes (see `AGENTS.md`). A path that lets HTTP input write to the analyzer
  database is a finding.
- Unauthenticated endpoints must not expose secrets: `config.json` values such
  as `apiKey`, channel keys, mail credentials, or other users' email addresses.

## How to exercise it

The checkout is at `/src`. Binaries are in `/src/dist/` (`make build`).

```sh
cd /src
make test            # go test ./... in every module (scripts/allmod.sh)
sh test-all.sh       # frontend unit tests (node, no browser)
```

Go tests use temporary SQLite databases, `httptest` servers and local fake MQTT
listeners; none need the internet. `cmd/server` has handler tests for most
routes, which are the easiest place for a reproducer. Decoder tests live next
to each `decoder.go`. Browser E2E tests in `tests/e2e/` need Playwright
browsers and are not installed in this image. `test-fixtures/*.db` is excluded
by `.dockerignore`, so the two tests that use it skip.

## How we rate severity

- **Critical:** remote code execution; authentication bypass to admin; reading
  or writing another user's account without credentials.
- **High:** stored XSS from radio or MQTT data (it reaches every visitor
  without interaction); account takeover needing user interaction; an
  unauthenticated request or a single packet/MQTT message that crashes the
  server or ingestor; disclosure of `config.json` secrets.
- **Medium:** reflected XSS; CSRF on a user action; privilege escalation
  between user-level features; resource exhaustion that needs sustained
  traffic; user enumeration.
- **Low:** issues that need admin access or a malicious operator config,
  missing hardening without a demonstrated impact.

## Leave alone

- MeshCore protocol design: channel encryption is AES-128-ECB with a 2-byte
  MAC because the firmware defines it that way. Public hashtag channel keys are
  derived from the channel name by design. Report a CoreScope bug only if
  CoreScope handles the protocol differently from the firmware.
- Data that is public by design: every packet, node, position and channel
  message heard on the mesh is published on purpose.
- `rejectUnauthorized: false` and `ENABLE_PPROF` are operator opt-ins.
- Dependencies, unless the vulnerable code is reachable through CoreScope.
