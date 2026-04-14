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
# Clean up any leftover Swarm stack/service (MeshSat must NEVER be Swarm)
# =============================================================================
# Remove the stack first (this removes all services in the stack).
# docker stack rm is async — we must wait for containers to actually stop.
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
  # Force-remove any stragglers
  docker ps -aq --filter "name=meshsat_meshsat" | xargs -r docker rm -f 2>/dev/null || true
  # Disable the Swarm compose file to prevent re-deployment by orchestrator
  SWARM_COMPOSE="/cubeos/coreapps/meshsat/appconfig/docker-compose.yml"
  if [ -f "$SWARM_COMPOSE" ]; then
    mv "$SWARM_COMPOSE" "${SWARM_COMPOSE}.disabled"
    echo "  Renamed docker-compose.yml -> docker-compose.yml.disabled (prevents Swarm re-deploy)"
  fi
fi

# =============================================================================
# Stop MeshSat BEFORE HAL changes to prevent stale serial fd issues
# =============================================================================
echo "Stopping MeshSat container before HAL reconfiguration..."
docker stop ${DIRECT_CONTAINER} 2>/dev/null || true
docker rm -f ${DIRECT_CONTAINER} 2>/dev/null || true
sleep 2

# =============================================================================
# Ensure HAL disables Meshtastic/Iridium serial access (MeshSat owns the ports)
# =============================================================================
HAL_COMPOSE="/cubeos/coreapps/cubeos-hal/appconfig/docker-compose.yml"
if [ -f "$HAL_COMPOSE" ]; then
  echo "HAL compose found — ensuring HAL_DISABLE_MESHTASTIC and HAL_DISABLE_IRIDIUM are set..."

  # Uncomment HAL_DISABLE_MESHTASTIC if commented out
  if grep -q '# *- *HAL_DISABLE_MESHTASTIC=true' "$HAL_COMPOSE"; then
    sed -i 's/# *- *HAL_DISABLE_MESHTASTIC=true/- HAL_DISABLE_MESHTASTIC=true/' "$HAL_COMPOSE"
    echo "  Uncommented HAL_DISABLE_MESHTASTIC=true"
  elif grep -q 'HAL_DISABLE_MESHTASTIC=true' "$HAL_COMPOSE"; then
    echo "  HAL_DISABLE_MESHTASTIC=true already active"
  else
    echo "  WARN: HAL_DISABLE_MESHTASTIC line not found in HAL compose — skipping"
  fi

  # Uncomment HAL_DISABLE_IRIDIUM if commented out
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
  # Force-stop the container first if it refuses to be removed
  docker stop cubeos-hal 2>/dev/null || true
  docker rm -f cubeos-hal 2>/dev/null || true
  if docker compose up -d 2>&1; then
    sleep 3
    echo "  HAL container recreated with serial devices disabled."
  else
    echo "  WARN: HAL container recreation failed (HAL image may not be available — non-fatal on standalone-only devices)"
  fi
else
  echo "No HAL compose found at $HAL_COMPOSE — skipping HAL reconfiguration."
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

# Device port auto-detection: no per-device overrides needed.
# The DeviceSupervisor scans all serial ports (ttyUSB*, ttyACM*, ttyAMA*)
# and identifies devices via VID:PID + protocol probes. Moving hardware
# between devices just works — no config changes required.
#
# Clean up any stale per-device env from previous deploys.
if [ -f /cubeos/config/meshsat.env ]; then
  echo "  Removing stale meshsat.env (auto-detection handles all devices)"
  rm -f /cubeos/config/meshsat.env
fi

# Strip any previously-injected device port env vars from compose file
for VAR in MESHSAT_IRIDIUM_PORT MESHSAT_IMT_PORT MESHSAT_CELLULAR_PORT MESHSAT_ZIGBEE_PORT MESHSAT_MESHTASTIC_PORT; do
  sudo sed -i "/${VAR}/d" docker-compose.direct.yml 2>/dev/null
done

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
