# Server

Initial server code is intentionally small. The first milestone is a
deterministic match simulation, not a distributed microservice zoo.

## Local run

```bash
go run ./cmd/game
```

Health endpoint (default port 8080; override with `WORDARENA_ADDR` when an
isolated port is required, e.g. `WORDARENA_ADDR=127.0.0.1:18080`):

```bash
curl http://localhost:8080/healthz
```

## Current state (M0 batch 1 — deterministic core)

| Area | Status |
|---|---|
| Deterministic PRNG (`internal/prng`) | xoshiro256** + splitmix64, golden vectors |
| Dictionary snapshots (`internal/dictionary`) | en/ru/uk embedded snapshots v1, normalization, sha256 manifest |
| Scoring (`internal/scoring`) | letter tables data v1, combo, bonuses, integer scoring |
| Match simulation (`internal/match`) | waves, claims, locks, cross-steal, replay, snapshots |
| Transport adapter | next batch (WebSocket + Protobuf) |
| Session/reconnect | next batch |
| Network fault sim / load baseline | next batch |

Regenerate protocol code after editing `proto/` (committed output keeps CI
free of protoc):

```bash
protoc -I proto --go_out=server --go_opt=module=github.com/JoTalbot/words/server proto/wordarena/v1/match.proto
```

Run all checks:

```bash
go vet ./...
go test ./...
go test -race ./internal/...
```

## Planned packages

```text
server/
├── cmd/
│   ├── game/              # process entrypoint
│   ├── matchmaker/        # matchmaking service
│   └── worker/            # async/event consumers
├── internal/
│   ├── match/             # authoritative simulation (implemented, M0)
│   ├── scoring/           # deterministic scoring (implemented, M0)
│   ├── dictionary/        # validator adapter (implemented, M0)
│   ├── prng/              # deterministic content RNG (implemented, M0)
│   ├── session/           # reconnect/resume state
│   └── protocol/          # transport adapters
└── tests/                 # integration/load helpers
```

Do not put business logic in HTTP/WebSocket handlers. Handlers translate
transport messages into domain commands; the domain simulation remains
independently testable.

## Determinism contract

- Match content never uses wall clocks or `math/rand`; see
  `docs/M0-MATCH-RULES.md`.
- The event log of a match is sufficient to reproduce the identical final
  state via `internal/match` (property-tested).
- Competitive rules live in `docs/M0-MATCH-RULES.md`; code must not silently
  contradict the spec.
