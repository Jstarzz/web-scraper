# web-scraper

Distributed self-hosted procurement scraper for **Amazon, AliExpress, and eBay**. It is designed as the final fallback behind official/free API providers, with a public API behind Cloudflare Tunnel and horizontally scalable workers.

## Architecture

```text
MCP / clients
     |
Cloudflare Tunnel
     |
 Go API :8080
     |
 PostgreSQL job queue + history
     |
 +----------------------+---------------------+
 |                                            |
Xeon worker(s)                              mini worker
 |                                            |
extraction worker                           extraction worker
 |                                            |
HTTP fast path -> Chromium fallback         HTTP fast path -> Chromium fallback
```

There is no Redis requirement in v0. PostgreSQL uses `FOR UPDATE SKIP LOCKED` as the distributed work queue, keeping the two-node deployment small and durable. Redis can be introduced later if queue contention actually becomes measurable.

## Amazon and AliExpress focus

Most extraction work is concentrated on Amazon and AliExpress:

- plain HTTP is attempted first for throughput and low RAM usage;
- Chromium/Playwright is the fallback when the HTTP response is thin or unusable;
- browser fallback blocks image/font/media transfers by default while keeping image URLs available in markup;
- Amazon uses the current `data-component-type="s-search-result"` structure plus a `data-asin` fallback;
- Amazon returns current/list prices, rating/review data and a sponsored-result flag when present;
- AliExpress prefers structured hydration data when present, then falls back to hydrated DOM cards;
- AliExpress browser fallback stops scrolling as soon as the requested number of unique products is present;
- AliExpress returns sale/original prices, seller, rating/review count and sold count when present;
- known challenge/verification pages are detected and treated as failures rather than being stored as product data;
- structured and DOM results are deduplicated by marketplace product ID.

The service does not implement CAPTCHA solving, fingerprint spoofing, login automation, or challenge bypass.

## What exists now

- Go API and distributed worker binaries.
- PostgreSQL-backed job queue safe for multiple workers.
- Per-client API keys: secrets are generated once, SHA-256 hashes are persisted, and each client gets an independent rate limit.
- Admin key inventory and revocation without exposing stored plaintext secrets.
- 60-day configurable product observation history.
- Change-only history writes plus a 24-hour heartbeat to avoid wasting storage.
- HTTP-first + Playwright extraction worker for Amazon and AliExpress; eBay remains supported as a lighter fallback path.
- Browser concurrency limits instead of launching an unbounded number of Chromium contexts.
- Stale-job recovery and worker heartbeats.
- Cloudflare Tunnel Compose profile.
- Separate worker-only Compose file for a 4-core / 8 GB secondary node.
- Proxmox LXC bootstrap installer.
- Parser regression tests for Amazon and AliExpress.
- CI for Go tests/vet, TypeScript/parser tests, Compose validation, and container image builds.

## API

Create a key with the admin token:

```bash
curl -X POST http://127.0.0.1:8080/admin/api-keys \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"josiah-mcp","rate_limit_per_minute":300}'
```

The returned `key` is shown once. Keep it outside git. Create separate keys for each consumer rather than sharing one secret.

List client keys and their last-use/revocation state:

```bash
curl http://127.0.0.1:8080/admin/api-keys \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

Revoke one without deleting its audit record:

```bash
curl -X DELETE http://127.0.0.1:8080/admin/api-keys/KEY_UUID \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

Search and wait up to 12 seconds for completion:

```bash
curl -X POST https://scrape.example.com/v1/search \
  -H "Authorization: Bearer $SCRAPER_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"marketplace":"aliexpress","query":"esp32 display","limit":20,"wait_ms":12000}'
```

AI/MCP callers can request the compact view by adding `?compact=1`. It keeps product IDs, titles, URLs, seller, price/shipping, availability, rating/review/sold counts and sponsored status, while dropping image URLs, repeated per-listing marketplace names, worker metadata and timestamps:

```bash
curl -X POST 'https://scrape.example.com/v1/search?compact=1' \
  -H "Authorization: Bearer $SCRAPER_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"marketplace":"aliexpress","query":"esp32 display","limit":10,"wait_ms":12000}'
```

If the job is still running the API returns HTTP 202 with the job ID. Poll it with the same view when desired:

```bash
curl 'https://scrape.example.com/v1/jobs/JOB_ID?compact=1' \
  -H "Authorization: Bearer $SCRAPER_API_KEY"
```

History:

```bash
curl 'https://scrape.example.com/v1/history/aliexpress/1005001234567890?days=60' \
  -H "Authorization: Bearer $SCRAPER_API_KEY"
```

## Proxmox LXC deployment

The recommended primary deployment is **one dedicated LXC** on the dual-Xeon host. Docker Compose runs inside that LXC; PostgreSQL, API, workers, Chromium and cloudflared remain isolated from the Proxmox host.

Suggested starting allocation:

- 8 vCPU
- 12 GB RAM (8 GB works with lower browser concurrency)
- 24-32 GB root filesystem
- HDD-backed bind/mount for `/srv/web-scraper/postgres`
- LXC features `nesting=1,keyctl=1`

Copy/clone the repo into the LXC and run:

```bash
cd /opt/web-scraper
chmod +x deploy/lxc/install.sh
deploy/lxc/install.sh
```

The installer generates database/admin secrets, starts the stack, and issues the first `josiah-mcp` API key once. Add `TUNNEL_TOKEN` to `.env` to enable the Cloudflare Tunnel profile.

After saving the returned client key, run the live smoke test:

```bash
chmod +x deploy/lxc/smoke.sh
export SCRAPER_API_KEY='ws_live_...'
deploy/lxc/smoke.sh
```

That sends one real AliExpress search and one real Amazon search through the full API -> queue -> worker -> extractor path.

Worker-only node:

```bash
docker compose -f compose.worker.yml up -d --build
```

See [`docs/deployment.md`](docs/deployment.md) and [`deploy/lxc/README.md`](deploy/lxc/README.md) for the dual-Xeon + 4-core/8-GB topology and Cloudflare/Tailscale notes.

## Storage

Do not store full HTML, screenshots, or traces for every successful scrape. Product metadata is normalized once and time-series observations hold changing values such as price, shipping, availability, rating, and review count. Browser artifacts should be retained only for failures when debugging support is added.

For this kind of workload, healthy HDD storage is fine until PostgreSQL latency proves otherwise. The scraper will hit target/network/browser limits long before a reasonable HDD array is saturated at initial scale.

## Responsible operation

Use official APIs first when they are available and suitable. This service is a fallback for public product data. Respect applicable site terms, robots directives, rate limits, and legal requirements. It intentionally does not include CAPTCHA bypass, fingerprint spoofing, account automation, or other challenge-evasion mechanisms.
