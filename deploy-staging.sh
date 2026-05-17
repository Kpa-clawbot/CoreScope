#!/bin/bash
set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$DEPLOY_DIR"

# Guard: `git reset --hard` below discards everything in the working tree.
# Abort if there are uncommitted local changes so deploys never silently
# destroy work on the deploy host. Commit/stash manually, then re-run.
if [ -n "$(git status --porcelain)" ]; then
  echo "[staging] ERROR: working tree at $DEPLOY_DIR is dirty." >&2
  echo "[staging] Refusing to 'git reset --hard' over uncommitted changes." >&2
  echo "[staging] Commit, stash, or discard them, then re-run this script." >&2
  git status --short >&2
  exit 1
fi

echo "[staging] Fetching latest from origin..."
git fetch origin

BRANCH="${1:-master}"
echo "[staging] Checking out $BRANCH..."
git reset --hard "origin/$BRANCH"

echo "[staging] Building Docker image..."
docker build -t meshcore-analyzer-staging .

echo "[staging] Stopping old container (30s grace period)..."
docker stop -t 30 meshcore-staging 2>/dev/null || true
docker rm meshcore-staging 2>/dev/null || true

echo "[staging] Starting new container..."
docker run -d --name meshcore-staging \
  --restart unless-stopped \
  -p 3001:3000 \
  -v "$(pwd)/config.json:/app/config.json:ro" \
  -v meshcore-staging-data:/app/data \
  meshcore-analyzer-staging

echo "[staging] Done. Live at https://staging.on8ar.eu"
