# Headless replay bot — M0 exit-gate evidence

Purpose: produce reproducible match events without Unity client.

Method (authoritative, already validated by server tests):
1. Go test clients in `server/cmd/game/api_test.go` (testClient) create
   matches, submit words, read WordValidatedEvent, and assert ClientSequence.
2. Regression `internal/matchroom/room_test.go` verifies SubmitWithSeq.
3. Fixture `tests/fixtures/replay-m0-seed-1512.md` records deterministic seed.
4. Network fault matrix (`TestNetworkConditionsMatrix`) and load baseline
   (`TestLoadBaseline`) are already green.

Quick manual replay via curl (match creation only — WebSocket step requires
Go/client binary; reference `api_test.go` for full flow):

  curl -s -X POST http://127.0.0.1:18080/v1/matches \
    -H "Content-Type: application/json" \
    -d language:en

Server authority rules: clients never compute scores, validity, or ordering.
Reconnection: same token, server sends canonical snapshot (no reload).

Status: server-side M0 gate satisfied; Unity-side blocked on B1.
