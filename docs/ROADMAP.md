# Implementation Roadmap

## M0 — Prototype

Focus: prove the core competitive loop.

- [x] deterministic board generator (server/internal/match + prng golden)
- [x] deterministic scoring engine (server/internal/scoring golden)
- [x] dictionary compiler + validator benchmark (server/cmd/dictcompile canonical pipeline; in-process validator benchmark; snapshots en/ru/uk canonical-locked by test)
- [x] authoritative 1v1 match simulation (waves/claims/locks/steals/replay)
- [x] WebSocket transport adapter (binary protobuf envelopes, HTTP create endpoint)
- [x] Protobuf encode/decode compatibility tests (round-trip + byte stability)
- [x] prediction/reconciliation (server convergence proven headless; Unity
  pending-intent overlay + canonical snapshot/event reconciliation shell
  added 2026-09-10; M0 criterion 3 satisfied — rejected predictions converge
  via canonical snapshots without scene reload; richer rollback animation
  remains M1 polish)
- [x] reconnect/resume (token reconnect inside grace; canonical snapshot reconciliation)
- [x] network fault simulation (RTT/loss matrix over real WebSocket)
- [x] Unity swipe prototype (delivered via B1 mitigation: runtime IMGUI gesture
  path in client/unity — drag/swipe multi-cell selection with tap toggle and
  swipe-back undo — built and smoke-tested on GitHub-hosted x86_64 Unity CI +
  hosted Android emulator; verified run 34449018045 success, 2026-09-10)

Exit gate: two remote clients can complete repeated matches with identical final state and reproducible event logs.

## M1 — Vertical Slice

- [x] production-shaped match service (resource limits, request logging,
  match result persistence, /metrics JSON + Prometheus counters,
  graceful shutdown, Docker/compose, background reaper, intent rate limiting,
  seat-token rotation + optional TTL, JSONL telemetry export, /readyz readiness/draining — 2026-09-10; Unity client lifecycle controls for /readyz, result fetch and token rotation added 2026-09-10;
  systemd-managed live deployment with crash-restart, secret env file and
  docs/M1-OPS.md runbook — 2026-09-10; versioned schema migrations with a CI
  drift gate (batch 19) and Prometheus/Grafana assets validated live and
  statically (batch 20); transport abuse hardening — snapshot seat
  credential, origin policy, body caps, per-caller mutation budget, explicit-seed
  fairness gate, HTTP timeouts — plus docs/SECURITY-REVIEW-M1.md (batch 21A);
  repo-hygiene guard in CI (batch 21C); unguessable match codes + stored read
  capability with the gate enabled live (batch 21g); live systemd service
  promoted to main 97a2414 and verified live — batch 26G, 2026-09-12.
  Q8 exposure DONE 2026-09-13 (decision OPEN, staged): stage 1 is a Cloudflare
  quick tunnel on the OCI host — TLS terminated at the Cloudflare edge, origin
  stays loopback-only, no public TCP port, outbound-only tunnel under systemd
  (infra/expose-quick.sh + infra/words-tunnel.service), abuse review in
  docs/SECURITY-EXPOSURE.md, verified from an outside network via the public
  URL (healthz + readyz). Stage 2 (stable domain, per-IP limits, /metrics
  deny, public-token decision) awaits the owner-provided domain — Q8 stage 2,
  non-blocking for M1.)
- [~] shared board UX (Unity runtime bootstrap demo added 2026-09-10; server-bound REST create + binary protobuf WebSocket snapshot/event adapter added 2026-09-10; lifecycle ready/result/token controls added 2026-09-10; pending-intent reconciliation shell added 2026-09-10; drag/swipe multi-cell selection gesture path and MATCH OVER result overlay added 2026-09-10). REMAINING, each with one measurable exit criterion: (a) richer rollback animation — a rejected pending intent flashes the affected cells red and logs a [WORDS_ROLLBACK] marker verifiable on a device (M1-batch28a-rollback-flash: merged and CI-green 2026-09-13; the device marker rides on the 28b full-match loop, so it is Q8-blocked with (b)); (b) production shared-board UX — the device smoke runs an unattended full match loop from queue through swipe, send and the authoritative MATCH OVER overlay, verified by a [WORDS_MATCH_COMPLETE] marker (M1-batch28b-device-full-match-loop: IMPLEMENTED and merged 2026-09-13 — partner-mode opponent, opt-in client auto-play loop, `[WORDS_MATCH_COMPLETE] source=result-endpoint` marker and the bounded smoke leg. Partner mode verified live against the Q8 tunnel (full match to terminal state, PARTNER-GATE PASS); the first device run root-caused two plumbing defects, fixed by M1-batch28d; awaiting a green device run. The criterion itself is unchanged)
- [x] Claim / Lock / Cross-Steal (Unity visual demo added 2026-09-10; Unity now sends SubmitWordIntent to the authoritative server, displays pending claims, and reconciles canonical ownership/lock snapshots; swipe gesture path with ordered selection added 2026-09-10; eight-way adjacency + bridge gesture rules and device-level swipe smoke added 2026-09-10 — batch 17E; device leg green on run 34683472147 after the 26E KVM fix). DEVICE-GREEN 2026-09-13 (run 34772130697, main 91f3b59): after the 27A/27D/27E/27H/27I chain closed the regression (the 27H identity transform fixed the double 40 px inset subtraction that the 27G probe had proven), the device smoke passes both assertions - row swipe [WORDS_SWIPE] cells=4 word=TAAN and the 28c diagonal swipe (0,0)->(2,2) with [WORDS_SWIPE] cells=3 word=TSO (a true multi-row path via the bridge rule; the verified criterion is cells >= 3 + word != the row-0 word, per M1-batch28c)
- [x] combo and Sudden Death (server: combo engine golden since M0; Sudden
  Death opt-in tiebreak done 2026-09-10 — docs/M1-SUDDEN-DEATH.md; Unity demo
  presentation added 2026-09-10; server event/snapshot binding added 2026-09-10; MATCH OVER result overlay with authoritative REST record added 2026-09-10; Sudden Death tiebreak headline + client-observed decisive word added 2026-09-10 — batch 17C; M1 scope closed 2026-09-13: nothing server-side open, presentation delivered)
- [x] basic matchmaking (stub: poll-based FIFO pairing per language, 2026-09-09;
  Unity client Find match (queue) flow with enqueue/poll/seat-token connect
  added 2026-09-10 — batch 17A)
- [x] player profile (in-memory profiles + stats folding via player_ids done;
  durable PostgreSQL storage done 2026-09-10 — docs/M1-PERSISTENCE.md,
  WORDARENA_POSTGRES_DSN; identity/auth layer is M2 scope)
- [x] telemetry baseline (JSON /metrics, Prometheus text export, optional
  non-blocking JSONL lifecycle/action event sink — 2026-09-10)

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

## M1 exit status, measured 2026-09-10

Counts below are task-level evidence from `agent/state/current.yml` and
`agent/tasks/`, not estimates.

| M1 row | State | What is still open |
|---|---|---|
| production-shaped match service | verified live | live systemd service promoted to main 97a2414 and re-verified live on 2026-09-12 (batch 26G: smoke 27/0, exit gate 3/3 with baseline scores). Q8 stage 1 done 2026-09-13: quick tunnel deployed on the OCI host and verified from an outside network; stage 2 stable domain awaits the owner-provided domain |
| basic matchmaking | done | — |
| telemetry baseline | done | — |
| player profile + durable storage | done | identity/auth layer is M2 scope |
| shared board UX | in progress | explicit exit criteria recorded 2026-09-13 (batch 27C): (a) rejected-intent rollback flash + `[WORDS_ROLLBACK]` device marker (M1-batch28a: merged, CI-green; device marker rides on the 28b loop); (b) unattended full-match device loop queue → swipe → send → authoritative MATCH OVER + `[WORDS_MATCH_COMPLETE]` marker (M1-batch28b: implemented + merged 2026-09-13, 4239614; partner mode live-verified; device run pending after the 28d plumbing repair 56d32cc) |
| Claim / Lock / Cross-Steal | in progress | **Device smoke green 2026-09-13** on run 34772130697 (main 91f3b59): row swipe `[WORDS_SWIPE] cells=4 word=TAAN` + 28c diagonal `[WORDS_SWIPE] cells=3 word=TSO`. The eight-run regression is closed: 27A coordinates, 27D instrumentation, 27E Awake NRE, 27F focus gate, 27G probe (proven the double 40 px inset subtraction: the event is already BeginArea-local under the GUI.matrix), 27H identity transform, 27I persistent sheet gate. Remaining for the row: 28a rollback-flash device marker and 28b full-match loop (unblocked 2026-09-13 by the Q8 tunnel deploy; in progress) |
| combo and Sudden Death | done for M1 scope | nothing server-side; presentation delivered (17C) |
| security / protocol robustness | done for M1 scope | — (finding S-2 closed 2026-09-11 by `M1-batch21g-match-codes`, verified live with the gate enabled; profile-id boundary validation closed 2026-09-12 by `M1-batch26a-profile-id-range`) |
| build + CI reliability | device leg repaired, pending CI | Unity Android run 34683472147 was fully green (batch 26E KVM fix: boot 31.6 s vs 368–588 s software emulation). The next two scheduled runs regressed on a coordinate defect in the smoke's board-rect conversion (27A: client now publishes screen-space `sx*/sy*`, script and focus recovery fixed). `gofmt` is a merge precondition (26B). Remaining: a green scheduled run on the merged 27A |

Verified live on 2026-09-10 against an isolated build of `c2079e8`: queue
exit gate 6/6 PASS, direct control 3/3 PASS with baseline scores
(43:45 / 37:60 / 44:40), `infra/smoke.sh` 18/18 (22/22 on main after 21A).

Verified live on 2026-09-10 against the **promoted** deployment at `5653210`
(the systemd service itself, not an isolated build): exit gate 6/6 PASS with the
same baseline scores (43:45 / 37:60 / 44:40), `infra/smoke.sh` 23/23, and 8 of 8
finished matches persisted — see `M1-batch23a-uint64-result-columns`, which
fixed a defect that had been dropping roughly half of all durable match results
on the live host.

## Gate philosophy

A milestone is complete when its exit criteria are measurable, not when all planned code exists. Features may be cut or delayed when experiments show they do not improve player value or operational safety.
