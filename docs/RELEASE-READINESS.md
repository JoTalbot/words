# M0 Release Readiness — Word Arena

Status: SERVER-SIDE COMPLETE (verified 2026-09-09). Client blocked on B1
(Unity/ARM64). Web transport proof created as an interim (client/web-m0/).

Evidence (verified by code and tests on the dev host):
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
- web client proof: client/web-m0/index.html + test-smoke.py (Playwright)

Validation commands (all green on the dev host, 2026-09-09):

```bash
cd /opt/words/server && go vet ./... && go test ./...
```

Blocked (documented, not hidden):
- B1 Unity Editor/CLI unavailable for Linux/ARM64 → Unity swipe prototype +
  client prediction/rollback + Android AVD
- B2 UK dictionary GPL-3.0+ derivative → legal review required before
  distribution (dictionary/NOTICE.md)

Release gate recommendation:
- If B1 resolves via a remote Unity build host or an approved Unity Linux/ARM64
  build, the remaining M0 client criteria can be demonstrated immediately; the
  server side already satisfies M0 criteria 1-8.
- If B1 stays unresolved, the M0 server gate is complete; client/web-m0/ is an
  interim transport proof, with the Unity client as a follow-up M1 feature.
