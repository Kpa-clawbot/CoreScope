# Catalogue key tier (`catalogKeys`, #2107)

The catalogue key tier lets channel and region names from a public catalogue
become keys, but only after traffic this instance has already stored confirms
them. It is off by default, and leaving the block out changes nothing. The
config reference is the `_comment_catalogKeys` entry in `config.example.json`.
This page covers what an operator needs to know when running it.

## What it writes

| Where | What |
|---|---|
| `catalog_keys` | One row per catalogue name with evidence: `kind` (`channel`/`region`), `name`, `key_hex`, `status`, `hits`, `threshold`, `window_n`, `candidates`. |
| key sets | Rows with status `promoted` are loaded as the lowest key tier, at startup before ingest and after every refresh. |
| history | Each new promoted key starts the key backfill (#2133). The backfill names unresolved rows (`transmissions.scope_name = ''` for regions, `channel_hash = 'enc_XX'` for channels) and records each rewrite in `tx_rewrite_feed`. |

`status` is one of:

- `proposed`: the gate confirmed the name, but it is not in force. Regions stay
  proposed unless `autoPromoteRegions` is on. A proposal that the current window
  no longer supports is dropped.
- `promoted`: in force. Promotion is sticky. Weaker evidence later does not
  demote it, and **switching `autoPromoteRegions` off later does not demote
  regions that are already promoted**. Switching `channels` or `regions` to
  `false` takes that kind out of force but keeps the rows.
- `rejected`: set only by an operator. The name is taken out of force at the
  next refresh or restart. It is never proposed or promoted again, even when the
  catalogue still lists it and traffic still matches.

## How likely is a wrong promotion?

- **Channels:** a wrong key has to pass the 2-byte MAC, decrypt to readable
  text, and carry a plausible sender timestamp. That leaves roughly 4e-10 per
  row and candidate.
- **Regions:** `regionAlpha` (default 0.05) is the expected number of wrongly
  confirmed names *per run*. With daily runs over a 14-day window,
  `autoPromoteRegions` can promote a wrong region about once a year. Leave it off
  if that is too often for you. Then you confirm proposals yourself by adding
  them to `hashRegions`.

## Reverting a promoted region

A wrongly promoted region names packets that belong to some other region, or to
none. Undoing it takes three things: take the name out of force for good,
un-name the history the backfill wrote, and let the remaining region keys
decide those rows again.

The ingestor is the only writer to the database, so stop it first. In the
Docker image, stop the container. Then, with `#wrong` being the name to revert:

```sql
-- 1. Keep the name out of force, now and on every later catalogue run.
UPDATE catalog_keys SET status = 'rejected'
 WHERE kind = 'region' AND name = '#wrong';

-- 2. Un-name the history written with it.
UPDATE transmissions SET scope_name = ''
 WHERE scope_name = '#wrong';

-- 3. Give every remaining region key a fresh pass over history, so the rows
--    from step 2 are decided again with the keys still in force.
DELETE FROM key_backfill_done  WHERE kind = 'region';
DELETE FROM key_backfill_state WHERE kind = 'region';
```

Then start the container again:

- At startup the ingestor loads only `promoted` rows, so `#wrong` is no longer
  in force. The region backfill then runs one full pass and names the rows from
  step 2 with any other key that matches them uniquely. Rows that only `#wrong`
  matched stay unnamed, which is what they were before the promotion.
- The server reloads the rewritten rows from the database at startup. Step 2
  does not go through `tx_rewrite_feed`, so a server you do not restart keeps
  showing `#wrong` until it reloads.

Step 3 costs one scan of the transmissions table in bounded batches, the same
cost as adding a region key. Live ingest keeps running while it does.

To find out which promoted region looks wrong, these queries help:

```sql
SELECT name, hits, threshold, window_n, candidates,
       datetime(first_seen, 'unixepoch') AS promoted_at
  FROM catalog_keys WHERE kind = 'region' AND status = 'promoted'
 ORDER BY first_seen DESC;

SELECT scope_name, COUNT(*) FROM transmissions
 WHERE scope_name IN (SELECT name FROM catalog_keys WHERE kind = 'region')
 GROUP BY scope_name;
```

To allow a rejected name again later, delete its row (`DELETE FROM
catalog_keys WHERE kind = 'region' AND name = '#wrong'`). The next run then
decides it from scratch.

## Rejecting a channel

`UPDATE catalog_keys SET status = 'rejected' WHERE kind = 'channel' AND name =
'#name'` takes a promoted catalogue channel out of force at the next restart
and keeps it out. Messages it already decrypted stay decrypted, because the
backfill never turns a decrypted row back into `enc_XX`. Given the channel
gate's false rate, a wrong channel promotion is a theoretical case.

## Private channels (`includeExplicitSecrets`)

With `includeExplicitSecrets: true`, catalogue entries whose secret is not
derived from `#name` are candidates too. These are shared private keys that
someone published. Once traffic confirms such a key, those private channels are
**decrypted and shown in plaintext** on this instance, like any other channel.
Enable it only if that is what you want. The ingestor logs a line at startup
when it is on.

Secrets may be 16 bytes (128-bit) or 32 bytes (256-bit), as hex or base64. A
32-byte secret whose upper half is zero is the 16-byte key.

## Catalogue URL

`url` is an `https://` URL or a local file path. Plain `http://` works, but
anyone on the path can then change the candidate list, and the ingestor logs a
warning. The evidence gates still apply to every candidate, so a tampered list
can only propose names that your own stored traffic confirms. The catalogue is
read up to 8 MiB. A larger one is refused, and the keys already promoted stay
in force.
