-- Add the simulated-player disclosure to durable match results.
--
-- M2 batch 32D closes product decision Q5: a seat played by a simulated player
-- is declared by the server, disclosed to every observer on the wire, and
-- recorded with the authoritative outcome. This migration is the durable half
-- of that last clause.
--
-- Why it has to be durable rather than a live-stream flag: the purpose of the
-- disclosure is to keep bot matches out of ratings and rewards, and those are
-- computed from the stored outcome, possibly after a restart and days after the
-- match. A fact that lives only in a room's memory cannot do that job.
--
-- Two columns, because they answer different questions. bot_present is the
-- predicate a rating or reward job needs ("exclude bot matches") and costs
-- nothing to evaluate; bots carries the seat numbers themselves for audit and
-- support ("which seats were bots?").
--
-- Defaults are the correct reading of pre-existing rows: matches written before
-- this migration were created before a bot could be declared at all, so they
-- contained none. NOT NULL with a default keeps the read paths free of NULL
-- handling, in contrast to the nullable match_code/read_capability columns of
-- migration 003, where NULL carried real meaning ("this row predates the
-- capability and cannot be read with it").
--
-- Idempotent: ADD COLUMN IF NOT EXISTS makes a re-run after a partial failure
-- safe.

ALTER TABLE match_results
    ADD COLUMN IF NOT EXISTS bot_present BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE match_results
    ADD COLUMN IF NOT EXISTS bots JSONB NOT NULL DEFAULT '[]'::jsonb;
