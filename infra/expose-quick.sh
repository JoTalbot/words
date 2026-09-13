#!/usr/bin/env bash
# Word Arena - temporary public exposure of the loopback match service
# (Q8 decision 2026-09-13, stage 1: Cloudflare quick tunnel).
#
# Run ON THE OCI HOST (129.213.177.56) as the ubuntu user:
#   bash infra/expose-quick.sh            # expose, print the public URL
#   bash infra/expose-quick.sh --status   # show tunnel state + current URL
#   bash infra/expose-quick.sh --stop     # rollback to loopback-only
#
# What it does:
#   - verifies the local origin (127.0.0.1:18080) is healthy FIRST
#   - installs cloudflared to /opt/words/bin if missing
#   - installs infra/words-tunnel.service, enables + starts it
#   - reads the ephemeral https://<random>.trycloudflare.com URL from the
#     tunnel log and verifies /healthz + /readyz THROUGH the public URL
#   - prints the URL, the stop command, and the rotation note
#
# The origin service itself is not touched at any point: no listen-address
# change, no config change, no restart. Rollback is one command and takes
# the public URL down within seconds while the service keeps serving
# loopback clients (headless-bot, host-local tools).
set -euo pipefail

ORIGIN="${WORDARENA_ORIGIN:-http://127.0.0.1:18080}"
CFD_BIN="/opt/words/bin/cloudflared"
# Architecture-aware binary selection (the OCI host is aarch64; a dev box
# or a stage-2 host may be x86_64).
case "$(uname -m)" in
  x86_64)  CFD_ARCH="amd64" ;;
  aarch64) CFD_ARCH="arm64" ;;
  *) echo "[expose] unsupported architecture: $(uname -m)"; exit 1 ;;
esac
CFD_URL="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${CFD_ARCH}"
UNIT_SRC="$(cd "$(dirname "$0")" && pwd)/words-tunnel.service"
UNIT_DST="/etc/systemd/system/words-tunnel.service"
LOG="/var/log/words-tunnel.log"

log() { printf '[expose] %s\n' "$*"; }

origin_healthy() {
  curl -sf -m 5 "$ORIGIN/healthz" >/dev/null 2>&1 \
    && curl -sf -m 5 "$ORIGIN/readyz" >/dev/null 2>&1
}

read_public_url() {
  # The quick tunnel prints its edge URL to the log on startup.
  sudo grep -oE 'https://[a-z0-9-]+\.[a-z]+\.trycloudflare\.com' "$LOG" 2>/dev/null | tail -1 || true
}

case "${1:-}" in
  --stop)
    log "stopping the tunnel (rollback to loopback-only)"
    sudo systemctl disable --now words-tunnel 2>/dev/null || true
    if systemctl is-active --quiet words-tunnel 2>/dev/null; then
      log "ERROR: tunnel still active"; exit 1
    fi
    log "stopped. The service is back to loopback-only; URL is dead."
    exit 0
    ;;
  --status)
    if systemctl is-active --quiet words-tunnel 2>/dev/null; then
      log "tunnel active; public URL: $(read_public_url || echo '<not in log yet>')"
    else
      log "tunnel inactive (loopback-only mode)"
    fi
    exit 0
    ;;
esac

log "origin check: $ORIGIN"
if ! origin_healthy; then
  log "ERROR: origin is not healthy - refusing to expose a broken service"
  exit 1
fi

if [ ! -x "$CFD_BIN" ]; then
  log "installing cloudflared to $CFD_BIN"
  mkdir -p "$(dirname "$CFD_BIN")"
  curl -sfL -m 120 -o "$CFD_BIN.tmp" "$CFD_URL"
  chmod +x "$CFD_BIN.tmp"
  mv "$CFD_BIN.tmp" "$CFD_BIN"
fi
log "cloudflared: $("$CFD_BIN" --version 2>/dev/null | head -1)"

log "installing systemd unit"
sudo cp "$UNIT_SRC" "$UNIT_DST"
sudo systemctl daemon-reload
sudo systemctl enable --now words-tunnel

log "waiting for the public URL in $LOG"
url=""
for _ in $(seq 1 30); do
  url=$(read_public_url)
  [ -n "$url" ] && break
  sleep 2
done
if [ -z "$url" ]; then
  log "ERROR: no public URL appeared; tail of $LOG:"
  sudo tail -20 "$LOG" || true
  log "rollback with: bash infra/expose-quick.sh --stop"
  exit 1
fi

log "verifying health THROUGH the public URL"
if ! curl -sf -m 10 "$url/healthz" >/dev/null 2>&1 || ! curl -sf -m 10 "$url/readyz" >/dev/null 2>&1; then
  log "ERROR: public URL did not serve /healthz + /readyz; URL was: $url"
  exit 1
fi

cat <<EOF

[expose] PUBLIC URL:  $url
[expose] Client:      enter this URL in the Unity client's Server field
                      (or push it via the 27B file channel for device runs).
[expose] Stop:        bash infra/expose-quick.sh --stop
[expose] Note:        the URL is EPHEMERAL - restarting the tunnel rotates
                      it. Stage 2 (stable name) needs a domain; see
                      docs/PRODUCT-DECISIONS.md Q8 and
                      docs/SECURITY-EXPOSURE.md.
EOF
