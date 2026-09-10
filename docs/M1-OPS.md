# M1 Operations Runbook — Word Arena match service

Status: LIVE on OCI `arm-server-01` since 2026-09-10 (batch 16). The service
runs under systemd (`wordarena.service`), with durable PostgreSQL storage and
JSONL telemetry. This runbook is the operational source of truth for the dev
deployment.

## Service layout (OCI arm-server-01)

| Item | Path / value |
|---|---|
| Binary | `/opt/words/bin/wordarena-server` (copy of the built `cmd/game`) |
| Repository | `/opt/words` (tracks `origin/main`) |
| systemd unit | `/etc/systemd/system/wordarena.service` |
| Secrets/env | `/etc/wordarena/wordarena.env` (mode 600, root-owned) |
| Telemetry sink | `/opt/words/telemetry/events.jsonl` (JSONL, append-only) |
| Logs | `journalctl -u wordarena.service` |
| Listen address | `127.0.0.1:18080` (loopback-only; no public exposure yet) |
| Postgres | host Postgres 16, db/role `wordarena`, local trust-less password auth |

Environment variables (see `cmd/game/main.go` and `cmd/game/telemetry.go`):

- `WORDARENA_ADDR` — listen address (default `:8080`).
- `WORDARENA_POSTGRES_DSN` — enables durable profile/result storage; the
  process fails fast if the connection cannot be established.
- `WORDARENA_TELEMETRY_JSONL` — path of the optional non-blocking JSONL
  lifecycle/action event sink.
- `WORDARENA_TELEMETRY_BUFFER` — optional buffer size for the JSONL exporter.
- `WORDARENA_MAX_ROOMS`, `WORDARENA_INTENTS_PER_SEC`,
  `WORDARENA_SEAT_TOKEN_TTL_SECONDS` — capacity/abuse knobs (see
  docs/M1-PERSISTENCE.md and the M1-prep batch notes in
  `agent/state/current.yml`).

## Operational endpoints

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | liveness only; never reports storage state |
| `GET /readyz` | readiness + draining gate; reports `storage` backend; 503-style `draining` while shutting down |
| `GET /metrics` | JSON counters |
| `GET /metrics/prometheus` | Prometheus text exposition |

## Deploy (redeploy current main)

```bash
cd /opt/words && git fetch origin && git reset --hard origin/main
export PATH=/opt/go/bin:$PATH
cd server && go vet ./... && go test -count=1 ./...
go build -o /tmp/words-game.new ./cmd/game
sudo cp /tmp/words-game.new /opt/words/bin/wordarena-server
sudo systemctl restart wordarena.service
curl -s http://127.0.0.1:18080/healthz && curl -s http://127.0.0.1:18080/readyz
```

The service drains gracefully on SIGTERM/SIGINT (`systemctl stop` or
`restart`): the API flips to draining, new mutating work and WebSocket
upgrades are rejected, and in-flight matches get a bounded drain window
before exit (see batch 12 readiness/draining implementation).

## Rollback

```bash
# keep the previous binary before overwriting:
sudo cp /opt/words/bin/wordarena-server /opt/words/bin/wordarena-server.prev
# rollback:
sudo cp /opt/words/bin/wordarena-server.prev /opt/words/bin/wordarena-server
sudo systemctl restart wordarena.service
```

Postgres schema is currently append-compatible (profiles/results tables only);
a binary rollback does not require data migration.

## Crash behaviour

`Restart=on-failure` with `RestartSec=2`: a SIGKILL/crash is restarted by
systemd within ~2s (verified 2026-09-10: SIGKILL → active + `/readyz` ready).

## Live smoke

```bash
# quick: health + readiness + durable profile round-trip
curl -s http://127.0.0.1:18080/healthz
curl -s http://127.0.0.1:18080/readyz
curl -s -X POST http://127.0.0.1:18080/v1/players \
  -H 'Content-Type: application/json' -d '{"nickname":"ops-smoke","language":"en"}'

# full deterministic exit gate (6 live matches, offline-replay equality):
cd /opt/words/server && go build -o /tmp/headless-bot ./cmd/headless-bot
WORDARENA_ADDR=http://127.0.0.1:18080 /tmp/headless-bot -rounds 2 -seeds 1512,1513,1517
# expected: EXIT-GATE: PASS with terminal_snapshots_equal/client_streams_equal true
```

## Telemetry checks

```bash
tail -f /opt/words/telemetry/events.jsonl   # lifecycle/action events, no tokens
curl -s http://127.0.0.1:18080/metrics/prometheus | head
```

## Pre-deploy checklist (LLM-drafted with qwen2.5:1.5b on the host, cleaned)

1. Verify Postgres is up and the `wordarena` DSN connects.
2. Confirm the JSONL telemetry path is writable.
3. Confirm `/healthz` returns `{"status":"ok"}` after restart.
4. Confirm `/readyz` reports `ready` with the expected `storage` backend.
5. Use `/healthz` for liveness probes and `/readyz` for traffic gating.
6. Exercise graceful shutdown (`systemctl restart`) while idle and under a live match.
7. Re-run the headless-bot exit gate after every binary change.
8. Keep `/opt/words/bin/wordarena-server.prev` for one-step rollback.

## Known constraints

- Loopback-only exposure: the Unity client can only reach the server from the
  host itself or an authorized tunnel. Public exposure requires an edge/TLS
  decision (docs/ARCHITECTURE.md) before enabling.
- Single process; no horizontal scale-out yet (M1 scope; see
  docs/M1-ARCH-PREP.md).
