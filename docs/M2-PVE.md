# M2 — First PvE content (batch 32E)

Status: implemented behind a deployment switch (default off). Register entry: Q5 / Q11.

## What this is

A **practice match**: one player plays a 1v1 against an opponent the server
drives. No second client, no second account, no external tooling — the reason
this is the first PvE content rather than a leaderboard or a tutorial is that
"a player cannot play alone" was the shortest path between a build that works
and a game someone can actually try.

Request it with `POST /v1/matches`:

```json
{"language": "en", "seed": 1512, "pve": true}
```

The response is the ordinary two-seat create response. Seat 0 is the caller,
seat 1 is the server's opponent. `seats` may not be set alongside `pve`: a
practice match is 1v1 by definition, and a mode that quietly accepts a roster
it cannot fill would be a worse contract than a refusal.

## Why it was safe to build now

Product decision **Q5** (docs/M2-BOT-POLICY.md) settled the three questions a
simulated player raises, and this feature inherits all three instead of
re-opening them:

- the opponent is a **declared** bot (`bot_seats` under the hood — nothing is
  inferred from a missing profile or an idle socket);
- it is **disclosed** on every surface a client can observe: the live
  `PlayerState.is_bot` field on the WebSocket stream, `is_bot` in the HTTP state
  view, the delta change set, telemetry, and the durable result
  (`bots` / `bot_present`);
- the match is recorded as **never rating- or reward-eligible**, durably, by
  `infra/migrations/004_match_results_bots.sql`.

So PvE is not a special case bolted onto the disclosure rules; it is the first
feature that the disclosure rules were written to make possible.

## How the opponent works

`server/internal/pve/opponent.go`, driven from the room ticker in
`cmd/game/api.go` (`startPvE`, `tickPvEOpponent`).

Three properties, in order of importance:

1. **Determinism.** The opponent's intent is a pure function of
   `(snapshot, tick, dictionary, policy)`. No wall clock, no RNG, no map
   iteration order: the candidate word list is sorted at load time, and cells
   are considered in id order. Replaying the same seed against the same policy
   reproduces the same match — the same guarantee PD-003 asks of scoring.
2. **No special authority.** The opponent submits through `Room.Submit`, the
   same door a human client uses, so the server validates its intents with the
   same rules and rejects the same mistakes. There is no privileged path inside
   the simulation that could drift from the rules players are held to. A
   rejected intent is logged as telemetry and costs the opponent a word, exactly
   as it would cost a player.
3. **Polite by default.** The shipped policy (`pve.DefaultPolicy`) plays words
   of 3–4 letters, waits 45 ticks (1.5 s) between words, and takes **free cells
   only** — it never steals a cell a player is still holding. The first PvE
   content exists to teach the loop, not to punish. `Policy.AllowSteal` exists
   and is exercised by a test, so a harder opponent is a policy change with
   evidence rather than a rewrite.

Availability follows the game's own rule rather than a private one: a cell is
usable if it is unlocked **and** does not already belong to the opponent's own
seat. This was caught by a test, not by review — an earlier version of the
aggressive policy produced paths made entirely of cells it already owned, which
the simulation correctly rejected as blocked-by-rule.

## Operational notes

- Gated by `WORDARENA_ALLOW_BOT_SEATS` (**default false**), the same switch as
  32D. The refusal names the switch. A deployment without it behaves exactly as
  it did before this batch, and a `pve` request never silently degrades into a
  match against nobody.
- If the dictionary cannot be loaded, or yields no words within the policy's
  length band, the match is **not** created: the room is discarded and the
  caller gets a 500 with a log line. A practice match whose opponent can never
  move is worse than an error.
- The opponent costs one dictionary load and one candidate slice per practice
  match; per-tick work is a scan of the candidate list over at most 60 cells,
  so it is bounded by the board, not by the player count.

## Deliberately not in this batch

- Difficulty selection, bot ratings, or a separate bot pool (Q5 clause 3 makes
  bot matches non-rating-eligible, so a bot pool has no meaning yet).
- Matchmaking against a bot ("play online" falling back to practice) — the
  matchmaker fills human lobbies only, and short-handed starts (31C) remain the
  answer to low population.
- Tutorial framing, hints, or scoring feedback aimed at a new player.
