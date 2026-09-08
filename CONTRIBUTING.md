# Contributing

## Workflow

Use short-lived branches from `main`:

```text
feat/<area>-<short-name>
fix/<area>-<short-name>
chore/<area>-<short-name>
```

Keep commits small and explain intent rather than implementation trivia.

## Pull requests

A PR should normally contain:

- a clear problem statement;
- implementation summary;
- tests or an explicit reason why tests are unnecessary;
- telemetry/observability impact;
- migration and rollback notes when relevant.

Gameplay, protocol, economy and anti-cheat changes should include the corresponding design or decision reference under `docs/`.

## Quality gates

Before merge:

- formatting and static analysis pass;
- unit tests pass;
- deterministic gameplay/golden tests pass for simulation changes;
- protocol compatibility is checked for `.proto` changes;
- no secrets or generated production artifacts are committed.

## Design changes

Do not silently alter gameplay behavior while fixing an implementation bug. Record material behavior changes in `docs/PRODUCT-DECISIONS.md` and update acceptance tests.
