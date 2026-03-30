#!/bin/bash
set -e

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$DEPLOY_DIR"

echo "[staging] Fetching latest from origin..."
git fetch origin

BRANCH="${1:-master}"
echo "[staging] Checking out $BRANCH..."
git reset --hard "origin/$BRANCH"

echo "[staging] Building Docker image..."
docker build -t meshcore-analyzer-staging .

echo "[staging] Restarting container..."
docker stop meshcore-staging 2>/dev/null || true
docker rm meshcore-staging 2>/dev/null || true
docker run -d --name meshcore-staging \
  --restart unless-stopped \
  -p 3001:3000 \
  -v "$(pwd)/config.json:/app/config.json:ro" \
  -v meshcore-staging-data:/app/data \
  meshcore-analyzer-staging

echo "[staging] Done. Live at https://staging.on8ar.eu"
