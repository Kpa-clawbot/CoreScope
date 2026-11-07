# Personal MQTT logins for observers

Optional, off by default. Needs [accounts](user-guide/accounts.md) (`userManagement.enabled`).

Without this feature every observer publishes with one shared broker login. Anyone who has
that password can publish under any topic, including as an existing observer. With it, each
CoreScope user can request a personal MQTT login on their account page. CoreScope generates the
username (`obs_` + 12 characters) and the password, and mails them; they are never shown in the
interface or returned by the API. One login serves all of a user's observers.

The broker asks CoreScope over HTTP whether a login may connect and whether it may publish.
A personal login may only publish to `meshcore/<iata>/<pubkey>/<type>` (with a 64-hex pubkey,
and not under the `client` IATA that client uploads use), and only for observers bound to its
user:

| Pubkey in the topic | Publish | Effect |
|---|---|---|
| bound to this login's user | allowed | none |
| bound to another user, or orphaned (owner deleted) | denied | logged with the username and the first 8 characters of the pubkey |
| not bound yet | allowed | bound to this user now (`auto`); admins get a mail if CoreScope already knew the observer |

Automatic binding stops once a user owns 20 observers, counting the ones an admin assigned;
past that, publishing under a new pubkey is denied until an admin assigns it
(`PUT /api/admin/observer-bindings/<pubkey>`). Subscribing is always denied for personal
logins. Admins revoke and reissue logins, and unbind or reassign observers, on *Admin,
Observer logins*.

When a login is created, the owner's notification events get "My observers go offline"
(`observer.offline.own`). Mails only go out once the user has switched notifications on; the
user can untick the event on the account page.

**Who can claim an observer.** Any active account can request a login, and that login binds
every observer it publishes for that is not bound yet, including observers CoreScope already
knows from the shared login (first come wins). The observer pubkeys are public on
`/api/observers`. With open registration this means: anyone with a mail address can claim up
to 20 unbound observers per account, and publish as them. Before this feature, publishing
needed the shared password you hand out. What limits it:

- every claim of an observer CoreScope already knew mails the admins (`observer-bound`);
- an admin can unbind or reassign a claimed observer, and revoke the login, on
  *Admin, Observer logins*;
- a claimed observer cannot be claimed again by another login, so the sooner the real owners
  migrate, the smaller the window.

Registration cannot be closed. If this is not acceptable for your instance, assign the known
observers to their owners yourself (once the owners have a login) before you announce the
feature, and watch the `observer-bound` mails during the migration.

Logins CoreScope does not manage (the shared login, bridges, your own operator users) are
answered with `ignore`, so the broker decides them with its own user store, exactly as before.
The shared login therefore keeps working, and can still publish as any observer, until you
remove it from the broker.

Only EMQX 5 is covered here. The setup below was tested on EMQX 5.8.6 in Docker, and runs on a
production EMQX 5.8.9 with about 50 clients, set up through the REST API as described in
section 2.

## 1. Configure CoreScope

```json
"userManagement": {
  "enabled": true,
  "observerLogins": {
    "enabled": true,
    "hookSecret": "",
    "encryptionKey": "",
    "broker": { "host": "mqtt.example.org", "port": 8883, "transport": "tcp", "tls": true }
  }
}
```

| Key | Meaning |
|---|---|
| `hookSecret` | Shared secret the broker sends in the `X-CoreScope-Hook-Secret` header. Required, at least 16 characters. `CORESCOPE_OBSERVER_HOOK_SECRET` wins over it. Generate one with `openssl rand -hex 32`. |
| `encryptionKey` | AES-256-GCM key for the stored passwords: standard base64 of exactly 32 bytes (`openssl rand -base64 32`). Required. `CORESCOPE_OBSERVER_KEY` wins over it. Passwords are stored encrypted, not hashed, so they can be mailed again; the key is not in `users.db`, so a copied `users.db` alone does not reveal them. Without the original key the stored passwords cannot be decrypted, so re-sending fails: reissue the login instead. Key rotation is not supported. |
| `broker.host` | Host observers connect to, as written in the credentials mail. Required. |
| `broker.port` | Default 1883. |
| `broker.transport` | `tcp` (default) or `websockets`, as in meshcoretomqtt. |
| `broker.tls` | Whether observers connect with TLS. Default false. |
| `sharedUsernames` | The broker's own shared login names. Optional, default none. The authz hook denies them a publish under an observer whose owner has confirmed it; see [Locking the shared login out of migrated observers](#locking-the-shared-login-out-of-migrated-observers). |

The server refuses to start when the block is enabled without a hook secret, a valid key or a
host. The `broker` values only fill the mail; CoreScope never connects to the broker.

With the feature on, two endpoints exist for the broker:

- `POST /api/mqtt-hook/emqx/authn`, body `{"username", "password", "clientid"}`
- `POST /api/mqtt-hook/emqx/authz`, body `{"username", "topic", "action"}`

Both answer `{"result": "allow" | "deny" | "ignore"}`, `401` without the right secret, and `503`
when the login index could not be loaded. Let the broker reach them over your internal network.
They are not for browsers: if CoreScope is public, you can also block `/api/mqtt-hook/` at the
reverse proxy for everything except the broker.

The credentials mail holds the login in five forms: a meshcoretomqtt `[[broker]]` block, the
observer firmware commands (Gessaman or Dutch-MeshCore, same CLI), a Home Assistant meshcore-ha block, the OpenHop
broker form, and the plain values. The OpenHop block names format Letsmesh (Meshcoretomqtt works too) and warns
against the format labelled "openHop", which publishes to `meshcore/repeater/<name>`; it also turns on Publish
Neighbours, whose `neighbors` topic the ingestor reads for region scopes. The firmware block ends with
`set mqtt.neighbors on`, the firmware's switch for the same report (every 24 hours by default, #1865); the
note tells owners of older firmware, which lacks the command, to skip it. The firmware commands use slot 4 (`set mqtt4.*`): on a fresh install slots 1-2 (Gessaman, Let's Mesh US/EU)
and 1-3 (Dutch-MeshCore) hold default broker presets, and slot 4 is empty on both. For the firmware the server
line is the bare host (`set mqtt4.server mqtt.example.org`): the firmware chooses the scheme
from the port alone (8883 TLS, 443 secure websockets, anything else plain MQTT), and firmware
before 2026-10-19 does not accept a scheme in the server. Only when the port would pick the
wrong scheme (for example TLS on 8884) does the mail write one (`mqtts://mqtt.example.org`),
which needs firmware from 2026-10-19 or later.

Websockets: meshcoretomqtt connects with websocket path `/`, EMQX listens on `/mqtt` by
default. Match the listener path to your uploaders, or use `tcp`.

## 2. Configure EMQX

Two chains, each with the CoreScope hook in front of what you have now.

**Authentication**: the HTTP authenticator first, your built-in database second. For a login
CoreScope does not know, the hook answers `ignore` and EMQX moves on to the built-in database.

**Authorization**: the HTTP authorizer first, then the built-in database, and last a file
authorizer that denies every `obs_` username. Why that last rule: with
`authorization.no_match = allow` and CoreScope unreachable, the HTTP
authorizer gets no answer, so a personal login that is already connected would fall through to
`no_match` and could publish under any topic. Measured on EMQX 5.8.6: without the rule, a
connected `obs_` login published to an unbound pubkey while CoreScope was stopped and the message
was delivered; with the rule it was denied.

As read-only config in `etc/emqx.conf` (in the dashboard: *Access Control, Authentication* and
*Authorization*, with the HTTP entries dragged to the top):

```hocon
authentication = [
  {
    mechanism = password_based
    backend = http
    enable = true
    method = post
    url = "http://<corescope-host>:<port>/api/mqtt-hook/emqx/authn"
    headers {
      "content-type" = "application/json"
      "X-CoreScope-Hook-Secret" = "<hook-secret>"
    }
    body {
      username = "${username}"
      password = "${password}"
      clientid = "${clientid}"
    }
    request_timeout = "5s"
  },
  {
    mechanism = password_based
    backend = built_in_database
    user_id_type = username
    password_hash_algorithm { name = sha256, salt_position = suffix }
  }
]

authorization {
  no_match = allow
  deny_action = ignore
  cache { enable = true, max_size = 32, ttl = 1m }
  sources = [
    {
      type = http
      enable = true
      method = post
      url = "http://<corescope-host>:<port>/api/mqtt-hook/emqx/authz"
      headers {
        "content-type" = "application/json"
        "X-CoreScope-Hook-Secret" = "<hook-secret>"
      }
      body {
        username = "${username}"
        topic = "${topic}"
        action = "${action}"
      }
      request_timeout = "5s"
    },
    { type = built_in_database, enable = true },
    { type = file, enable = true, path = "${EMQX_ETC_DIR}/corescope_acl.conf" }
  ]
}
```

`corescope_acl.conf`:

```erlang
{deny, {username, {re, "^obs_"}}, all, ["#"]}.
```

If you already use a file authorizer, add that line to your file instead, above any
`{allow, all}.` line. Do not create built-in users whose name starts with `obs_`: the rule above
denies them, and the prefix belongs to CoreScope.

Keep the settings of your existing built-in database entry (user id type, password hash); the
values above are only an example.

`no_match`: the setup above keeps `allow`, so the shared login and bridges keep their current
rights. With `no_match = deny` every non-CoreScope user needs explicit allow rules in the
built-in database or the file.

The authorization cache (`ttl = 1m` above) means a revoke or an unbind stops publishing on a
topic that was allowed in the last minute only when that cache entry expires. Revocation is
checked on every connect; an open connection stays open. Kicking it by hand
(`emqx ctl clients kick <clientid>`) was not tested.

### On a running broker, with the REST API

The same chains can be added to a live EMQX through its management API, without a restart and
without dropping connected clients. This is the order used on a production broker (EMQX 5.8.9,
about 50 clients). After each step, check that nothing else changed before going on.

Set `E=http://<emqx-host>:18083/api/v5` and use an API key (`-u <key>:<secret>`). Put the hook
URL and secret in two JSON files and send them with `--data-binary @file`, so the secret does
not end up in your shell history.

**0. Baseline.** Note the connection count and the drop rate:
`curl -s -u ... $E/monitor_current` (`connections`, `dropped_msg_rate`).

**1. The `^obs_` deny rule, last in the authorization chain.** No `obs_` user exists yet, so
this changes nothing:

```bash
curl -s -u ... -X POST -H 'Content-Type: application/json' $E/authorization/sources \
  -d '{"type":"file","enable":true,"rules":"{deny, {username, {re, \"^obs_\"}}, all, [\"#\"]}.\n"}'
curl -s -u ... -X POST -H 'Content-Type: application/json' $E/authorization/sources/file/move \
  -d '{"position":"rear"}'
```

A new source is added at the **front** of the chain, so the `move` is needed. `GET
$E/authorization/sources` must list `file` last.

**If `GET $E/authorization/sources` already lists a `file` source** (the stock EMQX config
can ship one with an `acl.conf` that ends in `{allow, all}.`), the POST above is refused:
EMQX keeps one source per type. Add the rule to the existing file instead, as its **first**
line, so no `allow` rule in that file matches an `obs_` login before it:

```bash
curl -s -u ... $E/authorization/sources/file > file.json   # keep the other fields
# edit file.json: prepend {deny, {username, {re, "^obs_"}}, all, ["#"]}.\n to "rules"
curl -s -u ... -X PUT -H 'Content-Type: application/json' $E/authorization/sources/file \
  --data-binary @file.json
```

Leave that source where it is in the chain: moving it would also move its other rules. The
HTTP authorizer goes in front of it in step 2, so it only sees what the hook ignored. Do not go
on to step 2 until the rule is in place; without it, a connected personal login can publish
anywhere while CoreScope is unreachable (section 3).

**2. The HTTP authorizer, first in the chain.** `authz.json`:

```json
{"type": "http", "enable": true, "method": "post",
 "url": "https://<corescope-host>/api/mqtt-hook/emqx/authz",
 "headers": {"content-type": "application/json", "X-CoreScope-Hook-Secret": "<hook-secret>"},
 "body": {"username": "${username}", "topic": "${topic}", "action": "${action}"},
 "request_timeout": "5s",
 "ssl": {"enable": true, "verify": "verify_peer", "server_name_indication": "<corescope-host>"}}
```

`POST $E/authorization/sources` with it lands at the front. Leave out the `ssl` block for a
plain `http://` URL on an internal network. Check `GET $E/authorization/sources/http/status`:
`status` is `connected`, and after a minute (the authorization cache) `metrics.ignore` grows
while `deny` stays 0. Every existing login is answered with `ignore`, so the connection count
and the drop rate do not change.

**3. The HTTP authenticator, first in the chain.** `authn.json`:

```json
{"mechanism": "password_based", "backend": "http", "enable": true, "method": "post",
 "url": "https://<corescope-host>/api/mqtt-hook/emqx/authn",
 "headers": {"content-type": "application/json", "X-CoreScope-Hook-Secret": "<hook-secret>"},
 "body": {"username": "${username}", "password": "${password}", "clientid": "${clientid}"},
 "request_timeout": "5s",
 "ssl": {"enable": true, "verify": "verify_peer", "server_name_indication": "<corescope-host>"}}
```

```bash
curl -s -u ... -X POST -H 'Content-Type: application/json' $E/authentication --data-binary @authn.json
curl -s -u ... -X PUT $E/authentication/password_based:http/position/front
```

`GET $E/authentication` must list `password_based:http` first. Test both paths with any MQTT
client: the shared login still gets CONNACK 0, and an unknown `obs_` username is refused with
`not authorised`. `GET $E/authentication/password_based:http/status` then shows `nomatch` for
both: the hook answered `ignore` and the built-in database decided.

**Rollback:** disable or delete the two `http` entries; the built-in database answers alone
again.

**Uploaders that already tried.** A user who configured a personal login before step 3 was
refused, and some uploaders stop retrying after that. They need a restart of the uploader or
the observer once the authenticator is in place. Personal logins appear under
`GET $E/clients?like_username=obs_`, and on *Admin, Observer logins* in CoreScope ("Last auth").

**Reverse proxy.** When the broker reaches CoreScope through its public HTTPS name, the proxy
must pass the `X-CoreScope-Hook-Secret` header through (most do by default). A `401` from
`curl -X POST https://<corescope-host>/api/mqtt-hook/emqx/authn` on the broker host shows that
the route is reachable; with the secret header and `{"username":"x","password":"x","clientid":"x"}` the answer is
`{"result":"ignore"}`.

## 3. When CoreScope is down

Measured on EMQX 5.8.6 with CoreScope stopped (the broker's connections to it timed out):

| Who | Result |
|---|---|
| Built-in user (the shared login), new connection | connects and publishes. The HTTP authenticator fails and EMQX moves on to the built-in database. In the test the first connection waited about 15 s (the HTTP connector's `connect_timeout`, 15 s by default) and its first publish another 5 s (`request_timeout`); once EMQX had marked the HTTP resource down, a new connection and publish took about 1 s. |
| Personal login, new connection | refused (`not authorised`): no authenticator knows it. |
| Personal login, already connected | publishing to a topic not in its authorization cache is denied, by the `^obs_` file rule. |

Personal logins work again when CoreScope is back and EMQX's next health check of the HTTP
resource passes (EMQX's default health-check interval; the resource stays marked down until then).

**Short outages while CoreScope is up.** EMQX can also mark the HTTP authenticator down for one
health-check interval while CoreScope is running fine. When CoreScope (or a proxy in front of
it) closes an idle keep-alive connection, and an EMQX health check lands in the moment between
the connection closing and its client process exiting, the check fails with
`resource down: closed`. In the test this happened 3 times (once in a manual round, twice in
one 150 s run of logins every 7 s) and lasted 15 s each time (alarm activated to deactivated).
Five further runs of 150 to 300 s, including one with four parallel clients and a server that
closes idle connections after 1 s, had none: it is a timing race, not a steady fault. During such a window a new connection with a
personal login is refused (`not authorised`) and the uploader has to reconnect; the shared login
and bridges are not affected. EMQX 5.8.6 has no setting that prevents it: the HTTP
authenticator exposes no keepalive or health-check options, and EMQX 5.10.5 runs the same
health-check code. Watch for the alarm in the EMQX log (`alarm_is_activated ...
emqx_authn_http`).

## 4. Migrating from the shared login

1. Enable the feature in CoreScope and restart it.
2. Add the HTTP authenticator and authorizer and the `^obs_` rule to EMQX as above. Check that the
   shared login and your bridges still connect and publish.
3. Ask observer owners to create an account, request a login under *My account, Observer MQTT
   login* and reconfigure their uploader with the mailed values. Each observer is bound to them
   on its first publish.
4. Follow progress on *Admin, Observer logins*: it lists every login, and every observer with
   its owner or "not migrated".
5. When enough observers moved, delete the shared login from the EMQX built-in database. Until
   then anyone with the shared password can still publish as any observer, bound or not.

Bridges (mosquitto bridges that forward many observers) and operator users stay in the built-in
database; this feature does not replace them.

### Locking the shared login out of migrated observers

Step 5 is all or nothing. To close the gap per observer while the shared login stays, list it in
`sharedUsernames`:

```json
"observerLogins": {
  "sharedUsernames": ["meshcore_shared"]
}
```

The authz hook then answers `deny` when one of these logins publishes to any topic that starts
with `meshcore/<iata>/<pubkey>` (any number of further segments, including none) and that
pubkey is **confirmed**: bound to a user whose own
personal login has published under it. CoreScope logs
`publish denied: user=<shared> observer=<8 hex>: confirmed owner`. Every other request from a
shared login (unbound, unconfirmed or orphaned observers, other topics, subscribe) keeps the
`ignore` it gets today, so the built-in database decides as before.

When a binding counts as confirmed:

- An observer bound by its owner's first publish is confirmed from that moment.
- An observer an admin assigns (*Reassign*) is not confirmed until the new owner's login
  publishes under it once. Reassigning to the same owner keeps the confirmation.
- Reassigning to another owner, unbinding, or deleting the owner's account clears it.

`sharedUsernames` entries are trimmed and empty ones dropped; a name starting with `obs_` is a
personal login and stops the server at startup.

Risk: an owner who confirmed an observer and also still runs a second uploader on the shared
login for the same observer (an old meshcoretomqtt install, a second firmware slot) loses that
second uploader: its publishes are denied. Check with the owner before you add the shared login
here, or watch the CoreScope log for the `confirmed owner` line.

*Admin, Observer logins* shows per observer a **Confirmed** column: the date the owner's login
first published as it, "not yet" for a binding that is not confirmed, and a dash for an observer
without a binding. `GET /api/admin/observer-logins` has the same value as `confirmedAt` on each
binding (null when unconfirmed).
