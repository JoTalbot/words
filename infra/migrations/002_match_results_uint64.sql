-- Widen match_results.match_id and match_results.seed to the full uint64 domain.
--
-- Why this exists: the service models both values as Go uint64 (matchResult in
-- server/cmd/game/api.go) but the baseline schema typed them BIGINT, i.e.
-- int64. Every value with the top bit set is therefore unrepresentable, and
-- pgx rejects it at encode time rather than at the database:
--
--   match lifecycle=result_store_error err=postgres: put result: unable to
--   encode 0xc07644e8862430b9 into binary format for int8 (OID 20):
--   13868347868007641273 is greater than maximum value for int64
--
-- Observed live on arm-server-01 on 2026-09-10. randomSeed() draws eight
-- crypto-random bytes, so roughly half of all server-chosen seeds landed above
-- MaxInt64 and those matches finished without a durable result row: the match
-- still played and still reported over=true on the socket, but GET
-- /v1/matches/{id}/result returned nothing after the in-memory TTL expired and
-- nothing survived a restart. The failure was silent because the match itself
-- was healthy.
--
-- A client-supplied seed (the explicit-seed path kept by the batch 21A
-- fairness gate) could hit the same wall at any value, so narrowing the
-- generator instead of widening the column would have left the bug reachable.
--
-- NUMERIC(20,0) is the exact, unsigned 64-bit integer type in Postgres:
-- 20 digits covers 0..18446744073709551615 with no loss and no sign bit. The
-- service binds and scans these columns as decimal text, which keeps the
-- round trip driver-independent and leaves the stored value identical to the
-- seed the HTTP API reports as JSON.
--
-- Idempotent: ALTER COLUMN ... TYPE is a no-op when the column already has the
-- target type, so a re-run after a partial failure is safe.

ALTER TABLE match_results
    ALTER COLUMN match_id TYPE NUMERIC(20,0);

ALTER TABLE match_results
    ALTER COLUMN seed TYPE NUMERIC(20,0);
