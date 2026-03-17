# End-to-End Test Scenarios

_Created: 2026-03-17_

## Scenario 1: Meshtastic → Bridge → TAK → ATAK Map

**Prerequisites:**
- Bridge running with Meshtastic radio connected
- Bridge TAK gateway configured and connected to OpenTAKServer
- ATAK client connected to same OpenTAKServer

**Message flow:**
1. Meshtastic node sends position (POSITION_APP, portnum 3)
2. Bridge DirectMeshTransport receives via USB serial
3. Processor pipeline: dedup → access rules → Dispatcher
4. Dispatcher matches rule: mesh → tak
5. TAKGateway.Forward() builds CoT PLI (a-f-G-U-C) with lat/lon
6. CoT XML written to TAK server TCP stream
7. OpenTAKServer distributes to connected ATAK clients
8. ATAK shows marker on map with callsign `MESHSAT-XXXX`

**Verification:**
- Check Bridge dashboard Messages tab for incoming mesh message
- Check Bridge dashboard Interfaces tab for TAK gateway connected + msgsOut incrementing
- Check ATAK map for new marker at expected coordinates
- Verify callsign matches `{callsign_prefix}-{node_id_last4}`

**Known limitations:**
- Meshtastic position update rate (default 900s) limits map freshness
- CoT stale time should be 2-3x the Meshtastic position interval

---

## Scenario 2: Iridium SBD MO → Hub → TAK → ATAK Map

**Prerequisites:**
- Field device with RockBLOCK 9603N sending MO messages
- Hub running with RockBLOCK webhook configured
- Hub TAK gateway enabled, connected to OpenTAKServer
- ATAK client connected to OpenTAKServer

**Message flow:**
1. Field device sends MO via Iridium 9603N
2. Iridium constellation → Ground Control gateway
3. Ground Control POSTs to Hub `/api/webhook/rockblock`
4. Hub verifies webhook, decodes SMAZ2, publishes to `meshsat/{imei}/mo/decoded`
5. Hub TAK subscriber receives MQTT message
6. Hub converts to CoT XML (a-f-G-U-C with Iridium CEP coordinates)
7. Hub sends CoT to OpenTAKServer on internal Docker port 8087
8. ATAK shows marker with `MESHSAT-HUB-{imei_last4}` callsign

**Verification:**
- Hub logs show webhook received and MQTT published
- MQTT client subscribed to `meshsat/+/mo/decoded` sees the message
- ATAK map shows new marker at Iridium geolocation coordinates
- Note: Iridium CEP (circular error probable) is ~10km — marker is approximate

**Known limitations:**
- Iridium geolocation accuracy is ~10km (beam-level, not GPS)
- If field device includes GPS in payload, Hub should use that instead of Iridium CEP
- MO delivery can take 1-30 seconds depending on satellite visibility

---

## Scenario 3: ATAK → TAK → Hub → MQTT → Bridge → Iridium MT → Field Device

**Prerequisites:**
- ATAK client connected to OpenTAKServer
- Hub TAK gateway subscribed to OpenTAKServer (bidirectional)
- Hub MT sender configured with Cloudloop API key
- Bridge running with Iridium 9603N connected (or field device directly)

**Message flow:**
1. ATAK operator sends GeoChat message (CoT type b-t-f)
2. OpenTAKServer distributes to all connected clients including Hub
3. Hub TAK gateway receives CoT XML, parses into internal message
4. Hub publishes to `meshsat/{device_id}/mt/send`
5. Hub MT sender subscribes, POSTs to Cloudloop API
6. Cloudloop queues for next Iridium pass
7. Iridium delivers MT to field device's 9603N buffer
8. Field device receives MT (ring alert or next mailbox check)

**Verification:**
- ATAK shows sent message in chat
- Hub logs show CoT received and MT queued
- Cloudloop API returns success status
- `meshsat/{device_id}/mt/status` shows delivery progression
- Field device receives message (verified via Bridge dashboard or serial monitor)

**Known limitations:**
- MT delivery depends on satellite pass schedule (can take minutes to hours)
- MT buffer is 270 bytes — long ATAK messages may be truncated
- SMAZ2 compression helps but doesn't eliminate the limit

---

## Scenario 4: APRS → Bridge → MQTT → Hub → TAK → ATAK Map

**Prerequisites:**
- Bridge running with Direwolf + AIOC connected
- Bridge APRS gateway configured and connected to Direwolf KISS TCP
- Bridge MQTT gateway publishing to Hub
- Hub TAK gateway enabled

**Message flow:**
1. APRS station transmits position on 144.800 MHz
2. Direwolf decodes 1200 baud AFSK → AX.25 frame → KISS TCP
3. Bridge APRSGateway reads KISS frame, decodes AX.25 + APRS position
4. Processor pipeline routes to MQTT gateway
5. Bridge publishes to Hub MQTT: `meshsat/{bridge_id}/mo/decoded`
6. Hub TAK subscriber converts to CoT PLI
7. ATAK shows APRS station on map

**Verification:**
- Direwolf log shows decoded packet
- Bridge dashboard shows APRS gateway msgsIn incrementing
- MQTT broker shows message on `meshsat/+/mo/decoded`
- ATAK map shows APRS station position

**Known limitations:**
- APRS position accuracy depends on the transmitting station's GPS
- Only uncompressed APRS positions are currently decoded
- Compressed APRS (base91) and Mic-E format not yet implemented

---

## Scenario 5: SOS → Iridium MO → Hub → TAK Emergency + SMS + Notification

**Prerequisites:**
- Field device with SOS button wired to MeshSat
- Hub with TAK, SMS gateway, and Apprise notification configured
- ATAK client connected

**Message flow:**
1. Field operator presses SOS button on field device
2. MeshSat Bridge publishes SOS to `meshsat/{device_id}/sos` via Iridium
3. Hub receives SOS via RockBLOCK webhook
4. Hub publishes to `meshsat/{imei}/sos`
5. Hub TAK gateway generates CoT with `<emergency type="911 Alert">`
6. ATAK shows emergency marker with alarm
7. Hub notification service (Apprise) sends SMS, email, push notification
8. Hub dashboard shows SOS alert

**Verification:**
- ATAK shows emergency marker (red, with alarm indicator)
- SMS/email received by configured emergency contacts
- Hub dashboard SOS panel shows active alert
- `meshsat/{imei}/sos` MQTT message contains lat/lon/timestamp

**Known limitations:**
- Iridium delivery latency (seconds to minutes)
- SOS depends on satellite visibility — dead zone = delayed alert
- ECP on field device continues attempting local alerting independently

---

## Scenario 6: Android (Offline, No Hub) → AIOC APRS → Local RF → Bridge

**Prerequisites:**
- Android phone with AIOC connected via USB-OTG
- APRSDroid running with KISS TCP on port 8001
- MeshSat Android with APRS interface configured
- Bridge running with Direwolf + AIOC on same APRS frequency (144.800 MHz)
- No internet, no Hub

**Message flow:**
1. MeshSat Android sends position via APRSDroid KISS TCP
2. APRSDroid modulates AFSK, AIOC transmits on 144.800 MHz
3. Bridge's Direwolf receives RF, decodes to KISS frame
4. Bridge APRSGateway decodes APRS position
5. Bridge routes to all other channels (Meshtastic, Iridium if available)

**Verification:**
- APRSDroid shows packet transmitted
- Direwolf log shows received packet with Android's callsign
- Bridge dashboard shows APRS inbound message
- Bridge routes message to Meshtastic mesh (visible on mesh nodes)

**Known limitations:**
- RF range depends on radio power and terrain (1-30km typical with handheld)
- AIOC + APRSDroid on Android requires USB-OTG support
- **PLANNED**: Android APRS adapter not yet implemented — this scenario requires Phase 2 of ANDROID_APRS_DECISION.md

---

## Scenario 7: Android Reconnects to Hub → Message Sync → TAK Updated

**Prerequisites:**
- Android previously online, sent positions via MQTT to Hub
- Hub was receiving and forwarding to TAK
- Android lost internet (went offline)
- Android regains internet

**Message flow:**
1. Android regains connectivity
2. Android MQTT client auto-reconnects to Hub broker (exponential backoff)
3. Android publishes current position to `meshsat/{device_id}/position`
4. Hub TAK gateway receives position, sends CoT PLI update to OpenTAKServer
5. ATAK map shows updated position for the Android device

**Verification:**
- Android log shows MQTT reconnected
- Hub MQTT broker log shows client reconnection
- ATAK map marker jumps to Android's current position
- Timestamp on ATAK marker reflects reconnection time

**Known limitations:**
- Messages sent during offline period are NOT retroactively synced to Hub (PLANNED: delivery ledger sync)
- MQTT retained messages ensure Hub gets latest state, but history is lost
- If Android sent messages via local channels (SMS, BLE mesh) during offline, those are tracked in Android's local delivery ledger but not replicated to Hub
- **PLANNED**: Full delivery ledger sync between Hub and Android/Bridge is a future feature
