#!/bin/sh
# Starts the bundled aria2c RPC daemon, then execs tellarr.
# Runs as root only to prepare the (host-mounted) download dir; both aria2c
# and tellarr run as the unprivileged "app" user via su-exec.
set -e

DOWNLOAD_DIR="${DOWNLOAD_DIR:-/data/downloads/tellarr}"
ARIA2_RPC_PORT="${ARIA2_RPC_PORT:-6800}"
ARIA2_SECRET="${ARIA2_SECRET:-tellarr}"
export ARIA2_RPC_URL="${ARIA2_RPC_URL:-http://127.0.0.1:${ARIA2_RPC_PORT}/jsonrpc}"

mkdir -p "$DOWNLOAD_DIR"
chown app:app "$DOWNLOAD_DIR" 2>/dev/null || true

# --stop-with-process makes aria2c exit when tellarr (this pid after exec) dies.
su-exec app:app aria2c \
  --enable-rpc \
  --rpc-listen-all=true \
  --rpc-listen-port="$ARIA2_RPC_PORT" \
  --rpc-secret="$ARIA2_SECRET" \
  --dir="$DOWNLOAD_DIR" \
  --continue=true \
  --auto-file-renaming=false \
  --allow-overwrite=true \
  --stop-with-process=$$ &

i=0
until wget -q -O- --post-data="{\"jsonrpc\":\"2.0\",\"id\":\"ping\",\"method\":\"aria2.getVersion\",\"params\":[\"token:$ARIA2_SECRET\"]}" \
    "http://127.0.0.1:${ARIA2_RPC_PORT}/jsonrpc" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 15 ]; then
    echo "entrypoint: aria2c RPC did not come up on port $ARIA2_RPC_PORT, starting tellarr anyway" >&2
    break
  fi
  sleep 1
done

exec su-exec app:app /app/tellarr
