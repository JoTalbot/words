# M1 — Telemetry baseline

Status: **accepted (M1)**. The game service now exposes scrapeable counters
and can export append-only operational events without making analytics a
runtime dependency of authoritative gameplay.

## 1. Surfaces

- `GET /metrics` — JSON counters for simple smoke tests and local tooling.
- `GET /metrics/prometheus` — Prometheus text exposition (`text/plain;
  version=0.0.4`) with the same counters and exporter health metrics.
- `WORDARENA_TELEMETRY_JSONL=/path/to/events.jsonl` — optional async JSONL
  event export. When unset, the service runs with counters only.
- `WORDARENA_TELEMETRY_BUFFER` — exporter queue depth (default `4096`, min
  `1`). `Publish` never blocks the caller; if the buffer is full, the event is
  dropped and `telemetry_events_dropped`/`wordarena_telemetry_events_dropped_total`
  make the loss visible.

## 2. Metric names

JSON `/metrics` fields:

- `matches_created`
- `matches_finished`
- `intents_received`
- `words_accepted`
- `words_rejected`
- `active_matches`
- `telemetry_events_enqueued`
- `telemetry_events_written`
- `telemetry_events_dropped`
- `telemetry_export_errors`

Prometheus `/metrics/prometheus` names use the `wordarena_` prefix and
`_total` suffix for counters, for example
`wordarena_matches_created_total` and
`wordarena_telemetry_events_written_total`. `wordarena_active_matches` is a
gauge.

## 3. JSONL event contract

Each line is one JSON object with `version: 1`, UTC `time`, and `type`.
Events contain only server-authoritative facts plus correlation fields. Seat
tokens, credentials and raw request bodies are never exported.

Current event types:

- `profile_created` — registered profile id/language.
- `match_created` — match id, seed, language and Sudden Death flag.
- `ws_connected` / `ws_disconnected` — match id, seat and user id.
- `word_validated` — match id, seat, user id, `client_sequence` correlation,
  normalized word, validation result, score delta/total, steal flag,
  state version and server tick.
- `intent_rate_limited` — rejected-at-transport intent with match id, seat,
  user id and client sequence.
- `seat_token_rotated` — match id, seat and user id; the token value is never
  logged/exported.
- `match_finished` — match id, seed, language, winner/tie, scores, final
  state version and server tick.

`client_sequence` remains telemetry/correlation only; it never orders or
validates competitive outcomes. Client timestamps and touch paths are still
non-authoritative per `docs/WIRE-PROTOCOL.md`.

## 4. Failure semantics

Telemetry export is deliberately best-effort:

- a bad JSONL path logs an error and the service continues with counters only;
- event publication is non-blocking and bounded by the configured buffer;
- exporter write/flush errors increment `telemetry_export_errors`;
- gameplay, score, board state, persistence and replay are never gated on an
  analytics sink.

This preserves the architecture rule that product analytics must not be a
synchronous match-path dependency.

## 5. Verification

Regression coverage:

```bash
cd server
go test ./cmd/game -run 'Test(PrometheusMetricsEndpoint|TelemetryJSONLExport)' -count=1 -v
go vet ./...
go test ./...
```

`TestTelemetryJSONLExport` verifies that meaningful zero values such as
`seat: 0` are retained in exported events.
