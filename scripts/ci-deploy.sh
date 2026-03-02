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

SERVICE_NAME="meshsat_meshsat"
HOST_PORT="6050"
HEALTH_TIMEOUT="90"

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

# --- Deploy via Swarm ---
if docker service inspect ${SERVICE_NAME} > /dev/null 2>&1; then
  echo "Service exists — updating image (detached)..."
  docker service update \
    --image "${LOCAL_REG_IMAGE}" \
    --update-order stop-first \
    --force \
    --detach \
    ${SERVICE_NAME}

  echo "  Update issued — waiting for convergence..."
  CONVERGE_ELAPSED=0
  CONVERGE_MAX=90
  set +e
  while [ $CONVERGE_ELAPSED -lt $CONVERGE_MAX ]; do
    UPDATE_STATE=$(docker service inspect ${SERVICE_NAME} \
      --format '{{.UpdateStatus.State}}' 2>/dev/null || echo "unknown")

    case "$UPDATE_STATE" in
      completed)
        echo "  Update converged in ${CONVERGE_ELAPSED}s"
        break
        ;;
      updating)
        echo "  ${CONVERGE_ELAPSED}/${CONVERGE_MAX}s — updating..."
        ;;
      paused|rollback_*)
        echo "  ERROR: Update failed (state: ${UPDATE_STATE})"
        docker service ps ${SERVICE_NAME} --no-trunc 2>/dev/null | head -5
        docker service logs ${SERVICE_NAME} --tail 20 2>/dev/null || true
        exit 1
        ;;
      *)
        echo "  ${CONVERGE_ELAPSED}s — state: ${UPDATE_STATE}"
        ;;
    esac

    sleep 3
    CONVERGE_ELAPSED=$((CONVERGE_ELAPSED + 3))
  done
  set -e

  if [ $CONVERGE_ELAPSED -ge $CONVERGE_MAX ]; then
    echo "  ERROR: Update did not converge within ${CONVERGE_MAX}s"
    docker service ps ${SERVICE_NAME} --no-trunc 2>/dev/null | head -5
    docker service logs ${SERVICE_NAME} --tail 20 2>/dev/null || true
    exit 1
  fi
else
  echo "Service doesn't exist — deploying fresh stack..."
  docker stack deploy -c "$COMPOSE_FILE" --resolve-image=never "$STACK_NAME"

  echo "  Stack deployed — waiting for convergence..."
  sleep 5
  CONVERGE_ELAPSED=0
  CONVERGE_MAX=60
  set +e
  while [ $CONVERGE_ELAPSED -lt $CONVERGE_MAX ]; do
    STATE=$(docker service ps ${SERVICE_NAME} \
      --filter "desired-state=running" \
      --format "{{.CurrentState}}" 2>/dev/null | head -1)

    case "$STATE" in
      Running*)
        echo "  Converged: ${STATE} (${CONVERGE_ELAPSED}s)"
        break
        ;;
      *)
        echo "  ${CONVERGE_ELAPSED}s — ${STATE:-scheduling...}"
        ;;
    esac

    sleep 3
    CONVERGE_ELAPSED=$((CONVERGE_ELAPSED + 3))
  done
  set -e
fi

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
    echo "Service: ${SERVICE_NAME}"
    echo ""
    docker service ls | grep ${STACK_NAME}
    echo ""
    echo "API: http://cubeos.cube:${HOST_PORT}/api/"
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
echo "Service status:"
docker service ls | grep ${STACK_NAME} || echo "  Stack not found"
echo ""
echo "Service tasks:"
docker service ps ${SERVICE_NAME} --no-trunc 2>/dev/null | head -5 || echo "  Service not found"
echo ""
echo "Recent logs:"
docker service logs ${SERVICE_NAME} --tail 30 2>/dev/null || echo "  No logs available"
echo ""
exit 1
