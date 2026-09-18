# M3 — Dictionary hotfix pipeline

## License and policy gates

The DICTIONARY DATA currently shipped is versioned and canonical-locked by
test, but its *distribution* is owner-gated the same as every other M3
content decision. No hotfix, add or remove is applied or published without
the right authorisation despite the tooling existing:

- **uk** requires the owner-provided licence before it may ship to players
  (B2 in `docs/PRODUCT-DECISIONS.md`). Hotfixing a pre-production snapshot
  ahead of that licence is not permitted by the security posture in effect;
  the snapshot is pre-production for exactly this reason.
- **en / ru** may gain a hotfix the moment a real blocker appears, but a
  blocker has not yet been named, and the "define the smallest version of
  the loop before optimising" rule means the pipeline is built on demand,
  not ahead of a problem.

The rest of this document describes the deterministic hotfix tooling that
exists and the audit guarantees the uncontentious half can carry.

## Tooling built (2026-09-18, batch 43)

`server/cmd/dictcompile` gains a hotfix mode on top of the full-compile
pipeline:

```bash
# full rebuild from an upstream wordlist (existing, unchanged):
go run ./cmd/dictcompile -lang en -src /tmp/upstream.txt -out en.words -write

# hotfix on top of the previous artifact (new):
go run ./cmd/dictcompile -lang en \
    -parent internal/dictionary/data/en.words \
    -add /tmp/add.txt -remove /tmp/remove.txt \
    -out internal/dictionary/data/en.words -write -version 2026-09-18.hf1
```

The hotfix path:

1. loads the parent artifact, dropping the canonical `#` header comment; the
   parent's data lines are the base set (they are already sorted/normalized);
2. removes the normalized forms of every line in `-remove` (lines that are
   not even alphabet-clean are no-ops and reported as ignored);
3. adds the normalized, length-filtered, deduped forms of every line in
   `-add`;
4. re-sorts the set and renders the canonical byte form.

Determinism is pinned by `TestApplyHotfixDeterministicAndAuditable` and
`TestApplyHotfixEmptyDeltasReplicateParentData` in `main_test.go`: identical
inputs rebuild a byte-identical artifact, and an empty delta reproduces the
parent's data lines exactly.

## Provenance (auditability)

The printed manifest entry carries four extra fields in hotfix mode:
`parent_sha256` (the parent artifact's digest), `added`, `removed`, and the
`version` label. A versioned artifact must state what it was built from and
over, or it cannot be audited — the same rule as the runtime snapshot's
version pinning (`dictionary.SnapshotVersion` in `snapshot.go`).

## What is still owner-gated

- Actually *publishing* an en/ru hotfix (the content decision plus its
  game-balance review).
- Shipping the uk snapshot at all (B2 licence), hotfixed or not.
- Any change to the length filters (`-min/-max`) used by the hotfix, since
  that changes what counts as a playable word and therefore board fairness.

The tooling cannot and does not publish: it writes a file and prints the
manifest entry; the rest of the deploy path is the same as every snapshot
change and is not automated away here.
