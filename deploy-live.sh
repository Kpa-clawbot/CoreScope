#!/bin/bash
set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
MATOMO_COMMIT="38c30f9"

cd "$DEPLOY_DIR"

# Guard: `git reset --hard` below discards everything in the working tree.
# Abort if there are uncommitted local changes so deploys never silently
# destroy work on the deploy host. Commit/stash manually, then re-run.
if [ -n "$(git status --porcelain)" ]; then
  echo "[deploy] ERROR: working tree at $DEPLOY_DIR is dirty." >&2
  echo "[deploy] Refusing to 'git reset --hard' over uncommitted changes." >&2
  echo "[deploy] Commit, stash, or discard them, then re-run this script." >&2
  git status --short >&2
  exit 1
fi

echo "[deploy] Fetching latest from origin..."
git fetch origin

echo "[deploy] Resetting to origin/main..."
git reset --hard origin/main

echo "[deploy] Building Docker image..."
docker build -t meshcore-analyzer .

echo "[deploy] Stopping old container (30s grace period)..."
docker stop -t 30 meshcore-analyzer && docker rm meshcore-analyzer
docker run -d --name meshcore-analyzer \
  --restart unless-stopped \
  -p 3000:3000 \
  -v "$(pwd)/config.json:/app/config.json:ro" \
  -v meshcore-data:/app/data \
  meshcore-analyzer

echo "[deploy] Done. Live at https://analyzer.on8ar.eu"
