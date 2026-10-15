# PostgreSQL ce023: complete paired results

[Summary](../postgresql-performance.md) · [Complete JSON](postgresql-ce023e0-details.json) · [Run](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37914893623) · [Raw public artifact](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37914893623/artifacts/11613951167)

All generated tables below are preserved from the validated final analysis.
The artifact-relative source directory is `corescope-bench-37914893623-1/public`.
The JSON's `analysis_reference` identifies the analyzer's source-review anchor
(`c315d7d`); its `provenance.candidate_sha` identifies the measured candidate
(`ce023e0`). The review checked 189 input hashes; the raw public bundle contains
234 files. No host-local paths or private payload databases are published here.

The [intermediate c315 run](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37901327267)
was diagnostic evidence for the RX generic-plan correction. It ran on another
Actions runner; no quantified cross-run speedup is claimed. All paired ratios
below compare ce023 with the unchanged upstream on the final run's same host.

Candidate: ce023e0d623116fe66987761d06016590f98fb11

Completed eligible B; five pairs. Values are median [min, max] across runs. Ratios are paired PostgreSQL/SQLite; no request populations are pooled.

## Per-run latency quantiles

| Scenario / class / timing | SQLite p50 ms | PostgreSQL p50 ms | SQLite p95 ms | PostgreSQL p95 ms | Paired p50 ratio (n) | Paired p95 ratio (n) |
|---|---:|---:|---:|---:|---:|---:|
| durable-ingest / additional / end_to_end | 1.461 [1.375, 1.517] | 2.049 [2.006, 2.15] | 1453 [1394, 1630] | 2.844 [2.788, 2.956] | 1.446 [1.335, 1.459] (n=5) | 0.001965 [0.001722, 0.002046] (n=5) |
| durable-ingest / additional / queue | 0.6397 [0.6228, 0.6449] | 0.621 [0.6148, 0.6294] | 1433 [1394, 1610] | 1.076 [1.069, 1.08] | 0.9763 [0.9611, 1.005] (n=5) | 0.0007504 [0.0006689, 0.0007669] (n=5) |
| durable-ingest / additional / service | 0.7615 [0.7034, 0.8227] | 1.367 [1.334, 1.45] | 1.267 [1.159, 1.353] | 2.041 [1.988, 2.185] | 1.86 [1.735, 1.896] (n=5) | 1.615 [1.502, 1.76] (n=5) |
| durable-ingest / duplicate / end_to_end | 1.328 [1.229, 1.419] | 1.897 [1.851, 1.979] | 1394 [1315, 1571] | 2.489 [2.426, 2.629] | 1.481 [1.304, 1.509] (n=5) | 0.00185 [0.001544, 0.00188] (n=5) |
| durable-ingest / duplicate / queue | 0.635 [0.6221, 0.6538] | 0.6051 [0.5988, 0.6299] | 1394 [1315, 1571] | 1.073 [1.065, 1.081] | 0.9709 [0.9194, 1.013] (n=5) | 0.0007642 [0.000688, 0.0008153] (n=5) |
| durable-ingest / duplicate / service | 0.6438 [0.5763, 0.6995] | 1.252 [1.211, 1.314] | 1.006 [0.9305, 1.109] | 1.674 [1.612, 1.761] | 1.993 [1.84, 2.101] (n=5) | 1.682 [1.589, 1.733] (n=5) |
| durable-ingest / late / end_to_end | 1.625 [1.535, 1.814] | 2.188 [2.12, 2.266] | 1355 [1276, 1531] | 2.904 [2.752, 2.953] | 1.347 [1.249, 1.415] (n=5) | 0.002143 [0.001854, 0.002244] (n=5) |
| durable-ingest / late / queue | 0.6344 [0.5942, 0.7032] | 0.6373 [0.5996, 0.6439] | 1354 [1275, 1531] | 1.072 [1.058, 1.085] | 0.9553 [0.889, 1.073] (n=5) | 0.0007966 [0.0007087, 0.0008381] (n=5) |
| durable-ingest / late / service | 0.9377 [0.849, 1.004] | 1.546 [1.505, 1.631] | 1.464 [1.361, 1.52] | 2.071 [1.919, 2.123] | 1.679 [1.616, 1.773] (n=5) | 1.408 [1.35, 1.464] (n=5) |
| durable-ingest / new / end_to_end | 1.671 [1.602, 1.831] | 2.18 [2.131, 2.311] | 1304 [1237, 1516] | 2.858 [2.831, 2.997] | 1.275 [1.25, 1.361] (n=5) | 0.00229 [0.001885, 0.002309] (n=5) |
| durable-ingest / new / queue | 0.6307 [0.6293, 0.6446] | 0.6211 [0.6192, 0.6458] | 1284 [1217, 1496] | 1.074 [1.07, 1.084] | 0.9845 [0.9779, 1.012] (n=5) | 0.0008446 [0.000718, 0.0008798] (n=5) |
| durable-ingest / new / service | 1.003 [0.9395, 1.073] | 1.515 [1.48, 1.607] | 1.634 [1.525, 1.803] | 2.084 [2.028, 2.211] | 1.542 [1.47, 1.575] (n=5) | 1.241 [1.223, 1.359] (n=5) |
| handler-control / 200_valid_envelopes / service | 0.8129 [0.8029, 0.8751] | 1.576 [1.546, 1.728] | 2.036 [1.799, 2.169] | 2.274 [2.113, 2.395] | 1.939 [1.903, 1.974] (n=5) | 1.148 [1.054, 1.264] (n=5) |
| mixed-http / analytics_channels / end_to_end | 30.11 [29.54, 30.58] | 31 [30.71, 31.11] | 34.08 [33.49, 35.53] | 36.57 [34.5, 41.08] | 1.03 [1.004, 1.053] (n=5) | 1.03 [1.025, 1.201] (n=5) |
| mixed-http / analytics_channels / queue | 0.3555 [0.2994, 0.4261] | 0.3569 [0.3157, 0.5119] | 1.033 [0.8971, 1.078] | 1.04 [0.9728, 1.065] | 1.062 [0.9062, 1.44] (n=5) | 1.021 [0.928, 1.157] (n=5) |
| mixed-http / analytics_channels / service | 29.51 [29.13, 30.11] | 30.49 [30.08, 30.61] | 33.77 [32.46, 34.82] | 36.47 [33.86, 40.57] | 1.033 [1.014, 1.051] (n=5) | 1.048 [1.005, 1.19] (n=5) |
| mixed-http / analytics_rf / end_to_end | 2.565 [2.541, 2.609] | 2.337 [2.259, 2.374] | 3.277 [3.126, 3.432] | 3.175 [3.015, 3.337] | 0.896 [0.8807, 0.9297] (n=5) | 0.9342 [0.9182, 1.037] (n=5) |
| mixed-http / analytics_rf / queue | 0.8237 [0.6097, 0.8441] | 0.6058 [0.5712, 0.6508] | 1.069 [1.052, 1.108] | 1.058 [1.021, 1.074] | 0.7696 [0.7355, 0.9646] (n=5) | 0.9893 [0.9418, 1.015] (n=5) |
| mixed-http / analytics_rf / service | 1.749 [1.742, 1.783] | 1.595 [1.576, 1.666] | 2.581 [2.472, 2.659] | 2.588 [2.394, 2.618] | 0.9115 [0.8988, 0.9344] (n=5) | 1.004 [0.9116, 1.021] (n=5) |
| mixed-http / analytics_rf_filtered / end_to_end | 1.898 [1.863, 2.016] | 2.225 [2.164, 2.325] | 582.5 [482.5, 783.3] | 576.1 [549.2, 622.3] | 1.158 [1.14, 1.2] (n=5) | 1.062 [0.7365, 1.141] (n=5) |
| mixed-http / analytics_rf_filtered / queue | 0.1195 [0.1159, 0.147] | 0.1377 [0.127, 0.2074] | 0.8623 [0.8086, 0.9341] | 0.9849 [0.9468, 1.054] | 1.188 [1.033, 1.411] (n=5) | 1.142 [1.119, 1.19] (n=5) |
| mixed-http / analytics_rf_filtered / service | 1.711 [1.653, 1.775] | 1.965 [1.897, 1.996] | 582 [482.3, 783.2] | 576 [548.4, 621.3] | 1.12 [1.114, 1.208] (n=5) | 1.062 [0.7364, 1.141] (n=5) |
| mixed-http / analytics_topology / end_to_end | 30.33 [30.18, 31.06] | 30.59 [30.55, 31.15] | 33.42 [32.16, 37.02] | 39.21 [33.59, 48.29] | 1.007 [0.9931, 1.029] (n=5) | 1.088 [0.9074, 1.445] (n=5) |
| mixed-http / analytics_topology / queue | 0.7453 [0.7158, 0.8084] | 0.6501 [0.5254, 0.7381] | 1.022 [0.9774, 1.087] | 1.036 [1.003, 1.074] | 0.8723 [0.6499, 1.016] (n=5) | 1.018 [0.9234, 1.072] (n=5) |
| mixed-http / analytics_topology / service | 29.64 [29.57, 30.39] | 30.08 [29.98, 30.58] | 32.52 [31.25, 36.5] | 38.55 [32.52, 47.81] | 1.013 [0.9994, 1.032] (n=5) | 1.103 [0.8908, 1.47] (n=5) |
| mixed-http / channels / end_to_end | 1.297 [1.262, 1.322] | 1.307 [1.266, 1.385] | 1.768 [1.748, 1.823] | 1.853 [1.794, 2.156] | 1.009 [0.9578, 1.097] (n=5) | 1.05 [1.015, 1.183] (n=5) |
| mixed-http / channels / queue | 0.5876 [0.5363, 0.6329] | 0.587 [0.5739, 0.637] | 1.046 [1.028, 1.076] | 1.058 [1.03, 1.063] | 1.036 [0.9067, 1.084] (n=5) | 1.002 [0.9846, 1.013] (n=5) |
| mixed-http / channels / service | 0.6692 [0.653, 0.6971] | 0.6724 [0.6581, 0.7051] | 0.9293 [0.8749, 1.064] | 1.058 [0.9314, 1.458] | 1.008 [0.9902, 1.021] (n=5) | 1.209 [0.9345, 1.371] (n=5) |
| mixed-http / messages_0 / end_to_end | 1.952 [1.936, 2.064] | 2.017 [1.963, 2.117] | 60.09 [58.82, 64.02] | 113.3 [108.3, 179] | 1.026 [1.014, 1.057] (n=5) | 1.907 [1.782, 2.796] (n=5) |
| mixed-http / messages_0 / queue | 0.549 [0.4973, 0.6505] | 0.5701 [0.5473, 0.6505] | 1.038 [1.015, 1.054] | 1.049 [1.031, 1.063] | 1.012 [0.9968, 1.206] (n=5) | 1.016 [0.9921, 1.024] (n=5) |
| mixed-http / messages_0 / service | 1.338 [1.32, 1.365] | 1.411 [1.354, 1.477] | 59.91 [58.73, 63.54] | 112.7 [107.7, 178.3] | 1.054 [1.008, 1.082] (n=5) | 1.9 [1.785, 2.806] (n=5) |
| mixed-http / messages_1000 / end_to_end | 2.011 [1.929, 2.142] | 2.089 [2.065, 2.199] | 83.21 [82.65, 86.91] | 114.9 [111.9, 165.4] | 1.031 [0.9738, 1.1] (n=5) | 1.376 [1.345, 1.903] (n=5) |
| mixed-http / messages_1000 / queue | 0.6226 [0.5858, 0.7269] | 0.6336 [0.5732, 0.6619] | 1.054 [1.039, 1.088] | 1.051 [1.04, 1.075] | 0.9207 [0.8867, 1.118] (n=5) | 1.002 [0.9653, 1.021] (n=5) |
| mixed-http / messages_1000 / service | 1.377 [1.344, 1.409] | 1.411 [1.39, 1.455] | 82.79 [81.99, 86.2] | 114.1 [111.4, 165.2] | 1.036 [1.002, 1.044] (n=5) | 1.379 [1.344, 1.917] (n=5) |
| mixed-http / metrics_dense_24h / end_to_end | 3.906 [3.845, 3.95] | 4.405 [4.335, 4.495] | 4.845 [4.579, 4.922] | 5.237 [5.152, 7.076] | 1.138 [1.102, 1.146] (n=5) | 1.073 [1.053, 1.545] (n=5) |
| mixed-http / metrics_dense_24h / queue | 0.5668 [0.4823, 0.6363] | 0.5668 [0.5126, 0.5854] | 1.048 [1.039, 1.057] | 1.039 [1.01, 1.072] | 1.01 [0.8887, 1.214] (n=5) | 0.9975 [0.9635, 1.013] (n=5) |
| mixed-http / metrics_dense_24h / service | 3.327 [3.284, 3.395] | 3.851 [3.796, 3.871] | 4.126 [4.075, 4.211] | 4.688 [4.503, 6.618] | 1.144 [1.14, 1.173] (n=5) | 1.13 [1.07, 1.624] (n=5) |
| mixed-http / metrics_sparse_7d / end_to_end | 2.044 [1.948, 2.16] | 2.283 [2.24, 2.337] | 2.503 [2.452, 2.898] | 2.819 [2.775, 2.987] | 1.115 [1.082, 1.188] (n=5) | 1.132 [0.9759, 1.172] (n=5) |
| mixed-http / metrics_sparse_7d / queue | 0.6256 [0.5906, 0.6783] | 0.5361 [0.5013, 0.576] | 1.054 [1.037, 1.064] | 1.017 [0.9639, 1.053] | 0.8013 [0.7605, 0.9753] (n=5) | 0.9681 [0.9145, 0.9953] (n=5) |
| mixed-http / metrics_sparse_7d / service | 1.369 [1.353, 1.43] | 1.736 [1.709, 1.787] | 1.606 [1.554, 2.147] | 1.961 [1.921, 2.119] | 1.261 [1.25, 1.268] (n=5) | 1.256 [0.9135, 1.298] (n=5) |
| mixed-http / nodes_2000 / end_to_end | 62.5 [61.84, 63.6] | 64.35 [63.35, 64.7] | 119.6 [69.93, 122.1] | 104.7 [72.78, 127] | 1.024 [1.014, 1.032] (n=5) | 0.8723 [0.6087, 1.727] (n=5) |
| mixed-http / nodes_2000 / queue | 0.6 [0.4865, 0.6183] | 0.5109 [0.4756, 0.5571] | 1.033 [0.9864, 1.081] | 1.045 [1.02, 1.06] | 0.8477 [0.7692, 1.132] (n=5) | 0.9911 [0.9756, 1.06] (n=5) |
| mixed-http / nodes_2000 / service | 61.85 [61.2, 62.99] | 63.77 [62.76, 64.36] | 119.4 [69.45, 121.7] | 103.8 [72.35, 126.9] | 1.027 [1.015, 1.033] (n=5) | 0.869 [0.6057, 1.73] (n=5) |
| mixed-http / nodes_50 / end_to_end | 3.661 [3.537, 3.765] | 5.526 [5.443, 5.646] | 8.125 [5.561, 8.896] | 9.892 [7.729, 9.995] | 1.509 [1.49, 1.552] (n=5) | 1.217 [0.8688, 1.797] (n=5) |
| mixed-http / nodes_50 / queue | 0.2297 [0.1344, 0.2799] | 0.1754 [0.1382, 0.1957] | 1.029 [0.9864, 1.051] | 1.016 [0.9824, 1.066] | 0.7635 [0.6017, 1.309] (n=5) | 1 [0.9348, 1.06] (n=5) |
| mixed-http / nodes_50 / service | 3.332 [3.238, 3.414] | 5.231 [5.068, 5.322] | 7.376 [4.911, 8.105] | 9.457 [6.928, 9.643] | 1.566 [1.539, 1.583] (n=5) | 1.294 [0.8548, 1.925] (n=5) |
| mixed-http / nodes_region / end_to_end | 5.921 [5.747, 6.172] | 8.268 [7.988, 8.485] | 12.2 [9.103, 14.11] | 15.57 [14.59, 16.3] | 1.376 [1.363, 1.439] (n=5) | 1.277 [1.035, 1.791] (n=5) |
| mixed-http / nodes_region / queue | 0.4812 [0.4291, 0.5252] | 0.4942 [0.4748, 0.5549] | 1.051 [1.036, 1.065] | 1.046 [1.024, 1.067] | 1.079 [0.9041, 1.152] (n=5) | 0.9877 [0.9702, 1.03] (n=5) |
| mixed-http / nodes_region / service | 5.476 [5.336, 5.637] | 7.724 [7.528, 7.812] | 11.61 [8.665, 13.74] | 15.28 [14.15, 15.75] | 1.402 [1.386, 1.431] (n=5) | 1.316 [1.03, 1.817] (n=5) |
| mixed-http / observers / end_to_end | 3.476 [3.44, 3.605] | 3.519 [3.463, 3.543] | 4.572 [4.434, 4.9] | 4.884 [4.819, 5.227] | 1.002 [0.9828, 1.023] (n=5) | 1.074 [1.054, 1.096] (n=5) |
| mixed-http / observers / queue | 0.5647 [0.5399, 0.5779] | 0.5578 [0.522, 0.5812] | 1.033 [1.019, 1.056] | 1.043 [1.034, 1.064] | 1.015 [0.9033, 1.065] (n=5) | 1.006 [0.9924, 1.03] (n=5) |
| mixed-http / observers / service | 2.846 [2.815, 2.967] | 2.932 [2.867, 2.991] | 3.912 [3.757, 4.061] | 4.267 [4.18, 4.618] | 1.027 [1.003, 1.051] (n=5) | 1.135 [1.066, 1.15] (n=5) |
| mixed-http / packet_detail_memory / end_to_end | 1.867 [1.846, 1.919] | 3.145 [3.069, 3.348] | 2.407 [2.345, 2.501] | 3.763 [3.732, 4.101] | 1.679 [1.65, 1.792] (n=5) | 1.55 [1.538, 1.749] (n=5) |
| mixed-http / packet_detail_memory / queue | 0.5601 [0.531, 0.5866] | 0.5716 [0.5094, 0.6415] | 1.028 [0.9905, 1.057] | 1.031 [1.012, 1.039] | 1.013 [0.9593, 1.094] (n=5) | 1.002 [0.9787, 1.039] (n=5) |
| mixed-http / packet_detail_memory / service | 1.264 [1.212, 1.369] | 2.582 [2.495, 2.779] | 1.586 [1.505, 1.955] | 3.093 [3.044, 3.304] | 2.043 [1.961, 2.17] (n=5) | 1.936 [1.69, 2.189] (n=5) |
| mixed-http / packet_detail_pruned / end_to_end | 1.275 [1.236, 1.431] | 1.254 [1.217, 1.357] | 1.734 [1.725, 1.791] | 1.823 [1.694, 2.103] | 0.9835 [0.8647, 1.074] (n=5) | 1.057 [0.9802, 1.174] (n=5) |
| mixed-http / packet_detail_pruned / queue | 0.5449 [0.4803, 0.7149] | 0.5003 [0.4245, 0.6195] | 1.062 [1.021, 1.072] | 1.036 [1.009, 1.051] | 0.9181 [0.6686, 0.9498] (n=5) | 0.9744 [0.9431, 1.029] (n=5) |
| mixed-http / packet_detail_pruned / service | 0.6807 [0.6523, 0.6935] | 0.7155 [0.6777, 0.7325] | 0.7926 [0.7728, 0.8374] | 1.051 [0.8693, 1.374] | 1.051 [0.9831, 1.108] (n=5) | 1.333 [1.097, 1.778] (n=5) |
| mixed-http / packet_detail_sql / end_to_end | 1.727 [1.68, 1.757] | 2.145 [2.106, 2.316] | 2.197 [2.151, 2.224] | 2.729 [2.662, 3.427] | 1.233 [1.217, 1.366] (n=5) | 1.261 [1.212, 1.541] (n=5) |
| mixed-http / packet_detail_sql / queue | 0.5701 [0.5563, 0.6822] | 0.6203 [0.5669, 0.6349] | 1.046 [1.033, 1.058] | 1.046 [1, 1.069] | 1.088 [0.8995, 1.122] (n=5) | 0.9991 [0.9564, 1.015] (n=5) |
| mixed-http / packet_detail_sql / service | 1.09 [1.066, 1.155] | 1.524 [1.509, 1.626] | 1.33 [1.293, 1.352] | 1.934 [1.79, 2.642] | 1.407 [1.388, 1.43] (n=5) | 1.446 [1.354, 1.954] (n=5) |
| mixed-http / packets_memory / end_to_end | 30.33 [30.07, 33.27] | 35.34 [34.69, 36.85] | 32.37 [31.87, 35.97] | 38.32 [37.21, 40.13] | 1.16 [1.107, 1.195] (n=5) | 1.167 [1.115, 1.229] (n=5) |
| mixed-http / packets_memory / queue | 0.6212 [0.5473, 0.6726] | 0.5808 [0.5089, 0.5875] | 1.056 [1.041, 1.06] | 1.015 [1.012, 1.055] | 0.8832 [0.826, 1.073] (n=5) | 0.9624 [0.9551, 1] (n=5) |
| mixed-http / packets_memory / service | 29.8 [29.35, 32.67] | 34.74 [34.08, 36.26] | 31.86 [31.5, 35.26] | 37.45 [36.81, 39.24] | 1.161 [1.11, 1.197] (n=5) | 1.168 [1.11, 1.246] (n=5) |
| mixed-http / reach / end_to_end | 1.394 [1.291, 1.424] | 1.413 [1.393, 1.476] | 1.811 [1.794, 1.885] | 1.877 [1.842, 1.927] | 1.01 [0.9778, 1.107] (n=5) | 1.034 [0.9878, 1.069] (n=5) |
| mixed-http / reach / queue | 0.5871 [0.5545, 0.6118] | 0.5899 [0.5659, 0.633] | 1.032 [0.9735, 1.056] | 1.053 [1.011, 1.067] | 1.035 [0.9639, 1.068] (n=5) | 1.005 [0.969, 1.096] (n=5) |
| mixed-http / reach / service | 0.802 [0.7769, 0.8079] | 0.8281 [0.8187, 0.8414] | 0.932 [0.9122, 0.9496] | 0.9602 [0.9469, 0.9832] | 1.044 [1.013, 1.066] (n=5) | 1.028 [1.004, 1.077] (n=5) |
| mixed-http / rx_coverage / end_to_end | 2.164 [2.137, 2.2] | 2.331 [2.277, 2.421] | 2.689 [2.623, 2.723] | 2.872 [2.745, 3.056] | 1.087 [1.051, 1.113] (n=5) | 1.076 [1.021, 1.122] (n=5) |
| mixed-http / rx_coverage / queue | 0.5906 [0.5682, 0.6063] | 0.5677 [0.5278, 0.594] | 1.045 [1.004, 1.054] | 1.038 [1.002, 1.048] | 0.9442 [0.8937, 1.028] (n=5) | 0.9927 [0.9507, 1.034] (n=5) |
| mixed-http / rx_coverage / service | 1.556 [1.539, 1.604] | 1.799 [1.681, 1.846] | 1.769 [1.765, 1.83] | 1.959 [1.842, 2.229] | 1.155 [1.092, 1.157] (n=5) | 1.107 [1.043, 1.218] (n=5) |
| mixed-http / stats / end_to_end | 1.11 [1.069, 1.145] | 1.075 [1.034, 1.143] | 2.746 [2.704, 3.13] | 4.481 [4.163, 5.316] | 0.9915 [0.9413, 1.006] (n=5) | 1.516 [1.431, 1.966] (n=5) |
| mixed-http / stats / queue | 0.5857 [0.5192, 0.6159] | 0.5614 [0.5127, 0.5887] | 1.035 [0.9994, 1.054] | 1.032 [1.013, 1.052] | 0.952 [0.8325, 1.134] (n=5) | 0.9919 [0.9628, 1.051] (n=5) |
| mixed-http / stats / service | 0.4691 [0.4541, 0.4871] | 0.4602 [0.4424, 0.4626] | 2.279 [2.1, 2.712] | 3.787 [3.708, 4.776] | 0.9791 [0.9269, 1.002] (n=5) | 1.662 [1.397, 2.075] (n=5) |
| sql-miss / channel_messages_sql / service | 76.11 [66.72, 81.74] | 106.3 [102.2, 118.3] | 85.14 [79.01, 103] | 122.7 [111.9, 183.9] | 1.433 [1.368, 1.531] (n=5) | 1.387 [1.292, 1.786] (n=5) |
| sql-miss / observer_metrics_sql / service | 0.7654 [0.7299, 0.8456] | 0.8473 [0.8147, 0.8923] | 1.314 [1.305, 1.467] | 1.382 [1.21, 1.409] | 1.071 [1.007, 1.166] (n=5) | 1.013 [0.8741, 1.072] (n=5) |
| websocket / new_transmission / receipt_after_ingest_return | 507.1 [506.1, 534.6] | 511.2 [505.5, 519.7] | 985.5 [969.2, 991.8] | 959.6 [946.8, 971] | 1.004 [0.9698, 1.027] (n=5) | 0.969 [0.9608, 0.9916] (n=5) |

## Eligible p99

| Scenario / class / timing | SQLite ms | PostgreSQL ms | Paired ratio (n) |
|---|---:|---:|---:|
| durable-ingest / additional / end_to_end | 7115 [6890, 7476] | 4.16 [3.517, 4.915] | 0.0005931 [0.0004829, 0.0006907] (n=5) |
| durable-ingest / additional / queue | 7076 [6890, 7308] | 1.124 [1.118, 1.137] | 0.0001588 [0.0001535, 0.0001651] (n=5) |
| durable-ingest / additional / service | 3.273 [3.075, 3.537] | 3.024 [2.628, 3.568] | 0.8551 [0.8029, 1.027] (n=5) |
| durable-ingest / duplicate / end_to_end | 7204 [6832, 7437] | 3.076 [2.793, 4.713] | 0.0004278 [0.0003756, 0.0006898] (n=5) |
| durable-ingest / duplicate / queue | 7196 [6812, 7436] | 1.118 [1.112, 1.14] | 0.0001553 [0.0001504, 0.0001673] (n=5) |
| durable-ingest / duplicate / service | 1.603 [1.484, 2.024] | 2.028 [1.936, 3.13] | 1.31 [1.153, 1.546] (n=5) |
| durable-ingest / new / end_to_end | 7113 [7046, 7302] | 3.862 [3.416, 4.425] | 0.0005296 [0.0004739, 0.000628] (n=5) |
| durable-ingest / new / queue | 7113 [7035, 7282] | 1.126 [1.115, 1.152] | 0.0001583 [0.0001532, 0.0001635] (n=5) |
| durable-ingest / new / service | 3.69 [3.642, 4.041] | 2.785 [2.578, 2.993] | 0.7308 [0.6877, 0.8112] (n=5) |
| websocket / new_transmission / receipt_after_ingest_return | 1389 [1330, 1892] | 999 [986.7, 1569] | 0.7334 [0.5279, 1.12] (n=5) |

## Durations, throughput and sampled resources

| Metric | Unit | SQLite | PostgreSQL | Paired ratio (n) |
|---|---|---:|---:|---:|
| durable-ingest/additional/completed_per_second_including_drain | per_second | 15.02 [15.02, 15.02] | 15.02 [15.02, 15.02] | 1 [1, 1] (n=5) |
| durable-ingest/duplicate/completed_per_second_including_drain | per_second | 7.515 [7.515, 7.515] | 7.515 [7.515, 7.515] | 1 [1, 1] (n=5) |
| durable-ingest/late/completed_per_second_including_drain | per_second | 2.506 [2.506, 2.506] | 2.506 [2.506, 2.506] | 1 [1, 1] (n=5) |
| durable-ingest/new/completed_per_second_including_drain | per_second | 25.03 [25.03, 25.03] | 25.03 [25.03, 25.03] | 1 [1, 1] (n=5) |
| migration/elapsed_ns | s | — | 147.6 [144.7, 151] | — (n=0) |
| migration/harness_validation_ns | s | — | 54.06 [53.28, 56.62] | — (n=0) |
| migration/migration_tool_ns | s | — | 90.99 [89.94, 94.13] | — (n=0) |
| migration/role_grants_ns | s | — | 0.7733 [0.7025, 0.8264] | — (n=0) |
| mixed-http/analytics_channels/completed_per_second_including_drain | per_second | 0.4109 [0.4109, 0.4109] | 0.4109 [0.4109, 0.4109] | 1 [1, 1] (n=5) |
| mixed-http/analytics_rf/completed_per_second_including_drain | per_second | 0.6157 [0.6157, 0.6157] | 0.6157 [0.6157, 0.6157] | 1 [1, 1] (n=5) |
| mixed-http/analytics_rf_filtered/completed_per_second_including_drain | per_second | 0.411 [0.411, 0.411] | 0.411 [0.411, 0.411] | 1 [1, 1] (n=5) |
| mixed-http/analytics_topology/completed_per_second_including_drain | per_second | 0.6156 [0.6156, 0.6156] | 0.6156 [0.6156, 0.6156] | 1 [1, 1] (n=5) |
| mixed-http/channels/completed_per_second_including_drain | per_second | 1.433 [1.433, 1.433] | 1.433 [1.433, 1.433] | 1 [1, 1] (n=5) |
| mixed-http/messages_0/completed_per_second_including_drain | per_second | 1.433 [1.433, 1.433] | 1.433 [1.433, 1.433] | 1 [1, 1] (n=5) |
| mixed-http/messages_1000/completed_per_second_including_drain | per_second | 1.229 [1.229, 1.229] | 1.229 [1.228, 1.229] | 1 [0.9993, 1] (n=5) |
| mixed-http/metrics_dense_24h/completed_per_second_including_drain | per_second | 1.026 [1.026, 1.026] | 1.026 [1.026, 1.026] | 1 [1, 1] (n=5) |
| mixed-http/metrics_sparse_7d/completed_per_second_including_drain | per_second | 1.026 [1.026, 1.026] | 1.026 [1.026, 1.026] | 1 [1, 1] (n=5) |
| mixed-http/nodes_2000/completed_per_second_including_drain | per_second | 1.432 [1.432, 1.432] | 1.432 [1.432, 1.432] | 1 [0.9998, 1] (n=5) |
| mixed-http/nodes_50/completed_per_second_including_drain | per_second | 1.433 [1.433, 1.433] | 1.433 [1.433, 1.433] | 1 [1, 1] (n=5) |
| mixed-http/nodes_region/completed_per_second_including_drain | per_second | 1.229 [1.229, 1.229] | 1.229 [1.229, 1.229] | 1 [1, 1] (n=5) |
| mixed-http/observers/completed_per_second_including_drain | per_second | 2.052 [2.052, 2.052] | 2.052 [2.052, 2.052] | 1 [1, 1] (n=5) |
| mixed-http/packet_detail_memory/completed_per_second_including_drain | per_second | 1.025 [1.025, 1.025] | 1.025 [1.025, 1.025] | 1 [1, 1] (n=5) |
| mixed-http/packet_detail_pruned/completed_per_second_including_drain | per_second | 1.108 [1.108, 1.108] | 1.108 [1.108, 1.108] | 1 [1, 1] (n=5) |
| mixed-http/packet_detail_sql/completed_per_second_including_drain | per_second | 1.034 [1.034, 1.034] | 1.034 [1.034, 1.034] | 1 [1, 1] (n=5) |
| mixed-http/packets_memory/completed_per_second_including_drain | per_second | 1.025 [1.025, 1.025] | 1.025 [1.025, 1.025] | 1 [1, 1] (n=5) |
| mixed-http/reach/completed_per_second_including_drain | per_second | 1.026 [1.026, 1.026] | 1.026 [1.026, 1.026] | 1 [1, 1] (n=5) |
| mixed-http/rx_coverage/completed_per_second_including_drain | per_second | 1.026 [1.026, 1.026] | 1.026 [1.026, 1.026] | 1 [1, 1] (n=5) |
| mixed-http/stats/completed_per_second_including_drain | per_second | 1.027 [1.027, 1.027] | 1.027 [1.027, 1.027] | 1 [1, 1] (n=5) |
| resource/aggregate_accounted_io_dbytes | MiB | 0 [0, 0] | 0.003906 [0, 0.003906] | — (n=0) |
| resource/aggregate_accounted_io_dios | operations | 0 [0, 0] | 1 [0, 1] | — (n=0) |
| resource/aggregate_accounted_io_rbytes | MiB | 0 [0, 0] | 5.762 [5.57, 6.875] | — (n=0) |
| resource/aggregate_accounted_io_rios | operations | 0 [0, 0] | 658 [641, 780] | — (n=0) |
| resource/aggregate_accounted_io_wbytes | MiB | 2360 [2335, 2378] | 433.6 [376.7, 507.6] | 0.1857 [0.1596, 0.2153] (n=5) |
| resource/aggregate_accounted_io_wios | operations | 2.187e+05 [2.14e+05, 2.236e+05] | 2.173e+04 [1.797e+04, 2.53e+04] | 0.1016 [0.08217, 0.1162] (n=5) |
| resource/average_cores_over_sampled_span | cores | 0.2422 [0.2295, 0.2484] | 0.3191 [0.3099, 0.3706] | 1.324 [1.28, 1.492] (n=5) |
| resource/cpu_delta_usec | CPU s | 43.45 [41.18, 44.58] | 56.86 [55.35, 66.47] | 1.332 [1.274, 1.491] (n=5) |
| resource/observed_peak_cgroup_memory_bytes | MiB | 4043 [4013, 4078] | 5681 [5581, 6144] | 1.415 [1.389, 1.518] (n=5) |
| resource/observed_peak_pss_bytes | MiB | 2922 [2892, 2925] | 3247 [3200, 3321] | 1.11 [1.1, 1.136] (n=5) |
| resource/observed_peak_rss_bytes | MiB | 2926 [2896, 2929] | 5055 [4965, 5221] | 1.728 [1.696, 1.803] (n=5) |
| resource/observed_peak_storage_bytes | MiB | 1098 [1095, 1122] | 2327 [2326, 2327] | 2.12 [2.073, 2.126] (n=5) |
| resource/observed_peak_wal_bytes | MiB | 40.01 [28.92, 53.02] | 1024 [1024, 1024] | 25.6 [19.31, 35.41] (n=5) |
| retention-empty/elapsed_ns | s | 0.0002677 [0.0002576, 0.000307] | 0.001732 [0.001672, 0.001941] | 6.471 [5.936, 7.459] (n=5) |
| retention/elapsed_ns | s | 10.49 [10.45, 10.73] | 0.905 [0.8444, 0.9622] | 0.08626 [0.0805, 0.09126] (n=5) |
| sql-miss/channel_messages_sql/completed_per_second_including_drain | per_second | 13.8 [12.73, 14.12] | 8.948 [7.699, 9.504] | 0.6471 [0.6049, 0.6733] (n=5) |
| sql-miss/observer_metrics_sql/completed_per_second_including_drain | per_second | 13.92 [12.83, 14.23] | 9.089 [7.87, 9.657] | 0.652 [0.6133, 0.6784] (n=5) |
| startup-hot1/first_http_ns | s | 9.565 [9.467, 10.18] | 11.48 [10.98, 11.68] | 1.187 [1.148, 1.223] (n=5) |
| startup-hot1/full_ready_ns | s | 20.52 [20.13, 21.75] | 20.15 [19.92, 20.65] | 0.9707 [0.9494, 1.006] (n=5) |
| startup-hot1/ready_ns | s | 9.565 [9.467, 10.18] | 11.48 [10.98, 11.68] | 1.187 [1.148, 1.223] (n=5) |
| startup/first_http_ns | s | 28.04 [27.44, 30.18] | 27.23 [27.14, 28.55] | 0.9713 [0.9383, 0.9896] (n=5) |
| startup/full_ready_ns | s | 28.06 [27.45, 30.19] | 27.44 [27.22, 29.31] | 0.9783 [0.9403, 0.9989] (n=5) |
| startup/ready_ns | s | 28.04 [27.44, 30.18] | 27.23 [27.14, 28.55] | 0.9713 [0.9383, 0.9896] (n=5) |

## Measured-window resource coverage

| Pair / backend | Samples | Sampled span / window s | Boundary gaps first / last s | Max interval s |
|---|---:|---:|---:|---:|
| 0 / sqlite | 180 | 179.425 / 180 | 0.318 / 0.257 | 1.006 |
| 0 / postgres | 175 | 179.331 / 180 | 0.298 / 0.371 | 1.058 |
| 1 / sqlite | 180 | 179.443 / 180 | 0.181 / 0.376 | 1.007 |
| 1 / postgres | 174 | 178.207 / 180 | 0.875 / 0.918 | 1.075 |
| 2 / sqlite | 180 | 179.432 / 180 | 0.207 / 0.361 | 1.009 |
| 2 / postgres | 174 | 178.591 / 180 | 0.875 / 0.534 | 1.102 |
| 3 / sqlite | 180 | 179.444 / 180 | 0.383 / 0.173 | 1.007 |
| 3 / postgres | 175 | 179.154 / 180 | 0.109 / 0.737 | 1.087 |
| 4 / sqlite | 179 | 178.460 / 180 | 0.833 / 0.707 | 1.026 |
| 4 / postgres | 175 | 179.421 / 180 | 0.295 / 0.284 | 1.118 |

## Acceptance counts

| Pair / backend | Scheduled / completed ingest | HTTP successes total / measured | Measured WS sentinels / missing / negative | Errors / drops |
|---|---:|---:|---:|---:|
| 0 / sqlite | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 1 | 0 / 0 |
| 0 / postgres | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |
| 1 / sqlite | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |
| 1 / postgres | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |
| 2 / sqlite | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |
| 2 / postgres | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |
| 3 / sqlite | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |
| 3 / postgres | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |
| 4 / sqlite | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |
| 4 / postgres | 12000 / 12000 | 4800 / 3600 | 4500 / 0 / 0 | 0 / 0 |

Per-run JSON retains per-device I/O deltas, exact sample bounds, counts, retention deletions, settings, provenance and input hashes.

## Provenance

- baseline_sha: 9dbc287579a237ffa744dd0c91fa7227d09763ac
- candidate_sha: ce023e0d623116fe66987761d06016590f98fb11
- harness_sha256: 207f4317b3dcf0395ca8f76f3a8d4398e2520d2aff429a3381b502becf856f8a
- go_version: go version go1.27.2 linux/amd64
- postgres_version: postgres (PostgreSQL) 18.6 (Ubuntu 18.6-1.pgdg24.04+2)
- compiler: cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0
- Workload: {"http_concurrency": 64, "http_rate": 20, "ingest_rate": 50, "ingestor_memory_mib": 512, "measured_seconds": 180, "packet_store_mib": 2048, "queue_limit": 1024, "retention_hours": 168, "server_memory_mib": 3072, "sql_reader_pool": 4, "sql_writer_pool": 1, "warmup_seconds": 60}
- Resource budget: {"allowed_cpus": [0, 1, 2, 3], "cpu_limit": 3.0, "enforced": true, "inactive_charge_reset_bytes": 5099520, "memory_limit_bytes": 6442450944, "memory_peak_reset": true, "resource_counters": ["cgroup.procs", "memory.current", "memory.events", "cpu.stat", "io.stat"]}

## Limits

- Five paired runs are the independent repetitions; request records are never pooled across runs.
- Absolute entries are medians of per-run quantiles, with observed min/max, not confidence intervals.
- p99 is omitted unless every contributing run has at least 1000 successful samples.
- Ingest service excludes replay queue wait; HTTP service includes response reading and validation. Queue and schedule-to-complete distributions are separate.
- Resource deltas use the first and last samples strictly inside the measured window; no boundary interpolation. Coverage and sampling gaps are reported.
- Memory/RSS/PSS/storage/WAL are observed sampled peaks, not proof against shorter spikes. Storage includes selected database files and WAL.
- I/O is per-device cgroup accounting. The aggregate can count layered devices twice and is not physical storage bytes.
- WebSocket lag is signed receipt minus ingest return; negative lags remain. Nonpositive baseline denominators have no ordinary speed ratio.
- Accounts are disabled by the reviewed harness. The OS cache is warm; startup uses a new process and approximately 100ms controller probes.
- No capacity, OS-cold, 65-minute soak or engine-wide causal claim follows from this report.
- Validation uses public digests and runtime evidence; it does not reopen private databases or payload files.

## Resource interpretation

The measured PostgreSQL cgroup-memory peaks for pairs 0–4 were 5581.371,
6143.984, 5742.367, 5680.508 and 5678.938 MiB. Their measured-window
`memory.events.max` deltas were 0, 444, 0, 0 and 0. All 174 samples in pair 1
were at least 99% of the 6-GiB limit; the nearest sampled headroom was 16 KiB.
SQLite's measured-window memory-event deltas were zero. All final OOM, OOM-kill
and OOM-group-kill counters were zero. Every PostgreSQL full leg approached
6144 MiB during import/startup, so the measured-window table must not be read as
a complete-run headroom or capacity guarantee.

Native `memory.peak` resets were checked for each cgroup transition; SQLite full
legs measured about 4231–4340 MiB after PostgreSQL legs near 6144 MiB, without
inheriting their prior peaks. PostgreSQL WAL was 1024 MiB in every measured
window, at its start and end: retained segments from import, not measured WAL
generation. Storage includes WAL. Cgroup per-device I/O accounting is not a
physical-disk-write estimate. Observed free filesystem minima (72.065 GiB in
measured windows; 70.817 GiB across full legs) describe this runner only, not a
required operator minimum.

## Preserved failed qualification

The [100-event/s run 37897747657](https://github.com/Kpa-clawbot/CoreScope/actions/runs/37897747657)
at `7b2c19a1eaf0050113c1ce8ae73cc69b6e541893` failed its SQLite leg. It completed
23,918 of 24,000 events and dropped 82, all during measured time, with zero
write errors and correct durable effects for completed work. The drop schedule
window was 205.92–206.76 seconds. Retention was scheduled at 195 seconds and took
11.744610088 seconds, deleting 16,000 transmissions and 256,000 observations.
Maximum ingestion queue wait was 11.054 seconds; source and raw timing review
identified maintenance-related queue saturation. The bounded queue held 1024
events: 10.24 seconds of offered arrivals at 100/s versus 20.48 seconds at 50/s.
This failed artifact supplies no cross-engine conclusion and is not pooled
with the final separately labeled common 50/s experiment.

## Production and harness hashes

The application source hash was equal before and after each build/measurement.
Documentation-only publication does not change which application was measured.

| Material | SHA-256 |
|---|---|
| Candidate production source | `74f4bdc613e1311f65dbbb27d216c186d499d82c05fbb7c9337e239a07b5ae5c` |
| Baseline production source | `7b786814eec6adc6c322a331727d9a4c2bd51a63456b6bb233f2a3327b39381c` |
| Benchmark harness | `207f4317b3dcf0395ca8f76f3a8d4398e2520d2aff429a3381b502becf856f8a` |
