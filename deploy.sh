#!/usr/bin/env bash
# deploy.sh — pull latest code and rebuild the bot container on the VM
#
# Usage (on the Linode VM, from the repo directory):
#   ./deploy.sh
#
# Prerequisites on the VM:
#   - git, docker, docker compose v2 installed
#   - .env file present in this directory with all required variables
#   - SSH key added to GitHub / Gitea / wherever the repo is hosted

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "==> Pulling latest changes..."
git pull origin main

echo "==> Building and starting container..."
docker compose up --build -d

echo "==> Removing dangling images..."
docker image prune -f

echo "==> Done. Container status:"
docker compose ps
