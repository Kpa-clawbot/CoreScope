# Startup cache budget repair

An ARM64 development instance repeatedly reached its 1.5 GiB application
container limit during recovery. The packet cache was configured for 256 MiB,
a one-hour hot startup and 24 hours of cached history. The initial loader used
a transmission-count estimate assuming ten observations per transmission; the
background loader then materialized whole daily windows without applying the
byte budget. Reducing the configured budget alone did not bound that backfill.

Startup and background loading now share a reader which accounts for actual
observations and publishes only complete transmissions that fit the existing
`packetStore.maxMemoryMB` budget. Bounded loading selects recent traffic first,
including old transmission IDs heard again recently. Reaching the configured
limit is successful cache initialization, with honest coverage reporting and
SQL fallback for omitted history. Live observations and indexes survive an
overlapping history load. Poll watermarks cover the complete database rather
than replaying excluded historical rows.

There is no schema change or new configuration setting. An unlimited cache
retains primary-key cursor traversal. Memory-capped pages require recency
sorting with the existing indexes; this repair does not add a full index to
every `last_seen` update. A native PostgreSQL plan regression verifies that an
unlimited 100-transmission page seeks past a 20,000-row timestamp tie without
rescanning those earlier rows.

## Reproduction and measurements

The committed benchmarks use canonical v3 fixtures with distinct observer/path
pairs, short synthetic packet metadata and twelve-hop paths. The hot window
contains one transmission; the remainder exercises historical backfill.
Both versions use a 16 MiB cache, a one-hour hot window and 24-hour retention.
The fixture has the production index layout, without an extra recency index.

Baseline server code: `2d1e763a7f2f710066246dcba47ba8cb4f4652e1`, using the
same benchmark fixture through a Go source overlay. Three one-iteration runs
per version/workload were measured on Windows amd64, Intel i5-4690K, Go 1.25.1,
native SQLite, cgo enabled and `GOMAXPROCS=2`. Fixture creation is outside the
timer. Retained Go heap is the change in `HeapAlloc` after explicit GC, keeping
the store alive. Values below are medians of those three runs.

| Synthetic workload | Startup before | Startup after | Tracked cache before / after | Retained Go heap before / after |
|---|---:|---:|---:|---:|
| 6,000 transmissions, 65 observations each | 5.882 s | 0.416 s | 226.4 / 15.96 MiB | 247.0 / 26.30 MiB |
| 30,000 transmissions, 6 observations each | 2.983 s | 0.277 s | 212.3 / 16.00 MiB | 151.2 / 20.27 MiB |

Startup times across the three runs were 5.882/5.671/7.192 s versus
0.416/0.435/0.395 s for high fanout, and 3.274/2.983/2.944 s versus
0.304/0.277/0.274 s for typical fanout. This is a cache-budget comparison:
the repaired version intentionally retains less history in RAM. Database
history remains available through SQL. These measurements are not a comparison
of equal in-memory coverage, whole-container memory, or steady-state API and
ingestion performance. The local host was not isolated from other work.

To repeat the candidate measurements from `cmd/server`:

```sh
CORESCOPE_TEST_BACKEND=sqlite CGO_ENABLED=1 GOMAXPROCS=2 \
  go test -run '^$' -bench '^BenchmarkStartupMemoryBudget' \
  -benchtime=1x -count=3 -benchmem .
```

Regression tests also cover an oversized single transmission, tied timestamps,
ID zero, a recently heard old ID, native PostgreSQL values, complete SQL history
fallback, accurate byte accounting and a live poll interleaved with backfill.
The same startup tests run against both native database backends.
