#!/usr/bin/env bash
# Word Arena — bounded local-LLM micro-worker (Ollama) runner.
#
# The factory routes small deterministic drafting tasks to a local model and
# keeps architecture/security/gameplay work for a stronger agent
# (docs/AUTONOMOUS-DEVELOPMENT-MASTER.md §7). Two failure modes made that
# unreliable before this script existed: a payload built by nesting quotes
# inside `jq -n` (bash expanded the variable first, so Ollama received
# "missing request body") and an unbounded generation that sat on the host
# while the orchestrator waited. Both are impossible here: the prompt is read
# from a file, the JSON is assembled by Python with no shell interpolation, and
# a hard timeout plus a token cap bounds the run.
#
# Usage:
#   tools/llm-micro-worker.sh -p PROMPT_FILE -o OUT_FILE [-m MODEL] [-t TIMEOUT_S]
#                             [-c MAX_TOKENS] [-T TEMPERATURE] [-j THREADS]
#                             [--force] [--dry-run]
#
# Exit codes: 0 generated, 1 model error/refusal, 2 usage/environment error,
# 3 timeout, 4 host capacity guard. The orchestrator reviews OUT_FILE; nothing is committed on the
# strength of a model reply alone.
set -uo pipefail

MODEL="qwen2.5:7b"
TIMEOUT=420
THREADS=""
FORCE=0
MAX_TOKENS=3000
TEMPERATURE="0.2"
PROMPT_FILE=""
OUT_FILE=""
DRY=0
HOST="${OLLAMA_HOST:-127.0.0.1:11434}"

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    -p) PROMPT_FILE="${2:-}"; shift 2 ;;
    -o) OUT_FILE="${2:-}"; shift 2 ;;
    -m) MODEL="${2:-}"; shift 2 ;;
    -t) TIMEOUT="${2:-}"; shift 2 ;;
    -c) MAX_TOKENS="${2:-}"; shift 2 ;;
    -T) TEMPERATURE="${2:-}"; shift 2 ;;
    -j) THREADS="${2:-}"; shift 2 ;;
    --force) FORCE=1; shift ;;
    --dry-run) DRY=1; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[ -n "$PROMPT_FILE" ] && [ -n "$OUT_FILE" ] || usage
[ -f "$PROMPT_FILE" ] || { echo "prompt file not found: $PROMPT_FILE" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 2; }

# Capacity guard. The dev host runs the service under test, and two 2026-09-10
# outages (SSH banner timeouts for every session) were traced to an inference
# job and a Docker build overlapping on a 4-core ARM box. A micro-worker must
# yield to the thing it is supposed to help develop, so it refuses to start when
# the machine is already busy instead of being the straw.
NCPU=$(nproc 2>/dev/null || echo 4)
: "${THREADS:=$(( NCPU > 2 ? 2 : 1 ))}"
if command -v uptime >/dev/null 2>&1; then
  LOAD1=$(awk '{print $1}' /proc/loadavg 2>/dev/null || echo 0)
  LIMIT=$(awk -v n="$NCPU" 'BEGIN{printf "%.2f", n*1.2}')
  BUSY=$(awk -v l="$LOAD1" -v m="$LIMIT" 'BEGIN{print (l>m) ? 1 : 0}')
  if [ "$BUSY" = "1" ] && [ "$FORCE" != "1" ]; then
    echo "host busy: load1=$LOAD1 > $LIMIT (nproc=$NCPU). Wait, or pass --force." >&2
    echo "  This guard exists because unbounded local inference took SSH down on" >&2
    echo "  2026-09-10 while the game service was under test." >&2
    exit 4
  fi
fi

REQ="$(mktemp)"
RAW="$(mktemp)"
trap 'rm -f "$REQ" "$RAW"' EXIT

python3 - "$PROMPT_FILE" "$REQ" "$MODEL" "$MAX_TOKENS" "$TEMPERATURE" "$THREADS" <<'PY'
import json, sys
prompt_path, req_path, model, max_tokens, temp, threads = sys.argv[1:7]
prompt = open(prompt_path, encoding="utf-8").read()
payload = {
    "model": model,
    "prompt": prompt,
    "stream": False,
    # keep_alive is short on purpose: a parked model holds gigabytes of the
    # dev host's memory and the host also runs the game service under test.
    "keep_alive": "30s",
    "options": {
        "temperature": float(temp),
        "num_predict": int(max_tokens),
        "num_ctx": 8192,
        # Without this Ollama spreads inference over every core, which is how a
        # "small drafting task" ends up starving the service being developed.
        "num_thread": int(threads),
    },
}
json.dump(payload, open(req_path, "w", encoding="utf-8"))
print(f"request: model={model} prompt_bytes={len(prompt)} num_predict={max_tokens} num_thread={threads}", file=sys.stderr)
PY

if [ "$DRY" = "1" ]; then
  echo "dry run ok; payload at $REQ ($(wc -c <"$REQ") bytes)"
  exit 0
fi

if ! command -v timeout >/dev/null 2>&1; then
  echo "timeout(1) is required to bound the generation" >&2
  exit 2
fi

echo "worker: $MODEL via $HOST (cap ${TIMEOUT}s / ${MAX_TOKENS} tokens)"
timeout "$TIMEOUT" curl -sS -m "$TIMEOUT" -X POST "http://$HOST/api/generate" \
  -H 'Content-Type: application/json' --data-binary "@$REQ" >"$RAW"
rc=$?
if [ "$rc" = 124 ] || [ "$rc" = 143 ]; then
  echo "worker timed out after ${TIMEOUT}s; nothing written" >&2
  exit 3
fi
if [ "$rc" != 0 ]; then
  echo "curl failed rc=$rc: $(head -c 200 "$RAW" 2>/dev/null)" >&2
  exit 2
fi

# The response is one JSON object; anything that is not valid JSON is an
# infrastructure failure (model not pulled, daemon restart), reported as such
# instead of being written out as if it were a draft.
python3 - "$RAW" "$OUT_FILE" <<'PY' || exit 1
import json, sys
raw_path, out_path = sys.argv[1], sys.argv[2]
raw = open(raw_path, encoding="utf-8", errors="replace").read().strip()
if not raw:
    print("empty response from the model", file=sys.stderr)
    sys.exit(1)
try:
    doc = json.loads(raw)
except json.JSONDecodeError as exc:
    print(f"model response is not JSON ({exc}); first 200 bytes:\n{raw[:200]}", file=sys.stderr)
    sys.exit(1)
if doc.get("error"):
    print(f"model error: {doc['error']}", file=sys.stderr)
    sys.exit(1)
text = doc.get("response", "")
done = doc.get("done")
evals = doc.get("eval_count")
if not text.strip():
    print("model returned an empty completion", file=sys.stderr)
    sys.exit(1)
open(out_path, "w", encoding="utf-8").write(text)
truncated = bool(evals) and not done
print(f"worker ok: {len(text)} chars, {evals} tokens generated, done={done}"
      + ("  ** TRUNCATED at num_predict **" if truncated else ""))
if truncated:
    sys.exit(1)
PY
