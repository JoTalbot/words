#!/usr/bin/env bash
# Postgres-backed load stage (M2 batch 35C, docs/M2-LOAD-TESTING.md).
#
# The in-memory stages in loadtest-isolated.sh measure the match transport and
# the room tickers. This script measures what a durable store ADDS to that: it
# creates a THROWAWAY database, applies the real migrations, runs the same
# stage twice - once with the throwaway Postgres behind the server, once
# in-memory back to back on the same box - and leaves both reports side by
# side so the delta is the database, not the environment.
#
# Safety: the one thing this script must never do is touch a database that
# anything else uses. It only ever creates and drops the database named by
# LT_DB (default wordarena_lt), refuses to run if the base DSN already points
# at that name's live counterpart, and drops only its own throwaway name.
#
# Usage:
#   tools/loadtest-pg.sh                      # 8 matches x 2 seats, 30 s
#   tools/loadtest-pg.sh 20 2 60              # matches, seats, seconds
#   LT_ADMIN_PSQL="sudo -u postgres psql" tools/loadtest-pg.sh
#
# Environment:
#   LT_DSN        full DSN to the THROWAWAY database (overrides derivation)
#   LT_BASE_DSN   deployment DSN to derive credentials/host from; default:
#                 parsed from /etc/wordarena/wordarena.env (WORDARENA_POSTGRES_DSN)
#   LT_ADMIN_PSQL how to run psql as a SUPERUSER on the target Postgres
#                 (default "sudo -u postgres psql"); needs CREATE/DROP DATABASE
#   LT_DB         throwaway database name (default wordarena_lt)
#   REPORT_DIR    where to write reports (default ./artifacts-35c-pg)
set -euo pipefail

MATCHES="${1:-${MATCHES:-8}}"
SEATS="${2:-${SEATS:-2}}"
DURATION="${3:-${DURATION:-30}}"
PORT="${WORDARENA_LOADTEST_PORT:-18080}"

# ---- port safety guard (batch 35C): see tools/loadtest-isolated.sh --------
# A load stage must never talk to anything it did not start. On the OCI
# measurement host the LIVE service owns 127.0.0.1:18080.
port_in_use() {
  ss -tln 2>/dev/null | awk -v p=":$PORT" 'NR>1 && $4 ~ p"$" {f=1} END{exit !f}'
}
refuse_if_port_busy() {
  if port_in_use; then
    echo "REFUSING to start: something already listens on $PORT:" >&2
    ss -tlnp 2>/dev/null | awk -v p=":$PORT" 'NR>1 && $4 ~ p"$" {print "  " $0}' >&2
    exit 1
  fi
}
assert_our_listener() {
  if ! ss -tlnp 2>/dev/null | grep -q "pid=$1,"; then
    echo "REFUSING to continue: /healthz answered, but the listener on $PORT is not our server (pid $1)." >&2
    exit 1
  fi
}

INTENT_GAP_MS="${INTENT_GAP_MS:-2000}"
LANG_CODE="${LANG_CODE:-en}"
LT_DB="${LT_DB:-wordarena_lt}"
REPORT_DIR="${REPORT_DIR:-./artifacts-35c-pg}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO="${GO:-go}"

command -v "$GO" >/dev/null || { echo "go not found; set GO=/path/to/go" >&2; exit 1; }

# --- resolve the base DSN (credentials + host), never the live database ----
BASE_DSN="${LT_BASE_DSN:-}"
if [[ -z "$BASE_DSN" ]]; then
  ENV_FILE="${LT_ENV_FILE:-/etc/wordarena/wordarena.env}"
  if [[ -r "$ENV_FILE" ]]; then
    BASE_DSN="$(sed -n 's/^WORDARENA_POSTGRES_DSN=//p' "$ENV_FILE" | tail -1 | tr -d '"')"
  elif command -v sudo >/dev/null && sudo -n test -r "$ENV_FILE" 2>/dev/null; then
    BASE_DSN="$(sudo -n sed -n 's/^WORDARENA_POSTGRES_DSN=//p' "$ENV_FILE" | tail -1 | tr -d '"')"
  fi
fi
if [[ -z "$BASE_DSN" ]]; then
  echo "no base DSN: set LT_BASE_DSN or make $LT_ENV_FILE readable" >&2
  exit 1
fi

# postgres://user:pass@host:port/dbname?params -> keep everything but the path
DSN_RE='^postgres(ql)?://([^:/@]+):([^@]+)@([^/?:]+)(:[0-9]+)?/([^?]+)(\?.*)?$'
[[ "$BASE_DSN" =~ $DSN_RE ]] || { echo "cannot parse base DSN" >&2; exit 1; }
PG_USER="${BASH_REMATCH[2]}"
PG_PASS="${BASH_REMATCH[3]}"
PG_HOST="${BASH_REMATCH[4]}"
PG_PORT="${BASH_REMATCH[5]}"
PG_DBNAME="${BASH_REMATCH[6]}"
PG_PARAMS="${BASH_REMATCH[7]:-}"
if [[ "$PG_DBNAME" == "$LT_DB" ]]; then
  echo "refusing: the base DSN already points at $LT_DB - LT_DB must be a throwaway name" >&2
  exit 1
fi
LT_DSN="postgres://${PG_USER}:${PG_PASS}@${PG_HOST}${PG_PORT}/${LT_DB}${PG_PARAMS}"

ADMIN="${LT_ADMIN_PSQL:-sudo -u postgres psql}"
command -v psql >/dev/null || { echo "psql not found on PATH" >&2; exit 1; }

refuse_if_port_busy  # fail fast, before any build
mkdir -p "$REPORT_DIR"
WORKDIR="$(mktemp -d /tmp/loadtest-pg.XXXXXX)"
cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORKDIR"
  # The throwaway database is the script's own; dropping it is cleanup, not a
  # destructive act against shared state.
  $ADMIN -c "DROP DATABASE IF EXISTS $LT_DB" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== building server, harness and migrate"
(cd "$ROOT/server" && "$GO" build -o "$WORKDIR/wordarena" ./cmd/game \
  && "$GO" build -o "$WORKDIR/loadtest" ./cmd/loadtest \
  && "$GO" build -o "$WORKDIR/migrate" ./cmd/migrate)

echo "== environment sidecar"
{
  printf '{"date":"%s","nproc":%s,"loadavg":"%s","kernel":"%s"}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(nproc)" \
    "$(cut -d' ' -f1-3 /proc/loadavg)" "$(uname -r)"
} > "$REPORT_DIR/pgstage-env.json"

echo "== creating throwaway database $LT_DB on ${PG_HOST}${PG_PORT}"
$ADMIN -c "DROP DATABASE IF EXISTS $LT_DB"
$ADMIN -c "CREATE DATABASE $LT_DB OWNER $PG_USER"

echo "== applying migrations to $LT_DB"
"$WORKDIR/migrate" -dsn "$LT_DSN" -dir "$ROOT/infra/migrations"

run_stage() { # $1 = label, $2 = report path, $3... = extra server env pairs
  local label="$1" report="$2"
  shift 2
  refuse_if_port_busy
  echo "== stage [$label]: $MATCHES matches x $SEATS seats for ${DURATION}s"
  env "$@" WORDARENA_ADDR="127.0.0.1:$PORT" \
    WORDARENA_MAX_ROOMS="${WORDARENA_MAX_ROOMS:-512}" \
    WORDARENA_MAX_SEATS="${WORDARENA_MAX_SEATS:-60}" \
    "$WORKDIR/wordarena" > "$WORKDIR/server-$label.log" 2>&1 &
  SERVER_PID=$!
  echo "$SERVER_PID" > "$WORKDIR/server.pid"
  for _ in $(seq 1 60); do
    curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
    kill -0 "$SERVER_PID" 2>/dev/null || { echo "server died:"; tail -5 "$WORKDIR/server-$label.log"; return 1; }
    sleep 0.5
  done
  assert_our_listener "$SERVER_PID"
  "$WORKDIR/loadtest" \
    -url "http://127.0.0.1:$PORT" \
    -matches "$MATCHES" -seats "$SEATS" \
    -duration "${DURATION}s" -intent-gap "${INTENT_GAP_MS}ms" \
    -language "$LANG_CODE" \
    -server-pid-file "$WORKDIR/server.pid" \
    -json "$report" || true
  curl -fsS "http://127.0.0.1:$PORT/metrics" > "$REPORT_DIR/metrics-$label.json" || true
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
}

# The Postgres leg FIRST, so if anything fails the throwaway DB still gets
# dropped by the trap and the in-memory comparison leg is not half-run.
run_stage pg "$REPORT_DIR/loadtest-pg.json" "WORDARENA_POSTGRES_DSN=$LT_DSN"
run_stage mem "$REPORT_DIR/loadtest-mem.json" "WORDARENA_POSTGRES_DSN="

echo "== reports"
for f in "$REPORT_DIR"/loadtest-pg.json "$REPORT_DIR"/loadtest-mem.json; do
  echo "--- $f"
  command -v jq >/dev/null && jq '{create_p50_ms,create_p95_ms,latency_p50_ms,latency_p95_ms,latency_p99_ms,intents_sent,intents_acked,server_cpu_s,server_rss_end_mb}' "$f" || cat "$f"
done
echo "== done (throwaway database $LT_DB dropped by exit trap)"
