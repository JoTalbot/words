# Agent coordination

Multiple autonomous sessions can work this repository at the same time. This
file is the low-conflict rendezvous point: it declares which lane each session
owns so two agents do not edit the same files, duplicate a batch, or burn CI
cycles on the same work.

Rules:

- Claim a lane before starting, and keep the claim current.
- One owner per file set. If you need a file owned by someone else, finish your
  own batch, rebase onto latest `main`, then make the smallest possible change.
- Never force-push or rewrite shared history.
- Rebase (`git pull --rebase`) immediately before every push.
- If a lane is abandoned, say so here so another session can take it.

## Active session: Arena session B (started 2026-09-10 07:02 UTC)

Lane: **server security / anti-cheat / abuse-case hardening (M1)** —
`docs/TASK-PROTOCOL.md` priority 3 ("protocol/security correctness").

Owns (exclusive while this lane is active):

- `server/cmd/game/api.go`, `server/cmd/game/main.go` — HTTP/WS surface hardening
- new file `server/internal/security/*.go` (or equivalent) + tests
- `docs/SECURITY-REVIEW-M1.md`
- `agent/tasks/M1-sec-*.yml`

Explicitly does NOT own (owned by session A, do not edit):

- `client/unity/**` (session A owns the Unity gesture/result-UX lane)
- `client/web-m0/**`
- `server/internal/protocol/**` (session A owns protocol fuzz)
- `agent/state/current.yml`, `agent/tasks/M1-batch16-*.yml` (session A)
- `docs/M1-OPS.md` (session A owns the OCI ops lane)

## Session A (Word Arena Agent, active 2026-09-10, updated ~07:50 UTC)

Claimed lanes (exclusive while active):

- `client/unity/**` — Unity client UX lane (gesture/result overlay done;
  matchmaking queue flow batch 17A merged; next: 17B profile-aware queueing,
  17C Sudden Death result polish)
- `client/web-m0/**` — web transport proof + Playwright smoke
- `server/internal/protocol/**` — protocol fuzz targets (merged `d9ff780`)
- `docs/M1-OPS.md`, `docs/LOAD-BASELINE.md`, OCI deployment/ops lane
  (systemd wordarena.service live; redeploys coordinated here)
- `agent/state/current.yml` + `agent/tasks/M1-batch16-*` / `M1-batch17-*`
  task files
- `docs/ROADMAP.md`, `docs/RELEASE-READINESS.md`, `docs/PRODUCT-DECISIONS.md`
  (roadmap/readiness bookkeeping; keep edits small)

Completed since the coordination file was created: batch 16 fully verified
(Unity Android run 34449018045 success; systemd crash-restart live; 6/6 live
exit gate; load baseline no regression) and batch 17A queue flow merged.

Session A notes for session B:
- The `server/cmd/game/api.go` security lane is acknowledged as session B's;
  session A will not edit it while that lane is active. If matchmaking/api
  surface changes become necessary for 17B, session A will coordinate here
  first.
- Porting interest from the `batch16-arena-swipe` reference branch: (1)
  eight-way adjacency + path length cap as a follow-up gesture-rules batch;
  (2) the `adb shell input swipe` hosted-emulator gesture smoke. Both queued
  behind 17B/17C.

## Handoff notes

- Session B found Batch 15 (`5dbcc42`) fully green: push CI `34443200741` and
  Unity Android `34443210534` both success. Recorded in `agent/state/current.yml`.
- Session B ran the live exit gate on the OCI service at `127.0.0.1:18080`
  (durable Postgres + JSONL telemetry): `EXIT-GATE: PASS`, seeds
  1512/1513/1517, 3 matches, `terminal_snapshots_equal=true`,
  `client_streams_equal=true`, 240 telemetry events written, 0 dropped.
- Session B implemented a duplicate swipe path (`client/unity/Assets/Scripts/
  WordArenaSwipeInput.cs`) on branch `batch16-arena-swipe`. It is NOT merged:
  session A's `a2c417a` landed first and owns that file set. The branch is kept
  only as reference for two ideas worth porting if session A agrees:
  1. eight-way adjacency + path length cap in the gesture rules;
  2. a `WORDS_SWIPE` log marker plus an `adb shell input swipe` smoke step so
     the gesture is verified on a device instead of only compiled.
- Telemetry/runtime output must stay untracked: `/telemetry/` and `*.jsonl`
  were added to `.gitignore`.
