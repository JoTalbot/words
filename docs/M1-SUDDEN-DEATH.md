# M1 — Sudden Death (opt-in tiebreak)

Status: **accepted (M1)**. Supersedes the M0 "out of scope" note for Sudden
Death in `docs/M0-MATCH-RULES.md` §9 only when explicitly enabled. M0 rules
remain the default: nothing in this document changes behaviour unless the
match is created with `sudden_death = true`.

## 1. Motivation

M0 ends a tied match as a draw (M0-MATCH-RULES §7). Ranked/competitive play
needs a deterministic way to break ties that keeps the same authority model:
server-side, deterministic, replayable.

## 2. Trigger

- Flag: `sudden_death` on match creation (`POST /v1/matches` body, default
  `false`; plumbed through `match.Config` / `matchroom.Config`).
- When the match would otherwise end after the final M0 wave with **equal
  scores** — either by wave timeout or by the board becoming fully owned —
  the match enters a Sudden Death wave instead of ending.

## 3. Sudden Death wave

- Wave index: `WavesPerMatch` (3). The board is generated deterministically
  as `board(S, 3)`, exactly like any other wave (`generateWave`).
- Time budget: full `WaveTicks` (60 s), same early-exit none.
- The **first accepted word ends the match immediately**. Its points are
  applied normally, so the scorer is ahead and wins.
- If the wave times out with no accepted word, the match ends as a **draw**
  (bounded: a single tiebreak wave; no chain of tiebreak waves).

## 4. Determinism & replay

- No wall clock or randomness enters the tiebreak: the wave-3 board is
  `board(S, 3)` and every intent still appends to the event log.
- Replaying the event log through the deterministic simulation reaches the
  identical final state (covered by `TestSuddenDeathReplayProperty`).
- `Snapshot.Phase` gains `"sudden_death"`; `Snapshot.SuddenDeath` reports the
  tiebreak wave. `over` stays `false` until the tiebreak resolves.
- `StateVersion` increments on the wave-3 transition and on match end.

## 5. Combo note

Combo (M0-MATCH-RULES §4) already applies to every accepted word, including
the tiebreak word; no change. The M1 roadmap's "combo" client work is
presentation only — the scoring engine is the M0 golden implementation.

## 6. Scope

- Opt-in only. M0 default (draw on tie) is unchanged and covered by
  `TestSuddenDeathDisabledTieIsDraw`.
- Not implemented: elimination, multiple tiebreak waves, per-mode tuning.
