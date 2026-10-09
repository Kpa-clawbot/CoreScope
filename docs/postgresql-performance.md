# PostgreSQL conversion performance

> Historical measurement of candidate `ce023e0`, before optional backend selection
> and upstream's #2147 concurrency fix. It does not measure the current dual-backend
> implementation. Keep these results separate from the
> [optional-backend comparison against upstream `3e21b17`](performance/optional-backend-b5-2026-10-09.md).

The PostgreSQL conversion reduced retention-related ingestion delays in this
test. Median writes and several reads were slower, and CPU, memory and storage
costs increased. These results describe the implemented conversion under one
synthetic workload.

Five alternating pairs completed in [run 37914893623](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37914893623), comparing
[candidate `ce023e0`](https://github.com/Kpa-clawbot/CoreScope/commit/ce023e0d623116fe66987761d06016590f98fb11)
with unmodified upstream
[`9dbc287`](https://github.com/Kpa-clawbot/CoreScope/commit/9dbc287579a237ffa744dd0c91fa7227d09763ac).
The [raw public artifact](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37914893623/artifacts/11613951167),
[complete tables](performance/postgresql-ce023e0-details.md) and
[machine-readable results](performance/postgresql-ce023e0-details.json) retain
every measured class, regression, range, input hash and limitation.

Both engines ran serially on the same Linux x86-64 runner, with Go 1.27.2,
PostgreSQL 18.6, durable settings and a common budget of **3 CPUs and 6 GiB** for
application and database. Corpus B
contained 128,000 transmissions, 2,048,000 observations, 2,000 nodes and 128
observers across eight days. Accounts were disabled; OS caches were warm.
Application startup used a new process;
channel SQL tests explicitly cleared the application cache.

The supplemental workload offered **50 ingest events/s and 20 HTTP requests/s**,
with 60 seconds of warmup and 180 measured seconds per engine. Every leg completed
12,000 scheduled ingest events and 4,800 expected HTTP responses with zero errors
or dropped schedules. Both startup modes loaded all 112,000 transmissions and
1,792,000 observations in the seven-day retained window, with zero evictions.
All 18-table migration and post-replay digests matched, source files stayed
unchanged, and retention deleted the expected rows.

Values below are **median [minimum, maximum] across five runs**. The ratio column
is the median and range of the five paired PostgreSQL/SQLite ratios; it is not
the quotient of the displayed medians. Ranges are observed variation, not
confidence intervals. Latencies are milliseconds. Ingest service excludes queue
wait in the replay driver; schedule→return includes it. HTTP timing includes scheduling delay, response
reading and validation.

| Latency metric (ms) | SQLite | PostgreSQL | Paired PG/SQLite |
|---|---:|---:|---:|
| New ingest: schedule→return p95 | 1304 [1237, 1516] | 2.858 [2.831, 2.997] | 0.00229 [0.001885, 0.002309] |
| New ingest: service p50 | 1.003 [0.9395, 1.073] | 1.515 [1.48, 1.607] | 1.542 [1.47, 1.575] |
| Channel messages: SQL miss p50 | 76.11 [66.72, 81.74] | 106.3 [102.2, 118.3] | 1.433 [1.368, 1.531] |
| Channel messages: HTTP first-page p95 | 60.09 [58.82, 64.02] | 113.3 [108.3, 179] | 1.907 [1.782, 2.796] |
| In-memory packet list: HTTP p50 | 30.33 [30.07, 33.27] | 35.34 [34.69, 36.85] | 1.16 [1.107, 1.195] |
| RX coverage: HTTP p50 | 2.164 [2.137, 2.2] | 2.331 [2.277, 2.421] | 1.087 [1.051, 1.113] |
| RX coverage: HTTP p95 | 2.689 [2.623, 2.723] | 2.872 [2.745, 3.056] | 1.076 [1.021, 1.122] |
| RF analytics: HTTP p50 | 2.565 [2.541, 2.609] | 2.337 [2.259, 2.374] | 0.896 [0.8807, 0.9297] |
| WebSocket receipt after ingest return: p95 | 985.5 [969.2, 991.8] | 959.6 [946.8, 971] | 0.969 [0.9608, 0.9916] |

Ingestion queue tails improved while median service costs rose across event
classes. Channel SQL misses, HTTP message-page tails and the in-memory packet
list regressed. RF analytics improved; RX coverage remained slightly slower.
WebSocket median lag was
similar (507.1 versus 511.2 ms); its p95 improved modestly. One negative SQLite
receipt-minus-return sample is retained, reflecting visibility before the final
ingest return. The details show p99 only with at least 1,000 samples per run.

| Duration or resource metric | SQLite | PostgreSQL | Paired PG/SQLite |
|---|---:|---:|---:|
| Retention pass (s) | 10.49 [10.45, 10.73] | 0.905 [0.8444, 0.9622] | 0.08626 [0.0805, 0.09126] |
| Full startup, full readiness (s) | 28.06 [27.45, 30.19] | 27.44 [27.22, 29.31] | 0.9783 [0.9403, 0.9989] |
| Hot startup, initial readiness (s) | 9.565 [9.467, 10.18] | 11.48 [10.98, 11.68] | 1.187 [1.148, 1.223] |
| Hot startup, full background fill (s) | 20.52 [20.13, 21.75] | 20.15 [19.92, 20.65] | 0.9707 [0.9494, 1.006] |
| Measured-window CPU time (CPU s) | 43.45 [41.18, 44.58] | 56.86 [55.35, 66.47] | 1.332 [1.274, 1.491] |
| Sampled cgroup memory peak (MiB) | 4043 [4013, 4078] | 5681 [5581, 6144] | 1.415 [1.389, 1.518] |
| Sampled process PSS peak (MiB) | 2922 [2892, 2925] | 3247 [3200, 3321] | 1.11 [1.1, 1.136] |
| Sampled database + WAL footprint (MiB) | 1098 [1095, 1122] | 2327 [2326, 2327] | 2.12 [2.073, 2.126] |
| Accounted device writes (MiB) | 2360 [2335, 2378] | 433.6 [376.7, 507.6] | 0.1857 [0.1596, 0.2153] |

Retention removed 16,000 transmissions and 256,000 observations. The empty pass
was slower on PostgreSQL, but small in absolute terms: 0.268 versus 1.732 ms.
Full startup and hot background completion were close; initial hot readiness
was slower on PostgreSQL. Both sustained the offered rate.

Paired CPU time increased about **33%**, PSS about **11%**, and sampled database
plus WAL footprint about **2.12×**. Resource deltas cover 178.2–179.4 sampled
seconds of each 180-second window; sampled peaks can miss spikes. Cgroup memory
includes file cache and charges retained from preparation/import, so it is not
an application-heap estimate. PostgreSQL's run in pair 1 had all 174 measured samples
at or above 99% of the memory limit, reached within **16 KiB** of it, and recorded
444 `memory.events.max` increments. No OOM or process/group OOM kills occurred.
Every PostgreSQL full leg approached the cap during import/startup. This supplies
no spare-capacity assurance.

The lower write counter is **cgroup device accounting**, not physical disk-write
volume; layered devices can be counted more than once. PostgreSQL retained
1,024 MiB of WAL segments from import throughout the measured windows. That is
part of the stored footprint, not WAL generated by timed ingestion. PSS accounts
for shared pages; summed RSS is reported separately in the details.

The synthetic telemetry migration tool took **90.99 s [89.94, 94.13]**. Including
role grants and independent harness validation, the phase took **147.6 s
[144.7, 151.0]**. These timings exclude a real operator's shutdown, account
migration, backups, restore checks and rollback work; they are not an outage
estimate. Follow the [upgrade runbook](postgresql-upgrade.md) and rehearse on
copies of the deployment's own data.

The earlier [100-event/s qualification](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37897747657)
remains failed: SQLite dropped 82 of 24,000 schedules during an 11.745-second
retention burst. The bounded 1,024-event queue was unchanged for this separately
labeled 50-event/s comparison. Failed and successful experiments are not pooled.

Production hashes matched before and after measurement; the full candidate,
baseline and harness hashes are in the details. Atomic serialized writes, native
SQL/indexes and maintenance behavior differ between implementations, so these
are conversion results rather than isolated engine effects. No L corpus,
OS-cold run, long soak, account workload or capacity qualification was performed.
Use the [harness instructions](../scripts/postgres-benchmark/README.md) to reproduce
the defined experiment; deployment hardware and workload can change the result.
