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

## Re-measurement on current main (2026-09-10)

Same OCI host (Ampere A1, 4 × Neoverse-N1), Go 1.27.1, commit 9a942df, host
otherwise idle at measurement time (previous numbers were captured while the
host ran unrelated load):

| Metric | 2026-09-08 | 2026-09-10 | Delta |
|---|---|---|---|
| Submissions/sec, total (32 matches) | ~1.48 M | ~1.84 M | +25 % |
| Submissions/sec per match | ~46 k | ~57.6 k | +25 % |
| Heap delta per match | ~463 KiB | ~382.6 KiB | -17 % |
| Dictionary Contains | ~382 ns/op | ~375.9 ns/op | stable |

No regression introduced by M1 batches 1–16 (matchmaking, persistence
interfaces, telemetry, readiness gate, token rotation, protocol fuzz seeds
are off the hot simulation path). The 2026-09-08 figures remain the
conservative planning baseline; treat the delta as host-load variance, not
optimization.

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

## Roster scaling (M2 batch 30F, measured 2026-09-14)

Measured by `TestRosterLoadBaseline` in `server/internal/match` on the dev
sandbox (2 logical CPUs), one match per row, one core, 3 000 ticks and 2 000
snapshots each:

| Seats | Tick | Snapshot | Realtime headroom (1 core) |
|---|---|---|---|
| 2 | ~68 ns | ~2.2 µs | x488 000 |
| 8 | ~38 ns | ~2.2 µs | x881 000 |
| 16 | ~29 ns | ~2.4 µs | x1 157 000 |
| 30 | ~108 ns | ~3.8 µs | x309 000 |
| 60 | ~102 ns | ~6.5 µs | x325 000 |

Reading:

- **Tick cost does not scale with the roster.** It stays in the tens of
  nanoseconds and is dominated by the 12-cell board, exactly as the
  board-sized design predicts; the 2-vs-16-seat ordering inverts because the
  numbers are below the noise floor of a shared 2-CPU sandbox.
- **Snapshot cost does scale with the roster**, roughly 3x from 2 to 60 seats,
  because a snapshot renders every player. At 30 Hz a 60-seat match spends
  ~0.2 ms/s rendering snapshots, so it is not a bottleneck either - but it is
  the term that grows, and it is per-subscriber fan-out (not this measurement)
  that will dominate a Royale match: 60 subscribers each receiving a snapshot
  that is itself 60 players long is 30x the bytes of a 1v1 at the same rate.
- **Conclusion for Royale sizing:** the simulation is not the constraint. The
  constraint to design against is snapshot fan-out bandwidth, which argues for
  delta or interest-scoped snapshots before a 60-player mode ships, not for a
  faster tick.

These are engineering baselines on a shared sandbox, not capacity commitments.

## Snapshot fan-out (M2 batch 31B, measured 2026-09-14)

Batch 30F identified per-subscriber fan-out as the real Royale constraint.
Measured by `TestSharedFanoutIsCheaperThanPerSubscriber` and the
`BenchmarkSnapshotFanout*` family in `server/internal/protocol`, fanning ONE
canonical 60-seat snapshot out to 60 subscribers:

| Strategy | Time | Allocations |
|---|---|---|
| Encode per subscriber (old) | ~906 µs | 8 280 |
| Encode once, share (current) | ~15 µs | 139 |

**~60x cheaper, ~60x less garbage.** The saving is exactly the roster size,
which is the point: a canonical snapshot is byte-for-byte identical for every
seat, so rendering and marshalling it once per connection multiplied the work
by the number of players for no benefit at all. At 1v1 the waste was invisible
(2x); at 60 seats it dominated.

Mechanism: `matchroom.SharedPayload` is a lazily-encoded, `sync.Once`-guarded
box attached to each broadcast frame. Every subscriber of that frame receives
the *same pointer*; the first one to write pays for the encoding and the rest
reuse the bytes. The room stays protocol-agnostic - it carries the box, the
transport supplies the encoder - and the shared slice is read-only.

Note this reduces CPU and allocation, **not bandwidth**: each of the 60
subscribers still receives the full snapshot over its own socket. Cutting the
bytes on the wire needs delta or interest-scoped snapshots, which require a
protocol change (`protoc` is not available in the dev sandbox) and are
therefore tracked separately.

## Snapshot wire cost and state deltas (M2 batch 32A, measured 2026-09-14)

Measured by `TestDeltaStreamReconstructsEveryFrame` in
`server/internal/protocol` on the dev sandbox: a 60-seat match driven through a
real room for its whole lifetime (180 frames at 1 Hz), with every submission
and every cell change produced by the simulation rather than by a fixture. The
test asserts that applying each consecutive delta to the receiver's state
reproduces the full snapshot exactly at every frame, so these byte counts
describe a stream that is also proven correct.

Two load profiles, because a delta can only save what did not change:

| Profile | Submissions accepted | Full snapshot | Delta | Reduction |
|---|---|---|---|---|
| saturated (20 attempts / 15 ticks) | 1 040 in 180 s (~5.8 / s) | ~1 463 B/frame | ~869 B/frame | 1.7x |
| typical (4 attempts / 30 ticks) | 720 in 180 s (~4 / s) | ~1 316 B/frame | ~603 B/frame | 2.2x |

Per client at the 1 Hz snapshot rate that is **0.6–0.9 KB/s**, down from
1.3–1.5 KB/s, and the full frame carries the entire 60-player roster and the
60-cell board in both profiles.

### Where the bytes actually go

The saturated run's churn, attributed to the field that caused each changed
entry (179 delta frames):

| Channel | Entries | Share of the change set |
|---|---|---|
| board: ownership change | 3 455 | 19 / frame |
| board: lock state change | 3 288 | 18 / frame |
| board: lock countdown only | 3 400 | 19 / frame |
| players: score change | 2 011 | 11 / frame |
| players: rank change | 127 | 0.7 / frame |
| board: wave letter change | 8 | — |

This is the number that matters for planning, and it is not the one the roadmap
hypothesis assumed. At a realistic claim rate most of the board is moving
between frames, so ~2x is the honest ceiling for a delta over this schema, not
the 10x the "cut the bytes" framing implied. Options that would go further, and
why they were not taken in this batch:

- **Replace `lock_remaining_ms` with a stable lock start/dispatch field.** The
  countdown is ~19 changed cells per frame (~27 % of the delta) purely because a
  display-only value is re-sent every second while the client has to interpolate
  it locally anyway. This is the single largest remaining win and it is a clean
  change, but it alters a field the M1 client reads, so it is a client-contract
  change rather than a server optimisation. Recorded as a follow-up, not done
  silently inside a bandwidth batch.
- **Interest-scoped snapshots.** Not applicable: every seat needs the whole
  board and the whole scoreboard in this mode, so scoping would cut nothing.
- **Lower the snapshot rate above some roster size.** Halves the bytes and
  doubles convergence latency; a product trade-off, not a free win.

### What this means for the 60-player row

The mode's wire cost is now characterized rather than assumed: ~0.6–0.9 KB/s
per client at 60 seats, i.e. roughly 50 KB/s of aggregate egress for a full
lobby, after the ~2x reduction this batch delivers. Bandwidth is not a
capacity blocker at this scale, so the remaining work on the row is the
product question about bot backfill and exposing the seats parameter on the
HTTP surface - not further byte shaving.
