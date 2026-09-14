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

- [~] 60-player mode - **rules now decided and implemented**; remaining work is
  snapshot fan-out, not gameplay. Merged 2026-09-13/14: batches 30A-30F made the
  whole server stack seat-count agnostic (simulation, room, matchmaker,
  transport), proved it at three layers (8-seat end-to-end over the real
  WebSocket surface, replay determinism at 3/5/8/16 seats) and characterized its
  cost (tick cost is roster-independent; snapshot cost grows ~3x from 2 to 60
  seats). Two hidden 1v1 bounds were found and fixed along the way: seats above
  1 could not submit a word at all (30D), and could neither steal nor be stolen
  from (31A). Batch 31A then implemented the two product decisions the owner
  delegated on 2026-09-14 - **Q9** per-wave cull to two thirds of the roster
  (60 -> 40 -> 26, never below 4 seats, ties across the cut line keep everyone,
  eliminated seats become logged-but-rejected spectators) and **Q10** a board
  that scales with the roster (12 cells at 1v1 up to a 6x10 grid, with the
  letter at cell i still a function of (seed, language, wave) alone so the 1v1
  board is bit-identical and a client can render before the lobby fills). Rules
  in docs/M2-ROYALE-RULES.md. Batches 31B and 31C then removed the two
  operational blockers: broadcast frames are now encoded ONCE per frame rather
  than once per subscriber (measured ~60x less CPU and allocation on a 60-seat
  fan-out), and a partial Royale lobby starts short-handed once its
  longest-waiting player has waited 20 s instead of expiring at the queue TTL.
  Batch 32A then closed the wire-bytes question - with a measurement that
  overturns the assumption behind it. State deltas are implemented
  (`MatchStateDelta`: scalars plus only the players and cells that changed,
  exact reconstruction proven with proto.Equal at every frame of a driven
  60-seat match, self-healing on a missed frame, and 1v1 byte-identical because
  deltas are scoped to rosters above two seats) and they cut the stream about
  **2x** (1 460 -> 870 B/frame saturated, 1 320 -> 600 B/frame typical;
  docs/LOAD-BASELINE.md has the churn breakdown). But the same measurement
  shows the bytes were never the blocker the row assumed: a 60-seat client
  costs **0.6-0.9 KB/s**, roughly 50 KB/s of aggregate egress for a full lobby.
  Interest-scoped snapshots cannot help either - every seat needs the whole
  board and the whole scoreboard. Batch 32B then put `seats` on the HTTP
  surface: `POST /v1/matches` accepts a roster size in `2..60`, the wider
  `tokens`/`user_ids` arrays are roster-shaped, and a created 60-seat room was
  verified over a real socket (60 players on a 60-cell board). The default
  stays 1v1 behind `WORDARENA_MAX_SEATS`, because a 60-seat match through an
  unauthenticated endpoint is 60 tokens and 60 sockets per request and the
  current public exposure was reviewed for a single-developer deployment - one
  environment variable turns it on, and the refusal names it.
  REMAINING on this row: a decision on bot backfill for under-filled lobbies
  (belongs with the bot disclosure row; the shipped default is the human-only
  short-handed start from 31C, so nothing is blocked on it). The largest
  remaining wire saving, if ever worth taking, is replacing the per-second lock
  countdown with a stable lock field (~27 % of the delta), which is a
  client-contract change rather than a server optimisation.
- [x] bot strategy and disclosure policy - **Q5 decided 2026-09-14** (owner
  delegated the choice) and implemented in batch 32D
  (docs/M2-BOT-POLICY.md): a bot is DECLARED by the server and never inferred,
  the declaration is DISCLOSED to every seat in every snapshot, in deltas, in
  the HTTP state view, in telemetry and in the finished result, and any match
  containing one is recorded as rating- and reward-ineligible
  (`bots` / `bot_present` / `rating_eligible`, durable via migration 004).
  The decision also closes the backfill question negatively: under-filled
  lobbies are never topped up with silent opponents, so the human-only
  short-handed start from 31C remains the answer to low population. The QA and
  device path declares itself at the queue, which is the one place a bot really
  appears today. Deliberately still open as *features* (not policy): a labelled
  practice/casual bot mode, a separate bot rating pool, and bot difficulty.
- [~] anti-snowball mechanics - the catch-up rule is implemented as an
  **opt-in** server rule (batch 32C, docs/M2-ANTI-SNOWBALL.md): a seat at least
  `CatchUpGap`=25 points behind the leader earns half again on an accepted word,
  capped at 15, computed from non-eliminated scores in integer arithmetic so a
  replay cannot diverge. It is off by default, so no M0/M1 baseline, replay or
  device assertion moves, and it deliberately does not touch cell credit values
  because that is what a steal debits - a re-priced steal would have been a much
  larger change than a score bonus. REMAINING: the board-side levers (leader
  lock duration, steal economics), a combo cap, exposing the option per match,
  and calibration of the constants against playtest rather than this guess.
- [x] first PvE content - **batch 32E** (docs/M2-PVE.md): `POST /v1/matches`
  with `"pve": true` creates a 1v1 against an opponent the SERVER drives, so a
  single player can play a real match with no second client and no external
  tooling. The opponent is a declared bot, so the whole 32D disclosure chain
  applies unchanged (labelled on the wire, in the HTTP state view, in deltas, in
  telemetry, in the durable result) and the match is never rating-eligible. Its
  intent is a pure function of `(snapshot, tick, dictionary, policy)` and it
  submits through `Room.Submit`, the same validated door a human uses - so a
  practice match replays like any other and the opponent has no privileged path
  in the simulation. Ships behind `WORDARENA_ALLOW_BOT_SEATS` (default off), 1v1
  only, and polite by default (3-4 letter words, 1.5 s between them, never
  steals). REMAINING: difficulty selection and any tutorial framing.
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
