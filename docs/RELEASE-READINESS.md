# Release readiness — evidence, not an approval

Updated 2026-09-19, session 14 / batch 44. **Production release is not approved.**
The session-13 safe-default bundle is recorded, but does not complete the
roadmap. No game-service deployment, dictionary publication, region expansion or
anti-cheat enforcement was performed by session 14.

## Roadmap scope

Counts are top-level checkboxes in `docs/ROADMAP.md`, not percentages or a
claim that every historical test was rerun this session.

| Milestone | Done | Partial | Not done | Meaning |
|---|---:|---:|---:|---|
| M0 | 10 | 0 | 0 | Prototype scope; historical server and hosted-device evidence linked in roadmap |
| M1 | 7 | 0 | 0 | Vertical slice, not a production-launch approval |
| M2 | 6 | 0 | 0 | Alpha foundation; 60-player row bookkeeping reconciled with already merged 31A–32D and 39A evidence |
| M3 | 0 | 2 | 4 | Dictionary hotfix tooling and behavioral measurement (40A/41A/42A/42B/42C/45A) partial; seasons/battle pass, cosmetics, moderation, economy simulation not done |
| M4 | 0 | 0 | 6 | Soft launch not complete |
| M5 | 0 | 0 | 6 | Global launch not complete |

Owner approval of unchanged defaults does not implement seasons, moderation,
economy, release automation or enforcement. The M3 anti-cheat enforcement
boundary in `docs/M3-ANTI-CHEAT.md` remains in force.

## Recovery findings and completed repairs

- OCI `/opt/words` was clean at `3555a82`, while GitHub main had reached
  `5663d0f`. Prior 42C path histograms and 43 dictionary tooling were already
  merged and were **not** reimplemented.
- Container smoke run **35406709596** was red on main: session-13 commits had
  changed 15 script modes from executable to non-executable (14 violated the
  shebang guard). **44A / PR #86 -> `8e5d4cb`** restored the original modes;
  all six core CI checks passed on both PR head and merge. No guard weakened.
- **44B / PR #87 -> `ea4ce0e`** replaces a reproducible false-positive browser smoke:
  previously `WebSocket error` still produced PASS and the server env setting
  was ignored. Five local Playwright checks pass against isolated port 18110:
  real authenticated OPEN + nonempty binary frame, invalid token rejected,
  invalid scheme rejected, unreachable endpoint rejected, credentials required.
  Missing server env also exits nonzero. A new CI job runs this exact smoke.
  The screenshot was inspected: OPEN, first frame 116 bytes, no credentials.
  All seven checks passed on the PR and merge, including the new browser job.
- `go test -p 2 ./...` and `go vet -p 2 ./...` passed on `8e5d4cb`.
  The targeted behavioral/telemetry/socket race suite passed. Local Go tests
  used no PostgreSQL DSN; hosted CI separately covers PostgreSQL integration.
- Isolated service `infra/smoke.sh`: **27 passed, 0 failed**. Monitoring
  assets: **11/11**. Test assets: **6/6**. Later CI/device/exit-gate results and
  exact integrated revisions are in `agent/state/current.yml` session 14.
  Deterministic exit gate: **6/6**, baseline scores 43:45 / 37:60 / 44:40,
  equal event streams and terminal snapshots. Integrated `1589fd5`: seven
  core CI checks green.

## Environment and safety

- Live service remains the **unchanged** `3555a82` binary, SHA-256
  `860b6e689b1bca8597d8742c40aa7c99fd5b77c6305c697029be532176d94134`.
  Build VCS metadata confirms the revision and `vcs.modified=false`.
  `/healthz` is OK, `/readyz` is ready with PostgreSQL, `NRestarts=0` at recovery.
  The rollback binary remains in place. New local smoke/load work used an
  isolated in-memory server, never the live database.
- ADB lists no local devices; AVD directory is empty; local emulator,
  system images and KVM are absent. The authorized fallback is the project's
  GitHub-hosted x86_64 emulator. Session 14 dispatched **35430541294** with
  smoke enabled: functional PASS, but screenshot review found retained old text.
  **44D / PR #88 -> `1589fd5`** fixed opaque background repaint in the
  camera-less scene. Follow-up run **35431164102** on exact client source
  `b5ac8a8` is build/device/vision PASS: TAAN 4 cells, TSO 3 cells, 46 rollback
  markers, authoritative full-match result **13:39**, partner PASS. Both new
  screenshots were inspected and the text trails are gone. The match used the
  runner-local server (`10.0.2.2`), not the temporary public tunnel.
- **44E:** the old dev tunnel was not usable despite systemd active (expired
  registration, `Tunnel not found`, dead DNS). One normal restart restored
  the already authorized stage-1 tunnel; external healthz/readyz now return
  200 at `https://motivated-select-campaigns-statement.trycloudflare.com`.
  `WORDS_SERVER_URL` was updated after verification. No game-service restart,
  new exposure policy, permanent domain or proxy-trust change. The URL is
  ephemeral, so future recovery must probe it rather than trust this text.
- Docker is available on OCI. Hosted Container smoke verifies the isolated
  Compose/PostgreSQL stack; unrelated containers were not changed.
- Qwen2.5:3b is available through host-local Ollama. A large read-only review
  timed out at 150 seconds, then a narrowed 674-byte prompt completed (172
  generated tokens, one thread). Its output was reviewed, not auto-committed.
- Historical worktrees were preserved. No other project workers were killed.
  No secrets are added to this report, state or commits. Credentials exposed
  in chat should be rotated by their owner; unrelated access is not revoked
  autonomously.

## Remaining release blocker and resume

**B2 scope conflict:** “uk fully disabled” is not true in the implementation.
The isolated main build accepts uk (HTTP 201); the dictionary is embedded.
PD-008 still requires three development languages. Confirm **publication hold
only** versus **release-specific full exclusion** as explained in
`docs/OWNER-GATE-DRAFT.md`. No licence is inferred, and neither a feature gate
nor a prose approval is legal clearance to distribute the data.

Resume from `agent/state/current.yml` session 14 and current `origin/main`.
After the B2 clarification, create a bounded implementation task for the
selected scope; keep unimplemented M3/M4/M5 work explicit and choose the next
release milestone rather than treating a green prototype as global launch.
