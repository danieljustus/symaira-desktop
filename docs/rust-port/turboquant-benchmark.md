# TurboQuant Go vs Rust benchmark (#1137)

Regenerate with `make retrieval-turboquant-benchmark` on an idle host.

Host darwin/arm64, 12 CPUs, go1.26.9; dim=768 bit_width=4 seed=42 vectors=2048 queries=32 rounds=5 (+1 warm-up), best of 3 alternating trials.
Encoded sidecar checksum 12355152198258238463 identical; scan score sums agree within 1e-6. Budget: Rust p95 and throughput within 10% of Go. Verdict: PASS.

| Operation | Impl | p50 | p95 | Throughput |
|---|---|---:|---:|---:|
| encode sidecar (1 vector) | Go | 26.875µs | 30.333µs | 35286 vectors/s |
| encode sidecar (1 vector) | Rust | 20.125µs | 21.25µs | 49245 vectors/s |
| quantized scan (2048 sidecars) | Go | 46.117792ms | 54.316834ms | 43134 scored/s |
| quantized scan (2048 sidecars) | Rust | 10.445542ms | 10.712709ms | 195777 scored/s |
