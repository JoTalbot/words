# Autonomous Task Protocol

## Task schema

Every autonomous task should be representable with the following fields:

```yaml
id: unique-task-id
title: short objective
owner: logical-agent-role
priority: critical|high|normal|low
complexity: trivial|small|medium|large
inputs:
  - repository paths, interfaces, fixtures, or prerequisites
dependencies:
  - task ids that must complete first
outputs:
  - code, tests, docs, fixtures, or artifacts
acceptance:
  - exact conditions proving completion
validation:
  - commands or automated checks
file_ownership:
  - paths the worker may modify
risk: low|medium|high
escalation: none|review|strong-model|human-blocker
```

## Task lifecycle

```text
QUEUED
  ↓
CLAIMED
  ↓
RUNNING
  ├──→ RETRYING → RUNNING
  ├──→ BLOCKED
  └──→ VALIDATING
             ↓
          REVIEWING
             ↓
        INTEGRATED
             ↓
          VERIFIED
```

A task must not be marked complete merely because a worker produced code. `VERIFIED` requires the acceptance criteria to pass.

## Worker contract

A worker receives:

1. task objective;
2. relevant repository context;
3. explicit file ownership;
4. dependency outputs;
5. acceptance criteria;
6. validation commands;
7. constraints and forbidden changes.

A worker returns:

- status;
- summary of changes;
- files changed;
- tests/checks executed;
- test results;
- commit or patch reference;
- remaining risks;
- recommended follow-up tasks.

## Failure handling

When a worker fails:

1. capture the error and relevant logs;
2. classify the failure as environment, dependency, implementation, test, or specification;
3. retry automatically when the failure is transient or mechanically fixable;
4. narrow the task if it is too large;
5. escalate only when the current model cannot safely resolve it;
6. preserve the failing fixture when useful for regression coverage.

Do not hide failed attempts or weaken tests to make a task appear successful.

## Integration contract

Before integration:

- task acceptance passes;
- changed code builds;
- relevant tests pass;
- no secret or generated local state is introduced;
- public interfaces are checked for compatibility;
- documentation is updated when required.

After integration:

- run the relevant regression suite;
- run broader CI checks;
- update dependent task states;
- create follow-up tasks for discovered defects.

## Priority policy

Prefer work in this order:

1. release-blocking defects;
2. deterministic gameplay correctness;
3. protocol/security correctness;
4. build and CI reliability;
5. automated test coverage;
6. critical infrastructure;
7. playable client functionality;
8. observability and tooling;
9. balancing and polish;
10. nonessential optimization.

Never allow cosmetic work to starve correctness or release-critical work.

## Machine-readable form (enforced in CI since 2026-09-10)

Every file under `agent/tasks/` must load as YAML and carry, at minimum:

| key | rule |
|---|---|
| `id` | string, equal to the file name without `.yml` |
| `title` | string |
| `status` | one snake_case token; qualified states are encouraged (`verified_local_pending_ci`, `validated_local_live_pending`) because they carry the evidence class |
| `acceptance` | non-empty list of strings |
| `validation` | non-empty list of strings |
| `risk` | string |

`tools/check-task-yaml.py` enforces this in the Container-smoke workflow and
prints the status vocabulary it sees, so vocabulary drift stays visible without
being frozen into an enum. `tools/normalize-task-yaml.py` is the repair tool and
is never run by CI.

Why this exists: when the rule was introduced, 13 of 28 task files could not be
parsed at all, and several others had silently become single-entry mappings
because a description contained ": " — for example a validation step written as
`python static Unity script sanity: braces, markers` loaded as
`{'python static Unity script sanity': 'braces, markers'}` instead of the string
it was meant to be. The protocol's guarantee that a session reads acceptance
criteria before starting and writes evidence before finishing was therefore not
machine-checkable for most of the graph.
