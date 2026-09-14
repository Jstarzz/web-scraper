# Proxmox LXC bootstrap

`create-lxc.sh` is the shortest path from a Proxmox VE host to a dedicated scraper container.

It creates an unprivileged Debian 12 LXC with Docker-compatible features, clones this repository into the container, and runs the normal LXC installer.

## Recommended primary Xeon allocation

```bash
VMID=310 \
CORES=8 \
MEMORY_MB=12288 \
SWAP_MB=2048 \
ROOTFS_GB=32 \
BRIDGE=vmbr0 \
HOST_DATA_DIR=/mnt/your-hdd-pool/web-scraper/postgres \
bash deploy/proxmox/create-lxc.sh
```

Defaults are deliberately conservative and can all be overridden with environment variables.

Important variables:

- `VMID`: Proxmox CT ID, default `310`.
- `LXC_HOSTNAME`: default `web-scraper`.
- `TEMPLATE_STORAGE`: storage containing LXC templates, default `local`.
- `ROOTFS_STORAGE`: CT root filesystem storage, default `local-lvm`.
- `BRIDGE`: Proxmox bridge, default `vmbr0`.
- `CORES`, `MEMORY_MB`, `SWAP_MB`, `ROOTFS_GB`: resource allocation.
- `HOST_DATA_DIR`: optional host directory mounted at `/srv/web-scraper/postgres` in the CT. Use this to keep PostgreSQL on the large HDD pool.
- `REPO_REF`: branch/tag to clone. While PR #1 is open it defaults to `feat/bootstrap-scraper`; switch to `main` after merge.

The script refuses to overwrite an existing CT ID.

## Container features

The CT is created unprivileged with:

```text
nesting=1,keyctl=1,fuse=1
```

Those features are required for the Docker/Chromium deployment used by this project. The service itself still runs in Docker Compose inside the LXC, so Proxmox remains the outer isolation boundary.

## HDD bind mount

When `HOST_DATA_DIR` is provided, the script creates the host directory and assigns it to the default unprivileged-LXC root mapping before adding it as `mp0`. The in-container installer then resolves the actual PostgreSQL image UID/GID and fixes the mounted database-directory ownership before starting PostgreSQL.

Do not point `HOST_DATA_DIR` at a directory containing unrelated data.

## After provisioning

Enter the container:

```bash
pct enter 310
```

The installer prints the first `ws_live_...` API key. Save it. Then configure the Cloudflare Tunnel public hostname to use `http://api:8080` as the origin service because cloudflared runs in the same Compose network.

Once the hostname is live, the matching MCP configuration is:

```env
AMAZON_SELFHOSTED_URL=https://YOUR_HOSTNAME/v1/search
ALIEXPRESS_SELFHOSTED_URL=https://YOUR_HOSTNAME/v1/search
SELFHOSTED_API_KEY=ws_live_...
```

Run the live extraction validation inside the CT:

```bash
cd /opt/web-scraper
export SCRAPER_API_KEY='ws_live_...'
deploy/lxc/smoke.sh
```

The smoke test fails if either Amazon or AliExpress returns no listings or no parsed prices.
