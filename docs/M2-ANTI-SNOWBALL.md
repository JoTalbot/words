# M2 — Anti-snowball: the catch-up rule

Status: implemented as an **opt-in** rule (M2 batch 32C). `Match.AntiSnowball`
is off unless `match.Config.AntiSnowball` is set.

## Why this exists

The competitive loop rewards whoever gets there first. A claim locks its cells,
a lock denies rivals, and consecutive claims compound through the combo
multiplier. At two seats that is a fair duel; at sixty it is the classic
runaway - the leader holds the board and the trailing seats cannot get a word
in, so the second half of the match is decided by the first thirty seconds.

Two documents require this work rather than merely suggesting it:

- The M2 roadmap carries **anti-snowball mechanics** as its own row.
- Product decision **Q1** requires the score formula to define "catch-up
  mechanics" and "hard caps to prevent runaway scores". This is the first half
  of that requirement. Q1's numbers are calibration candidates, which is
  exactly how the constants below are treated.

## The rule

When a seat that is at least `CatchUpGap` points behind the leader scores an
accepted word, it earns a bonus of `points / CatchUpBonusDivisor`, capped at
`CatchUpMaxBonus` points.

| Constant | Value | Why |
|---|---|---|
| `CatchUpGap` | 25 | Expressed in points, not places, so it cannot depend on roster size. It has to be large enough that ordinary score spread does not trigger it, and small enough that a genuinely losing seat is helped. |
| `CatchUpBonusDivisor` | 2 | "Half again". Integer division on purpose: a competitive score must not depend on floating-point behaviour (**PD-003**, deterministic scoring). |
| `CatchUpMaxBonus` | 15 | The hard cap Q1 asks for. A long word on a fresh board is worth far more than a short one; without the cap the mechanic hands the game to whoever is furthest behind the moment a big board opens. |

The comparison is against the highest score among **non-eliminated** seats. An
eliminated seat is a spectator (Q9) whose frozen score must not define how far
behind the living are.

## The properties that make it safe to ship

**It is off by default.** Every existing match, replay, golden test, exit-gate
baseline and device smoke is unaffected: the M0/M1 1v1 numbers do not move
because nothing calls the rule. Enabling it is a deliberate act.

**It is a pure function of canonical, logged state and integer arithmetic.**
The inputs are the scores of living seats at evaluation time. No wall-clock, no
floating point, no client input. Two replays of the same event log produce the
same scores and the same events, and the bonus participates in
`Match.Fingerprint`, so a divergent bonus cannot replay equal.

**It does not re-price steals.** The bonus is added to the acting seat's score.
It is never folded into a cell's `CreditedValue`, because `CreditedValue` is
what a later steal debits: boosting it would silently re-price every steal on
the board - a larger and far less predictable change than the one being made.
`TestCatchUpDoesNotRepriceSteals` runs the same submissions with the rule off
and on and requires the victim's two scores to differ by **exactly** the bonus,
before and after being stolen from.

**It is recorded, not inferred.** `Event.CatchUpBonus` carries the bonus folded
into `ScoreAdded`, so a replay can prove *why* a score differs, not only that it
does. The wire event needs no change: `ScoreAdded` and `TotalScore` already
carry the effect.

## What it does not do

- **It does not touch locks, steals or the board.** The board mechanic that
  causes the runaway is unchanged in this batch. Shortening the leader's locks,
  or making steals cheaper against a leader, are the obvious next levers and
  each is a separate rules change with its own evidence.
- **It does not change the combo multiplier.** Combos are how a leader
  compounds; capping them is a second, distinct decision.
- **It is a per-match option, not a deployment capability.** `POST
  /v1/matches` accepts `anti_snowball: true` (batch 35A); the create response
  echoes it, the seat-authorized state view shows it, and every replay event
  that carries a bonus records it in `catch_up_bonus`. It is not gated by an
  environment variable - it changes fairness, not capability - and the
  matchmaker queue deliberately keeps the default: making it a queue
  attribute is a product decision, not a server detail. The rule's CONSTANTS
  are not request fields either: whatever the calibration settles on is a
  code-level value until playtest evidence says otherwise, and an
  unauthenticated endpoint should not be a tuning panel.
- **Its constants are calibrated in batch 35A, not guessed again.** 25 / 2 /
  15 were engineering defaults, consistent with PD-007's framing of M0 numbers
  as calibration candidates. The simulation sweep (below) measures what the
  rule actually does before the constants move, and that will be a data change
  rather than a redesign.

## Calibration (batch 35A)

PD-007 makes the constants calibration candidates, so the question is not
"should there be a catch-up rule?" but "what does the rule DO, and which of
the three levers does the evidence need?" The harness (`server/cmd/calibrate`)
answers it with simulation rather than playtest - the rule is a pure
function of canonical state, so a driven match is a legitimate measurement
bed until there are players to measure.

**The grid.** 24 fixed seeds × rosters 2 / 8 / 30 / 60 × six constant sets
(`default` 25/2/15, `gap15`, `gap40`, `div3`, `cap10`, `cap20` - one lever
bracketed at a time) = 576 variant matches, each against the rule-OFF
baseline of its (seed, roster) pair (96 baselines). Grid order is (seed,
roster) with sets innermost, and shards own whole pairs, so each baseline is
computed exactly once; `calibrate -shard N -shards M` splits the work and
`-merge` combines the shard files and prints the summary.

**The driver.** Every seat is played by the pve opponent (the same validated
door a human uses, a pure function of snapshot/tick/dictionary/policy) in an
"aggressive" shape (3-6 letter words, 0.5 s between them, steals allowed) so
that a score gap actually opens inside one match - the precondition for the
rule to matter. One seat acts per tick in a ROTATING round-robin: with a
fixed seat order and the policy's shortest-lexicographic-first word choice,
seats 0 and 1 enter a degenerate steal ping-pong over the lexicographic
minimum word of the whole dictionary and the rest of the roster never scores
(2 of 60 seats scoring, measured). Real populations have independent
preferences and independent timing; the rotating order models the second and
removes the first-mover bias. The baseline and every variant use the same
driver, so the comparison is the rule and nothing else.

**What it measures.** For each configuration: how much of the baseline's
final gap (leader over lowest seat) the bonus erases, whether the winner
flips, what the bonus costs in points, and the per-match exposure - the
share of matches where the rule fires at all, which is the other half of the
roadmap row (a rule that fires in 1 of 96 matches is not really on).

**Results** (run 2026-09-16, 4 ARM cores, ~38 min wall for the 60-seat
shard; artifact `/home/ubuntu/artifacts-35a/sweep-full.json` on the OCI
server, regenerated with the merge command above):

Per constant set, averaged over all 96 (seed, roster) pairs:

| set (gap/div/cap) | fired | bonus pts/match | winner flips | final gap off -> on |
|---|---|---|---|---|
| `gap40` (40/2/15) | 72/96 (75%) | 743 | 31 | 77.6 -> 92.2 |
| `div3` (25/3/15) | 93/96 (97%) | 1016 | 44 | 77.6 -> 124.3 |
| `cap10` (25/2/10) | 93/96 (97%) | 1624 | 51 | 77.6 -> 148.1 |
| `default` (25/2/15) | 93/96 (97%) | 1726 | 48 | 77.6 -> 159.7 |
| `cap20` (25/2/20) | 93/96 (97%) | 1748 | 48 | 77.6 -> 162.3 |
| `gap15` (15/2/15) | 96/96 (100%) | 2462 | 48 | 77.6 -> 211.9 |

Averaging rosters hides the shape, which is the finding:

| roster | fired | scoring seats off -> on | top seat unchanged | gap (leader over lowest scorer) off -> on |
|---|---|---|---|---|
| 2 | 21/24 | 2 -> 2 | 21/24 | **20.1 -> 12.8** |
| 8 | 24/24 | 8 -> 8 | 12/24 | 93.9 -> 157.0 |
| 30 | 24/24 | 30 -> 30 | 3/24 | 66.4 -> 205.2 |
| 60 | 24/24 | 35 -> 37 | 12/24 | 127.7 -> 223.0 |

Reading:

- **In the duel the rule does what it was written to do.** At two seats it
  fires in 87.5% of matches, closes the leader-to-trail gap by a third
  (20.1 -> 12.8), and leaves the winner in place 87.5% of the time. That is
  catch-up without a different game.
- **At eight seats and up, the same constants are not catch-up - they are
  inflation with reshuffling.** The 25-point trigger is roster-blind by
  design (points, not places), but the score scale grows with the roster: at
  60 seats the final gap is 127.7 points, so "25 behind the leader" is a
  permanent state for most of the field. The bonus then rides on top of
  nearly every word (97-100% fire), the average match hands out 3840 bonus
  points against a 223-point final spread, and the top seat changes in
  43-88% of matches instead of a trailing seat catching up.
- **The levers, in the order the evidence needs them.** `gap` is the real
  one: `gap40` is the closest behavior to the baseline (92.2 vs 77.6) and the
  fewest flips (31). `div3` roughly halves the bonus volume (1016 vs 1726
  pts) without changing the trigger. `cap` barely matters (148 vs 162): at
  these word lengths points/2 rarely reaches 15, so the hard cap Q1 asks for
  is a safety property, not a tuning knob.

**Decision (provisional, 2026-09-16).** The constants STAY 25 / 2 / 15. The
1v1 evidence says the shipped set is right for the duel the rule was written
for, and no set in the grid does anything better at two seats - so moving the
defaults on the strength of the large-roster data would be trading a
measured-correct behavior for a measured-wrong one. What the evidence DOES
close is the assumption that these constants could simply be applied at sixty
seats: they cannot, without a roster-scaled trigger (for example a gap
expressed against the current leader's score, or per-roster values). That is
a separate rules change with its own design note - the same status the
board-side levers and the combo cap hold - and it is recorded on the ROADMAP
row rather than invented in this batch. Until then, large-roster deployments
that want the rule should know what they are buying: the table above.

## Roster-scaled trigger (batch 35D)

The 35A decision left large rosters with a documented-harmful rule and a
required follow-up: a trigger that scales with the game. The shipped form is
LEADER-RELATIVE, not roster-keyed:

```
eligible when   leader - score >= max(Gap, leader * GapPercent / 100)
```

in integer arithmetic. The leader's score is the scale the fixed 25 was blind
to - it grows with the roster, the language and the wave shape without any
lookup table, and a replay computes it bit for bit. The absolute `Gap` stays
as the floor: at two seats (and in any early game where the leader score is
small) the percentage never binds, which is what preserves the 35A-measured
duel behaviour by construction rather than by exception. `GapPercent = 0` is
the exact legacy trigger (pinned by test at a 1000-point leader); negative
values clamp to zero like every other nonsensical `CatchUpParams` input.

Run (server, 4 shards, artifacts `artifacts-35d/`): 24 seeds x rosters
2/8/30/60 x sets `default,gap40,pct10,pct20,pct30,pct40` = 576 variant
matches + 96 baselines, deterministic, same seed list as 35A - the duel row
reproduces 35A exactly (default fires 87.5%, gap 20.1 -> 12.8, winner stable
21/24), which is the cross-check that the harness changes did not move the
simulation. The shard rebalance (roster-outer grid) worked: runtimes
2562-2648 s, spread 1.03x vs 35A's 2.04x (1124 vs 2289 s). Host loadavg
10.4-13.7 during the run (octopus co-tenant; recorded in `run-meta.json`).

Results (per set x roster, n=24; "gap" is the leader-to-final-second gap,
off -> on; "flip" is a changed winner vs the same-seed baseline):

| set | fire% / flip% @2 | gap @2 | fire% / flip% @8 | gap @8 | fire% / flip% @30 | gap @30 | fire% / flip% @60 | gap @60 |
|-----|------------------|--------|------------------|--------|-------------------|---------|-------------------|---------|
| default | 87.5 / 12.5 | 20.1 -> 12.8 | 100 / 50.0 | 93.9 -> 157.0 | 100 / 87.5 | 66.8 -> 205.3 | 100 / 50.0 | 129.8 -> 263.5 |
| gap40 | 37.5 / 12.5 | 20.1 -> 17.1 | 79.2 / 16.7 | 93.9 -> 105.2 | 87.5 / 62.5 | 66.8 -> 87.7 | 95.8 / 37.5 | 129.8 -> 158.9 |
| pct10 | 87.5 / 12.5 | 20.1 -> 13.8 | 100 / 50.0 | 93.9 -> 156.5 | 100 / 87.5 | 66.8 -> 203.7 | 100 / 50.0 | 129.8 -> 260.6 |
| pct20 | 87.5 / 8.3 | 20.1 -> 14.0 | 100 / 37.5 | 93.9 -> 136.8 | 100 / 91.7 | 66.8 -> 159.2 | 100 / 41.7 | 129.8 -> 214.2 |
| pct30 | 83.3 / 4.2 | 20.1 -> 16.2 | 100 / 45.8 | 93.9 -> 118.7 | 100 / 95.8 | 66.8 -> 125.8 | 100 / 37.5 | 129.8 -> 187.0 |
| pct40 | 79.2 / 4.2 | 20.1 -> 16.7 | 100 / 29.2 | 93.9 -> 106.7 | 100 / 91.7 | 66.8 -> 108.3 | 100 / 37.5 | 129.8 -> 170.9 |

The duel behaves as designed: `pct10`/`pct20` are duel-neutral (the absolute
floor of 25 governs whenever the leader is under 250/125 points, which is the
whole duel), and `pct30`/`pct40` weaken it gently (closing 19.7%/17.2% vs the
legacy 36.6%, with fewer winner flips, 1/24). So the mechanism's floor
semantics are correct in the place the rule is locked.

**At eight seats and up the answer is negative, and it is not close.** No
tested set - absolute (`gap40`) or leader-relative (`pct10`..`pct40`) - closes
the gap at 8+ seats. Not on average and not even per-pair: at 30 and 60 seats
**zero of 24 pairs** end with a smaller leader gap under any set; median
pair deltas are positive everywhere (e.g. `pct40` @8: median +15, IQR
[+5,+24]; @30: +41; @60: +30). The leader-relative trigger does tame the
rule monotonically - `pct40` cuts the damage from -67% to -14% @8, from
-207% to -62% @30, from -103% to -32% @60 - but taming is not catching up.

**Why, and what it points at.** The fire rate stays 100% at 8+ for every
percentage set: with tens of seats, SOMEONE is always >40% behind the leader,
so per-seat selectivity cannot silence the rule - and the aggregate harm
tracks the total bonus volume, not the threshold: default hands out 368/2542/
3840 bonus points per match @8/30/60 and opens the gap by 67/208/103%;
`pct40` cuts the volume to 192/960/2552 and the damage to 14/62/32%; `gap40`
cuts it to 85/381/2497 and 12/31/22%. Whatever threshold picks the seats,
the repeated bonuses pump points into the chasing pack, the pack's new words
then earn bonuses of their own, and the leader keeps scoring on top of a
wider field. The harmful object at Royale scale is the **volume**, which is
exactly the lever the planned combo cap / per-seat bonus budget holds.

**Decision (2026-09-16, closes the 35A follow-up).** Negative result, three
parts:

1. `GapPercent` STAYS in the code as measured infrastructure: default 0 is
   the exact legacy trigger (pinned by test), the floor preserves the
   duel-locked 25/2/15 behaviour, and it is available to a future set with
   evidence. Nothing default changes.
2. NO percentage is provisioned. `anti_snowball` at any seat count keeps
   25/2/15; the flag remains opt-in, and the recommendation for Royale
   sizes (8+) is to leave it OFF - every tested trigger configuration opens
   the gap there. The buyer-beware table is this section.
3. The next anti-snowball lever at scale is VOLUME, not selection: a
   per-seat bonus budget (the combo-cap family already on the ROADMAP row).
   That is now evidence-motivated rather than speculative, and it is a
   separate rules change with its own batch.

## The volume lever: a per-seat bonus budget (batch 36A)

35D closed the selection family and named volume as the next lever: the harm at
8+ seats tracked the total bonus points a match handed out (3840 pts @60 opens
the final gap by 103%), and the fire rate stayed pinned at 100% for every
threshold, because with tens of seats somebody is always far behind. So the
question 36A asks is not *who* is helped but *how much*: `CatchUpParams.BonusBudget`,
the maximum total catch-up bonus one seat may absorb in a match. Zero means no
budget, which is exactly 32C/35A/35D; a budget clamps rather than gates, so the
invariant "a seat's lifetime bonus == min(budget, unbounded)" holds to the point.

Run: 24 seeds x rosters 2/8/30/60 x sets `default,b128,b64,b32,b16,b8` = 576
variant matches + 96 baselines, the 35A/35D grid and seed list unchanged so the
rows stay comparable. Executed on the sandbox host (2 vCPU, x86_64) rather than
the OCI box, and that choice is evidence-backed rather than convenient: the
`default` row reproduced the 35D published numbers **bit for bit** on different
architecture (fire 87.5%, gap 20.1 -> 12.8, flip 12.5%) - which is PD-003
holding across machines, and the cross-check that the harness did not move the
simulation. Shard wall times 1638/1731 s (spread 1.06x, the 35D roster-outer
order still working). Artifacts `artifacts-36a/` on the OCI host and in the
session sandbox; binary `sha256 d4b65cb3...`.

`vol%` is the set's bonus volume as a share of the unlimited run's, at the same
roster - the quantity the lever is meant to control. `closed` counts pairs whose
final leader-to-last gap got **smaller**, out of 24: the only column that says
"catch-up happened".

| set | vol% @2 | gap @2 | closed @2 | vol% @8 | gap @8 | closed @8 | vol% @30 | gap @30 | closed @30 | vol% @60 | gap @60 | closed @60 |
|-----|---------|--------|-----------|---------|--------|-----------|----------|---------|------------|----------|---------|------------|
| default | 100 | 20.1 -> 12.8 | 14 | 100 | 93.9 -> 157.0 | 2 | 100 | 66.8 -> 205.3 | 0 | 100 | 129.8 -> 263.5 | 0 |
| b128 | 75.6 | 20.1 -> 13.8 | 14 | 94.6 | 93.9 -> 150.8 | 2 | 85.1 | 66.8 -> 175.0 | 0 | 84.9 | 129.8 -> 236.2 | 0 |
| b64 | 49.6 | 20.1 -> 15.9 | 10 | 73.3 | 93.9 -> 127.4 | 2 | 52.3 | 66.8 -> 124.4 | 0 | 51.6 | 129.8 -> 186.4 | 0 |
| b32 | 25.8 | 20.1 -> 15.9 | 9 | 48.9 | 93.9 -> 107.5 | 3 | 29.0 | 66.8 -> 93.9 | 0 | 27.3 | 129.8 -> 157.3 | 0 |
| b16 | 13.2 | 20.1 -> 16.3 | 9 | 27.5 | 93.9 -> 98.6 | 2 | 16.0 | 66.8 -> 78.2 | 2 | 13.7 | 129.8 -> 142.5 | 0 |
| b8 | 6.9 | 20.1 -> 17.3 | 9 | 14.1 | 93.9 -> 96.6 | 2 | 8.4 | 66.8 -> 72.6 | 3 | 6.9 | 129.8 -> 135.4 | 0 |

**The lever works mechanically, and that is all it does.** Volume falls with the
budget and the damage falls with the volume, monotonically, at every roster:
@60 the gap inflation runs +103% -> +82% -> +43.5% -> +21.1% -> +9.7% -> +4.3%
as the budget runs 128 -> 8. The safety invariant held on all 576 pairs (no
match's total bonus exceeded roster x budget, zero exceptions), and winner
instability at 60 seats tracks it the same way: flips 50% -> 29% -> 17% -> 17%
-> 12.5% -> 8.3%. A budget therefore genuinely bounds what the rule can do.

**It never buys catch-up at scale; it buys silence.** `closed` is 0/24 at 30 and
60 seats for *every* budget - the same per-pair statement that made 35D's result
negative - and at 8 seats it never exceeds 3/24, which is what cutting the volume
to 14% of the unlimited run should produce: a rule that mostly does not fire
meaningfully. The budget is a mute button, not a tuning knob.

**And the mute is paid for out of the duel, where the rule is locked good.** At
roster 2, closing pairs run 14/24 -> 10 -> 9 -> 9 -> 9 as the budget tightens,
and the gap the rule closes falls 36.6% -> 21.1% -> 19.0% -> 13.9%. `b128` is the
only setting that leaves the duel's per-pair behaviour untouched (14/24 closed,
20.1 -> 13.8, 75.6% of the volume) - and at 60 seats it still opens the gap by
82%. There is no point on this grid that is both duel-preserving and Royale-safe:
the budget's effect at 60 requires a ceiling so low it removes a third of the
duel's benefit.

One number is worth recording without over-reading it: `b16` and `b8` at 30 seats
close 2/24 and 3/24 pairs - the first *non-zero* closing counts any configuration
in the whole 35A/35D/36A program has produced at 8+ seats. At n=24 that is noise,
not a result (a 0/24 -> 3/24 shift is not distinguishable from chance), and it is
listed here so a future pass does not have to rediscover it. If it were real it
would say something specific: that the rule helps at scale only when it is nearly
turned off - which is an argument for the board, not for the bonus.

Saturation, for the record: at 60 seats a budget uses only 42-55% of its own
ceiling on average (mean total 265-3262 against a 480-7680 ceiling). The budget
is clipping the heavy tail, not the typical seat - consistent with 35D's finding
that the pathology is repeated helping of the same chasers.

**Decision (2026-09-17, closes the volume lever).**

1. `BonusBudget` **stays in the code** as measured infrastructure, exactly as
   `GapPercent` did after 35D: default 0 is the legacy unlimited behaviour (pinned
   by test), the clamp is a safety property worth having, and the ledger makes
   "why did this seat stop being helped" answerable from the event log.
2. **Nothing is provisioned.** No budget at the duel (it costs 5 of 14 closing
   pairs and a third of the measured benefit), and no budget at 8+ (it cannot
   close a single pair there at 30/60 seats). The flag stays opt-in and the
   Royale recommendation from 35D is unchanged: leave it OFF.
3. **The point-redistribution family is now closed.** Selection (35A: thresholds
   inflate at 8+), leader-relative selection (35D: still inflate, 0/24 close),
   and volume (36A: bounded inflation, 0/24 close, duel degraded) have each been
   measured with the same grid and the same seeds. A mechanic that pays losing
   seats cannot be tuned into catch-up at Royale size, because at that size "the
   leader" is not a person - it is a rotating seat, and every bonus moves the
   crown instead of closing a distance. The remaining lever on the row is the
   only one that changes who can score rather than who is paid: **board-side**
   (leader lock duration, steal economics), with its own evidence.

## The board: lock duration and steal economics (batch 36D)

36A closed the ledger and left one sentence standing: the board is the only place
a point can *move* instead of being minted. This batch measures that claim. Two
knobs became calibration parameters (`match.BoardParams`, same code-level status
as `CatchUpParams` and the 36A budget, and for the same reason not a request
field):

- **lock duration** - how long a claimed cell is safe from a rival (`LockTicks`,
  shipped 90 ticks = 3 s). It is the leader's protection, so it is the one board
  quantity a trailing seat cannot work around by playing better.
- **steal debit** - what share of a stolen cell's `CreditedValue` is taken back
  from its previous owner (shipped 100%). It is the reason a steal is a transfer
  rather than a gift.

Grid: the 35A/35D/36A grid (24 seeds x rosters 2/8/30/60), one arm per pair, and
the catch-up rule **off in every row including the baseline** - the question was
what the board does on its own, and a run mixing both could not answer it. The
four baseline columns reproduce the published 36A table exactly (20.1 / 93.9 /
66.8 / 129.8) on x86_64 after 35D measured aarch64, which is PD-003 holding again
and, at the far end of the grid, the proof that the new zero value is a no-op.
768 pairs, shard wall times 2061/2172 s, artifacts `artifacts-36d/` (mirrored on
the OCI host), binary recorded in `run-meta.txt`.

`closed` is 36A's column (final leader-to-last gap got smaller, out of 24).
`share` is the leader's points as a fraction of every point scored in the match -
the scale-free reading - and `total pts`/`words` say what happened to the match
while the gap was moving. That last pair is not decoration here: see finding 3.

| roster | arm | gap off -> on | delta | closed | share off -> on | share | total pts | words | flip |
|---|---|---|---|---|---|---|---|---|---|
| 2 | `lock45` | 20.1 -> 21.5 | +6.6% | 0/24 | 57.3% -> 57.2% | -0.2 pp | 208 -> 316 | 143 -> 284 | 4.2% |
| 2 | `lock180` | 20.1 -> 20.1 | +0.0% | 0/24 | 57.3% -> 57.9% | +0.6 pp | 208 -> 154 | 143 -> 72 | 0.0% |
| 2 | `lock300` | 20.1 -> 20.4 | +1.2% | 4/24 | 57.3% -> 58.6% | +1.3 pp | 208 -> 125 | 143 -> 44 | 0.0% |
| 2 | `debit75` | 20.1 -> 13.2 | -34.6% | 22/24 | 57.3% -> 50.9% | -6.4 pp | 208 -> 750 | 143 -> 143 | 4.2% |
| 2 | `debit50` | 20.1 -> 10.7 | -46.8% | 21/24 | 57.3% -> 50.6% | -6.7 pp | 208 -> 932 | 143 -> 143 | 8.3% |
| 2 | `debit25` | 20.1 -> 5.8 | -71.0% | 20/24 | 57.3% -> 50.2% | -7.1 pp | 208 -> 1455 | 143 -> 143 | 29.2% |
| 2 | `debit0` | 20.1 -> 5.5 | -72.7% | 19/24 | 57.3% -> 50.2% | -7.1 pp | 208 -> 1634 | 143 -> 143 | 50.0% |
| 2 | `lock45-debit50` | 20.1 -> 11.4 | -43.5% | 20/24 | 57.3% -> 50.3% | -7.0 pp | 208 -> 1807 | 143 -> 284 | 12.5% |
| 8 | `lock45` | 93.9 -> 143.5 | +52.8% | 7/24 | 30.3% -> 32.9% | +2.6 pp | 324 -> 473 | 246 -> 477 | 95.8% |
| 8 | `lock180` | 93.9 -> 89.8 | -4.4% | 14/24 | 30.3% -> 43.8% | +13.5 pp | 324 -> 202 | 246 -> 124 | 95.8% |
| 8 | `lock300` | 93.9 -> 64.2 | -31.6% | 17/24 | 30.3% -> 41.6% | +11.3 pp | 324 -> 152 | 246 -> 75 | 95.8% |
| 8 | `debit75` | 93.9 -> 245.3 | +161.3% | 0/24 | 30.3% -> 22.8% | -7.5 pp | 324 -> 1244 | 246 -> 246 | 45.8% |
| 8 | `debit50` | 93.9 -> 295.1 | +214.4% | 1/24 | 30.3% -> 22.2% | -8.1 pp | 324 -> 1537 | 246 -> 246 | 58.3% |
| 8 | `debit25` | 93.9 -> 436.9 | +365.4% | 1/24 | 30.3% -> 21.9% | -8.4 pp | 324 -> 2381 | 246 -> 246 | 70.8% |
| 8 | `debit0` | 93.9 -> 365.9 | +289.7% | 3/24 | 30.3% -> 19.4% | -10.9 pp | 324 -> 2634 | 246 -> 246 | 87.5% |
| 8 | `lock45-debit50` | 93.9 -> 642.6 | +584.6% | 2/24 | 30.3% -> 25.7% | -4.6 pp | 324 -> 2943 | 246 -> 481 | 100.0% |
| 30 | `lock45` | 66.8 -> 247.5 | +270.8% | 0/24 | 7.1% -> 18.0% | +10.9 pp | 1031 -> 1351 | 942 -> 1837 | 100.0% |
| 30 | `lock180` | 66.8 -> 65.3 | -2.1% | 13/24 | 7.1% -> 8.3% | +1.2 pp | 1031 -> 790 | 942 -> 473 | 100.0% |
| 30 | `lock300` | 66.8 -> 65.8 | -1.5% | 14/24 | 7.1% -> 9.7% | +2.6 pp | 1031 -> 679 | 942 -> 285 | 95.8% |
| 30 | `debit75` | 66.8 -> 245.1 | +267.2% | 0/24 | 7.1% -> 5.9% | -1.1 pp | 1031 -> 4598 | 942 -> 942 | 100.0% |
| 30 | `debit50` | 66.8 -> 314.4 | +371.0% | 0/24 | 7.1% -> 5.9% | -1.1 pp | 1031 -> 5793 | 942 -> 942 | 100.0% |
| 30 | `debit25` | 66.8 -> 501.7 | +651.6% | 0/24 | 7.1% -> 6.1% | -1.0 pp | 1031 -> 8807 | 942 -> 942 | 100.0% |
| 30 | `debit0` | 66.8 -> 579.6 | +768.3% | 0/24 | 7.1% -> 6.2% | -0.9 pp | 1031 -> 9958 | 942 -> 942 | 100.0% |
| 30 | `lock45-debit50` | 66.8 -> 885.2 | +1226.2% | 0/24 | 7.1% -> 8.3% | +1.3 pp | 1031 -> 11365 | 942 -> 1880 | 95.8% |
| 60 | `lock45` | 129.8 -> 138.0 | +6.3% | 8/24 | 12.8% -> 9.8% | -3.1 pp | 998 -> 1415 | 942 -> 1866 | 100.0% |
| 60 | `lock180` | 129.8 -> 65.3 | -49.7% | 20/24 | 12.8% -> 8.2% | -4.7 pp | 998 -> 802 | 942 -> 473 | 100.0% |
| 60 | `lock300` | 129.8 -> 62.6 | -51.8% | 21/24 | 12.8% -> 9.2% | -3.7 pp | 998 -> 678 | 942 -> 285 | 100.0% |
| 60 | `debit75` | 129.8 -> 260.6 | +100.7% | 0/24 | 12.8% -> 5.7% | -7.1 pp | 998 -> 4593 | 942 -> 942 | 4.2% |
| 60 | `debit50` | 129.8 -> 319.0 | +145.7% | 0/24 | 12.8% -> 5.6% | -7.3 pp | 998 -> 5807 | 942 -> 942 | 12.5% |
| 60 | `debit25` | 129.8 -> 444.2 | +242.1% | 0/24 | 12.8% -> 5.0% | -7.9 pp | 998 -> 9039 | 942 -> 942 | 25.0% |
| 60 | `debit0` | 129.8 -> 506.4 | +290.1% | 0/24 | 12.8% -> 5.0% | -7.8 pp | 998 -> 10201 | 942 -> 942 | 100.0% |
| 60 | `lock45-debit50` | 129.8 -> 434.4 | +234.6% | 0/24 | 12.8% -> 4.1% | -8.7 pp | 998 -> 11337 | 942 -> 1880 | 100.0% |

**Four findings.**

1. **The board controls the gap; the ledger does not.** The best ledger arm in
   36A closed 0/24 pairs at 60 seats. `lock300` closes **21/24** there and takes
   the gap from 129.8 to **62.6** (-51.8 percent), with scores *falling*
   (998 -> 678) instead of inflating, and the leader's share falling with them
   (12.8 -> 9.2 percent). It is the first arm in the whole anti-snowball program -
   32C, 35A, 35D, 36A - that materially flattens the distribution at Royale size,
   and it does it without minting a single point.
2. **It buys that with the board's own activity, which is a product-visible
   price, not a rounding error.** At 60 seats the words played per match fall from
   942 to 285; in the duel from 143 to 44. A long lock does not redistribute the
   fight, it ends it: cells stop changing hands, so the roster stops scoring
   against itself. That is a different game mode, not a tuned one, and no
   measurement can decide whether it is the one Word Arena wants.
3. **Lowering the steal debit is a snowball accelerator, and it is the trap in
   this family.** At 8 and 30 seats the gap *explodes* - 93.9 -> 245.3 (+161
   percent) and 66.8 -> 245.1 (+267 percent) at `debit75`, rising to 66.8 ->
   **579.6** (+768 percent) at `debit0`, with `closed` 0/24 in every large-roster
   row. The reason is structural: the debit is what makes stealing cost the
   holder, so removing it turns every contested cell into net new points for
   whoever takes it, and the driver's own comment already describes the failure
   mode this enables (seats ping-ponging the lexicographically smallest word).
   The trap is the scale-free column: at 8 seats `debit0` *improves* leader share
   from 30.3 to 19.4 percent while the distance grows 3.9x, because the minted
   points make every seat enormous. A board lever read without total points in
   view reports the opposite of what happened. The duel is the only place the arm
   looks like catch-up (-34.6 percent, 22/24 at 2 seats) - the same
   size-dependence 35A found in the thresholds, arriving from the other side.
4. **The sign of the lock flips with roster size, so a shipped version could not
   be a constant.** At 8 seats `lock300` cuts the gap (-31.6 percent, 17/24) yet
   *raises* the leader's share by 11.3 points (30.3 -> 41.6 percent); at 60 seats
   both improve; at 30 the gap is flat (-1.5 percent) and the share worsens
   (+2.6 points); at 2 the gap does not move at all (+1.2 percent) while activity
   drops by two thirds. The mechanism is one sentence: **a lock protects whoever
   currently holds cells**, and who that is changes with the size of the pack - at
   Royale size it is mostly trailing seats stealing from each other, in an
   eight-seat ladder it is mostly the leader.

`flip` deserves its own caveat rather than a finding: at 30 and 60 seats *every*
arm flips the winner in about 100 percent of pairs, including `lock180` at 30
seats, which barely moves the gap. At those sizes the board is knife-edge and "the
crown changed" says nothing about catch-up (36A could read a monotone signal in
flip because its arms were one lever at different volumes; here the arms change
what the game is). Where flip is informative it is reassuring: in the duel
`lock180` and `lock300` leave it at **0.0 percent** - a quieter board does not make
the winner less determined, it makes the match less played.

One side finding, recorded because it cost a test failure to notice: the lock
boundary is exact, not inclusive. A cell locked for N ticks is stealable after N
ticks, because the counter is decremented on the tick that follows the claim. The
35A-era test used `LockTicks+1` and so never measured the edge; `boardlevers_test`
now asserts N-1 protected and N released, in both directions.

**Decision.**

1. **No shipped constant changes.** `LockTicks` stays 90 and the steal debits in
   full. The lock result is real but it is a pacing decision about how
   interactive a Royale board should feel, and 24 seeds cannot settle an
   aesthetic question - it can only show that the trade exists, which it now does.
2. **`BoardParams` stays in the tree** as measurement infrastructure, exactly as
   36A kept `BonusBudget`: two lines of rule logic plus normalization, zero cost
   at the default, and a future pacing or steal-economics decision can be swept on
   the existing grid instead of needing a harness again. Nothing is provisioned.
3. **The anti-snowball row is closed on evidence, both halves of it.** The ledger
   cannot close a Royale gap (35A thresholds, 35D relative selection, 36A volume -
   three independent levers, three negatives, one grid). The board can, and its
   price is the interaction that makes the mode worth playing (36D). The shipped
   answer stays what 32C shipped: an opt-in, duel-calibrated rule, OFF for Royale.
   Any reopening has to arrive as a roster-scaled rule measured at n>=96 with
   words-per-match published beside the gap, because a lever that closes the
   distance by ending the fight is not a catch-up win.

## Validation

`server/internal/match/antisnowball_test.go`:

| Test | Property |
|---|---|
| `TestAntiSnowballIsOffByDefault` | A default match awards nothing; the M0 baseline is untouched. |
| `TestCatchUpBonusAppliesWhenFarBehind` | The bonus is half the word's own points, folded into the score and the event. |
| `TestCatchUpBonusNotAwardedInsideTheGap` | Inside the gap nothing is awarded - including to the leader. |
| `TestCatchUpBonusIsCapped` | The cap binds on the rule and through a real submission. |
| `TestCatchUpBonusIgnoresEliminatedLeaders` | A spectator's frozen score does not define the gap. |
| `TestCatchUpDoesNotRepriceSteals` | The steal debit is identical with the rule on and off. |
| `TestCatchUpBudgetZeroIsTheLegacyUnlimitedBehaviour` | An unreachable budget is indistinguishable from none (32C/35A/35D data stays comparable). |
| `TestCatchUpBudgetClampsTheWordThatRunsItOut` | The word that crosses the line earns the remainder, then nothing - a bound, not a cliff. |
| `TestCatchUpBudgetIsPerSeatNotAMatchPool` | Every trailing seat can absorb the whole budget; it is not a shared pool. |
| `TestCatchUpBudgetIsANeverExceededBound` | Lifetime bonus == min(budget, unbounded), exactly, over a sweep of budgets. |
| `TestCatchUpNegativeBudgetAwardsNothing` | A nonsensical negative resolves to "allows nothing", never to "unlimited". |
| `TestCatchUpBudgetReplaysExactly` | The ledger is derived: same driven words, same fingerprint. |
| `TestAntiSnowballIsDeterministicAndParticipatesInTheFingerprint` | Same log, same outcome; the rule is visible in the fingerprint. |
`server/internal/match/boardlevers_test.go` (batch 36D):

| Test | Property |
|---|---|
| `TestBoardZeroValueIsTheShippedRule` | An omitted board is fingerprint-identical to explicit defaults; no fixture, replay or baseline moves. |
| `TestBoardLockTicksShortensLeaderProtection` | The lock boundary is exact - N-1 ticks still protected, N released - at both the shipped and a 1-tick setting, and a fresh claim cannot be recaptured inside its own wave even at `LockTicks=1`. |
| `TestBoardStealDebitIsAFractionOfTheCreditedValue` | The debit is the per-cell credited value times the percent, floored per cell, at 100/75/50/25/none. |
| `TestBoardDebitFractionLeavesTheStealersWordScoreAlone` | The victim's loss is strictly monotone in the percent while the stealer's score is identical: the lever moves a point, it does not reprice a word. |
| `TestBoardParamsAreClamped` | Nonsense normalizes to a playable match instead of erroring; the 20 s ceiling and the 0-100 band hold. |
| `TestDriverIsDeterministic` (36D rows) | An explicit default board reproduces the shipped record byte-for-byte, and removing the debit changes at least one of twelve seeds - a sweep arm cannot be a silent no-op. |

