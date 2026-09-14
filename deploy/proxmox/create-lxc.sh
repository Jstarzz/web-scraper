#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo "Run this script as root on the Proxmox host." >&2
  exit 1
fi

if ! command -v pct >/dev/null 2>&1 || ! command -v pveam >/dev/null 2>&1; then
  echo "pct/pveam not found. Run this on a Proxmox VE host." >&2
  exit 1
fi

VMID="${VMID:-310}"
HOSTNAME="${LXC_HOSTNAME:-web-scraper}"
TEMPLATE_STORAGE="${TEMPLATE_STORAGE:-local}"
ROOTFS_STORAGE="${ROOTFS_STORAGE:-local-lvm}"
BRIDGE="${BRIDGE:-vmbr0}"
CORES="${CORES:-8}"
MEMORY_MB="${MEMORY_MB:-12288}"
SWAP_MB="${SWAP_MB:-2048}"
ROOTFS_GB="${ROOTFS_GB:-32}"
HOST_DATA_DIR="${HOST_DATA_DIR:-}"
REPO_URL="${REPO_URL:-https://github.com/Jstarzz/web-scraper.git}"
REPO_REF="${REPO_REF:-feat/bootstrap-scraper}"
LXC_REPO_DIR="${LXC_REPO_DIR:-/opt/web-scraper}"

if pct status "$VMID" >/dev/null 2>&1; then
  echo "CT ${VMID} already exists. Refusing to overwrite it." >&2
  exit 1
fi

pveam update >/dev/null
TEMPLATE="${TEMPLATE:-$(pveam available --section system | awk '/debian-12-standard/ {print $2}' | tail -n1)}"
if [[ -z "$TEMPLATE" ]]; then
  echo "Could not find a Debian 12 standard LXC template." >&2
  exit 1
fi

TEMPLATE_PATH="${TEMPLATE_STORAGE}:vztmpl/${TEMPLATE}"
if ! pvesm path "$TEMPLATE_PATH" >/dev/null 2>&1; then
  echo "Downloading ${TEMPLATE} to ${TEMPLATE_STORAGE}..."
  pveam download "$TEMPLATE_STORAGE" "$TEMPLATE"
fi

CREATE_ARGS=(
  "$VMID" "$TEMPLATE_PATH"
  --hostname "$HOSTNAME"
  --ostype debian
  --unprivileged 1
  --features nesting=1,keyctl=1,fuse=1
  --cores "$CORES"
  --memory "$MEMORY_MB"
  --swap "$SWAP_MB"
  --rootfs "${ROOTFS_STORAGE}:${ROOTFS_GB}"
  --net0 "name=eth0,bridge=${BRIDGE},ip=dhcp,type=veth"
  --onboot 1
  --start 0
)

if [[ -n "$HOST_DATA_DIR" ]]; then
  mkdir -p "$HOST_DATA_DIR"
  # Root in an unprivileged CT maps to host uid/gid 100000 by default.
  chown 100000:100000 "$HOST_DATA_DIR"
  chmod 700 "$HOST_DATA_DIR"
  CREATE_ARGS+=(--mp0 "${HOST_DATA_DIR},mp=/srv/web-scraper/postgres")
fi

echo "Creating CT ${VMID} (${HOSTNAME})..."
pct create "${CREATE_ARGS[@]}"
pct start "$VMID"

for _ in $(seq 1 60); do
  if pct exec "$VMID" -- true >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

if ! pct exec "$VMID" -- true >/dev/null 2>&1; then
  echo "CT ${VMID} did not become responsive." >&2
  exit 1
fi

pct exec "$VMID" -- bash -lc "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates git"
pct exec "$VMID" -- bash -lc "rm -rf '${LXC_REPO_DIR}' && git clone --depth 1 --branch '${REPO_REF}' '${REPO_URL}' '${LXC_REPO_DIR}'"
pct exec "$VMID" -- bash -lc "cd '${LXC_REPO_DIR}' && chmod +x deploy/lxc/install.sh deploy/lxc/smoke.sh && deploy/lxc/install.sh"

echo
echo "CT ${VMID} is provisioned."
echo "Console: pct enter ${VMID}"
echo "Status:  pct status ${VMID}"
echo "Logs:    pct exec ${VMID} -- bash -lc \"cd ${LXC_REPO_DIR} && docker compose logs --tail=200\""
if [[ -n "$HOST_DATA_DIR" ]]; then
  echo "PostgreSQL host data: ${HOST_DATA_DIR}"
else
  echo "PostgreSQL data is inside the CT rootfs. Set HOST_DATA_DIR on a fresh install to bind it to your HDD pool."
fi
