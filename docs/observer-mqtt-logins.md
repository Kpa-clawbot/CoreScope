# Personal MQTT logins for observers

Optional, off by default. Needs [accounts](user-guide/accounts.md) (`userManagement.enabled`).

Without this feature every observer publishes with one shared broker login. Anyone who has
that password can publish under any topic, including as an existing observer. With it, each
CoreScope user can request a personal MQTT login on their account page. CoreScope generates the
username (`obs_` + 12 characters) and the password, and mails them; they are never shown in the
interface or returned by the API. One login serves all of a user's observers.

The broker asks CoreScope over HTTP whether a login may connect and whether it may publish.
A personal login may only publish to `meshcore/<iata>/<pubkey>/<type>`, and only for observers
bound to its user:

| Pubkey in the topic | Publish | Effect |
|---|---|---|
| bound to this login's user | allowed | none |
| bound to another user, or orphaned (owner deleted) | denied | logged with the username and the first 8 characters of the pubkey |
| not bound yet | allowed | bound to this user now (`auto`); admins get a mail if CoreScope already knew the observer |

A user can auto-bind at most 20 observers; past that, publishing under a new pubkey is denied
until an admin assigns it (`PUT /api/admin/observer-bindings/<pubkey>`). Subscribing is always
denied for personal logins. Admins revoke and reissue logins, and unbind or reassign observers,
on *Admin, Observer logins*.

Logins CoreScope does not manage (the shared login, bridges, your own operator users) are
answered with `ignore`, so the broker decides them with its own user store, exactly as before.
The shared login therefore keeps working, and can still publish as any observer, until you
remove it from the broker.

Only EMQX 5 is covered here. The setup below was tested on EMQX 5.8.6.

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
| `hookSecret` | Shared secret the broker sends in the `X-CoreScope-Hook-Secret` header. Required. `CORESCOPE_OBSERVER_HOOK_SECRET` wins over it. Generate one with `openssl rand -hex 32`. |
| `encryptionKey` | AES-256-GCM key for the stored passwords: standard base64 of exactly 32 bytes (`openssl rand -base64 32`). Required. `CORESCOPE_OBSERVER_KEY` wins over it. Passwords are stored encrypted, not hashed, so they can be mailed again; the key is not in `users.db`, so a copied `users.db` alone does not reveal them. Without the original key the stored passwords cannot be decrypted, so re-sending fails: reissue the login instead. Key rotation is not supported. |
| `broker.host` | Host observers connect to, as written in the credentials mail. Required. |
| `broker.port` | Default 1883. |
| `broker.transport` | `tcp` (default) or `websockets`, as in meshcoretomqtt. |
| `broker.tls` | Whether observers connect with TLS. Default false. |

The server refuses to start when the block is enabled without a hook secret, a valid key or a
host. The `broker` values only fill the mail; CoreScope never connects to the broker.

With the feature on, two endpoints exist for the broker:

- `POST /api/mqtt-hook/emqx/authn`, body `{"username", "password", "clientid"}`
- `POST /api/mqtt-hook/emqx/authz`, body `{"username", "topic", "action"}`

Both answer `{"result": "allow" | "deny" | "ignore"}`, `401` without the right secret, and `503`
when the login index could not be loaded. Let the broker reach them over your internal network.
They are not for browsers: if CoreScope is public, you can also block `/api/mqtt-hook/` at the
reverse proxy for everything except the broker.

The credentials mail holds the login in three forms: a meshcoretomqtt `[[broker]]` block, the
Dutch-MeshCore observer firmware commands, and the plain values. For the firmware the server
line always carries a scheme (`set mqtt1.server mqtts://mqtt.example.org`), because a bare host
makes the firmware choose the scheme from the port alone (8883 TLS, 443 secure websockets,
anything else plain MQTT).

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

## 3. When CoreScope is down

Measured on EMQX 5.8.6 with CoreScope stopped (the broker's connections to it timed out):

| Who | Result |
|---|---|
| Built-in user (the shared login), new connection | connects and publishes. The HTTP authenticator fails and EMQX moves on to the built-in database. In the test the first connection waited about 15 s (the HTTP connector's `connect_timeout`, 15 s by default) and its first publish another 5 s (`request_timeout`); once EMQX had marked the HTTP resource down, a new connection and publish took about 1 s. |
| Personal login, new connection | refused (`not authorised`): no authenticator knows it. |
| Personal login, already connected | publishing to a topic not in its authorization cache is denied, by the `^obs_` file rule. |

Personal logins work again when CoreScope is back and EMQX reconnects its HTTP resource (EMQX
checks every 15 s).

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
