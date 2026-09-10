# Infrastructure

Infrastructure is separated by environment and must remain reproducible.

## Layout

```text
infra/
├── Dockerfile             # multi-stage static build of the match service
├── docker-compose.yml     # local stack: game + postgres + telemetry volume
├── smoke.sh               # end-to-end verification of a running service
├── migrations/            # versioned SQL: 001_init.sql
├── monitoring/            # Prometheus file_sd targets + Grafana dashboard
└── README.md
```

The directories below are the planned target layout and are **not present
yet**; they are recorded here so the shape is deliberate rather than
accidental:

```text
infra/
├── compose/               # local dependencies (future)
├── k8s/                   # Kubernetes manifests / Helm (future)
├── agones/                # game server fleet configuration (future)
└── envoy/                 # edge routing and websocket policy (future)
```

Container and compose operations are documented in `docs/M1-CONTAINERS.md`;
the systemd deployment on the dev host is documented in `docs/M1-OPS.md`.

## Environments

### Local

Run only the minimum services required for feature development. Local game simulation should not require Kubernetes.

The M0 game service ships a self-contained static binary (dictionary data is
embedded), so the local environment is a single container:

```bash
docker compose -f infra/docker-compose.yml up --build
# http://127.0.0.1:18080/healthz   -> liveness
# http://127.0.0.1:18080/readyz    -> readiness/draining/storage check
# http://127.0.0.1:18080/metrics   -> telemetry counters (JSON)
# http://127.0.0.1:18080/metrics/prometheus -> Prometheus text metrics
# POST http://127.0.0.1:18080/v1/matches  -> create a match
# ws://127.0.0.1:18080/v1/match/ws        -> live play
```

`infra/Dockerfile` builds the binary with a multi-stage Go 1.24 → Alpine
(nonroot) pipeline; build context is the repository root. Runtime limits are
configurable via `WORDARENA_MAX_ROOMS` / `WORDARENA_MAX_WS_BYTES` (see
`docs/WIRE-PROTOCOL.md`). Optional append-only event export is enabled with
`WORDARENA_TELEMETRY_JSONL`; the compose file mounts a `telemetry` volume at
`/var/log/wordarena` for this purpose.

### Staging

Production-like protocol, service boundaries and persistence behavior. Agones should be used here before validating scale claims.

### Production

Multi-region deployment, autoscaling, managed PostgreSQL/Redis/ClickHouse where appropriate, object storage and CDN.

## Reliability principles

- Match gameplay must continue during analytics outages.
- Reward/economy writes are idempotent.
- Match servers have bounded session lifetime and resource limits.
- Health (`/healthz`), readiness/draining (`/readyz`) and load-balancer removal are distinct states.
- Deployments must have a rollback path.
- Capacity tests are based on observed CPU, memory, network and tick timing, not concurrent-client counts alone.
