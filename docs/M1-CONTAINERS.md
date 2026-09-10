# M1 — Container and compose operations

This is the runbook for the **deliverable artifact**: the container image built
from `infra/Dockerfile` and the local stack in `infra/docker-compose.yml`.

Until batch 18 (2026-09-10) the image and the compose file had only been
*statically reviewed*. They are now built and exercised on every push by
`.github/workflows/container-smoke.yml`, so a broken Dockerfile fails CI
instead of failing at deploy time.

Scope note: this file covers the container path. The systemd deployment on the
OCI host is documented separately in `docs/M1-OPS.md`.

## Build

The build context is the **repository root**, not `server/`, because the
Dockerfile copies `server/` into the build stage.

```bash
docker build -f infra/Dockerfile -t wordarena-game:local .
```

The image is two-stage: `golang:1.24-alpine` compiles a static
(`CGO_ENABLED=0`, `-trimpath`, `-ldflags="-s -w"`) binary, and `alpine:3.20`
runs it as the non-root user `wordarena`. The dictionary data is embedded in
the binary, so the runtime image needs no data volume.

## Run (compose)

```bash
docker compose -f infra/docker-compose.yml up -d --build
```

The stack has two services:

| Service | Image | Role |
|---|---|---|
| `db` | `postgres:16-alpine` | durable storage for profiles and match results |
| `game` | built from the repo root | the match service |

`game` declares `depends_on: db: condition: service_healthy`, so the match
service only starts after `pg_isready` succeeds. The compose healthcheck polls
`/healthz`; readiness (`/readyz`) additionally pings Postgres and reports the
storage backend.

### Host port

The published host port is overridable, because the systemd deployment on the
dev host already owns `127.0.0.1:18080`:

```bash
WORDARENA_COMPOSE_HOST_PORT=18090 docker compose -f infra/docker-compose.yml up -d --build
```

Default is `18080`.

## Verify

`infra/smoke.sh` is the single verification entry point. It exits non-zero if
any check fails and reports every failure in one run.

```bash
WORDARENA_COMPOSE_HOST_PORT=18090 docker compose -f infra/docker-compose.yml up -d --build
# wait for both services to report healthy
docker compose -f infra/docker-compose.yml ps
# run the checks
SMOKE_EXPECT_STORAGE=postgres bash infra/smoke.sh http://127.0.0.1:18090
```

The script asserts:

1. `GET /healthz` is 200 and reports `{"status":"ok"}`.
2. `GET /readyz` is 200, reports `ready`, and (when `SMOKE_EXPECT_STORAGE` is
   set) reports that storage backend.
3. `GET /metrics` exposes the match counters.
4. `GET /metrics/prometheus` exposes `wordarena_*` series.
5. `POST /v1/matches` returns 2xx with two **distinct** seat tokens.
6. `GET /v1/match/ws` rejects a missing or invalid seat token with **401** —
   the seat token is the only credential a client holds, so this is the
   security-relevant assertion of the smoke run.
7. A real WebSocket upgrade with a valid seat token is accepted and the first
   binary frame (the canonical snapshot) arrives with opcode 2.
8. `POST /v1/players` then `GET /v1/players/{id}` round-trip a profile through
   the storage backend and preserve the nickname.
9. `GET /v1/matches/{id}/replay` returns **404 while the match is still
   active**. The replay log is published at match end; a 200 here would mean a
   partial, non-final event log is being served.

Telemetry is verified at the container level rather than over HTTP:

```bash
docker compose -f infra/docker-compose.yml exec -T game \
  sh -c 'test -s /var/log/wordarena/events.jsonl'
```

Tear down with:

```bash
docker compose -f infra/docker-compose.yml down -v
```

## Configuration

| Variable | Default in compose | Meaning |
|---|---|---|
| `WORDARENA_ADDR` | `:8080` | listen address inside the container |
| `WORDARENA_MAX_ROOMS` | `128` | concurrent room cap; new matches get 429 beyond it |
| `WORDARENA_MAX_WS_BYTES` | `65536` | per-frame WebSocket read limit |
| `WORDARENA_POSTGRES_DSN` | `postgres://wordarena:wordarena@db:5432/wordarena?sslmode=disable` | durable storage; unset means in-memory |
| `WORDARENA_TELEMETRY_JSONL` | `/var/log/wordarena/events.jsonl` | optional append-only JSONL event sink |
| `WORDARENA_TELEMETRY_BUFFER` | `4096` | event buffer depth; a full buffer drops events rather than blocking a match |
| `WORDARENA_COMPOSE_HOST_PORT` | `18080` | host port published by compose (not read by the service) |

`WORDARENA_SEAT_TOKEN_TTL_SECONDS`, `WORDARENA_INTENTS_PER_SEC` and the other
runtime limits documented in `docs/WIRE-PROTOCOL.md` are inherited unchanged.

## Troubleshooting

| Symptom | Likely cause | Check |
|---|---|---|
| `game` container restarts in a loop | Postgres not reachable yet, or DSN typo | `docker compose logs game`; the DSN host must be the compose service name `db`, not `127.0.0.1` |
| `/readyz` returns 503 `storage_unavailable` | DB up but ping failing (auth, database missing) | `docker compose exec -T db pg_isready -U wordarena -d wordarena` |
| `/readyz` reports `"storage":"memory"` | `WORDARENA_POSTGRES_DSN` not set for the `game` service | `docker compose exec -T game printenv WORDARENA_POSTGRES_DSN` |
| WebSocket upgrade returns 401 | wrong or already-rotated seat token | re-create the match; seat tokens rotate via `POST /v1/matches/{id}/token/rotate` |
| `events.jsonl` stays empty | telemetry only emits on lifecycle/action events | create a match and connect a client, then re-check; `telemetry_events_dropped` in `/metrics` reveals a saturated buffer |
| compose port bind fails | host port already taken (systemd deployment) | set `WORDARENA_COMPOSE_HOST_PORT` |
| smoke passes locally but fails in CI | timing: CI waits on healthchecks, not sleeps | confirm both services report `healthy` before `infra/smoke.sh` runs |

## Rollback

The image is reproducible from a commit, so rollback is a rebuild at a known
revision:

```bash
git checkout <known-good-commit>
docker build -f infra/Dockerfile -t wordarena-game:local .
docker compose -f infra/docker-compose.yml up -d --build
```

For the systemd deployment the equivalent procedure (binary swap plus
`wordarena-server.prev`) is in `docs/M1-OPS.md`.
