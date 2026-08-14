# RockBLOCK 9704 Comprehensive Test Report

**Date:** 2026-03-24
**Tester:** kyriakosp (automated via Claude Code)
**Modem:** RockBLOCK 9704, IMEI 30025806XXXXXXX, Serial 1a07ty, HW 0x0601
**ICCID:** 8988169771XXXXXXXXX (internal eSIM)
**JSPR API:** v1.7.0 (also supports v1.6.1, v1.6.0)
**Firmware interface:** JSPR (JSON-Based Serial Protocol for REST)

---

## 1. Test Environments

### 1.1 Laptop (x86_64)
- **Host:** ankh (kyriakosp@ankh)
- **OS:** Linux 6.17.0-19-generic x86_64, Ubuntu
- **USB:** FTDI FT234XD, VID:PID 0403:6015
- **Serial port:** `/dev/ttyUSB0` (re-enumerated from `/dev/ttyUSB1` after I/O error)
- **Python:** 3.x with pyserial 3.5

### 1.2 Raspberry Pi / Mule (aarch64)
- **Host:** nllei01mule01 (cubeos@nllei01mule01-wireless)
- **OS:** Ubuntu 24.04.3 LTS, Linux 6.8.0-1031-raspi aarch64
- **Platform:** CubeOS v0.2.0-beta.05
- **USB:** FTDI FT234XD, VID:PID 0403:6015, Bus 002 Device 003
- **Serial port:** `/dev/ttyUSB0`
- **Python:** 3.x with pyserial 3.5
- **Blocker:** `cubeos-hal` Docker container grabs the serial port; `cubeos-watchdog.timer` respawns it every ~60s. Both must be disabled before testing (see Section 6).

---

## 2. Serial / Protocol Configuration

| Parameter | Value |
|-----------|-------|
| Baud rate | 230400 (fixed) |
| Data bits | 8 |
| Parity | None |
| Stop bits | 1 |
| Flow control | None (no XON/XOFF, no RTS/CTS, no DSR/DTR) |
| Line format (TX) | `METHOD target {json}\r` |
| Line format (RX) | `CODE target {json}\r` |
| JSON spacing | **Mandatory** spaces after `:` and `,` — e.g. `{"key": "value", "key2": 42}` |
| JSON in Python | `json.dumps(obj, separators=(", ", ": "))` |

### JSPR Response Codes

| Code | Meaning |
|------|---------|
| 200 | Success |
| 299 | Unsolicited / async message |
| 400 | Bad request (modem not ready / API not negotiated) |
| 402 | Already configured (idempotent repeat) |
| 405 | Malformed (cold start, first command after port open) |
| 406 | Already in requested state |
| 407 | Bad JSON (spacing violation or wrong field names) |
| 410 | Precondition not met (e.g. activate without SIM) |
| 418 | Operation not possible in current state |

---

## 3. Test Results

### 3.1 Handshake

#### Laptop
```
TX: GET apiVersion {}
RX: 200 apiVersion {"supported_versions":[{"major":1,"minor":7,"patch":0},
     {"major":1,"minor":6,"patch":1},{"major":1,"minor":6,"patch":0}]}

TX: PUT apiVersion {"active_version": {"major": 1, "minor": 7, "patch": 0}}
RX: 200 apiVersion {"supported_versions":[...],"active_version":{"major":1,"minor":7,"patch":0}}

TX: PUT simConfig {"interface": "internal"}
RX: 200 simConfig {"interface":"internal"}
RX: 299 simStatus {"card_present":true,"sim_connected":true,"iccid":"8988169771XXXXXXXXX"}

TX: PUT operationalState {"state": "active"}
RX: 200 operationalState {"state":"active"}
RX: 299 operationalState {"state":"active","reason":0}
RX: 299 constellationState {"constellation_visible":false,"signal_bars":0}
RX: 299 constellationState {"constellation_visible":true,"signal_bars":4,"signal_level":-111}

TX: GET hwInfo {}
RX: 200 hwInfo {"hw_version":"0x0601","serial_number":"1a07ty","imei":"30025806XXXXXXX","board_temp":19}
```

#### Mule (Pi)
```
TX: GET apiVersion {}
RX: 200 apiVersion {"supported_versions":[...],"active_version":{"major":1,"minor":7,"patch":0}}

TX: PUT apiVersion {"active_version": {"major": 1, "minor": 7, "patch": 0}}
RX: 402 apiVersion {}   (already negotiated by previous HAL session)

TX: PUT simConfig {"interface": "internal"}
RX: 402 simConfig {}    (already configured)
RX: 299 constellationState {"constellation_visible":true,"signal_bars":4,"signal_level":-109}

TX: PUT operationalState {"state": "active"}
RX: 406 operationalState {}   (already active)
RX: 299 constellationState {"constellation_visible":true,"signal_bars":4,"signal_level":-109}
RX: 299 constellationState {"constellation_visible":true,"signal_bars":5,"signal_level":-108}

TX: GET hwInfo {}
RX: 200 hwInfo {"hw_version":"0x0601","serial_number":"1a07ty","imei":"30025806XXXXXXX","board_temp":17}
```

### 3.2 Signal

| Environment | Constellation Visible | Signal Bars | Signal Level (dBm) |
|-------------|----------------------|-------------|-------------------|
| Laptop | true | 0–5 (fluctuating) | -110 to -118 |
| Mule (Pi) | true | 0–5 (fluctuating) | -108 to -128 |

### 3.3 MO (Mobile Originated) Messages Sent

#### Laptop — Message ID 5
```
Payload: "MeshSat laptop test 2026-03-24T18:28:01Z"
CRC-16/CCITT: 0xAAEB
Total length (with CRC): 42 bytes
Base64: TWVzaFNhdCBsYXB0b3AgdGVzdCAyMDI2LTAzLTI0VDE4OjI4OjAxWqqr

TX: PUT messageOriginate {"topic_id": 244, "message_length": 42, "request_reference": 1}
RX: 200 messageOriginate {"topic_id":244,"request_reference":1,"message_id":5,
     "message_response":"message_accepted"}
RX: 299 messageOriginateSegment {"topic_id":244,"message_id":5,"segment_length":42,"segment_start":0}

TX: PUT messageOriginateSegment {"topic_id": 244, "message_id": 5, "segment_length": 42,
     "segment_start": 0, "data": "<base64>"}
RX: 200 messageOriginateSegment {"topic_id":244,"message_id":5}
RX: 299 messageOriginateStatus {"topic_id":244,"message_id":5,"final_mo_status":"mo_ack_received"}

Result: SUCCESS — mo_ack_received
```

#### Mule (Pi) — Message ID 1
```
Payload: "MeshSat mule test 2026-03-24T18:46:23Z"
CRC-16/CCITT: 0x56C5
Total length (with CRC): 40 bytes

TX: PUT messageOriginate {"topic_id": 244, "message_length": 40, "request_reference": 1}
RX: 200 messageOriginate {"topic_id":244,"request_reference":1,"message_id":1,
     "message_response":"message_accepted"}
RX: 299 messageOriginateSegment {"topic_id":244,"message_id":1,"segment_length":40,"segment_start":0}

TX: PUT messageOriginateSegment {"topic_id": 244, "message_id": 1, "segment_length": 40,
     "segment_start": 0, "data": "<base64>"}
RX: 200 messageOriginateSegment {"topic_id":244,"message_id":1}
RX: 299 messageOriginateStatus {"topic_id":244,"message_id":1,"final_mo_status":"mo_ack_received"}

Result: SUCCESS — mo_ack_received
```

### 3.4 MT (Mobile Terminated) Messages Received

#### Laptop — 4 messages during MO satellite pass
| Msg ID | Base64 | Payload (text) | Length | CRC |
|--------|--------|---------------|--------|-----|
| 30 | `dHN0Z9E=` | `tst` | 5 bytes | 0x67D1 |
| 31 | `dHN0Z9E=` | `tst` | 5 bytes | 0x67D1 |
| 32 | `dHN0Z9E=` | `tst` | 5 bytes | 0x67D1 |
| 33 | `dHN0Z9E=` | `tst` | 5 bytes | 0x67D1 |

#### Mule (Pi) — 11 messages over 5-minute poll
| Msg ID | Base64 | Payload (text) | Length |
|--------|--------|---------------|--------|
| 34 | `dHN0Z9E=` | `tst` | 5 |
| 35 | `dHN0Z9E=` | `tst` | 5 |
| 36 | `VFNUMzMzMzMzMzMzMzNHhw==` | `TST33333333333` | 16 |
| 37 | `dHN0NjY2NjY2NjY2Tlg=` | `tst666666666` | 14 |
| 38 | `dHN0Nzc3Nzc3Nzc3NzciYA==` | `tst77777777777` | 16 |
| 39 | `dHN0MTExMTExMTExMTExMTExGUM=` | `tst111111111111111` | 20 |
| 40 | `dHN0MjIyMjIyMjIyMjIyuS0=` | `tst222222222222` | 17 |
| 41 | `dHN0MzMzMzMzMzMzM7uJ` | `tst3333333333` | 15 |
| 42 | `YWFhYWFhYWFhYWFhYWFhYWFhYWERrA==` | `aaaaaaaaaaaaaaaaaaaa` | 22 |
| 43 | `YmJiYmJiYmJiYmLdPg==` | `bbbbbbbbbbb` | 13 |
| 44 | `Y2NjY2NjY2NjY2NjY2NjY7A+` | `cccccccccccccccc` | 18 |

All MT messages: topic_id=244, final_mt_status=`complete`.

---

## 4. Failed Attempts and Lessons Learned

### 4.1 simConfig: "embedded" returns 407
The initial prompt specified `"embedded"` as the SIM interface value. The firmware rejected it with 407 BAD_JSON. The correct value is `"internal"`, found by reading the official C library source at `rock7/RockBLOCK-9704` on GitHub.

Valid `simConfig` interface values (from `jspr.c`):
- `"none"` — no SIM configured
- `"local"` — local SIM
- `"remote"` — remote SIM
- `"internal"` — onboard eSIM (correct for RockBLOCK 9704)

### 4.2 PUT apiVersion: wrong JSON key
The initial prompt did not specify the JSON key for version negotiation. Attempts with top-level `{"major": 1, ...}` all returned 407. The correct key is `"active_version"`, found in `jspr_command.c`:
```c
"PUT apiVersion {\"active_version\": {\"major\": %d, \"minor\": %d, \"patch\": %d}}\r"
```

### 4.3 CRC: byte-sum vs CRC-16/CCITT
The initial prompt specified a simple byte-sum CRC. This produced `message_dropped_local_crc_error`. The correct algorithm is **CRC-16/CCITT (XModem)** with initial value 0, using a 256-entry lookup table, found in `rockblock_9704.c`.

### 4.4 messageOriginateSegment: missing fields
Sending only `{"message_id": X, "segment_data": "..."}` returned 407. The correct format requires **all fields** echoed back from the 299 segment request, plus the `"data"` field (not `"segment_data"`):
```
PUT messageOriginateSegment {"topic_id": T, "message_id": M,
    "segment_length": L, "segment_start": S, "data": "BASE64"}
```
Found in `jspr_command.c`:
```c
"PUT messageOriginateSegment {\"topic_id\":%d, \"message_id\":%d,
    \"segment_length\":%ld, \"segment_start\":%d, \"data\":\"%s\"}\r"
```

### 4.5 405 MALFORMED on cold start
The first command sent after opening the serial port always returns `405 MALFORMED {}`. This is expected behavior — the modem's UART buffer contains stale data. Solution: send a throwaway `GET apiVersion {}`, discard the response, then proceed with real commands.

### 4.6 cubeos-hal port contention on Pi
The `cubeos-hal` Docker container (restart policy: `unless-stopped`) opens `/dev/ttyUSB0` and holds it. The `cubeos-watchdog.timer` systemd timer runs `/cubeos/coreapps/scripts/watchdog-health.sh` every ~60 seconds, which restarts HAL if it's not running. Both must be disabled:
```bash
sudo systemctl stop cubeos-watchdog.timer
sudo systemctl disable cubeos-watchdog.timer
sudo docker stop cubeos-hal
```
To re-enable after testing:
```bash
sudo systemctl enable --now cubeos-watchdog.timer
```

---

## 5. Code Used

### 5.1 Laptop (x86_64) — Interactive one-shot commands

All laptop tests were run as individual pyserial commands. The successful MO sequence:

```python
#!/usr/bin/env python3
"""RockBLOCK 9704 — laptop MO send (x86_64). Run with: python3 laptop_mo.py"""
import serial, time, json, base64, struct
from datetime import datetime, timezone

PORT = "/dev/ttyUSB0"   # May be /dev/ttyUSB1 on first enumeration
BAUD = 230400

CRC16_TABLE = [
  0x0000,0x1021,0x2042,0x3063,0x4084,0x50a5,0x60c6,0x70e7,
  0x8108,0x9129,0xa14a,0xb16b,0xc18c,0xd1ad,0xe1ce,0xf1ef,
  0x1231,0x0210,0x3273,0x2252,0x52b5,0x4294,0x72f7,0x62d6,
  0x9339,0x8318,0xb37b,0xa35a,0xd3bd,0xc39c,0xf3ff,0xe3de,
  0x2462,0x3443,0x0420,0x1401,0x64e6,0x74c7,0x44a4,0x5485,
  0xa56a,0xb54b,0x8528,0x9509,0xe5ee,0xf5cf,0xc5ac,0xd58d,
  0x3653,0x2672,0x1611,0x0630,0x76d7,0x66f6,0x5695,0x46b4,
  0xb75b,0xa77a,0x9719,0x8738,0xf7df,0xe7fe,0xd79d,0xc7bc,
  0x48c4,0x58e5,0x6886,0x78a7,0x0840,0x1861,0x2802,0x3823,
  0xc9cc,0xd9ed,0xe98e,0xf9af,0x8948,0x9969,0xa90a,0xb92b,
  0x5af5,0x4ad4,0x7ab7,0x6a96,0x1a71,0x0a50,0x3a33,0x2a12,
  0xdbfd,0xcbdc,0xfbbf,0xeb9e,0x9b79,0x8b58,0xbb3b,0xab1a,
  0x6ca6,0x7c87,0x4ce4,0x5cc5,0x2c22,0x3c03,0x0c60,0x1c41,
  0xedae,0xfd8f,0xcdec,0xddcd,0xad2a,0xbd0b,0x8d68,0x9d49,
  0x7e97,0x6eb6,0x5ed5,0x4ef4,0x3e13,0x2e32,0x1e51,0x0e70,
  0xff9f,0xefbe,0xdfdd,0xcffc,0xbf1b,0xaf3a,0x9f59,0x8f78,
  0x9188,0x81a9,0xb1ca,0xa1eb,0xd10c,0xc12d,0xf14e,0xe16f,
  0x1080,0x00a1,0x30c2,0x20e3,0x5004,0x4025,0x7046,0x6067,
  0x83b9,0x9398,0xa3fb,0xb3da,0xc33d,0xd31c,0xe37f,0xf35e,
  0x02b1,0x1290,0x22f3,0x32d2,0x4235,0x5214,0x6277,0x7256,
  0xb5ea,0xa5cb,0x95a8,0x8589,0xf56e,0xe54f,0xd52c,0xc50d,
  0x34e2,0x24c3,0x14a0,0x0481,0x7466,0x6447,0x5424,0x4405,
  0xa7db,0xb7fa,0x8799,0x97b8,0xe75f,0xf77e,0xc71d,0xd73c,
  0x26d3,0x36f2,0x0691,0x16b0,0x6657,0x7676,0x4615,0x5634,
  0xd94c,0xc96d,0xf90e,0xe92f,0x99c8,0x89e9,0xb98a,0xa9ab,
  0x5844,0x4865,0x7806,0x6827,0x18c0,0x08e1,0x3882,0x28a3,
  0xcb7d,0xdb5c,0xeb3f,0xfb1e,0x8bf9,0x9bd8,0xabbb,0xbb9a,
  0x4a75,0x5a54,0x6a37,0x7a16,0x0af1,0x1ad0,0x2ab3,0x3a92,
  0xfd2e,0xed0f,0xdd6c,0xcd4d,0xbdaa,0xad8b,0x9de8,0x8dc9,
  0x7c26,0x6c07,0x5c64,0x4c45,0x3ca2,0x2c83,0x1ce0,0x0cc1,
  0xef1f,0xff3e,0xcf5d,0xdf7c,0xaf9b,0xbfba,0x8fd9,0x9ff8,
  0x6e17,0x7e36,0x4e55,0x5e74,0x2e93,0x3eb2,0x0ed1,0x1ef0,
]

def crc16(data):
    crc = 0
    for b in data:
        crc = ((crc << 8) ^ CRC16_TABLE[((crc >> 8) ^ b) & 0xFF]) & 0xFFFF
    return crc

def jd(obj):
    return json.dumps(obj, separators=(", ", ": "))

ser = serial.Serial(PORT, BAUD, bytesize=8, parity='N', stopbits=1, timeout=2)
ser.reset_input_buffer(); time.sleep(0.3)

# Flush cold buffer
ser.write(b'GET apiVersion {}\r'); time.sleep(0.5); ser.read(4096)

# Build payload
utc_now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
payload = f"MeshSat laptop test {utc_now}".encode('utf-8')
crc = crc16(payload)
payload_with_crc = payload + struct.pack('>H', crc)
payload_b64 = base64.b64encode(payload_with_crc).decode('ascii')
total_len = len(payload_with_crc)
print(f"Payload: {payload.decode()} | CRC: 0x{crc:04X} | Len: {total_len}")

# Send MO
cmd = jd({"topic_id": 244, "message_length": total_len, "request_reference": 1})
ser.write((f"PUT messageOriginate {cmd}\r").encode('ascii'))
print(f"TX: PUT messageOriginate {cmd}")

# Poll and respond to segment request immediately
buf = b""
deadline = time.time() + 10
segment_sent = False
while time.time() < deadline:
    n = ser.in_waiting
    if n:
        buf += ser.read(n)
        while b'\r' in buf:
            line, buf = buf.split(b'\r', 1)
            line = line.strip()
            if not line: continue
            decoded = line.decode('ascii', errors='replace')
            print(f"RX: {decoded}")
            if '299 messageOriginateSegment' in decoded and not segment_sent:
                d = json.loads(decoded.split(' ', 2)[2])
                seg = jd({"topic_id": d["topic_id"], "message_id": d["message_id"],
                    "segment_length": d["segment_length"], "segment_start": d["segment_start"],
                    "data": payload_b64})
                ser.write((f"PUT messageOriginateSegment {seg}\r").encode('ascii'))
                print(f"TX: PUT messageOriginateSegment ...")
                segment_sent = True
    else:
        time.sleep(0.02)
ser.close()
```

#### Laptop handshake — individual commands

```python
# Step 1: Version negotiation
ser.write(b'PUT apiVersion {"active_version": {"major": 1, "minor": 7, "patch": 0}}\r')

# Step 2: SIM configuration
ser.write(b'PUT simConfig {"interface": "internal"}\r')

# Step 3: Activate modem
ser.write(b'PUT operationalState {"state": "active"}\r')

# Step 4: Get hardware info
ser.write(b'GET hwInfo {}\r')

# Step 5: Get signal
ser.write(b'GET constellationState {}\r')
```

### 5.2 Mule / Pi (aarch64) — rb_mule_test.py

Deployed via `scp` to `/tmp/rb_mule_test.py`. Run with:
```bash
# Prerequisite: stop HAL
sudo systemctl stop cubeos-watchdog.timer
sudo systemctl disable cubeos-watchdog.timer
sudo docker stop cubeos-hal

# Run individual steps
PYTHONUNBUFFERED=1 python3 -u /tmp/rb_mule_test.py handshake
PYTHONUNBUFFERED=1 python3 -u /tmp/rb_mule_test.py signal
PYTHONUNBUFFERED=1 python3 -u /tmp/rb_mule_test.py mo
PYTHONUNBUFFERED=1 python3 -u /tmp/rb_mule_test.py mt

# Or all at once
PYTHONUNBUFFERED=1 python3 -u /tmp/rb_mule_test.py all
```

```python
#!/usr/bin/env python3
"""RockBLOCK 9704 test for mule device. Run with: python3 rb_mule_test.py [step]
Steps: handshake, signal, mo, mt, all"""
import serial, time, json, base64, struct, sys
from datetime import datetime, timezone

PORT = "/dev/ttyUSB0"
BAUD = 230400

CRC16_TABLE = [
  0x0000,0x1021,0x2042,0x3063,0x4084,0x50a5,0x60c6,0x70e7,
  0x8108,0x9129,0xa14a,0xb16b,0xc18c,0xd1ad,0xe1ce,0xf1ef,
  0x1231,0x0210,0x3273,0x2252,0x52b5,0x4294,0x72f7,0x62d6,
  0x9339,0x8318,0xb37b,0xa35a,0xd3bd,0xc39c,0xf3ff,0xe3de,
  0x2462,0x3443,0x0420,0x1401,0x64e6,0x74c7,0x44a4,0x5485,
  0xa56a,0xb54b,0x8528,0x9509,0xe5ee,0xf5cf,0xc5ac,0xd58d,
  0x3653,0x2672,0x1611,0x0630,0x76d7,0x66f6,0x5695,0x46b4,
  0xb75b,0xa77a,0x9719,0x8738,0xf7df,0xe7fe,0xd79d,0xc7bc,
  0x48c4,0x58e5,0x6886,0x78a7,0x0840,0x1861,0x2802,0x3823,
  0xc9cc,0xd9ed,0xe98e,0xf9af,0x8948,0x9969,0xa90a,0xb92b,
  0x5af5,0x4ad4,0x7ab7,0x6a96,0x1a71,0x0a50,0x3a33,0x2a12,
  0xdbfd,0xcbdc,0xfbbf,0xeb9e,0x9b79,0x8b58,0xbb3b,0xab1a,
  0x6ca6,0x7c87,0x4ce4,0x5cc5,0x2c22,0x3c03,0x0c60,0x1c41,
  0xedae,0xfd8f,0xcdec,0xddcd,0xad2a,0xbd0b,0x8d68,0x9d49,
  0x7e97,0x6eb6,0x5ed5,0x4ef4,0x3e13,0x2e32,0x1e51,0x0e70,
  0xff9f,0xefbe,0xdfdd,0xcffc,0xbf1b,0xaf3a,0x9f59,0x8f78,
  0x9188,0x81a9,0xb1ca,0xa1eb,0xd10c,0xc12d,0xf14e,0xe16f,
  0x1080,0x00a1,0x30c2,0x20e3,0x5004,0x4025,0x7046,0x6067,
  0x83b9,0x9398,0xa3fb,0xb3da,0xc33d,0xd31c,0xe37f,0xf35e,
  0x02b1,0x1290,0x22f3,0x32d2,0x4235,0x5214,0x6277,0x7256,
  0xb5ea,0xa5cb,0x95a8,0x8589,0xf56e,0xe54f,0xd52c,0xc50d,
  0x34e2,0x24c3,0x14a0,0x0481,0x7466,0x6447,0x5424,0x4405,
  0xa7db,0xb7fa,0x8799,0x97b8,0xe75f,0xf77e,0xc71d,0xd73c,
  0x26d3,0x36f2,0x0691,0x16b0,0x6657,0x7676,0x4615,0x5634,
  0xd94c,0xc96d,0xf90e,0xe92f,0x99c8,0x89e9,0xb98a,0xa9ab,
  0x5844,0x4865,0x7806,0x6827,0x18c0,0x08e1,0x3882,0x28a3,
  0xcb7d,0xdb5c,0xeb3f,0xfb1e,0x8bf9,0x9bd8,0xabbb,0xbb9a,
  0x4a75,0x5a54,0x6a37,0x7a16,0x0af1,0x1ad0,0x2ab3,0x3a92,
  0xfd2e,0xed0f,0xdd6c,0xcd4d,0xbdaa,0xad8b,0x9de8,0x8dc9,
  0x7c26,0x6c07,0x5c64,0x4c45,0x3ca2,0x2c83,0x1ce0,0x0cc1,
  0xef1f,0xff3e,0xcf5d,0xdf7c,0xaf9b,0xbfba,0x8fd9,0x9ff8,
  0x6e17,0x7e36,0x4e55,0x5e74,0x2e93,0x3eb2,0x0ed1,0x1ef0,
]

def crc16(data):
    crc = 0
    for b in data:
        crc = ((crc << 8) ^ CRC16_TABLE[((crc >> 8) ^ b) & 0xFF]) & 0xFFFF
    return crc

def jd(obj):
    return json.dumps(obj, separators=(", ", ": "))

def p(msg):
    print(msg, flush=True)

def open_port():
    ser = serial.Serial(PORT, BAUD, bytesize=8, parity='N', stopbits=1, timeout=2)
    ser.reset_input_buffer()
    time.sleep(0.3)
    # Flush cold buffer (first command always returns 405)
    ser.write(b'GET apiVersion {}\r')
    time.sleep(0.5)
    ser.read(ser.in_waiting or 4096)
    return ser

def send(ser, cmd, timeout=2):
    ser.reset_input_buffer()
    ser.write((cmd + '\r').encode('ascii'))
    time.sleep(0.3)
    buf = b""
    deadline = time.time() + timeout
    while time.time() < deadline:
        n = ser.in_waiting
        if n:
            buf += ser.read(n)
        elif buf:
            break
        else:
            time.sleep(0.05)
    lines = []
    for line in buf.split(b'\r'):
        line = line.strip()
        if line:
            decoded = line.decode('ascii', errors='replace')
            p(f"  RX: {decoded}")
            lines.append(decoded)
    return lines

def do_handshake():
    p("=== HANDSHAKE ===")
    ser = open_port()
    p("GET apiVersion...")
    send(ser, 'GET apiVersion {}')
    p("PUT apiVersion v1.7.0...")
    send(ser, 'PUT apiVersion ' + jd({"active_version": {"major": 1, "minor": 7, "patch": 0}}))
    p("PUT simConfig internal...")
    send(ser, 'PUT simConfig ' + jd({"interface": "internal"}), timeout=3)
    p("PUT operationalState active...")
    send(ser, 'PUT operationalState ' + jd({"state": "active"}), timeout=5)
    p("GET hwInfo...")
    send(ser, 'GET hwInfo {}')
    ser.close()

def do_signal():
    p("=== SIGNAL ===")
    ser = open_port()
    send(ser, 'GET constellationState {}', timeout=3)
    ser.close()

def do_mo():
    p("=== MO SEND ===")
    ser = open_port()
    utc_now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    payload = f"MeshSat mule test {utc_now}".encode('utf-8')
    crc = crc16(payload)
    payload_with_crc = payload + struct.pack('>H', crc)
    payload_b64 = base64.b64encode(payload_with_crc).decode('ascii')
    total_len = len(payload_with_crc)
    p(f"Payload: {payload.decode()} | CRC: 0x{crc:04X} | Len: {total_len}")

    cmd = jd({"topic_id": 244, "message_length": total_len, "request_reference": 1})
    p(f"TX: PUT messageOriginate {cmd}")
    ser.write((f"PUT messageOriginate {cmd}\r").encode('ascii'))

    buf = b""
    deadline = time.time() + 10
    segment_sent = False
    while time.time() < deadline:
        n = ser.in_waiting
        if n:
            buf += ser.read(n)
            while b'\r' in buf:
                line, buf = buf.split(b'\r', 1)
                line = line.strip()
                if not line: continue
                decoded = line.decode('ascii', errors='replace')
                p(f"  RX: {decoded}")
                if '299 messageOriginateSegment' in decoded and not segment_sent:
                    d = json.loads(decoded.split(' ', 2)[2])
                    seg = jd({"topic_id": d["topic_id"], "message_id": d["message_id"],
                        "segment_length": d["segment_length"], "segment_start": d["segment_start"],
                        "data": payload_b64})
                    p(f"TX: PUT messageOriginateSegment ...")
                    ser.write((f"PUT messageOriginateSegment {seg}\r").encode('ascii'))
                    segment_sent = True
        else:
            time.sleep(0.02)
    ser.close()

def do_mt():
    p("=== MT RECEIVE (10s) ===")
    ser = open_port()
    buf = b""
    deadline = time.time() + 10
    while time.time() < deadline:
        n = ser.in_waiting
        if n:
            buf += ser.read(n)
            while b'\r' in buf:
                line, buf = buf.split(b'\r', 1)
                line = line.strip()
                if not line: continue
                decoded = line.decode('ascii', errors='replace')
                p(f"  RX: {decoded}")
                if 'messageTerminateSegment' in decoded:
                    d = json.loads(decoded.split(' ', 2)[2])
                    raw = base64.b64decode(d.get('data', ''))
                    if len(raw) >= 2:
                        p(f"  MT payload: {raw[:-2].decode('utf-8', errors='replace')}")
        else:
            time.sleep(0.02)
    if not buf.strip():
        p("  No MT messages")
    ser.close()

step = sys.argv[1] if len(sys.argv) > 1 else "all"
if step == "all":
    do_handshake()
    do_signal()
    do_mo()
    do_mt()
elif step == "handshake": do_handshake()
elif step == "signal": do_signal()
elif step == "mo": do_mo()
elif step == "mt": do_mt()
else: p(f"Unknown step: {step}")
```

### 5.3 MT polling script (used for extended receive on Pi)

```python
#!/usr/bin/env python3
"""Long MT poll — filters output to only show MT messages and non-zero signal."""
import serial, time, json, base64, sys
sys.stdout.reconfigure(line_buffering=True)

ser = serial.Serial("/dev/ttyUSB0", 230400, bytesize=8, parity="N", stopbits=1, timeout=2)
ser.reset_input_buffer(); time.sleep(0.3)
ser.write(b"GET apiVersion {}\r"); time.sleep(0.5); ser.read(4096)

print("Polling for MT messages... send now.", flush=True)
buf = b""
deadline = time.time() + 300  # 5 minutes
while time.time() < deadline:
    n = ser.in_waiting
    if n:
        buf += ser.read(n)
        while b"\r" in buf:
            line, buf = buf.split(b"\r", 1)
            line = line.strip()
            if not line: continue
            decoded = line.decode("ascii", errors="replace")
            if "messageTerminate" in decoded:
                print(decoded, flush=True)
                if "messageTerminateSegment" in decoded:
                    try:
                        d = json.loads(decoded.split(" ", 2)[2])
                        raw = base64.b64decode(d.get("data", ""))
                        if len(raw) >= 2:
                            print(f">>> MT payload: {raw[:-2].decode('utf-8', errors='replace')}",
                                  flush=True)
                    except: pass
            elif "signal_bars" in decoded:
                try:
                    d = json.loads(decoded.split(" ", 2)[2])
                    bars = d.get("signal_bars", 0)
                    if bars > 0:
                        print(f"signal: {bars} bars", flush=True)
                except: pass
    else:
        time.sleep(0.02)
print("Poll ended.", flush=True)
ser.close()
```

---

## 6. CubeOS HAL Management (Pi only)

### Disable HAL for testing
```bash
# Stop the watchdog that respawns HAL every ~60s
sudo systemctl stop cubeos-watchdog.timer
sudo systemctl disable cubeos-watchdog.timer

# Stop the HAL container
sudo docker stop cubeos-hal

# Verify
sudo docker ps | grep hal   # should return nothing
```

### Re-enable HAL after testing
```bash
sudo systemctl enable --now cubeos-watchdog.timer
# Watchdog will restart HAL within ~60 seconds
```

### What cubeos-hal does
- Docker container: `localhost:5000/cubeos-app/hal:latest`, command `./cubeos-hal`
- Manages serial port `/dev/ttyUSB0` at 19200 baud (conflicts with JSPR's 230400)
- Health endpoint: `hal.cubeos.cube:6005`
- Respawn chain: `cubeos-watchdog.timer` -> `cubeos-watchdog.service` -> `/cubeos/coreapps/scripts/watchdog-health.sh` -> `cubeos-normal-boot.sh` -> `docker compose up cubeos-hal`
- Docker compose file: `/cubeos/coreapps/cubeos-hal/appconfig/docker-compose.yml`

---

## 7. JSPR Command Reference (verified working)

### Handshake sequence (must be in order)
```
GET apiVersion {}
PUT apiVersion {"active_version": {"major": 1, "minor": 7, "patch": 0}}
PUT simConfig {"interface": "internal"}
PUT operationalState {"state": "active"}
```

### Information queries
```
GET hwInfo {}
GET constellationState {}
GET simConfig {}
GET operationalState {}
GET simStatus {}
GET messageProvisioning {}
GET firmware {"slot": "<slot>"}
```

### MO send flow
```
1. PUT messageOriginate {"topic_id": 244, "message_length": LEN, "request_reference": 1}
   -> 200 messageOriginate {"message_id": ID, "message_response": "message_accepted"}
   -> 299 messageOriginateSegment {"topic_id": T, "message_id": ID,
          "segment_length": L, "segment_start": S}

2. PUT messageOriginateSegment {"topic_id": T, "message_id": ID,
       "segment_length": L, "segment_start": S, "data": "BASE64"}
   -> 200 messageOriginateSegment {"topic_id": T, "message_id": ID}

3. (poll) -> 299 messageOriginateStatus {"topic_id": T, "message_id": ID,
                  "final_mo_status": "mo_ack_received"}
```

### MT receive flow
```
(poll) -> 299 messageTerminate {"topic_id": T, "message_id": ID, "message_length_max": L}
       -> 299 messageTerminateSegment {"topic_id": T, "message_id": ID,
              "segment_length": L, "segment_start": S, "data": "BASE64"}
       -> 299 messageTerminateStatus {"topic_id": T, "message_id": ID,
              "final_mt_status": "complete"}
```

### CRC-16/CCITT calculation
```python
CRC16_TABLE = [0x0000, 0x1021, 0x2042, 0x3063, ...]  # 256 entries, standard CCITT

def crc16(data, init=0):
    crc = init
    for b in data:
        crc = ((crc << 8) ^ CRC16_TABLE[((crc >> 8) ^ b) & 0xFF]) & 0xFFFF
    return crc

# Append as 2-byte big-endian after payload
payload_with_crc = payload + struct.pack('>H', crc16(payload))
```

---

## 8. Dependencies

- **Python 3.x** (tested with system Python on both platforms)
- **pyserial 3.5** (`pip3 install pyserial`)
- No other dependencies. Do NOT use the `rockblock9704` pip package (C extension issues on ARM64).

---

## 9. Summary

| Test | Laptop (x86_64) | Mule Pi (aarch64) |
|------|-----------------|-------------------|
| Serial port | /dev/ttyUSB0 | /dev/ttyUSB0 |
| FTDI VID:PID | 0403:6015 | 0403:6015 |
| apiVersion | 200 OK, v1.7.0 | 200 OK, v1.7.0 |
| simConfig | internal | internal |
| operationalState | active | active |
| IMEI | 30025806XXXXXXX | 30025806XXXXXXX |
| Board temp | 19-20°C | 17-18°C |
| Signal bars | 0-5 | 0-5 |
| MO sent | msg_id 5, mo_ack_received | msg_id 1, mo_ack_received |
| MT received | 4 messages (tst) | 11 messages (tst, aaa, bbb, ccc, etc.) |
| Blockers | None | cubeos-hal + watchdog |

**All tests passed on both platforms.**

---

## 8. Bridge Pipeline Verification (2026-03-25)

After fixing three root cause bugs, MO+MT was verified working through the full MeshSat bridge pipeline on mule01:

### Bugs Fixed
1. **Split-line bug in jspr_helper.py** (c49c7be) — `_rx_buf` was local to `jspr_receive()`, discarded on timeout. At 230400 baud on ARM64, JSPR lines can arrive in chunks that split across reads, silently dropping messageTerminate messages. Fix: persist `_rx_buf` as instance variable across calls.
2. **Missing mt_received event handler** (c49c7be) — `IridiumGateway.ringAlertListener()` had no case for `mt_received` events from the IMT transport. MT messages were buffered in `mtPending` but the gateway never triggered `handleRingAlert()`.
3. **Modem hung state** (4e848ab) — RockBLOCK 9704 firmware stops responding to all JSPR commands (except cold-start GET apiVersion) after repeated failed serial sessions. USB driver unbind/bind forces full re-initialization. Auto-triggered after 3 handshake failures.

### Bridge MO Result
```
delivery_id: 514, gateway: iridium_imt
jspr: MO via helper complete final_status=mo_ack_received msg_id=18
dispatcher: delivery sent + acked channel=iridium_imt_0 id=514 qos=1
```

### Bridge MT Results (3 messages from Cloudloop)
| Msg ID | Payload | Size | When |
|--------|---------|------|------|
| 60 | (during MO satellite session) | 17 bytes | During MO |
| 61 | "another new message" | 19 bytes | 13s after MO |
| 62 | "last new message" | 16 bytes | 36s after MO |

All MT messages flowed through the complete pipeline: helper → readerLoop → pollLoop → processMTAnnouncements → jsprReceiveMT → mt_received event → handleRingAlert → Receive.

### Environment
- Same as Section 1.2 (mule01, Pi 5)
- Running inside Docker container (privileged, /dev mounted)
- Docker confirmed NOT a factor — identical results on bare metal and in container

### Key Finding: MT Delivery Requires Active Satellite Session
MT messages are delivered to the modem during/after MO satellite passes. The modem connects to an Iridium Certus satellite during MO, and pending MT messages are pushed at that time. Without MO activity, MTs queue on the Iridium network. MT can also arrive unsolicited during handshake if the modem has a recent active session.
