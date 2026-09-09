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

## M0 snapshot location and compiler

M0 snapshot artifacts live at `server/internal/dictionary/data/` (embedded
into the server binary via `go:embed`):

- `en.words`, `ru.words`, `uk.words` — canonical compiled word lists;
- `manifest.json` — versioned manifest with per-file sha256 and counts.

The M0 compiler is `server/cmd/dictcompile` (Go, deterministic pipeline:
trim -> per-language normalization (lowercase, alphabet filter, ru ё->е) ->
length filter 3..9 -> dedupe -> byte-wise sort -> one word per line).
Usage:

```bash
cd server
go run ./cmd/dictcompile -lang en -src internal/dictionary/data/en.words \
    -out internal/dictionary/data/en.words      # canonical? (verify-only)
go run ./cmd/dictcompile -lang ru -src /tmp/upstream-ru.txt \
    -out internal/dictionary/data/ru.words -write -version 2026-09-08.v2
```

Tests in `cmd/dictcompile` lock reproducibility: `TestShippedSnapshotsCanonical`
recompiles every shipped artifact and enforces byte-identical output plus
manifest sha256/counts, so hand edits of data files now fail CI.

The full graph-compiler pipeline below (DAWG artifacts, dynamic tombstones)
supersedes the flat text format later; the runtime loader
(`internal/dictionary.LoadSnapshotFromReader`) already accepts any
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
