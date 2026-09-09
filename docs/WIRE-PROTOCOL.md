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

## 3. Reconnect / resume

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

## 4. Server authority notes

- Intents are applied in receive order under the room lock; simultaneous
  intents resolve identically for both clients because the outcome is a
  deterministic function of the applied order (property-tested).
- Clients never compute scores, validity or ownership.

## 5. HTTP snapshot (debug/tooling)

```
GET /v1/match/{id}/snapshot
```

JSON rendering of the canonical snapshot; used by tools and QA.

## 6. Conventions

- Errors: JSON `{"error": "..."}` with proper HTTP status; WS auth failure
  refuses the upgrade (no frame).
- Language tags: `en`, `ru`, `uk` (dictionary snapshot v2, see
  `dictionary/NOTICE.md`).
- Frame payloads are protobuf v3; proto3 scalars default to zero values.
- Field numbers are never reused; breaking changes need a version
  transition and compatibility window (docs/ARCHITECTURE.md).
