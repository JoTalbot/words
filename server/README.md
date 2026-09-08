# Server

Initial server code is intentionally small. The first milestone is a deterministic match simulation, not a distributed microservice zoo.

## Local run

```bash
go run ./cmd/game
```

Health endpoint:

```bash
curl http://localhost:8080/healthz
```

## Planned packages

```text
server/
├── cmd/
│   ├── game/              # process entrypoint
│   ├── matchmaker/        # matchmaking service
│   └── worker/            # async/event consumers
├── internal/
│   ├── match/             # authoritative simulation
│   ├── scoring/           # deterministic scoring
│   ├── dictionary/        # validator adapter
│   ├── session/           # reconnect/resume state
│   └── protocol/          # transport adapters
└── tests/                 # integration/load helpers
```

Do not put business logic in HTTP/WebSocket handlers. Handlers translate transport messages into domain commands; the domain simulation remains independently testable.
