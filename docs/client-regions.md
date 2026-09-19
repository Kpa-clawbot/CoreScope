# Client Region-Discovery Answers

A companion mobile app asks each repeater it comes within direct RF range of which regions that
repeater *declares* flood-allowed (`ANON_REQ_TYPE_REGIONS`), and relays the answer to CoreScope on a
dedicated topic. CoreScope already shows what each repeater is *observed* forwarding
(`transported_scopes`, derived from transport-route packets actually seen); this feature adds the
declared side, which is what makes a comparison between "claims" and "does" possible.

## Enabling region-discovery answers (operators)

Off by default. To turn it on:

1. In CoreScope's `config.json`, set `"clientRegions": { "enabled": true }` and restart the ingestor.
   Independent of `clientRxCoverage`, `clientRxObservations` and `clientRfSamples` — a deployment can
   run any combination.
2. **Required: an ACL-capable broker**, same as the other client streams. Bind
   `meshcore/client/{PUBLIC_KEY}/regions` so each companion may publish only under its own pubkey. The
   ingestor already subscribes under `meshcore/#`, so no separate subscription is needed.
3. Optionally set `retention.clientRegionsDays` to bound the table.

## MQTT topic & payload

Topic: `meshcore/client/{PUBLIC_KEY}/regions` — `{PUBLIC_KEY}` is the reporting companion's pubkey.

```json
{
  "type": "REGIONS",
  "timestamp": "2026-08-18T10:00:00.000Z",
  "target": "bb11223344556677889900aabbccddeeff00112233445566778899aabbccddaa",
  "regions": ["be", "be-vlg"],
  "truncated": false,
  "repeater_clock": 1755504000,
  "gps": { "lat": 51.2, "lon": 4.4, "acc_m": 8.0 }
}
```

- `target` is the repeater that was asked, as a lowercase-hex pubkey. It is payload data, not
  ACL-bound identity, so it is validated before being trusted — but validated strictly: exactly 64 hex
  characters, a full pubkey, not the looser 2-64-character rule used for the topic pubkey. The app
  cannot produce anything else — its request builder throws unless the pubkey it sends is exactly 64
  hex characters, so it can only ever ask about (and report back) a full pubkey. A shorter `target`
  therefore indicates a malformed or forged payload, not a legitimate shorthand worth keeping: a
  wrongly-accepted short value would still insert a row, but `CurrentDeclaredRegions` matches on the
  exact target string, so that row would never surface for the real node's queries — it would just
  accumulate silently as junk that looks like data. A missing, malformed, or wrong-length `target`
  drops the whole message.
- `regions` is the CSV-decoded list the repeater returned. **An empty list is a valid, deliberate
  answer** — the repeater declared nothing flood-allowed — and is stored as a row with an empty
  `regions_csv`, not dropped or treated as absence. A missing or malformed `regions` field (not an
  array, or containing a non-string entry) is a different thing entirely — a malformed payload, not an
  answer — and the whole message is dropped and logged rather than half-stored.
- `truncated` is a hint the app forwards as reported, not something the ingestor re-derives: the
  firmware's `exportNamesTo` skips names that don't fit the reply's CSV budget and keeps going, so an
  overflowing answer has holes rather than a truncated prefix, with no marker on the wire that lets
  either side detect it after the fact. The app flags `truncated` heuristically (close to its budget
  ceiling); the ingestor stores that flag verbatim.
- `gps` is **optional**, unlike the client coverage and RF-sample streams. A declared-regions answer is
  meaningful on its own — it says what a repeater claims to forward, regardless of where the companion
  was standing when it asked. A missing or out-of-range fix stores the row with `lat`/`lon` as SQL
  `NULL` rather than dropping it (contrast with `handleClientPacket`, which drops a position-less
  message outright because a coverage point without a position is not coverage at all). `gps.acc_m` is
  stored as `pos_acc_m`, the same column name `client_rf_samples` uses for this shape; a missing
  `acc_m` stores `NULL`, not `0`.
- `repeater_clock` is the repeater's own RTC reading at the moment it answered — the only signal on this
  topic that would reveal a repeater with a broken clock. Stored verbatim in the `repeater_clock` column
  via the same `optInt64` helper `handleClientRfSample` uses for this shape: a missing field stores
  `NULL`, never `0` — absent is not zero, the same rule enforced for `recv_errors` on the `/rf` topic.
- Subscription: the ingestor's default subscription (`meshcore/#`) already covers this topic. Sources
  configured with an explicit topic list must add `meshcore/client/+/regions`.

## Silence carries no meaning

There is no "did not answer" message on this topic. The repeater only replies to a `ANON_REQ_TYPE_REGIONS`
request delivered over a **DIRECT** route — a companion that sent the request but never received a
reply (out of range, rate-limited, busy, or old firmware that doesn't implement the request) has
nothing to publish, and publishes nothing. That means:

- **A row exists** ⇒ the repeater answered, and the row is that answer (however it reads — an empty
  list included).
- **No row for a target** ⇒ either it has never been asked, or it was asked and never answered. The
  absence itself is not evidence of anything about what the repeater declares — it must never be read
  as "declares nothing," which is what an empty-list row means.

## Trust

Identity = the reporting companion's pubkey, taken from the `{PUBLIC_KEY}` topic segment — never from a
payload-supplied field, which would defeat the ACL trust model. The ingestor rejects any topic pubkey
that is not lowercase hex before writing (the same `clientPubkeyRe` used across every client sub-topic),
and the observer blacklist is enforced before any client-topic write, so a blacklisted operator cannot
contribute region-discovery answers any more than it can contribute coverage or RF samples.

`target`, unlike the topic pubkey, is not ACL-enforced — anyone publishing on their own
`meshcore/client/{own_pubkey}/regions` topic could in principle claim any `target`. It is validated only
as a well-formed pubkey, not verified against the actual RF exchange; treat `node_declared_regions` as
one companion's report of what one repeater told it, not as ground truth independent of the reporting
companion's trustworthiness.

## Storage — `node_declared_regions` (ingestor-owned)

```
node_declared_regions(
  id, target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated, lat, lon,
  pos_acc_m, repeater_clock,
  UNIQUE(target, rx_pubkey, observed_at))   -- idempotent re-ingest; different reporters both persist
```

Every answer is stored as an **observation**, never as replacing state — this table accumulates a
history of answers rather than holding one row per target. `observed_at` is stored at **millisecond**
precision (the `rxTimeMillisLayout` format shared with `client_rx_observations.rx_at` and
`client_rf_samples.sampled_at`), not the second-resolution RFC3339 used by `client_receptions.rx_at`.
This matters for the same reason as those tables: a future retention prune compares its cutoff
lexicographically against these stored strings, and a second-resolution cutoff compared against
millisecond-resolution values would delete rows inside the retention window (`.` sorts before `Z`).

`Store.CurrentDeclaredRegions(target)` returns the most recent answer for a target, ordered by
**`observed_at`** (when the repeater actually answered), not `ingested_at` (when the row arrived at the
ingestor) — a drive buffered offline and uploaded days later must not overwrite a fresher reading with a
stale one just because it arrived later.

Retention: `retention.clientRegionsDays` bounds the table by `observed_at`; `0` disables it (Task 6).

## Configurable values (future customizer)

`retention.clientRegionsDays` is the only tunable so far; no map rendering or comparison UI is built on
this table yet in this task — that is future work once both the declared side (here) and the observed
side (`transported_scopes`) can be joined and rendered together.
