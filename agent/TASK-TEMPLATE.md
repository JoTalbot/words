# Autonomous Task Template

```yaml
id: replace-me
title: replace-me
owner: replace-me
priority: normal
complexity: small
inputs:
  - replace-me
dependencies: []
outputs:
  - replace-me
acceptance:
  - replace-me
validation:
  - replace-me
file_ownership:
  - replace-me
risk: low
escalation: none
```

## Worker prompt

Implement only the task above.

Rules:

- inspect the existing code before changing it;
- do not modify files outside `file_ownership` unless the task explicitly permits it;
- preserve existing contracts;
- add or update tests for changed behavior;
- do not weaken or delete tests to obtain a passing result;
- run the listed validation commands;
- report failures honestly;
- leave the worktree clean except for intended changes;
- never add credentials, secrets, or machine-local state.

## Completion report

```text
STATUS: VERIFIED | FAILED | BLOCKED

SUMMARY:

FILES:

TESTS:

RESULTS:

RISKS:

FOLLOW-UP:
```
