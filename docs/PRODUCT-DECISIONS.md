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

### Q9 — Royale elimination rule
**Status: OPEN — blocks M2 Royale gameplay. Engineering is ready and waiting.**
As of 2026-09-14 the whole server stack (simulation, room, matchmaker,
transport) is seat-count agnostic and verified at up to 60 seats, but
`PlayerView.IsEliminated` is always false because nobody has decided what
elimination means. The plausible shapes, with their consequences:
1. **No elimination, pure scoring.** Simplest, already works today; every
   player plays the full three waves and rank is by score. Risk: a player who
   falls behind early has no reason to stay for 100 seconds.
2. **Per-wave last-place cull.** Classic battle-royale tension; needs a rule
   for ties at the cut line and a spectator mode for the culled, otherwise
   most of the lobby is staring at a dead screen for two thirds of the match.
3. **Score floor per wave.** Players below a threshold drop out; keeps
   agency (you know the number you must hit) but the threshold has to be
   tuned per language, because the dictionary changes how scoreable a board is.
Whichever is chosen must stay deterministic and replayable: elimination has to
be a function of logged state at a tick, since replay equality is an existing
invariant (`Match.Fingerprint`, roster replay tests).

### Q10 — Board sizing for a large roster
**Status: OPEN — blocks M2 Royale gameplay.**
`CellsPerWave` is 12 (a 4-column grid), sized for two players. Sixty players
contesting twelve cells is not a game. The decision is what the board becomes
as the roster grows: a fixed larger board, a board that scales with the
roster, or per-player/regional sub-boards. Constraints from the code and from
measurement (docs/LOAD-BASELINE.md, M2 batch 30F):
- Wave generation must stay deterministic per `(seed, language, wave)` and
  must NOT depend on the roster - a test now pins that boards are identical
  at 2..60 seats, because a client renders the board before the lobby fills.
- Tick cost does not scale with the roster (tens of ns, board-dominated), so
  a larger board is affordable; snapshot cost does scale, ~3x from 2 to 60
  seats, and per-subscriber fan-out is the real constraint: 60 subscribers
  each receiving a 60-player snapshot is ~30x the bytes of a 1v1. A bigger
  board multiplies that again, which is an argument for delta or
  interest-scoped snapshots as part of whatever sizing is chosen.
- `MaxPathCells` is 12 and adjacency is 8-way; both are board-shape
  assumptions that a resize has to revisit.


## Rules for future changes

- Gameplay changes require an explicit design note and deterministic test update.
- Protocol changes require compatibility analysis.
- Economy changes require simulation or cohort evidence.
- Security changes require abuse-case documentation.
- Infrastructure changes require capacity and rollback notes.
