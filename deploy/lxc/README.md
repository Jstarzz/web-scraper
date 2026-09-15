# Proxmox LXC deployment

The recommended primary deployment is one dedicated Debian/Ubuntu LXC containing the Compose stack. The LXC is the isolation boundary; Docker Compose inside it runs PostgreSQL, the Go API, Go workers, the extraction worker, and optionally cloudflared.

## Suggested primary LXC

Start with:

- 8 vCPU
- 12 GB RAM (8 GB works at lower browser concurrency)
- 1-2 GB swap
- 24-32 GB root filesystem
- HDD-backed mount for `/srv/web-scraper/postgres`
- nesting enabled
- keyctl enabled when using an unprivileged LXC

For Proxmox, enable the LXC features `nesting=1,keyctl=1`. Do not expose port 5432 publicly.

A healthy HDD array is fine for the 60-day history store. Browser/network latency should dominate this workload. Keep browser temporary data in the LXC root/tmpfs and use the HDD mount for durable PostgreSQL data. Move WAL or the full database to SSD only if database latency actually becomes material.

## Install

Clone/copy the repository into `/opt/web-scraper`, then:

```bash
cd /opt/web-scraper
chmod +x deploy/lxc/install.sh
sudo ./deploy/lxc/install.sh
```

The installer:

1. installs Docker if needed;
2. creates `.env` from `.env.example`;
3. generates PostgreSQL and admin secrets;
4. creates the durable data directory;
5. builds and starts the stack;
6. creates the initial `josiah-mcp` API key once.

Save the returned `ws_live_...` value. Only its hash is persisted.

## Cloudflare Tunnel

Create a remotely managed tunnel in Cloudflare and route the desired hostname to:

```text
http://api:8080
```

Place the tunnel token in `.env`:

```env
TUNNEL_TOKEN=...
```

Then restart with:

```bash
docker compose --profile tunnel up -d
```

Cloudflare is the public ingress; the scraper's own bearer API key remains mandatory. PostgreSQL and the extraction worker stay private to the LXC/Compose network.

## Secondary 4-core / 8 GB worker

Use the existing `compose.worker.yml` on the smaller server. Start with `BROWSER_CONCURRENCY=1` or `2` and `WORKER_CONCURRENCY=2`. Point `DATABASE_URL` at the primary over LAN/Tailscale and firewall PostgreSQL so only the worker node can reach it.
