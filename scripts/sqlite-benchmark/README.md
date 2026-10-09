# CoreScope native SQLite before/after benchmark

This standalone harness measures two exact application revisions using native
CGO SQLite. It contains no production application changes or database-conversion
path. A separate exact harness revision supplies the same test-only overlays to
both archived application trees; neither application branch needs to contain
the harness. All three revisions are recorded and verified. An A/A run qualifies
the controller and supplies no performance-improvement claim.

This first slice has portable control and native overlay checks. A reviewed,
committed harness must pass the Linux A/A pilot before a candidate matrix.
No new B performance result or L capacity result is claimed here.

## Sources and prerequisites

- Pass full lowercase 40-character commits with `--baseline-sha`,
  `--candidate-sha` and `--harness-sha`. Mutable refs are rejected. The executable
  harness files must match `--harness-sha` (UTF-8 text with LF normalization);
  dirty, missing, unexpected and symlinked harness files fail verification.
- Use one isolated Linux host, Python 3.12+, Go 1.27.2, a C compiler, Git and
  coreutils. Both revisions use the same compiler and `-trimpath` build flags.
  Actual SQLite versions and PRAGMAs are captured through real connections.
- The host needs at least four CPUs and 8 GiB physical RAM. The *available*
  memory guard requires 8 GiB before preparation: the 6 GiB measured envelope
  plus coordinator/generator and host headroom. Nominal installed RAM is not
  evidence that this much is available.
- Primary and diagnostic runs require an owned cgroup-v2 subtree with `cpu`,
  `memory` and `io` delegated. The coordinator belongs in a sibling of the
  measured child. Every measured application child enters the common
  **3-CPU / 6-GiB** envelope before execution. Required counters and effective
  limits are checked before corpus generation. No service or system-wide
  cgroup changes are performed by this script.
- Use a fresh output directory whose name begins `corescope-bench-`. Corpus
  disk guards are S:4, B:12 and L:128 GiB currently free, then at least eight
  times the actual generated database size before each restore. These are
  conservative guards, not measured corpus sizes or capacity assurances.

Do not run alongside another heavy benchmark. Keep all data synthetic and use
only the owned local application processes. No MQTT broker or production
endpoint is contacted. The server keeps its native read-only SQLite connection;
the ingestor alone performs application writes and retention.

## Commands

Use the runner from the reviewed harness checkout. `REPO` must have all three
commit objects; it may be a different checkout. Parent CI can invoke these
commands through the existing opt-in workflow without putting harness files on
the application candidate branch.

```sh
python3 "$HARNESS/scripts/sqlite-benchmark/run.py" --repo "$REPO" \
  --harness-sha "$HARNESS_SHA" --baseline-sha "$BASELINE_SHA" \
  --candidate-sha "$BASELINE_SHA" --corpus S --profile smoke --pairs 1 \
  --warmup 2 --seconds 10 --ingest-rate 50 --http-rate 20 \
  --cgroup "$CORESCOPE_BENCH_CGROUP" --output /tmp/corescope-bench-aa-pilot
```

After the A/A pilot and a reviewed application fix, run five alternating B pairs:

```sh
python3 "$HARNESS/scripts/sqlite-benchmark/run.py" --repo "$REPO" \
  --harness-sha "$HARNESS_SHA" --baseline-sha "$BASELINE_SHA" \
  --candidate-sha "$CANDIDATE_SHA" --corpus B --profile primary --pairs 5 \
  --ingest-rate 50 --http-rate 20 --cgroup "$CORESCOPE_BENCH_CGROUP" \
  --output /tmp/corescope-bench-sqlite-primary
```

Primary defaults to 60 seconds of warmup and 180 measured seconds. Both revisions
receive the same generated event file, rate and durations. Five pairs contain
40 minutes of load windows plus preparation, validation and startup. Do not run
this matrix until the harness and application candidate have been reviewed.

Profiling is a **separate diagnostic invocation**, never a headline elapsed run:

```sh
python3 "$HARNESS/scripts/sqlite-benchmark/run.py" --repo "$REPO" \
  --harness-sha "$HARNESS_SHA" --baseline-sha "$BASELINE_SHA" \
  --candidate-sha "$CANDIDATE_SHA" --corpus B --profile diagnostic --pairs 1 \
  --warmup 10 --seconds 60 --ingest-rate 50 --http-rate 20 \
  --cgroup "$CORESCOPE_BENCH_CGROUP" --output /tmp/corescope-bench-sqlite-profiles
```

## Workload and correctness

S contains 30,000 transmissions / 90,000 observations. B contains 128,000 /
2,048,000, 2,000 nodes and 128 observers over eight days. The generator uses the
baseline's actual schema, decoder, ingestion APIs and SQLite adapter. The
firmware reference is MeshCore `a366955cb2f67b8e6842d4f00d2b6a554dddd88a`.
Protocol construction and encryption reuse the existing tested helpers.

B/L fanouts are 50%×4, 20%×22, 20%×23 and 10%×50 (mean 16). Nullable observers,
fractional signal values, integral scores, and one-/two-/three-byte hop hashes
are included. Observation identities are unique under both SQLite and the
unchanged in-memory loader's observer/path dedup rule; bounded deterministic
path retries preserve NULL/fanout counts and fail on exhaustion. Integer scores
retain common baseline behavior; this harness does not patch production code.

One recorded offset freshens receipt times for each pair. Signed wire content
and hashes stay unchanged. The recorded input is 50% new transmissions, 30%
additional observations, 15% duplicates and 5% late observations. The ingestion
queue remains bounded at 1024; HTTP concurrency at 64. **Any dropped schedule or
unexpected request/write error, including warmup, fails the run.** No rate is
automatically lowered. An overloaded run remains failed if a later common rate
is chosen for a separately labeled experiment.

The concurrent HTTP mix has 100 slots: nodes 20, channels/messages 15, observers
5, observer metrics 10, paths/reach/neighbors/RX coverage 20, packets/details 15,
analytics 10, and stats/healthz 5. All traffic goes through the real handlers.
Queries must return meaningful data; fixed pages must have their expected size,
and healthz must be ready with loaded rows. Expired detail requests become an
explicit expected-404 class after retention starts. Retention uses the actual
application method, followed by a verified empty pass. A separate 200-envelope
handler control proves durable writes from the real callback.

Both full and hot startup must load the complete retained S/B corpus, computed
independently from SQLite. B expects 112,000 transmissions / 1,792,000 observations
under the common seven-day retention, 2048 MiB store and 3072 MiB server limits.
S/B coverage overrides are refused. All 18 tables undergo ordered logical
digest checks before and after replay; the source and recorded input must remain
unchanged. Operational receipt times alone are excluded from post-run parity.
Schemas, indexes, application code and maintenance algorithms are not rewritten.

## Diagnostics and artifacts

The diagnostic test overlay calls the real server `main` and the real ingestor
workload. It writes CPU, heap, mutex and block profiles directly to private files
during the diagnostic measured window. Heap is captured after GC; mutex sampling
uses fraction 5 and block sampling 10,000 ns. `ENABLE_PPROF` is forced off, so the
application's optional all-interface profiling listener is never opened.
Both app and test binaries are built with `-trimpath`.

Raw profiles stay under each run's `.profiles/` directory and are not uploaded.
`go tool pprof -top` produces public summaries with local mapping paths removed.
Diagnostic runs are comparison-ineligible because sampling adds overhead.
The format, source provenance and file capture helpers can be reused by another
native adapter; this harness intentionally has only the SQLite adapter.

`db-pool.json` records actual ingestor `database/sql` WaitCount and WaitDuration
deltas across the measured window, with sample bounds and pool limit. They are
separate from request latency and writer-lock statistics. The existing server
does not expose its pool handle to the test launcher; server DBStats are
**unobserved**, not assumed zero or inferred by subtracting latency percentiles.
Server CPU/heap/mutex/block profiles still capture its actual execution.

The public bundle includes:

| File | Evidence |
|---|---|
| `manifest.json` | Three exact refs, harness/production/binary hashes, shared settings, workload, A/A or before/after scope |
| `ingest.jsonl`, `requests.jsonl`, `websocket.jsonl` | Raw event timings and outcomes; no packet bodies |
| `summary.csv` | Separate queue, service and schedule-to-complete distributions; p99 only at ≥1000 samples |
| `paired-summary.json` | Per-pair candidate/baseline ratios, without pooling requests or dividing by nonpositive baselines |
| `startup*.json`, `*validation*.json`, `retention*.json` | Loaded-count, durable-effect, full-table parity and retention checks |
| `resources.csv`, `processes.csv`, `resource-final.json` | Cgroup CPU/memory/I/O, RSS/PSS, database/WAL bytes; native peak reset per leg |
| `db-pool.json`, `profiles/*-top.txt`, `profile-capture.json` | Pool wait deltas and separately labeled diagnostic profiles |
| `failure-*.json`, `resource-error.json` | Bounded sanitized failures; arbitrary logs remain private |

`.runtime/`, databases, input event bodies, credentials, raw profiles and raw logs
are private. Upload only `public/`. Warm OS caches are explicit; cgroup I/O is
accounting, not physical disk traffic. WAL size is retained file footprint,
not generated write volume. Five pairs provide observed spread, not an
engine-wide speed guarantee. No OS-cold or long-soak result is implied.

## Planned 18-million-observation qualification

L defines 1,125,000 transmissions / 18,000,000 observations, 5,000 nodes and 512
observers over 30 days. Its complete repeating path-key period is covered by
generator controls. The actual corpus/restore/profile run has **not** been
qualified at this scale. B must pass first.

An L run refuses to start without both `--expected-loaded-transmissions` and
`--expected-loaded-observations`. Those must be reviewed fixed cache targets
declared before comparing candidates, not copied afterward from an incomplete
load. Both full/hot startup and both revisions must meet them exactly under
identical limits. The report labels this as bounded cache coverage and never
claims that all retained L rows were loaded. If upstream cannot sustain those
targets, the qualification fails; the count gate is not relaxed.

Before authorizing L, review actual free RAM/disk, measured B database size and
resource use, then the proposed cache/retention targets and maintenance duration.
The script enforces the 128-GiB disk floor, 8-GiB currently available RAM, cgroup
limit/counter checks, and the later eight-times-actual-file-size guard. These do
not prove sufficient capacity or authorize a large run on a busy workstation.

## Focused controls

```sh
python3 -m unittest discover -s scripts/sqlite-benchmark -p 'test_*.py'
```

Builds execute the actual generator and shared profile/endpoint controls in both
compiled variants before preparation. No production source file is changed by
the overlays; the source hashes are checked again at completion. CI wiring and
the first committed-harness A/A pilot are separate review steps.
