---
title: "Troubleshooting"
weight: 5
---

# Troubleshooting

## Quick Diagnostics

Run these checks first to narrow down issues:

```bash
# 1. Is MeshSat running?
docker compose -f docker-compose.prod.yml ps

# 2. Is the API healthy?
curl http://localhost:6050/health

# 3. Check recent logs for errors
docker compose -f docker-compose.prod.yml logs --since 10m meshsat

# 4. Check interface states
curl http://localhost:6050/api/interfaces

# 5. Check delivery pipeline
curl http://localhost:6050/api/deliveries/stats
```

---

## Startup Issues

### MeshSat Container Won't Start

**Symptom:** Container exits immediately or enters restart loop.

```bash
docker compose -f docker-compose.prod.yml logs meshsat
```

| Log Message | Cause | Fix |
|-------------|-------|-----|
| `database is locked` | Another instance running | Stop stale containers: `docker ps -a \| grep meshsat` |
| `bind: address already in use` | Port 6050 conflict | Check: `ss -tlnp \| grep 6050` |
| `failed to open database` | Bad DB path or permissions | Verify `MESHSAT_DB_PATH` and directory exists |
| `no such table` | Migration not run | Migrations run automatically — check DB file isn't corrupt |

### Database Corruption

If SQLite reports corruption:

```bash
# Stop MeshSat
docker compose -f docker-compose.prod.yml stop meshsat

# Check integrity
sqlite3 /path/to/meshsat.db "PRAGMA integrity_check;"

# If corrupt, restore from backup
./restore.sh db backups/meshsat-db-LATEST.sqlite.gz
```

---

## Connectivity Issues

### TLS Certificate Not Provisioning

```bash
docker compose -f docker-compose.prod.yml logs caddy
```

| Issue | Fix |
|-------|-----|
| DNS not resolving | Verify: `dig hub.example.com` points to server IP |
| Port 80 blocked | Caddy needs port 80 for ACME HTTP-01 challenge |
| Rate limited | Check Let's Encrypt [rate limits](https://letsencrypt.org/docs/rate-limits/) |
| Wrong domain in `.env` | Update `DOMAIN=` and restart Caddy |

### MQTT — Field Devices Can't Connect

```bash
# Test broker locally
docker compose -f docker-compose.prod.yml exec mosquitto \
  mosquitto_pub -t test -m "hello"

# Check connected clients
docker compose -f docker-compose.prod.yml exec mosquitto \
  mosquitto_sub -t '$SYS/broker/clients/connected' -C 1
```

| Issue | Fix |
|-------|-----|
| Firewall blocking 8883 | Open port: `ufw allow 8883/tcp` |
| TLS cert mismatch | Ensure Mosquitto cert matches the domain field devices connect to |
| Auth rejected | Verify password file: `mosquitto_passwd -U /mosquitto/config/passwd` |
| `connection refused` | Check Mosquitto listener config binds to `0.0.0.0` |

---

## Channel Issues

### Iridium — No Signal

```bash
curl http://localhost:6050/api/iridium/signal
```

| Signal | Meaning |
|--------|---------|
| 0 bars | No satellite visible — check antenna, obstructions, time of day |
| 1-2 bars | Marginal — may work for short messages, expect retries |
| 3-5 bars | Good — normal operation |

**Common fixes:**
- Ensure the antenna has a clear view of the sky (no metal roof overhead)
- Check serial connection: the 9603N is at 19200 baud, TX/RX must NOT be swapped
- After power cycle, wait 30 seconds for modem registration
- Check DLQ for stuck messages: `curl http://localhost:6050/api/deliveries?status=dead_letter`

### Iridium — Messages Stuck in Queue

```bash
# Check queue
curl http://localhost:6050/api/iridium/queue

# Check dead letter queue
curl http://localhost:6050/api/deliveries?status=dead_letter
```

| MO Status | Meaning | Fix |
|-----------|---------|-----|
| 32 | No network service | Wait for better signal, check antenna |
| 35 | Queue full at GSS | Automatic 30s retry — will resolve |
| 36 | Band violation | 3-min backoff active — do not retry manually |

**Warning:** Aggressive manual retries during mo_status 32/36 cause a registration death spiral. Let the DLQ handle backoff automatically.

### Meshtastic — No Nodes Visible

```bash
curl http://localhost:6050/api/nodes
```

- Verify USB serial connection: `ls /dev/ttyACM* /dev/ttyUSB*`
- Check interface state: `curl http://localhost:6050/api/interfaces` — should show `online`
- Ensure the Meshtastic device has the correct region set (EU_868 or US_915)
- Try rebooting the Meshtastic device: `curl -X POST http://localhost:6050/api/admin/reboot`

### Cellular — Modem Not Registering

```bash
curl http://localhost:6050/api/cellular/status
```

- Check SIM card is inserted and PIN is correct: `curl -X POST http://localhost:6050/api/cellular/pin -d '{"pin":"1234"}'`
- Verify signal: `curl http://localhost:6050/api/cellular/signal`
- Check modem detection: `ls /dev/ttyUSB*`
- Some modems need AT+CFUN=1 after insertion — check modem docs

### APRS — No Packets

- Verify Direwolf is running: `docker ps | grep direwolf`
- Check audio levels: Direwolf logs show decode success rate
- Ensure KISS TCP port (8001) is accessible
- Verify frequency: EU uses 144.800 MHz, NA uses 144.390 MHz

---

## Routing & Delivery Issues

### Messages Not Being Delivered

1. **Check access rules** — implicit deny blocks everything not explicitly permitted:
   ```bash
   curl http://localhost:6050/api/access-rules
   ```

2. **Check delivery ledger** for the specific message:
   ```bash
   curl http://localhost:6050/api/deliveries/message/{ref}
   ```

3. **Check loop metrics** — loops are silently dropped:
   ```bash
   curl http://localhost:6050/api/loop-metrics
   ```

4. **Check interface state** — delivery only works to `online` interfaces:
   ```bash
   curl http://localhost:6050/api/interfaces
   ```

### Duplicate Messages

- Check deduplicator is running (it starts automatically)
- Duplicates within 10 minutes are dropped by composite key (source + content hash)
- If MQTT and mesh both receive the same message, this is expected — dedup handles it

---

## Performance Issues

### High Memory Usage

| Component | Expected | Concern |
|-----------|----------|---------|
| MeshSat | 50-100 MB | > 200 MB |
| llama-zip sidecar | 400-600 MB | > 800 MB |
| MSVQ-SC sidecar | 150-250 MB | > 400 MB |
| Mosquitto | 10-30 MB | > 100 MB |

If MeshSat memory grows over time, check:
- Retention settings: `MESHSAT_RETENTION_DAYS` — lower to reduce DB size
- SSE client count: many open `/api/events` connections consume memory

### Slow API Responses

- Check database size: `ls -lh /cubeos/data/meshsat.db`
- Large databases (> 500 MB) may benefit from lower retention
- Run `PRAGMA optimize` via sqlite3 if queries are slow

---

## Backup & Restore Issues

### Backup Fails

```bash
# Check backup script output
./backup.sh /mnt/backups/meshsat 2>&1

# Common issues:
# - Disk full → check: df -h
# - Permission denied → backup dir needs write access
# - API unreachable → MeshSat must be running for API backup
```

### Restore Fails

```bash
# Always preview before restoring
curl -X POST http://localhost:6050/api/backup/preview \
  -F "file=@meshsat-api-backup.zip"
```

- **API restore** (online): MeshSat must be running
- **DB restore** (offline): MeshSat must be stopped first
- Both scripts create a pre-restore snapshot automatically

---

## Getting Help

If the issue persists:

1. Collect diagnostics:
   ```bash
   curl http://localhost:6050/health > health.json
   curl http://localhost:6050/api/interfaces > interfaces.json
   curl http://localhost:6050/api/deliveries/stats > delivery-stats.json
   docker compose -f docker-compose.prod.yml logs --since 1h > meshsat-logs.txt 2>&1
   ```

2. Open an issue on [GitHub](https://github.com/cubeos-app/meshsat/issues) with the collected diagnostics.
