# Design — Multi-Bridge + NAT Traversal + Hub Relay (Phase 9)

## Goal

Per `EXECUTION-PLAN.md §0` decision #3 (2026-04-18), the Hub WS relay ships in v1. This phase closes the 3-tier NAT-traversal story so a CG-NAT-LTE-bound field kit remains reachable from any internet-connected Android, plus introduces the Android multi-bridge UI for operators with several kits.

## Wire diagram

```
                    Android (paired with bridge B)
                              │
            ┌─────────────────┼─────────────────┐
            │ (tier 1)        │ (tier 2)         │ (tier 3)
            ▼                 ▼                  ▼
       LAN HTTP          Reticulum link     Hub WS relay
       (preferred)       via bridge.rns_dest  (last resort)
            │                 │                  │
            ▼                 ▼                  ▼
       wss://lan-ip:6050  RNS dest hash       meshsat/relay/
       (mTLS direct)      (LoRa/Iridium      {bridge_id}/{client_id}
                          /MQTT/TCP)         (end-to-end mTLS;
                                              Hub sees ciphertext only)
                                                   │
                                                   ▼
                                            bridge subscribes
                                            to its own relay
                                            topic + forwards
                                            inbound to localhost
                                            HTTP server (REQ-813)
```

## Tier selection

```
function pickTransport(bridge):
  if canReachLAN(bridge.lan_ip): return TransportLAN  ← REQ-800 green
  if bridge.rns_dest && rnsLink(bridge.rns_dest):
      return TransportReticulum                       ← REQ-800 amber
  if bridge.hub_relay_url && hubRelay(bridge.bridge_id):
      return TransportHubRelay                        ← REQ-800 amber
  return TransportUnreachable                          ← REQ-800 red + REQ-815 banner
```

## Hub relay (REQ-809..813, REQ-818..819)

Hub-side adds a small WebSocket gateway that listens on `meshsat/relay/{bridge_id}/{client_id}` and bidirectionally tunnels opaque bytes. Critical properties:

- **End-to-end mTLS** (REQ-810): the Android holds the same mTLS cert it would use on LAN; the bridge's NATS terminates the mTLS. The Hub relay only sees encrypted frames. This is the same "edge SNI passthrough" design from `meshsat-hub/adr/0008-hub-as-ca-haproxy-sni-passthrough.md`.
- **Per-client isolation** (REQ-811): client_id is part of the topic + checked at frame ingress; cross-client tunneling is rejected.
- **Rate limit** (REQ-812): 100 req/min/client default; tenant-overridable.
- **Metrics** (REQ-818/819): counter for active tunnels + bytes-transported per bridge.

## Foreground service (REQ-805)

Android holds an SSE connection per paired bridge. The foreground service is a single notification ("MeshSat listening to N bridges") preventing Android from killing the process. SSE auto-reconnects on transport failover (LAN → RNS → Hub) using the same fallback chain.

## Diagnostic ring buffer (REQ-816)

100-event circular buffer logging every transport transition. Operator can dump from Settings → Diagnostics. Helps debug "why did messages take 30 minutes to arrive?" → likely a slow LAN → Hub-relay fallback cascade.

## Out of scope

- Bridge-to-bridge mesh peering (one Android client talks to one bridge at a time today; future spec).
- Hub-relay traffic shaping per-bridge (we rely on the per-client rate-limit + tenant-tier global cap, not per-bridge fairness).
- Tier-4 sneakernet / store-and-forward via paired Android — operators carry their own message journal already.
