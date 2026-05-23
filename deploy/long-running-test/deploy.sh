#!/bin/bash
set -euo pipefail

# Long-running-lab deploy. Pushes this directory to the remote host,
# brings up the base containerlab lab (once), then starts the compose
# stack. Re-runnable: clab destroy is idempotent.
#
# CAVEAT: this script is pinned to the colinwood homelab. Parameterize
# REMOTE_HOST / REMOTE_USER / REMOTE_DIR before sharing.

REMOTE_HOST="${REMOTE_HOST:-macbookpro-2015}"
REMOTE_USER="${REMOTE_USER:-ansible}"
REMOTE_DIR="${REMOTE_DIR:-/home/ansible/long-running-test}"

# 1) Sync (exclude SSH keys and local env)
ssh "$REMOTE_USER@$REMOTE_HOST" "mkdir -p $REMOTE_DIR"
rsync -avz \
  --exclude 'deploy.sh' \
  --exclude 'id_ed25519' \
  --exclude 'id_ed25519.pub' \
  --exclude '.env' \
  . "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/"

# 2) Bring up the base lab (idempotent)
ssh "$REMOTE_USER@$REMOTE_HOST" "cd $REMOTE_DIR && sudo containerlab deploy -t base.clab.yml --reconfigure"

# 3) Start the compose stack
ssh "$REMOTE_USER@$REMOTE_HOST" "cd $REMOTE_DIR && docker compose --env-file .env up -d"
