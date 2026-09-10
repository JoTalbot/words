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

Open items before the M1 gate can be declared:

1. Device-level swipe smoke confirmation (batch 17E) — currently blocked by
   hosted-emulator boot flake (`input keyevent 82` Broken pipe / exit 224);
   code itself CI-green; retry in flight, will not block indefinitely.
2. Live OCI via-queue regression (batch 17D) — local validation green; live
   run pending OCI SSH recovery (degraded since ~08:20Z on 2026-09-10).
3. External access decision (PRODUCT-DECISIONS.md Q8) — loopback-only until
   an edge/TLS + abuse review is made; does not block the vertical slice.
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
