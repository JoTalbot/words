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

**Status: DECIDED 2026-09-14 (owner delegated the choice to the agent).**
**Implemented in M2 batch 32D** — see `docs/M2-BOT-POLICY.md` for the design
note, the wire contract and the tests.

The original framing: bots should not silently impersonate human players in
ranked competition; their use needs an explicit policy covering ratings,
rewards and disclosure. The decision resolves it in three clauses, each chosen
because its opposite is a product failure that no later feature can undo:

1. **A bot is DECLARED, never inferred.** A seat is played by a simulated
   player only when the server was told so, explicitly and at the seat level.
   "No profile" is *not* bot-ness: anonymous humans, tooling seats and
   load-test seats all exist and are not bots. Consequence: the server never
   guesses, so it can never mislabel a human as a bot (insulting and
   stats-corrupting) or a bot as a human (the masquerade Q5 forbids).

2. **The declaration is DISCLOSED on every surface, permanently.** A declared
   bot is marked in every snapshot every seat receives (and in deltas, in the
   debug state view, in telemetry, and in the finished result). Disclosure is
   not a client's rendering choice: the client cannot clear the flag, because
   there is no request field that clears it — a declaration only ever *adds* a
   disclosure. Consequence: a player can always know whether they played a
   person.

3. **A match that involved a bot is never rating- or reward-eligible.** The
   authoritative result carries the bot seats, a `bot_present` flag and
   `rating_eligible`. Ratings and rewards do not exist yet (M3), so this is
   the rule made *enforceable now*: the disqualifying fact is in the durable
   row, not in someone's memory of which queue the match came from. The main
   abuse path this closes is farming: a rating that can be gained against a
   bot is not a rating.

Consequences that follow and are NOT open questions:

- **Under-filled lobbies are never topped up with silent opponents.** The
  human-only short-handed start from batch 31C stays the answer to low
  population: a small honest match beats a full dishonest one. The server
  never *creates* a bot to fill a seat.
- **Bots that a client operates must declare themselves.** The QA/device path
  (`headless-bot -partner`, the M1 device smoke) joins through the queue, so
  the declaration is part of that call. This makes an existing flow
  policy-compliant rather than leaving the one real masquerade path in the
  product.
- **Declaring seats is gated per deployment** (`WORDARENA_ALLOW_BOT_SEATS`,
  default off): it is a QA/practice capability, not something an arbitrary
  caller asserts about a match.
- **A bot may not bind a human's profile.** A bot accruing a human's lifetime
  stats is the same abuse in a different shape, and the combination is refused
  rather than half-honoured.

What this decision does NOT settle (and should not be read as settling):
whether a *labelled* bot mode (practice, casual, or an explicitly bot-filled
non-ranked lobby) should exist, whether bots get their own rating pool, and
what a bot's in-match difficulty should be. Those are feature decisions that
the disclosure and eligibility rules above make *safe to take later*.

**Follow-up, same day:** the first of those feature questions was then taken —
the labelled practice mode now exists (batch 32E, `docs/M2-PVE.md`) precisely
because this decision made it safe to take. The mode is a *user* of the three
clauses above and changed none of them. The remaining three (bot rating pool,
matchmaking fallback to bots, difficulty selection) stay open, and nothing in
32E is a precedent for the second one, which Q5 already answered negatively.

### Q12 — Guild identity and membership rules

**Status: the rules below are DECIDED as part of batch 32G
(docs/M2-GUILDS.md); the open questions named at the end are explicitly NOT
decided.**

The question a guild raises is not "what is a guild" but "who is allowed to say
they are someone". Guilds are the first feature that needs a durable identity,
and the server had none: profiles were anonymous and any caller could act as any
player. The foundations decided here:

1. **Guild mutations authenticate with a per-profile owner token**, returned
   once at profile creation and stored only as a hash. A caller never supplies a
   player id, so impersonation is impossible by construction.
2. **One guild per player**, enforced by a unique index rather than a check.
3. **Names and tags are unique case-insensitively**, 3–24 and 2–5 characters,
   with the same character policy as nicknames.
4. **A guild always has exactly one owner and never exists without members**:
   ownership passes to the earliest-joined member if the owner leaves, and the
   guild is dissolved when the last member leaves. A headless guild — or one
   squatting on a name — is not a state the model allows.
5. **Rosters are public; mutations are not.** Reading a guild needs no token.

Deliberately left open (each needs product input, none blocks the foundation):
guild chat and its moderation obligations, invite-only join and join requests,
co-owners, the roster cap number (50 is a placeholder), whether guilds ever
affect matchmaking, and whether the owner token grows into the real M2 identity
work or is replaced by sessions.

### Q11 — What the first practice mode is

**Status: DECIDED 2026-09-14 (settled as part of batch 32E; the product choice
was to ship the smallest useful thing and let evidence size the rest).**

The question is what "first PvE content" means, now that a labelled bot is
policy-safe. Options were: a tutorial with scripted lessons, a difficulty-ladder
practice mode, or a plain practice match against a server-driven opponent.

Decided: **a plain 1v1 practice match**, because it is the only one of the three
that fixes a blocking problem rather than adding scope — today a player cannot
play the game alone at all, with no second client and no second human. The
design follows from three constraints rather than from taste:

1. **The opponent is a client of the match, not part of it.** It submits through
   the ordinary validated path and holds no privileges, so practice cannot
   diverge from the rules (and a bug in it costs a word, not a board state).
2. **It is polite by default**: 3–4 letter words, 1.5 s between them, never
   steals. The first content teaches claiming before defending; a difficulty
   ladder is a later feature with playtest evidence, not a guess now.
3. **It is deterministic**, so a practice match replays and debugs like any
   other match — which is what lets the mode be tested at all.

Explicitly *not* decided here: difficulty levels, rewards for practice, whether
practice is ever reachable from the ranked path (Q5 clause 3 says such a match
stays non-rating-eligible either way), and tutorial framing. Nothing is blocked
on them, and the mode is behind `WORDARENA_ALLOW_BOT_SEATS`, off by default.


### Q6 — Cross-language LPI
The proposed LPI needs empirical calibration against actual dictionaries/corpora. A formula based only on mean word length, alphabet size and frequency can create unintended advantages. Validate with simulation before tying rewards to it.

### B2 — uk dictionary licence (owner item, 2026-09-14)

The owner confirmed the uk dictionary licence exists and will be provided
later. Recorded so it stops being tracked as a blocker for M2/M3: the
development snapshot in `dictionary/` stays as-is (it is canonical-locked by
test, so no code depends on its size), and swapping in the licensed corpus is a
data update that the versioned-snapshot design (PD-004) already supports. No
engineering work is waiting on it; the only rule is that the licensed data must
not be committed before it arrives, which is already true.


### Session-13 safe-default record and session-14 B2 conflict (2026-09-19)

Commits `6606716` and `5663d0f` record owner approval of the seven-item
safe-default bundle. Keep shipped rules, region, guild/practice scope and
stage-1 exposure unchanged; do not infer new launch or enforcement authority.
The two factual errors in the consolidation are corrected by primary
contracts: the shipped steal debit is **100%**, not 50%; Q6 is cross-language
LPI calibration before rewards, not cross-match identity maturity.

**Unresolved B2 scope:** the same record says both "uk fully disabled" and
"no publication; no code/config changes". PD-008 and the B2 paragraph above
still permit the development snapshot; the actual API admits uk and
`snapshot.go` embeds its data (isolated main-build probe: HTTP 201, uk).
Those are different release scopes. Until the owner clarifies publication
hold versus release-specific full exclusion, keep development fixtures/live
configuration unchanged, publish no new uk content, and block the affected
release decision. See `docs/OWNER-GATE-DRAFT.md`. A runtime flag alone would
not remove the embedded corpus from distributed binaries.

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
**Status: DECIDED 2026-09-14 (owner delegated the choice to the agent).
Implemented in M2 batch 31A.**

**Decision: per-wave cull with a survivor share of two thirds, floored, with
ties resolved in the players' favour.** At every wave boundary the lowest
scorers are eliminated until two thirds of the still-active seats remain
(`SurvivorsNumerator/SurvivorsDenominator`), never below `MinSurvivors` (2).
A 60-seat lobby therefore goes 60 -> 40 -> 26 across the three waves.

Why this over the alternatives:
- **Pure scoring** (no elimination) was rejected: a player who falls behind in
  wave 1 has no reason to keep playing for another 100 seconds, and the mode
  would be indistinguishable from 60 people playing solitaire on one board.
- **A score floor** was rejected because the threshold has to be retuned per
  language and per dictionary - the same number means different things in `en`
  and `uk` - which makes it a balancing liability rather than a rule.
- A **share-based cull** needs no tuning constant that depends on content, and
  the tension curve is the same in every language.

Guarantees that fall out of the implementation:
- **Rosters below `EliminationMinSeats` (4) never cull**, so 1v1 and small
  lobbies are exactly what M0/M1 shipped.
- **Deterministic and replayable**: the cut is by score descending, ties by
  seat index, computed only from logged state at a wave boundary, and
  `EliminatedAtWave` is folded into `Match.Fingerprint` so a replay that culled
  a different set cannot compare equal.
- **A tie across the cut line keeps everyone**: losing a Royale on your seat
  number would be indefensible, so the roster shrinks more slowly instead.
- **An eliminated seat becomes a spectator**: its intents are still logged
  (the replay sees them) but are rejected with `blocked_by_rule` and cannot
  mutate competitive state.

### Q10 — Board sizing for a large roster
**Status: DECIDED 2026-09-14 (owner delegated the choice to the agent).
Implemented in M2 batch 31A.**

**Decision: the board scales with the roster.** `match.BoardCells(seats)`
returns `CellsPerWave` (12) for two seats and otherwise
`seats * BoardCellsPerSeat` (2 cells per player) rounded up to whole rows of
`BoardColumnsLarge` (6), capped at `MaxCellsPerWave` (60). A full 60-seat lobby
plays a 6x10 board - one cell per player, which is the intended Royale
contention - and a 1v1 match keeps the exact 4x3 M0 board.

Constraints this respects:
- **Wave generation stays a pure function of `(seed, language, wave)`.** The
  letter at cell *i* does not depend on the roster: a bigger board is a
  prefix-compatible extension of a smaller one, drawn from the same shuffle.
  So a 1v1 board is bit-identical to M0/M1 forever, and a client can render
  before the lobby has filled. (Pinned by
  `TestBoardGenerationIsPrefixStableAcrossRosters`; this replaces the older
  "identical board at every roster" rule, which Q10 deliberately retires.)
- **Grid width is part of the board contract, not a rendering detail.** The
  server exposes `match.BoardColumns(seats)` (4 small / 6 large) and the Unity
  client derives the same value from the cell count, because the 8-way
  adjacency gesture rules and the renderer must agree or swipes would not
  match what the player sees.
- **`MaxPathCells` stays 12.** A longer board does not mean longer words; the
  cap is about word length, not geometry.
- **Fan-out remains the real cost** (docs/LOAD-BASELINE.md, batch 30F): a
  bigger board multiplies snapshot bytes on top of the roster factor, which is
  why delta or interest-scoped snapshots are the next Royale work item.

## Rules for future changes

- Gameplay changes require an explicit design note and deterministic test update.
- Protocol changes require compatibility analysis.
- Economy changes require simulation or cohort evidence.
- Security changes require abuse-case documentation.
- Infrastructure changes require capacity and rollback notes.
