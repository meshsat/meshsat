#!/usr/bin/env bash
# =============================================================================
# MeshSat — Pi-side deploy script (executed via SSH from GPU VM)
# =============================================================================
# Deploys MeshSat as a standalone Docker Compose container (direct serial mode).
# MeshSat standalone requires privileged access to /dev and /sys for serial
# devices — it NEVER runs as a Swarm service.
#
# If a leftover Swarm service exists, it is removed automatically.
# =============================================================================
set -euo pipefail

DIRECT_COMPOSE_FILE="/cubeos/coreapps/meshsat/appconfig/docker-compose.direct.yml"
GHCR_IMAGE="ghcr.io/cubeos-app/meshsat"
LOCAL_REG_IMAGE="localhost:5000/cubeos-app/meshsat:latest"
DIRECT_CONTAINER="cubeos-meshsat-direct"
HOST_PORT="6050"
HEALTH_TIMEOUT="90"

echo "=== MeshSat Deploy (standalone direct mode) ==="

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

# --- Source env files for compose variable substitution ---
if [ -f /cubeos/config/defaults.env ]; then
  set -a
  source /cubeos/config/defaults.env
  set +a
fi
if [ -f /cubeos/config/secrets.env ]; then
  set -a
  source /cubeos/config/secrets.env
  set +a
fi

# --- Source image-versions.env for MESHSAT_TAG etc. ---
if [ -f /cubeos/coreapps/image-versions.env ]; then
  set -a
  source /cubeos/coreapps/image-versions.env
  set +a
fi

# =============================================================================
# Clean up any leftover Swarm service (legacy — MeshSat should never be Swarm)
# =============================================================================
if docker service inspect meshsat_meshsat > /dev/null 2>&1; then
  echo "WARNING: Found leftover Swarm service meshsat_meshsat — removing..."
  docker service rm meshsat_meshsat 2>/dev/null || true
  docker stack rm meshsat 2>/dev/null || true
  sleep 3
  echo "  Swarm service removed."
fi

# =============================================================================
# Deploy: Docker Compose (direct serial mode, privileged)
# =============================================================================
if [ ! -f "$DIRECT_COMPOSE_FILE" ]; then
  echo "ERROR: Compose file not found at $DIRECT_COMPOSE_FILE"
  exit 1
fi

echo "Deploying standalone container (docker-compose)..."
cd /cubeos/coreapps/meshsat/appconfig
docker compose -f docker-compose.direct.yml up -d --force-recreate --pull never 2>&1

echo "  Container recreated — waiting for health..."

# --- Health check ---
echo ""
echo "Waiting for MeshSat to be healthy (timeout: ${HEALTH_TIMEOUT}s)..."
HEALTH_URL="http://127.0.0.1:${HOST_PORT}/health"
SECONDS_WAITED=0
INTERVAL=3

while [ ${SECONDS_WAITED} -lt ${HEALTH_TIMEOUT} ]; do
  RESPONSE=$(curl -sf ${HEALTH_URL} 2>/dev/null) && {
    echo ""
    echo "Health check passed after ${SECONDS_WAITED}s!"
    echo ""
    echo "=== Deployment Summary ==="
    echo "Image:   ${LOCAL_REG_IMAGE}"
    echo "Mode:    standalone direct (docker-compose, serial)"
    echo ""
    docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' | grep meshsat
    echo ""
    echo "API: http://127.0.0.1:${HOST_PORT}/api/"
    exit 0
  }

  SECONDS_WAITED=$((SECONDS_WAITED + INTERVAL))
  echo "  ${SECONDS_WAITED}/${HEALTH_TIMEOUT}s..."
  sleep ${INTERVAL}
done

echo ""
echo "Health check failed after ${HEALTH_TIMEOUT}s"
echo ""
echo "=== Diagnostics ==="
echo "Container status:"
docker ps -a --format 'table {{.Names}}\t{{.Status}}' | grep meshsat || echo "  Not found"
echo ""
echo "Recent logs:"
docker logs ${DIRECT_CONTAINER} --tail 30 2>/dev/null || echo "  No logs available"
echo ""
exit 1
