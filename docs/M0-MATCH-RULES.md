# M0 Match Rules — Deterministic 1v1 Prototype Specification

Status: **prototype v0.1**. All numeric values below are engineering defaults
adopted so M0 can ship a deterministic core. They are explicitly provisional:
calibration happens after playtesting and simulation (see
`docs/PRODUCT-DECISIONS.md` Q1/Q2/Q6). Every change to this document requires a
design note and a deterministic test update.

## 1. Scope and authority

- The **match server** owns all competitive truth: board generation, word
  legality, cell ownership, locks, steals, scoring, timing and outcome.
- Clients send intents; the server orders them by receive sequence and assigns
  them to ticks. Client timestamps are telemetry only.
- Everything a client needs to reconcile is contained in server snapshots and
  word-validated events.

## 2. Match structure

- A match is 1v1, single language `language ∈ {en, ru, uk}`.
- A match is a sequence of **waves** (`current_wave`, 0-based, M0: 3 waves).
- Each wave presents a **shared board of 12 cells**. Both players see the same
  cells and the same letter pool.
- The board for wave `w` of match seed `S` is generated deterministically:
  `board(S, w)` (see §5). No wall-clock randomness is ever used.

## 3. Cells, ownership, lock, steal

- Cell states: `FREE`, `OWNED_LOCKED`, `OWNED_UNLOCKED`. A locked cell has a
  remaining lock duration counted in ticks.
- **Claim**: a successful word causes every `FREE` cell of the word to become
  `OWNED_LOCKED` with `lock_duration = 90 ticks` (3 s at 30 Hz) for the scoring
  player.
- Cells that the player already owns and reuses in a word do not change state.
- **Lock**: while a cell is `OWNED_LOCKED` it cannot be stolen.
- **Cross-steal**: after the lock expires (`OWNED_UNLOCKED`), an opponent may
  include that cell in a word. On success the cell changes owner to the
  attacking player and becomes `OWNED_LOCKED` again (90 ticks). Points the
  previous owner scored for that cell are debited from the previous owner and
  credited to the new owner. Steal events set `is_steal = true`.
- Word validity constraints:
  1. the letter sequence spells a word in the match dictionary (§6);
  2. word length ≥ 3;
  3. the word contains ≥ 1 `FREE` cell **or** ≥ 1 opponent `UNLOCKED` cell
     (pure re-use of own cells is rejected as `BLOCKED_BY_RULE`); a word may
     mix `FREE`, own and stealable opponent cells;
  4. every opponent cell in the word must be `OWNED_UNLOCKED`
     (otherwise `BLOCKED_BY_RULE`);
  5. cell indices are distinct.

## 4. Scoring and combo

Per-language cell letter values are data (snapshot v1), not match-rule code.

- Word base score: `sum(letter_value(cell))` over word cells (fresh claims and
  steals count each participating cell; re-used own cells contribute `0`).
- Length bonus: `+5` for length ≥ 5, `+12` for length ≥ 7.
- Combo: every accepted word increments the player combo (starting at 1).
  Combo resets to 1 when the player's previous accepted word is older than
  300 ticks (10 s). Multiplier `m = 1 + 0.25 × min(combo − 1, 4)`, so
  `m ∈ [1.0, 2.0]`; capped at combo 5.
- Word points: `floor(base × m)`. Steal additionally transfers the stolen
  cells' previously scored value from victim to attacker, per §3.
- Score is an integer; no floating point is used in stored scores.

## 5. Deterministic board generation

- PRNG: splitmix64 seeding + xoshiro256** stream, implemented in
  `server/internal/prng`, versioned and golden-tested.
- Letter pools are per language with static per-letter weights (data v1,
  rough frequency weights adequate for prototyping; replacement weights are a
  balancing task, not a correctness task).
- For wave `w`, draw 12 letters **without replacement** from the weighted pool,
  using `rng = prng(S, language, w)`; the pool is large enough that depletion
  is impossible in M0 (weighted alphabet sampling with per-letter counts).
- Cell placement in the 4×3 grid is fixed by index; the "matrix shape" is a
  client presentation concern for M0.

## 6. Dictionary

- Every match pins a dictionary snapshot: `(language, version)`.
- Validation is in-process and deterministic (no network in the match path).
- Snapshot v1 for M0 are **fixture dictionaries** shipped with the repo
  (small but real word lists). Full-size dictionaries are pipeline work, not a
  match-logic dependency.
- Normalization (M0): NFC; lowercase; `ё → е` for ru; alphabet membership per
  language. All normalization is deterministic and unit-tested per language.

## 7. Timing model

- Simulation tick: fixed 33.333 ms logical step (30 Hz). All durations are
  expressed in ticks; no wall clock enters match logic.
- Wave duration: 1800 ticks (60 s). Wave ends early when all 12 cells are
  owned. Match duration: 3 waves.
- After the final wave the player with the higher total score wins. Ties are
  recorded as draws.
- Grace period for reconnect/resume: 300 ticks (10 s) of player disconnection.

## 8. Events, snapshots, replay

- Every accepted/rejected word produces a `WordValidatedEvent` with the
  monotonic `state_version` incremented on every state mutation.
- `MatchStateSnapshot` reflects the canonical state at a tick.
- An **event log** of (tick, player, intent, result) is sufficient to replay
  the match: replaying the log through the deterministic simulation yields the
  identical final state and score. Property test in §M0-7.

## 9. Out of scope for M0

Elimination, Sudden Death, ranked MMR, bots, economy, guilds. `is_eliminated`
remains in the wire schema for forward compatibility and is always false in M0.

## 10. Acceptance mapping (docs/M0.md)

| M0 acceptance | Covered by |
|---|---|
| 1. same deterministic board seed to both clients | §5, board test fixtures |
| 2. simultaneous claims resolve identically | ordering test, §3 |
| 3. rejected/lost prediction converges via snapshot | snapshot test |
| 4. resume inside grace period | session task (M0 batch 2) |
| 5. same event log → same final score | replay property test |
| 6. validator identical across platforms | dictionary table tests |
| 7. tests under 50/100/150 ms RTT, 0/1/3% loss | network sim task (M0 batch 2) |
| 8. load baseline (actions/s, CPU/RAM per match) | load task (M0 batch 2) |
