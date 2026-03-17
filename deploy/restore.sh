#!/usr/bin/env bash
# MeshSat Hub — Restore script
#
# Restores from backup files created by backup.sh.
#
# Usage:
#   ./restore.sh api   backups/meshsat-api-20260317-030000.zip
#   ./restore.sh db    backups/meshsat-db-20260317-030000.sqlite.gz
#   ./restore.sh mqtt  backups/mosquitto-data-20260317-030000.tar.gz
#
# WARNING: This replaces current state. The script creates a pre-restore
# backup automatically before applying changes.

set -euo pipefail

COMPOSE_FILE="$(dirname "$0")/docker-compose.prod.yml"
MESHSAT_URL="${MESHSAT_URL:-http://localhost:6050}"

usage() {
  echo "Usage: $0 <type> <backup-file>"
  echo ""
  echo "Types:"
  echo "  api   — Restore via MeshSat API (config, rules, devices, contacts)"
  echo "  db    — Restore full SQLite database (stops meshsat during restore)"
  echo "  mqtt  — Restore Mosquitto data (stops mosquitto during restore)"
  exit 1
}

[ $# -eq 2 ] || usage

TYPE="$1"
FILE="$2"

[ -f "$FILE" ] || { echo "ERROR: File not found: $FILE" >&2; exit 1; }

case "$TYPE" in
  api)
    echo "Previewing API restore from: $FILE"
    PREVIEW=$(curl -s -X POST "$MESHSAT_URL/api/backup/preview" \
      -F "file=@$FILE" 2>/dev/null)
    echo "$PREVIEW" | python3 -m json.tool 2>/dev/null || echo "$PREVIEW"
    echo ""
    read -rp "Apply this restore? [y/N] " CONFIRM
    [ "$CONFIRM" = "y" ] || { echo "Aborted."; exit 0; }

    # Pre-restore backup
    echo "Creating pre-restore backup..."
    curl -s -o "/tmp/meshsat-pre-restore-$(date +%s).zip" \
      -X POST "$MESHSAT_URL/api/backup/create"

    echo "Restoring..."
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
      -X POST "$MESHSAT_URL/api/backup/restore" \
      -F "file=@$FILE")
    if [ "$HTTP_CODE" = "200" ]; then
      echo "API restore complete."
    else
      echo "ERROR: Restore failed (HTTP $HTTP_CODE)" >&2
      exit 1
    fi
    ;;

  db)
    echo "Restoring SQLite database from: $FILE"
    read -rp "This will stop MeshSat and replace the database. Continue? [y/N] " CONFIRM
    [ "$CONFIRM" = "y" ] || { echo "Aborted."; exit 0; }

    # Pre-restore: copy current DB
    DB_CONTAINER=$(docker compose -f "$COMPOSE_FILE" ps -q meshsat 2>/dev/null || true)
    if [ -n "$DB_CONTAINER" ]; then
      echo "Creating pre-restore DB snapshot..."
      docker exec "$DB_CONTAINER" sqlite3 /cubeos/data/meshsat.db \
        ".backup '/tmp/pre-restore.db'" 2>/dev/null
      docker cp "$DB_CONTAINER:/tmp/pre-restore.db" \
        "/tmp/meshsat-pre-restore-$(date +%s).sqlite" 2>/dev/null || true
    fi

    echo "Stopping MeshSat..."
    docker compose -f "$COMPOSE_FILE" stop meshsat

    # Decompress if needed
    RESTORE_FILE="$FILE"
    if [[ "$FILE" == *.gz ]]; then
      RESTORE_FILE="/tmp/meshsat-restore-$$.sqlite"
      gunzip -c "$FILE" > "$RESTORE_FILE"
    fi

    # Copy into volume
    docker compose -f "$COMPOSE_FILE" run --rm -v "$(realpath "$RESTORE_FILE"):/restore.db:ro" \
      meshsat cp /restore.db /cubeos/data/meshsat.db

    echo "Starting MeshSat..."
    docker compose -f "$COMPOSE_FILE" up -d meshsat

    # Clean up temp file
    [ "$RESTORE_FILE" != "$FILE" ] && rm -f "$RESTORE_FILE"
    echo "Database restore complete."
    ;;

  mqtt)
    echo "Restoring Mosquitto data from: $FILE"
    read -rp "This will stop Mosquitto and replace its data. Continue? [y/N] " CONFIRM
    [ "$CONFIRM" = "y" ] || { echo "Aborted."; exit 0; }

    echo "Stopping Mosquitto..."
    docker compose -f "$COMPOSE_FILE" stop mosquitto

    MQTT_CONTAINER=$(docker compose -f "$COMPOSE_FILE" run --rm -d mosquitto sleep 30 2>/dev/null)
    docker cp "$FILE" "$MQTT_CONTAINER:/tmp/restore.tar.gz"
    docker exec "$MQTT_CONTAINER" sh -c "rm -rf /mosquitto/data/* && tar xzf /tmp/restore.tar.gz -C /mosquitto/data/"
    docker stop "$MQTT_CONTAINER" 2>/dev/null || true

    echo "Starting Mosquitto..."
    docker compose -f "$COMPOSE_FILE" up -d mosquitto
    echo "Mosquitto restore complete."
    ;;

  *)
    usage
    ;;
esac
