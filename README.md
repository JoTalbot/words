# Word Arena: Masters of Letters

Multiplayer competitive word game built around shared-board anagram combat, synchronous PvP, social progression and LiveOps.

## Repository status

**Stage:** Pre-production / M0 bootstrap

The repository is intentionally initialized as a production-oriented monorepo skeleton. Gameplay rules and network contracts are documented separately from implementation so the team can iterate without coupling design decisions to runtime code.

## Product pillars

- **Simple Core:** one-handed swipe anagram input.
- **Competitive Depth:** shared board, claiming, locking, stealing, timing and combo play.
- **Authoritative Multiplayer:** server owns match state, validation, scoring and progression.
- **Social Meta:** guilds, seasons, events, territory and cooperative bosses.
- **LiveOps First:** versioned dictionaries, telemetry, economy configuration and remotely controlled content.

## Planned stack

- Client: Unity / C#, IL2CPP, URP
- Realtime transport: WebSocket + Protobuf v3
- Match server: Go first, with Rust/C++ reserved for isolated performance-critical components
- Edge: Envoy
- Orchestration: Kubernetes + Agones
- Data: PostgreSQL, Redis, ClickHouse, S3-compatible object storage
- Messaging: NATS JetStream or Redis Streams
- Observability: OpenTelemetry, Prometheus, Grafana

## Repository layout

```text
.
├── client/                 # Unity project
├── server/                 # Go backend and match services
├── proto/                  # Versioned Protobuf contracts
├── dictionary/             # Dictionary compiler, validators and language data tooling
├── infra/                  # Docker, Kubernetes, Agones, Envoy and local environments
├── analytics/              # Event schemas, ClickHouse models and product analytics
├── tools/                  # Load generators, bots, replay and developer utilities
├── tests/                  # Cross-component and protocol test assets
├── docs/                   # Product, technical and operational documentation
└── .github/                # CI/CD, issue templates and repository automation
```

## Development principles

1. The match server is authoritative. Clients never decide competitive outcomes.
2. Protobuf schemas are versioned and backwards-compatible by default.
3. Competitive scoring is deterministic and covered by golden tests before client polish.
4. Dictionary content is data, not application logic.
5. Every production feature ships with telemetry and an operational rollback path.
6. Security, anti-cheat and abuse controls are treated as platform concerns, not last-minute patches.

## M0 entry criteria

Before moving into the vertical slice, the team should have:

- deterministic word validation;
- a versioned match protocol;
- a minimal authoritative 1v1 simulation;
- reconnect/resume semantics;
- reproducible local development;
- CI that builds and tests server, protocol and dictionary components.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), [`docs/M0.md`](docs/M0.md) and [`docs/PRODUCT-DECISIONS.md`](docs/PRODUCT-DECISIONS.md).
