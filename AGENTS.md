# AGENTS.md — Word Arena autonomous development

## Mission

`Word Arena: Masters of Letters` is developed as an autonomous, multi-agent software factory. The repository is the source of truth for architecture, product decisions, implementation rules, tests, and release criteria.

The default operating mode is continuous autonomous execution. Agents should decompose work into small bounded tasks, execute independent tasks in parallel, validate every change, integrate continuously, and keep moving without waiting for a human when a safe engineering decision can be made from the repository and documented defaults.

## Authority order

1. Explicit product decisions in `docs/PRODUCT-DECISIONS.md`.
2. Architecture and protocol contracts in `docs/ARCHITECTURE.md` and `proto/`.
3. Acceptance criteria in `docs/M0.md`, `docs/ROADMAP.md`, and task-specific specifications.
4. Tests and executable invariants.
5. Repository conventions and this document.
6. Agent judgement, with decisions recorded when they materially affect the system.

When sources conflict, stop the affected task, record the conflict, and resolve it using the highest-authority source. Do not silently invent a competing specification.

## Autonomous execution rules

- Inspect the current repository and runtime before making assumptions.
- Break large work into small tasks with explicit inputs, outputs, dependencies, and acceptance criteria.
- Run independent tasks in parallel whenever doing so cannot create file or state conflicts.
- Give each concurrent worker exclusive ownership of the files it edits. Use branches/worktrees where appropriate.
- Prefer simple, deterministic tasks for smaller local LLMs. Escalate architecture, security, protocol, and ambiguous tasks to stronger reasoning agents.
- Never accept generated code without tests, static checks, or a reproducible validation step appropriate to the change.
- Never commit secrets, credentials, private keys, tokens, production data, or generated local state.
- Never force-push or rewrite shared history as part of routine autonomous work.
- Keep commits small and meaningful. Integrate frequently.
- Update documentation when behavior, interfaces, decisions, or operational procedures change.
- If a task fails, diagnose the failure, retry with a corrected approach, and continue. Do not simply report the failure and wait.
- Preserve reproducibility: record commands, versions, fixtures, and deterministic seeds where useful.

## Human intervention policy

Do not ask the human to perform routine development, testing, debugging, deployment, browsing, emulator interaction, or repository operations that can be automated safely.

A human blocker is justified only for:

- missing credentials, secrets, or external authorization that cannot be provisioned automatically;
- an irreversible destructive action with meaningful data-loss or production impact;
- a product/legal/business decision that is genuinely unresolved and materially changes the product;
- a security or safety decision where available evidence is insufficient for an autonomous choice.

For ordinary engineering ambiguity, choose the safest documented default, record the decision, and continue.

## Required quality loop

`discover → plan → decompose → parallel execute → test → review → integrate → run broader tests → update docs → repeat`

The loop continues until the current release gate is satisfied or a genuine blocker is recorded.

## Testing expectations

Depending on the change, use unit tests, integration tests, deterministic replay, protocol compatibility tests, fuzz/property tests, network fault injection, load tests, browser E2E, Android emulator tests, screenshot/vision validation, and security checks.

For competitive gameplay, server authority and deterministic outcomes are mandatory. Client timestamps, client hashes, animation state, and touch trajectories are never authoritative for score, ordering, legality, or match state.

## Tooling

Agents must discover installed tooling rather than assuming a particular stack is available. Candidates include Go, Rust, C/C++, Python, Node.js, Docker/Compose, Kubernetes/Agones, Android SDK/ADB/emulators, Unity command-line tooling, Playwright/Chromium, local LLM runners, Hermes CLI, Ollama, llama.cpp, vLLM, and other installed automation tools.

Use project-controlled browser environments and test systems for autonomous browser automation. Respect external service authorization and terms; do not bypass access controls.

## Current product constraints

- M0 prioritizes a deterministic 1v1 core.
- Match authority is server-side.
- WebSocket + Protobuf is the initial transport direction.
- The initial match loop targets 30 Hz server simulation.
- Dictionary data is versioned and should support immutable snapshots plus controlled deltas.
- Anti-cheat is multi-signal risk assessment, not an automatic ban from one heuristic.
- Bots must not masquerade as human players in ranked play unless an explicit product decision changes that rule.
- Kubernetes/Agones and large-scale infrastructure should follow proven gameplay correctness rather than block the first playable milestone.

## Definition of Done

A task is done only when the implementation is complete, tests and relevant validation pass, documentation/contracts are updated where needed, no known regression is introduced, and the resulting state is committed and integrated.
