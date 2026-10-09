# PostgreSQL comparison harness

This harness measures the actual CoreScope application methods and server. It
does not replace them with standalone SQL insert loops. Production files are
archived at exact commits and remain unchanged; Go build overlays add only test
drivers. Fixture construction happens outside measured ingestion.

## Status

The generator, protocol vectors, bounded fixture/handler test, and harness unit
checks have been exercised locally. The Linux controller, full S/B corpora,
paired runtime profiles, and resource enforcement still need an isolated Linux
pilot. **No empirical database comparison is included in this change.**

## Prerequisites

- One disposable Linux host: at least four CPUs and 8 GiB RAM for `primary`.
- Go **1.27.2**, a C compiler, Git, Python 3.12+, and native PostgreSQL **18.6**.
- PostgreSQL tools default to `/usr/lib/postgresql/18/bin`; override with
  `--postgres-bin`. The controller uses `postgres`, `initdb`, `psql`, and
  `pg_isready`, and never contacts an existing PostgreSQL service.
- Run as a non-root user. PostgreSQL starts on a newly selected loopback port
  with a private data directory and synthetic, local roles.
- Primary runs require a delegated cgroup v2 parent with CPU, memory and I/O
  controllers enabled. Required resource counters are checked before builds.
  The runner creates its own child and writes its limits
  before any application/database child executes. All descendants inherit the
  same **3 CPU / 6 GiB** application budget. The coordinator and event generator
  stay outside that child.
- The coordinator must be inside a sibling of the measured child under the
  delegated parent, or otherwise have permission to migrate its children at
  their common cgroup ancestor. Chowning an unrelated destination alone is not
  sufficient. `memory.reclaim` is required to remove inactive prior-backend
  charges between serial runs; the script never clears the host page cache.
- Capacity guards are S:4 GiB, B:12 GiB, L:128 GiB free before generation. A
  second guard uses eight times the *measured* canonical file size before each
  restore/import. These are conservative guards, not dataset-size claims.

## GitHub Actions opt-in

The existing CI workflow can run the comparison for a pull request. Add this
checked item to the PR body **before pushing the reviewed candidate**:

```markdown
- [x] Run PostgreSQL comparison
```

The checkbox is evaluated on the normal PR events: opened, reopened and head
updates. Editing the body alone does not launch or cancel CI. Without the checked
item, the benchmark is skipped. A documentation-only PR also skips it through
the change-scope gate.

The comparison checks out the PR's exact head SHA from its head repository,
including forks. It does not benchmark GitHub's synthetic merge commit. The
executed workflow and benchmark harness must match that head; mismatches fail
before running the experiment. The job has read-only repository access, persists
no checkout credentials, inherits no deployment secrets and cannot publish or
deploy. Other PR checks continue normally.

Wait for the comparison to finish, then **uncheck the item before pushing a
documentation-only results/report update**. Change scope covers the full PR diff,
so a PR that still contains implementation changes would otherwise request
another primary run. Existing PR concurrency can also cancel an unfinished run
when a new commit arrives.

Manual dispatch remains available: use `deploy.yml` with
`postgres_benchmark=true` and the full `candidate_sha`, from the matching
candidate branch/commit. That dispatch runs the comparison alone and skips normal
publishing/deployment jobs. Both entry points run the small pilot first and then
five primary pairs; upload only the sanitized `public/` outputs.

## Commands

First run the portable controls and compile/protocol checks:

```sh
python3 -m unittest discover -s scripts/postgres-benchmark -p 'test_*.py'
```

Pilot on the same Linux host intended for the comparison:

```sh
python3 scripts/postgres-benchmark/run.py --repo . \
  --candidate-sha <full-40-character-commit> --corpus S --pairs 1 \
  --profile smoke --ingest-rate 10 --http-rate 5 \
  --output /tmp/corescope-bench-pilot
```

Smoke results are explicitly ineligible for a primary comparison. Primary:

```sh
python3 scripts/postgres-benchmark/run.py --repo . \
  --candidate-sha <full-40-character-commit> --corpus B --pairs 5 \
  --profile primary --cgroup "$CORESCOPE_BENCH_CGROUP" \
  --output /tmp/corescope-bench-primary
```

The baseline is fixed to `9dbc287579a237ffa744dd0c91fa7227d09763ac`. Floating
revisions and existing output directories are refused. Primary defaults to
60 seconds of warmup and 180 measured seconds per backend, with 100 offered
ingest events/second and 20 HTTP requests/second. Five alternating A/B pairs
therefore spend 40 minutes in load windows, plus preparation, imports,
validation, startup and drain time. The longer design profile is explicit:
`--warmup 120 --seconds 600`. Record and compare identical locked settings;
changing rates after a pilot produces a separately identified experiment.

Both engines use a 2,048 MiB packet-store allowance and a 3,072 MiB server Go
memory limit. The ingestor remains at 512 MiB and the whole application/database
cgroup remains capped at 3 CPUs / 6 GiB. These are common settings for full
startup, hot startup and replay; the manifest records them.

The earlier 1,024 MiB store / 1,536 MiB server profile failed B capacity
qualification: the unchanged baseline loaded all 112,000 retained transmissions
and 1,792,000 observations, then its own memory accounting triggered a 25%
eviction. The corrected corpus accounts for about 1,222 MiB before replay.
A conservative check using the baseline's actual estimators bounds all 24,000
primary replay events at another 190 MiB, even counting every event as a new
transmission plus observation with padded field widths. The new common store
allowance leaves more than 630 MiB above that bound. This is capacity evidence,
not a performance result; measurements from the earlier profile must not be
pooled with this one. Rates, corpus sizes, durability and count gates stay fixed.

## Data and correctness

The immutable baseline creates the complete canonical SQLite schema and seeds
all 18 telemetry tables. S has 30,000 transmissions / 90,000 observations; B has
128,000 / 2,048,000; L has 1,000,000 / 16,000,000. S cycles through fanouts 1–5;
B/L use 50%×4, 20%×22, 20%×23 and 10%×50. Inputs include missing observers,
nullable and fractional signal values, integral scores, resolved/unresolved paths, and
one-, two-, and three-byte hop hashes. S/B have seven retained days and one
expired day. The current day includes a real hot-startup window.

Every generated observation also has a unique normalized observer/path key
within its transmission, matching the unchanged server's in-memory identity.
NULL observer positions and fanout counts stay fixed. If short path prefixes
collide for two NULL observations, the generator deterministically tries the next
valid node/path variant, up to 64 attempts, preserving the wire payload/hash and
route/hop/hash-size shape. Exhaustion fails generation. This avoids accidental
duplicate rows that SQLite's nullable unique index permits but the loader merges.
Compiled generator controls, including the complete repeating B/L path key space,
run during both builds before corpus generation. The full retained-count gate
continues to compare every eligible row.

Scores in the timed corpus and replay are integral values supported by both
revisions. The unchanged SQLite server silently drops fractional-score rows;
including those would compare different loaded datasets after the PostgreSQL
loader correction. Separate native PostgreSQL regressions exercise fractional,
large integral and null scores through every loader and the existing HTTP
response. The benchmark never patches the baseline application to fix that bug.

Adverts have deterministic Ed25519 identities and unique synthetic advert
labels; channel frames use the repository's existing AES/HMAC test helper.
Both are checked through the real decoder. Protocol references are MeshCore
`a366955cb2f67b8e6842d4f00d2b6a554dddd88a`: `src/Packet.cpp`,
`src/Mesh.cpp::createAdvert`, `src/helpers/AdvertDataHelpers.h`, and
`docs/packet_format.md`. No real packet bodies, identities, or locations are
fetched. The generated coordinates, names and messages are fictional.

Each pair receives one recorded receipt-time offset. All persisted receipt and
bookkeeping timestamps shift together, preserving fractional precision. Signed
wire payloads and emission timestamps stay unchanged, so their identities
remain stable. Both backends use exactly that same pair copy and pre-generated
event file. The event file contains 50% new transmissions, 30% distinct later
observations, 15% exact duplicates and 5% late observations. Replayed existing
transmissions are selected from retained history so retention does not
introduce nondeterministic delete/recreate races.

The shipping importer loads PostgreSQL. Before timing, every table's ordered,
typed logical digest and row count must match the pair's SQLite copy; the source
must remain unchanged. After replay and retention, counts, write errors,
relationships, and full logical parity are checked again. Only documented
operational receipt/creation times are excluded from the final digest; packet
timestamps, raw frames, IDs, scores and nulls remain part of it. A separate
200-envelope `handleMessage` control follows parity and verifies real durable
effects, rather than trusting the void callback return.

## Measurements

- Real server launch to first HTTP, ready header/health, and full background
  startup completion, with `hotStartupHours` 0 and 1. Loaded transmission and
  observation counts must agree across each pair under identical memory limits.
  For S and B, both counts must also equal the complete retained corpus computed
  independently from SQLite; two equally incomplete loads cannot pass. L records
  bounded memory coverage for optional capacity qualification.
- Startup uses new application processes after complete data validation, so the
  OS cache is warm. Mixed requests have a separate application warmup. The
  manifest records these actual per-phase policies; there is no cache-state
  switch or unsupported OS-cold label.
- Real `InsertTransmission` latency and durable effects; a bounded 1024-event
  queue reports every dropped schedule and drain time.
- The fixed weighted API mix and production caches: nodes 20%, channels/messages
  20%, observers 10%, metrics 10%, reach/RX 10%, packets/details 15%, analytics
  10%, stats 5%. Nonempty data and requested page sizes are checked. Individual
  HTTP cache hits are **unobserved**, not inferred from a fake cache-bust flag.
- Separate real channel-message SQL misses (using the existing cache-clear seam)
  and observer-metric SQL queries. These are not confused with warm HTTP hits.
- Real WebSocket receipts correlated to committed new-transmission hashes.
  Negative receipt-minus-return lags are retained and counted: the baseline can
  expose a transmission before all its auto-committed writes finish. Multiple
  hash receipts may be legitimate observation updates, and are not mislabeled
  as duplicate packet bugs.
- The real 250-transmission retention path during replay, followed by an empty
  retention pass. A packet-detail request for expired history becomes an
  explicitly separate expected-404 class after retention starts.
- Whole application/database cgroup CPU, memory and I/O; per-process RSS/PSS,
  database/WAL storage, and remaining filesystem headroom. RSS is not used as a
  substitute for shared-memory-aware PSS/cgroup memory.
- Actual compiled retention and advert-preservation query plans in a separate
  diagnostic phase. PostgreSQL uses `EXPLAIN (ANALYZE, BUFFERS, SETTINGS, FORMAT
  JSON)`; SQLite uses `EXPLAIN QUERY PLAN`. Plan collection is outside headlines.

The runner never weakens WAL durability, adds a cache service, or changes the
poll interval. SQLite WAL/FULL/FK and PostgreSQL fsync/synchronous-commit/full-page
writes are checked on real connections. Actual toolchain versions, binary
hashes, schemas, run order, and production-tree hashes before and after the run
are retained. The unit of variation is a paired run, not millions of requests
treated as independent experiments. Per-class p99 requires at least 1,000
successful samples; unavailable values remain null, never zero.

## Artifacts and limits

Upload **only `<output>/public/`**. It is an explicit sanitized whitelist and is
also produced on controller failure. It contains manifests, typed validation
digests, per-run JSONL/CSV samples, plans, summaries and the report. Do not upload
`.runtime`, control files, binaries, databases, raw event payload streams or
unfiltered build logs. Completed temporary per-run databases are removed only
after verification; failure preserves them. The original canonical corpus stays
available locally for investigation.

`summary.csv` contains absolute per-run values. `paired-summary.json` contains
paired ratios, sample/pair counts and observed spread. Raw data remains the
authority. Missing prerequisites, wrong source hashes, unsafe destinations,
unverified cgroup limits, empty results, errors, loss or parity mismatches fail
the run. A timeout, error or partial artifact must never be relabeled zero work.

The controller does not claim verified OS-cold results, a 65-minute maintenance
soak, size-target 5.5/13-GiB qualification, or an optional million-mobile-RX
extension. L is capacity qualification only when actually run. Preparation,
receipt-time freshening and protocol generation stay outside timed ingestion.
Migration, startup, diagnostics, and steady-state profiles remain separate in
the resource samples. Results measure the conversion as shipped, including its
transaction/batching/index changes; they cannot isolate the database engine alone.
