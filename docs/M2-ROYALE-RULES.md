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

## 5. Lobby formation (batch 31C)

A Royale lobby cannot assume sixty people queue at the same moment. A roster
larger than two therefore starts **short-handed** once the longest-waiting
player has waited `lobbyFillWait` (20 s):

- A full roster still starts **immediately** — the timeout only applies to a
  partial one.
- The wait is measured from the **oldest** waiting entry, not the newest, so a
  trickle of late arrivals cannot postpone the start indefinitely; every
  player's wait is bounded.
- A lobby never starts with fewer than two players, and queues never mix
  languages.
- **1v1 is unaffected**: short-handed starts are disabled for a two-seat
  matchmaker, because a "match" of one is not a match.
- Pairing runs both on enqueue and from the server's periodic sweep, so a
  lobby that simply stops receiving arrivals still starts rather than expiring
  everyone at the queue TTL.

## 6. Reachability (updated by batches 32A and 32B)

- **Snapshot fan-out: measured and closed.** A 60-seat frame repeats the whole
  roster and board, so batch 32A added `MatchStateDelta` - scalars plus only
  what changed - which cuts the stream about 2x, and the same measurement
  showed the absolute cost was never the blocker: a 60-seat client pays
  **0.6-0.9 KB/s**, roughly 50 KB/s for a full lobby
  (`docs/LOAD-BASELINE.md`, `docs/WIRE-PROTOCOL.md` § State deltas). Interest
  scoping would have cut nothing, since every seat needs the whole board and
  the whole scoreboard.
- **`seats` is on the HTTP surface now (batch 32B).** `POST /v1/matches`
  accepts `seats` (`docs/WIRE-PROTOCOL.md` § 1), so the mode is requestable by
  a client rather than only reachable internally through `createRoomN`.
- **The default is still 1v1, on purpose.** `WORDARENA_MAX_SEATS` defaults to
  `2`: a 60-seat match through an unauthenticated endpoint is 60 seat tokens
  and 60 sockets per request, and the current public exposure was reviewed for
  a single-developer deployment (`docs/SECURITY-EXPOSURE.md`). Turning Royale
  on is one environment variable, and the refusal says so.

## 7. What is deliberately NOT decided here

- **Bot backfill.** Whether an under-filled lobby is topped up with bots is
  still open and belongs with the bot disclosure roadmap row (Q5). What *is*
  decided (batch 31C) is the human-only fallback above, and that fallback is
  the default: a lobby starts short-handed rather than waiting forever or
  inventing opponents. No bot-fill policy has been implemented on the strength
  of an assumption.
