# Golden vectors

Golden vectors pin the deterministic behaviour that the competitive core
depends on: board generation, scoring, and the state transitions of a claim,
lock and cross-steal.

## Where the vectors live

The executable golden tests live next to the code, because they must compile
against the unexported functions they pin:

| Behaviour | Test | Package |
|---|---|---|
| Deterministic board generation | `prng_test.go`, `match_test.go` | `server/internal/prng`, `server/internal/match` |
| Deterministic scoring (base points, combo, steal) | `scoring_test.go` | `server/internal/scoring` |
| Wave / claim / lock / steal transitions | `match_test.go` | `server/internal/match` |
| Protocol byte stability and round-trip | `convert_test.go`, `fuzz_test.go` | `server/internal/protocol` |

This directory holds the *description* of each vector family and the procedure
for re-recording them, so a reviewer can tell a legitimate update from a
silently weakened assertion.

## Rules for changing a golden vector

1. A golden value may only change when the game rules change deliberately.
2. Every change must be accompanied by a design note (`docs/M0-MATCH-RULES.md`
   or a new decision in `docs/PRODUCT-DECISIONS.md`) and by the reason recorded
   in the commit message.
3. Re-record, never hand-edit. Prefer regenerating the expected value from the
   implementation only after the implementation change has been justified.
4. Seeds are part of the contract. Changing a seed is a change of the vector.
5. If a golden test fails after an unrelated change, treat it as a
   determinism regression and fix the cause; do not update the expectation to
   make the suite green.

## Re-recording

Golden values in this project are asserted inline in Go tests rather than in
data files, so "re-recording" means updating the constant in the test that
declares it. Run the full suite afterwards:

```bash
cd server && go test -count=1 ./...
```

The suite must be green on a clean checkout before the change is integrated.
