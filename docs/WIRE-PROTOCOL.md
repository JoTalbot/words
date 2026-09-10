# M0 Wire Protocol — transport behavior (v1)

Schemas: `proto/wordarena/v1/match.proto` (authoritative). Deterministic
game rules: `docs/M0-MATCH-RULES.md`. This document describes how the
transport behaves in the M0 dev service (`cmd/game`).

## 1. Match creation

```
POST /v1/matches
Content-Type: application/json

{ "language": "en" | "ru" | "uk", "seed": 1512, "sudden_death": true }  // optional fields
```

`201 Created`:

```json
{
  "match_id": 1,
  "seed": 1512,
  "language": "en",
  "sudden_death": true,
  "tokens": ["<seat0 token>", "<seat1 token>"],
  "user_ids": [1, 2]
}
```

A fixed `seed` makes the whole match reproducible (same board sequence,
same outcomes) and is how two remote clients can play identical matches.
Random seeds are generated when absent. Tokens authorize the WebSocket
seats. `user_ids` are the account ids reported in snapshots and events.
`sudden_death` (default `false`) enables the opt-in tiebreak described in
`docs/M1-SUDDEN-DEATH.md`: a tied final score plays one extra wave where the
first accepted word wins. With it off, ties are draws (M0 rules).

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

## 4. Health, readiness and resource limits

```
GET /healthz  -> process liveness, 200 {"status":"ok"}
GET /readyz   -> orchestration readiness
```

`/readyz` returns `200` while the process is ready to accept new work:

```json
{"status":"ready","storage":"memory","active_matches":0}
```

When PostgreSQL durability is enabled, `/readyz` pings the database before
returning ready and reports `503 {"status":"not_ready","storage":"postgres",
"error":"storage_unavailable"}` if storage is unavailable. During graceful
shutdown `Stop()` marks the API as draining: `/readyz` returns
`503 {"status":"draining"}` and new mutating work / WebSocket upgrades are
rejected with `503`.

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


## 5. Metrics and telemetry

Counters are served in two formats:

```
GET /metrics              -> JSON
GET /metrics/prometheus   -> Prometheus text exposition
```

Both include match lifecycle/action counters (`matches_created`,
`matches_finished`, `intents_received`, `words_accepted`, `words_rejected`,
`active_matches`) plus exporter health (`telemetry_events_enqueued`,
`telemetry_events_written`, `telemetry_events_dropped`,
`telemetry_export_errors`).

Optional append-only event export is enabled with
`WORDARENA_TELEMETRY_JSONL=/path/to/events.jsonl`; each line is a versioned
JSON event. The exporter is asynchronous and bounded by
`WORDARENA_TELEMETRY_BUFFER` (default 4096), so analytics backpressure cannot
block authoritative match handling. Seat token values and credentials are not
exported. See `docs/M1-TELEMETRY.md` for the event schema.

## 6. Matchmaking (basic, M1 stub)

Two players are paired FIFO per language via a poll-based queue:

```
POST /v1/queue          { "language": "en", "player_id": 1 }  -> 202 {"queue_id":"..","status":"waiting"}
GET  /v1/queue/{id}                             -> 200 {"status":"waiting"}
                                                -> 200 {"status":"matched","match_id":..,"seed":..,"token":"..","user_id":..}
                                                -> 410 Gone when expired
```

The second player to enqueue for a language triggers pairing immediately; the
matched entry is delivered exactly once and carries the same join info a
direct `POST /v1/matches` would return. Entries expire after 2 minutes.

`player_id` is optional. When supplied it must be a registered profile
(`404` otherwise) and binds that seat to the profile; the resulting match
folds its outcome into the profile's stats on completion. Anonymous seats
keep synthetic user ids. Enqueueing the same profile twice in one language
queue is idempotent: the existing entry is returned.

## 7. Player profiles

Profile registry behind `ProfileRepo` — in-memory by default, Postgres when
`WORDARENA_POSTGRES_DSN` is set (see `docs/M1-PERSISTENCE.md`). Same HTTP
surface either way:

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

## 8. Replay

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

## 9. Reconnect / resume

- Reconnect = open the WebSocket again with the same token while the match
  is alive. The server immediately sends the canonical snapshot; the client
  reconciles from it (no scene reload needed — M0 acceptance 4).
- A seat may reconnect any number of times; the simulation never pauses for
  a disconnected player. After the match ends subscribers first receive the
  terminal `over=true` snapshot, then the room is removed ~3 s later;
  further connects get 404.

### Seat-token rotation (M1)

```
POST /v1/matches/{id}/token/rotate   {"token": "<current seat token>"}
  -> 200 {"match_id":..,"seat":0|1,"user_id":..,"token":"<fresh token>"}
  -> 401 invalid/expired token
  -> 404 match not live
```

Presenting a valid seat token exchanges it for a fresh one; the presented
token stops authenticating immediately (WS dials with it get 401). Rotation
bounds the exposure window of seat credentials. With
`WORDARENA_SEAT_TOKEN_TTL_SECONDS` set (default 0 = no expiry), a token
minted or rotated at time T stops authenticating after T+TTL; rotation
refreshes the deadline.


### 3.1 Grace window and seat re-entry

- `GraceTicks = 300` (code `server/internal/matchroom/room.go`) = ~10 s at 30 Hz.
- A seat may disconnect and reconnect with the same token any time before
  the match ends; the simulation never pauses and the token remains valid.
- After the match ends, the room is removed ~3 s later; reconnect then
  returns 404.
- Seat substitution / rotation is out of scope for M0; tokens are
  match-scoped and do not carry across matches.

## 10. Server authority notes

- Intents are applied in receive order under the room lock; simultaneous
  intents resolve identically for both clients because the outcome is a
  deterministic function of the applied order (property-tested).
- Clients never compute scores, validity or ownership.

## 11. HTTP snapshot (debug/tooling)

```
GET /v1/match/{id}/snapshot
```

JSON rendering of the canonical snapshot; used by tools and QA. The response
includes `"phase"` (`active` | `sudden_death` | `over`) and `"sudden_death"`
(`true` while the tiebreak wave is live), alongside `server_tick`, `wave`,
`state_version`, `cells` and `players`.

**Authorization (added 2026-09-10, batch 21A).** Because match ids are
sequential and this is the only read that exposes live in-progress state, the
endpoint requires a seat credential:

```
Authorization: Bearer <seat token>     # preferred
?token=<seat token>                    # fallback for tools that cannot set headers
```

`401` when no credential is presented, `403` when it is unknown or expired.
The `GET /v1/matches/{id}/result` and `/replay` summaries of *finished*
matches remain unauthenticated by design for now — see
`docs/SECURITY-REVIEW-M1.md` finding S-2 for the disposition and the M2
follow-up that replaces sequential ids with unguessable match codes.

## 12. Conventions

- Errors: JSON `{"error": "..."}` with proper HTTP status; WS auth failure
  refuses the upgrade (no frame). Internal failures are logged server-side and
  reported as a fixed message; wrapped error text is never echoed to clients.
- Request bodies: JSON only, capped at `WORDARENA_MAX_BODY_BYTES` (default
  16 KiB), unknown fields rejected. `POST /v1/matches`, `POST /v1/queue` and
  `POST /v1/players` are additionally limited per caller
  (`WORDARENA_MUTATIONS_PER_MIN`, default 120/min; `0` disables).
- WebSocket handshake: browser `Origin` values must pass the deployment
  allowlist (`WORDARENA_WS_ALLOWED_ORIGINS`, default: loopback/private only);
  requests with no `Origin` header are native clients and always pass.
  `WORDARENA_ALLOW_EXPLICIT_SEED=false` makes a client-supplied `seed` a `400`
  instead of a deterministic match.
- Language tags: `en`, `ru`, `uk` (dictionary snapshot v2, see
  `dictionary/NOTICE.md`).
- Frame payloads are protobuf v3; proto3 scalars default to zero values.
- Field numbers are never reused; breaking changes need a version
  transition and compatibility window (docs/ARCHITECTURE.md).

## The first snapshot a socket receives is not a synchronisation point

Recorded 2026-09-11 from batch 22b, which had to prove this from code before it
could decide whether a measured flake was a server defect or a bad check.

The room ticker starts when the room is created. For a match created through
`POST /v1/matches` that is the request; for a match created by the matchmaker it
is the moment the two seats are paired. Each WebSocket is sent a canonical
snapshot when it connects, not when the room starts.

Consequences that clients and test harnesses must not get wrong:

- Two sockets on the same match can receive first frames with different
  `server_tick`, `state_version` and `remaining_time_ms`. This is normal for a
  30 Hz stream and is not a desync.
- If a wave boundary falls between the two connections, the first frames carry
  different `current_wave` and different `cells`. Also normal.
- What *is* guaranteed, and what a conformance check should assert: the same
  `match_id`; the same board, ownership and scores once both sockets have reached
  the same `current_wave`; identical event streams from then on; and exactly one
  terminal `over=true` snapshot that is identical for both seats.

A client that needs both seats to render the same board at join must wait for its
own first snapshot and key rendering off `current_wave`, not off the assumption
that the peer saw the same tick. Reconciling to the canonical snapshot is already
the required behaviour for prediction rollback, so this adds no new mechanism.

Measurement behind the decision: 147 queue-created matches on 2026-09-10 produced
12 failures (~8%), of which 4 were this clock-skew class and 5 were the offline
planner being unable to cover an arbitrary board. After separating the planner
class and comparing the game rather than the clock, 98 further queue matches
produced 0 divergence failures and 5 planner skips (5.1%).
