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
| PD-006 | M0 match server starts in Go | Proposed | Keeps the first production path simple; optimize only after profiling |

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

## Rules for future changes

- Gameplay changes require an explicit design note and deterministic test update.
- Protocol changes require compatibility analysis.
- Economy changes require simulation or cohort evidence.
- Security changes require abuse-case documentation.
- Infrastructure changes require capacity and rollback notes.
