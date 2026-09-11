-- Add the unguessable match code and the per-match read capability.
--
-- Closes docs/SECURITY-REVIEW-M1.md finding S-2: GET /v1/matches/{id}/result and
-- /v1/matches/{id}/replay were unauthenticated and keyed by a monotonic counter,
-- so a one-line loop read the final score, winner and full word-by-word event
-- log of every finished match.
--
-- S-2 explicitly deferred this rather than bolting a gate onto sequential ids,
-- because durable results are a verified acceptance criterion of batch 8: a
-- finished result must stay readable after a restart, and the seat tokens are
-- process-lifetime state. A credential that has to outlive a restart therefore
-- has to be stored with the result. That is what read_capability is.
--
-- match_code and read_capability are 128 bits of crypto/rand each, hex-encoded.
-- They are separate columns so a code can be shared - on a post-match screen, in
-- a support ticket, in a URL - without also sharing the right to read the
-- outcome.
--
-- Both columns are nullable on purpose: rows written before this migration have
-- no code and no capability, and the service refuses a capability-gated read of
-- such a row rather than inventing one. A UNIQUE index treats NULLs as distinct,
-- so the pre-existing rows do not collide.
--
-- Idempotent: ADD COLUMN IF NOT EXISTS and CREATE UNIQUE INDEX IF NOT EXISTS
-- make a re-run after a partial failure safe.

ALTER TABLE match_results
    ADD COLUMN IF NOT EXISTS match_code TEXT;

ALTER TABLE match_results
    ADD COLUMN IF NOT EXISTS read_capability TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS match_results_match_code_key
    ON match_results (match_code);
