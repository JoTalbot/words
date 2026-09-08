# Autonomous Agent Operating Mode

## 1. Purpose

This project is designed to be developed by an orchestrated fleet of agents running on a remote development server. A controller such as Arena acts as the coordinator; local LLMs perform bounded implementation and QA tasks; stronger agents handle architecture, integration, security, and difficult debugging.

The goal is not merely to automate individual commands. The goal is a closed development loop that can plan, execute, validate, repair, integrate, and repeat with no routine human intervention.

## 2. Runtime model

```text
                    Arena / Orchestrator
                           |
                task graph + priorities
                           |
        +------------------+------------------+
        |                  |                  |
   Strong agents       Local LLM pool      QA agents
        |                  |                  |
        +------------------+------------------+
                           |
                     Git worktrees
                           |
                    Build / Test / CI
                           |
       +-------------------+-------------------+
       |                   |                   |
   Game server         Android emulator     Browser E2E
       |                   |                   |
       +-------------------+-------------------+
                           |
                    Review + integrate
                           |
                     Release gates
```

## 3. Agent roles

Recommended logical roles:

- **Orchestrator** — owns the task graph, dependencies, priorities, retries, and integration.
- **Architect** — maintains architecture, contracts, boundaries, and ADR-style decisions.
- **Gameplay** — implements deterministic match rules, scoring, board logic, and replayability.
- **Protocol** — owns Protobuf schemas, compatibility, sequencing, snapshots, and transport behavior.
- **Backend** — owns the authoritative match server and supporting services.
- **Dictionary** — owns normalization, lexical validation, versions, DAWG/index structures, and language data.
- **Client** — owns Unity/client implementation once the client baseline is established.
- **Android QA** — runs the application in an emulator, uses ADB and vision/screenshot tooling, reproduces UI failures, and validates fixes.
- **Browser QA** — runs permitted browser automation against project-controlled web interfaces and test environments.
- **Infra** — owns containers, CI, deployment, observability, and reproducible environments.
- **QA/Test** — owns test strategy, fuzzing, fault injection, deterministic replay, regression coverage, and release gates.
- **Security/Anti-cheat** — reviews trust boundaries, abuse resistance, telemetry, and false-positive handling.
- **Product/Balancing** — turns unresolved gameplay/product questions into explicit decisions and measurable acceptance criteria.

Roles are logical responsibilities, not necessarily separate machines or models.

## 4. Task sizing

Prefer tasks that a local model can finish in one bounded session. A good task normally has:

- one clear objective;
- a small file ownership set;
- explicit dependencies;
- an exact acceptance test;
- a rollback-safe change;
- no hidden product decisions.

Bad task: `Implement the whole multiplayer backend.`

Good tasks:

- `Add server-side monotonic intent sequence to MatchState.`
- `Add replay fixture for two simultaneous word submissions.`
- `Add property test proving the same replay produces the same score.`
- `Add reconnect-token expiry validation.`

## 5. Parallel execution

Tasks may execute concurrently when their dependency graph permits it.

Rules:

1. Never allow two agents to edit the same file concurrently.
2. Prefer separate Git branches/worktrees for concurrent implementation.
3. Keep interfaces stable while dependent work is running.
4. Integrate small changes frequently.
5. Run the affected test suite before integration.
6. Run broader regression tests after integration.

If parallel work conflicts, the orchestrator resolves the conflict. Do not leave competing implementations silently merged.

## 6. Local LLM delegation

Use the least expensive model that can reliably complete the task.

Suitable local-model tasks:

- small code changes;
- test generation;
- fixture generation;
- documentation updates;
- mechanical refactors;
- log analysis;
- lint/build repair;
- repetitive migration work.

Escalate to a stronger model for:

- protocol or architecture changes;
- security-sensitive code;
- deterministic simulation semantics;
- complex concurrency bugs;
- ambiguous product decisions;
- cross-component integration failures.

A local model's output is never trusted merely because the model completed the task. Tests and review determine correctness.

## 7. Continuous autonomous loop

The orchestrator should repeatedly execute:

1. inspect repository state;
2. inspect open tasks and release gate;
3. inspect available runtime/tooling;
4. construct or update the dependency graph;
5. select the highest-value unblocked tasks;
6. dispatch independent tasks to available agents;
7. collect results and artifacts;
8. run tests and static checks;
9. review risky changes;
10. integrate passing work;
11. deploy to an isolated development environment;
12. run Android/browser/load/replay tests where relevant;
13. repair failures automatically;
14. update documentation and state;
15. repeat.

Do not stop merely because one task failed. Stop only for a genuine blocker under the human intervention policy in `AGENTS.md`.

## 8. Android autonomous QA

When an Android build exists:

- start or reuse an isolated emulator;
- install the build using ADB;
- launch the application;
- collect logcat and application diagnostics;
- execute scripted UI flows;
- capture screenshots/video where useful;
- use vision-capable tooling to inspect layout and state;
- reproduce failures;
- patch the code;
- rebuild and reinstall;
- rerun the failing flow and regression suite.

The emulator is a test environment, not a substitute for deterministic automated tests.

## 9. Browser autonomous QA

Use Playwright or an equivalent installed browser automation stack when available. Automate project-owned dashboards, web clients, admin tools, APIs, documentation sites, and test environments.

Browser automation should cover:

- smoke flows;
- authentication in test environments;
- gameplay UI where a web client exists;
- network failure/reconnect flows;
- accessibility and basic layout assertions;
- screenshots on regression-sensitive screens.

Do not bypass authentication, anti-bot controls, rate limits, or access restrictions on third-party services.

## 10. Release discipline

Every release candidate must have:

- reproducible build instructions;
- passing unit/integration tests;
- deterministic gameplay/replay checks;
- protocol compatibility checks;
- security checks appropriate to the release;
- client smoke tests when a client exists;
- Android emulator smoke tests for Android builds;
- browser E2E where applicable;
- CI validation;
- updated release notes and documentation.

The release process should be automated as far as credentials and external approvals permit.

## 11. State and reporting

The orchestrator should maintain machine-readable task state outside source code when possible. Repository documentation should contain only durable project state and decisions, not transient logs.

Each completed task should leave enough evidence to answer:

- what changed;
- why it changed;
- which tests ran;
- whether they passed;
- which commit contains the change;
- what remains blocked.
