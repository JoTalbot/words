# M0 Load Baseline (acceptance 8)

Measured 2026-09-08 on the dev host (OCI Ampere A1, 4 × Neoverse-N1
vCPU, Ubuntu 24.04, Go 1.27; host shared with unrelated services).

## Throughput

`go test ./internal/match/ -run TestLoadBaseline -v` (32 concurrent
matches, 2 000 submissions each, tick mixed in):

| Metric | Value |
|---|---|
| Submissions/sec, total | ~1.48 M |
| Submissions/sec per match | ~46 k |
| Heap delta per match | ~463 KiB (includes unbounded event log of 2 000 events) |

The event log dominates per-match heap at this depth (≈237 B/event);
production rooms will bound retained replay depth (deterministic replay can
re-stream from compact event sinks).

## Dictionary validation

`go test ./internal/dictionary/ -bench BenchmarkContains` (in-process,
en_US snapshot, includes normalization; ARM host above):

| Metric | Value |
|---|---|
| Contains (normalize + lookup) | ~382 ns/op (~2.6M validations/s/core) |
| Cold load of 123k-word uk snapshot | not measured separately (cached after first load) |

## Methodology notes

- In-process domain benchmark; transport and network are excluded by
  design (M0 rules: benchmark raw simulation separately).
- A single logical CPU at 30 Hz consumes far below 1% of one core per
  match; tick throughput is not the bottleneck for the M0 scale target.
- These are engineering baselines, not capacity commitments. Re-measure on
  production-class hardware before capacity planning (docs/ROADMAP.md gate
  philosophy).

## Targets (hypotheses, to be revised from evidence)

- Sustained 30 Hz ticks for ≥ 1 000 concurrent matches per process: no
  measured blocker so far (per-match work is O(12 cells) per tick).
- Word validation is a hash lookup after normalization: benchmark in
  `internal/dictionary` when shipping full-size dictionaries.
