# M2 Royale rules (design note)

Status: implemented in M2 batch 31A (2026-09-14). This is the design note the
repository rules require for a gameplay change; the decisions themselves are
recorded as Q9 and Q10 in `docs/PRODUCT-DECISIONS.md`.

Royale is **not a second simulation**. It is the same deterministic match
(`docs/M0-MATCH-RULES.md`) with a roster larger than two. Everything below
reduces exactly to the M0 rules when `seats == 2`, which is asserted by tests
rather than assumed.

## 1. Roster

`Config.Seats` picks the roster, bounded by `MinSeats` (2) and `MaxSeats` (60).
Zero means two, so every pre-existing caller is unchanged.

## 2. Board (Q10)

| Roster | Cells | Grid |
|---|---|---|
| 2 (1v1) | 12 | 4 x 3 — the unchanged M0 board |
| 3..29 | `seats * 2` rounded up to a multiple of 6 | 6 wide |
| 30..60 | capped at 60 | 6 x 10 |

- `match.BoardCells(seats)` and `match.BoardColumns(seats)` are the contract.
- The letter at cell *i* depends only on `(seed, language, wave)` — **never**
  on the roster. A larger board is a prefix-compatible extension of a smaller
  one, so the 1v1 board is bit-identical to M0/M1 and a client can render
  before the lobby fills.
- Two cells per player keeps the board contested: one cell per player would
  mean a single claim ends the wave. At the 60-seat cap it tightens to one
  cell per player, which is the intended Royale pressure.
- `MaxPathCells` stays 12. A bigger board does not mean longer words.

## 3. Elimination (Q9)

At every wave boundary the lowest scorers are culled until two thirds of the
still-active seats remain, never below `MinSurvivors` (2):

```
60 -> 40 -> 26     (three waves, a full lobby)
```

- **Rosters below `EliminationMinSeats` (4) never cull.** 1v1 and small lobbies
  behave exactly as in M0/M1.
- The cut is by **score descending, ties by seat index ascending**, computed
  only from state already in the log.
- **A tie across the cut line keeps everyone tied with the last survivor.**
  Being knocked out of a Royale because of your seat number would be
  indefensible; the roster shrinks more slowly instead.
- An eliminated seat is a **spectator**: its intents are still appended to the
  event log (so the replay is complete) but are rejected with
  `blocked_by_rule` and cannot change competitive state.
- `PlayerState.EliminatedAtWave` records *when*, and it is folded into
  `Match.Fingerprint`, so a replay that culled a different set cannot compare
  equal.

## 4. Competitive interactions at roster scale

Claim, lock and cross-steal are unchanged in meaning, but "the opponent" is
now **any seat other than the actor** rather than `Seat(1 - seat)`. A steal
debits the actual victim, whichever seat owned the cell. Re-using your own
unlocked cells never scores twice.

## 5. What is deliberately NOT decided here

- **Snapshot fan-out.** A 60-seat match sends every subscriber a snapshot
  containing 60 players and up to 60 cells; that is the dominant cost
  (`docs/LOAD-BASELINE.md`, batch 30F), and delta or interest-scoped snapshots
  are the next Royale work item. The simulation itself is not the constraint.
- **Lobby formation for a large roster.** The matchmaker can form an N-player
  lobby (`newMatchmakerWithSeats`), but nothing decides how long an
  incomplete Royale lobby waits or whether bots backfill it — bot disclosure
  is its own roadmap row.
- **No HTTP endpoint exposes a `seats` knob yet.** Royale is reachable
  internally via `createRoomN`; the public surface stays 1v1 until the
  fan-out work lands.
