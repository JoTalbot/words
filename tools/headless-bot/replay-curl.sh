#!/bin/bash
ADDR="${WORDARENA_ADDR:-http://127.0.0.1:18080}"
echo "Creating M0 match (seed 1512, en) at $ADDR ..."
curl -s -X POST "$ADDR/v1/matches" \
  -H "Content-Type: application/json" \
  -d "{\"language\":\"en\",\"seed\":1512}" | head -c 400
echo ""
echo "Next: connect WS with token from response; submit via ClientEnvelope (see api_test.go)."
