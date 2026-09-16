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

CALIBRATION_RESULTS_PLACEHOLDER

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
| `TestAntiSnowballIsDeterministicAndParticipatesInTheFingerprint` | Same log, same outcome; the rule is visible in the fingerprint. |
