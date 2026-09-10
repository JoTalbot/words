# Release Readiness — Word Arena

## M1 Vertical Slice Readiness (assessment in progress, 2026-09-10)

M1 scope per docs/ROADMAP.md; evidence trail in agent/state/current.yml and
the batch task files under agent/tasks/.

| M1 item | Status | Evidence |
|---|---|---|
| Production-shaped match service | verified live | batches 1–12; systemd wordarena.service on OCI with durable Postgres, JSONL telemetry, /readyz draining, crash-restart, 6/6 live exit gate; docs/M1-OPS.md |
| Basic matchmaking | verified | server FIFO pairing (batch 3/7), live two-client pairing check, Unity queue flow (batch 17A CI+Unity green) |
| Player profile + durable storage | verified live | batches 5/8; Postgres durability across restart on OCI; Unity profile-aware queueing + stats refresh (batch 17B CI+Unity green) |
| Telemetry baseline | verified live | batch 10; JSON /metrics + Prometheus + JSONL on OCI |
| Shared board UX | verified (CI + hosted emulator) | batches 11–17F: runtime board, server protocol binding, lifecycle controls, prediction shell, swipe gestures with eight-way adjacency + device swipe smoke (pending emulator stability), result overlay, Sudden Death polish, color legend |
| Claim / Lock / Cross-Steal | server verified; client presentation verified | M0 simulation + Unity intent flow + reconciliation |
| Combo and Sudden Death | verified | server golden + opt-in tiebreak (docs/M1-SUDDEN-DEATH.md) + result presentation (batch 17C) |
| Protocol robustness | verified | fuzz targets (batch 16D, ~800k execs 0 crashes), byte stability, compat tests |
| Security / abuse resistance | verified for M1 scope | batch 21A: live-board read requires a seat credential, origin policy replaces `OriginPatterns: ["*"]`, 16 KiB body cap + unknown-field rejection, per-caller mutation budget with Retry-After, explicit-seed fairness gate, nickname policy, fixed 500 text, HTTP timeouts. 9 findings with dispositions in docs/SECURITY-REVIEW-M1.md; new gates covered by 15 tests (package + integration) |
| Containers, migrations, monitoring | verified in CI | batches 18/19/20: compose stack + smoke script executed in CI, `001_init.sql` baseline with a schema-drift gate that fails the build, Prometheus scrape config and a 9-panel Grafana dashboard cross-checked against a live exposition |
| Build/CI reliability | in progress | batch 21B: device smoke moved to `tools/android-smoke.sh` with retried device ops, a focus gate, ANR-dialog suppression, INFRA_FAIL vs PRODUCT_FAIL classes, a retry leg on another emulator image and a scheduled re-run; `agent/state/current.yml` is now parseable YAML (it had never been) |

Open items before the M1 gate can be declared:

1. Device-level swipe smoke confirmation (batch 17E) — **root cause found, not
   assumed**: run 34492844342 captured a screenshot showing "System UI isn't
   responding" holding the foreground, so the synthetic swipe never reached
   Unity; install/launch/log capture all succeeded. Batch 21B adds the focus
   gate and a second-emulator-image retry leg. Code is CI-green.
2. Live OCI via-queue regression (batch 17D) — **CLOSED 2026-09-10**: isolated
   build of `c2079e8` on the host, `-via-queue -rounds 2 -seeds 1,2,3` => 6/6
   EXIT-GATE PASS (terminal snapshots equal, client streams equal), direct
   control 3/3 PASS reproducing the baseline scores, `infra/smoke.sh` 18/18.
3. External access decision (PRODUCT-DECISIONS.md Q8) — loopback-only until
   an edge/TLS + abuse review is made; does not block the vertical slice.
   Note the operational side effect recorded this session: with no second
   observation channel, a host load spike makes the deployment unobservable
   for ~15 minutes (measured load1 8.6 -> 15-min average 256-276, kswapd
   active, ~2650 tasks) while `wordarena.service` itself stayed healthy.
3b. Security finding S-2 — sequential match ids make finished-match results
   enumerable. Accepted for the loopback deployment, blocked on
   `agent/tasks/M1-batch21g-match-codes.yml` before any public exposure.
3c. Deployment drift — the live systemd binary is `5dbcc42`; main is ahead by
   six batches. Promotion plus re-verification is the next ops step.
4. B2 uk dictionary legal review — distribution blocker only.

## M0 Release Readiness

Status: SERVER-SIDE COMPLETE (verified 2026-09-09, re-verified live
2026-09-10). CLIENT UNBLOCKED VIA REMOTE BUILD/DEVICE BACKENDS (B1
mitigated 2026-09-09/10): Unity builds on GitHub-hosted x86_64 Unity CI and
the Android APK is installed/launched on a GitHub-hosted emulator. The Unity
client is an M1 vertical-slice feature on top of the completed M0 server.

Evidence (verified by code and tests on the dev host and in CI):
- deterministic board seed (prng golden, replay fixture seed 1512)
- simultaneous claims identical (match lock + deterministic apply)
- prediction convergence via canonical snapshot (reconnect + grace)
- reconnect within GraceTicks=300 (~10 s) verified
- replay produces identical score (replay property trials; fixture seed 1512)
- dictionary validator identical (snapshots v2 en/ru/uk; test/dictionary)
- network fault matrix green (RTT 50/100/150 ms; loss 0/1/3 %)
- load baseline documented (docs/LOAD-BASELINE.md)
- protocol byte stability + round-trip verified
- regression test: TestSubmitWithSeqEchosClientSequence (matchroom)
- repeated complete matches over the real transport verified
  (TestExitGateRepeatedFullMatches: 2 rounds x seeds 1512/1513/1517,
  identical terminal state on both clients, live scores reproduce the
  offline replay; protocol carries a terminal over=true snapshot)
- live exit gate re-run against deployed build 5dbcc42 on OCI (2026-09-10):
  6/6 matches EXIT-GATE PASS with scores identical to the 2026-09-09 baseline
  (43:45 / 37:60 / 44:40 across seeds 1512/1513/1517)
- protocol decode path fuzz-hardened (server/internal/protocol/fuzz_test.go;
  ~800k execs across three targets with 0 crashes, seed corpus in CI)
- web client proof: client/web-m0/index.html + test-smoke.py (Playwright),
  re-verified 2026-09-10 with headless Chromium against a local build of
  current main: authoritative WebSocket OPEN with a real match token

Client-side M0 criteria status (B1 mitigation path):
- Unity swipe prototype: implemented as the runtime IMGUI gesture path in
  client/unity (drag/swipe multi-cell selection, tap toggle, swipe-back
  undo); built and smoke-tested on the GitHub-hosted emulator pipeline.
- Client prediction with rollback: pending-intent overlay + canonical
  snapshot/event reconciliation shell (batch 15); richer mobile rollback
  animation remains an M1 UX item.

Validation commands (green on the dev host 2026-09-09 and re-run on OCI and
in GitHub CI 2026-09-10):

```bash
cd /opt/words/server && go vet ./... && go test ./...
WORDARENA_ADDR=http://127.0.0.1:18080 /tmp/headless-bot -rounds 2 -seeds 1512,1513,1517
```

Operational state (2026-09-10): systemd unit wordarena.service on OCI with
durable Postgres + JSONL telemetry; runbook docs/M1-OPS.md; loopback-only
exposure pending the edge/TLS decision (PRODUCT-DECISIONS.md Q8).

Blocked (documented, not hidden):
- B2 UK dictionary GPL-3.0+ derivative → legal review required before
  distribution (dictionary/NOTICE.md)
- (mitigated) B1 Unity Editor/CLI unavailable for Linux/ARM64 → replaced by
  GitHub-hosted x86_64 Unity CI + hosted Android emulator; see
  docs/unity-ci.md and docs/android-build.md. Local ARM64 editor sessions
  remain unavailable but no longer gate the pipeline.

Release gate recommendation:
- M0 server gate: COMPLETE (criteria 1–8 verified; re-verified live
  2026-09-10).
- M0 client gate: satisfied via the remote build/device backends for the
  transport, prediction shell and swipe prototype; remaining polish
  (rollback animation, production UX) is tracked as M1 roadmap [~] items.
- B2 remains the only hard legal blocker and only affects uk-dictionary
  distribution, not the en/ru vertical slice.
