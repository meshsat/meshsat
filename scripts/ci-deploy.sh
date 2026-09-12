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

GHCR_IMAGE="ghcr.io/meshsat/meshsat"
CONTAINER_NAME="meshsat"
HOST_PORT="6050"
HEALTH_TIMEOUT="90"

# =============================================================================
# Survive an SSH drop [MESHSAT-756]
# =============================================================================
# CI runs this script over SSH. On a kit with a flapping WiFi link (parallax,
# MESHSAT-751) the connection can die mid-run: sshd SIGHUPs the session's
# process group and the script stops wherever it happened to be. The dangerous
# window is between "docker rm -f meshsat" and "docker compose up -d" — the kit
# is left with no bridge and nothing brings it back. That is how parallax lost
# its bridge on 2026-09-02.
#
# So the deploy always runs in its OWN session (setsid: new session, new process
# group, no controlling terminal), where sshd's SIGHUP cannot reach it. The
# SSH-attached invocation becomes a follower: it streams the log and exits with
# the detached run's status. If SSH dies, CI reports a failed deploy and the
# detached run still finishes — the kit ends up healthy either way.
DEPLOY_STATE_DIR="/tmp/meshsat-deploy"
DEPLOY_RUNNER="${DEPLOY_STATE_DIR}/run.sh"
DEPLOY_LOG="${DEPLOY_STATE_DIR}/deploy.log"
DEPLOY_STATUS="${DEPLOY_STATE_DIR}/deploy.status"
DEPLOY_LOCK="${DEPLOY_STATE_DIR}/deploy.lock"
DEPLOY_PIDFILE="${DEPLOY_STATE_DIR}/deploy.pid"

if [ "${MESHSAT_DEPLOY_DETACHED:-0}" != "1" ]; then
  # Fail closed. Running attached is exactly the hazard this block removes, so a
  # missing setsid is a refusal, never a fallback.
  if ! command -v setsid > /dev/null 2>&1; then
    echo "ERROR: setsid not found on this device — refusing to run the deploy attached to the SSH session [MESHSAT-756]"
    exit 1
  fi

  mkdir -p "$DEPLOY_STATE_DIR"

  # Run from a copy: CI deletes /tmp/ci-deploy-meshsat.sh as soon as the SSH
  # command returns, which can be while the detached run is still going.
  if [ "$0" != "$DEPLOY_RUNNER" ]; then
    cp "$0" "$DEPLOY_RUNNER"
    chmod +x "$DEPLOY_RUNNER"
  fi

  rm -f "$DEPLOY_STATUS" "$DEPLOY_PIDFILE"
  : > "$DEPLOY_LOG"

  MESHSAT_DEPLOY_DETACHED=1 setsid bash "$DEPLOY_RUNNER" \
    >> "$DEPLOY_LOG" 2>&1 < /dev/null &
  DEPLOY_PID=$!

  # setsid execs in place when the caller is not already a process group leader
  # (the usual case for a background job in a non-interactive shell) but forks
  # when it is — and then $! is setsid's PID, which exits immediately and would
  # look to the follower below like a dead deploy. The child records its own PID
  # instead; $! is only the fallback.
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s "$DEPLOY_PIDFILE" ] && break
    sleep 1
  done
  if [ -s "$DEPLOY_PIDFILE" ]; then
    DEPLOY_PID=$(cat "$DEPLOY_PIDFILE")
  fi
  echo "Deploy running detached as PID ${DEPLOY_PID} (log: ${DEPLOY_LOG})"

  # Follow the log until the detached run records its exit status. Deliberately
  # a poll loop rather than "tail -f --pid": no coreutils version assumptions,
  # and the status file is written by the child's EXIT trap after all of its
  # output, so observing it means the log is already complete.
  DEPLOY_SEEN=0
  DEPLOY_GRACE=0
  while :; do
    DEPLOY_DONE=0
    [ -f "$DEPLOY_STATUS" ] && DEPLOY_DONE=1

    DEPLOY_SIZE=$(stat -c %s "$DEPLOY_LOG" 2>/dev/null || echo 0)
    if [ "$DEPLOY_SIZE" -gt "$DEPLOY_SEEN" ]; then
      tail -c "+$((DEPLOY_SEEN + 1))" "$DEPLOY_LOG" 2>/dev/null || true
      DEPLOY_SEEN="$DEPLOY_SIZE"
    fi

    [ "$DEPLOY_DONE" = "1" ] && break

    # The child died without recording a status (killed, OOM). Give it a few
    # seconds in case the trap is still running, then give up — but note that
    # the container state is whatever the child left behind, so say so loudly.
    if ! kill -0 "$DEPLOY_PID" 2>/dev/null; then
      DEPLOY_GRACE=$((DEPLOY_GRACE + 1))
      if [ "$DEPLOY_GRACE" -ge 5 ]; then
        echo "ERROR: detached deploy (PID ${DEPLOY_PID}) exited without recording a status."
        echo "       Check ${DEPLOY_LOG} and the container state on this device."
        exit 1
      fi
    fi

    sleep 2
  done

  DEPLOY_RC=$(cat "$DEPLOY_STATUS" 2>/dev/null || echo 1)
  exit "${DEPLOY_RC:-1}"
fi

# --- Detached run from here on ---
trap '' HUP
trap 'MESHSAT_DEPLOY_RC=$?; echo "$MESHSAT_DEPLOY_RC" > "$DEPLOY_STATUS"' EXIT
echo $$ > "$DEPLOY_PIDFILE"

# One deploy at a time. A CI retry while an earlier detached run is still
# tearing down and recreating the container is the one way left to produce the
# interleaving this whole block exists to prevent.
if command -v flock > /dev/null 2>&1; then
  exec 9> "$DEPLOY_LOCK"
  if ! flock -w 900 9; then
    echo "ERROR: another MeshSat deploy has held the lock for 15 minutes — refusing to start"
    exit 1
  fi
fi

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
# One retry before giving up. On 2026-09-05 (pipeline 51914) the first pull
# timed out on parallax's WiFi after most layers had landed and the second
# attempt took five seconds, because everything was already cached. [MESHSAT-809]
PULL_OK=0
for PULL_ATTEMPT in 1 2; do
  PULL_TIMEOUT=$((PULL_ATTEMPT * 120))
  echo "Pulling ${GHCR_IMAGE}:latest from GHCR (attempt ${PULL_ATTEMPT}, timeout ${PULL_TIMEOUT}s)..."
  if timeout "$PULL_TIMEOUT" docker pull "${GHCR_IMAGE}:latest" 2>&1; then
    PULL_OK=1
    break
  fi
  echo "  Pull attempt ${PULL_ATTEMPT} failed"
done
if [ "$PULL_OK" != "1" ]; then
  echo "  Both pull attempts failed — falling back to the cached image, which must still"
  echo "  match the digest the pipeline built (checked below)."
fi

# Never take the running bridge down for an image we do not have. On
# 2026-09-02 a pull timed out on a flapping WiFi link, the container was
# stopped and removed, and compose then failed with "No such image",
# leaving the kit without a bridge. [MESHSAT-756]
if ! docker image inspect "${GHCR_IMAGE}:latest" >/dev/null 2>&1; then
  echo "ERROR: ${GHCR_IMAGE}:latest is not available locally after the pull; leaving the running container untouched"
  exit 1
fi

# =============================================================================
# The image must be the one this pipeline built [MESHSAT-809]
# =============================================================================
# Until now the only check was "some image carries the :latest tag", which a
# stale cached image satisfies. The container was then recreated from it, the
# old build answered /health, and the job exited 0 — a green deploy on
# yesterday's code (parallax, 5 Sep 2026, four minutes on the 3 Sep image).
#
# MESHSAT_EXPECTED_DIGEST is the manifest digest the package stage pushed.
# MESHSAT_EXPECTED_SHA is the commit short sha, which package also pushes as
# its own tag and which is only used to make the log readable.
#
# This runs BEFORE the container is stopped, so a mismatch costs nothing: the
# bridge keeps running on whatever it already had.
EXPECTED_DIGEST="${MESHSAT_EXPECTED_DIGEST:-}"
LOCAL_DIGESTS=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "${GHCR_IMAGE}:latest" 2>/dev/null || true)
LOCAL_IMAGE_ID=$(docker image inspect --format '{{.Id}}' "${GHCR_IMAGE}:latest" 2>/dev/null || echo "unknown")

if [ -z "$EXPECTED_DIGEST" ]; then
  echo "WARNING: MESHSAT_EXPECTED_DIGEST is not set — cannot prove this is the image the"
  echo "         pipeline built. Deploying UNVERIFIED (expected only for a hand-run)."
  IMAGE_VERIFIED="UNVERIFIED"
elif printf '%s' "$LOCAL_DIGESTS" | grep -qF "$EXPECTED_DIGEST"; then
  echo "  Image digest matches the pipeline: ${EXPECTED_DIGEST}"
  IMAGE_VERIFIED="verified"
else
  echo ""
  echo "ERROR: the local ${GHCR_IMAGE}:latest is NOT the image this pipeline built."
  echo "       expected digest: ${EXPECTED_DIGEST}"
  echo "       local digests:   ${LOCAL_DIGESTS:-none}"
  echo "       local image id:  ${LOCAL_IMAGE_ID}"
  echo "       commit:          ${MESHSAT_EXPECTED_SHA:-unknown}"
  echo ""
  echo "       The pull did not deliver the new image (a timeout on this device's link is"
  echo "       the usual cause). The running container has been left untouched and is still"
  echo "       serving the previous build. Re-run the deploy, or on the device:"
  echo "         cd ${COMPOSE_DIR} && docker compose pull && docker compose up -d"
  exit 1
fi

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
    # What is actually RUNNING, not what we hoped to deploy. A tag proves
    # nothing; this is the line to read when a deploy is in doubt. [MESHSAT-809]
    RUNNING_IMAGE=$(docker inspect -f '{{.Image}}' "${CONTAINER_NAME}" 2>/dev/null || echo "unknown")
    RUNNING_CREATED=$(docker image inspect -f '{{.Created}}' "${RUNNING_IMAGE}" 2>/dev/null || echo "unknown")
    echo "Running: ${RUNNING_IMAGE}"
    echo "Built:   ${RUNNING_CREATED}"
    echo "Commit:  ${MESHSAT_EXPECTED_SHA:-unknown} (${IMAGE_VERIFIED})"
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
