# Word Arena Architecture

## Architectural target

Word Arena uses a server-authoritative model. The client is responsible for presentation and input collection; the server owns competitive truth.

```text
Mobile Client (Unity)
        |
   TLS WebSocket
        |
   Envoy / Edge
        |
   +----+-------------------------------+
   |                                    |
Matchmaking                         Game Session
   |                                    |
   +----------------+-------------------+
                    |
          Domain / Platform Services
       /          |         |          \
  Identity    Profile    Guilds     Economy
       |          |         |          |
                 PostgreSQL
                    |
        +-----------+-----------+
        |                       |
     Event Bus              Redis
        |                       |
   +----+---------+             |
   |              |             |
ClickHouse      Object Store / CDN

Dictionary data -> compiled immutable graph + versioned delta buffers
```

## Service boundaries

### Match Service
Owns room lifecycle, tick loop, authoritative board state, word claims, locks, steals, scoring, elimination and reconnect state.

Initial implementation should prefer **Go** unless profiling proves a specific hot path requires Rust/C++. Premature multi-language service boundaries create a very expensive form of optimism.

### Matchmaker
Owns queue admission and candidate selection. MMR, latency, language and party constraints are inputs. Matchmaking decisions must be observable and replayable.

### Dictionary Service / Library
Owns dictionary versions and validation APIs. Runtime validation must be deterministic and local to the match process or a low-latency library call. A network round-trip per word is unacceptable for core gameplay.

### Profile / Economy
Owns durable player state, currencies, inventory, rewards and idempotent transaction processing.

### Guild / Social
Owns guild membership, permissions, chat routing, guild progression and social events.

### Analytics
Consumes versioned events. Product analytics must never become a dependency in the synchronous match path.

## Data ownership rules

| Data | Source of truth | Cache | Notes |
|---|---|---|---|
| Match state | Match server | Redis optional | Ephemeral; deterministic snapshot/replay support |
| Wallet | PostgreSQL | Redis optional | Every mutation idempotent and auditable |
| Profile | PostgreSQL | Redis | Versioned writes |
| Dictionary | Versioned object artifact | Process memory | Immutable runtime snapshot + delta buffer |
| Leaderboards | Redis / durable projection | Redis | Rebuildable from authoritative events |
| Telemetry | ClickHouse | Kafka/NATS buffer | Append-only event model |

## Realtime protocol

Protobuf contracts live under `/proto` and are treated as public internal APIs. Field numbers are never reused. Breaking changes require a protocol version transition and a compatibility window.

The initial wire contract should transmit **intent**, not client-authoritative outcomes. Server timestamps and sequencing are authoritative.

## Reconnect

A player may resume a live match using a scoped session-resume token. The server returns a canonical snapshot plus a monotonic state/version marker. Clients discard obsolete local predictions and reconcile to the canonical state.

## Observability

Every match should produce:

- match lifecycle events;
- per-action validation outcomes;
- server tick and latency metrics;
- reconnect/disconnect events;
- suspicious-input signals;
- reward/economy events;
- deterministic replay metadata.

Use OpenTelemetry where possible so traces, metrics and logs share correlation IDs.

## Environment model

```text
local       -> Docker Compose / local Unity + local services
ci          -> ephemeral service containers + deterministic tests
staging     -> Kubernetes + Agones, production-like topology
production  -> multi-region Kubernetes + Agones + managed data services
```

The local environment must exercise the same protocol and service contracts as staging. Mocks should replace external infrastructure only where the behavior is explicitly modeled and tested.
