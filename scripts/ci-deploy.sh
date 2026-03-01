#!/usr/bin/env bash
# =============================================================================
# MeshSat — Pi-side deploy script (executed via SSH from GPU VM)
# =============================================================================
# Usage: GHCR_TOKEN=... GHCR_USER=... bash /tmp/ci-deploy-meshsat.sh
# =============================================================================
set -euo pipefail

COMPOSE_FILE="/cubeos/coreapps/meshsat/appconfig/docker-compose.yml"
GHCR_IMAGE="ghcr.io/cubeos-app/meshsat"
LOCAL_REG_IMAGE="localhost:5000/cubeos-app/meshsat:latest"
STACK_NAME="meshsat"

echo "=== MeshSat Deploy ==="

# --- Pre-flight ---
if [ ! -f "$COMPOSE_FILE" ]; then
  echo "ERROR: MeshSat compose file not found at $COMPOSE_FILE"
  exit 1
fi

# --- GHCR login ---
echo "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USER" --password-stdin

# --- Pull from GHCR ---
echo "Pulling latest MeshSat image from GHCR..."
timeout 120 docker pull "${GHCR_IMAGE}:latest" 2>&1 || {
  echo "Pull failed, using cached..."
}

# --- Retag for local registry ---
docker tag "${GHCR_IMAGE}:latest" "${LOCAL_REG_IMAGE}" 2>/dev/null || true

# --- Push to local registry ---
docker push "${LOCAL_REG_IMAGE}" 2>/dev/null && \
  echo "  Pushed to local registry: ${LOCAL_REG_IMAGE}" || \
  echo "  WARN: Local registry push failed (non-fatal)"

# --- Deploy via Swarm stack ---
echo "Deploying MeshSat stack..."
docker stack deploy -c "$COMPOSE_FILE" --resolve-image=never "$STACK_NAME"

# --- Health check ---
sleep 5
for i in $(seq 1 10); do
  if curl -sf http://127.0.0.1:6050/health >/dev/null 2>&1; then
    echo "MeshSat healthy"
    break
  fi
  [ "$i" -eq 10 ] && echo "MeshSat may still be starting..."
  sleep 3
done

echo "API: http://cubeos.cube:6050/api/"
echo "Deploy complete"
