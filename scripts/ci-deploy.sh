#!/usr/bin/env bash
# =============================================================================
# MeshSat — Pi-side deploy script (executed via SSH from GPU VM)
# =============================================================================
# Deploys MeshSat as a standalone Docker Compose container (direct serial mode).
# MeshSat standalone requires privileged access to /dev and /sys for serial
# devices — it NEVER runs as a Swarm service.
#
# Supports two layouts:
#   Field kit:  /srv/meshsat/docker-compose.yml     (parallax01, tesseract)
#   CubeOS:     /cubeos/coreapps/meshsat/appconfig/  (legacy CubeOS devices)
#
# The script auto-detects which layout is present on the target device.
# =============================================================================
set -euo pipefail

GHCR_IMAGE="ghcr.io/cubeos-app/meshsat"
CONTAINER_NAME="meshsat"
HOST_PORT="6050"
HEALTH_TIMEOUT="90"

echo "=== MeshSat Deploy (standalone direct mode) ==="
echo "  Target: ${DEPLOY_TARGET:-unknown}"

# =============================================================================
# Auto-detect deployment layout
# =============================================================================
FIELDKIT_COMPOSE="/srv/meshsat/docker-compose.yml"
CUBEOS_COMPOSE="/cubeos/coreapps/meshsat/appconfig/docker-compose.direct.yml"

if [ -f "$FIELDKIT_COMPOSE" ]; then
  DEPLOY_LAYOUT="fieldkit"
  COMPOSE_DIR="/srv/meshsat"
  COMPOSE_FILE="docker-compose.yml"
  echo "  Layout: field kit (/srv/meshsat)"
elif [ -f "$CUBEOS_COMPOSE" ]; then
  DEPLOY_LAYOUT="cubeos"
  COMPOSE_DIR="/cubeos/coreapps/meshsat/appconfig"
  COMPOSE_FILE="docker-compose.direct.yml"
  echo "  Layout: CubeOS (/cubeos/coreapps/meshsat/appconfig)"
else
  echo "ERROR: No compose file found at $FIELDKIT_COMPOSE or $CUBEOS_COMPOSE"
  exit 1
fi

# --- GHCR login ---
echo "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USER" --password-stdin

# --- Pull from GHCR ---
echo "Pulling latest MeshSat image from GHCR..."
timeout 120 docker pull "${GHCR_IMAGE}:latest" 2>&1 || {
  echo "Pull failed, using cached..."
}

# --- Local registry (CubeOS only — field kits don't have one) ---
if [ "$DEPLOY_LAYOUT" = "cubeos" ]; then
  LOCAL_REG_IMAGE="localhost:5000/cubeos-app/meshsat:latest"
  docker tag "${GHCR_IMAGE}:latest" "${LOCAL_REG_IMAGE}" 2>/dev/null || true
  docker push "${LOCAL_REG_IMAGE}" 2>/dev/null && \
    echo "  Pushed to local registry: ${LOCAL_REG_IMAGE}" || \
    echo "  WARN: Local registry push failed (non-fatal)"
fi

# --- Source env files for compose variable substitution ---
if [ "$DEPLOY_LAYOUT" = "cubeos" ]; then
  for ENVFILE in /cubeos/config/defaults.env /cubeos/config/secrets.env /cubeos/coreapps/image-versions.env; do
    if [ -f "$ENVFILE" ]; then
      set -a; source "$ENVFILE"; set +a
    fi
  done
elif [ -f "/srv/meshsat/.env" ]; then
  set -a; source "/srv/meshsat/.env"; set +a
fi

# =============================================================================
# CubeOS-only: Clean up leftover Swarm stack/service
# =============================================================================
if [ "$DEPLOY_LAYOUT" = "cubeos" ]; then
  SWARM_CLEANUP_NEEDED=false

  if docker stack ls 2>/dev/null | grep -q '^meshsat '; then
    SWARM_CLEANUP_NEEDED=true
    echo "WARNING: Found Swarm stack 'meshsat' — removing..."
    docker stack rm meshsat 2>/dev/null || true
  fi

  for SVC_NAME in meshsat_meshsat cubeos_meshsat; do
    if docker service inspect "$SVC_NAME" > /dev/null 2>&1; then
      SWARM_CLEANUP_NEEDED=true
      echo "WARNING: Found Swarm service $SVC_NAME — removing..."
      docker service rm "$SVC_NAME" 2>/dev/null || true
    fi
  done

  if [ "$SWARM_CLEANUP_NEEDED" = true ]; then
    echo "  Waiting for Swarm containers to drain..."
    for i in $(seq 1 20); do
      REMAINING=$(docker ps -q --filter "name=meshsat_meshsat" 2>/dev/null | wc -l)
      if [ "$REMAINING" -eq 0 ]; then
        echo "  Swarm containers drained after ${i}s."
        break
      fi
      sleep 1
    done
    docker ps -aq --filter "name=meshsat_meshsat" | xargs -r docker rm -f 2>/dev/null || true
    SWARM_COMPOSE="/cubeos/coreapps/meshsat/appconfig/docker-compose.yml"
    if [ -f "$SWARM_COMPOSE" ]; then
      mv "$SWARM_COMPOSE" "${SWARM_COMPOSE}.disabled"
      echo "  Renamed docker-compose.yml -> docker-compose.yml.disabled (prevents Swarm re-deploy)"
    fi
  fi
fi

# =============================================================================
# Stop existing MeshSat container
# =============================================================================
echo "Stopping MeshSat container..."
# Stop by name (current convention) and legacy name (cubeos-meshsat-direct)
for NAME in "$CONTAINER_NAME" "cubeos-meshsat-direct"; do
  docker stop "$NAME" 2>/dev/null || true
  docker rm -f "$NAME" 2>/dev/null || true
done
sleep 2

# =============================================================================
# CubeOS-only: Ensure HAL disables serial access (MeshSat owns the ports)
# =============================================================================
HAL_COMPOSE="/cubeos/coreapps/cubeos-hal/appconfig/docker-compose.yml"
if [ -f "$HAL_COMPOSE" ]; then
  echo "HAL compose found — ensuring HAL_DISABLE_MESHTASTIC and HAL_DISABLE_IRIDIUM are set..."

  if grep -q '# *- *HAL_DISABLE_MESHTASTIC=true' "$HAL_COMPOSE"; then
    sed -i 's/# *- *HAL_DISABLE_MESHTASTIC=true/- HAL_DISABLE_MESHTASTIC=true/' "$HAL_COMPOSE"
    echo "  Uncommented HAL_DISABLE_MESHTASTIC=true"
  elif grep -q 'HAL_DISABLE_MESHTASTIC=true' "$HAL_COMPOSE"; then
    echo "  HAL_DISABLE_MESHTASTIC=true already active"
  else
    echo "  WARN: HAL_DISABLE_MESHTASTIC line not found in HAL compose — skipping"
  fi

  if grep -q '# *- *HAL_DISABLE_IRIDIUM=true' "$HAL_COMPOSE"; then
    sed -i 's/# *- *HAL_DISABLE_IRIDIUM=true/- HAL_DISABLE_IRIDIUM=true/' "$HAL_COMPOSE"
    echo "  Uncommented HAL_DISABLE_IRIDIUM=true"
  elif grep -q 'HAL_DISABLE_IRIDIUM=true' "$HAL_COMPOSE"; then
    echo "  HAL_DISABLE_IRIDIUM=true already active"
  else
    echo "  WARN: HAL_DISABLE_IRIDIUM line not found in HAL compose — skipping"
  fi

  echo "Recreating HAL container with updated config..."
  cd /cubeos/coreapps/cubeos-hal/appconfig
  docker stop cubeos-hal 2>/dev/null || true
  docker rm -f cubeos-hal 2>/dev/null || true
  if docker compose up -d 2>&1; then
    sleep 3
    echo "  HAL container recreated with serial devices disabled."
  else
    echo "  WARN: HAL container recreation failed (non-fatal on standalone-only devices)"
  fi
else
  echo "No HAL found — skipping HAL reconfiguration."
fi

# =============================================================================
# Deploy: Docker Compose (direct serial mode, privileged)
# =============================================================================
echo "Deploying standalone container (docker-compose)..."
cd "$COMPOSE_DIR"

# CubeOS-only: clean up stale per-device env overrides
if [ "$DEPLOY_LAYOUT" = "cubeos" ]; then
  if [ -f /cubeos/config/meshsat.env ]; then
    echo "  Removing stale meshsat.env (auto-detection handles all devices)"
    rm -f /cubeos/config/meshsat.env
  fi
  for VAR in MESHSAT_IRIDIUM_PORT MESHSAT_IMT_PORT MESHSAT_CELLULAR_PORT MESHSAT_ZIGBEE_PORT MESHSAT_MESHTASTIC_PORT; do
    sudo sed -i "/${VAR}/d" "$COMPOSE_FILE" 2>/dev/null
  done
fi

docker compose -f "$COMPOSE_FILE" up -d --force-recreate --pull never 2>&1

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
    echo "Image:   ${GHCR_IMAGE}:latest"
    echo "Layout:  ${DEPLOY_LAYOUT} (${COMPOSE_DIR})"
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
docker logs ${CONTAINER_NAME} --tail 30 2>/dev/null || echo "  No logs available"
echo ""
exit 1
