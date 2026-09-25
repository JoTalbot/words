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
| `word_probe` (41A) | the SAME word string is rejected again and again in one match | 5 identical rejections of one word; only the three rejection classes advance the counter; a different word is its own episode | (seat, word) episode per match |
| `flash_path` (41A) | a path of ≥ 4 cells arrives back-to-back within 400 ms of the seat's previous submit | cells ≥ 4 AND gap < 400 ms; short paths and slower multi-cell submits are excluded | seat per match |
| `multi_signal` (42A) | two or more DISTINCT signal families have fired for the same seat in one match | the join fires when the seat's family set reaches arity 2; re-firing a family never advances it; rate-limited closes are a transport guard and are not a family | seat per match |

Plus one counter that existed as a telemetry event since M1 and is now also a
metric: `intent_rate_limited_total` (websocket connections closed by the
per-seat rate limit).

Deliberately NOT a signal: anything derived from client timestamps, client
hashes, animation state or touch trajectories (never authoritative per
AGENTS.md); server-side processing time (`wordarena_intent_process_us`
already measures it and it reflects the server, not the player).

## Where signals surface

- `/metrics` (JSON): `behavior_metronomic_events`,
  `behavior_rejection_streak_events`, `behavior_word_probe_events`,
  `behavior_flash_path_events`, `behavior_multi_signal_events`,
  `behavior_closed_with_signals`, `behavior_closed_flagged_seats`,
  `behavior_closed_family_seats`, `intent_rate_limited_total`,
  `behavior_max_rejection_streak` (running max, process lifetime),
  `streak_max_per_match` (per-match maximum streak histogram, 45A),
  `probe_max_per_match` (per-match maximum probe-depth histogram, 46A),
  `families_max_per_match` (per-match maximum family-count histogram, 46A).
- `/metrics/prometheus`: `wordarena_behavior_metronomic_events_total`,
  `wordarena_behavior_rejection_streak_events_total`,
  `wordarena_behavior_word_probe_events_total`,
  `wordarena_behavior_flash_path_events_total`,
  `wordarena_behavior_multi_signal_events_total`,
  `wordarena_behavior_closed_with_signals_total`,
  `wordarena_behavior_closed_flagged_seats_total`,
  `wordarena_behavior_closed_family_seats_total`,
  `wordarena_behavior_streak_max_per_match` (histogram family:
  `_bucket`/`_sum`/`_count`, 45A),
  `wordarena_behavior_probe_max_per_match` (histogram family, 46A),
  `wordarena_behavior_families_max_per_match` (histogram family, 46A),
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

## Longitudinal aggregates (42B)

The event-time counters above fire at the INSTANT a signal fires; they never
answer "how many matches actually ended with a flagged seat", because that
only becomes knowable when the room closes. `clear` therefore scans the
final per-seat family mask before dropping it:

- `behavior_closed_with_signals` — matched closed with at least one flagged
  seat (the per-match denominator);
- `behavior_closed_flagged_seats` — flagged seats summed over closed matches;
- `behavior_closed_family_seats` — (seat, family) incidences summed over
  closed matches (a seat with two families counts twice).

The scan reads the FINAL family mask, not the event counters: a seat that
fires one family twice closes with one incidence. These aggregates are what
future per-signal longitudinal deltas build on (per-match maxima alongside
the process-lifetime maxima), and they stay measurement-only like everything
else in this file.

## Per-match maximum streak (45A)

The first of those longitudinal deltas, the one 42B already named: the
`behavior_max_rejection_streak` gauge is a process-lifetime fact ("deepest
streak this process ever saw"), which answers the worst case but not the
distribution. A policy day that wants to calibrate the `rejectionStreakSignalAt`
threshold must know what depth of streak matches actually exhibit: how many
closed matches never came close to the threshold, how many reached it, and
how the mass is distributed in between.

At room close `clear` now records, into a fixed-bucket histogram
(`streak_max_per_match`, measurement only):

- the **per-match maximum** consecutive-rejection streak — the deepest streak
  any seat reached in that match, not its final streak (an accepted word
  resets the running streak, so the final value understates the worst run);
- only for matches with a **non-zero** maximum — a clean match, like a clean
  match in 42B, stays quiet and does not inflate the count.

Buckets are fixed at `2, 4, 8, 12, 16, 20, 40` (exclusive upper, cumulative):
the human band (le_2..le_16), `le_20` (matches whose deepest streak stayed
**below** the signal threshold — i.e. where no seat fired the streak signal),
and `le_40` + overflow (matches that exhibited signal-grade streak depth,
exactly `rejectionStreakSignalAt`=20 and up). `_count` is the number of closed
matches with at least one rejection, and `_sum/_count` is the average
per-match maximum streak depth. Two scrapes days apart stay comparable
because the bounds are fixed, exactly like the other histograms.

## Per-match maxima for probe depth and family count (46A)

The same longitudinal-delta boundary, applied to the two other families that
have a natural per-match maximum:

- **`probe_max_per_match`** — the per-match maximum word-probe episode depth
  (the deepest same-word rejection episode any seat reached). Fixed buckets
  `2,3,4,5,6,8,10`: `le_5` counts matches whose deepest episode stayed
  **below** the `wordProbeRepeatAt` (5) signal threshold, and `le_6` up counts
  the matches whose deepest episode reached or exceeded it. This calibrates
  the probe threshold the way `streak_max_per_match` calibrates the streak
  threshold: how many matches come close, how many sit at the line, how many
  blow past it.
- **`families_max_per_match`** — the per-match maximum number of distinct
  signal families a seat fired. Fixed buckets `2,3,4`: `le_2` counts matches
  whose deepest seat fired exactly one family (flagged but never joined),
  `le_3` the matches whose deepest seat reached the `multiSignalArity` join,
  and `le_4` + overflow the matches whose deepest seat fired three or more
  families — the heaviest co-occurrence evidence a policy day could weigh.

Both are recorded only for matches with a non-zero maximum (clean matches
stay quiet), both are bucketed on FIXED bounds so scrapes stay comparable,
and nothing consumes either authoritatively — measurement only, the M3
boundary unchanged.

## Cell-path length measurement (42C)

The "cell-path geometry vs dictionary structure" candidate is the one Known
limit whose naive incarnation cannot fire: an admit-reject on
"longer than the longest word" has no reachable state below the board's
always-admissible size on a 12-cell board, and the 60-cell ceiling admits
even the longest pinned words. A signal that can never fire measures nothing,
so 42C records the DISTRIBUTION the future signal would threshold instead:
four fixed-bucket histograms of the submitted path length (cells), split by
the authoritative outcome class (`accepted`, `rejected_not_in_dict`,
`blocked_by_rule`, `invalid_input`). `MATCH_NOT_ACTIVE` is not recorded - it
is the designed post-over refusal, and its length distribution is not a word
signal. When the owner opens enforcement, a threshold can be calibrated from
these histograms (e.g. the outcome-conditional length bands a human never
produces) rather than declared from intuition.

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
- arity 2 for `multi_signal`: one fired family can be a fluke; two DISTINCT
  families in the same match is the first instant a seat shows more than one
  kind of machine behaviour at once. Families are edge-triggered, so the
  join is cohort evidence (which families co-occur) rather than a new
  heuristic of its own — it is the observable half of the enforcement
  boundary's "weigh multiple signals together" rule, because a policy day
  can only weigh signals it has observed co-occurring. Re-firing a family
  never advances the join.

All thresholds are fixed in code (like the intent histogram's fixed
buckets) so two scrapes days apart are comparable; retuning them is a code
change with a test update, not a config knob.

## Known limits (honest)

- A disciplined cheat that jitters its cadence, varies its words and paces
  its multi-cell submits is invisible here — by design these are cheap,
  deterministic, zero-risk signals, not a detection ceiling. The natural next
  candidates reuse this file's boundary and export paths: cross-match
  aggregation per user (needs the identity layer to mature, see below),
  cell-path geometry vs dictionary structure (measured as distributions by
  42C), and longitudinal deltas of the signals this file already produces
  (started by 45A with the per-match maximum streak histogram; 46A adds the
  per-match maximum probe depth and family count; the remaining families
  follow the same boundary when they need calibrating).
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
word-probe episode semantics (fires at the 5th identical rejection, per
word, immune to accepted/MATCH_NOT_ACTIVE outcomes), flash-path firing once
for fast multi-cell spam while short paths and slow submits stay quiet,
multi-signal joining exactly at the second distinct family (repeats of one
family never advance it, cleared state re-separates the families while the
process-lifetime counter survives), and state clearing with process-lifetime
counters surviving.
