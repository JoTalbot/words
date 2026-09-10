# M1 — Durable storage (PostgreSQL)

Status: **accepted (M1)**. Profiles and finished match results become
durable across restarts while the in-memory fast path stays the default for
single-process development.

## 1. Model

- **Profiles** go through `ProfileRepo` (`server/cmd/game/profile.go`):
  `Create`, `Get`, `Record` (stats fold), `Close`.
- **Match results** go through `ResultRepo` (`server/cmd/game/store.go`):
  `Put`, `Get`. The in-memory `results` cache stays the fast path; writes are
  mirrored to `ResultRepo` and cache misses read through, so a restart serves
  results and replays from durable storage.
- Backends: in-memory (default) and PostgreSQL
  (`server/cmd/game/store_pg.go`, driver `github.com/jackc/pgx/v5` via
  `database/sql`, pure Go — works with `CGO_ENABLED=0`).

## 2. Activation

- `WORDARENA_POSTGRES_DSN` (optional). When set, the service connects, pings
  and applies the schema **before listening**; startup fails if Postgres is
  unreachable so durability is never silently dropped. When unset, in-memory
  storage is used (tests, single-node dev).
- Example:
  `postgres://wordarena:wordarena@127.0.0.1:5432/wordarena?sslmode=disable`

## 3. Schema (created idempotently on connect)

- `players(id BIGSERIAL PK, nickname, language, created_at, matches_played,
  wins, losses, draws, total_score)` — stats increment with SQL `UPDATE` so
  concurrent matches fold safely.
- `match_results(match_id BIGINT PK, seed, language, over, winner_seat,
  is_tie, score0, score1, state_version, server_tick, events JSONB,
  recorded_at)` — `events` holds the deterministic replay log; duplicate
  `match_id` inserts are `ON CONFLICT DO NOTHING`.

## 4. Semantics preserved

- Unknown profile ids (synthetic seats) are no-ops in both backends.
- Idempotency: `ResultRepo.Put` is upsert-safe; profile stats are additive
  increments, not absolute writes.

## 5. Verification

`store_pg_test.go` runs only when `WORDARENA_TEST_POSTGRES_DSN` is set:
CRUD/stats, result round-trip, and a full live-match durability test that
"restarts" the service (second instance over the same DSN) and reads profiles
and replay back.

## Match-id resumption across restarts (added 2026-09-10, batch 22A)

Match ids come from a per-process counter, while `match_results` is keyed by
`match_id` and outlives the process. Left alone, a restart re-issued ids that
already had durable rows, and three things followed:

1. `Put` uses `ON CONFLICT (match_id) DO NOTHING`, so the *new* match's real
   outcome was silently discarded;
2. `GET /v1/matches/{id}/result` and `/replay` answered from the aliased
   **previous** match — one match's data served as another match's;
3. `infra/smoke.sh` caught it as "replay is 404 while the match is active"
   returning 200 against the live systemd service (it had never appeared in CI,
   where every run starts with an empty database).

The service now calls `primeMatchIDs()` during Postgres startup: it reads
`SELECT COALESCE(MAX(match_id), 0) FROM match_results` and continues the
sequence from there, failing fast if that read fails — starting at 1 anyway
would be a silent data-integrity failure. In-memory deployments skip it (there
is nothing to alias).

`TestAliasingReproducesWithoutPriming` pins the bug and
`TestMatchIDsDoNotAliasAcrossRestarts` pins the fix; both are in `go test
./cmd/game`.

Follow-up this exposes but does not fix: ids are still *sequential*, which is
what makes finished matches enumerable at all. That is finding S-2 in
docs/SECURITY-REVIEW-M1.md and task `M1-batch21g-match-codes` (unguessable
match codes), not a persistence concern.
