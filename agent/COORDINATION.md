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

## Session A (Word Arena Agent, active 2026-09-10)

Observed lanes (from commits `1cdd567`..`9a942df` and later):

- Unity swipe gesture selection + result overlay (`a2c417a`, batch 16)
- M1 operations runbook + OCI ops task (`docs/M1-OPS.md`)
- protocol decode fuzz targets (`server/internal/protocol/fuzz_test.go`)
- `client/web-m0` binary-frame decode + parameterized smoke proof

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
