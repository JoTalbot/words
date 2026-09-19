# Owner decisions — safe-default bundle and B2 clarification

Session 13 (commits `6606716`, `5663d0f`, 2026-09-18) records owner approval
of the seven-item safe-default bundle. This is retained, not re-requested.
It does **not** implement unbuilt M3/M4/M5 features or authorize production
release. Session 14 corrected the factual errors below against primary
contracts and code; no gameplay or live configuration changed.

| Item | Recorded safe default | Evidence / scope |
|---|---|---|
| Q8 | Keep stage-1 dev tunnel; defer permanent domain, proxy-trust switch and edge policy | `docs/SECURITY-EXPOSURE.md`; no new production exposure authorized |
| B2 | Do not publish the unreviewed uk snapshot | `dictionary/NOTICE.md`, `docs/PRODUCT-DECISIONS.md` B2; see unresolved scope below |
| Q1/Q2 | Keep shipped prototype constants | `DefaultBoardParams()` in `server/internal/match/boardlevers.go`: 90 ticks at 30 Hz (3 s), **100%** steal debit, 12-cell duel; Royale board scales under Q10. The earlier draft's 50% was wrong; no 50% rule was approved by this correction |
| Q7 | Single-region development; no geography expansion | Soft-launch geography remains a future launch decision, not an engineering hold |
| Q6 | No competitive rewards tied to uncalibrated cross-language LPI | Q6 in the primary register is about word length, alphabet size and frequency, **not** League/Progression/Identity. Cross-match identity aggregation remains separately parked |
| Q11 | Shipped practice mode / easy, normal, hard remain unchanged | `docs/M2-PVE.md`; no new tutorial framing without evidence |
| Q12 | Existing guild foundation remains unchanged | Single owner, open join, placeholder cap 50, no guild matchmaking/rewards/chat; `docs/M2-GUILDS.md` |

## B2 is the one release-scope clarification still needed

`5663d0f` says both **“uk fully disabled”** and **“no publication; no code/config
changes”**. Those are not technically equivalent. Primary PD-008 still requires
en/ru/uk for development, and B2 permits the pre-production fixture until a
licensed corpus arrives. The API still admits uk match/queue/profile requests;
`server/internal/dictionary/snapshot.go` embeds the entire `data` directory.
Session 14's isolated same-main build returned **HTTP 201, language=uk** when
creating a match. No such probe was made against the live database.

Before changing language availability or declaring an artifact releasable,
confirm which scope the owner intended:

1. **Publication hold only:** retain uk in development/testing, do not publish
   its snapshot or make a production-release claim without the licence review.
2. **Full disablement for release:** add a reviewed release-specific exclusion
   of uk dictionary data, runtime admission and client selection, while retaining
   clearly separated internal fixtures. A runtime flag alone would not remove
   the embedded data from a binary.

Until clarified, the affected release lane is **blocked**. Keep the existing
development service and fixtures unchanged, publish no new uk content, and do
not silently resolve the contradictory wording by changing competitive rules.
This does not block unrelated engineering or revoke the other safe defaults.
