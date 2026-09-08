# Agent Factory

This directory contains durable assets for autonomous multi-agent development.

## Files

- `prompts/arena-start.md` — canonical startup prompt for the Arena orchestrator.
- `TASK-TEMPLATE.md` — schema and worker contract for bounded tasks.

The operating policies live in `AGENTS.md` and `docs/`.

## Runtime principle

The repository does not require a specific agent CLI. Arena should discover what is installed on the server and select the best available combination of local LLM runners, shell tooling, Git worktrees, test runners, Android automation, and browser automation.

A suggested implementation is:

```text
Arena
  ├─ strong reasoning worker(s)
  ├─ local low-cost LLM workers
  ├─ test/review workers
  └─ automation workers
        ↓
   isolated worktrees
        ↓
   validation + CI
        ↓
     integration
```

Keep the orchestration layer replaceable. Hermes CLI, Ollama, llama.cpp, vLLM, or another installed runtime can be used as a worker backend without changing the product architecture.
