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
# Ports: WORDARENA_LOADTEST_PORT (default 18080). WARNING (batch 35C): on the
# OCI measurement host the LIVE service itself listens on 127.0.0.1:18080 -
# the port guard below refuses to run in that case; pick another port there.
set -euo pipefail

MATCHES="${1:-${MATCHES:-8}}"
SEATS="${2:-${SEATS:-2}}"
DURATION="${3:-${DURATION:-30}}"
PORT="${WORDARENA_LOADTEST_PORT:-18080}"

# ---- port safety guard (batch 35C) ---------------------------------------
# A load stage must never talk to anything it did not start. On the OCI
# measurement host the LIVE systemd service owns 127.0.0.1:18080 (the old
# comment claiming 18080 is "deliberately not the service port" was written
# for the sandbox/runner where nothing else listens, and was WRONG on that
# host - batch 35C found out the hard way when a default-port stage silently
# drove the live service). Two guards close the hole: refuse to start when
# anything already listens on the port, and after /healthz answers, verify
# the listener is OUR process, not whoever was there first.
port_in_use() {
  ss -tln 2>/dev/null | awk -v p=":$PORT" 'NR>1 && $4 ~ p"$" {f=1} END{exit !f}'
}
refuse_if_port_busy() {
  if port_in_use; then
    echo "REFUSING to start: something already listens on $PORT:" >&2
    ss -tlnp 2>/dev/null | awk -v p=":$PORT" 'NR>1 && $4 ~ p"$" {print "  " $0}' >&2
    echo "  If that is the live wordarena service, pick another port" >&2
    echo "  (WORDARENA_LOADTEST_PORT / PORT env)." >&2
    exit 1
  fi
}
assert_our_listener() {
  # $1 = server pid, $2 = server log path. Two failure causes are kept
  # distinct: a foreign listener answered /healthz (the hole this guard
  # closes), or OUR server crashed between healthz and this check (the log
  # is the evidence).
  if ! port_in_use; then
    echo "OUR SERVER (pid $1) ANSWERED /healthz AND IS NOW GONE - it crashed at startup; log tail:" >&2
    tail -10 "${2:-/dev/null}" >&2 || true
    exit 1
  fi
  if ! ss -tlnp 2>/dev/null | grep -q "pid=$1,"; then
    echo "REFUSING to continue: /healthz answered, but the listener on $PORT is not our server (pid $1):" >&2
    ss -tlnp 2>/dev/null | awk -v p=":$PORT" 'NR>1 && $4 ~ p"$" {print "  " $0}' >&2
    exit 1
  fi
}

INTENT_GAP_MS="${INTENT_GAP_MS:-2000}"
LANG="${LANG_CODE:-en}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
refuse_if_port_busy  # fail fast, before any build
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
refuse_if_port_busy
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
assert_our_listener "$SERVER_PID" "$WORKDIR/server.log"

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
