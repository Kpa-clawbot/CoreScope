# SQLite retention scheduling: five-pair B comparison

The yield between committed retention batches reduced queue-inclusive ingestion tails in this workload, with a service-time tradeoff. Additional-observation p95 was **5.0% lower** and p99 **8.1% lower** by median paired ratio; its service-time p99 was **29.1% higher**. Cleanup was **0.5% longer**. This supports a narrow scheduling benefit, not a general database or application speed claim.

[Successful run 37975105215](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37975105215), attempt 1; [primary artifact 11641718171](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37975105215/artifacts/11641718171).

## Scope and correctness

- Baseline `9dbc287579a237ffa744dd0c91fa7227d09763ac`; candidate `b70a9f6bc4e78ab9f13f21e6bf063b48e986f109`; harness `1c5eb9d04436225460a530275764357c56b28461`. This is historical upstream SQLite before later #2147/#2155 changes. It does **not** measure the optional-backend candidate.
- Five alternating pairs on one Linux x86-64 Actions host; Go 1.27.2, CGO SQLite 3.53.4. Both legs used the same 3-CPU/6-GiB cgroup limit, 2-GiB packet-store setting, 3-GiB server and 512-MiB ingestor Go memory limits, WAL and synchronous=FULL.
- B contains 128,000 transmissions and 2,048,000 observations across 8 days. Full and hot-start background completion loaded the entire retained 7 days: 112,000 transmissions/1,792,000 observations, with zero startup evictions in every leg.
- Each leg ran 60s warmup plus 180s measured, at 50 ingest events/s and 20 HTTP requests/s. Retention started 135s into the measured window. All 120,000 replay events and 48,000 HTTP requests completed across 10 legs, with zero errors or drops. All 6,000 full-stream new-transmission sentinels per leg arrived exactly once; 4,500 per leg belong to the measured window.
- All 18 exported table count/digest records match within each pair before and after replay; post-replay parity excludes only the harness-declared volatile time columns. Each cleanup removed exactly 16,000 transmissions/256,000 observations; final durable totals were 118,000/1,802,200. The subsequent empty pass deleted nothing.
- Production hashes were independently recomputed from both Git archives; the complete harness hash and all 930 summary rows were independently reconciled. Replay sequence/class/hash identities, shared-clock schedules, SQL sample coverage and response digests also match. Raw SQLite databases are not in the public bundle: their byte-level immutability is a controller attestation, while the exported logical parity was independently checked.

The failed run 37965218190, smoke artifacts and diagnostic runs are excluded.

## Latency: separate queuing from work

Times below are milliseconds. Absolute columns are medians of the five per-run quantiles; C/B is the median of five **within-pair** ratios, so it need not equal the ratio of those absolute medians. Quantiles use nearest rank. No events were pooled across runs; p99 is withheld below 1,000 samples per class/leg.

| Metric | Samples/leg | Baseline median | Candidate median | Median C/B | Pair ratio range |
|---|---:|---:|---:|---:|---:|
| additional scheduled→complete p95 | 2,700 | 6,620.419 | 6,252.805 | 0.9503 (-5.0%) | 0.918–0.985 |
| additional scheduled→complete p99 | 2,700 | 13,037.450 | 12,025.582 | 0.9191 (-8.1%) | 0.903–0.936 |
| new scheduled→complete p95 | 4,500 | 6,717.353 | 6,233.006 | 0.9392 (-6.1%) | 0.880–0.983 |
| new scheduled→complete p99 | 4,500 | 12,934.441 | 12,177.854 | 0.9412 (-5.9%) | 0.922–0.961 |
| duplicate scheduled→complete p95 | 1,350 | 6,542.523 | 6,174.586 | 0.9420 (-5.8%) | 0.917–0.985 |
| duplicate scheduled→complete p99 | 1,350 | 13,088.091 | 12,159.148 | 0.9399 (-6.0%) | 0.901–0.960 |
| late scheduled→complete p95 | 450 | 6,658.226 | 6,135.665 | 0.9359 (-6.4%) | 0.897–0.959 |
| additional service p99 | 2,700 | 5.349 | 6.302 | 1.2914 (+29.1%) | 1.064–2.155 |
| new service p99 | 4,500 | 6.945 | 8.027 | 1.1582 (+15.8%) | 0.916–2.012 |
| duplicate service p99 | 1,350 | 3.651 | 5.182 | 1.3904 (+39.0%) | 1.235–1.639 |

All five pairs improved scheduled-to-complete p95 for all four ingest classes, and p99 for the three classes with enough samples. Almost all of the additional-observation tail is waiting before the serial consumer starts work: its queue p95 ratio is 0.9503. Additional service p95 nevertheless rose 8.9%, and its service p99 worsened in all five pairs; duplicate service p99 also worsened in all five. Late has 450 measured samples/leg, so no p99 claim is made.

Large stalls remain: additional-observation scheduled-to-complete maxima span 14.21–14.82s at baseline and 13.23–13.79s with the candidate. Both streams finished their scheduled work before the measured window ended; there was no post-window ingest or HTTP drain. The fixed offered load and zero drops do not establish higher throughput capacity.

WebSocket first-receipt lag, measured from ingest completion rather than scheduling, improved at p95 (C/B 0.7230; 2,179.6→1,553.5ms absolute medians). Its p99 ratio was 0.9492, with a 0.842–1.142 pair range. These are signed differences; this run had no negative samples or duplicate receipts.

HTTP was mixed. Scheduled-to-complete p95 ratios ranged from 0.9259 for `nodes_2000` to 1.0569 for `nodes_50`; `paths` was 1.0262 and `analytics_channels` 1.0202. `nodes_2000` varied 0.850–1.842 by pair and `healthz` 0.036–3.778, so neither supports a stable broad API claim. Every HTTP class had only 45–252 samples/leg: no HTTP p99 is reported. The 135 successful history-detail requests and 45 expected post-retention 404s per leg are separate classes, not failures.

## Cleanup and resource cost

| Measure | Baseline leg median | Candidate leg median | Median paired C/B |
|---|---:|---:|---:|
| Concurrent cleanup | 15.945s | 16.000s | 1.0049 (+0.5%) |
| Empty cleanup probe | 0.284ms | 0.297ms | 1.0221 (+2.2%) |
| Sampled cgroup memory peak | 3.972GiB | 3.998GiB | 1.0065 (+0.6%) |
| Sampled aggregate PSS peak | 2.869GiB | 2.886GiB | 1.0058 (+0.6%) |
| Sampled DB+WAL storage peak | 1.071GiB | 1.079GiB | 1.0004 (+0.0%) |
| Observed average CPU cores | 0.298 | 0.297 | 0.9873 |
| Device 8:0 write rate | 13.26MiB/s | 13.28MiB/s | 1.0010 |

Cleanup was longer in four pairs, with C/B 0.993–1.021. The scheduling change does not bound a dense single transaction. Full-stream lock instrumentation (including warmup) recorded median maximum ingest lock waits of 596.9→463.5ms, but still hundreds of milliseconds.

Resource gauges and counter deltas use only samples inside the 180s measured interval. Counter spans cover 178.43–179.47s because sampling is about 1 Hz; rates normalize by their actual span. Raw write-byte deltas rose 0.64% by paired median, while normalized write rate was nearly flat (+0.10%). Only device 8:0 was present; read-byte deltas were zero in this warm-cache run. Do not add duplicate layers when comparing other hosts.

Measured sampled cgroup memory never exceeded 4.061GiB. The separately reset, FD-local **whole-leg** kernel peak reached 4.227GiB; it includes startup/warmup and is not a measured-window peak. All `memory.events` max/oom/oom_kill counters remained zero. Minimum observed disk free space was 71.89GiB. Cgroup memory includes file cache; aggregate PSS and RSS have different meanings. These observations establish neither a memory reduction nor a capacity guarantee.

## Interpretation limits and reproducibility

The manifest records identical production-server binary hashes between revisions. Startup and isolated SQL controls precede concurrent retention; their differences are not attributed to the ingestor scheduling change. Full-ready startup C/B was 0.9998; isolated channel-message SQL p95 was 1.0529. This comparison measures one fixed corpus/seed on one warm-cache host, five pairs and a short replay—not OS-cold startup, arbitrary fanout, long soak, saturation capacity, later upstream cache/locking changes, or PostgreSQL.

Query plans were captured after replay/retention and cannot establish the initial workload plan. The [machine-readable summary](sqlite-retention-b5-2026-10-09.json) includes every class/timing, per-pair absolute values and ratios, resource windows, settings, source hashes and raw-file hashes. Reproduce from the linked primary artifact using the pinned harness definitions: group measured events per class/leg, compute nearest-rank quantiles, then median the five candidate/baseline ratios. Resource deltas use each leg’s shared replay origin + 60s through origin + 240s.
