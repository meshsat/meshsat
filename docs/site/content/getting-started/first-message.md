---
title: "First Message"
weight: 3
---

# Send Your First Message

This guide walks you through sending a message from the Meshtastic mesh network through the Iridium satellite link.

## Prerequisites

- MeshSat running with Meshtastic and Iridium hardware connected
- A Meshtastic node (phone app or standalone device) joined to the same mesh
- RockBLOCK account activated with message credits

## Steps

1. **Open the dashboard** at `http://<your-pi>:6050`
2. **Check interfaces** — navigate to the Interfaces page and verify both `meshtastic` and `iridium` show as online (green badge)
3. **Create a routing rule** — go to Bridge > Rules and create:
   - Source: `meshtastic`
   - Destination: `iridium`
   - Action: Allow
4. **Send a message** from your Meshtastic app on the default channel
5. **Watch the dashboard** — the message appears in the Messages tab, and a delivery record shows the Iridium send attempt
6. **Check your RockBLOCK account** — the MO message appears in your Ground Control portal

## What Happens Under the Hood

1. Meshtastic radio receives the packet via serial
2. MeshSat's `DirectMeshTransport` decodes the protobuf
3. The message enters the `Processor` pipeline
4. The `Dispatcher` evaluates access rules — finds a rule allowing mesh→iridium
5. The `TransformPipeline` applies any configured compression (SMAZ2 by default)
6. The `IridiumGateway` queues the message, waits for a satellite pass if using pass scheduling
7. The 9603N modem sends via AT+SBDIX
8. If `mo_status=0-4` (success), the delivery is marked complete
9. If `mo_status=32/36`, the DLQ retries with 3-minute backoff

## Troubleshooting

- **Iridium shows "offline"**: Check serial connection, verify with `AT` command
- **Messages stuck in DLQ**: The modem may not have satellite visibility — check the Passes page for next pass time
- **"Access denied"**: No matching rule exists — create one in Bridge > Rules
