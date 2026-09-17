# M3 — Behavioral anti-cheat signals

Status: **measurement layer live** (batch 40A). Enforcement: **none**, and it
is owner-gated by design (see "The enforcement boundary").

## Why this exists

AGENTS.md fixes the product constraint: anti-cheat is multi-signal risk
assessment, not an automatic ban from one heuristic. M3's "behavioral
anti-cheat signals" roadmap row therefore starts with the half that needs no
product decision and no owner input: **making abuse patterns observable**.
A signal that nobody records cannot be weighed later, and a heuristic that
nobody measured cannot be calibrated. The enforcement half (what to do when
signals agree) is a genuine product decision and stays with the owner.

## The boundary (load-bearing)

Nothing in `server/cmd/game/behaviorsignal.go` may reject, delay, score,
rank or otherwise influence a request, a seat, a room or a match outcome.
The signals are written on the side of the intent path, exported to
operators, and consumed by nothing else. This mirrors the rule the security
package already carries ("a limit may reject a request earlier, never change
what a successful request means") and the server-authority rule: match
determinism is a function of the simulation alone.

The day any enforcement consumes these signals, it must:

1. be a separate, reviewed change with its own product decision entry;
2. weigh multiple signals together against a declared, documented policy;
3. never act on a single heuristic automatically;
4. keep ranked play auditable (replay + telemetry events remain the record).

## Signals (batch 40A)

| signal | fires when | thresholds | once per |
|---|---|---|---|
| `rejection_streak` | a seat accumulates consecutive rejected intents (`rejected_not_in_dict`, `blocked_by_rule`, `invalid_input`) | 20 consecutive; an accepted word resets; `MATCH_NOT_ACTIVE` (the designed post-over refusal, batch 38C) never counts | streak episode (re-armed by acceptance) |
| `metronomic_cadence` | inter-submit gaps become machine-regular: coefficient of variation (stddev/mean) < 5% over ≥ 8 gaps while the mean sits in 200 ms..10 s | cv 0.05; mean floor 200 ms excludes bursts (the per-seat intent rate limiter's domain); mean ceiling 10 s excludes idle stretches | seat per match |

Plus one counter that existed as a telemetry event since M1 and is now also a
metric: `intent_rate_limited_total` (websocket connections closed by the
per-seat rate limit).

Deliberately NOT a signal: anything derived from client timestamps, client
hashes, animation state or touch trajectories (never authoritative per
AGENTS.md); server-side processing time (`wordarena_intent_process_us`
already measures it and it reflects the server, not the player).

## Where signals surface

- `/metrics` (JSON): `behavior_metronomic_events`,
  `behavior_rejection_streak_events`, `intent_rate_limited_total`,
  `behavior_max_rejection_streak` (running max, process lifetime).
- `/metrics/prometheus`: `wordarena_behavior_metronomic_events_total`,
  `wordarena_behavior_rejection_streak_events_total`,
  `wordarena_intent_rate_limited_total`,
  `wordarena_behavior_max_rejection_streak`.
- JSONL telemetry: `behavior_signal` events carrying `signal`, `match_id`,
  `seat`, `user_id` — the per-seat context lives in the event stream on
  purpose, so /metrics stays fixed-cardinality (no per-seat metric labels:
  a 60-seat Royale must not widen the scrape).

Counters are cumulative for the process lifetime and are not reset at match
end; per-seat state is deleted with the rate-limit windows when the room
closes (`clearSeatWindows` path), so a long-lived server does not accumulate
seat state.

## Threshold rationale

- 20 consecutive rejections: no human-plausible reading of pure failure at
  that depth — and because an accepted word resets the streak, a lucky word
  re-arms rather than accumulates, so the episode must be genuine.
- cv < 5% over ≥ 8 gaps: human input jitters by tens of percent over that
  window; clock-regular submission is machine behaviour by definition.
- 200 ms mean floor: faster bursts are already governed by the per-seat
  intent limiter and surface as rate-limit closes; double-reporting a
  metronome there would conflate a transport guard with a behavior signal.
- 10 s mean ceiling: gaps that wide are idle stretches, not a cadence.

All four constants are fixed in code (like the intent histogram's fixed
buckets) so two scrapes days apart are comparable; retuning them is a code
change with a test update, not a config knob.

## Known limits (honest)

- A disciplined cheat that jitters its cadence and stays under the streak
  threshold is invisible here — by design these are cheap, deterministic,
  zero-risk signals, not a detection ceiling. Richer signals (word-length vs
  submit-timing impossibilities, dictionary-oracle probing patterns,
  cross-match aggregation per user) are the natural next batches and reuse
  this file's boundary and export paths.
- The gap ring may be read mid-write by a scrape (single-writer ring, atomic
  slots): a scrape can see a slightly mixed window. Nothing consumes these
  values authoritatively, so the race is benign by construction.
- Signals are per seat per match; correlating seats across matches into a
  user-level reputation needs the identity layer to mature (M2 batch 32G
  owner tokens are the seed) and is future work.

## Validation

`go test ./cmd/game/` carries the behavioral suite
(`behaviorsignal_test.go`): streak edge semantics (once per episode, re-arm,
`MATCH_NOT_ACTIVE` exclusion, running max), metronomic firing exactly once
per seat, human jitter never flagging, burst and idle cadences excluded,
state clearing with process-lifetime counters surviving.
