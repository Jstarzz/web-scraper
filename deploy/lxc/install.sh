#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "Run this installer as root inside the LXC." >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
ROOT_DIR="${SCRAPER_ROOT:-${REPO_DIR}}"
DATA_DIR="${SCRAPER_DATA_DIR:-/srv/web-scraper/postgres}"
CLIENT_NAME="${BOOTSTRAP_CLIENT_NAME:-josiah-mcp}"

install_tools() {
  apt-get update
  apt-get install -y ca-certificates curl git jq openssl
}

if ! command -v curl >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1 || ! command -v openssl >/dev/null 2>&1; then
  install_tools
fi

if ! command -v docker >/dev/null 2>&1; then
  install_tools
  curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
  sh /tmp/get-docker.sh
  rm -f /tmp/get-docker.sh
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required." >&2
  exit 1
fi

cd "$ROOT_DIR"
mkdir -p "$DATA_DIR"

if [[ ! -f .env ]]; then
  cp .env.example .env
  POSTGRES_PASSWORD="$(openssl rand -hex 24)"
  ADMIN_TOKEN="$(openssl rand -hex 32)"
  sed -i "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${POSTGRES_PASSWORD}|" .env
  sed -i "s|^DATABASE_URL=.*|DATABASE_URL=postgres://scraper:${POSTGRES_PASSWORD}@db:5432/scraper?sslmode=disable|" .env
  sed -i "s|^ADMIN_TOKEN=.*|ADMIN_TOKEN=${ADMIN_TOKEN}|" .env
  sed -i "s|^SCRAPER_DATA_DIR=.*|SCRAPER_DATA_DIR=${DATA_DIR}|" .env
  chmod 600 .env
  echo "Created .env with generated database/admin secrets. Add TUNNEL_TOKEN before enabling the tunnel profile."
fi

# A bind-mounted PostgreSQL directory owned root:root with mode 0700 is not writable by
# the postgres user inside the container. Let the exact image resolve its own postgres UID/GID.
docker pull postgres:17-alpine >/dev/null
docker run --rm --entrypoint sh \
  -v "$DATA_DIR:/var/lib/postgresql/data" \
  postgres:17-alpine \
  -c 'chown postgres:postgres /var/lib/postgresql/data && chmod 700 /var/lib/postgresql/data'

if grep -Eq '^TUNNEL_TOKEN=.+$' .env; then
  docker compose --profile tunnel up -d --build
else
  docker compose up -d --build
fi

for _ in $(seq 1 90); do
  if curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

if ! curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
  echo "API/database did not become ready. Run: docker compose logs --tail=200" >&2
  exit 1
fi

for _ in $(seq 1 45); do
  if docker compose exec -T browser-worker node -e "fetch('http://127.0.0.1:3000/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

if ! docker compose exec -T browser-worker node -e "fetch('http://127.0.0.1:3000/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))" >/dev/null 2>&1; then
  echo "Extraction worker did not become healthy. Run: docker compose logs --tail=200 browser-worker worker" >&2
  exit 1
fi

if [[ -z "$(docker compose ps --status running -q worker)" ]]; then
  echo "Go worker is not running. Run: docker compose logs --tail=200 worker" >&2
  exit 1
fi

if [[ ! -f .bootstrap-client-created ]]; then
  ADMIN_TOKEN="$(sed -n 's/^ADMIN_TOKEN=//p' .env | head -n1)"
  echo
  echo "Creating bootstrap API key for ${CLIENT_NAME}..."
  BOOTSTRAP_RESPONSE="$(curl -fsS -X POST http://127.0.0.1:8080/admin/api-keys \
    -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"${CLIENT_NAME}\",\"rate_limit_per_minute\":300}")"
  CLIENT_KEY="$(printf '%s' "$BOOTSTRAP_RESPONSE" | jq -er '.key')"
  touch .bootstrap-client-created

  echo
  echo "Bootstrap client key created. Save this value now; the service stores only its hash:"
  echo "$CLIENT_KEY"
  echo
  echo "For ebay-search-mcp after your Cloudflare hostname is configured:"
  echo "SELFHOSTED_SCRAPER_URL=https://YOUR_HOSTNAME"
  echo "SELFHOSTED_SCRAPER_API_KEY=${CLIENT_KEY}"
  echo "This single URL/key pair enables Amazon, AliExpress, and eBay self-hosted fallbacks."
fi

echo
echo "web-scraper is running."
echo "Local readiness: http://127.0.0.1:8080/readyz"
echo "Data: ${DATA_DIR}"
echo "If TUNNEL_TOKEN is configured, Cloudflare Tunnel is running as well."
