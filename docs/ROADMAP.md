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
- [x] shared board UX (Unity runtime bootstrap demo, server-bound REST create +
  binary protobuf WebSocket adapter, lifecycle controls, pending-intent
  reconciliation, drag/swipe selection and the MATCH OVER overlay — 2026-09-10).
  BOTH exit criteria VERIFIED ON A REAL DEVICE 2026-09-13 (Unity Android run
  34782169182, main 726f759, hosted API 35 emulator against the Q8 stage-1
  tunnel): (a) rollback animation — rejected pending intents flash red and log
  `[WORDS_ROLLBACK] word=... result=rejected: not in dictionary` / `blocked by
  rule`, captured many times in that run's logcat (M1-batch28a); (b) production
  shared-board UX — the smoke ran an unattended FULL match: the client queued,
  paired with the `headless-bot -partner` opponent, played 107 auto-submitted
  paths across three waves and the match reached its authoritative terminal
  state, asserted by `[WORDS_MATCH_COMPLETE] final=33:16 match=9290
  winner=seat0 version=16 source=result-endpoint` plus
  `WORDS_FULL_MATCH_SMOKE_OK`, with the partner printing
  `PARTNER-GATE: PASS` on the same match (M1-batch28b, plumbing repaired by
  M1-batch28d). The marker can only come from the REST result endpoint, so
  the criterion is authoritative, not a screenshot.

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

- [~] 60-player mode — foundation in progress. Merged 2026-09-13: batch 30A
  (the simulation is seat-count agnostic, `Config.Seats`, bounds 2..60, rank /
  tie / result / fingerprint expressed over the roster, 1v1 bit-identical) and
  batch 30B (roster-shaped room via `SeatUserIDs`/`SeatTokens`, seat-indexed
  `SnapshotToProto`) and batch 30C (`createRoomN` mints a token and a non-zero
  user id per seat, `newMatchmakerWithSeats(n)` forms an N-player lobby in FIFO
  seat order; the 1v1 path stays byte-identical) and batch 30D (an 8-seat
  end-to-end run over the real HTTP + WebSocket surface, which caught a
  leftover 1v1 bound in `Room.SubmitWithSeq` that would have left every player
  except the first two unable to play a word) and batch 30E (replay
  determinism at 3/5/8/16 seats rebuilt from the event log alone, and an
  identical board at 2..60 seats so the roster cannot leak into wave
  generation) and batch 30F (cost model measured at 2..60 seats: tick cost is
  roster-independent, snapshot cost grows ~3x, and per-subscriber fan-out is
  the real constraint). The ENGINEERING half of this row is done; what remains
  is product, now formalized as Q9 (elimination rule) and Q10 (board sizing)
  in docs/PRODUCT-DECISIONS.md. The server stack is now
  seat-count agnostic from the simulation up to queue admission, and no HTTP
  endpoint exposes a seats knob yet, because Royale gameplay itself is
  BLOCKED ON TWO PRODUCT DECISIONS: the elimination rule (`IsEliminated` is still always false)
  and board sizing for a large roster (`CellsPerWave` is 12, sized for two);
  anti-snowball and bot disclosure depend on both.
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
| shared board UX | **verified on device** | both criteria green on Unity Android run 34782169182 (2026-09-13, main 726f759): `[WORDS_ROLLBACK]` device markers for rejected intents (28a) and a full unattended match ending in `[WORDS_MATCH_COMPLETE] final=33:16 ... source=result-endpoint` + `WORDS_FULL_MATCH_SMOKE_OK` against the Q8 tunnel, with `PARTNER-GATE: PASS` from the opponent (28b, repaired by 28d). Nothing open |
| Claim / Lock / Cross-Steal | in progress | **Device smoke green 2026-09-13** on run 34772130697 (main 91f3b59): row swipe `[WORDS_SWIPE] cells=4 word=TAAN` + 28c diagonal `[WORDS_SWIPE] cells=3 word=TSO`. The eight-run regression is closed: 27A coordinates, 27D instrumentation, 27E Awake NRE, 27F focus gate, 27G probe (proven the double 40 px inset subtraction: the event is already BeginArea-local under the GUI.matrix), 27H identity transform, 27I persistent sheet gate. Row CLOSED 2026-09-13: run 34782169182 added the 28a `[WORDS_ROLLBACK]` device markers and the 28b full-match loop (`[WORDS_MATCH_COMPLETE] ... source=result-endpoint`) |
| combo and Sudden Death | done for M1 scope | nothing server-side; presentation delivered (17C) |
| security / protocol robustness | done for M1 scope | — (finding S-2 closed 2026-09-11 by `M1-batch21g-match-codes`, verified live with the gate enabled; profile-id boundary validation closed 2026-09-12 by `M1-batch26a-profile-id-range`) |
| build + CI reliability | green | Unity Android run 34683472147 was fully green (batch 26E KVM fix: boot 31.6 s vs 368–588 s software emulation). The next two scheduled runs regressed on a coordinate defect in the smoke's board-rect conversion (27A: client now publishes screen-space `sx*/sy*`, script and focus recovery fixed). `gofmt` is a merge precondition (26B). Green on the merged chain: device runs 34772130697, 34773815149 and 34782169182 (the last one including the full-match leg). Container smoke hygiene gate repaired on main (1dfe1b7) |

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
