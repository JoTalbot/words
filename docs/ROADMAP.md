# Implementation Roadmap

## M0 — Prototype

Focus: prove the core competitive loop.

- [x] deterministic board generator (server/internal/match + prng golden)
- [x] deterministic scoring engine (server/internal/scoring golden)
- [x] dictionary compiler + validator benchmark (server/cmd/dictcompile canonical pipeline; in-process validator benchmark; snapshots en/ru/uk canonical-locked by test)
- [x] authoritative 1v1 match simulation (waves/claims/locks/steals/replay)
- [x] WebSocket transport adapter (binary protobuf envelopes, HTTP create endpoint)
- [x] Protobuf encode/decode compatibility tests (round-trip + byte stability)
- [~] prediction/reconciliation (server convergence proven headless; Unity client pending)
- [x] reconnect/resume (token reconnect inside grace; canonical snapshot reconciliation)
- [x] network fault simulation (RTT/loss matrix over real WebSocket)
- [ ] Unity swipe prototype (blocked: no Unity Editor for Linux/ARM64 on the dev host)

Exit gate: two remote clients can complete repeated matches with identical final state and reproducible event logs.

## M1 — Vertical Slice

- [~] production-shaped match service (resource limits, request logging,
  match result persistence, /metrics counters, graceful shutdown,
  Docker/compose, background reaper, intent rate limiting, seat-token
  rotation + optional TTL — 2026-09-10)
- [ ] shared board UX
- [ ] Claim / Lock / Cross-Steal
- [~] combo and Sudden Death (server: combo engine golden since M0; Sudden
  Death opt-in tiebreak done 2026-09-10 — docs/M1-SUDDEN-DEATH.md; client
  presentation pending)
- [x] basic matchmaking (stub: poll-based FIFO pairing per language, 2026-09-09)
- [~] player profile (in-memory profiles + stats folding via player_ids done;
  durable PostgreSQL storage done 2026-09-10 — docs/M1-PERSISTENCE.md,
  WORDARENA_POSTGRES_DSN)
- [~] telemetry baseline (in progress: lifecycle + per-action counters via
  /metrics; export/pipeline still TODO)

## M2 — Alpha Core

- [ ] 60-player mode
- [ ] bot strategy and disclosure policy
- [ ] anti-snowball mechanics
- [ ] first PvE content
- [ ] guild foundation
- [ ] infrastructure load testing

## M3 — Feature Complete Beta

- [ ] seasons and battle pass
- [ ] cosmetics
- [ ] behavioral anti-cheat signals
- [ ] dictionary hotfix pipeline
- [ ] appeals/moderation tooling
- [ ] economy simulation

## M4 — Soft Launch

- [ ] release pipeline
- [ ] crash and performance monitoring
- [ ] FTUE instrumentation
- [ ] retention/cohort dashboards
- [ ] economy tuning
- [ ] server capacity validation

## M5 — Global Launch

- [ ] multi-region production
- [ ] first season
- [ ] guild territory layer
- [ ] world boss
- [ ] tournament operations
- [ ] LiveOps calendar

## Gate philosophy

A milestone is complete when its exit criteria are measurable, not when all planned code exists. Features may be cut or delayed when experiments show they do not improve player value or operational safety.
