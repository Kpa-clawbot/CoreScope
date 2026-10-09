# Optional backend: two five-pair B experiments, 9 October 2026

Both experiments passed the benchmark data and coverage gates for `c03a2585eef3819cf06640911b2f7308d99339db`. They do **not** support a general speed claim. The measured c03 SQLite candidate has a material `/api/observers` regression, with a later correction separately evidenced by the native control below. Its Linux numbers remain unchanged. Candidate PostgreSQL greatly shortens this retention workload and its associated ingest queue, but costs more CPU, memory and storage and slows many reads.

**Exact scope.** Baseline: `3e21b179ecccae005aa5f6f45bc095353c94b343`; measured application candidate: `c03a2585eef3819cf06640911b2f7308d99339db`. These are separate same-host paired studies, with different runners and HTTP mixes. Never rank candidate PostgreSQL against candidate SQLite by combining their numbers. The SQLite mix adds paths, neighbors and health requests and changes endpoint weights. Later test fixes and the observer correction were not measured by these Linux runs.

| Study | Successful benchmark job / sanitized artifact | Harness |
|---|---|---|
| Native SQLite before/after | [Run 37985636346, job 114006837291](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37985636346/job/114006837291), artifact `11645874366` | `4de419413c0a6fd93ecc2361cd704cd2a59e673e`; SHA-256 `f06f836de930a16d428b7c644231d8460e1a7ab736eed855e5d3eae7f2e23e96` |
| Upstream SQLite versus candidate PostgreSQL | [Run 37985643046, job 114006882924](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37985643046/job/114006882924), artifact `11647406303` | Harness from `c03a2585eef3819cf06640911b2f7308d99339db`; SHA-256 `7c234ba61c93351688d7068c9d8405f7d2906558d797854738eb0ff059f35d84` |

The PostgreSQL benchmark job succeeded independently of the workflow’s unrelated test-fixture failures. Smoke/pilot runs and previous failed studies are excluded.

**Method and acceptance.** Each study has five alternating pairs, Linux amd64 / Go 1.27.2, enforced 3 CPU / 6 GiB for the whole measured stack, a 2 GiB packet-store limit and 3 GiB server memory setting. PostgreSQL is 18.6. The B corpus contains 128,000 transmissions and 2,048,000 observations over eight days. Every full and hot startup loaded the complete retained 112,000 / 1,792,000 records, with zero startup evictions. Accounts are disabled.

Each of the ten legs per study completed all 12,000 offered ingest events and 4,800 HTTP requests: 50 ingest/s and 20 HTTP/s, 60 s warmup plus 180 s measured. Independently reconstructed schedules, warmup flags, replay identities, full WebSocket receipt coverage, errors and drops all passed. Post-replay validation recorded 118,000 transmissions / 1,802,200 observations in every leg after deleting 16,000 / 256,000; the empty second cleanup deleted zero. All 18 table digests match within each pair: full initial data and logical post-replay data. Post-replay comparison excludes only the declared operational receipt/maintenance timestamps listed in the JSON. SQLite comparison normalizes only the exact, verified `observers_identity_autoincrement_v1` migration marker; observer rowids and all other logical application values remain checked.

All 1,000 paired isolated SQL-control responses per study matched in bytes and SHA-256. Static mixed-request controls (metrics, rx-coverage, packet detail and unfiltered RF) also matched. Mixed live response bodies are not universally byte-identical: public artifacts retain hashes, sizes and structural/count checks, not bodies sufficient to explain every difference. This is not proof of universal API response equality.

Tables show medians of per-leg quantiles and the median of five candidate/baseline ratios, with their observed range; these are not confidence intervals or ratios of pooled requests. Lower is better for times and resource use. Service time excludes dispatch/replay queue wait; scheduled-to-complete includes it. HTTP service includes response reading and validation. p99 is omitted below 1,000 samples per leg; the 900 late-ingest events, individual HTTP classes and 100-per-class SQL controls therefore have no p99 claim.

**SQLite before/after.** All five observer p95 comparisons regress (180 measured requests per leg). Its roughly 53× increase is the principal performance blocker for this measured candidate. Ingest scheduled latency improves, but duplicate service p99 increases and several reads regress.

| Metric (ms) | Upstream SQLite | Candidate SQLite | Median paired ratio [range] |
|---|---:|---:|---:|
| Observers HTTP p95 | 3.653 | 194.9 | 53.36 [50.05, 54.08] |
| Additional ingest scheduled p95 | 2203 | 1583 | 0.7466 [0.6328, 0.9491] |
| Additional ingest scheduled p99 | 8066 | 6803 | 0.8422 [0.7889, 0.9025] |
| Additional ingest service p95 | 1.132 | 0.7674 | 0.6956 [0.6357, 0.7325] |
| New ingest service p95 | 1.572 | 1.174 | 0.7446 [0.7076, 0.763] |
| Duplicate ingest service p99 | 1.392 | 2.067 | 1.704 [0.5793, 2.745] |
| Health HTTP p95 | 73.54 | 1.288 | 0.01737 [0.003842, 0.939] |
| Paths HTTP p95 | 46.28 | 47.79 | 1.03 [0.9701, 1.038] |
| Reach HTTP p95 | 0.7175 | 0.8227 | 1.098 [0.947, 1.203] |
| Topology HTTP p95 | 37.84 | 37.72 | 1.102 [0.7627, 1.387] |
| Uncached channel SQL p95 | 90.02 | 96.89 | 1.075 [1.02, 1.082] |
| WebSocket signed return-to-receipt p99 | 1976 | 1317 | 0.5422 [0.4464, 0.9699] |

Cleanup took 11.231 → 11.091 s (paired ratio 0.9949); it remains a substantial queue interruption. Full-start ready took 28.268 → 27.848 s (0.9891), but **candidate storage adoption separately took 42.03–42.59 s, median 42.50 s, before startup timing began**. The baseline needed no adoption. Neither leg drained ingest or HTTP work past the scheduled window’s end.

| Metric (unit in row) | Upstream SQLite | Candidate SQLite | Median paired ratio [range] |
|---|---:|---:|---:|
| CPU consumed (s) | 54.68 | 53.52 | 0.9787 [0.9597, 1.023] |
| Cgroup sampled peak (GiB) | 3.988 | 3.944 | 0.9944 [0.9891, 1.002] |
| PSS sampled peak (GiB) | 2.897 | 2.87 | 0.9947 [0.9904, 1.005] |
| Selected DB/cluster + WAL peak (GiB) | 1.071 | 1.069 | 0.9987 [0.9896, 1] |
| WAL sampled peak (MiB) | 38.7 | 29.6 | 0.9496 [0.6983, 1.003] |
| Device 8:0 write delta (GiB) | 2.31 | 2.24 | 0.9694 [0.9652, 0.9812] |

Memory and CPU differences are small: the normalized average-CPU paired ratio is 0.9837 and its range crosses 1. All SQLite measured memory-limit/OOM deltas were zero; candidate whole-leg kernel peaks were 4.045–4.120 GiB. This is not a large retained-memory win.

**PostgreSQL comparison.** These ratios use the upstream SQLite leg of the PostgreSQL study, not the candidate SQLite experiment above. Cleanup fell from 11.268 to 0.974 s (paired ratio 0.08708). Scheduled ingest tails fell sharply while additional and duplicate service p95 increased, so the queue-inclusive result must not be presented as a universal per-write speedup.

| Metric (ms) | Upstream SQLite | Candidate PostgreSQL | Median paired ratio [range] |
|---|---:|---:|---:|
| Additional ingest scheduled p95 | 2262 | 3.044 | 0.001346 [0.001186, 0.001457] |
| Additional ingest scheduled p99 | 8075 | 5.57 | 0.0006807 [0.0006152, 0.0007697] |
| Additional ingest service p95 | 1.627 | 2.239 | 1.373 [0.956, 1.513] |
| Duplicate ingest service p95 | 1.049 | 1.874 | 1.713 [1.626, 1.94] |
| New ingest service p95 | 2.689 | 2.238 | 0.8562 [0.6949, 0.9195] |
| New ingest service p99 | 5.492 | 4.041 | 0.7722 [0.6216, 0.8537] |
| Observers HTTP p95 | 3.762 | 4.06 | 1.079 [1.071, 1.154] |
| Nodes (50) HTTP p95 | 5.241 | 10.55 | 1.784 [1.656, 2.156] |
| Messages (offset 0) HTTP p95 | 69.11 | 114.9 | 1.654 [1.626, 2.519] |
| Packet detail, memory HTTP p95 | 1.644 | 4.013 | 2.407 [2.298, 2.553] |
| Uncached channel SQL p95 | 95.29 | 122.6 | 1.278 [1.219, 1.54] |
| WebSocket signed return-to-receipt p99 | 2035 | 998 | 0.5318 [0.4524, 0.5928] |

Full-start ready was 28.965 → 27.712 s (0.9701), while hot-start first-ready was 9.768 → 11.271 s (1.1427). The separately measured import, grants and validation stage took a median **148.887 s**; neither startup number includes it. There was no work draining past the scheduled measured endpoint.

| Metric (unit in row) | Upstream SQLite | Candidate PostgreSQL | Median paired ratio [range] |
|---|---:|---:|---:|
| CPU consumed (s) | 45.02 | 59.23 | 1.329 [1.31, 1.519] |
| Average CPU cores | 0.2506 | 0.3311 | 1.323 [1.308, 1.525] |
| Cgroup sampled peak (GiB) | 3.954 | 5.536 | 1.422 [1.371, 1.527] |
| PSS sampled peak (GiB) | 2.868 | 3.194 | 1.11 [1.088, 1.148] |
| Selected DB/cluster + WAL peak (GiB) | 1.072 | 2.273 | 2.119 [2.099, 2.146] |
| WAL sampled peak (MiB) | 30.71 | 1024 | 33.35 [25.54, 40.79] |
| Device 8:0 write delta (GiB) | 2.315 | 0.3907 | 0.1702 [0.1619, 0.2152] |

**PostgreSQL memory pressure is a sizing constraint.** All five descriptor-local whole-leg kernel peaks reached the 6 GiB limit, with small kernel-accounting overshoots of 4–116 KiB. Measured-window `memory.events max` increments were `[338, 0, 522, 0, 0]`; OOM and OOM-kill increments were zero. The lowest sampled headroom was only 20 KiB. An absence of killed processes does not establish spare capacity. Cgroup memory includes file cache. PSS apportions shared pages; summed RSS (paired 1.853× here) double-counts shared PostgreSQL mappings and must not be interpreted as unique resident memory.

PostgreSQL retained **1 GiB of WAL throughout every measured window after migration**. Its storage peak therefore includes that carryover and is not all newly generated runtime data. PostgreSQL storage counts the private cluster; SQLite counts its database and sidecars. Conversion source/recovery copies are additional. Lower windowed write accounting does not measure later checkpoints or steady-state write amplification. Minimum sampled free disk was 72.11 GiB in the PostgreSQL legs and 75.81 GiB in candidate SQLite’s separate study; these well-provisioned runners do not establish minimum operator disk sizing.

**Limits.** Resource deltas use the first and last sample inside the exact 180 s measured window, not startup or warmup; sampled spans are about 178–180 s. Gauge peaks can miss subsecond spikes; whole-leg kernel peaks are separately reset/read through the same retained file descriptor and include setup/startup/validation. `memory.events` raw final counters are cumulative; only explicit differences are per-window events. I/O is device-level cgroup accounting, not a physical-device write-amplification measurement. Query plans were collected after retention. WebSocket timing is signed receipt minus ingest-return; no negative measured samples occurred here. OS cache is warm, tests are synthetic, accounts are disabled, and neither capacity, OS-cold behavior, a long soak, Windows performance, nor a generally superior backend follows.

All per-run metrics, paired distributions, body-hash comparison counts, settings, source/binary identities and raw-file SHA-256 values are in the [accompanying JSON](optional-backend-b5-2026-10-09.json). Production hashes and both harness hashes were recomputed from immutable Git objects; public database digest attestations were checked without reopening private payload databases.

**Separate observer follow-up control.** A separate Windows amd64 / Go 1.27.2 native SQLite response-build control used 2,048,000 observations, 128,000 transmissions, 128 observers and eight days, WAL/FULL, a 2,000 KiB reader page cache, GOMAXPROCS=2 and GOMEMLIMIT=512MiB. Each stage timed 50 uncached buildObserversDefaultResponse calls with warm OS/SQLite page caches. Before correction the upgraded layout had p50/p95 169.0/195.7 ms versus its same-run legacy control 19.46/20.29 ms. After the writer-side observer-statistics correction, upgraded p50/p95 were 19.59/20.42 ms versus same-run legacy 19.68/21.21 ms. Exact recent counts and full typed observer responses matched, excluding server_time. This supports the local planner repair; it is not full HTTP service, concurrent replay, PostgreSQL, or a replacement Linux B5 result.

Source/evidence: base `c6fee4e8872b6c0caf7af0aadc3013d48b704882`; before tree `0041649915878be1782e7d620a9336207493b83d`; corrected tree `1d9a08258b1350e512528075ca70ad988d569af7`. Frozen logs `observers-native-b-before.log` (SHA-256 `859686c1b225c5044e7ffa3175a8e7efc35576584cecd43f5b4c0601ca58cc4b`) and `observers-native-b-after.log` (`bb362f9d4544bef9bf31c0a2682346ef9dd1808f51d2d950c0e99dc9a5d0e50f`) were hash-checked and read.
