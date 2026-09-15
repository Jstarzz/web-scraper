#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${SCRAPER_BASE_URL:-http://127.0.0.1:8080}"
API_KEY="${SCRAPER_API_KEY:-}"
POLL_INTERVAL="${SMOKE_POLL_INTERVAL:-1}"
POLL_ATTEMPTS="${SMOKE_POLL_ATTEMPTS:-45}"

if [[ -z "$API_KEY" ]]; then
  echo "Set SCRAPER_API_KEY to a ws_live_... client key first." >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required for smoke-test validation." >&2
  exit 1
fi

poll_job() {
  local job_id="$1"
  local response=""
  for _ in $(seq 1 "$POLL_ATTEMPTS"); do
    response="$(curl -fsS --max-time 15 \
      "${BASE_URL}/v1/jobs/${job_id}" \
      -H "Authorization: Bearer ${API_KEY}")"
    case "$(printf '%s' "$response" | jq -r '.status // empty')" in
      complete)
        printf '%s' "$response"
        return 0
        ;;
      failed)
        printf '%s\n' "$response" >&2
        return 1
        ;;
    esac
    sleep "$POLL_INTERVAL"
  done
  echo "Timed out waiting for scrape job ${job_id}." >&2
  return 1
}

search() {
  local marketplace="$1"
  local query="$2"
  local response status job_id count priced

  echo
  echo "== ${marketplace}: ${query} =="
  response="$(curl -fsS --max-time 35 \
    -X POST "${BASE_URL}/v1/search" \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "{\"marketplace\":\"${marketplace}\",\"query\":\"${query}\",\"limit\":10,\"wait_ms\":30000}")"

  status="$(printf '%s' "$response" | jq -r '.status // empty')"
  if [[ "$status" == "queued" || "$status" == "running" ]]; then
    job_id="$(printf '%s' "$response" | jq -er '.id')"
    response="$(poll_job "$job_id")"
    status="$(printf '%s' "$response" | jq -r '.status // empty')"
  fi

  printf '%s\n' "$response" | jq .

  if [[ "$status" != "complete" ]]; then
    echo "${marketplace} smoke test did not finish successfully (status=${status:-missing})." >&2
    return 1
  fi

  count="$(printf '%s' "$response" | jq '.result | if type == "array" then length else 0 end')"
  if (( count < 1 )); then
    echo "${marketplace} returned zero listings." >&2
    return 1
  fi

  priced="$(printf '%s' "$response" | jq '[.result[] | select((.price_minor | type) == "number")] | length')"
  if (( priced < 1 )); then
    echo "${marketplace} returned listings but none had a numeric price_minor." >&2
    return 1
  fi

  echo "Validated ${count} listing(s); ${priced} had parsed prices."
}

curl -fsS "${BASE_URL}/readyz" >/dev/null
search aliexpress "esp32 development board"
search amazon "usb c multimeter"

echo
echo "Live Amazon + AliExpress smoke test passed."
