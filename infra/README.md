# Infrastructure

Infrastructure is separated by environment and must remain reproducible.

## Layout

```text
infra/
├── compose/               # local dependencies
├── k8s/                   # Kubernetes manifests / Helm
├── agones/                # game server fleet configuration
├── envoy/                 # edge routing and websocket policy
├── monitoring/            # Prometheus, Grafana, alerts
└── migrations/            # data/schema migration assets
```

## Environments

### Local

Run only the minimum services required for feature development. Local game simulation should not require Kubernetes.

### Staging

Production-like protocol, service boundaries and persistence behavior. Agones should be used here before validating scale claims.

### Production

Multi-region deployment, autoscaling, managed PostgreSQL/Redis/ClickHouse where appropriate, object storage and CDN.

## Reliability principles

- Match gameplay must continue during analytics outages.
- Reward/economy writes are idempotent.
- Match servers have bounded session lifetime and resource limits.
- Health, readiness and draining are distinct states.
- Deployments must have a rollback path.
- Capacity tests are based on observed CPU, memory, network and tick timing, not concurrent-client counts alone.
