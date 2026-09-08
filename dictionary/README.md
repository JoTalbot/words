# Dictionary Pipeline

Dictionary content is versioned data and is never hard-coded into the match
rules.

## Pipeline

```text
Source word lists / corpora
        |
 normalization + policy
        |
 lexical metadata
        |
 deterministic compiler
        |
 immutable DAWG artifact
        |
 version manifest + checksums
        |
 runtime validator
        +
 dynamic whitelist / tombstones
```

## M0 snapshot location

While the dictionary compiler does not exist yet, M0 fixture snapshots live
at `server/internal/dictionary/data/` (embedded into the server binary via
`go:embed`):

- `en.words`, `ru.words`, `uk.words` — curated fixture word lists (small but
  real; adequate to prove the deterministic core);
- `manifest.json` — versioned manifest with per-file sha256; tests enforce
  the checksums, so data edits must refresh the manifest deliberately.

The full compiler pipeline below supersedes these snapshots; the runtime
loader (`internal/dictionary.LoadSnapshotFromReader`) already accepts any
deterministic source format that follows the normalization rules.

## Requirements

- Unicode normalization rules are explicit per language.
- Inflection, capitalization, diacritics and language-specific rules are
  modeled before compilation.
- Every artifact has a language, version, source revision, compiler version
  and checksum.
- Blacklist/tombstone entries override all positive dictionary sources.
- Hotfixes are auditable and reversible.
- Competitive matches pin a dictionary version for the lifetime of the match.

## Benchmarking

Benchmark raw in-process lookup separately from network or serialization
overhead. Do not publish a sub-microsecond claim until it is demonstrated on
representative production hardware and realistic word distributions.

## Planned tooling

```text
dictionary/
├── cmd/compiler/          # source -> artifact compiler
├── cmd/inspect/           # artifact diagnostics
├── internal/normalize/    # language normalization rules
├── internal/dawg/         # graph compiler/runtime
├── manifests/             # versioned language metadata
└── fixtures/              # deterministic test dictionaries
```
