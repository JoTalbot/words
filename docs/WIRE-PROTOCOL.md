# M0 Wire Protocol — transport behavior (v1)

Schemas: `proto/wordarena/v1/match.proto` (authoritative). Deterministic
game rules: `docs/M0-MATCH-RULES.md`. This document describes how the
transport behaves in the M0 dev service (`cmd/game`).

## 1. Match creation

```
POST /v1/matches
Content-Type: application/json

{ "language": "en" | "ru" | "uk", "seed": 1512 }   // seed optional
```

`201 Created`:

```json
{
  "match_id": 1,
  "seed": 1512,
  "language": "en",
  "tokens": ["<seat0 token>", "<seat1 token>"],
  "user_ids": [1, 2]
}
```

A fixed `seed` makes the whole match reproducible (same board sequence,
same outcomes) and is how two remote clients can play identical matches.
Random seeds are generated when absent. Tokens authorize the WebSocket
seats. `user_ids` are the account ids reported in snapshots and events.

## 2. Live play

```
GET /v1/match/ws?match_id=<id>&token=<seat token>
```

Upgrade to WebSocket. All frames are **binary protobuf**:

- Client → server: `ClientEnvelope` (`submit_word` for M0).
- Server → client:
  - immediately on connect: one `MatchStateSnapshot` (canonical anchor);
  - `WordValidatedEvent` after every evaluated intent (accepted or not);
  - a `MatchStateSnapshot` once per second (every 30 ticks at 30 Hz);
  - when the match finishes: exactly one terminal `MatchStateSnapshot` with
    `over=true` (final scores included), so clients can deterministically
    stop play and render the result; the room is removed ~3 s later.

### Word submission

`SubmitWordIntent`:

- `letter_indices` — ordered board cell ids spelling the word
  (`indices[i]` letter concatenation must equal a dictionary word);
- `client_sequence` — client monotonic counter; the server **echoes it**
  back in `WordValidatedEvent.client_sequence` so intents and outcomes can
  be correlated; it never orders competitive state;
- `client_timestamp_ms` — telemetry only, never authoritative;
- `touch_path` — reserved for anti-cheat telemetry, not used by M0 rules.

### Outcomes

`WordResult` semantics come from `docs/M0-MATCH-RULES.md` §3/§4:
`ACCEPTED`, `REJECTED_NOT_IN_DICT`, `BLOCKED_BY_RULE`, `INVALID_INPUT`,
`MATCH_NOT_ACTIVE`.

`state_version` increments on every state mutation; clients discard local
predictions older than the latest snapshot/event version and reconcile to
canonical state (M0 acceptance 3).

## 3. Match result

After a match finishes, its final outcome is persisted in-memory (TTL 5 min)
and served as JSON:

```
GET /v1/matches/{id}/result
```

```json
{
  "match_id": 1,
  "seed": 1512,
  "language": "en",
  "over": true,
  "winner_seat": 0,
  "is_tie": false,
  "scores": [42, 21],
  "state_version": 14,
  "server_tick": 210
}
```

`winner_seat` is `-1` when the match is a draw. `404` means the match is
still active, unknown, or its result has expired. Durable (PostgreSQL-backed)
match persistence is M1 work; this endpoint is the M0-adequate persistence
surface and the hook clients/tools use to read final outcomes.

## 4. Resource limits

The dev service enforces two limits, both configurable via environment:

- `WORDARENA_MAX_ROOMS` (default `128`): concurrent live matches. Creating a
  match beyond the cap returns `429 Too Many Requests`.
- `WORDARENA_MAX_WS_BYTES` (default `65536`): maximum WebSocket message size.
  A frame larger than this terminates the connection (oversized-message
  protection). `0` disables the room cap; the read limit is always applied
  when positive.
- `WORDARENA_INTENTS_PER_SEC` (default `60`): per-seat word-intent budget.
  Exceeding it closes the WebSocket with a policy-violation status. This is a
  transport-level abuse guard (event-log flooding); it never influences match
  determinism. `0` disables the limit.

Abandoned state is reaped automatically: matchmaking queue entries expire
after their TTL (2 min) whether or not the client polls, and finished match
results/event logs are dropped after 5 min.

All HTTP handlers are wrapped in request logging (method, path, status,
bytes, duration); WebSocket upgrades are logged on handshake.

## 5. Matchmaking (basic, M1 stub)

Two players are paired FIFO per language via a poll-based queue:

```
POST /v1/queue          { "language": "en" }   -> 202 {"queue_id":"..","status":"waiting"}
GET  /v1/queue/{id}                             -> 200 {"status":"waiting"}
                                                -> 200 {"status":"matched","match_id":..,"seed":..,"token":"..","user_id":..}
                                                -> 410 Gone when expired
```

The second player to enqueue for a language triggers pairing immediately; the
matched entry is delivered exactly once and carries the same join info a
direct `POST /v1/matches` would return. Entries expire after 2 minutes.

## 6. Player profiles

In-memory profile registry (M1 stub; durable storage is a follow-up):

```
POST /v1/players          {"nickname":"alice","language":"en"}  -> 201 Profile
GET  /v1/players/{id}                                           -> 200 Profile
```

`Profile` carries identity plus lifetime stats: `id`, `nickname`, `language`,
`created_at`, `matches_played`, `wins`, `losses`, `draws`, `total_score`.

A match can bind its seats to profiles by passing `player_ids` at creation:

```
POST /v1/matches  {"language":"en","seed":1512,"player_ids":[1,2]}
```

When `player_ids` is present, the seats' `user_id`s equal those profile ids,
and the match outcome is folded into both profiles' stats when the match
ends. Anonymous matches keep synthetic user ids and never touch profile
stats. Both ids must exist and be distinct (`404`/`400` otherwise).

## 7. Replay

```
GET /v1/matches/{id}/replay
```

Returns the finished match's deterministic event log for audit and replay
tooling:

```json
{
  "match_id": 1, "seed": 1512, "language": "en", "over": true,
  "scores": [42, 21],
  "events": [
    {"seq":1,"tick":3,"seat":0,"cell_ids":[9,1,0],"word":"cat",
     "result":"accepted","score_added":5,"total_score":5,
     "is_steal":false,"state_version":2}
  ]
}
```

## 8. Reconnect / resume

- Reconnect = open the WebSocket again with the same token while the match
  is alive. The server immediately sends the canonical snapshot; the client
  reconciles from it (no scene reload needed — M0 acceptance 4).
- A seat may reconnect any number of times; the simulation never pauses for
  a disconnected player. After the match ends subscribers first receive the
  terminal `over=true` snapshot, then the room is removed ~3 s later;
  further connects get 404.
- Ranked/account session semantics (expiry, rotation) are post-M0 work.


### 3.1 Grace window and seat re-entry

- `GraceTicks = 300` (code `server/internal/matchroom/room.go`) = ~10 s at 30 Hz.
- A seat may disconnect and reconnect with the same token any time before
  the match ends; the simulation never pauses and the token remains valid.
- After the match ends, the room is removed ~3 s later; reconnect then
  returns 404.
- Seat substitution / rotation is out of scope for M0; tokens are
  match-scoped and do not carry across matches.

## 9. Server authority notes

- Intents are applied in receive order under the room lock; simultaneous
  intents resolve identically for both clients because the outcome is a
  deterministic function of the applied order (property-tested).
- Clients never compute scores, validity or ownership.

## 10. HTTP snapshot (debug/tooling)

```
GET /v1/match/{id}/snapshot
```

JSON rendering of the canonical snapshot; used by tools and QA.

## 11. Conventions

- Errors: JSON `{"error": "..."}` with proper HTTP status; WS auth failure
  refuses the upgrade (no frame).
- Language tags: `en`, `ru`, `uk` (dictionary snapshot v2, see
  `dictionary/NOTICE.md`).
- Frame payloads are protobuf v3; proto3 scalars default to zero values.
- Field numbers are never reused; breaking changes need a version
  transition and compatibility window (docs/ARCHITECTURE.md).
