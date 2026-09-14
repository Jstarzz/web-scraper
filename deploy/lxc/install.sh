#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "Run this installer as root inside the LXC." >&2
  exit 1
fi

ROOT_DIR="${SCRAPER_ROOT:-/opt/web-scraper}"
DATA_DIR="${SCRAPER_DATA_DIR:-/srv/web-scraper/postgres}"
CLIENT_NAME="${BOOTSTRAP_CLIENT_NAME:-josiah-mcp}"

if ! command -v docker >/dev/null 2>&1; then
  apt-get update
  apt-get install -y ca-certificates curl git openssl
  curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
  sh /tmp/get-docker.sh
  rm -f /tmp/get-docker.sh
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required." >&2
  exit 1
fi

mkdir -p "$DATA_DIR"
cd "$ROOT_DIR"

if [[ ! -f .env ]]; then
  cp .env.example .env
  POSTGRES_PASSWORD="$(openssl rand -hex 24)"
  ADMIN_TOKEN="$(openssl rand -hex 32)"
  sed -i "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${POSTGRES_PASSWORD}|" .env
  sed -i "s|^ADMIN_TOKEN=.*|ADMIN_TOKEN=${ADMIN_TOKEN}|" .env
  sed -i "s|^SCRAPER_DATA_DIR=.*|SCRAPER_DATA_DIR=${DATA_DIR}|" .env
  echo "Created .env with generated database/admin secrets. Add TUNNEL_TOKEN before enabling the tunnel profile."
fi

mkdir -p "$DATA_DIR"
chmod 700 "$DATA_DIR"

if grep -Eq '^TUNNEL_TOKEN=.+$' .env; then
  docker compose --profile tunnel up -d --build
else
  docker compose up -d --build
fi

for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

if ! curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
  echo "API did not become healthy. Run: docker compose logs --tail=200" >&2
  exit 1
fi

if [[ ! -f .bootstrap-client-created ]]; then
  ADMIN_TOKEN="$(sed -n 's/^ADMIN_TOKEN=//p' .env | head -n1)"
  echo
  echo "Bootstrap API key (save the returned ws_live_ value now; it is only returned once):"
  curl -fsS -X POST http://127.0.0.1:8080/admin/api-keys \
    -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"${CLIENT_NAME}\",\"rate_limit_per_minute\":300}"
  echo
  touch .bootstrap-client-created
fi

echo
echo "web-scraper is running."
echo "Local health: http://127.0.0.1:8080/healthz"
echo "Data: ${DATA_DIR}"
echo "If TUNNEL_TOKEN is configured, Cloudflare Tunnel is running as well."
