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

## Follow-up stages (2026-09-16, batch 35C, commit 9ffece0)

The three REMAINING items above each became a stage. All ran on the OCI
measurement host (4 vCPU, arm64, Go 1.27.1) which was UNDER CO-TENANT LOAD
for the whole session (loadavg 4.8-11.5 from an unrelated project's browser
processes; every artifact directory records the loadavg at stage start).
The numbers are therefore upper bounds on cost and lower bounds on capacity -
which is the honest direction for a follow-up stage whose headline findings
are ratios and limits, not absolute throughput.

### Postgres-backed stage (tools/loadtest-pg.sh)

8 matches x 2 seats for 210 s - long enough for every match to FINISH inside
the window (all 8 did, both legs), so the terminal result INSERT is inside
the measurement, not after it. The pg leg runs against a THROWAWAY database
the script creates, migrates and drops (`wordarena_lt`; the script refuses any
non-throwaway name); the mem leg is the same stage back-to-back on the same
box with the DSN unset.

| metric (8x2, 210 s)      | Postgres leg | in-memory leg |
|--------------------------|--------------|---------------|
| create p50               | 0.39 ms      | 0.37 ms       |
| intent RTT p50           | 1.04 ms      | 0.81 ms       |
| intent RTT p99           | 6.4 ms       | 6.3 ms        |
| server CPU               | 0.75% of 1 core | 0.71% of 1 core |
| server RSS (end)         | 23.3 MB      | 20.9 MB       |
| matches finished in window | 8/8        | 8/8           |
| server-side intent processing (histogram) | 94% < 50 µs | n/a (not captured) |

Verdict: **the durable store is not on the hot path.** Create latency, intent
round trips, CPU and RSS are within noise of the in-memory leg while every
result row was written at match end. That is by design - results persist when
a match ends, not per intent - and now it is measured rather than assumed.
Create p95 (33-35 ms both legs) is one slow create out of eight under host
contention; treat p50 as the signal at this sample size.

### Per-seat intent limit stage

The per-seat limiter (`WORDARENA_INTENTS_PER_SEC`, default 60) is not a
reject-and-continue limiter: the 61st intent in a rolling second KILLS the
socket (`conn.Close(PolicyViolation, "intent rate limit exceeded")`). The
harness now reports `seats_dropped` so that behaviour is a measured outcome
instead of a silent stall.

Stage: 4 matches x 2 seats, `-intent-gap 5ms` (200 intents/s/seat attempted
against the 60/s budget), 30 s:

- **seats_dropped: 8 of 8** - every seat's connection was killed by the
  limiter.
- **intents sent 488 = 61.0 per seat** - the limiter enforced exactly
  budget+1 per seat before the kill; the in-flight 61st intent of each seat
  (8 total) never received an ack, which is the observable cost of the kill.
- No dial errors, no throttles: the per-CALLER mutation limit (120/min) never
  engaged - this stage pushes the per-SEAT limit only, as intended.
- After the kills the four rooms kept ticking to their natural end
  (abandoned-room behaviour, consistent with the 32F drain measurement).

Verdict: the per-seat budget is enforced precisely and per-seat (a seat
breaching does not disturb the others), and the current design consequence -
a hard disconnect rather than a retryable rejection - is now documented and
measured. If product ever wants a gentler per-seat policy, that is a rules
change with its own evidence, not a bug.

### Generator off-box stage (tools/loadtest-offbox-target.sh)

The generator ran on a DIFFERENT machine (2 vCPU x86_64 sandbox) against the
target on the OCI host, through an SSH tunnel - no new listening surface: the
isolated server stays loopback-only. This adds REAL network latency to the
measurement (~140-190 ms path RTT, tunnel /healthz baseline p50 189.8 ms /
p95 191.2 ms over 150 probes), which is the dimension the 32F stages
explicitly did not cover. The new `wordarena_intent_process_us` histogram
(`/metrics` JSON + Prometheus, Grafana panel 10) separates what the path
costs from what the server costs.

Stage A - 8 matches x 2 seats, 45 s, intent every 2 s:

- client-observed intent RTT: p50 67.3 ms, p95 126.7 ms, p99 166.5 ms
- create p50 63.9 ms (vs 0.4 ms on-box: the path, again)

Stage B - 2 matches x 60 seats (120 clients), 40 s, intent every 2 s:

- client-observed intent RTT: p50 131.7 ms, p95 145.4 ms, p99 188.9 ms
- per-client wire 922 B/s (the top of the 32A band, consistent with the 32F
  in-box ~900 B/s full-lobby figure), 108.1 KB/s aggregate for the two
  lobbies
- 2400/2400 intents acked, zero dropped seats, zero errors

Server-side, both stages combined (10 matches, 2468 intents; the histogram
count equals intents_received, which is itself a consistency check):

- mean intent processing **0.57 ms**; 84% of intents < 50 µs, 92% < 100 µs,
  95.5% < 2.5 ms; the 36 observations above 10 ms are the 60-seat fan-out
  frames (encode-once-per-frame broadcast work), not queueing
- server CPU for the whole 239 s window: 0.018 cores; RSS 12.7 -> 32.1 MB
  with 10 live rooms at window end

Verdict: **the path is ~99% of the latency** - a client-observed 67-132 ms
round trip contains a 0.57 ms mean of server work. The server does not
become the bottleneck when real network latency enters the picture; the
generator's own box (2 vCPU) drove 120 clients without distress.

### The port-safety incident (recorded, not hidden)

The first Postgres-stage run of this batch silently drove the LIVE systemd
service instead of its own isolated instance: the load scripts default to
port 18080, and on the measurement host the live service listens on
127.0.0.1:18080 - a comment in `loadtest-isolated.sh` claimed that port was
"deliberately not the service port", which was true in the sandbox and CI
runner where the tooling was developed and false on the host it measures on.
Seventeen throwaway test matches landed on the live deployment - equivalent
to a routine smoke run (no data impact, nothing rating-eligible), but exactly
what the isolation rule forbids. The 32F numbers are unaffected: those runs
predate the service's move to that port and their committed report shows a
genuinely isolated instance (its server CPU/RSS sampling could only have
come from the stage's own process). Fix: all three load scripts now REFUSE
to build or start when anything already listens on the chosen port (the
occupant is named), and after /healthz answers they assert the listener pid
is the one they started - a foreign listener or an early crash of our own
server both abort the stage loudly.

## Reproducing

```sh
# default stage: 8 matches x 2 seats for 30 s on an isolated instance
tools/loadtest-isolated.sh

# a full-lobby stage
MATCHES=3 SEATS=60 DURATION=30 tools/loadtest-isolated.sh

# what an abandoned match costs: no clients at all
tools/loadtest-isolated.sh 400 2 60 &   # then: go run ./server/cmd/loadtest -skip-clients ...

# Postgres-backed stage vs in-memory, back to back (batch 35C)
# On the measurement host pick a FREE port - the live service owns 18080
# there and the scripts now refuse to run against it.
WORDARENA_LOADTEST_PORT=28080 tools/loadtest-pg.sh 8 2 210

# per-seat intent limit: 200 intents/s/seat against the 60/s budget
WORDARENA_LOADTEST_PORT=28080 INTENT_GAP_MS=5 REPORT_JSON=perseat.json \
  tools/loadtest-isolated.sh 4 2 30

# generator off-box (two terminals, two machines):
#   target host:  tools/loadtest-offbox-target.sh 240 28081
#   generator:    ssh -N -L 28081:127.0.0.1:28081 <target-host>   # then
#                 go run ./server/cmd/loadtest -url http://127.0.0.1:28081 \
#                   -matches 8 -seats 2 -duration 45s -json offbox.json
```

`tools/loadtest-isolated.sh` writes `loadtest-report.json` (machine-readable);
`REPORT_JSON=...` redirects it so a sweep can keep one file per stage.

CI runs a two-match, three-second version of the same code path
(`go test ./cmd/loadtest/`), which is a regression guard, not a capacity claim.

## Not measured here (and why)

- **Multi-hour soak.** Room lifetime is 180 s, so a soak mostly measures
  whatever the harness does on minute three. The abandoned-room stage covers the
  interesting part (steady state with no clients) far more cheaply.
- **Adversarial network conditions.** The off-box stage adds real WAN latency
  (~190 ms path RTT) but not loss or jitter; the M1 netem tests cover
  adversarial conditions, this covers scale and path decomposition.
- **Profiled matches under load** (player_ids attached, profile rows and stats
  fold on match end). The Postgres stage exercised match-result writes; the
  profile path is the same single-write-at-end shape, but it has not been
  staged separately.

## Soak verdict (M2 batch 37B, 2026-09-17)

Two-hour-plus soak of one isolated in-memory server instance (never the
live service; `WORDARENA_LOADTEST_PORT` discipline throughout, isolated
`WORDARENA_ADDR=127.0.0.1:18099`, detached from the long-lived proxy
connection so sandbox network timeouts cannot touch it). All artifacts under
`/home/ubuntu/artifacts-37b/` (v1, v2, v3 subdirectories).

**Soak v1** (`e22504a`, 06:00-06:20): 5 complete legs (cycle 8x2 / 12x2 / 2x8
seats, 600 s per leg), then the run was truncated by an OPERATOR ERROR, not
by the product: a `pkill -f "wordarena$"` typed during an unrelated repro
setup matched the soak server is own path suffix and gracefully SIGTERMed it
(server.log: "signal terminated received: shutting down", 06:09:07). Legs
7-12 failed fast against the dead listener. Rule codified (state facts):
kill long-lived test instances by EXACT PID files only on this shared host.
v1 partial data: RSS 12.8 -> 23.9 MB across 5 legs with a decelerating
slope, fds flat at 6, threads 9 -> 11, 44 matches / 3300 intents, zero
create/dial errors.

**Soak v2** (12/12 legs, 06:30-08:31, 88 matches, 9384 intents): the soak did
exactly what a soak exists for and caught a real defect.

- **Defect found: handleWS goroutine-leak deadlock (fixed, batch 38A /
  PR #61).** The goroutine series climbed perfectly linearly 7 -> 455 across
  the 12 legs = +2 per closed websocket connection, and NEVER decreased
  between legs despite active_matches returning to 0. Goroutine pprof at
  soak end: 224 handlers parked in handleWS at the `<-done` tail + 224
  writer goroutines parked in their select. Root cause: the handler tail
  waits for the writer (`<-done`), while the writer is only exit path was
  `r.Context().Done()` - which is cancelled when the handler RETURNS, i.e.
  never for a client-initiated disconnect. Fix: writer + all its writes now
  run on a context the handler cancels BEFORE waiting on `<-done`.
  Regression test `TestWSGoroutinesReapedOnClientDisconnect`: pre-fix
  abandon-cycle deltas 5,5 (4 handler goroutines + 1 room ticker), post-fix
  1,1 = only the legitimate `runRoomTicker` of each still-resumable match
  (SeatTTL semantics preserved by design). Post-fix confirmation soak v3
  (`d202e4b`, 3 legs, otherwise identical protocol): goroutines and RSS
  return to baseline between legs (see v3 series in artifacts).
- **RSS 13.2 -> 33.7 MB with matching heap-inuse only 0.54 -> 3.56 MB:**
  the growth was almost entirely leaked-goroutine stacks plus allocator
  retention, NOT accumulated match state. Post-38A that reclaimable class
  is gone. fds flat 6-7, threads flat 9-13 across the whole run.
- **Correctness/header integrity:** create_errors=0 and dial_errors=0 on
  every leg; active_matches returned to 0 between legs every leg (no room
  leaks); zero server restarts or error-level log lines across 2 h.
- **Royale (2x8) legs measure transport and room churn, not scoring
  pressure.** ~95% of royale intents were rejected (BLOCKED_BY_RULE,
  NOT_IN_DICT, MATCH_NOT_ACTIVE after a match is over+3 s close), and the
  accepted count plateaued at wave boundaries (cull -> spectator). The
  word-search geometry on the royale-scaled board is the harness side of
  that; follow-up task 37E is queued for the harness. `seats_dropped` on
  royale legs is likewise a HARNESS ACCOUNTING artifact: `runRoomTicker`
  closes rooms at over+3 s by design, royale matches always end mid-leg, so
  the close counts still-connected seats as "dropped". Neither number
  indicates a product defect.
- Duty cycle documented: the harness creates its cohort once per leg and
  drives it for the leg is duration; a match is ~180 s live span ends well
  inside the 600 s leg, so later leg minutes exercise disconnect/close
  handling (which is precisely how the leak surfaced).

**Corrected by batch 38C (see the next section):** both royale-leg readings
in the bullet list above - `seats_dropped` and the accepted plateau - came from
ONE harness defect (it read only full snapshots, so in delta mode it flew a
frozen board and never saw the match end). They were not two independent
artifacts, and the same defect also inflated those legs' rejected-intent share.

**Outcome:** launch-scale transport is stable across the tested envelope
(matches/8..12 concurrent, seats 2..8, ~40 conn churn events/leg sustained
over 2 h). The one defect the soak surfaced is fixed, regression-guarded,
and re-confirmed by a post-fix soak; the M2 load-testing line is satisfied
by the combination of the earlier stage suite + this soak.

(The earlier "Not measured here: multi-hour soak" note above is superseded
by this section - it predicted a soak would mostly measure empty harness
time; instead it found the one defect that every shorter stage missed.)

### Batch 38C: the harness was flying a frozen board (2026-09-17)

The soak's two royale-leg findings above were classified as harness artifacts;
this batch found the single cause: **the harness read only full snapshots.** The
server switches a subscriber to deltas after its first frame (batch 32A), and in
delta mode it never sends another full snapshot - so the harness kept spelling
words over the board it received at dial time and never learned that the match
had ended, because the terminal state arrives as a delta whose `over` flag it
ignored.

Three runs of the same 4-seat leg (195 s, isolated instance on 127.0.0.1, `en`
dictionary, one match) separate the two changes:

| harness build | seats_dropped | close statuses | intents sent / accepted | reading |
|---|---|---|---|---|
| main (pre-38C) | 4 | - (field absent) | 368 / 15 | the reported artifact |
| 38C part 1: post-terminal closes counted in their own field | 4 | 1011 internal error x4 | 368 / 15 | **still 4** - no terminal frame had been observed, so nothing could be reclassified; the closes came from the submit-error path after the match was gone |
| 38C complete: deltas applied under the base_version rule | 0 | 1000 normal x4 | 48 / 12 | the seats see the match end, stop submitting, and are not counted as dropped |

The fix applies `protocol.ApplyDelta` exactly as the protocol documents it: a
delta whose `base_version` is not the receiver's `state_version` is dropped and
the next full snapshot self-heals (counted as `deltas_stale`, 0 in this run).
The terminal frame is now recognised from either frame kind, and a seat whose
connection ends *after* it is counted in `seats_closed_after_over` rather than
folded into `seats_dropped` - the counters stay apart on purpose, because
merging them would re-hide the kill the 35C stage added `seats_dropped` for.

What a corrected Royale leg shows:

- **A small-roster Royale match is short, not 180 s.** The board scales with the
  roster (Q10), so a 4-seat board is small and a machine client clears all three
  waves in ~24 s (server log: `lifecycle=over ticks=721 winner=3 scores=0:21`).
  A leg of a few seats therefore spends most of its wall clock after the match
  has ended: a leg meant to measure scoring pressure needs a Royale-sized roster,
  or the match budget has to be read as "however long the board lasts".
- **Post-over intents are refused, not dropped.** They return
  `MATCH_NOT_ACTIVE` word events; the old harness turned them into a server close
  with status 1011 ("submit failed"), which is where the phantom `seats_dropped`
  came from. A client that stops at the terminal frame never takes that path.

One lifecycle observation, recorded rather than changed: the room is removed
~3 s after the terminal frame (docs/WIRE-PROTOCOL.md) and its subscriber channels
close, but the server does not close the client's socket - the handler's reader
stays blocked in `Read` until the client acts or disconnects. Measured here as
`GET /v1/match/ws ... dur=3m15s` for a run whose match ended at 24 s. The
documented contract says only that the room is removed, so this is not a
contract violation; it is queued as a candidate task, because a client that idles
after the end of a match holds one goroutine and one socket per seat.

Artifacts (Arena sandbox, `artifacts-38c/`): `prefix-royale.json`,
`fixed-royale.json`, `fixed2-royale.json`.

