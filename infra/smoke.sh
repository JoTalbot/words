#!/usr/bin/env bash
# Word Arena — container/compose smoke verification.
#
# Verifies that a running game service is actually usable end to end, not
# merely listening. Used by:
#   - .github/workflows/container-smoke.yml (GitHub-hosted runner)
#   - manual/OPS verification against any deployment (docs/M1-CONTAINERS.md)
#
# Usage:
#   infra/smoke.sh [BASE_URL]           # default http://127.0.0.1:18080
#
# Exit status is non-zero if any check fails. Every check is independent: the
# script keeps going after a failure so a single run reports every problem.

set -uo pipefail

BASE="${1:-${SMOKE_BASE_URL:-http://127.0.0.1:18080}}"
BASE="${BASE%/}"

PASS_COUNT=0
FAIL_COUNT=0

ok()   { printf 'PASS  %s\n' "$1"; PASS_COUNT=$((PASS_COUNT + 1)); }
bad()  { printf 'FAIL  %s\n' "$1"; FAIL_COUNT=$((FAIL_COUNT + 1)); }
info() { printf '      %s\n' "$1"; }

# check <description> <expected> <actual>
check() {
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$2', got '$3')"; fi
}

# check_in <description> <expected csv> <actual>
check_in() {
  case ",$2," in
    *",$3,"*) ok "$1" ;;
    *) bad "$1 (expected one of '$2', got '$3')" ;;
  esac
}

# json_field <json> <field> — minimal scalar extraction (string or number).
json_field() {
  printf '%s' "$1" | python3 -c '
import json, sys
try:
    doc = json.load(sys.stdin)
except Exception:
    sys.exit(0)
cur = doc
for part in sys.argv[1].split("."):
    if isinstance(cur, dict) and part in cur:
        cur = cur[part]
    else:
        sys.exit(0)
print(cur if not isinstance(cur, bool) else str(cur).lower())
' "$2" 2>/dev/null
}

need() {
  command -v "$1" >/dev/null 2>&1 || { bad "required tool '$1' is not installed"; exit 2; }
}

need curl
need python3

printf 'Word Arena smoke — %s\n\n' "$BASE"

# ---------------------------------------------------------------- liveness ---
CODE=$(curl -s -o /tmp/wa_healthz.json -w '%{http_code}' -m 10 "$BASE/healthz")
check "GET /healthz returns 200" "200" "$CODE"
check "/healthz reports status ok" "ok" "$(json_field "$(cat /tmp/wa_healthz.json)" status)"

# --------------------------------------------------------------- readiness ---
CODE=$(curl -s -o /tmp/wa_readyz.json -w '%{http_code}' -m 10 "$BASE/readyz")
check "GET /readyz returns 200" "200" "$CODE"
READY_STATUS=$(json_field "$(cat /tmp/wa_readyz.json)" status)
check "/readyz reports ready" "ready" "$READY_STATUS"
READY_STORAGE=$(json_field "$(cat /tmp/wa_readyz.json)" storage)
info "storage backend: ${READY_STORAGE:-unknown}"
if [ "${SMOKE_EXPECT_STORAGE:-}" != "" ]; then
  check "/readyz storage is $SMOKE_EXPECT_STORAGE" "$SMOKE_EXPECT_STORAGE" "$READY_STORAGE"
fi

# ----------------------------------------------------------------- metrics ---
CODE=$(curl -s -o /tmp/wa_metrics.json -w '%{http_code}' -m 10 "$BASE/metrics")
check "GET /metrics returns 200" "200" "$CODE"
if python3 -c 'import json,sys; d=json.load(open("/tmp/wa_metrics.json")); sys.exit(0 if "matches_created" in d else 1)' 2>/dev/null; then
  ok "/metrics exposes match counters"
else
  bad "/metrics does not expose match counters"
fi

CODE=$(curl -s -o /tmp/wa_prom.txt -w '%{http_code}' -m 10 "$BASE/metrics/prometheus")
check "GET /metrics/prometheus returns 200" "200" "$CODE"
if grep -q '^wordarena_' /tmp/wa_prom.txt 2>/dev/null; then
  ok "/metrics/prometheus exposes wordarena_* series"
else
  bad "/metrics/prometheus has no wordarena_* series"
fi

# ------------------------------------------------------------ match create ---
CODE=$(curl -s -o /tmp/wa_match.json -w '%{http_code}' -m 15 \
  -X POST "$BASE/v1/matches" \
  -H 'Content-Type: application/json' \
  -d '{"language":"en","seed":1512}')
check_in "POST /v1/matches returns 2xx" "200,201" "$CODE"

MATCH_ID=$(json_field "$(cat /tmp/wa_match.json)" match_id)
if [ -n "$MATCH_ID" ] && [ "$MATCH_ID" != "0" ]; then
  ok "match created (id $MATCH_ID)"
else
  bad "match creation returned no match id"
fi

# Batch 21g: the create response is the only place the unguessable match code
# and the per-match read capability are ever handed out, so their absence here
# means a client can never read the result once WORDARENA_REQUIRE_READ_CAPABILITY
# is set. Both must be 128 bits of hex; a short or empty value would be guessable
# and would reopen security finding S-2.
MATCH_CODE=$(json_field "$(cat /tmp/wa_match.json)" match_code)
READ_CAP=$(json_field "$(cat /tmp/wa_match.json)" read_capability)
for pair in "match_code:$MATCH_CODE" "read_capability:$READ_CAP"; do
  name=${pair%%:*}; value=${pair#*:}
  if printf '%s' "$value" | grep -qE '^[0-9a-f]{32}$'; then
    ok "$name issued as 128 bits of hex"
  else
    bad "$name is missing or not 32 hex chars (got '${value}')"
  fi
done
if [ -n "$MATCH_CODE" ] && [ "$MATCH_CODE" != "$READ_CAP" ]; then
  ok "match code and read capability are distinct"
else
  bad "match code and read capability must differ"
fi
# The code route must resolve even while the match is active (404, not 400): a
# malformed route would answer 400 and hide a wiring mistake behind the same
# status the id form legitimately returns.
CODE_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -m 10 \
  -H "Authorization: Bearer $READ_CAP" "$BASE/v1/matches/$MATCH_CODE/result")
check "GET /v1/matches/{code}/result is 404 while the match is active" "404" "$CODE_STATUS"

# Seat tokens are the only credential a client holds; two distinct seats must
# be issued or the match cannot be played by two players.
TOKEN0=$(printf '%s' "$(cat /tmp/wa_match.json)" | python3 -c '
import json,sys
try:
    print(json.load(sys.stdin)["tokens"][0])
except Exception:
    print("")' 2>/dev/null)
TOKEN1=$(printf '%s' "$(cat /tmp/wa_match.json)" | python3 -c '
import json,sys
try:
    print(json.load(sys.stdin)["tokens"][1])
except Exception:
    print("")' 2>/dev/null)

if [ -n "$TOKEN0" ] && [ -n "$TOKEN1" ] && [ "$TOKEN0" != "$TOKEN1" ]; then
  ok "two distinct seat tokens issued"
else
  bad "seat tokens missing or identical"
fi

# ----------------------------------------------------- WS authorization ---
if [ -n "${MATCH_ID:-}" ]; then
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 10 \
    "$BASE/v1/match/ws?match_id=$MATCH_ID&token=not-a-valid-token")
  check "WS rejects an invalid seat token with 401" "401" "$CODE"

  CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 10 \
    "$BASE/v1/match/ws?match_id=$MATCH_ID")
  check "WS rejects a missing seat token with 401" "401" "$CODE"
fi

# ------------------------------------------- live board requires a seat ---
# The REST snapshot exposes the in-progress board, locks and scores. Match
# ids are sequential, so an anonymous read would turn the service into a
# spectator feed anyone could enumerate. Both halves are pinned: the abuse is
# refused and an authorized seat still works.
if [ -n "${MATCH_ID:-}" ]; then
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 10 \
    "$BASE/v1/match/$MATCH_ID/snapshot")
  check "GET /v1/match/{id}/snapshot is 401 without a seat token" "401" "$CODE"

  CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 10 \
    "$BASE/v1/match/$MATCH_ID/snapshot?token=not-a-valid-token")
  check "GET /v1/match/{id}/snapshot is 403 with a bad seat token" "403" "$CODE"

  CODE=$(curl -s -o /tmp/wa_snapshot.json -w '%{http_code}' -m 10 \
    -H "Authorization: Bearer $TOKEN0" "$BASE/v1/match/$MATCH_ID/snapshot")
  check "GET /v1/match/{id}/snapshot is 200 with a bearer seat token" "200" "$CODE"
  PHASE=$(json_field "$(cat /tmp/wa_snapshot.json 2>/dev/null)" phase)
  check_in "authorized snapshot reports a match phase" "active,sudden_death,over" "$PHASE"
fi

# ------------------------------------------------- WS canonical snapshot ---
# A real RFC 6455 handshake over the standard library: the service must accept
# the upgrade and immediately push a binary canonical snapshot envelope.
if [ -n "${MATCH_ID:-}" ] && [ -n "${TOKEN0:-}" ]; then
  WS_HOST=$(printf '%s' "$BASE" | sed -e 's|^https\?://||' -e 's|/$||')
  WS_SCHEME=ws
  case "$BASE" in https://*) WS_SCHEME=wss ;; esac

  WS_OUT=$(SMOKE_WS_URL="$WS_SCHEME://$WS_HOST/v1/match/ws?match_id=$MATCH_ID&token=$TOKEN0" \
    python3 - <<'PY'
import base64, os, socket, sys, urllib.parse

url = urllib.parse.urlparse(os.environ["SMOKE_WS_URL"])
host = url.hostname
port = url.port or (443 if url.scheme == "wss" else 80)
key = base64.b64encode(os.urandom(16)).decode()

req = (
    f"GET {url.path}?{url.query} HTTP/1.1\r\n"
    f"Host: {host}:{port}\r\n"
    "Upgrade: websocket\r\n"
    "Connection: Upgrade\r\n"
    f"Sec-WebSocket-Key: {key}\r\n"
    "Sec-WebSocket-Version: 13\r\n"
    "\r\n"
)

try:
    s = socket.create_connection((host, port), timeout=15)
    s.settimeout(15)
    s.sendall(req.encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        chunk = s.recv(4096)
        if not chunk:
            break
        buf += chunk
except Exception as exc:  # noqa: BLE001 - report and exit non-zero
    print(f"connect-error: {exc}")
    sys.exit(1)

head, _, rest = buf.partition(b"\r\n\r\n")
status = head.split(b"\r\n")[0].decode(errors="replace")
if " 101" not in status:
    print(f"upgrade-rejected: {status.strip()}")
    sys.exit(1)

# Read the first frame. The service pushes a canonical snapshot immediately
# after the upgrade, so one frame is enough to prove the live path works.
data = rest
while len(data) < 2:
    chunk = s.recv(4096)
    if not chunk:
        break
    data += chunk
if len(data) < 2:
    print("no-frame")
    sys.exit(1)

opcode = data[0] & 0x0F
length = data[1] & 0x7F
if length == 126:
    length = int.from_bytes(data[2:4], "big")
elif length == 127:
    length = int.from_bytes(data[2:10], "big")

s.close()
print(f"upgraded opcode={opcode} payload_bytes={length}")
PY
  )
  WS_RC=$?
  if [ $WS_RC -eq 0 ] && printf '%s' "$WS_OUT" | grep -q 'opcode=2'; then
    ok "WebSocket upgrade accepted and binary snapshot received"
    info "$WS_OUT"
  else
    bad "WebSocket snapshot check failed: $WS_OUT"
  fi
fi

# ----------------------------------------------------------- profile / pg ---
# Round-trips a profile through the storage backend, which is the only way to
# prove the DSN wiring in the compose stack actually persists.
CODE=$(curl -s -o /tmp/wa_player.json -w '%{http_code}' -m 15 \
  -X POST "$BASE/v1/players" -H 'Content-Type: application/json' \
  -d '{"nickname":"smoke-bot","language":"en"}')
check_in "POST /v1/players returns 2xx" "200,201" "$CODE"
PLAYER_ID=$(json_field "$(cat /tmp/wa_player.json)" id)
if [ -n "$PLAYER_ID" ]; then
  CODE=$(curl -s -o /tmp/wa_player_get.json -w '%{http_code}' -m 15 "$BASE/v1/players/$PLAYER_ID")
  check "GET /v1/players/{id} returns 200" "200" "$CODE"
  GOT_NICK=$(json_field "$(cat /tmp/wa_player_get.json)" nickname)
  check "stored profile keeps its nickname" "smoke-bot" "$GOT_NICK"
else
  info "profile id missing from the create response; skipping read-back"
fi

# ----------------------------------------------------------- replay / TTL ---
# The replay log is published when a match ends, so a freshly created (still
# active) match must answer 404. Asserting that here pins the contract and
# catches a regression that would serve a partial, non-final event log.
#
# This must go through the match code, not the numeric id. With
# WORDARENA_REQUIRE_READ_CAPABILITY=true - which is how the service is deployed
# - resolveMatchRef refuses every numeric ref outright, so the id form answered
# 404 no matter what state the match was in and the assertion could never fail.
# The code form carries a valid capability, so a 404 here can only mean "no
# result row yet": the 404 is caused by the match being active, which is what
# the check claims to test. It is also the form that stays meaningful when the
# gate is off, so one assertion covers both configurations.
if [ -n "${MATCH_CODE:-}" ]; then
  CODE=$(curl -s -o /tmp/wa_replay.json -w '%{http_code}' -m 10 \
    -H "Authorization: Bearer $READ_CAP" "$BASE/v1/matches/$MATCH_CODE/replay")
  check "GET /v1/matches/{code}/replay is 404 while the match is active" "404" "$CODE"
fi

rm -f /tmp/wa_healthz.json /tmp/wa_readyz.json /tmp/wa_metrics.json \
      /tmp/wa_prom.txt /tmp/wa_match.json /tmp/wa_player.json /tmp/wa_player_get.json /tmp/wa_replay.json \
      /tmp/wa_snapshot.json

printf '\nsmoke result: %d passed, %d failed\n' "$PASS_COUNT" "$FAIL_COUNT"
[ "$FAIL_COUNT" -eq 0 ] || exit 1
