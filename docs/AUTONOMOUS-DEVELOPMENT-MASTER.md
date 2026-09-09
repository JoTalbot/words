# Word Arena — Master Autonomous Development Specification

## 1. Purpose

This document is the durable, long-form operating specification for autonomous development of `Word Arena: Masters of Letters`.

The project is intended to be developed as a continuously operating multi-agent software factory. Arena is the orchestration layer. SSH-connected server infrastructure is the execution layer. Local LLMs are workers. Git is the durable source of implementation history. `agent/state/current.yml` is the durable resume state.

The objective is continuous progress toward release with minimal human involvement.

## 2. Operating architecture

```text
                     ARENA
              orchestration / planning
                       |
              state recovery + graph
                       |
        +--------------+--------------+
        |              |              |
   strong agents   local LLMs     QA/review agents
        |              |              |
        +--------------+--------------+
                       |
                 SSH SERVER
                       |
             isolated worktrees
                       |
       +---------------+----------------+
       |               |                |
     build           tests           services
       |               |                |
       +---------------+----------------+
                       |
        +--------------+---------------+
        |              |               |
    Android QA     Browser QA      Load/Replay
        |              |               |
        +--------------+---------------+
                       |
                review/integration
                       |
                      Git
                       |
               persistent state
                       |
                  next cycle
```

The architecture must tolerate an Arena session ending. A new session must be able to resume from repository state and server state without reconstructing the entire project manually.

## 3. Nonstop is a system property

A prompt alone cannot guarantee that an external agent UI remains alive forever. Arena sessions can terminate because of platform limits, context limits, network errors, crashes, or stalled generation.

Therefore nonstop operation means:

- work is persisted frequently;
- tasks are small;
- every task has a durable status;
- active work can be recovered;
- failed workers can be replaced;
- a new Arena session can resume;
- the server can continue running independent workers where possible;
- tests and automation can operate without a human;
- no progress depends exclusively on one conversation context.

Never treat the end of an Arena session as the end of the project.

## 4. Startup and resume

Every Arena session must begin with state recovery rather than a fresh project plan.

Inspect:

- repository state;
- Git history;
- worktrees;
- branches;
- running processes;
- worker processes;
- Docker/services;
- CI state;
- `agent/state/current.yml`;
- task files;
- recent logs;
- latest test results.

Determine what is actually complete. Do not trust stale status text when code and tests prove otherwise.

## 5. Task graph

Every task must have:

- unique ID;
- owner role;
- priority;
- complexity;
- dependencies;
- inputs;
- outputs;
- acceptance criteria;
- validation commands;
- file ownership;
- risk;
- escalation policy.

Tasks should be small enough for one bounded worker session wherever practical.

Prefer deterministic tasks with objective validation.

## 6. Parallel execution

Independent tasks should run concurrently.

Examples:

- protocol tests + gameplay unit tests;
- dictionary fixtures + server test harness;
- documentation + isolated test additions;
- Android test preparation + browser test preparation.

Do not parallelize tasks that edit the same files or depend on an unstable interface.

Use worktrees or equivalent isolation. The orchestrator integrates results.

## 7. Model routing

Detect all available local models and CLIs before assigning work.

Potential worker backends include Hermes CLI, Ollama, llama.cpp, vLLM, or another installed runtime.

The system should maintain a capability map such as:

```text
worker → model → context size → tools → concurrency → cost → reliability
```

Use the cheapest reliable worker for bounded tasks. Escalate when the task exceeds the worker's capability.

## 8. Autonomous recovery

### Worker failure

Capture logs, classify failure, retry, and reassign if necessary.

### Test failure

Reproduce, preserve a regression case, fix, and rerun.

### Build failure

Inspect compiler/toolchain output, make the smallest safe fix, and rerun.

### CI failure

Determine whether the failure is code, dependency, infrastructure, or workflow related. Fix the appropriate layer and rerun.

### Emulator failure

Attempt local recovery first. If the local emulator is unavailable or unreliable, switch to a remote/cloud/online Android testing provider that is actually accessible and authorized.

### Browser failure

Restart isolated browser context, collect diagnostics, retry, then fix the application if the failure is reproducible.

## 9. Android testing fallback hierarchy

Android testing must not depend on one specific emulator installation.

Use this fallback order:

1. local Android emulator on the development server;
2. another available local AVD/device;
3. containerized or virtual Android environment if already supported;
4. accessible remote/cloud/online Android emulator or device service;
5. another authorized automated Android testing mechanism available in the environment.

Examples of possible remote providers may include services such as MyAndroid.org or other online Android emulators, cloud device farms, or browser-accessible Android environments. **Do not hard-code one provider.** At runtime, discover what service is accessible, permitted, stable, and automatable.

The orchestrator should test remote options by checking:

- network reachability;
- authentication/authorization availability;
- API or automation interface;
- ability to install the build;
- ability to launch the application;
- screenshot/video access;
- input automation;
- log retrieval;
- test result retrieval;
- rate limits and cost constraints.

Do not use an online emulator merely because its website exists. Prefer an actual automation/API interface. If only interactive browser control exists, use browser automation where permitted.

Never bypass provider authentication, access controls, CAPTCHA, rate limits, or other protections.

The test report must identify the actual Android execution backend used:

```text
ANDROID_BACKEND=local-emulator
ANDROID_BACKEND=remote-device
ANDROID_BACKEND=online-emulator
ANDROID_BACKEND=unavailable
```

If Android is unavailable, continue all other development and create a bounded Android-blocked task rather than stopping the entire project.

## 10. Android autonomous validation

For every meaningful Android change:

```text
build
→ obtain test artifact
→ select available Android backend
→ install
→ launch
→ execute scripted flow
→ collect logs
→ collect screenshot/video
→ vision inspection
→ compare expected state
→ report failure
→ fix
→ rebuild
→ reinstall
→ retest
```

Use deterministic test data and isolated accounts/environments.

## 11. Browser automation

Use Playwright or the best available automation framework.

Browser automation should support:

- smoke tests;
- E2E flows;
- UI regression;
- reconnect/error paths;
- screenshots;
- API/browser integration;
- controlled admin/test environments.

Use only authorized environments.

## 12. Test strategy

The autonomous factory should continuously increase confidence through:

- unit tests;
- integration tests;
- deterministic simulation;
- replay tests;
- property-based tests;
- fuzzing;
- malformed-input tests;
- network fault injection;
- latency/loss/reordering tests;
- reconnect tests;
- load tests;
- security tests;
- Android E2E;
- browser E2E.

A green test suite must not be achieved by deleting or weakening assertions.

## 13. Word Arena correctness principles

The competitive match server is authoritative.

Client data can be useful telemetry, but must not become an authority for:

- score;
- ordering;
- legality;
- match state;
- final result.

The server must independently validate gameplay intents.

Gameplay must be deterministic enough to support replay and debugging.

Dictionary data must be versioned and reproducible.

Anti-cheat must combine multiple signals and account for false positives.

## 14. M0 discipline

M0 should prove the smallest meaningful deterministic multiplayer core.

Do not block M0 on future-scale infrastructure unless that infrastructure is genuinely required for the current acceptance criteria.

Prioritize:

1. authoritative match state;
2. deterministic word submission;
3. scoring;
4. board rules;
5. protocol;
6. dictionary validation;
7. replay/determinism;
8. reconnect;
9. automated testing;
10. playable client validation.

## 15. Progress reporting in Russian

During autonomous work, the orchestrator must provide progress updates **in Russian**.

Every meaningful step should have a short status message containing:

```text
[ЭТАП] Что сейчас делается
[ЗАЧЕМ] Почему это делается
[ПРОГРЕСС] Текущий этап и процент/счётчик, если его можно определить
[РЕЗУЛЬТАТ] Что получено
[ДАЛЬШЕ] Следующее действие
```

Do not claim an exact percentage when it cannot be calculated. In that case use milestone/task counts, for example:

```text
Прогресс M0: 7/18 задач проверены.
```

For long-running operations, report start, meaningful intermediate milestones, completion, failure, retry, and recovery.

Do not spam one message per shell command. Aggregate low-level commands into meaningful steps.

## 16. Roadmap progress

Maintain visible progress against `docs/ROADMAP.md`.

At every significant cycle report:

```text
ROADMAP
M0  ███████░░░ 70%  deterministic core
M1  ░░░░░░░░░░  0%  vertical slice
M2  ░░░░░░░░░░  0%  alpha
M3  ░░░░░░░░░░  0%  beta
M4  ░░░░░░░░░░  0%  soft launch
M5  ░░░░░░░░░░  0%  global launch
```

The actual values must be derived from acceptance criteria and completed tasks, not guessed.

Roadmap progress is a management signal, not a substitute for technical verification.

## 17. State persistence

Update `agent/state/current.yml` after each meaningful batch.

At minimum record:

- current milestone;
- current status;
- completed tasks;
- active tasks;
- failed tasks;
- blocked tasks;
- next tasks;
- last verified commit;
- test status;
- active workers;
- Android backend;
- resume point;
- timestamp.

Do not store secrets.

## 18. Session handoff

When a session is ending, save state before producing a final response.

The handoff must be sufficient for another Arena session to continue without asking what happened.

Minimum handoff:

```text
last verified commit
active tasks
worker state
failed tasks
test results
Android backend
next batch
resume command/context
```

## 19. Human intervention

Human involvement is reserved for genuine blockers:

- unavailable credentials;
- unavailable external authorization;
- irreversible production action;
- material unresolved product/legal/business decision;
- critical security/safety decision.

Routine work must be automated.

## 20. Security

Never commit credentials or secrets.

Never expose secrets in logs or state files.

Do not bypass external authentication, CAPTCHAs, access controls, rate limits, or provider protections.

Run destructive or load-heavy tests only against isolated environments.

## 21. Definition of Done

A task is complete when:

- implementation is present;
- acceptance criteria pass;
- relevant tests pass;
- static/build checks pass;
- integration is complete;
- documentation/contracts are updated where required;
- no unintended files or secrets are included;
- the commit is recorded;
- task state is updated.

## 22. Definition of continuous progress

Continuous progress means the system always does one of these:

- executes an available task;
- validates a result;
- repairs a failure;
- integrates verified work;
- prepares the next batch;
- waits only on a real external dependency.

A planning-only state is not considered progress.

A final chat response is not considered completion.

The release gate is the completion criterion.
