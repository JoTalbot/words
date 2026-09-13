# Product & Technical Decision Register

This file records decisions that materially affect implementation. Open questions stay explicit until validated by prototype, playtest or production data.

## P0 decisions

| ID | Decision | Status | Rationale |
|---|---|---|---|
| PD-001 | Competitive match state is server authoritative | Accepted | Required for fair PvP and anti-cheat |
| PD-002 | M0 ships 1v1 before 60-player Royale | Accepted | Validates core loop before scale complexity |
| PD-003 | Competitive scoring is deterministic | Accepted | Enables replay, auditing and desync diagnosis |
| PD-004 | Dictionary is versioned data with immutable runtime snapshot + deltas | Accepted | Enables safe LiveOps word corrections |
| PD-005 | Core match path must not depend on analytics availability | Accepted | Analytics outages must never stop a match |
| PD-006 | M0 match server starts in Go | Accepted | Keeps the first production path simple; optimize only after profiling |
| PD-007 | M0 match rules v0.1 (see docs/M0-MATCH-RULES.md) | Accepted (prototype) | Engineering defaults so the deterministic core can ship; numbers are calibration candidates per Q1/Q2 |
| PD-008 | Supported languages at M0: en, ru, uk (match dictionaries + UI localization scope) | Accepted | Product requirement: game must be playable in Russian, Ukrainian and English |

## Questions requiring product validation

### Q1 — Score formula
The supplied specification references a mathematical scoring formula, but the actual formula is absent from the submitted text. This must be defined before implementation of scoring tests.

Required fields should include at least:

- base points by word length;
- rarity/frequency treatment;
- combo multiplier and decay;
- steal modifier;
- lock/hold reward;
- catch-up mechanics;
- hard caps to prevent runaway scores.

### Q2 — Board model
The specification describes a 10–14-cell shared crossword matrix, but does not define the deterministic board-generation algorithm, topology constraints or legal-word placement rules.

### Q3 — Match authority timing
Do not use a client-provided timestamp as competitive authority. The server should order intents using server receive sequence / monotonic clock and a deterministic tie-break policy. Client timestamps may be retained for telemetry.

### Q4 — Anti-cheat policy
Trajectory heuristics should be treated as risk signals, not proof. False-positive cost is high. Automatic isolation should require multiple independent signals and preferably a review/reversal path.

### Q5 — Simulated players
Bots should not silently impersonate human players in ranked competition. Their use needs an explicit product policy, especially for ratings, rewards and player disclosure.

### Q6 — Cross-language LPI
The proposed LPI needs empirical calibration against actual dictionaries/corpora. A formula based only on mean word length, alphabet size and frequency can create unintended advantages. Validate with simulation before tying rewards to it.

### Q7 — Soft-launch geography
The proposed Poland/Canada/Australia cohort should be treated as a hypothesis. Region selection should consider payment support, language coverage, UA costs, platform review requirements and legal/privacy readiness.

### Q8 — External access to the dev match service
The live M1 service on OCI is loopback-only (127.0.0.1:18080). Exposing it
for device testing requires an edge/TLS decision: public exposure needs TLS
termination, origin allowlisting for the Unity client, and an abuse review of
unauthenticated match creation (rate limiting exists but was sized for a
single developer). Safe default until decided: keep loopback-only and test
via host-local clients (web-m0, headless-bot) or an authorized tunnel.
Recorded 2026-09-10 alongside docs/M1-OPS.md.

**Decision (2026-09-13, owner): OPEN.** The dev match service is exposed
publicly, staged:
1. **Now: temporary tunnel.** Cloudflare quick tunnel on the OCI host
   (infra/expose-quick.sh): TLS terminated at the Cloudflare edge, the
   origin stays loopback-only, no public TCP port, no domain, no account.
   The URL (https://<random>.trycloudflare.com) is ephemeral - restart the
   tunnel to rotate it. This unblocks real-device play and the M1-batch28b
   full-match device loop (emulator reaches the server through the 27B
   endpoint channel).
2. **Later: stable name.** Requires a domain (owner to provide). Named
   Cloudflare tunnel or Caddy + Let's Encrypt on the host; when that
   lands, re-run the abuse review for a permanent URL (per-IP limits at
   the edge, and whether public match creation gets an access token).

Abuse/capacity/rollback notes for the exposure live in
docs/SECURITY-EXPOSURE.md (required by the "Rules for future changes":
security changes document abuse cases, infrastructure changes carry
capacity and rollback notes).

## Rules for future changes

- Gameplay changes require an explicit design note and deterministic test update.
- Protocol changes require compatibility analysis.
- Economy changes require simulation or cohort evidence.
- Security changes require abuse-case documentation.
- Infrastructure changes require capacity and rollback notes.
