# M1 — Vertical Slice Architecture Prep (post-M0)

Based on ROADMAP.md M1 section; blocked until B1 resolved.

Prerequisites (authoritative server M0 verified):
- Deterministic PRNG + board generation
- Dictionary compiler + snapshots v2 (en/ru/uk)
- Authoritative 1v1 match simulation (waves/claims/locks/steals/replay)
- WebSocket + protobuf transport (binary envelopes, HTTP create endpoint)
- Protocol compatibility tests (round-trip + byte stability)
- Reconnect/resume + network fault matrix

M1 incremental targets (not started — client blocker B1):
- Production-shaped match service (scaling, session semantics)
- Shared board UX (Unity / WebGL / mobile — depends on B1 resolution)
- Claim / Lock / Cross-Steal UI + server-side rules (already simulated)
- Combo and Sudden Death scoring rules (scoring engine verified)
- Basic matchmaking (post-client; server infra ready)
- Player profile + telemetry baseline (observability)

Architecture decision preserved (M0): authorative simulation stays in one process (Go) until measured bottleneck.

Exit gate for M1 start: Unity client prototype works on dev or build host; B2 resolved if distributing UK dict; CI green for 30 days.
