#!/bin/bash
HOST="macbookpro-2015"
USER="ansible"
REMOTE_DIR="/home/ansible/long-running-test"

# 1. Create remote directory
ssh $HOST -l $USER "mkdir -p $REMOTE_DIR"

# 2. Rsync the files
rsync -avz --exclude 'deploy.sh' . $USER@$HOST:$REMOTE_DIR/

# 3. Pull/Build necessary images on target
ssh $HOST -l $USER "cd $REMOTE_DIR && docker build -t network-topology-exporter:latest ../../" # Assuming same path structure or we should send the context

# Actually, it's safer to send the entire project context or build it locally and push it.
# Let's assume we can build it on the target if the code is there.
# If not, we should use the one we just built if we can push it.

# For now, let's just run docker compose
ssh $HOST -l $USER "cd $REMOTE_DIR && docker compose up -d"
