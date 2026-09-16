#!/usr/bin/env bash
# Run the M2 load harness against an ISOLATED game-server instance.
#
# Why a script rather than a documented command line: the one thing a load test
# must never do is touch the live service, so the safe path has to be the easy
# path. This starts its own server on its own port with its own (in-memory)
# storage, drives it, tears it down, and leaves the systemd service alone.
#
# Usage:
#   tools/loadtest-isolated.sh                       # default stage: 8 matches x 2 seats, 30 s
#   tools/loadtest-isolated.sh 20 2 60               # matches, seats, seconds
#   SEATS=60 MATCHES=3 DURATION=45 tools/loadtest-isolated.sh
#   GAME_BIN=/opt/words/bin/wordarena tools/loadtest-isolated.sh   # reuse a build
#
# Ports: WORDARENA_LOADTEST_PORT (default 18080) - deliberately not the service
# port, so a mistake cannot reach the live instance.
set -euo pipefail

MATCHES="${1:-${MATCHES:-8}}"
SEATS="${2:-${SEATS:-2}}"
DURATION="${3:-${DURATION:-30}}"
PORT="${WORDARENA_LOADTEST_PORT:-18080}"
INTENT_GAP_MS="${INTENT_GAP_MS:-2000}"
LANG="${LANG_CODE:-en}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO="${GO:-go}"
WORKDIR="$(mktemp -d /tmp/loadtest-isolated.XXXXXX)"
GAME_BIN="${GAME_BIN:-}"

export GOMODCACHE="${GOMODCACHE:-/home/user/go/pkg/mod}"
export GOCACHE="${GOCACHE:-/home/user/go/cache}"
export GOPATH="${GOPATH:-/home/user/go}"
# Batch 35C hygiene: this script used to prepend /home/user/go-toolchain/go/bin
# to PATH - a path that no longer exists in any environment it runs in (the
# sandbox was wiped; the server toolchain lives at
# /home/ubuntu/go-toolchain/bin). If go is not on PATH, say so instead of
# silently relying on a stale path.
if ! command -v "$GO" >/dev/null; then
  echo "go not found on PATH; set GO=/path/to/go (e.g. GO=/home/ubuntu/go-toolchain/bin/go)" >&2
  exit 1
fi

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

if [[ -z "$GAME_BIN" ]]; then
  echo "== building target server and harness"
  (cd "$ROOT/server" && "$GO" build -o "$WORKDIR/wordarena" ./cmd/game && "$GO" build -o "$WORKDIR/loadtest" ./cmd/loadtest)
  GAME_BIN="$WORKDIR/wordarena"
  HARNESS="$WORKDIR/loadtest"
else
  (cd "$ROOT/server" && "$GO" build -o "$WORKDIR/loadtest" ./cmd/loadtest)
  HARNESS="$WORKDIR/loadtest"
fi

# In-memory storage on purpose: this measures the match transport and the room
# tickers. Postgres throughput is a separate question with its own bottleneck,
# and mixing them would make neither number actionable. Point WORDARENA_POSTGRES_DSN
# at a throwaway database to include it.
echo "== starting isolated server on 127.0.0.1:$PORT (no WORDARENA_POSTGRES_DSN)"
env -u WORDARENA_POSTGRES_DSN \
  WORDARENA_ADDR="127.0.0.1:$PORT" \
  WORDARENA_MAX_ROOMS="${WORDARENA_MAX_ROOMS:-512}" \
  WORDARENA_MAX_SEATS="${WORDARENA_MAX_SEATS:-60}" \
  "$GAME_BIN" >"$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
echo "$SERVER_PID" >"$WORKDIR/server.pid"

echo "== waiting for /healthz"
for i in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "server exited during startup:"; cat "$WORKDIR/server.log"; exit 1
  fi
  sleep 0.5
done

echo "== load: $MATCHES matches x $SEATS seats for ${DURATION}s, intent every ${INTENT_GAP_MS}ms"
"$HARNESS" \
  -url "http://127.0.0.1:$PORT" \
  -matches "$MATCHES" -seats "$SEATS" \
  -duration "${DURATION}s" -intent-gap "${INTENT_GAP_MS}ms" \
  -language "$LANG" \
  -server-pid-file "$WORKDIR/server.pid" \
  -json "${REPORT_JSON:-$ROOT/loadtest-report.json}" || true

echo "== server log tail"
tail -5 "$WORKDIR/server.log" || true
echo "== report written to ${REPORT_JSON:-$ROOT/loadtest-report.json}"
