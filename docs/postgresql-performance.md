# PostgreSQL conversion performance

**Status: not measured.** No same-host, source-pinned paired comparison has been
executed for the final candidate. A PostgreSQL speedup, regression, capacity
increase or equivalent resource cost has not been established.

The comparison baseline is unmodified upstream commit
`9dbc287579a237ffa744dd0c91fa7227d09763ac`. The final candidate must be identified by
its full commit SHA. The [benchmark harness](../scripts/postgres-benchmark/README.md)
archives both revisions and adds test-only overlays. Production source hashes
before and after measurement prove that a later documentation-only commit did
not change the application being measured.

The primary corpus is synthetic B: 128,000 transmissions and 2,048,000
observations, plus the documented nodes, observers, metrics and feature tables.
Five alternating paired runs use one Linux host, Go 1.27.2, PostgreSQL 18.6,
durable settings, equal cache policy, and one shared 3-CPU/6-GiB application plus
database envelope. The actual corpus size, configuration, binaries, settings and
resource enforcement must be present in the result manifest.

| Acceptance evidence | Current result |
| --- | --- |
| Durable ingestion by event class and full-handler control | Not measured |
| First/full startup, including hot startup and loaded counts | Not measured |
| SQL misses separately from cached/mixed HTTP requests | Not measured |
| Concurrent ingest/API and WebSocket visibility | Not measured |
| Bounded retention under ingestion and empty maintenance pass | Not measured |
| Application + PostgreSQL CPU, cgroup memory, PSS/RSS, I/O, storage and WAL | Not measured |
| Offline migration duration, source immutability, parity and disk headroom | Not measured |
| Five paired repetitions, absolute p50/p95 and adequately sampled p99 | Not measured |
| Actual query plans and raw artifacts | Not measured |
| OS-cold, long soak, L and multi-GiB qualification | Not measured; optional capacity/extended runs |

The generator and harness controls have local correctness checks. These checks
are preparation evidence, not a database performance comparison. Existing unit
timing assertions likewise do not establish matched upstream/candidate results.

When the Linux run completes, replace this placeholder with links to the
sanitized manifest and raw artifacts; report absolute measurements, paired
variation, errors, gains and regressions together. Keep p99 unavailable for
classes with fewer than 1,000 successful samples. Preserve a failed or overloaded
run when selecting a lower common offered rate for a separately labeled run.

The conversion changes more than the driver: packet writes become atomic and
serialized through commit; observer identity becomes explicit; the native SQL
and indexes differ; repeated neighbor-edge writes are aggregated before bounded
upserts; PostgreSQL owns vacuum and checkpoint maintenance. Results therefore
describe this conversion as shipped, not an isolated engine substitution.

No production data or endpoint is a benchmark input. Publish only the harness's
`public/` bundle, and retain the private disposable state for diagnosing failures.
