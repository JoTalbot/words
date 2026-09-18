#!/usr/bin/env bash
# Off-box generator stage, TARGET side (M2 batch 35C, docs/M2-LOAD-TESTING.md).
#
# The in-memory stages run the generator and the server on the same box, so
# the generator's own CPU is part of the server's measured environment. This
# script is the other half of a two-box run: it starts ONE isolated server on
# a loopback port and does nothing else - the load generator runs on a
# DIFFERENT machine and reaches this port through an SSH tunnel
# (ssh -N -L 18080:127.0.0.1:18080 <this-host>), so the tunnel carries only
# client traffic (tens of KB/s; sshd crypto cost is negligible at that rate)
# and the target box runs nothing but the server and a 1 Hz sampler.
#
# It samples the server's CPU (utime+stime jiffies) and RSS every second into
# samples.csv, and on exit captures /metrics + /metrics/prometheus into the
# workdir, so the run leaves the same evidence the co-located harness would
# have produced via -server-pid-file.
#
# Usage (on the target host, from the repo root):
#   tools/loadtest-offbox-target.sh [duration-seconds] [port]
#   DURATION=120 PORT=18080 tools/loadtest-offbox-target.sh
# Stop early: touch "$WORKDIR/stop" (path is printed).
set -euo pipefail

DURATION="${1:-${DURATION:-120}}"
PORT="${2:-${PORT:-18080}}"

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

REPORT_DIR="${REPORT_DIR:-./artifacts-35c-offbox}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
refuse_if_port_busy  # fail fast, before any build
GO="${GO:-go}"

command -v "$GO" >/dev/null || { echo "go not found; set GO=/path/to/go" >&2; exit 1; }

mkdir -p "$REPORT_DIR"
WORKDIR="$(mktemp -d /tmp/loadtest-offbox.XXXXXX)"
cleanup() {
  # Capture metrics BEFORE killing the server - a dead process serves
  # nothing (found by the batch 35C self-test: empty metrics files).
  curl -fsS "http://127.0.0.1:$PORT/metrics" > "$REPORT_DIR/target-metrics.json" 2>/dev/null || true
  curl -fsS "http://127.0.0.1:$PORT/metrics/prometheus" > "$REPORT_DIR/target-metrics.prom" 2>/dev/null || true
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

echo "== building isolated server"
(cd "$ROOT/server" && "$GO" build -o "$WORKDIR/wordarena" ./cmd/game)

refuse_if_port_busy
echo "== starting target on 127.0.0.1:$PORT (no DSN: in-memory on purpose -
   this stage isolates the generator, the database stage is loadtest-pg.sh)"
env -u WORDARENA_POSTGRES_DSN \
  WORDARENA_ADDR="127.0.0.1:$PORT" \
  WORDARENA_MAX_ROOMS="${WORDARENA_MAX_ROOMS:-512}" \
  WORDARENA_MAX_SEATS="${WORDARENA_MAX_SEATS:-60}" \
  "$WORKDIR/wordarena" > "$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
echo "$SERVER_PID" > "$WORKDIR/server.pid"

for _ in $(seq 1 60); do
  curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  kill -0 "$SERVER_PID" 2>/dev/null || { echo "server died:"; tail -5 "$WORKDIR/server.log"; exit 1; }
  sleep 0.5
done
assert_our_listener "$SERVER_PID"

{
  printf '{"date":"%s","nproc":%s,"loadavg_start":"%s","port":%s}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(nproc)" "$(cut -d' ' -f1-3 /proc/loadavg)" "$PORT"
} > "$REPORT_DIR/target-env.json"

echo "== sampling CPU/RSS at 1 Hz for ${DURATION}s"
echo "t,cpu_jiffies,rss_kb" > "$REPORT_DIR/samples.csv"
HZ="$(getconf CLK_TCK)"
START="$(date +%s)"
while :; do
  NOW="$(date +%s)"
  (( NOW - START >= DURATION )) && break
  [[ -f "$WORKDIR/stop" ]] && break
  if [[ -r "/proc/$SERVER_PID/stat" ]]; then
    # utime+stime are fields 14+15; RSS (kB) is VmRSS in status.
    read -r _ _ _ _ _ _ _ _ _ _ _ _ _ U S _ < "/proc/$SERVER_PID/stat" || true
    RSS="$(awk '/VmRSS/{print $2}' "/proc/$SERVER_PID/status" 2>/dev/null || echo 0)"
    echo "$((NOW - START)),$((U + S)),${RSS:-0}" >> "$REPORT_DIR/samples.csv"
  fi
  sleep 1
done
# CPU summary: first-to-last jiffy delta over elapsed seconds, normalized by
# nproc and CLK_TCK.
awk -F, -v hz="$HZ" -v nproc="$(nproc)" '
  NR==2 {first=$2; t0=$1}
  NR>2 {last=$2; t1=$1}
  END {
    if (NR>2 && t1>t0) {
      printf "target cpu seconds (normalized to 1 core): %.2f over %ds => %.4f cores\n", (last-first)/hz, t1-t0, (last-first)/hz/(t1-t0)
    } else { print "no samples" }
  }' "$REPORT_DIR/samples.csv" | tee "$REPORT_DIR/target-cpu-summary.txt"
cut -d' ' -f1-3 /proc/loadavg > "$REPORT_DIR/loadavg-end.txt" || true
echo "== target window over; metrics captured to $REPORT_DIR"
