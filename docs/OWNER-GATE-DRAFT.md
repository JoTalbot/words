# Owner-Gate Draft — Session 13 (2026-09-18)

DECISION RECORDED 2026-09-18: OWNER APPROVED SAFE-DEFAULT BUNDLE for all 7 items.
No autonomous product decision made before this point; after approval, safe defaults stay as documented.

Consolidation of the seven owner-blocked items from docs/ROADMAP.md / GitHub #1.
No autonomous product decision is made; safe defaults remain shipped.
Evidence references are local to this repo; no new secrets or credentials.

## Questions (safe default = shipped status quo)

1. Q8 — Stage-2 domain / /metrics deny / token decision
   - Evidence: docs/SECURITY-EXPOSURE.md (batch 37C, PR #59 -> 23e4a03); live config trustProxy=OFF; /metrics deny not yet deployed; token not rotated.
   - Safe default: keep live as-is (trustProxy off, stage-2 deploy deferred, token unchanged); do NOT flip on production without owner sign-off.

2. B2 — Ukrainian (uk) dictionary publication
   - Evidence: docs/M3-DICT-HOTFIX.md; dictcompile supports -parent/-add/-remove deterministically; uk publication requires B2 licence review.
   - Safe default: do NOT publish uk snapshot; en/ru remain shipped; hotfix tooling stays available.

3. Q1 / Q2 — Prototype constants (lock duration / steal debit / board size / difficulty presets)
   - Evidence: docs/M2-ANTI-SNOWBALL.md; batch 32B/35B; defaults byte-for-byte shipped rules; measurement shows softening debit accelerates snowball (NEGATIVE).
   - Safe default: keep all shipped constants (lock 3 s, debit 50%, 12-cell board default, difficulty presets as shipped).

4. Q7 — Soft-launch geography
   - Evidence: docs/M1-OPS.md; current tunnel URL ephemeral (trycloudflare); live service at 129.213.177.56.
   - Safe default: keep single-region deployment; do NOT expand geography until owner approves.

5. Q6 — LPI (League / Progression / Identity maturity)
   - Evidence: docs/M2-PVE.md; cross-match aggregation stays parked on identity maturity.
   - Safe default: do NOT enable cross-match aggregation; identity stays anonymous per-match.

6. Q11 — PvE tutorial framing
   - Evidence: docs/M2-PVE.md; difficulty selection shipped (35B); framing open by name only.
   - Safe default: keep shipped difficulty names (easy/normal/hard); do NOT change framing without UX evidence.

7. Q12 — Guild product inputs (chat/moderation, invite-only, co-owners, cap, guild-vs-guild, rewards)
   - Evidence: docs/M2-GUILDS.md (if present); all six items evaluated session 7; safe default = status quo for each.
   - Safe default: single owner, open-join foundation, cap 50 placeholder, guilds never affect matchmaking, owner token grows later; consolidate into ONE owner question at gate.

## Action recommendation (NOT executed autonomously)

- Owner should review this draft and provide ONE consolidated response per item, or approve the safe-default bundle as-is.
- Engineering does NOT need answers to continue M3 measurement or maintain live service.
- If owner approves safe defaults, the release gate is satisfied for engineering and only cosmetic/monetization/enforcement work remains.
DECISION LOCKED 2026-09-18: Safe bundle (all 7) + B2 uk fully disabled (no publication). No code/config changes.
