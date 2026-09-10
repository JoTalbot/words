# Monitoring

The match service exposes Prometheus text metrics at `GET /metrics/prometheus`
and JSON counters at `GET /metrics` (see `docs/M1-TELEMETRY.md`). Until this
directory existed, nothing scraped either endpoint, so the counters were only
readable by hand.

```text
infra/monitoring/
├── prometheus/
│   └── wordarena.yml          # file_sd target list for the match service
└── grafana/
    └── dashboards/
        └── wordarena.json     # "Word Arena — Match Service" dashboard
```

## Prometheus

`prometheus/wordarena.yml` is a `file_sd` target file, so it can be reloaded
without restarting Prometheus:

```yaml
# /etc/prometheus/prometheus.yml
global:
  scrape_interval: 15s
scrape_configs:
  - job_name: wordarena
    metrics_path: /metrics/prometheus
    file_sd_configs:
      - files: ['/etc/prometheus/wordarena.yml']
```

Two notes that matter for this service:

- **Use `/metrics/prometheus`, not `/metrics`.** `/metrics` returns the JSON
  counter document for humans and ad-hoc scripts; it is not a valid Prometheus
  exposition format.
- **Counters are per process.** `wordarena_matches_created_total` and friends
  reset when the service restarts. Grafana panels therefore plot
  `rate(...[1m])` rather than raw counters, and a restart shows as a rate drop
  rather than a cliff.

### Scraping the systemd deployment

The service listens on loopback only (`127.0.0.1:18080`, see
`docs/M1-OPS.md` and `PRODUCT-DECISIONS.md` Q8). A Prometheus on the same host
can scrape it directly with the file above. A Prometheus elsewhere cannot, and
must not be given access by widening the listen address — that is the Q8 edge
and TLS decision, not a monitoring detail.

### Scraping the compose stack

The compose `db` service is not published to the host, and the `game` service
publishes only its HTTP port. Add a temporary port publication through an
override file rather than editing the committed compose file:

```bash
cat > /tmp/monitoring-override.yml <<'YAML'
services:
  game:
    ports:
      - "18090:8080"
YAML
docker compose -f infra/docker-compose.yml -f /tmp/monitoring-override.yml up -d
```

## Grafana

Import `grafana/dashboards/wordarena.json` (Dashboards → New → Import → upload
JSON). It expects a Prometheus datasource; Grafana will prompt for one on
import.

The dashboard is a 3x3 grid covering every metric the service emits:

| Panel | Type | Query |
|---|---|---|
| Active matches | stat | `wordarena_active_matches` |
| Match creation rate | timeseries | `rate(wordarena_matches_created_total[1m])` |
| Match completion rate | timeseries | `rate(wordarena_matches_finished_total[1m])` |
| Intent rate | timeseries | `rate(wordarena_intents_received_total[1m])` |
| Intent outcome | timeseries | `rate(wordarena_words_accepted_total[1m])`, `rate(wordarena_words_rejected_total[1m])` |
| Telemetry enqueue rate | timeseries | `rate(wordarena_telemetry_events_enqueued_total[1m])` |
| Telemetry write rate | timeseries | `rate(wordarena_telemetry_events_written_total[1m])` |
| Telemetry backpressure | timeseries | `rate(wordarena_telemetry_events_dropped_total[1m])`, `rate(wordarena_telemetry_export_errors_total[1m])` |
| Matches created (process total) | stat | `wordarena_matches_created_total` |

`tools/check-monitoring-assets.py` asserts that every metric the code emits is
plotted and that no panel references a metric that does not exist, which is the
usual way a dashboard silently renders an empty graph.

Backpressure is the panel to alert on. Telemetry is deliberately non-blocking
(`docs/M1-TELEMETRY.md`): a saturated JSONL buffer drops events instead of
slowing a match, so a rising `dropped_total` means observability is being lost
while gameplay looks perfectly healthy.

## Metric reference

| Metric | Type | Meaning |
|---|---|---|
| `wordarena_matches_created_total` | counter | matches created by this process |
| `wordarena_matches_finished_total` | counter | matches that reached `over` |
| `wordarena_intents_received_total` | counter | word intents received |
| `wordarena_words_accepted_total` | counter | accepted intents |
| `wordarena_words_rejected_total` | counter | rejected intents |
| `wordarena_active_matches` | gauge | live rooms |
| `wordarena_telemetry_events_enqueued_total` | counter | events accepted into the async buffer |
| `wordarena_telemetry_events_written_total` | counter | events written by the exporter |
| `wordarena_telemetry_events_dropped_total` | counter | events dropped to backpressure or shutdown |
| `wordarena_telemetry_export_errors_total` | counter | exporter write or flush errors |

Metric names are asserted by `tools/check-monitoring-assets.py`, which parses
the live metric names out of `server/cmd/game/telemetry.go` and fails if the
dashboard or this table references something the service does not emit.
