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
Playwright browser worker                  Playwright browser worker
```

There is no Redis requirement in v0. PostgreSQL uses `FOR UPDATE SKIP LOCKED` as the distributed work queue, keeping the two-node deployment small and durable. Redis can be introduced later if queue contention actually becomes measurable.

## What exists now

- Go API and distributed worker binaries.
- PostgreSQL-backed job queue safe for multiple workers.
- Per-client API keys: secrets are generated once, SHA-256 hashes are persisted, and each client gets an independent rate limit.
- 60-day configurable product observation history.
- Change-only history writes plus a 24-hour heartbeat to avoid wasting storage.
- Playwright browser worker with Amazon, AliExpress, and eBay search extractors.
- Browser concurrency limits instead of launching an unbounded number of Chromium contexts.
- Stale-job recovery and worker heartbeats.
- Cloudflare Tunnel Compose profile.
- Separate worker-only Compose file for a 4-core / 8 GB secondary node.
- CI for Go tests/vet and strict TypeScript compilation.

## API

Create a key with the admin token:

```bash
curl -X POST http://127.0.0.1:8080/admin/api-keys \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"josiah-mcp","rate_limit_per_minute":300}'
```

The returned `key` is shown once. Keep it outside git.

Search and wait up to 12 seconds for completion:

```bash
curl -X POST https://scrape.example.com/v1/search \
  -H "Authorization: Bearer $SCRAPER_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"marketplace":"aliexpress","query":"esp32 display","limit":20,"wait_ms":12000}'
```

If the job is still running the API returns HTTP 202 with the job ID. Poll it with:

```bash
curl https://scrape.example.com/v1/jobs/JOB_ID \
  -H "Authorization: Bearer $SCRAPER_API_KEY"
```

History:

```bash
curl 'https://scrape.example.com/v1/history/aliexpress/1005001234567890?days=60' \
  -H "Authorization: Bearer $SCRAPER_API_KEY"
```

## Deployment

Copy `.env.example` to `.env`, set database/admin secrets, and point `SCRAPER_DATA_DIR` at the primary server's data disk.

Primary:

```bash
docker compose --profile tunnel up -d --build
```

Worker-only node:

```bash
docker compose -f compose.worker.yml up -d --build
```

See [`docs/deployment.md`](docs/deployment.md) for the dual-Xeon + 4-core/8-GB topology and Cloudflare/Tailscale notes.

## Storage

Do not store full HTML, screenshots, or traces for every successful scrape. Product metadata is normalized once and time-series observations hold changing values such as price, shipping, availability, rating, and review count. Browser artifacts should be retained only for failures when debugging support is added.

For this kind of workload, healthy HDD storage is fine until PostgreSQL latency proves otherwise. The scraper will hit target/network/browser limits long before a reasonable HDD array is saturated at initial scale.

## Responsible operation

Use official APIs first when they are available and suitable. This service is a fallback for public product data. Respect applicable site terms, robots directives, rate limits, and legal requirements. It intentionally does not include CAPTCHA bypass, fingerprint spoofing, account automation, or other challenge-evasion mechanisms.
