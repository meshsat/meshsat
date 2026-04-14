# Hub TAK + APRS-IS Deployment Guide

## TAK/CoT on Hub

Hub runs OpenTAKServer as an optional Docker Compose service. When enabled, Hub's Go service subscribes to device MQTT topics and forwards position/SOS/telemetry data to OpenTAKServer as CoT events.

### Enable TAK

```bash
# Start Hub with TAK profile
docker compose --profile tak up -d
```

Configure in `.env` or `docker-compose.yml` environment:

```bash
HUB_TAK_ENABLED=true
HUB_TAK_HOST=opentakserver      # Internal Docker hostname
HUB_TAK_PORT=8087               # TCP CoT (plaintext, internal)
HUB_TAK_SSL=false               # TLS not needed for internal Docker network
HUB_TAK_CALLSIGN_PREFIX=MESHSAT-HUB
HUB_TAK_COT_STALE_SECONDS=600  # 10 min — appropriate for satellite update intervals
```

### How It Works

1. Hub subscribes to MQTT topics: `meshsat/+/position`, `meshsat/+/sos`, `meshsat/+/telemetry`, `meshsat/+/mo/decoded`
2. When a message arrives from any field device (Bridge or Android, via Iridium webhook, Tor MQTT, or WireGuard MQTT), Hub converts it to CoT XML
3. Hub sends CoT to OpenTAKServer on port 8087 (internal Docker network, no TLS needed)
4. ATAK/WinTAK/iTAK clients connect to OpenTAKServer on ports 8087 (TCP), 8089 (TLS), or 8443 (HTTPS)

### MQTT → CoT Mapping

| MQTT Topic | CoT Event Type | Detail |
|------------|---------------|--------|
| `meshsat/{id}/position` | `a-f-G-U-C` | PLI with lat/lon from JSON |
| `meshsat/{id}/sos` | `a-f-G-U-C` + `<emergency>` | Emergency detail block |
| `meshsat/{id}/telemetry` | `t-x-d-d` | Sensor data in remarks |
| `meshsat/{id}/mo/decoded` | `b-t-f` or `a-f-G-U-C` | Text message or position depending on content |

### Connecting TAK Clients

TAK clients (ATAK, WinTAK, iTAK) connect directly to OpenTAKServer:

- **TCP CoT**: `<hub-ip>:8087`
- **TLS CoT**: `<hub-ip>:8089` (requires cert configuration in OpenTAKServer)
- **HTTPS DataPackage**: `<hub-ip>:8443`

### Android Benefit

Android nodes that cannot run a direct TAK connection benefit automatically. Android publishes positions to Hub via MQTT → Hub forwards to OpenTAKServer → ATAK operators see all field devices on the TAK map without Android needing any TAK-specific code.

---

## APRS-IS IGate on Hub

Hub can connect to the APRS-IS network as an Internet Gateway (IGate). This makes satellite-originated positions visible on aprs.fi and other APRS tracking sites.

### Enable APRS-IS

```bash
HUB_APRSIS_ENABLED=true
HUB_APRSIS_SERVER=euro.aprs2.net:14580   # EU tier-2 server
HUB_APRSIS_CALLSIGN=PA3XYZ               # Your amateur radio callsign
HUB_APRSIS_PASSCODE=12345                 # APRS-IS verification passcode
```

**Note:** APRS-IS requires a valid amateur radio callsign and the corresponding APRS verification passcode. Generate your passcode from your callsign using any standard APRS-IS passcode calculator.

### How It Works

1. Hub subscribes to `meshsat/+/position` and `meshsat/+/mo/decoded` MQTT topics
2. When a position arrives from a satellite channel (Iridium SBD/IMT), Hub formats it as an APRS position packet
3. Hub sends the APRS packet to APRS-IS via TCP (standard APRS-IS protocol)
4. The position appears on aprs.fi, aprsdirect.com, and all APRS-IS clients worldwide

### APRS-IS Packet Format

Hub generates APRS-IS packets in the standard third-party format:

```
CALLSIGN-10>APMSHT,TCPIP*:!DDMM.MMN/DDDMM.MMW-MeshSat via Iridium SBD
```

Where:
- `CALLSIGN-10` is your configured callsign with SSID 10 (IGate convention)
- `APMSHT` is the MeshSat tocall
- Position is uncompressed APRS format
- Comment identifies the satellite source

### Reverse Direction (APRS-IS → Field Devices)

Hub can also receive APRS-IS messages addressed to registered MeshSat devices:

1. Hub subscribes to APRS-IS for messages to configured callsigns
2. Incoming APRS messages are published to `meshsat/{device_id}/mt/send`
3. Hub's MT sender forwards via Cloudloop API → Iridium MT → field device

This makes MeshSat devices reachable from the global APRS network.

### EU Frequency Reference

APRS-IS is frequency-agnostic (internet only), but for reference:
- **EU APRS**: 144.800 MHz
- **North America**: 144.390 MHz
- Bridge APRS adapter uses the RF frequency; Hub APRS-IS uses only TCP
