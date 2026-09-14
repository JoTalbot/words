# M2 — Infrastructure load testing (batch 32F)

Status: harness, isolation procedure and first measurements. Register: M2 row
"infrastructure load testing".

## The question

Not "how many players fit" — that number depends entirely on the box, and a
number without a method is a rumour. The questions worth answering are:

1. **What does one instance actually cost per match and per client**, measured
   from outside, over the real transport?
2. **What breaks first** when it is pushed: CPU, memory, bandwidth, a limiter,
   or the client's own scheduling?
3. **What has to be true of the next test** so that the numbers stay comparable?

Question 2 is the point of the exercise. The first version of this run answered
it immediately and not at all where the plan expected.

## What runs

- `server/cmd/loadtest` — the harness. It creates matches over the real HTTP
  surface, connects one WebSocket client per seat, submits **dictionary words
  over free cells** on a fixed cadence, and measures create latency, intent
  round-trip latency (p50/p95/p99), frames and bytes per kind, server CPU and
  RSS from `/proc`, and whether the instance is still healthy afterwards.
- `tools/loadtest-isolated.sh` — the safe path: build, start a **separate**
  server on a spare port with in-memory storage, drive it, drain it, tear it
  down. The live systemd service is never a target.
- `server/cmd/loadtest/loadtest_test.go` — a small CI run against a real server
  process, so the tool cannot silently rot between capacity runs.

Three properties make the numbers mean something:

- **Real words only.** The harness loads the dictionary and prefers the shortest
  word the free cells can spell. A generator that submits nonsense mostly
  measures the cheap rejection path; the expensive path is an accepted word
  (scoring, event, state version, broadcast to every seat).
- **Honest accounting of the rejection path.** When the board can spell nothing,
  the client *waits* and counts a skip, because that is what a player does. The
  `-submit-invalid` flag exists to exercise the rejection path deliberately.
- **Everything that limits the run is reported**: creations throttled with 429,
  dial errors, skips, and the intent-outcome histogram. A capacity figure from a
  run that quietly dropped half its load is worthless.

## Isolation rule

Load runs are destructive to whatever hosts them: rooms keep ticking to the end
of the match whether or not anyone is connected, memory grows with live rooms,
and intents consume the mutation and intent budgets. So:

- run only against an instance you started for the purpose
  (`tools/loadtest-isolated.sh`, spare port, in-memory storage);
- never against the live `wordarena.service`;
- if you must measure with Postgres, point it at a throwaway database and say
  so in the report — persistence has its own bottleneck and mixing them makes
  neither number actionable;
- record the box (CPU count, RAM) and the commit next to the numbers. An
  unlabelled latency number is not evidence.

## Measurements (2026-09-14, batch 32F, commit 1ce9aba)

Box: 4 vCPU sandbox, 23 GiB RAM, harness **running on the same box** (so
latency and CPU are upper bounds — the generator competes with the server for
scheduling). Server: in-memory storage on an isolated port, `WORDARENA_MAX_SEATS=60`,
mutation limit raised for the measurement runs (see "what breaks first").
Each stage: 30 s held load, one intent per client every 2 s; the last stage
deliberately has no clients at all.

| stage | rooms | clients | create p95 | intent p95 | wire/client | wire total | server CPU | server RSS |
|---|---|---|---|---|---|---|---|---|
| small 8×2 | 8 | 16 | 15.0 ms | 3.0 ms | 163 B/s | 2.6 KB/s | 1.7 % of a core | 10.6 → 18.7 MB |
| lobby 3×60 | 3 | 180 | 1.1 ms | 56.0 ms | 900 B/s | 158 KB/s | 4.7 % of a core | 20.1 → 33.3 MB |
| mid 120×2 | 120 | 240 | 0.5 ms | 32.1 ms | 169 B/s | 39.5 KB/s | 10.3 % of a core | 38.9 → 53.3 MB |
| big 300×2 | 300 | 600 | 0.4 ms | 67.4 ms | 178 B/s | 104 KB/s | 12.7 % of a core | 60.0 → 94.0 MB |
| abandoned 400×2 | 400 | 0 | 0.4 ms | — | — | — | 10.9 % of a core | 107.6 MB steady |

Zero read errors, zero write errors, zero failed creates once the mutation limit
was opened, `/healthz` healthy after every stage, and `active_matches` returned
to 0 in every stage after the drain: **no room leak, no dropped frame, no
degraded terminal state** in any of these runs.

### What the numbers say

- **Wire cost matches the 32A budget.** A 1v1 client costs 163–178 B/s, and a
  client in a full 60-seat lobby costs ~900 B/s — the top of the 0.6–0.9 KB/s
  band 32A predicted from the frame layout. A full lobby is ~50 KB/s, measured
  here as 158 KB/s for three lobbies. The wire was never the constraint, and now
  it is measured rather than argued.
- **A room is cheap to tick**: 400 rooms held with no clients cost 6.6 CPU
  seconds over 60 s = **0.28 ms of CPU per room per second**. A room is not the
  unit that runs out first on a box this size.
- **Memory is the first resource to watch**, at ~270 KB per live room (107.6 MB
  for 400). Unlike CPU it has no idle floor: it grows with every unfinished
  match and is only released when the match ends.
- **A full lobby's cost is per-frame work, not bytes.** 180 clients submitting
  90 intents/s produced 162 000 `word_event` frames in 30 s — 5 400 frames/s —
  because every evaluated intent is broadcast to every seat. Word events are
  only ~29 bytes each, so the byte budget hides this entirely: **the cost of a
  lobby scales with intents × seats**, and a design that lowers bytes without
  lowering frames has not addressed it.
- **Latency is scheduling, not the protocol, in these runs.** p95 rose from
  3 ms (16 clients) to 67 ms (600 clients) while server CPU stayed at 12.7 % of
  one core, which is the signature of a generator and a server sharing 4 vCPUs,
  not of a saturated server. Definitive latency numbers need the generator on a
  different machine; the numbers above are upper bounds and should be quoted as
  such.

## What breaks first (measured, not predicted)

**The first ceiling anyone will meet is the mutation rate limit, not capacity.**
The first attempt at a 400-room stage created 121 rooms and then failed every
remaining create with:

```
429 {"error":"too many requests from this caller"}   (Retry-After: 60)
```

`WORDARENA_MUTATIONS_PER_MIN` defaults to **120 per caller per minute**
(`envInt("WORDARENA_MUTATIONS_PER_MIN", 120)` in `cmd/game/api.go`). This is the
per-IP admission control for state-changing calls from S1 (it protects against
match-creation floods, and it is deliberately separate from the per-seat intent
limit so a NATed household is not throttled behind it). The consequence for
capacity planning is concrete:

> A burst of N matches produced by one caller takes at least N/120 minutes,
> because the limiter — not the box — is the gate.

The harness now honours `Retry-After`, counts throttles in the report
(`create_throttled`), and offers `-create-rate` to pace creations under the
limit instead of waiting it out. The capacity stages above were run with the
limit raised on the isolated instance *on purpose*, and that is recorded here
rather than hidden: an admission control is not the thing being measured, but it
is the first thing a real burst meets.

**Second: rooms outlive their clients.** A room runs to the end of the match
regardless of who is connected. Measured drain with every client gone:
**180.2 s** — the full match length (`WaveTicks × 3 / 30 Hz`). So capacity is a
product, not a count:

```
live rooms  ≈  creations per second × match length (s)
```

A burst of 400 matches in a few seconds leaves 400 rooms ticking for ~3 minutes,
which is why the abandoned-room stage exists: that is the state a box sits in
after a matchmaking spike, or after an incident that dropped every client at
once. It is also why the memory figure above (not CPU) is the one to watch.

**Third (not reached here): per-seat intent limits.** The 60-seat lobby stage
ran 90 intents/s across 180 clients, all inside the per-seat budget, with zero
rejections for rate (the 2 676 `BLOCKED_BY_RULE` outcomes are two clients racing
for the same cells, which is the game, not the limiter). A run that pushes
intent rate per seat is the next thing to measure, and the harness's
`-intent-gap` is the knob.

## Reproducing

```sh
# default stage: 8 matches x 2 seats for 30 s on an isolated instance
tools/loadtest-isolated.sh

# a full-lobby stage
MATCHES=3 SEATS=60 DURATION=30 tools/loadtest-isolated.sh

# what an abandoned match costs: no clients at all
tools/loadtest-isolated.sh 400 2 60 &   # then: go run ./server/cmd/loadtest -skip-clients ...
```

`tools/loadtest-isolated.sh` writes `loadtest-report.json` (machine-readable);
`REPORT_JSON=...` redirects it so a sweep can keep one file per stage.

CI runs a two-match, three-second version of the same code path
(`go test ./cmd/loadtest/`), which is a regression guard, not a capacity claim.

## Not measured here (and why)

- **Postgres-backed runs.** The migration path, the result row and the replay
  fetch have their own cost profile; measuring them inside a room-transport test
  would blur both. The isolated script deliberately omits a DSN.
- **Multi-hour soak.** Room lifetime is 180 s, so a soak mostly measures
  whatever the harness does on minute three. The abandoned-room stage covers the
  interesting part (steady state with no clients) far more cheaply.
- **Real network latency or loss.** The generator is on loopback. The M1 netem
  tests cover adversarial conditions; this covers scale.
