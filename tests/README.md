# Test data assets

Executable Go tests live next to the code under `server/`. This directory holds
the **data assets** those tests consume or that document how they are produced,
as required by `docs/M0.md` §Required test assets.

## Layout

| Directory | Contents | Consumed by |
|---|---|---|
| `fixtures/` | Hand-written deterministic match/replay fixtures | `server/...` replay tests, `server/cmd/headless-bot`, operator runbooks |
| `golden/` | Expected scoring and state-transition vectors, and how to re-record them | `server/internal/scoring`, `server/internal/match` |
| `network/` | Latency / loss / reordering profiles for the fault matrix | `server/cmd/game/netsim_test.go` |
| `load/` | Synthetic client and action-distribution parameters | `server/internal/match/bench_test.go` |

`tools/check-test-assets.py` parses the Go sources and fails when an asset in
`network/` or `load/` drifts from the constants the tests actually use.

## Conventions

- Every asset is deterministic: fixed seeds, no wall-clock values, no host-specific results.
- No secrets, credentials, tokens or production data may be committed here.
- No generated build output, coverage data or fuzz corpora are committed here; generated fuzz corpora intentionally stay out of the repository so CI coverage remains deterministic and repository size bounded.
- Machine-readable formats are preferred (JSON) so assets can be validated by tooling instead of reviewed by eye.
- Fixtures stay small. Prefer parameters that regenerate a scenario over recording a large artefact.
- Measured results are documentation, not fixtures: they belong in `docs/` (for example `docs/LOAD-BASELINE.md`), never in this tree.
