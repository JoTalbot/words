-- Baseline schema for the durable Postgres store (M1).
--
-- This is the versioned copy of the schema that the match service also applies
-- idempotently on connect (const pgSchema in server/cmd/game/store_pg.go).
-- Keeping the two in lockstep is enforced by a drift test in
-- server/cmd/migrate/main_test.go, so a column added in one place and
-- forgotten in the other fails the build rather than production.
--
-- Rules for files in this directory:
--   * name is NNN_snake_case.sql with a zero-padded, gap-free version;
--   * a migration is applied at most once, inside a single transaction;
--   * never edit an applied migration — add the next version instead;
--   * statements must be idempotent where practical so a re-run after a
--     partial failure is safe (CREATE TABLE IF NOT EXISTS, ADD COLUMN IF NOT
--     EXISTS via a DO block, CREATE INDEX IF NOT EXISTS).

CREATE TABLE IF NOT EXISTS players (
    id             BIGSERIAL PRIMARY KEY,
    nickname       TEXT        NOT NULL,
    language       TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    matches_played BIGINT      NOT NULL DEFAULT 0,
    wins           BIGINT      NOT NULL DEFAULT 0,
    losses         BIGINT      NOT NULL DEFAULT 0,
    draws          BIGINT      NOT NULL DEFAULT 0,
    total_score    BIGINT      NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS match_results (
    match_id      BIGINT      PRIMARY KEY,
    seed          BIGINT      NOT NULL,
    language      TEXT        NOT NULL,
    over          BOOLEAN     NOT NULL,
    winner_seat   INT         NOT NULL,
    is_tie        BOOLEAN     NOT NULL,
    score0        BIGINT      NOT NULL,
    score1        BIGINT      NOT NULL,
    state_version INT         NOT NULL,
    server_tick   INT         NOT NULL,
    events        JSONB       NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL
);
