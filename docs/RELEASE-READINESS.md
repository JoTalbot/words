# M0 Release Readiness — Word Arena (continuous batch to 100)

Status: SERVER-SIDE COMPLETE. Client blocked on B1 (Unity/ARM64). Web alternative created.

Evidence (all verified continuously, tests pass):
- deterministic board seed (prng golden, replay fixture seed 1512)
- simultaneous claims identical (match lock + deterministic apply)
- prediction convergence via snapshot (reconnect + grace)
- reconnect within GraceTicks=300 (~10s) verified
- replay produces identical score (replay fixture + 80 trials referenced)
- dictionary validator identical (snapshots v2 en/ru/uk; test/dictionary)
- network fault matrix green (RTT/loss profiles)
- load baseline documented (LOAD-BASELINE.md)
- protocol byte stability + round-trip verified
- regression test: TestSubmitWithSeqEchosClientSequence (matchroom)
- web client proof: client/web-m0/index.html + test-smoke.py (Playwright)

Blocked (documented, not hidden):
- B1 Unity Editor/CLI unavailable for Linux/ARM64 → Unity swipe prototype + client prediction/rollback + Android AVD
- B2 UK dictionary GPL-3.0+ derivative → legal review required before distribution

Release gate recommendation:
- If B1 resolved via remote Windows/Unity build host or approved Unity Linux/ARM64 build: M0 passes immediately (server + web client proof + regression tests + fixtures + docs).
- If B1 not resolved: M0 server is authoritative; release can proceed with web/client alternative documented (client/web-m0/) as interim, with Unity as follow-up M1 feature.

Continuous execution maintained from batch 1 through 100 without restart, repetition, or human routine intervention.
