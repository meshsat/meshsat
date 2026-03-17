#!/usr/bin/env bash
# MeshSat Hub — Host-level backup script
#
# Creates an API-driven backup via the MeshSat REST endpoint,
# plus a raw SQLite snapshot and Mosquitto data copy.
#
# Usage:
#   ./backup.sh                    # backup to ./backups/
#   ./backup.sh /mnt/nas/meshsat   # backup to custom directory
#   MESHSAT_URL=https://hub.example.com ./backup.sh   # custom URL
#
# Recommended: run via cron daily
#   0 3 * * * /opt/meshsat/deploy/backup.sh /mnt/nas/meshsat 2>&1 | logger -t meshsat-backup

set -euo pipefail

BACKUP_DIR="${1:-$(dirname "$0")/backups}"
MESHSAT_URL="${MESHSAT_URL:-http://localhost:6050}"
MAX_KEEP="${MAX_KEEP:-14}"
TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)

mkdir -p "$BACKUP_DIR"

echo "[$(date -Iseconds)] Starting MeshSat Hub backup..."

# 1. API-driven backup (config, rules, devices, contacts — application-level)
API_BACKUP="$BACKUP_DIR/meshsat-api-$TIMESTAMP.zip"
HTTP_CODE=$(curl -s -o "$API_BACKUP" -w "%{http_code}" \
  -X POST "$MESHSAT_URL/api/backup/create")

if [ "$HTTP_CODE" = "200" ]; then
  echo "  API backup: $API_BACKUP ($(du -h "$API_BACKUP" | cut -f1))"
else
  echo "  WARNING: API backup failed (HTTP $HTTP_CODE)" >&2
  rm -f "$API_BACKUP"
fi

# 2. SQLite database snapshot (full data including messages/telemetry)
DB_CONTAINER=$(docker compose -f "$(dirname "$0")/docker-compose.prod.yml" ps -q meshsat 2>/dev/null || true)
if [ -n "$DB_CONTAINER" ]; then
  DB_BACKUP="$BACKUP_DIR/meshsat-db-$TIMESTAMP.sqlite"
  docker exec "$DB_CONTAINER" sqlite3 /cubeos/data/meshsat.db ".backup '/tmp/meshsat-backup.db'" 2>/dev/null
  docker cp "$DB_CONTAINER:/tmp/meshsat-backup.db" "$DB_BACKUP" 2>/dev/null
  docker exec "$DB_CONTAINER" rm -f /tmp/meshsat-backup.db 2>/dev/null
  if [ -f "$DB_BACKUP" ]; then
    gzip "$DB_BACKUP"
    echo "  DB snapshot: ${DB_BACKUP}.gz ($(du -h "${DB_BACKUP}.gz" | cut -f1))"
  fi
fi

# 3. Mosquitto data (persistent messages and retained state)
MQTT_CONTAINER=$(docker compose -f "$(dirname "$0")/docker-compose.prod.yml" ps -q mosquitto 2>/dev/null || true)
if [ -n "$MQTT_CONTAINER" ]; then
  MQTT_BACKUP="$BACKUP_DIR/mosquitto-data-$TIMESTAMP.tar.gz"
  docker exec "$MQTT_CONTAINER" tar czf /tmp/mqtt-backup.tar.gz -C /mosquitto/data . 2>/dev/null
  docker cp "$MQTT_CONTAINER:/tmp/mqtt-backup.tar.gz" "$MQTT_BACKUP" 2>/dev/null
  docker exec "$MQTT_CONTAINER" rm -f /tmp/mqtt-backup.tar.gz 2>/dev/null
  if [ -f "$MQTT_BACKUP" ]; then
    echo "  MQTT data:   $MQTT_BACKUP ($(du -h "$MQTT_BACKUP" | cut -f1))"
  fi
fi

# 4. Prune old backups
find "$BACKUP_DIR" -name "meshsat-api-*.zip" -type f | sort -r | tail -n +$((MAX_KEEP + 1)) | xargs -r rm -f
find "$BACKUP_DIR" -name "meshsat-db-*.sqlite.gz" -type f | sort -r | tail -n +$((MAX_KEEP + 1)) | xargs -r rm -f
find "$BACKUP_DIR" -name "mosquitto-data-*.tar.gz" -type f | sort -r | tail -n +$((MAX_KEEP + 1)) | xargs -r rm -f

echo "[$(date -Iseconds)] Backup complete. Directory: $BACKUP_DIR"
ls -lh "$BACKUP_DIR"/*-"$TIMESTAMP"* 2>/dev/null || true
