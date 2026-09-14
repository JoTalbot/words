# Security & Abuse Review — M1 vertical slice

Owner: security/anti-cheat lane (claimed by session B on 2026-09-10, delivered by
the orchestrator session on 2026-09-10 as batch 21A). Scope: the authoritative match
service (`server/cmd/game`), its transport (`docs/WIRE-PROTOCOL.md`), the client
surfaces that talk to it, and the deployment posture on the OCI dev host.

Method: read every route in `API.Routes()`, enumerate what an unauthenticated caller
can cause, then confirm each claim with an executable test. No finding is listed as
fixed unless a test in this repository fails when the fix is reverted.

## Trust model

| Boundary | Authority | Notes |
|---|---|---|
| Client → server | Server decides score, legality, ordering, match state | `docs/ARCHITECTURE.md`; unchanged by this batch |
| Seat token | Sole gameplay credential (WS + live snapshot) | 128 bits from `crypto/rand` (`randomHex(16)`) |
| Match id | **Unguessable? No** — monotonic counter (`a.counter.Add(1)`) | root cause of S-2 |
| Player profile id | Sequential Postgres `BIGSERIAL` | root cause of S-5 |
| Transport | loopback-only on the dev host | `PRODUCT-DECISIONS.md` Q8; TLS/edge not yet decided |

Anything reachable only from `127.0.0.1` on a dev box is a lower-severity problem
than the same thing behind a public edge. Several dispositions below are explicitly
conditional on that, which is exactly why the Q8 exposure decision must not be made
lightly.

## Findings

### S-1 — Live board readable without a credential (HIGH, fixed)

`GET /v1/match/{id}/snapshot` returned the in-progress board, every cell letter,
ownership, lock timers and both scores for any match id, with no authentication at
all. Match ids are sequential, so a one-line loop reads every live match.

Why it matters beyond privacy: it is a **cheating channel** — a third party can
relay board and lock state to a player, and it defeats the "server is the only
source of competitive truth" property by turning that truth into a public feed.

Fix: the endpoint requires a seat credential, accepted as `Authorization: Bearer`
(preferred, keeps the token out of URL-shaped logs) or `?token=` (tooling fallback).
`401` without a credential, `403` with an unknown/expired one. Rotation is checked:
a rotated-away token no longer reads the board.
Pinned by `TestSnapshotRequiresSeatCredential` and four checks in `infra/smoke.sh`.

### S-2 — Sequential match ids make finished-match data enumerable (MEDIUM, fixed 2026-09-11 by batch 21g)

`GET /v1/matches/{id}/result` and `GET /v1/matches/{id}/replay` are unauthenticated
and enumerable. The payload is the final score, winner, and the full word-by-word
event log of a finished match.

Why this is not simply "gate it too": durable results are a *verified acceptance
criterion* of batch 8 (a finished result must be readable after a restart, and the
seat credentials are process-lifetime state). Gating the read would either break that
criterion or require storing a credential handle with each result — a schema change
that belongs with replacing sequential ids, not bolted onto them.

Disposition: accepted for the loopback dev deployment, **must not** be exposed on a
public edge without the follow-up.

**Fixed 2026-09-11** by `agent/tasks/M1-batch21g-match-codes.yml`. Migration
`003_match_codes.sql` adds `match_code` and `read_capability` to `match_results`;
`POST /v1/matches` and the matchmaking poll response issue both, once; and
`WORDARENA_REQUIRE_READ_CAPABILITY=true` refuses the sequential-id form with 404
and requires the capability on the code form. The batch 8 restart criterion
survives because the capability is stored with the result rather than being
process-lifetime state - which is exactly the schema change this finding said the
gate needed. See docs/WIRE-PROTOCOL.md for the transition window.

Two things this deliberately does not claim. Match ids are still sequential and
still appear in logs, telemetry and the create response; what changed is that
they no longer *authorize* anything. And rows written before migration 003 have
no capability, so a hardened deployment refuses to serve them rather than
inventing a credential on read.

### S-3 — WebSocket accepted any origin (HIGH, fixed)

`websocket.Accept` was called with `OriginPatterns: []string{"*"}`, which disables
coder/websocket's built-in same-origin check outright. A page on any site could open
a socket to the service — and if a token ever leaked (browser history, proxy log,
shared screenshot), it could drive a real match from the attacker's origin.

Fix: one origin decision in one place (`internal/security.OriginPolicy`), evaluated
before the handshake. Requests with no `Origin` header (Unity, headless-bot,
curl-based tooling) always pass; a browser origin must be loopback/private or be
listed in `WORDARENA_WS_ALLOWED_ORIGINS`. `InsecureSkipVerify` is set on the
handshake *because* the policy already decided — a second pattern list maintained
elsewhere is how two origin rules drift apart.
Pinned by `TestWSOriginPolicy` (foreign origin `403`; native and loopback still
connect; an explicitly allowlisted origin connects).

### S-4 — Unbounded request bodies (MEDIUM, fixed)

Every JSON handler decoded `r.Body` with no cap, so a single request could force an
arbitrary allocation before the decoder noticed the shape was wrong. `POST /v1/queue`
and `POST /v1/players` were the cheapest targets.

Fix: shared `decodeJSON` wraps the body in `http.MaxBytesReader`
(`WORDARENA_MAX_BODY_BYTES`, default 16 KiB — the largest legitimate body here is
~120 bytes) and rejects unknown fields, so a typo'd or smuggled key is an error
rather than a silently ignored input.
Pinned by `TestRequestBodyCap` and `TestUnknownFieldRejected`.

### S-5 — Unauthenticated, unlimited mutation of server state (MEDIUM, fixed with a budget)

`POST /v1/matches`, `POST /v1/queue` and `POST /v1/players` each allocated a room, a
queue entry or a durable profile row, with no per-caller bound. The room cap
(`WORDARENA_MAX_ROOMS`, default 128) bounded *concurrency*, not *churn*: an attacker
could keep the service at the cap permanently (denial) and inflate Postgres with
profiles (retention cost). `PRODUCT-DECISIONS.md` Q8 already noted the existing
limits "were sized for a single developer"; that sizing is now explicit.

Fix: `internal/security.Limiter` — fixed window per caller, 120 mutations/minute by
default (`WORDARENA_MUTATIONS_PER_MIN`, `0` disables), bounded key map with reaping
from the existing 30 s reaper so the tracker itself cannot be inflated.
`429` carries `Retry-After`.

Sizing rationale: the exit-gate tooling needs ≤ 30 creating requests per run
(6 queued matches = 12 queue posts + 6 creates), so 120/min gives a ~4× margin for a
developer running QA while still being ~2 orders of magnitude below a flood. Reads
are deliberately **not** limited: an already connected player must never be cut off
because a script hammered `create`, and authenticated seat traffic is bounded by the
per-seat intent limit instead.
Pinned by `TestMutationRateLimit`, `TestLimiterFixedWindow`,
`TestLimiterKeyCapacityIsBounded`.

### S-6 — Client-supplied seed is a fairness hole (MEDIUM, gated)

`POST /v1/matches` accepted a caller-chosen `seed`. Deterministic seeds are the
backbone of replay/E2E tooling, so this cannot simply be deleted — but on a public
deployment whoever pins the seed already knows the whole board before playing.

Fix: `WORDARENA_ALLOW_EXPLICIT_SEED=false` turns an explicit seed into `400`. Default
stays permissive for dev/CI, and the switch is documented as a deployment requirement
rather than an optional tweak. Matchmaking never accepted a client seed, so the queue
path is unaffected.
Pinned by `TestExplicitSeedPolicyGate`.

### S-7 — Free-form nicknames (LOW, fixed)

The only rule was `len ≤ 32` in bytes. A nickname containing `\n` forged log lines
(the service logs `nickname` through telemetry and access paths); markup and
whitespace break the IMGUI client, which renders names inline. Byte length also let a
12-character Cyrillic name be rejected while a 33-byte ASCII name passed.

Fix: one policy function (`security.ValidateNickname`) used by both storage
backends: 1..32 *runes*, letters required, Latin + Cyrillic + digits + `_- .` only,
no control characters or spaces.
Pinned by `TestNicknamePolicy` and `TestValidateNickname`.

### S-8 — Internal error text echoed to clients (LOW, fixed)

`handleCreateMatch` answered `500` with `err.Error()`, which can carry storage or
initialisation detail. Now logged server-side, answered with a fixed string.
Pinned by `TestInternalErrorIsNotEchoed`.

### S-9 — No HTTP listener timeouts (LOW, fixed)

Only `ReadHeaderTimeout` was set. A slow-body client could hold a connection
open indefinitely. `ReadTimeout`/`WriteTimeout`/`IdleTimeout` added; hijacked
WebSocket connections are outside the write deadline by design, so live matches are
not affected.

## Deliberately not changed in this batch

- **Seat token as a query parameter on `/v1/match/ws`.** Native `ClientWebSocket`
  cannot set an `Authorization` header on the upgrade; the standard workarounds are
  `Sec-WebSocket-Protocol` or a short-lived one-time ticket. Both change the wire
  contract, so they need a protocol version transition (docs/ARCHITECTURE.md), not a
  drive-by edit. The access log prints `r.URL.Path` only (verified) so the token does
  not land in the service's own logs; the remaining exposure is any future proxy that
  logs full URLs — which is precisely the Q8 edge decision.
- **`GET /v1/players/{id}`.** Public profile + lifetime stats is the intended shape
  of a leaderboard-style profile; it is enumerable (S-5 limiter now applies to
  creation, reads are bounded by the edge). Revisit with identity/ranking work.
- **Anti-cheat signals.** Word legality, timing and ordering are already server-side;
  multi-signal risk scoring is M3 scope per `docs/ROADMAP.md`.
- **Rate limits as fairness.** Every limit here is abuse protection. None may
  influence a match outcome; a limited request is refused, never reinterpreted.

## Validation

```bash
cd server
go vet ./...
go test -count=1 ./...
go test -race ./cmd/game ./internal/security ./internal/match ./internal/matchroom -count=1
go build -o /tmp/wa ./cmd/game
WORDARENA_ADDR=127.0.0.1:18181 /tmp/wa &
infra/smoke.sh http://127.0.0.1:18181     # 22 passed / 0 failed on 2026-09-10
```

## M2 surface review (batch 34C, 2026-09-14)

Scope: the surfaces M2 added after the review above — the per-profile OWNER
TOKEN and guild endpoints (batch 32G, migration 005), the PvE field on match
creation (batch 32E), the bot-declaration fields (batch 32D) and the
protocol-version handshake on the connect path (batch 33). Method is unchanged:
read every route, enumerate what an unauthenticated or malicious caller can
cause, and confirm each claim against code and tests rather than against the
state file's description of it. No new HIGH or MEDIUM defect was found; the
findings below record what was verified, and the two places where the posture
is deliberately narrower than it looks.

### S-10 — Owner token lifecycle (32G): verified sound

The owner token is the only durable "who is asking" credential the server has,
so its whole lifecycle was re-read rather than sampled:

- **Minting**: `randomHex(16)` = 128 bits from `crypto/rand` (the same
  generator as seat tokens and match codes), minted inside
  `handlePlayerCreate` and returned exactly once in the creation response.
- **At rest**: only `SHA-256(token)` is stored. For a 128-bit random secret a
  fast hash is appropriate (there is no low-entropy space to dictionary
  attack), and a database read cannot be replayed as a player.
- **No overwrite, either backend**: `memProfileStore.SetOwnerToken` refuses a
  profile that already has a hash; the Postgres form is
  `UPDATE ... WHERE owner_token_hash IS NULL` with a `RowsAffected` check. A
  re-mint path (which would make a leaked token indistinguishable from a
  legitimate one) does not exist, matching the Q12 decision that pre-32G
  profiles deliberately cannot use guilds.
- **Lookup**: `players_owner_token_hash_key` is a UNIQUE partial index, so an
  authentication attempt is an index probe, not a seq scan — a flood of bad
  tokens cannot be turned into database CPU amplification. The query carries
  the 5 s statement timeout of every other store call.
- **No oracle**: `authenticatePlayer` answers malformed and unknown tokens
  with the identical 401 ("invalid player token"); the 32-char length
  pre-check rejects junk before any hashing or query work.
- **No leak**: the token never appears in a profile GET (unexported
  `ownerTokenHash` field, never serialized; pinned by the 32G test and
  re-verified LIVE during the batch 34A promotion), in telemetry
  (`profile_created` carries id/language only), or in the request log
  (method/path/status/bytes only — headers are not logged, so
  `X-Player-Token` never lands in the journal).
- **Brute force**: 2^128 space at the per-caller mutation budget (120/min)
  is not a feasible attack; the budget applies to every guild mutation.

### S-11 — Guild endpoints (32G): mutations gated, reads public by decision

All four mutating routes (`POST /v1/guilds`, `POST /v1/guilds/{id}/members`,
`DELETE /v1/guilds/{id}/members/{player_id}`) run `rejectIfDraining` →
`allowMutation` (the S-5 per-caller budget) → `authenticatePlayer` in that
order, and take the acting player ONLY from the token — no route reads a
caller-supplied player id for identity, so impersonation is impossible by
construction rather than by a check. `decodeJSON` (16 KiB cap + unknown-field
rejection) covers every body. Storage invariants are enforced where races
would otherwise live: unique indexes on `guilds.name_key` / `guilds.tag`, and
the one-guild-per-player unique index; the roster cap (50) bounds a guild's
fan-out; dissolution on last-member-leave frees the name, so a dead guild
cannot squat. READS (`GET /v1/guilds/{id}`, `GET /v1/players/{id}/guild`) are
unauthenticated **by product decision** (Q12: rosters are public social data,
and secrecy would make moderation harder). Consequence accepted and recorded:
sequential guild ids are enumerable and pair a public player id with a public
nickname — the same exposure class as the already-accepted public profile read
(S-5 disposition), not a new one.

### S-12 — PvE creation field (32E): gated, bounded, no privileged path

`"pve": true` is refused with a 400 that names `WORDARENA_ALLOW_BOT_SEATS` on
a deployment without it (default OFF), and refused for any roster but 1v1.
Creation flows through the same per-caller mutation budget and `MAX_ROOMS`
cap as every other match, which also bounds the per-create dictionary load
(`pve.New`) an abuser can induce. The opponent runs inside the room's own
ticker goroutine — there is no per-PvE-match goroutine to leak — and the
`a.pve` map entry is deleted on BOTH teardown paths (`discardRoom` and the
ticker's post-match cleanup), verified by reading both. The opponent submits
through `Room.Submit`, the same validated door a human uses; a rejected
intent is telemetry (`pve_intent_rejected`) and never a crash path. Bot
matches are durably `rating_eligible=false` (Q5, migration 004), so PvE
volume cannot pollute ratings.

### S-13 — Protocol handshake (33): authenticated, first-message-only

`ClientHello` is read only AFTER the WebSocket upgrade has passed the seat
token check, the origin policy and `SetReadLimit`, so the handshake adds no
unauthenticated surface. Only the FIRST binary message is interpreted as a
hello; later `hello` payloads fall through to the intent path and are ignored
(`GetSubmitWord() == nil` → continue), so a client cannot re-negotiate
mid-match. `useDelta` starts false and a negotiated client cannot receive a
delta before its own negotiation — the pre-33 unilateral decision is gone.
NOTE for the next protocol version: `ServerHello` always echoes
`serverProtocolVersion` (the "graceful downgrade" is capability-based, not
version-based). That is exact while v1 is the only version; when a v2 exists,
the echo must become `min(client, server)` or a v1 client will be told "v2"
while receiving v1 frames. Recorded here so the debt is found by reading, not
by incident.

### S-14 — Shared mutation budget behind the stage-1 tunnel (informational, pre-existing)

Not new to M2, but the guild and PvE endpoints inherit it: the per-caller
mutation budget keys on the peer address, and every request through the Q8
stage-1 Cloudflare tunnel arrives from the local `cloudflared` process, so ALL
public callers share ONE 120/min budget (`WORDARENA_TRUST_PROXY_HEADERS` is
correctly unset — `cloudflared` does not overwrite `X-Forwarded-For`, and
trusting client-supplied headers would let anyone evade the limiter by
spoofing). One abuser can therefore starve everyone's creation traffic long
before the box itself is stressed (32F measured exactly this ceiling). Per-IP
limiting at a real edge is already a Q8 stage-2 condition
(docs/SECURITY-EXPOSURE.md); nothing in M2 makes it more urgent, but the
shared budget now covers more endpoints.

### Validation (batch 34C)

The claims above were confirmed against the code paths named, plus:
`go test ./cmd/game -run 'Guild|Token|PvE|Bot|Protocol' -count=1` (the 32G
refusal suite, 32E pve suite, 32D bot-policy suite and the batch-33
protocol-version tests), and LIVE re-verification during the batch 34A
promotion: guild create/get over the real deployment with a real owner token,
token absent from the profile read, PvE match created with
`WORDARENA_ALLOW_BOT_SEATS=true`.
