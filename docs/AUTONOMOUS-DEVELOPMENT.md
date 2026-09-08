# Autonomous Development Factory

## Objective

Build and operate Word Arena as a continuously progressing software project. Human involvement is reserved for genuine blockers; routine coding, testing, debugging, emulator interaction, browser testing, repository maintenance, and release preparation are automated.

## Controller responsibilities

The Arena controller is responsible for:

- discovering the environment;
- reading the repository contracts;
- maintaining the task graph;
- assigning tasks to the smallest capable worker;
- enforcing file ownership and dependency boundaries;
- collecting and validating results;
- coordinating retries and escalation;
- integrating verified work;
- keeping release gates current;
- producing concise progress state.

## Environment discovery checklist

At startup, inspect rather than assume:

- OS, kernel, architecture, CPU, RAM, disk;
- Git version and repository status/remotes/branches;
- Go, Rust, C/C++, Python, Node.js versions;
- Docker and Docker Compose;
- Kubernetes and Agones if installed;
- Unity Editor/CLI if installed and the project has been added;
- Android SDK, ADB, emulator/AVD availability;
- browser binaries and Playwright or equivalent;
- local LLM runners and CLIs, including Hermes CLI, Ollama, llama.cpp, vLLM, and alternatives;
- test, lint, coverage, profiling, and load-testing tools;
- available ports, services, and isolated work directories.

Do not install heavyweight infrastructure merely because it appears in the long-term architecture. Install or activate it when required by the current milestone and when the environment supports it reproducibly.

## Agent factory pattern

Use a small-worker strategy:

```text
Specification
    ↓
Orchestrator
    ↓
Task graph
    ├── protocol ────────┐
    ├── gameplay ────────┤
    ├── dictionary ──────┤
    ├── backend ─────────┤
    ├── QA ───────────────┤──→ Integration
    ├── infra/CI ─────────┤
    ├── Android QA ───────┤
    └── browser QA ───────┘
                 ↓
          Release gates
```

Parallel workers should operate on independent files or isolated worktrees. The orchestrator owns integration.

## Continuous execution

The system should remain productive while there are unblocked tasks. After a task completes, immediately unlock dependent tasks and fill available worker capacity with the next highest-priority work.

When no implementation task is ready, generate useful work from:

- uncovered acceptance criteria;
- missing tests;
- flaky tests;
- security gaps;
- performance measurements;
- documentation drift;
- emulator/browser regressions;
- CI failures;
- release readiness gaps.

Do not manufacture pointless work merely to keep agents busy.

## Simple-model work queue

The orchestrator should actively identify tasks suitable for local low-cost models. Examples include:

- adding focused unit tests;
- creating fixtures;
- implementing small pure functions;
- updating generated documentation;
- fixing straightforward compiler errors;
- converting repetitive code patterns;
- analyzing logs and proposing a bounded fix;
- adding assertions around existing behavior.

Before dispatching a task, provide the model with only the context it needs. Afterward, validate mechanically.

## Strong-model queue

Reserve stronger reasoning capacity for:

- architecture;
- game-rule semantics;
- distributed-state correctness;
- protocol evolution;
- security/anti-cheat;
- concurrency;
- performance bottlenecks;
- cross-platform integration;
- ambiguous failures.

## Autonomous test matrix

As components become available, automate:

| Area | Required automation |
|---|---|
| Gameplay | deterministic simulations, replay tests, property tests |
| Protocol | schema validation, compatibility tests, malformed-input tests |
| Server | unit/integration tests, race checks, load tests |
| Network | latency, loss, duplication, reordering, reconnect tests |
| Dictionary | normalization, versioning, lexical edge cases |
| Security | authorization, replay, tampering, rate limits, fuzzing |
| Android | build/install/launch, ADB flows, screenshots, vision checks |
| Browser | Playwright smoke/E2E, reconnect/error flows |
| CI | build/test/lint and artifact validation |

## Development environment isolation

Development and test deployments should be isolated from production. Destructive experiments, fault injection, load tests, and malformed-input testing must target disposable environments.

## Release progression

The autonomous factory should respect the project's milestone order. M0 correctness comes before large-scale infrastructure and feature breadth. Do not use the existence of the long-term architecture as a reason to block a minimal playable core.

## Durable decisions

When an agent makes a material engineering choice that was not explicitly specified, record it in the appropriate documentation, preferably `docs/PRODUCT-DECISIONS.md` for product/gameplay semantics or `docs/ARCHITECTURE.md` for architecture. Include the reason and impact.

## Human blockers

Record a blocker with:

- exact missing capability;
- why automation cannot safely resolve it;
- attempted alternatives;
- affected tasks;
- what the human must provide or decide.

A blocker must be specific. `Need human input` is not an acceptable blocker description.
