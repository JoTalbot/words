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
