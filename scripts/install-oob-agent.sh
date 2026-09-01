#!/usr/bin/env bash
# install-oob-agent.sh: install the MeshSat OOB host agent on a field kit.
#
# The bridge container executes remote management commands (REBOOT, RESET,
# LOG, STATUS-NET) through a small host agent with a fixed allowlist,
# reached over a Unix socket in /run/meshsat-oob/. This script installs:
#
#   1. /usr/local/bin/meshsat-oob-agent          (Python 3, stdlib only)
#   2. meshsat-oob-agent.socket + .service        (socket activation)
#   3. /etc/tmpfiles.d/meshsat-oob.conf           (the socket DIRECTORY,
#      created at boot and never recreated by the service, so the
#      container's bind mount of the directory stays valid)
#   4. the bind mount line in the kit's compose file (/srv/meshsat/
#      docker-compose.yml by default), with a backup and a validation
#
# What this deliberately does NOT do: restart the bridge container. Adding
# the mount only takes effect on the next `docker compose up -d`, which is
# a management action for the operator (set MESHSAT_OOB_RESTART_BRIDGE=1
# to have this script do it). It never touches /etc/netplan.
#
# Idempotent. Safe to re-run.
#
# Usage:
#   sudo bash scripts/install-oob-agent.sh
#   sudo MESHSAT_COMPOSE=/srv/meshsat/docker-compose.yml bash scripts/install-oob-agent.sh
#   sudo MESHSAT_OOB_RESTART_BRIDGE=1 bash scripts/install-oob-agent.sh
#
# [MESHSAT-756]

set -euo pipefail

COMPOSE="${MESHSAT_COMPOSE:-/srv/meshsat/docker-compose.yml}"
RESTART_BRIDGE="${MESHSAT_OOB_RESTART_BRIDGE:-0}"
MOUNT_LINE="      - /run/meshsat-oob:/run/meshsat-oob"
SOCKET_PATH="/run/meshsat-oob/agent.sock"

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DEPLOY_DIR="$REPO_DIR/deploy/oob"

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "This script must run as root: sudo bash $0" >&2
  exit 1
fi

for f in meshsat-oob-agent meshsat-oob-agent.socket meshsat-oob-agent.service meshsat-oob.tmpfiles.conf; do
  if [ ! -f "$DEPLOY_DIR/$f" ]; then
    echo "Deploy file missing: $DEPLOY_DIR/$f" >&2
    exit 1
  fi
done

echo "══ MeshSat OOB host agent provisioning ══"
echo "  compose : $COMPOSE"
echo "  socket  : $SOCKET_PATH"

# ─── 1. agent binary ───
echo "[1/5] Installing agent"
command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 1; }
install -m 0755 "$DEPLOY_DIR/meshsat-oob-agent" /usr/local/bin/meshsat-oob-agent

# ─── 2. socket directory via tmpfiles.d ───
echo "[2/5] Socket directory"
install -m 0644 "$DEPLOY_DIR/meshsat-oob.tmpfiles.conf" /etc/tmpfiles.d/meshsat-oob.conf
systemd-tmpfiles --create /etc/tmpfiles.d/meshsat-oob.conf

# ─── 3. units ───
echo "[3/5] Units"
install -m 0644 "$DEPLOY_DIR/meshsat-oob-agent.socket" /etc/systemd/system/meshsat-oob-agent.socket
install -m 0644 "$DEPLOY_DIR/meshsat-oob-agent.service" /etc/systemd/system/meshsat-oob-agent.service
systemctl daemon-reload
systemctl enable --now meshsat-oob-agent.socket >/dev/null 2>&1
# A running service picks up a new agent binary only on restart.
if systemctl is-active --quiet meshsat-oob-agent.service; then
  systemctl restart meshsat-oob-agent.service
fi

# ─── 4. self-test ───
echo "[4/5] Self-test"
python3 - "$SOCKET_PATH" <<'EOF'
import json, socket, sys
path = sys.argv[1]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5)
s.connect(path)
s.sendall(b'{"action":"ping"}\n')
reply = json.loads(s.recv(4096).decode())
s.close()
if not reply.get("ok"):
    print("agent ping failed: %r" % reply)
    sys.exit(1)
print("  agent answers, version %s" % reply.get("version"))
EOF

# ─── 5. compose bind mount ───
echo "[5/5] Compose mount"
COMPOSE_STATUS="not found"
if [ -f "$COMPOSE" ]; then
  if grep -qF '/run/meshsat-oob:/run/meshsat-oob' "$COMPOSE"; then
    COMPOSE_STATUS="already present"
  elif grep -qE '^\s*- /dev:/dev' "$COMPOSE"; then
    cp -p "$COMPOSE" "$COMPOSE.bak-$(date +%Y%m%d%H%M%S)"
    # Insert after the first /dev:/dev volume line, matching its indent.
    awk -v line="$MOUNT_LINE" '
      { print }
      !done && $0 ~ /^[[:space:]]*- \/dev:\/dev/ {
        indent = $0; sub(/- \/dev:\/dev.*/, "", indent)
        sub(/^[[:space:]]*/, "", line)
        print indent line
        done = 1
      }' "$COMPOSE" > "$COMPOSE.tmp" && mv "$COMPOSE.tmp" "$COMPOSE"
    if docker compose -f "$COMPOSE" config >/dev/null 2>&1; then
      COMPOSE_STATUS="added (validated)"
    else
      COMPOSE_STATUS="added but 'docker compose config' FAILED, review $COMPOSE"
    fi
  else
    COMPOSE_STATUS="no '- /dev:/dev' anchor found, add manually: $MOUNT_LINE"
  fi
fi

echo "══ Done ══"
echo "  socket unit : $(systemctl is-active meshsat-oob-agent.socket)"
echo "  agent       : $(systemctl is-active meshsat-oob-agent.service 2>&1)"
echo "  directory   : $(ls -ld /run/meshsat-oob 2>&1 | awk '{print $1, $3, $4}')"
echo "  compose     : $COMPOSE_STATUS"
case "$COMPOSE_STATUS" in
  added*)
    if [ "$RESTART_BRIDGE" = "1" ]; then
      echo "  restarting the bridge container (MESHSAT_OOB_RESTART_BRIDGE=1)"
      docker compose -f "$COMPOSE" up -d
    else
      echo ""
      echo "  The mount takes effect on the next bridge start. When you are ready:"
      echo "    docker compose -f $COMPOSE up -d"
      echo "  then verify inside the container:"
      echo "    docker exec meshsat ls -l /run/meshsat-oob/"
      echo "    curl -s http://localhost:6050/api/oob/agent"
    fi
    ;;
esac
