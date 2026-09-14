#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${SCRAPER_BASE_URL:-http://127.0.0.1:8080}"
API_KEY="${SCRAPER_API_KEY:-}"

if [[ -z "$API_KEY" ]]; then
  echo "Set SCRAPER_API_KEY to a ws_live_... client key first." >&2
  exit 1
fi

search() {
  local marketplace="$1"
  local query="$2"
  echo
  echo "== ${marketplace}: ${query} =="
  curl -fsS --max-time 40 \
    -X POST "${BASE_URL}/v1/search" \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "{\"marketplace\":\"${marketplace}\",\"query\":\"${query}\",\"limit\":10,\"wait_ms\":30000}"
  echo
}

curl -fsS "${BASE_URL}/readyz" >/dev/null
search aliexpress "esp32 development board"
search amazon "usb c multimeter"

echo
echo "Live smoke test completed. Inspect both responses for non-empty result arrays and plausible prices."
