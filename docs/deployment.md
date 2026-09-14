# Deployment topology

## Recommended layout

The primary dual-Xeon host runs the control plane and most capacity:

- PostgreSQL 17 on the large HDD-backed data volume
- `api` on port 8080, bound only to localhost
- one or more Go `worker` processes
- browser worker with 4-6 Chromium contexts to start
- Cloudflare Tunnel as the only public ingress

The 4-core / 8 GB host is a worker node:

- Go worker
- browser worker with 1-2 concurrent Chromium contexts
- no public API
- no local database
- connects to PostgreSQL on the primary over a private LAN or Tailscale address

Do not expose PostgreSQL through Cloudflare Tunnel or the public Internet.

## Cloudflare Tunnel

Create or reuse a remotely-managed Cloudflare Tunnel and route a hostname such as `scrape.example.com` to `http://api:8080` when `cloudflared` runs in the same Compose network.

Set `TUNNEL_TOKEN` in `.env`, then start:

```bash
docker compose --profile tunnel up -d --build
```

The API still requires its own bearer API key. Cloudflare is transport/edge protection, not the application authorization layer. Cloudflare Access can be added as a second layer for admin endpoints if desired.

## API keys

Set a long random `ADMIN_TOKEN` and create client-specific keys. Plaintext client keys are returned once and only hashes are stored.

```bash
curl -X POST https://scrape.example.com/admin/api-keys \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"josiah-mcp","rate_limit_per_minute":300}'
```

Create separate keys for each consumer, for example `josiah-mcp`, `development`, and any future service. Revoke/rotation support is intentionally separated from provider credentials: users of this API never receive HasData, ScrapingDog, SerpApi, Apify, eBay, or other upstream secrets.

## Secondary worker

On the primary, bind PostgreSQL only to the private/Tailscale interface by setting `POSTGRES_BIND_IP` to that interface address and restrict TCP/5432 in the host firewall to the worker node.

On the secondary worker set:

```env
DATABASE_URL=postgres://scraper:...@PRIMARY_PRIVATE_IP:5432/scraper?sslmode=disable
WORKER_ID=mini-01
BROWSER_WORKER_URL=http://browser-worker:3000
BROWSER_CONCURRENCY=2
```

Then run:

```bash
docker compose -f compose.worker.yml up -d --build
```

For a routed network you do not fully trust, enable PostgreSQL TLS rather than using `sslmode=disable`.

## HDD versus SSD

HDD storage is acceptable for this workload at the beginning. Scraping is normally network/browser-bound and observations are append-heavy. The schema also stores a new observation only when tracked values change, with one 24-hour heartbeat, which sharply reduces random writes.

Keep PostgreSQL on the HDD array if it is healthy and has adequate IOPS. Move PostgreSQL WAL or the whole database to SSD/NVMe only if metrics show database fsync/latency becoming material. Browser caches and temporary files do not need premium storage.

Backups matter more than raw media speed. Use a mirrored/RAID-backed data path where practical and schedule `pg_dump` or physical backups independently of the 60-day observation retention policy.
