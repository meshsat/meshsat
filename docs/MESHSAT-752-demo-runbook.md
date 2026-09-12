# MESHSAT-752 demo runbook (LOCAL ONLY, never commit)

Friday 2026-09-04 16:00 CEST, Meshtastic call. Cross-kit mesh to SMS to mesh relay, both directions.
Source-verified and live-verified 2026-09-01 evening. Session was read-only (shadow mode), so
nothing below has been applied yet.

## 1. State verified 2026-09-01

| Check | tesseract | parallax | Verdict |
|---|---|---|---|
| Bridge container | `ghcr.io/cubeos-app/meshsat:latest`, up 2h, healthy | same | Identical, freeze is safe |
| XIAO mesh node | ttyACM1, node 2715247548 "Meshtastic 63bc" | ttyACM4, node 1808552882 "Meshtastic 9d70" | Both enumerated and connected, EU_868 |
| `mesh_0` | online, transforms `[]` both ways | online, transforms `[]` both ways | Plaintext on the mesh leg, correct for the demo |
| `cellular_0` transforms | smaz2, encrypt(sms:shared), base64 | identical | Symmetric, decrypts itself |
| `cellular_0` gateway | connected, dest `<parallax SIM>` | connected, dest `<tesseract SIM>` | Cross-peer wiring correct |
| `max_sms_segments` | 1 | 1 | 160 char hard ceiling, see section 3 |
| `allowed_senders` | unset | unset | Must be set, see section 4 step 3 |
| `/api/access-rules` | `null` | `null` | Nothing forwards yet, as expected |
| `brcmfmac roamoff` | 1 | 1 | WiFi fix took on this boot |

The earlier "roamoff read back empty" was a permissions artifact. The file is root-readable only;
`sudo cat` returns 1 on both kits.

Drift worth one check on Wednesday: parallax `mesh_0` records `device_port: /dev/ttyACM1` but the
XIAO is currently at ttyACM4 (it re-enumerated at 19:23 local). `/api/radio/setup` reports the
radio connected with the right node number, so this is probably a stale bind record rather than a
broken radio. Send one local mesh message on parallax before trusting it.

## 2. How the relay actually works

The relay mechanism is the access-rules engine. With no rules there is implicit deny, which is why
nothing forwards today. Four rules, mirrored on both kits, `POST /api/access-rules`:

```json
{"name":"demo mesh to sms","interface_id":"mesh_0","direction":"ingress","action":"forward",
 "forward_to":"cellular_0","enabled":true,"priority":10,
 "rate_limit_per_min":6,"rate_limit_window":60}
```
```json
{"name":"demo sms to mesh","interface_id":"cellular_0","direction":"ingress","action":"forward",
 "forward_to":"mesh_0","enabled":true,"priority":10,
 "rate_limit_per_min":6,"rate_limit_window":60}
```

Field names verified against `database/interfaces.go:34`.

Path out (mesh to SMS), `engine/dispatcher.go`:
inbound mesh text, `DispatchAccess("mesh_0", ...)`, delivery row, `DeliveryWorker`, egress
transforms at :1229 encrypt ONLY `del.TextPreview` (not a JSON envelope, the code special-cases
cellular), version byte 0x01 prepended at :1275, `CellularGateway.Forward`, `sendSMSSync`
(`gateway/cellular.go:201`) sees `msg.Encrypted` and sends the raw base64 with no prefix.

Path back (SMS to mesh):
inbound SMS, `StartGatewayReceiver` (`engine/processor.go:995`) passes the raw SMS bytes,
`DispatchAccess("cellular_0", ...)` strips the version byte at :491 then `ApplyIngress` at :500
reverses the transform order (`engine/transform.go:111`), so base64 decode, decrypt, decompress.
Delivery to `mesh_0` is special-cased at `dispatcher.go:1322` to `mesh.SendMessage(Text:
del.TextPreview)`, a normal Meshtastic text message. Because `mesh_0` has no transforms, the
T-Echo sees readable plaintext.

Loop prevention, three layers, all already in place: gateway-injection dedup on a 5 minute window
keyed on sha256 of the decoded text, the evaluator self-loop skip plus visited set, and
`MESHSAT_MAX_HOPS` (default 8) at `DispatchAccess` entry.

## 3. The three numbers that decide the demo

**Keep every demo message under 80 characters.** This is the one that bites silently.
`cellular.go:224` truncates with `text = text[:maxLen]` where `maxLen = 160 * max_sms_segments =
160`. No error, no log. A truncated base64 blob fails to decrypt on the far side and the message
simply vanishes with the delivery marked sent. The budget:

    SMS length = 1 (version byte) + 4*ceil((smaz2(text) + 28) / 3)

28 is the AES-GCM overhead, 12 byte nonce plus 16 byte tag, stated in-tree at
`transform.go:383`. Solving for 160 gives smaz2 output <= 89 bytes. Typical English compresses
enough that ~160 characters would fit, but assume zero compression as the worst case and cap at 80.

**Every demo message must be unique text.** The 5 minute injection dedup keys on the exact decoded
text. A repeat, or a reply that happens to match a recently relayed message, is silently swallowed.
Number the messages.

**There is no 60 second paid-rate-limit stall.** `MESHSAT_PAID_RATE_LIMIT=60` is set on both kits
but `SetPaidRateLimit` only feeds the API server's cost analysis (`api/router.go:170`). It does not
gate sends. The only enforced limiter is per-rule `rate_limit_per_min`, which is also the only SMS
cost guard, so it must be set on the rules rather than tuned in the environment.

## 4. Wednesday 2026-09-02 sequence

1. Baseline. Capture `/api/gateways`, `/api/interfaces`, `/api/access-rules` on both kits to a
   scratch dir before touching anything.
2. Split the mesh. Two channels with distinct PSKs, segment A = T-Keyboard plus tesseract XIAO,
   segment B = T-Echo plus parallax XIAO. Use the `meshtastic` CLI directly, not
   `POST /api/channels`: the bridge endpoint silently no-ops on parallax (the handshake times out
   against its ~41 node NodeDB, leaving myNodeNum 0, and the API returns success anyway). Stop the
   bridge first for port access, leave 2 s between CLI calls, do not loop more than ~3 calls.
   Restore baseline is in memory `project_meshtastic_channel_meshsat.md` (name MeshSat, PSK
   `fHuZ2UHjcSfwW0NKf3TTvrSiVFm59jl6W0EzctqPVkM=`, plus the share URL).
3. Set `allowed_senders` on each `cellular_0` to exactly the peer kit's number. With the sms to
   mesh rule live and this unset, any inbound SMS including KPN promo spam gets injected into the
   mesh mid-call.
4. Create the four rules from section 2.
5. Forward leg once. Watch `message_deliveries` on both kits. Confirm the T-Echo shows readable
   plaintext, not base64. If it shows base64, the version byte did not survive GSM-7 (section 6).
6. Reply leg once. Then three consecutive round trips with unique numbered texts, timing each.
   Pace the call script on the measured latency, not on an assumption.
7. Check `message_deliveries` for ping-pong before leaving the rules enabled unattended.
8. Decide the Iridium finale from 3 of 3 clean IMT rounds on parallax from the exact bench position.

## 5. Rollback after Friday

Delete the four rules, clear `allowed_senders`, restore `max_sms_segments` if it was changed,
restore the shared MeshSat channel on all four nodes from the memory file above. The environment
`MESHSAT_PAID_RATE_LIMIT` needs no change because it was never enforcing anything.

## 6. Open risk: the 0x01 version byte through GSM-7

Unproven, and the most likely cause of a failed forward leg.

Outbound SMS text is `0x01` followed by base64. It is written raw into an `AT+CMGS` text-mode body
(`transport/direct_cell.go:1067`, `CMGF=1`). No `AT+CSCS` is set anywhere in the tree, so the
modem's default TE charset decides how 0x01 is mapped. In the GSM charset 0x01 is the code for the
pound sign, so a symmetric A7670E pair may well round-trip it verbatim, but that has never been
exercised kit to kit. On ingress `StripVersionByte` (`codec/version.go:30`) only strips a literal
0x01; anything else is treated as legacy and left in place, base64 decode then fails, and
`dispatcher.go:502` logs "ingress transform failed, forwarding raw payload" and forwards the
ciphertext to the mesh.

This fails visibly, not silently: the T-Echo shows a base64 blob. If that happens on Wednesday, the
fallback is to clear the `cellular_0` transforms on both kits for the demo, which switches
`sendSMSSync` to the plain branch (`cellular.go:210`) and sends readable `MeshSat <sender>: <text>`
through `SanitizeSMSText`. That path is GSM-safe by construction and is arguably a better demo
because the SMS is legible on any phone, at the cost of dropping the encrypted-hop story. Restore
the transforms afterwards.

## 7. State after the 3 Sep evening tests (22:00 to 22:20 CEST)

Applied: `allowed_senders` = peer number on both cellular gateways; rules id 1 (mesh_0 to
cellular_0, `filters {"portnums":"[1]"}`, 6/min) and id 2 (cellular_0 to mesh_0, 6/min) on
both kits. Baseline gateway JSON in the session scratchpad `baseline/`.

**Add to section 3: the portnum filter is mandatory.** `handleMessage` passes every
non-routing packet to `DispatchAccess`, telemetry included, and each kit's own XIAO emits a
TELEMETRY_APP packet every minute. Without `{"portnums":"[1]"}` on the mesh-ingress rule
that is 60 SMS per hour per kit.

| Check | Result |
|---|---|
| SMS T to P (OOB PING, encrypted + clear) | PASS, 13 to 28 s round trip |
| SMS P to T (first success since 24 Apr, after the EUR 10 top-up) | PASS, 41 to 54 s round trip |
| Tesseract inbound SMS | proven independently: KPN confirmation SMS received 21:58 CEST |
| APRS rx on either kit | 0 while both key the AIOC ~3/min; software path verified up to the AIOC capture, radios unverified |
| LoRa hop between the XIAOs | DOWN: API-originated texts crossed in neither direction; tesseract watchdog saw 619 s of silence; parallax handshake times out |

Morning order: (1) parallax XIAO power cycle, confirm `/api/radio/setup` shows a node number
and `/api/nodes` lists both kits; (2) UV-K5 checklist on both kits (ON, 144.800 FM, squelch
0 or 1, volume ~60 %, AIOC K1 seated) then `POST /api/oob/send {"peer_id":9631,"via":"aprs_0",
"cmd":"PING","encrypt":false}` from each kit and watch `heard_count` on the other; (3) mesh
split (section 4 step 2); (4) T-Keyboard to T-Echo round trips with unique texts, three in a
row, timed; (5) screen recording as fallback.

**Addendum 22:30 CEST, after the owner-instructed warm reboot of both Pis:** both kits back in under
60 s with rules and gateway config intact; both handshakes completed at first, then parallax's XIAO
re-enumerated 2.5 min after boot and went silent again (handshake timeout). Tesseract's XIAO is
clean. API texts still crossed in neither direction. So the morning starts with the X1202 power
button on parallax, then `/api/radio/setup` must show a node number and `/api/nodes` must list
tesseract before anything else is attempted.

## 8. Why the mesh leg lies, and what to trust instead

Added 2026-09-03 22:45 CEST by a second (shadow-mode, read-only) session. Static analysis only,
nothing applied. This does not change the morning order in section 7; it explains why step 1 is
the right gate and adds two verification rules plus one open decision.

**8.1 A timed-out mesh handshake is not an error, and the transport still reports connected.**
`connectLocked` (`transport/direct_mesh.go:262`) treats the `want_config_id` deadline as success:
it logs "config handshake timed out, continuing with partial NodeDB", sets `configDone = true`
and returns `nil`. `t.connected` was already set true at :240, before the wait. `GetStatus`
(:948) then reports `Connected: t.connected`, so `mesh_0` shows online against a radio that has
sent literally nothing. This is exactly parallax's state and it is why the interface list looked
healthy on 1 Sep while nothing crossed.

The discriminator is `myNodeNum`. It is only ever set from a `MyInfo` frame (:406), which the
radio emits at the very start of the config stream. If it is still 0, the radio has not spoken
at all. `GetStatus` leaves `NodeID` empty when it is 0 (:962), and `/api/radio/setup` derives
`node_num` from that `NodeID` (`api/radio_setup.go:106`).

    mesh_0 is alive  iff  /api/radio/setup returns a non-zero node_num.
    Connected: true  proves nothing.

Section 7 step 1 already says this. The reason it must not be softened: a "connected" reading is
not weak evidence here, it is no evidence.

**8.2 Mesh sends are fire and forget. A delivery marked sent proves a USB write, not a
transmission.** `DeliveryWorker` calls `mesh.SendMessage` at `engine/dispatcher.go:1359` and
branches straight to `handleSuccess` on a nil error (:1370). `SendMessage`
(`transport/direct_mesh.go:875`) checks only `connected && file != nil`, then hands the frame to
`sendFrame`. There is no ACK, no radio confirmation, no retry. A write to an open CDC endpoint on
a wedged XIAO succeeds.

Consequence for Friday, and this is the one that could go wrong on camera: the acceptance
criteria have both operator dashboards screen-shared side by side. With a wedged radio the
dashboards will show a complete green path, mesh_0 online, delivery sent, while the T-Echo shows
nothing. **The far-end device screen is the only proof. Frame the T-Keyboard and the T-Echo on
camera and read the result off the devices, never off the dashboard.** If the demo is narrated
from the dashboard and the radio is dead, the failure is invisible until someone asks to see the
T-Echo.

**8.3 The portnum filter is correct as written, but it fails open.** `{"portnums":"[1]"}` is the
right shape: `Filters.Portnums` is a string holding a JSON array (`rules/access.go:31`) and is
unmarshalled at :288. However the guard is `if err == nil && len(portnums) > 0` (:288), so a
malformed filter string skips the filter entirely and every packet passes. There is no log on
that path.

So do not assume the filter took. After enabling the rules, leave the mesh idle for three minutes
and confirm **zero** new rows in `message_deliveries`. Each kit's own XIAO emits TELEMETRY_APP
about once a minute, so a filter that failed open shows up as roughly three delivery rows in that
window, and on KPN prepaid it would be burning credit into the call.

**8.4 Open decision: the 10 minute serial watchdog during the call.** `MESHSAT_MESH_WATCHDOG_MIN`
defaults to 10. On expiry `watchdogTriggered` (:350) closes the serial port and forces a full
reconnect, which means a CDC close and reopen on an ESP32-S3, the operation the XIAO wedge is
associated with. It already fired on tesseract last night at 619 s of silence.

Two things make it less likely to fire during the demo than it first looks. It only fires when
the NodeDB holds at least one remote node (:359), and any packet whose `From` is not us refreshes
the timer (:568), including cross-segment ciphertext the radio cannot decrypt, because
`handlePacket` runs for undecryptable packets too (:556). After the split both segments still
share the EU_868 preset, so traffic on either segment keeps both kits' timers alive.

The exposure is the quiet stretch: bridge restarted around 15:30, call opens 16:00 with roughly
twenty minutes of talking before anything is typed. That is a plausible 10 minute LoRa silence on
both segments at once, and the recycle would land mid-call.

Note the 2.5 minute post-boot re-enumeration on parallax is **not** this. No watchdog can fire
that early. That one is hardware, firmware or USB power, consistent with the wedge memory, and
the X1202 power button remains the right first move.

Trade-off, not a recommendation: raising `MESHSAT_MESH_WATCHDOG_MIN` to 60 for the demo window
puts the first possible fire after the call and keeps the watchdog for the rest of the week. The
cost is that it needs an env change and a container restart to apply, and last night's restart is
what preceded parallax's XIAO going silent, so the restart is not free. Leaving it at 10 keeps
the only automatic recovery for a stale CDC session. Owner call, see the poll in the session
reply.

## 9. The section 6 risk is still open, and it can be closed without the mesh

Added 2026-09-04 by a third shadow-mode (read-only) session. Static analysis only, nothing
applied. This does not change the morning order in section 7; it adds one test that can run in
parallel with step 1, off the critical path.

**9.1 Last night's 4 of 4 SMS PASS does not cover the demo's SMS leg.** Those were OOB frames,
and OOB is exempt from exactly the two things section 6 is about. `dispatcher.go:1264` gates the
whole egress-transform block on `del.Class != database.DeliveryClassOOB`, and
`PrependVersionByte` sits inside that block at :1303. So an OOB frame is sent as plain GSM-safe
base32 with no version byte, while a relayed demo message is sent as `0x01` plus base64. The
relay leg's wire format has still never crossed KPN. Do not let 4/4 read as "SMS is proven" for
the demo.

**9.2 The version byte is prepended after the GSM-safety check, so the byte actually transmitted
was never validated.** The retry loop at `dispatcher.go:1290` calls `gateway.IsGSMSafe` on the
base64 output and re-encrypts with a fresh nonce up to five times if it fails. `0x01` is added
after that loop. `sendSMSSync` then comments "GSM safety was already validated by the dispatcher
(re-encrypt loop)" (`cellular.go:206`), which is true of the base64 and false of the first byte.

**9.3 The failure is narrower than section 6 assumed.** Three cases, and only one of them breaks
the demo:

| What KPN and the A7670E pair do to `0x01` | Result |
|---|---|
| Preserved verbatim | `StripVersionByte` takes it, base64 decodes, relay works |
| Dropped entirely | first byte is a base64 char, `StripVersionByte` returns the payload unchanged, base64 decodes, relay works |
| Mapped to anything else (`£`, `?`, UTF-8 `0xC2 0xA3`) | `base64.StdEncoding.Decode` (`transform.go:251`) errors, `dispatcher.go:502` logs "ingress transform failed, forwarding raw payload" and the T-Echo shows ciphertext |

Only the third case is a demo failure, and it is visible, not silent.

**9.4 Test it without the mesh, without the T-Keyboard, without the split.** The direct-send API
uses the identical egress path: `POST /api/messages/send {"gateway":"cellular","text":"GSMCHK-1"}`
→ `handleSendMessage` (`api/messages.go:124`) → `ResolveGatewayInterface("cellular")` →
`QueueDirectSend("cellular_0", ...)` → default class, so transforms and the version byte both
apply. Run it from tesseract, read the result on parallax. Unique text, under 80 chars, per
section 3.

Read the answer off parallax's log and `GET /api/deliveries`:

| parallax shows | Meaning |
|---|---|
| delivery to `mesh_0` with `text_preview` = `GSMCHK-1` | relay leg PROVEN, section 6 closed |
| delivery to `mesh_0` with a base64 `text_preview`, plus "ingress transform failed, forwarding raw payload" | case 3, use the section 6 fallback (clear `cellular_0` transforms on both kits) |
| nothing at all | the SMS did not arrive; a bearer problem, not a transform problem |

The delivery row is created whether or not parallax's XIAO is wedged, because a wedged radio
still accepts the USB write (8.2). That is what makes this test independent of the morning's
step 1.

**9.5 Secondary: if `0x01` survives, the Comms view shows ciphertext while the relay works.**
`StripVersionByte` has exactly two call sites in the tree, `dispatcher.go:491` and
`aprs.go:449`. The inbound-SMS persist path calls `ApplyIngress` directly on `msg.Text` at
`processor.go:1065` with no strip, and `oobCandidate` does the same at :124. So in case 1 above
the dispatch path decodes correctly and the persist path fails, logging "gateway inbound:
ingress transform failed, storing raw" and writing the base64 into `messages.decoded_text`.

Two consequences. On camera, the receiving kit's Comms list would show a base64 blob next to a
T-Echo showing clean plaintext, which reads as broken. And `markGatewayInjection`
(`processor.go:1101`) would be keyed on the ciphertext rather than the plaintext, which is the
exact no-op its own code comment warns about, so dedup layer 1 is lost. Layers 2 and 3 (visited
set, `MESHSAT_MAX_HOPS`) still hold, so this is not a loop by itself in the two-segment topology.

This is a real defect but it must NOT be fixed before Friday: it needs a rebuild and a redeploy,
and last night's deploy restart is what preceded parallax's XIAO going silent. Narrate from the
devices (8.2) and file it for after the call.

## 10. Split shape, hot config, and a Plan B that does not need parallax's radio

Added 2026-09-04 00:55 CEST by a fourth shadow-mode session. Read-only: SSH to the kits is gated,
so nothing below is live-verified tonight. Static analysis only. It does not change the morning
order in section 7. It fixes one way the split can be done wrong, names which config changes are
hot, and adds the fallback the runbook is missing if the X1202 cycle does not revive parallax.

**10.1 The split MUST replace the PRIMARY channel PSK. Never add a secondary channel.**
The delivery worker builds the mesh send as `transport.SendRequest{Text, To}` and never sets
`Channel` (`engine/dispatcher.go:1359`), so it is the zero value. `SendMessage` passes it straight
through as `buildTextMessage(req.Text, to, uint32(req.Channel))` (`transport/direct_mesh.go:891`).
The bridge therefore always transmits on channel index 0, and there is no config anywhere to
change that. If the split is done with `meshtastic --ch-add DEMO_B`, segment B lands on index 1,
the far device listens on index 1, and the bridge keeps talking on index 0 to nobody. The demo
would fail in the direction that matters, on camera, with the dashboards green.

Correct shape, on all four nodes: keep one PRIMARY channel at index 0 and change its PSK. PSK-A on
the T-Keyboard and tesseract's XIAO, PSK-B on the T-Echo and parallax's XIAO. Isolation comes from
the channel hash plus PSK mismatch, which is also what the memory file restores afterwards.

**10.2 If parallax's XIAO does not come back, do NOT do the split at all.**
The split exists only to stop LoRa from delivering directly between the two segments. A radio that
cannot complete a handshake cannot hear the T-Keyboard either, so isolation is already absolute.
Doing the split anyway costs a bridge stop, CLI writes to four nodes and a restore afterwards, all
for nothing, on the morning of the call. Skip section 4 step 3 and go to Plan B.

**10.3 Plan B: one kit, one mesh segment, the far end is the SMS itself.**
Forward leg unchanged: T-Keyboard, mesh segment A, tesseract, rule 1, SMS to parallax. Parallax's
modem is healthy and independent of its radio, so the SMS still arrives and still creates a
delivery row for `mesh_0` (a wedged radio accepts the USB write, see 8.2). What changes is where
the proof is read: parallax's Comms view, plus a second SMS copy on a handset in frame (10.5).
Reply leg: an SMS typed on that handset into tesseract, rule 2, mesh, T-Echo, which for Plan B
sits on segment A next to the T-Keyboard.

This proves mesh ingress, rules engine, SMS egress, SMS ingress, mesh egress: the whole bridge.
What it does not prove is kit to kit across two isolated segments. Say that plainly on the call
rather than letting it be inferred; it stays inside prototype framing and it is a better position
than a green dashboard nobody can check.

Plan B needs the transforms cleared (10.4) so the SMS is legible, and it needs the handset's
number in `allowed_senders` on tesseract. `isAllowedSender` (`gateway/cellular.go:365`) is exact
string equality with no normalisation, so copy the number in the exact form KPN presents it, which
is the `phone` column of a real inbound row in `sms_messages`, not the form you would dial.

**10.4 Clearing the cellular transforms is HOT. It needs no restart.**
Both directions read the interface row from the database per message: `DispatchAccess` at
`dispatcher.go:499` and the delivery worker at `dispatcher.go:1265`. `InterfaceManager.UpdateInterface`
(`engine/interface_manager.go:247`) writes the row and swaps the in-memory copy. It does not stop
the gateway, does not touch the modem and does not reopen a serial port. An empty chain passes
validation (`engine/transform.go:327` returns no errors for zero transforms).

So the section 6 fallback can be applied between two demo messages. That matters because every
alternative involves a restart, and last night's restart is what preceded parallax's XIAO going
silent.

One trap. `PUT /api/interfaces/{id}` decodes the body into a fresh `database.Interface`, so it is a
full-row replace. If the body omits `device_id`, the update sees a changed device and drops the
interface to StateUnbound (`interface_manager.go:257`). GET the interface, edit only
`egress_transforms` and `ingress_transforms` to `[]`, PUT the whole object back.

**10.5 To put a readable SMS on a handset in frame, use the rule, not the gateway.**
`PUT /api/gateways/cellular` restarts the gateway instance (3 Sep finding). The rule path does not:
set `forward_options` to `{"sms_contacts":[<id>]}` on rule 1 and the worker resolves the numbers
from the `sms_contacts` table at `dispatcher.go:1407`.

Two precisions. The resolved contacts REPLACE the gateway's `destination_numbers` rather than
adding to them, because `sendSMSSync` only falls back to the gateway config when
`msg.SMSDestinations` is empty (`cellular.go:232`). To keep the kit-to-kit leg AND add the handset,
the contact list must contain both numbers. And `sendSMSSync` returns the first error across
destinations (`cellular.go:245`), so one failing number fails the whole delivery and it retries,
spending SMS again on the destination that already worked. Two destinations is two SMS per relayed
message.

**10.6 `PUT /api/access-rules/{id}` is also a full-row replace.**
`UpdateAccessRule` (`database/interfaces.go:278`) sets every column from the struct. A partial PUT
that adds `forward_options` and omits `filters` blanks `{"portnums":"[1]"}`, and the telemetry
flood from 8.3 comes straight back at roughly 60 SMS per hour per kit, mid-call, on prepaid. GET
rule 1, add the one field, PUT the whole rule. Then re-run the three-minute idle check from 8.3.

**Morning delta.**
Step 1 is unchanged and still gates everything: X1202 button on parallax, then `/api/radio/setup`
must return a non-zero `node_num`. Run the section 9.4 GSMCHK test in parallel with it, since it
needs no radio. Then branch:

- `node_num` present: full demo, split done as a PRIMARY PSK change per 10.1.
- `node_num` still zero after a second power cycle: drop to Plan B per 10.3 and do not spend the
  morning on the split.

## 11. Session end, 4 Sep 01:20 CEST

Kits on b84af42 (MESHSAT-786 power-cycle code, host agent v2, MESHSAT-787 registration refresh),
three redeploys tonight all green. SMS nominal both ways, rules 1 and 2 plus allowed_senders
verified across restarts, zero unintended SMS. Parallax XIAO still wedged: the morning starts with
the X1202 power button. Morning order is section 7, with section 8's rule on top: read results off
the device screens, not the dashboards. The StarTech hubs arrive Saturday and are not part of the
demo.

## 12. Results of 4 Sep 2026, the day of the call (written 4 Sep evening)

**Timeline (CEST):** ~15:07 both kits `systemctl poweroff` (owner instruction), X1202 button cycle by the owner, both back by 15:12 with parallax's XIAO alive (node_num 1808552882) and all four nodes (both XIAOs, `!27ca8f1c`, `!4a20b4e0`) heard by both kits on the shared channel. 16:00 call: pictures and video shown, not the live relay. 17:39 transcript done (Whisper large-v3, GPU).

**Proven, one bearer at a time:** LoRa API text tesseract->parallax 8 s, parallax->tesseract 12 s (`POST /api/messages/send {"text":...}` with NO gateway field: `gateway: mesh` returns 400, the default path is the radio). SMS OOB PING both ways (request legs 1 to 4 s; one P->T SMS took ~8 min, one was lost). **APRS OOB PING both ways, 4 s request legs, when fired one at a time.** The four failed rounds between 15:17 and 15:45 were simultaneous PINGs in both directions: two half-duplex UV-K5s keying over each other on 144.800 looks exactly like one dead receiver. Rule: one RF test in flight per bearer.

**Found: the relay loops and fragments on an unsplit mesh.** With rule 1 (mesh->cellular) live on both kits, a LoRa text heard by both kits went out as SMS from each, the SMS envelope needed 2 segments so the DTN fragmenter produced `[frag 1/2]`/`[frag 2/2]` deliveries, the far side injected each fragment as its own mesh text, the prefix made the text unique so the 5-min dedup missed it, and the next pass produced `[frag 1/3] [frag 1/2] ...`: 13 SMS in ~2 min before rule 1 was disabled on both kits at 13:23Z. Same shape over APRS (rules created: parallax id 3 mesh_0->aprs_0 with the portnum filter, tesseract id 3 aprs_0->mesh_0): the message crossed LoRa, APRS and LoRa in 25 s but as two fragments, the second binary garbage, and parallax relayed it twice before rule 3 was disabled. Root cause: `DispatchAccess` forwards the whole mesh message JSON (261 B) as the payload; `forwardOptions` has only `ttl_seconds` and `sms_contacts`. Fix = text-only forward, MESHSAT-792. Nothing in this section changes sections 6 or 10; it confirms both.

**State left on both kits:** rule 1 OFF, rule 2 (cellular_0->mesh_0) ON, rule 3 (aprs relay) OFF; `allowed_senders` unchanged; OOB `reply_budget` 40 (was 12, exhausted); radios alive; no reboot since 15:08. PSK split NOT done. Generated keys for the split: PSK-A (T-Keyboard + tesseract) `jy9v1jisLJa7UsMe4UI9xVEmYCFdKHyOzBoqK9hKkJw=`, PSK-B (T-Echo + parallax) `nSKo/up/ZVeDX4xD60tFMmIhub7sLrG37hTQArPACC0=`, restore `fHuZ2UHjcSfwW0NKf3TTvrSiVFm59jl6W0EzctqPVkM=`.

**Order for the next live attempt (no date):** MESHSAT-792 fix deployed -> island split per 10.1 (owner does the two handhelds, kit XIAOs via `meshtastic --port /dev/serial/by-id/usb-Espressif_Systems_seeed-xiao-s3_*-if00 --ch-index 0 --ch-set psk base64:<PSK>` with the bridge stopped) -> enable rule 3 on both kits -> one text each way over APRS, one at a time -> film -> disable rules, restore the shared PSK.

## 13. Evening of 4 Sep: power, not radios (read before any RF test)

Both kits died with empty packs by 21:09. Cause chain and the fix are on MESHSAT-793 (12 V inlet refit ordered) and MESHSAT-794 (monitor never halts cleanly). What this runbook needs from it:

- **Pre-flight, always:** `cat /run/x1202.json` (pack SoC, input present) and `journalctl -u x1202-monitor --since -1h | grep -c "AC power lost"` on both kits before any bearer test. A pack under 20 % or a non-zero loss count means power first, RF later.
- **On the current 5 V bench supplies the kits cannot charge under full load** (25 W ceiling vs ~27 W demand): charging only happens with the USB hub cable and the T-Call unplugged from the Pi.
- **Empty-pack loop:** the X1202 auto-starts the Pi on any input and the Pi runs from a boost on the cell node; below ~3.4 V it boot-collapses in a loop that neither the button nor unplugging the input stops. Recovery: cells out, USB load off, cells in, charger straight onto the X1202 board, re-attach above 50 %.
- **APRS:** the afternoon's "parallax cannot decode" was two radios keying at once (section 12); after 15:43 the real cause was power (tesseract's AIOC dropped off the bus at 15:39, parallax's pack went flat). Both radios decoded fine at 15:42 to 15:43 when fired one at a time.

## 14. PicoAPRS V4 replaces the UV-K5 and the AIOC for TTC (owner decision 6 Sep 2026; supersedes the DMR858M plan of 5 Sep) — SUPERSEDED 7 Sep 2026: the UV-K5 + AIOC chain IS the TTC hardware, see section 16

**Decision.** The DMR858M needs six soldered wires to castellated pads and a bulk capacitor, and the owner rules out electronics work, so the modules go to the V2 carrier (PCB-D has their socket). The community-graduated replacement is the **PicoAPRS V4 VHF** (DB1NTO, WiMo SKU PICOAPRS.VHF, EUR 299 incl. VAT, in stock 6 Sep): a complete 2 m APRS transceiver with its own AFSK TNC, 0.8 W, 7-pole harmonic filter, KISS over its USB-C virtual COM port at 115200 baud, at most 500 mA from USB, runs while charging, 850 mAh cell that must stay inserted. Two ordered, one per kit. The NA-771 bulkhead leads screw onto its SMA socket (light loads only, no strain on the socket). The UV-K5 and its AIOC cable stay in the pouch until closed-case kit-to-kit pings pass on both kits. Research record: MESHSAT-748 comments of 6 Sep and the `DMR858M for MeshSat APRS` artifact.

**What changes in the bridge (MESHSAT-821):** the APRS gateway opens the TNC's serial port itself (`kiss_device` in the aprs_0 gateway config, first-boot default `MESHSAT_APRS_KISS_DEVICE`, baud `kiss_baud` / `MESHSAT_APRS_KISS_BAUD` default 115200), no Direwolf, no sound card, no preflight. The Reticulum `ax25_0` interface takes its frames from the gateway's fan-out with `MESHSAT_AX25_KISS_ADDR=gateway` (a serial TNC is one file handle). The receive watchdog works from frame counts (`receive_level` is gone, `receive_state` and `last_decode_at` stay); its second rung reopens the TNC port instead of cutting the AIOC's hub port, and `RESET aprs 3` over OOB does the same plus a gateway restart. The device supervisor is told to leave the TNC's port alone (`ExcludePort`): the PicoAPRS's CP2102 has the ZigBee dongle's VID:PID 10c4:ea60, and the ZNP probe would pulse the modem lines into the ESP32's reset.

**Device setup, once per unit (bench, tesseract first):**
1. Battery in, PTT held 3 s to turn on. Firmware update to the current version over WiFi via the web UI, then WiFi off. Record the version on MESHSAT-821.
2. Device Mode = TNC, USB-C. TNC frequency 144.800 (its own store since firmware 23). TX power 0.8 W. GPS Powersave on. Brightness minimum. Beep off. Bluetooth off. Callsign and SSID are irrelevant in TNC mode; the bridge writes the source address.
3. Persistence check: battery out and in, USB off and on; it must come back in TNC mode on 144.800 with nothing but the 3 s turn-on press.
4. Charge fully on the bench before it goes on the hub; the 500 mA charge current lands on the Pi's USB budget (section 13, MESHSAT-523 class) until the cell is full.

**Kit configuration:** `ls -l /dev/serial/by-id/` with the unit plugged into the StarTech port 2 (the AIOC's old port) gives the stable name (`usb-Silicon_Labs_CP2102N_..._-if00-port0`). In `/srv/meshsat/docker-compose.yml`: `MESHSAT_APRS_KISS_DEVICE=<that path>`, `MESHSAT_AX25_KISS_ADDR=gateway`; `MESHSAT_AIOC_CAPTURE` and the preflight become inert. If the product string is custom, a udev `SYMLINK+="picoaprs"` rule in `scripts/install-kit-network.sh` keeps both kits identical; otherwise the by-id path is a documented per-kit difference.

**Order of tests (one direction at a time, section 12 rule):** (1) raw, pyserial on the by-id port at 115200 while the other kit beacons: KISS frames `C0 00 ... C0` arrive; (2) bridge: `GET /api/gateways` shows `aprs_0` connected with `tnc_serial: true`, `receive_state` moving to `ok` on the first frame, `ax25_0` online; (3) OOB PING request leg, then reply leg, then closed cases across the lab, both kits; binary payloads (OOB, Reticulum) pass unchanged; (4) hub-port cut of the TNC port: receive continues on the battery, the port re-enumerates under the same by-id name, the gateway reconnects by itself; (5) total power loss: the unit stays off until the 3 s press, which is the one runbook line for the booth; (6) 24 h soak with the receive watchdog, no `deaf`, no rung above 1. Go/no-go before 18 Sep: (3) and (6) pass on both kits, then the K5 and the AIOC leave the kits.

**Booth line:** after any total power loss of a kit, hold the PicoAPRS PTT for 3 seconds until it beeps. Nothing else on the device is ever touched.

## 15. APRS receive health, the watchdog and the demo failover (MESHSAT-814, 5 Sep 2026)

**Why:** APRS receive on this chain failed on three consecutive days for three different reasons (dead UV-K5 packs, an AIOC brownout, an unexplained 39 minutes on parallax with transmit working). Transmit never failed. A silent receiver looked "connected" on every dashboard. The demo must not rest on one radio chain staying up unnoticed.

**What the bridge does now:**
- Direwolf runs with `-a 10`, so it reports the receive audio level every 10 s whether or not anything decodes. `GET /api/gateways` and `GET /api/aprs/status` carry `receive_level`, `receive_level_at`, `last_decode_at` and `receive_state` (`ok`, `quiet`, `deaf`, `unknown`). Target level is about 50 (Direwolf User Guide); the AIOC capture gain is set by the preflight (`MESHSAT_AIOC_CAPTURE`, default 94 %, parallax 90 %). `receive_level` is the level of the last 10 s statistics window, so between frames it reads 0 on a healthy receiver; read `last_decode_at` (and the `audio level = N` decode lines in the log) for signal strength, and use the level only as "the audio path is alive" evidence. `receive_state` survives a bridge restart since 7e452cf: the last-heard time is kept in `system_config`, so a receiver that was hearing the peer before a deploy and hears nothing afterwards turns `deaf` after the silence window, fails over and walks the ladder; a kit switched on alone stays `quiet`.
- The receive watchdog (`MESHSAT_APRS_RX_WATCHDOG_MIN`, default 5) declares the receiver deaf when no frame decodes for that long after the channel was heard within two hours, or when Direwolf runs but stops reporting. It then walks the ladder one step per window: restart the APRS gateway (Direwolf respawn), cut the AIOC's hub port through the host agent and restart, restart the bridge (at most once an hour). Every step is an SSE event and an audit entry. A kit alone in the field stays `quiet` and never escalates.
- While deaf, `aprs_0` and `ax25_0` score 0 in the health scorer, so a failover group routes around them.

**Demo routing:** make the cross-kit hop a failover group with `aprs_0` primary and `cellular_0` fallback (Settings > Routing > Failover groups, or `POST /api/failover-groups`), and point the relay rule's destination at the group instead of `aprs_0`. Rehearse once with the receiver deliberately silenced on the receiving kit (`POST /api/gateways/aprs/stop`) and confirm the message arrives over SMS within about a minute; then start the gateway again.

**One-line receive check on a kit:** `docker logs --since 10m meshsat 2>&1 | grep -ac "audio level"` (the peer beacons every ~10 s, so 0 in ten minutes means deaf), and `curl -s localhost:6050/api/aprs/status | python3 -m json.tool | grep -E "receive|decode"`.

**Hardware side (owner):** UV-K5 menu for data use: BatSav OFF, VOX OFF, STE OFF, RP STE OFF, RxMode MAIN ONLY, TOT 60 s. The DMR858M (section 14) replaces the K5's speaker amplifier with a line output and removes the amplifier shutdown, battery and volume variables; the AIOC stays. Ferrites on both ends of the AIOC USB lead (RF on that lead is the documented cause of latched PTT and USB drops). AIOC firmware 1.4.1 lets the serial DTR/RTS PTT source be switched off and adds RX gain; update one kit first, tesseract, from a running device: `dfu-util -d 1209:7388 -a 0 -s 0x08000000:leave -D aioc-fw-1.4.1.bin`.



## 16. APRS between the kits on the UV-K5 + AIOC chain: what was wrong, what was fixed, the booth pre-flight (7 Sep 2026, MESHSAT-857/858/859)

**Owner rulings 7 Sep evening:** the UV-K5 + AIOC chain is the chain going to TTC (build and test on it as final hardware); Reticulum over AX.25 (`ax25_0`) is off for the booth; the MESHSAT-792 text payload lands; the booth relay is APRS first with SMS as the fallback, both directions, and the panels animate the bearer each message really took.

**What was actually losing messages, in order of damage:**
1. **Receiver dead after a watchdog restart (MESHSAT-858).** The receive watchdog restarts `aprs_0` under a 90 s step context and the gateway manager started the new receiver with it, so 90 s later Direwolf kept decoding into a channel nobody read: `messages_in` climbed, no `gateway inbound message` line, nothing relayed. Fixed in 56e2f9b (receivers live as long as the manager). Symptom to recognise: decoded `[0.N] MSxxx-10>APMSHT:{E1}...` lines with no `gateway inbound message` after them.
2. **Physical link losing one frame in four even on a quiet channel.** 20 direct frames one way (`scratchpad/aprs-linktest.sh`): 16/20 and 15/20 at Direwolf's default `TXDELAY 30`. The UV-K5 squelch opens late for a 300 ms preamble. `tx_delay 50` gave 20/20 (see the numbers below). The value lives in the `aprs_0` gateway config (`tx_delay`, `tx_tail`, `persist`, `slot_time`, Direwolf units) and is applied with `PUT /api/gateways/aprs {"enabled":true,"config":{...}}`.
3. **Collisions with our own housekeeping.** The mesh time-sync request went out every 30 s from each kit over `ax25_0` (26 B `MSxxx>RTICUL`), ~240 frames per kit in two hours, and each one could sit on top of a relay frame (half-duplex). `POST /api/interfaces/ax25_0/disable` does nothing; the switch is `MESHSAT_AX25_KISS_ADDR=` (empty) in `/srv/meshsat/.env` on each kit and `docker compose up -d` (backup `.env.bak-2026-09-07-ax25`). Follow-up for a real per-interface toggle: MESHSAT-859.
4. **The watchdog escalating on a quiet peer.** Five minutes without a decoded frame counted as "our receiver is deaf" and walked to the AIOC port cut and a bridge restart; at a quiet booth that is normal silence. Since 41f3ee9 rungs 2 and 3 only run when Direwolf's own audio stats are stale (`hung`); rung 1 (Direwolf respawn) still runs on silence and is harmless. Env: `MESHSAT_APRS_RX_WATCHDOG_MIN` (5), `MESHSAT_APRS_RX_HEARD_WITHIN_MIN` (120), `MESHSAT_APRS_RX_STATS_STALE_SEC` (90).
5. **No liveness signal on the channel.** Each kit now transmits a plain APRS status beacon every 45 s (`beacon_secs` in the aprs config, text `>MeshSat <call> ok <n>`, no digipeater path). It keeps the peer's watchdog satisfied, shows the kits to any APRS receiver at the show, costs ~0.4 s of airtime, and is never a message (the gateway stops at the heard list for status frames; the panels pulse the air link only).
7. **No acknowledgement on APRS.** After the timing fix the link still lost about one frame in twenty, and a lost frame is a lost message. `tx_repeat 2` in the aprs config sends every message twice, `tx_repeat_gap_ms 4000` apart (longer than the far kit's own transmit bursts, so the copies never both fall into its blind time); the far bridge's payload dedup and the mesh text dedup drop the copy; beacons get the same copies since f9c9a24. Final relay test 22:39: 3/3 each way over APRS.
8. **The failover group picked SMS over a healthy APRS (found on the first live handheld text, 20:24).** The resolver only asked the interface manager, where gateway-backed channels stay unbound; since b265c06 a connected gateway counts as online. Check: `GET /api/interfaces/aprs_0` may read `unbound` forever, that is normal; the relay log line `failover: resolved to online member group=peer_link ... resolved=aprs_0` is the proof.
6. **388 bytes on the air for a two-character text (MESHSAT-792).** The relay carried the whole JSON envelope; since 41f3ee9 a mesh text bound for a text bearer (APRS, SMS, mesh) travels as the text: one short frame, one SMS, no `[frag n/2]` on the far handheld.

**Relay pre-flight without a handheld:** `POST /api/messages/simulate-mesh-rx {"text":"pre-flight 1"}` on a kit feeds the text through the radio-receive path (dedup, rules, delivery ledger); the far kit must log `gateway inbound message source=aprs` and `delivery sent channel=mesh_0`, the sender `failover: resolved ... resolved=aprs_0`. Driver: `scratchpad/relay-test.sh <label> [N] [aprs|sms]` (both directions, counts on both kits).

**Results 7 Sep 20:56 to 21:11 (image 93d8ec920d37):** APRS both ways 3/3 (about 3 s end to end including the repeat copy); parallax's APRS gateway stopped: parallax's own texts to SMS 2/2 (about 5 s), tesseract's texts to the deaf parallax lost over APRS until tesseract's watchdog declared the peer silent (3 min 16 s with `MESHSAT_APRS_RX_WATCHDOG_MIN=3`, now in both kit compose files), then SMS 2/2 with `receiver is deaf, skipping`; gateway restored: tesseract heard parallax again within 30 s, APRS 2/2 both ways. **The window a dead peer radio costs is therefore up to 3 minutes of lost APRS texts on the other kit**; the beacon is what closes it. First quiet check (beacon 90 s, silence 3 min): parallax hears tesseract weakly (audio level 36 against 55), missed two consecutive beacons, restarted its gateway, which cost tesseract two beacons, which restarted tesseract: a coupled oscillation. Beacon set to 45 s (four consecutive losses needed for a false deaf), re-checked over 15 quiet minutes. Root cause found the same night: parallax's `.env` had `MESHSAT_AIOC_CAPTURE=90%` (about -10 dB, a 5 Sep leftover) against tesseract's 94 %; at 94 % parallax decodes tesseract at level 66 (16/12), the same as the other direction. Both kits now run 94 %.

**Booth pre-flight (both kits, in this order):**
1. `docker logs --since 10m meshsat 2>&1 | grep -a "audio level = "`: lines with the peer's callsign every 90 s (its beacon). Zero lines in ten minutes = deaf; the `ADEVICE ... CH0` stats lines do not count.
2. `GET /api/aprs/status`: `connected true`, `receive_state ok`.
3. `GET /api/gateways/aprs`: `tx_delay 50`, `beacon_secs 30`, `tx_repeat 2`, `tx_repeat_gap_ms 4000` (messages and beacons both go out twice, 4 s apart: with 1.5 s both copies fell inside the 2 s in which the far kit transmits its own beacon pair and cannot hear, and one message in four was still lost).
4. No `MSxxx>RTICUL` in either Direwolf log (ax25_0 off).
5. Rules: `GET /api/access-rules` shows rule 1 `mesh_0 -> peer_link` on, the inbound `aprs_0 -> mesh_0` and `cellular_0 -> mesh_0` rules on, no direct `mesh_0 -> aprs_0`; `GET /api/failover-groups` lists `peer_link` (aprs_0 priority 1, cellular_0 priority 2).
6. One text from each handheld: one line on the far handheld, APRS lane on both panels. If a receiver is deaf (Direwolf stopped, radio off), the same text goes over SMS and the panel shows the SMS lane.
7. UV-K5 menu on both radios: BatSav OFF, STE OFF, VOX OFF, RxMode MAIN ONLY, squelch at the lowest level that stays closed on the empty channel; packs charged. A radio left in a menu or rebooted takes both kits deaf while everything on the bridge looks healthy (20:00 on 7 Sep).
8. `relay-test.sh preflight 2 aprs`: 2/2 each way with `resolved=aprs_0`.
9. Receive levels: `grep -a "audio level = "` on each kit shows the peer's beacons at 50 to 70; below 45 check `MESHSAT_AIOC_CAPTURE` in `/srv/meshsat/.env` (both kits 94 %) and the K5 volume.

**Measured numbers (7 Sep 2026, quiet channel, 20 frames one way, 6 s apart):**
| Setting | parallax -> tesseract | tesseract -> parallax | receive level |
|---|---|---|---|
| TXDELAY 30 (default), no beacon | 16/20 decoded, 16/16 processed | 15/20, 15/15 | 64 to 67 (17/14) / 37 to 38 (10/8) |
| TXDELAY 50, beacon 90 s | 20/20, 20/20 | 20/20, 20/20 | 64 to 65 (17/13) / 35 to 36 (10/7) |
| TXDELAY 70, beacon 90 s | 19/20 | 18/20 | same |
| TXDELAY 50, confirmation run, beacons excluded | 18/20 | 20/20 | same |

Per frame the tuned link sits at about 90 to 100 percent; per message, with `tx_repeat 2`, a loss needs both copies to fail. The link test reports unique decoded frames, so with the repeat on it still counts one per message.


## 17. TTC readiness board (updated 8 Sep 2026, end of the booth-selector session; about 75 %; **10 Sep late: about 85 %, section 26 all green and the Hub SMS lane proven, section 18; 12 Sep 01:15: still about 85 %, both kits all green after a reboot, section 27 lists the two power-on traps the booth must expect**)

**Amended late on 8 Sep (section 21).** Still about 75 %: everything that moved that evening was software, and the gap to ready is unchanged. Both kits are **powered off** and need a physical X1202 long-press before anything remote works. Two risks were added to the board rather than removed from it: a wedged cellular AT channel can kill both booth SMS lanes for as long as nobody restarts the bridge, while the panel still shows cellular connected (MESHSAT-986); and APRS can still go deaf for up to an hour on the K5 chain (fourth instance that evening, 62 minutes). The PicoAPRS swap is the single highest-value item of the hardware week.

Moved on 8 September: the booth flow selector is live on both kits with four paths offered and the kit-to-kit SMS path proven, the booth screen was rebalanced so the island is the hero, the fourth path over Iridium IMT landed, and the Hub can now pair as a management peer from the bridge side. Nothing moved on the hardware chain, so the PicoAPRS swap, the 12 V refit and both soaks are still the gap between here and ready.

| Area | State | Closes when |
|---|---|---|
| Kit-to-kit relay, APRS first, SMS fallback, both directions | proven through the rules on the UV-K5 + AIOC chain (section 16 numbers) | re-measured on the PicoAPRS chain, then a 24 h soak with beacons only |
| Booth screen (TTC mode) | 16/16 checks on both panels at the real viewport; rebalanced 8 Sep so the island with the kit and the handheld is the hero and the message strip is one row (d127869, f04fa41), live on both panels | a full-day run on both panels |
| Radios | Meshtastic 2.6.10 on both kits, mesh split (msat-ttc-01 / 02), Bluetooth off | Bluetooth read-back at the next serial window |
| Power | 5 V inlet unchanged (both kits died on 4 Sep) | 12 V refit (parts 9 Sep) and 24 h zero-AC-loss on both kits |
| APRS hardware | PicoAPRS V4 live on both kits since 10 Sep; receive never deaf in 7.5 h; stray-byte frames repaired and the port no longer reopened on a bad frame (MESHSAT-1020); 17 to 20 of 20 texts per run, second copy + SMS fallback behind | MESHSAT-1021 (message copy vs beacon copy share the 4 s gap), 24 h soak, TNC port cut, power-loss + 3 s PTT (MESHSAT-821); K5 + AIOC stay in the pouch until then |
| Handhelds | T-Echo (mesh A), T-Deck Plus (mesh B), T-Deck Pro ordered, power packs 8 Sep | Pro joined to an island, charging plan for two days |
| Bench items | SanDisk card (MESHSAT-819), X1202 switch plug (MESHSAT-805) arrived; screen protectors FDHYFGDY 2-pack for the Touch Display 2 ordered 8 Sep (Amazon 407-8701954-9922741, EUR 9.99, Sat 12 Sep; one sheet per kit, no spare) | installed on both kits; protector checked against the 155.5 x 88 mm window before peeling, fitted with the top plate off |
| Plate stack | middle plate sags under the X1202, cells and Pi 5; two extra M3 rods at mid-span of the long edges (MESHSAT-863) | fitted on both kits, plate pulled flat, fieldkit BUILD.md + CAD updated |
| Software follow-ups | MESHSAT-861 (resolver honours a disabled interface, receive_state after a restart), MESHSAT-859 (time-sync config) | landed and verified |
| Booth paths on the panel | four lanes drawn, tap to choose (MESHSAT-962; kit-to-kit SMS proven 8 Sep 10:27Z, APRS unchanged, **Hub SMS lane proven 10 Sep 20:44 to 20:51Z, 10/10 both ways, 4 to 7 s, MESHSAT-1022**, satellite lane waits for tesseract's 9704 and the Hub route) | 5/5 texts each way on APRS and kit-to-kit SMS on the PicoAPRS chain, IMT lane (section 20) |
| Hub over SMS/IMT without internet on the kit | satellite fallback uplink wired (MESHSAT-963, code only); Hub commands over SMS/IMT (MESHSAT-964, after the Hub migration) | 963: Hub fleet page shows the kit alive with WiFi off; 964: PING over SMS with WiFi off |
| Logistics | hotel and taxis arranged, prints and stickers 9 to 11 Sep | booth slot and Hub allowlist from Thomas, transport and setup plan |

Twenty task "TTC booth readiness follow-ups" carries the same list with a 15 Sep due date.

## 18. Booth flow selector: three paths on the panel, one lit (MESHSAT-962, 8 Sep 2026)

Owner rulings 8 Sep: the Hub is never reached over the internet from the kits at the booth; it talks to
the bridges over satellite (no sky indoors) with SMS as the fallback. Visitors pick the path on the
panel itself: the three lanes are drawn together, the chosen one is lit, the other two dimmed, and a
tap on a lane selects it for THIS kit's outbound messages. A second tap on the lit lane opens its card.

| Path | What happens | Rule on the kit |
|---|---|---|
| APRS radio | mesh text goes out on aprs_0 through the `peer_link` group (SMS to the peer when its receiver is deaf), the 7 Sep relay | `ttc:aprs` = the old rule 1, renamed (all three are INGRESS rules on mesh_0, the relay convention; the first deploy created egress rules by mistake, which the relay path never evaluates, deleted from both kits 8 Sep 10:05) |
| SMS via the Hub | one SMS from this kit's SIM to the Hub's Twilio number; the Hub's route texts the other kit's SIM; that kit's cellular->mesh rule delivers | `ttc:hub_sms` (forward_options.sms_contacts = the Hub contact) |
| SMS kit to kit | one SMS straight to the peer kit's SIM | `ttc:b2b_sms` (sms_contacts = the peer contact) |

Selection is per kit and egress only. Inbound stays open on both kits for all three sources
(aprs_0->mesh_0 and cellular_0->mesh_0 enabled, `allowed_senders` on cellular_0 = the peer SIM AND the
Hub number). The reply follows whatever the OTHER panel has selected. The far screen tells the SMS
lanes apart by the sender: the peer's SIM is "SMS kit to kit", the Hub's number is "SMS via the Hub".
Messages animate on the lane they actually took, whatever is selected.

API (all on the kit, port 6050):

```
POST /api/ttc/flow/setup  {"peer_number":"+316...","hub_number":"+3197010258258"}   once per kit
GET  /api/ttc/flow                                                               path, rules, ready, issues
PUT  /api/ttc/flow        {"path":"aprs"|"hub_sms"|"b2b_sms"}                    what the lane tap does
```

Setup is idempotent: it stores the numbers in `system_config` (`ttc_peer_number`, `ttc_hub_number`;
the peer falls back to cellular_0's first destination number, the Hub to `MESHSAT_HUB_SMS_NUMBER`),
creates the two SMS contacts, adopts rule 1 as `ttc:aprs`, creates the two SMS relay rules disabled (ingress on mesh_0), enables
the two inbound rules, and extends `allowed_senders` (that last step restarts the cellular gateway,
the modem answers again after about 20 s, so run setup before the doors open, never during a demo).
`PUT` flips exactly one `ttc:*` rule on and reloads the evaluator; the choice survives a restart.
The panel composer's "Remote mesh" follows the chosen path too (`gateway` aprs or cellular, and `to`
= the Hub number for the Hub path; `/api/messages/send` honours `to` for gateway sends since this change).

How the Hub leg works on the wire (found while building this, 8 Sep): the Hub's routing engine
relays a matched SMS as plain text with an `[origin] ` prefix (`formatRoutedSMS`), it cannot decrypt
the kits' shared SMS key, and it has no sender filter, so both routes fire on every inbound SMS and the
Hub also texts the copy back to the kit that sent it. The bridge handles all three: the Hub's number is
a `plaintext_peer` on cellular_0 (setup adds it): SMS to it leave as bare text without the interface
transforms, SMS from it skip the ingress transforms, the `[origin] text` prefix is parsed off, and a
copy whose origin is not an allowed sender (this kit itself, or a stranger texting the Hub) is dropped
with a log line `Hub echo of this kit's own SMS, ignoring`. Kit-to-kit SMS stays encrypted as before.
The Hub-side sender filter is a follow-up on the Hub after its migration (MESHSAT-964 family).

Bench order when the kits are back:
1. Both kits: `POST /api/ttc/flow/setup` with the peer's and the Hub's numbers; read `GET /api/ttc/flow`
   until `ready` is true and `issues` is empty.
2. Hub routes: DONE 8 Sep 2026 on the NL DMZ Hub (they migrate with the data): `TTC: kit A tesseract
   -> kit B parallax over SMS` (route-1788829643834124971, source sms, filter +31653207829) and
   `TTC: kit B parallax -> kit A tesseract over SMS` (route-1788829643942342000, filter +31653618463).
   The Hub's routing engine has no sender filter, so BOTH fire on every inbound SMS: the copy that goes
   back to the origin kit is dropped by the bridge's echo guard, the other one is the relay. The April
   test route `Relay MO -> SMS mule01` (any source -> tesseract's SIM) is disabled so tesseract does not
   get every message twice. Two April routes still stand and are the owner's call: `Relay MO -> SMS
   Android` and `Relay -> SMS Android` send every inbound SMS (so every booth message on the Hub path)
   to the owner's phone as well, one or two extra Twilio SMS per text. Until MESHSAT-910 lands the
   routing engine runs on both DMZ nodes and a matched route can dispatch twice (MESHSAT-711); count
   the SMS on the far kit and Twilio's log before the show, the bridge's relay dedup hides the second
   copy on the mesh but Twilio bills it. API from the DMZ host: `docker exec meshsat-hub-nginx wget -qO-
   --header="Authorization: Bearer $HUB_AUTH_TOKEN" http://meshsat-hub:6070/api/routes` (the 8451 proxy
   speaks PROXY protocol to HAProxy and cannot be curled locally; the edge answers 401/403 elsewhere).
3. Per path, 5 texts each way from the handhelds, `scratchpad/relay-test.sh` style, and the latency
   per path noted here. Each text must arrive exactly once.
4. Both SIM balances and the Twilio balance sized for two days at up to two SMS per text.

**Hub lane PROVEN 10 Sep 2026 20:44 to 20:51 UTC (MESHSAT-1022): 10/10 both directions, modem send
to far-kit receive 4 to 7 s (median 6), inject to far-kit log 11 to 13 s, exactly one copy per text,
no echo, no `[frag n/m]`.** It took two Hub fixes the same evening: the Twilio webhook's plain-text
branch never published to the routing engine's topic (only base64 SMS did, and even that carried the
text under the wrong key), and the phone number's `+` is an MQTT wildcard, so the first corrected
publish was refused by NATS and took a Hub replica into a 30 s reconnect loop until the pod was
deleted (Hub MRs !126 and !129, build 8db42cf1, ids now travel percent-encoded on the wire).
Booth facts that follow: the far kit needs no lane selected (inbound is open, the lane is egress
only); the Hub's `Relay MO -> SMS Android` route is DISABLED for TTC so the owner's phone does not
get a copy of every visitor text (re-enable on MESHSAT-860); the five seeded `Satellite -> *`
routes fired on SMS until 22:11Z the same night (Hub MR !132: they now listen to a `satellite` source
only, an SOS text over the Hub lane raises the Hub's SOS event, no escalation chain is configured on
the live Hub). Test tool: `POST /api/messages/simulate-mesh-rx` on the
sending kit with `hub_sms` selected, then `docker logs` on the far kit for
`SMS received sender=+3197010258258 text="[<sender SIM>] ..."`.

## 19. Hub without internet on the kit: the satellite fallback uplink (MESHSAT-963, 8 Sep 2026)

`internal/hubreporter/satfallback.go` existed since April and was never wired (no caller in main.go).
Since this change the bridge arms it whenever a Hub URL is configured: when the MQTT session to the
Hub is down for `MESHSAT_HUB_FALLBACK_AFTER_MIN` (5), the kit sends compact binary frames (magic
`MS`, under 340 bytes) to the Hub: position every `MESHSAT_HUB_FALLBACK_POSITION_MIN` (15), health every
`MESHSAT_HUB_FALLBACK_HEALTH_MIN` (60), and an SOS at once when SOS is activated on the panel. The
frames ride the delivery ledger as class `hub_uplink` (bypasses egress rules and interface transforms,
visible in the queue widget): raw bytes over `iridium_0` (SBD or IMT; the SBD gateway got a raw path),
or base64 text over `cellular_0` to the Hub's number (`MESHSAT_HUB_SMS_NUMBER`, or the `ttc_hub_number`
stored by the TTC setup, which wins). The Hub already decodes them from its SMS, RockBLOCK and
Cloudloop webhooks and marks the bridge online with its position.

Bearer policy `MESHSAT_HUB_FALLBACK_BEARER`: `auto` (default) takes the satellite gateway only when it
is connected AND has moved traffic in the last 30 min, otherwise SMS (indoors the modem is present but
a queued satellite frame would never leave); `satellite` and `sms` force one leg. Kill switch
`MESHSAT_HUB_SAT_FALLBACK=0`. A failed initial Hub connect counts as a disconnect.

Bench: kit WiFi off (or the Hub MQTT port blocked), wait 5 min, then `docker logs` shows
`satfallback: activating satellite fallback mode` and a `hub uplink frame` delivery on cellular_0;
the Hub fleet page shows the kit online with the position within a minute of the SMS; with sky on
parallax the same frame leaves over IMT. Owed: that bench run on both kits, and a look at where the
Hub UI shows an SMS-borne health frame (fleet page shows online + position; health may be log-only).

## 20. Fourth booth path: kit to kit over Iridium IMT (MESHSAT-962 follow-up, 8 Sep 2026)

Scenario: the free RockBLOCK 9704 from Ground Control lands and goes into tesseract, so both kits carry an
IMT modem and the demo can run a satellite leg under sky (outside the hall, or the antenna at a window).
The panel draws a fourth lane above the radio, "Satellite, Iridium" (lavender, a small satellite glyph),
selectable like the other three; it reads "modem not answering" in amber until the 9704 gateway is up.

On the wire: a mesh text on kit A leaves over `iridium_imt_0` as an MO to Cloudloop; the Hub relays it as
an MT to kit B's 9704 (Hub route, MESHSAT-964 deliverable D, an external dependency); kit B's IMT gateway
receives the MT and the inbound rule `iridium_imt_0 -> mesh_0` puts it on mesh B. There is no
device-to-device Iridium.

Rules (both kits, created by `POST /api/ttc/flow/setup`): `ttc:imt` = `mesh_0` ingress, forward_to
`iridium_imt_0`, disabled until chosen; inbound `ttc:in iridium_imt_0 -> mesh_0`, enabled. `GET
/api/ttc/flow` carries an `imt` block: running, connected, imei, last_mo_at, last_mo_status, recent_mo
(10 min), last_mt_at, queued, dlq_pending. Choosing imt on a kit without its modem is allowed; the one
issue says the texts will queue.

What to expect on the screen: the dot climbs from the kit to the satellite and waits there ("on its way
up to the satellite, this can take a minute or two"); a second text within the first one's session parks at
the kit ("waiting for the satellite slot, one message at a time": the delivery worker sends one at a time
and a JSPR MO can block up to 180 s); on a failed session the status reads "no satellite in view yet,
trying again at hh:mm:ssZ" (retries 30 s, 60 s, 120 s, then dead: "no satellite in view, it did not get
out"); on success "accepted by the satellite at hh:mm:ssZ, the other screen shows it arriving" and a `sat
tx` packet with `mo_status=0` in the nerds table; the far kit shows `sat rx` from cloudloop and the text
on its mesh. Nothing in the dispatcher enforces `MESHSAT_PAID_RATE_LIMIT`; the serialisation is the worker.

Tesseract 9704 install (owner, hardware and host):
1. Pull the 9603 off UART0; free BCM 22 and 23. Wire the 9704 like parallax: 5 V pin 2, GND, BCM 4 (pin 7)
   UART2 TX to 9704 RXD, BCM 5 (pin 29) UART2 RX to 9704 TXD, BCM 23 I_BTD (input, pull-up), BCM 26 I_EN,
   BCM 24 P_EN low. Never USB and the 16-pin header together.
2. `/boot/firmware/config.txt`: `dtoverlay=uart0-pi5` out, `dtoverlay=uart2-pi5` in, `enable_uart=0`
   stays; reboot; `/dev/ttyAMA2` appears.
3. Install and enable `meshsat-gpio.service` from parallax.
4. `/srv/meshsat/.env`: `MESHSAT_IMT_PORT=/dev/ttyAMA2`, `MESHSAT_IMT_GPIO_I_EN=26`,
   `MESHSAT_IMT_GPIO_I_BTD=23`, `MESHSAT_IMT_GPIO_CHIP=gpiochip4`; remove the five `MESHSAT_IRIDIUM_*`
   lines; restart the container.
5. Bridge: disable the `iridium` gateway, enable `iridium_imt`; `GET /api/gateways` shows it connected
   with the new IMEI.
6. Cloudloop: register the IMEI as a Thing on an IMT plan (airtime is not part of the free unit), webhook
   to the Hub. Hub device registry: replace tesseract's 9603 IMEI. The Hub route A to B and back needs
   both IMEIs.
7. `POST /api/ttc/flow/setup` on both kits, then `GET /api/ttc/flow` until `imt.connected` is true.
8. The 9704 firmware is unreliable below 10 C; evening outdoor tests need that in mind.

Test order under sky, after the Hub route exists: both panels on the Satellite lane; one text from the
T-Deck; watch queued, climb, `sent` with mo_status 0, the Cloudloop MO, the Hub MT to tesseract's IMEI,
`sat rx` on tesseract, the text on the T-Echo; time both hops; the reverse; two texts within a minute; the
antenna covered; five each way arriving exactly once. Record on MESHSAT-962.

## 21. Evening of 8 Sep 2026: booth screen redesign, kiosk drift, and two faults found on the kits

Written at the end of the session that powered both kits down for the night. Both kits ran build `6ad829d` when they were halted.

### 21.1 The kits are OFF and need a hand to come back

Both were stopped at 20:00 UTC: docker first (`systemctl stop docker.service docker.socket`, so the SQLite WAL closes cleanly), then `poweroff`. **A remote poweroff is one-way.** It halts the Pi but does not cut the X1202's output, so the boards keep drawing from the packs, and the X1202 only auto-powers-on when input *appears* — on a kit sitting on mains, the input never went away. Nothing remote reaches them until someone long-presses each X1202 button. **And after the button press the bridge does not start by itself (9 Sep 2026, MESHSAT-994):** the recipe's `docker stop` marks the container manually stopped and `restart: unless-stopped` honours that across the power cycle; both kits ran an hour with docker up, no bridge, and Chromium parked on an error page. Since 9 Sep both kit compose files carry `restart: always` and the kiosk launcher relaunches Chromium when the bridge turns healthy. Still, after every power-on run `docker ps` on both kits; if the container is exited, `cd /srv/meshsat && sudo docker compose up -d` and `sudo systemctl start meshsat-kiosk-restart.service`. Do not push to main expecting a deploy while they are down: the deploy jobs will simply fail, and the kits will boot on whatever image they already hold.

### 21.2 Booth route selector, redesigned (MESHSAT-962, commits 6a3d684 + 6ad829d)

The four routes were four labels floating above four captions, and the sub of one route sat closer to the next route's label than to its own; the "tap a path to choose it" hint read as a fifth option; and two of the four routes had no icon at all. Each route is now **one line of text riding its own lane line**: icon at the inner end, name plus the fact that identifies the bearer above the line, state word pinned to the outer end, and the explaining sentence under the **chosen route only**. All four carry an icon of one family at one size in the bearer's colour, filled when chosen.

Constraints for anyone editing `web/src/views/TtcView.vue` later (also in `.claude/rules/web-spa.md`): the detail sentence has about **42 characters** of room; a state word beyond about **8 characters** collides with the fact on one mirror and the name on the other; no boxes, because the lane must keep running off the screen edge; Signal Orange stays reserved for live traffic, never selection; each glyph needs its own vertical nudge to sit on the wire.

Same screen also got: footer counters labelled `packets in/out, last minute` with a slash instead of the middle dot that read as a decimal, finger-sized footer buttons that say what they do, and plain words for the foreign-mesh packet line.

**How it was verified, and how to verify the panel in future:** Playwright against each kit's live SPA (never a dev server on the runner), viewport 853x480 with `device_scale_factor=1.5` and `has_touch=True`, `wait_until="load"` — `networkidle` never fires because the SPA holds SSE and polling open. History-mode URL: `http://<kit>:6050/ttc?kiosk=1&shell=operator`. Shoot **both** kits: the mirrored layout failed differently from the near one, twice.

### 21.3 Kiosk config drift, and a landmine in this repo (MESHSAT-987)

The two kits' `~kiosk/.config/labwc/autostart` had diverged (parallax had `--ozone-platform=wayland`, a named `--user-data-dir` and a log redirect; tesseract had none). Worse, `deploy/kiosk/labwc-autostart` and `deploy/kiosk/99-touch-rotate.rules` in this repo still carried April's `--transform 90` and the CCW touch matrix, while **both** kits run `270` with `0 1 0 -1 0 1`: an `install-kiosk.sh` run would have turned that kit's panel upside down with mis-mapped touch. Both files now carry what the kits run, and the kits are hash-identical across `autostart`, `rc.xml`, `99-touch-rotate.rules`, `meshsat-kiosk.json` and `meshsat-backlight`.

**Chromium is pinned:** both kits on 152.0.7977.64 (rev 3524) with auto-refresh **held until 2026-10-09**. snapd defers a refresh while the kiosk holds the snap open and then forces it when its inhibition window expires — that would have landed inside the TTC window. Unhold after 23 Sep. To align or refresh a kit, tear the session down first with the recipe in `deploy/kiosk/meshsat-kiosk-restart.service`; `systemctl stop getty@tty1` alone does not kill Chromium.

### 21.4 Two faults found, one filed as a booth risk

**MESHSAT-986 (Open), cellular:** a wedged AT channel parks the device-health probe forever, because `DirectCellTransport.Probe` ignores its context and `execAT` enqueues on `cmdCh` with a blocking send that has no timeout. `DeviceHealth.tick` then skips the target for good, so the ladder freezes at step 1 and neither `AT+CFUN=1,1` nor the VBUS cut can run. parallax sat like that for about 20 minutes with **both booth SMS lanes dead while `/api/gateways` and `/api/cellular/status` still reported connected and registered_home**. Only a container restart clears it. Same shape in `execRawFn`; the IMT and SBD command channels want the same audit.

**APRS, fourth deaf window (MESHSAT-748):** tesseract 18:43:19 to 19:44:51 UTC, 62 minutes, `0 errors` with `receive audio level CH0 0` throughout, surviving two Direwolf respawns and a container recreate, transmit unaffected. The 5 Sep "began seconds after a VBUS cut on a neighbouring hub port" correlation does **not** hold here. The K5 speaker amplifier remains the standing explanation and the PicoAPRS swap remains the fix. Note for forensics: `docker logs` only holds the current container, so a recreate destroys the evidence — `GET /api/audit?limit=300` keeps the watchdog's "APRS receive silent" rows with the exact silence start.

## 22. Afternoon of 9 Sep 2026: an hour without a bridge after a scripted poweroff (MESHSAT-994)

**Timeline (CEST):** 12:52 both kits cold powered on after the night off. 13:11 owner instruction "poweroff both kits", executed as the clean recipe: `docker stop -t 30 meshsat`, `systemctl stop docker.socket docker.service`, `systemctl poweroff`. About 13:50 both kits powered on by the X1202 button. The Pi, docker and the labwc session came up; the bridge did not. 13:56 the kiosk launcher gave up its five-minute health wait and started Chromium on an error page. 14:38 found; 14:41 `docker compose up -d` on both kits and a kiosk relaunch; 14:46 both panels showing the dashboard; parallax ZigBee needed one watchdog cycle (MESHSAT-815 recurrence, recovered at 15:01).

**Cause:** the recipe's `docker stop` marks the container manually stopped and `restart: unless-stopped` honours that across daemon restarts and reboots. Nothing on the kit starts the bridge after a power-on. The kiosk launcher's soft launch then parks the browser on "site can't be reached" indefinitely.

**Fix (06ebbe3, pipeline 52754, both kits verified 16:05):** kit compose files and `deploy/docker-compose.prod.yml` run `restart: always`, applied live with `docker update --restart=always meshsat` so no recreate was needed; the launcher's soft launch now arms a watcher that polls `/health` every 5 s and replaces Chromium once the bridge answers. Bench on both kits with a daemon restart standing in for the power cycle: container back in 6 to 12 s; tesseract also ran the full soft-launch path and the watcher swapped the browser 5 s after health. The compose recreate in `ci-deploy.sh` kept the policy.

**Still owed:** one real button power cycle, which TTC setup provides. After the first power-on there, `docker ps` on both kits; if a container is exited, `cd /srv/meshsat && sudo docker compose up -d` and `sudo systemctl start meshsat-kiosk-restart.service`. Section 21.1 carries the same recipe.

## 23. Evening of 9 Sep 2026: the ZigBee coordinator was being reset by the bridge itself (MESHSAT-815)

**Symptom:** since 5 Sep both kits lost the ZigBee coordinator roughly every 30 minutes and once more 20 to 50 s after every bridge start. The device-health ladder healed it each time (1 to 3 min, sometimes a hub-port VBUS cut), so the booth screen showed the sensor lane going amber several times an hour. Filed as an unexplained "power-up" reset of the CC2652P.

**Cause (found from the logs, 9 Sep):** `InterfaceManager.scanDevices()` ran every 5 s over every serial port, claimed ones included, and resolved the two shared VID:PIDs (CP210x = ZigBee dongle, CH9102 = T-Call) through a probing classifier whose probe opens the port. The container has CAP_SYS_ADMIN, so the transport's exclusive lock does not stop a second open(); the CP210x open asserts DTR/RTS, which fires the Sonoff auto-BSL circuit and resets the coordinator, and the same open reboots the T-Call's ESP32. A 30-minute probe cache (MESHSAT-510) was the only throttle, hence the period; the first scan after start explained the post-start reset. dmesg showed only software USB resets from our own recovery, never a physical disconnect: the hub port and the dongle were innocent. Side effect closed by the same fix: the cellular signal-poll timeouts that sat on the 30-minute marks.

**Fix (38a03ed, pipeline 52831, image 5733d0d on both kits 21:30 CEST):** the scan labels ports through the DeviceSupervisor's claimed roles and never opens them; the probe cache and the probing classifier are gone. Verified 21:30 to 23:05 CEST on both kits: zero coordinator resets, stuck reads, ZigBee heal steps or cellular poll timeouts, no cp210x lines in dmesg, sensors reading, hard budgets untouched. MESHSAT-815 at To Verify; the owner's check is `GET /api/devices/health` on both kits after a night, expecting no zigbee heal events.

**Rule that came out of it:** only the DeviceSupervisor may open a serial port to identify it, and only an unclaimed one, once. Anything else that needs a port's type asks the supervisor's registry. And a "hardware" reset with a round period is a timer in our own code: grep for the period before blaming USB.

**Seen alongside, unrelated:** parallax's XIAO missed its handshake after the container restart and the mesh target walked to level 3 (hub-port power cycle) at 21:35 to 21:38 CEST, back in 3.5 min. Known parallax pattern after a warm restart (memory `project_meshtastic_channel_meshsat.md`).

## 24. Morning of 10 Sep 2026: the power cycle passed, and the two radios were talking to each other all night (MESHSAT-994, MESHSAT-1000)

**Power cycle.** Both kits were powered off at 23:30 on 9 Sep (clean systemd poweroff in the journal) and came back at about 11:36 on 10 Sep. On both the bridge was up and healthy inside three minutes with `restart: always` intact, and grim captures showed Chromium on the operator dashboard (tesseract dark theme, parallax NVIS). The one open item of section 22 is closed; MESHSAT-994 is Done. Keep the `docker ps` habit after the first TTC power-on regardless.

**Deploy trap.** Pipeline 52846 for the MESHSAT-996 merge ran at 23:36 on 9 Sep, six minutes after the kits went off. Its two deploy jobs failed (allow_failure, pipeline green) and both kits kept the previous image until the next push. After any power-on, compare `docker inspect -f '{{.Image}}' meshsat` on both kits with the last pipeline's image before assuming the kits carry a fix.

**The loop.** Owner report: a text sent from the T-Deck showed on parallax's screen and reached the T-Echo, but never showed on tesseract's screen. The relay had worked: parallax stored the text at 09:47:32Z, the `ttc:aprs` rule sent it on `aprs_0` (delivery 319 plus the repeat copy), tesseract decoded it from MSPRLX-10 at 09:47:35Z, rule 3 queued it to `mesh_0` (delivery 329, second copy deduped) and stored it twice (rows 101851 and 101857). What hid it: since the channel split each kit radio hears the other on a key it cannot decrypt; the transport treated every such packet as an unnamed node and sent it a NodeInfo request, once per packet, unthrottled; the peer could not read the request and its bridge sent one back. About 30 directed packets a minute each way, one NAK per request, every hop persisted as a messages row: tesseract took 16,128 rows in 24 hours, and of its last 2,000 rows two were texts. `GET /api/messages` returns 50 rows, filled in 40 seconds, so the dashboard feed never held the text. The loop started about eight minutes after every boot since 6 Sep, and burned 868 MHz airtime in the room the whole time.

**Fix (ccad4d9, pipeline 52916, image 9061a79f on both kits by 11:05Z).** `handlePacket` never auto-requests NodeInfo across a packet with `Decoded == nil` and asks any unnamed node at most once per 10 minutes; gateway inbound decodes the ingress transforms before the `inbound` live event, so the activity log shows the text and not the APRS ciphertext; the operator dashboard fetches text messages only (`portnum=1`) and refreshes the feed after `message`, `text`, `inbound` and `relay` events instead of once at mount. Verified: zero auto-requests on both kits, ROUTING_APP rows down from about 27 a minute to one. What still crosses between the radios is each kit's own telemetry broadcast (about once a minute, 33-byte frames in pairs) and the 5-minute routing announce (175 bytes); the telemetry interval looks short for a booth and is a radio-config item for after TTC.

**Still owed:** one T-Deck text while tesseract's screen is watched, to confirm the feed now draws a relayed text. The APRS repeat copy persists the inbound text twice while the delivery is deduped once; cosmetic, noted on MESHSAT-1000.

## 25. Afternoon of 10 Sep 2026: PicoAPRS V4 on both kits, the booth screen draws relayed texts, parallax loses its RTL-SDR (MESHSAT-821, MESHSAT-1000, MESHSAT-1001, MESHSAT-1002)

**PicoAPRS V4 is the APRS chain on both kits.** Parallax from 15:02, tesseract from 15:12, each on StarTech port 2 where the AIOC sat. The device is set up on its own buttons (Settings > Device Mode > KISS TNC > USB-C, APRS frequency 144.800); USB is charge plus KISS only, there is no configuration or firmware path over USB. Kit switch per kit, in this order: the two `.env` lines (`MESHSAT_APRS_KISS_DEVICE=<by-id path>`, `MESHSAT_APRS_KISS_BAUD=115200`; the compose file passes both through), `docker compose up -d`, then `PUT /api/gateways/aprs` with `kiss_device` and `kiss_baud` in the config. The env alone does not switch an existing kit: the stored gateway config carries an empty `kiss_device` and a stored key beats the env default; the env is still needed so the device supervisor excludes the port (the PicoAPRS's CP2102N shares the ZigBee dongle's USB IDs, and before the switch the supervisor probed it every 14 s). Proof of the day: unit to unit 12 of 14 and 11 of 12 frames decoded, 0 errors; OOB PING both legs (3 s and 9 s); a simulated T-Deck text on parallax on tesseract's mesh in 2.4 s. The receive watchdog on a serial TNC has two software rungs (gateway restart, port reopen) and cannot reach the bridge restart, which needs Direwolf audio stats; no rung cuts USB power. Firmware: latest V26 (Oct 2025), nothing after V23 touches TNC mode; read Device Info, leave units at 23 or above alone. Still owed before the K5 and AIOC leave the kits: the TNC port cut, a total power loss with the 3 s PTT press, the 24 h soak.

**First 100 minutes on the new chain, and two fixes (MESHSAT-1020).** Each kit logged `kiss: trailing escape` twice, each followed by `reconnected to the TNC` 5 s later. The read worker treated every decode error as a dead link and reopened the serial port, a Direwolf-over-TCP assumption; on the PicoAPRS the reopen discards what the TNC buffered and pulses the CP2102's modem lines, and the packet ring shows each event cost the peer's repeat copy as well as the corrupted frame (tesseract 17:40:39 UTC = parallax's 17:40:38 beacon and its 17:40:42 copy; parallax 17:20:23 = tesseract's 17:20:23 and 17:20:27 pair). Since MESHSAT-1020 a frame that does not decode is dropped with its bytes in the log (`aprs: dropped undecodable KISS frame`, `frame=<hex>`), counted as `bad_frames` on `GET /api/aprs/status` and `GET /api/gateways`, and the port stays open; only EOF or a vanished port reconnects. The next occurrence tells us what the TNC actually sent. Frame loss measured 17:19 to 17:52 UTC on a mostly quiet channel: parallax to tesseract 29 of 158 (18 %), tesseract to parallax 33 of 156 (21 %); only 9 and 13 of those fell within 2 s of the receiver's own transmission, nearly all during the two-minute link test, so collisions are a minority and the rest is a radio question for the 24 h soak (two 0.8 W units a few metres apart on rubber ducks; try spacing or the case antennas and re-measure). The beacon wait is now jittered ±20 percent so the two kits cannot lock phases; `beacon_secs 30`, `tx_repeat 2`, gap 4000 ms stay as chosen on 7 Sep. Link test on the chain before the change: 19 of 20 messages processed each way, 0 to 4 s. **The hex answered it within fifteen minutes of the deploy:** every dropped frame was a complete AX.25 frame (a beacon `>MeshSat MSPRLX ok 15`, an encrypted text ending in its base64 padding) with one stray 0xDB appended before the closing FEND, on both kits, on both copies of the same message, about 2 to 3 percent of frames. So the PicoAPRS adds a byte rather than losing one, and a strict decoder loses exactly those messages (the 17 of 20 and 16 of 20 of the first post-deploy link test). Since the second MESHSAT-1020 commit a lone trailing FESC is stripped and the frame kept, counted as `repaired_frames` on `GET /api/aprs/status`; `bad_frames` is now only for frames that really do not decode.

**The booth screen now draws a relayed text.** The morning fix (section 24) had touched the operator dashboard; the panels run `/ttc`. On the receiving kit an encrypted APRS frame produces a `packet` event with no text, which the booth view filed as housekeeping. Since c563581 the `inbound` event carries the decoded text and the booth view starts the inbound trip from it (15 s same-sender dedup for the APRS repeat). Proven with `POST /api/messages/simulate-mesh-rx` on parallax and grim on tesseract. **Owner ruling 15:47: both kits boot into `/ttc?kiosk=1&shell=operator` for TTC week** (installed autostart, backups next to it), so the nightly recycle and any Chromium relaunch keep the booth page. Revert after TTC is on MESHSAT-860.

**Parallax's RTL-SDR is off the bus.** It failed re-enumeration at 12:21 and stayed dead through a host-controller reset and two ganged root VBUS cuts (5 s and 25 s, bridge stopped around them, every other device came back clean, the modem within a minute). The owner re-seated it by hand at 14:49 and it came straight back: a contact fault, not a dead dongle. Tested 16:30 to 16:45 against tesseract's: scans every 2 s, 0 errors, three bands clear with baselines within 2 dB, no USB errors since the re-seat (MESHSAT-1001, To Verify overnight). Consequences already landed (f275ecc): the spectrum monitor attaches a dongle that appears after startup, the `rtl_sdr` health target always exists, and its hard rung asks for the hub-port cut before the sysfs reset. Two more StarTech hubs arrive 17 Sep (MESHSAT-1002) so RTL-SDR and GPS move off the Pi root ports, which the Pi 5 can only switch as one group.

## 26. Evening of 10 Sep 2026: full health check of both kits, the battery reads 100 %, and the LTE bands never calibrate (MESHSAT-674, MESHSAT-1017, MESHSAT-1018)

**Health check, 16:38 and 17:37 CEST, read-only over SSH and the bridge API.** Both kits green: bridge healthy on the same image (3d6fddfd, then 1a12b4ec after the evening deploy), mains present, WiFi at -39 to -43 dBm, Hub MQTT connected with the birth certificate published, clocks synced, kiosks up. Every watchdog target `ok` on both kits: mesh (8 nodes on tesseract, 49 on parallax), cellular registered home on KPN, 9603 answering AT on tesseract, 9704 with live JSPR on parallax, PicoAPRS decoding both ways (tesseract rx 266 / tx 316, parallax rx 253 / tx 312 by 17:37), ZigBee, GPS, RTL-SDR scanning. The full table is the 10 Sep comment on MESHSAT-674. Parallax's DCF77 line is quiet as before.

**One watchdog heal on parallax's RTL-SDR (16:56 to 17:01 CEST).** Four `rtl_power_fftw` scans in a row were killed, the ladder went scan-restart at 16:57:50 and USB reset at 17:00:50 (the root port is not switchable, so the hub cut fell through to `USBDEVFS_RESET`), the dongle recovered at 17:01:20 and has scanned clean since. Same dongle the owner re-seated at 16:49 (section 25, MESHSAT-1001); noted on that issue as evidence for the verify.

**The two LTE bands never finish calibrating (MESHSAT-1017, Open).** `lte_b20_dl` and `lte_b8_dl` sit at `calibrating` with 0 samples on both kits while LoRa, APRS and GPS L1 are `clear` with 300+. The calibration window is 30 s and needs 5 samples; a 3 MHz LTE scan takes about 10 s, so it collects 3, logs `insufficient calibration samples` and retries every 35 s (107 warnings an hour on tesseract). Cellular jamming detection is therefore silently absent. Not booth critical.

**Why the battery never showed 100 % (MESHSAT-1018, To Verify).** Tesseract sat at 4.15 V / 95.0 % and parallax at 4.12 V / 91.5 % for five hours on mains; 48 h maxima 4.17 V / 97.8 % and 4.13 V / 92.8 %. The number is the raw MAX17040 SOC register. Its fixed cell model puts 100 % near 4.2 V open circuit, while the X1202 terminates at 4.23 V and only recharges below 4.1 V (Geekworm spec), so a full pack rests at 4.12 to 4.17 V and the register plateaus at 91 to 98 %. The quickstart the monitor sends at every boot re-seeds the SOC from the loaded voltage, which costs a few more percent. Not a charging fault, and the 12 V refit (MESHSAT-793) will not move it.

**Fix (da4cd6c, pipeline 52983, installed by hand on both kits 17:54 CEST).** `scripts/x1202-monitor.py` learns each pack's own full point: mains present, cell above the 4.1 V recharge threshold, raw SOC unchanged (within 0.25 %) for 20 min means the charger has terminated, and that raw value is stored in `/var/lib/x1202/full_soc.json` as this pack's 100 % (floor 85 %). Every reading is scaled by it and clamped to 100; `soc_raw` and `full_soc` are added to `/run/x1202.json`; the shutdown thresholds keep the raw register. Learned at 18:13 to 18:14 CEST: tesseract raw 95.0 % at 4.146 V, parallax raw 91.3 % at 4.117 V, both now 100 % on `GET /api/system/battery`. The hand-installed diagnostic `/usr/local/bin/ups_check.py` read the register directly and still said 91 and 95; it now prints the scaled value with the raw register and the learned point beside it, and is tracked as `scripts/ups_check.py` (7ae9950). Old copies of both scripts are backed up next to them with a `.bak-2026-09-10` suffix.

## 27. 11 Sep 2026, a full day on the kits: PicoAPRS off after a night without USB, Hub link lost at boot, the panel gets the T-Deck Pro and a poster, the LTE bands calibrate, the SanDisk clone fails (MESHSAT-1027, 1028, 826, 1017, 819, 1001)

**Morning, both kits powered on by the owner at 12:32.** Health board green except two things the board could not see. (1) tesseract's Hub MQTT link: the first connect failed at boot (`hubreporter connect: network Error : EOF`, WiFi not yet associated) and the reporter never retries a failed first connect, so the TAK relay stayed down and the satellite fallback armed on home WiFi. A bridge restart fixed it; the code gap is **MESHSAT-1027** (Open, `SetConnectRetry` missing). **Power-on checklist item: `GET /api/gateways` must show `tak_hub_relay` connected on both kits; if not, `POST /api/system/restart`.** (2) Both PicoAPRS units were silent: `rx 0`, `heard 0`, zero bytes on the USB bus in 70 s while the bridge wrote beacons, no RF on the peer's SDR. Cause per the WiMo manual and firmware changelog: the unit shuts down on an empty cell (850 mAh) and only a 3 s PTT press turns it on; the kits had been without USB for 12 h. **Power-on checklist item: after any night with the kits off, press PTT 3 s on each PicoAPRS and check `rx` climbs on `GET /api/aprs/status`.** The bridge cannot tell: the CP2102N enumerates from VBUS whatever the unit's state, and the receive watchdog only fires once it has heard a frame. **MESHSAT-1028** (Open) carries the silent-TNC detection and the cold-start arm. Datapoint the same night: parallax off 20:54 to 01:00 (6 h without USB) and its PicoAPRS decoded within a minute of power-on without a press; the cell holds more than 6 h and less than 12.

**Booth screen (owner requests, all live, MESHSAT-826):** tesseract draws the T-Deck Pro (store product photo, e-paper covered by the live message list), parallax the T-Deck Plus without the SMA stub; the per-handheld facts sit in one `DEVICES` table in TtcView.vue. The route tap is capped at 8 s and says "failed" on the row instead of hanging: the owner's taps did nothing while a deploy restarted the bridge, and every push restarts both bridges for tens of seconds, **so do not push while someone is tapping the panels.** The kiosk poster (owner's bears artwork) is an interlude, not a lid: after 3 min idle it shows for 20 s every 4 min and drops by itself; any touch or a relayed message drops it at once; `?saverMs`, `?saverShowMs`, `?saverEveryMs` override. The backlight dim is 3 min (swayidle 180, both kits) and still never fires on mains (MESHSAT-827). Verified with Playwright at 853x480 / DSF 1.5 and grim captures of the real panels.

**Spectrum (MESHSAT-1017, To Verify, e3eb692):** the LTE bands never calibrated because their 3.0 MHz windows plus crop padding made rtl_power_fftw hop (3.6 MHz above the dongle's 2.4 MHz), 9.5 s per scan against 2 s, three samples per 30 s window where five were needed, the dongle busy two thirds of the time, the other bands sampled every 13 to 21 s instead of 3. Now 2 MHz single-hop windows (the GPS L1 shape), sample-driven calibration with a 90 s ceiling, a test that fails the build if any band hops. Both kits: five bands clear in 2.5 min from start, every scan under 2.5 s, parallax load from 4.5 to 2.1. MESHSAT-1029 closed as a duplicate; MESHSAT-1030 filed: the About tab's spectrum section 404s (double `/api` prefix).

**SD card clone (MESHSAT-819):** parallax powered off cleanly at 20:54 (docker stopped, WAL checkpointed, db backed up). **`poweroff` cuts the X1202 output by itself; the kit goes dark, no button press needed (owner ruling); power-on is the button.** The Samsung (2018, 128 GB) checked clean in the laptop and was imaged in 10 min on a USB 3 reader (`~/parallax-clone-2026-09-11/` on the laptop: boot area + used root blocks, 52 GB, verified against the source; also a backup of parallax as of 20:54). The SanDisk SR256 write lost data silently: a corrupted directory, 75 MB of zeros where the image has data, fsck removed about 1,800 files, the final compare differed in 133,065 blocks; a full surface test then found 174,336 bad 4K blocks (681 MB) in 11 runs between 22 and 39 GB. **Card returned to Amazon, replacement and a Transcend TS-RDF8K2 reader ordered.** Lesson: a clone is verified only by a full read-back compare of the used blocks; an fsck that passes and spot checks that match prove nothing about file data. Parallax is back on the Samsung.

**tesseract's RTL-SDR left the bus at 21:32** (root port 4-1, `device not accepting address, error -62`, the parallax signature of 10 Sep); the watchdog's ladder cannot reach a device that is off the bus on a root port. A plain `systemctl reboot` of both kits at 01:09 brought it back; both kits all green at 01:14, five bands clear. The second StarTech hub (17 Sep, MESHSAT-1002) moves the dongle off the root port for good. **Power-on checklist item: `lsusb | grep 0bda` on both kits; a missing dongle needs a reboot or a re-seat, not the watchdog.**


## 28. 12 Sep 2026: both kits boot eight hours in the past, and the bridge was handing that clock to the radio (MESHSAT-1056)

**Found by accident.** The kits were powered off on request at 09:18 and back on a minute later. The health sweep afterwards was all green on both, but the journals disagreed with the wall clock: the poweroff commands are stamped `01:45:43` and `01:46:21` in the previous boot, and that boot's own `uptime` said 35 minutes at a moment when the real time was 09:17. The kits had been running since 08:42 with a system clock 7h32m in the past, and chrony never corrected it for the whole 36-minute life of that boot.

**Root cause, two parts, both confirmed on both kits.** A Pi 5 has no RTC backup cell fitted, so the kernel starts at the epoch: `rpi-rtc soc:rpi_rtc: setting system clock to 1970-01-01T00:00:14 UTC` appears in every boot. `fixrtc` on the kernel command line then sets the clock from the root filesystem's last mount time, and the boot re-writes that field with the value it just restored. `tune2fs -l /dev/mmcblk0p2` reads `Last mount time: Sat Sep 12 01:10:14` on tesseract and `01:10:49` on parallax, which is exactly what each kit booted with, twice in a row. **The clock is frozen at the last mount that happened to be correct, so every cold boot from here comes up at 12 Sep 2026 01:10 until a time source breaks the loop.**

**The correction was a coin flip.** `chrony.conf` carries `makestep 1 3`, so chrony may only step during its first three clock updates, and nothing ordered `docker.service` after time sync (`chrony-wait.service` was disabled on both kits, docker had no `After=time-sync.target`). On the 08:42 boot chrony selected sources at 01:10:30 and logged no step at all. On the 09:19 boot it logged `System clock wrong by 29363.704900 seconds` and stepped 17 s in, seven seconds before docker started. Same kits, same configuration, an hour apart.

**Why a wrong clock is worse than wrong timestamps.** The kit exports it. `direct_mesh.go:1063` writes `time.Now()` into the attached radio's RTC on every serial handshake (the fan-out to every NodeDB node stays off, `MESHSAT_MESH_TIMESYNC_REMOTE` unset, so the handhelds were not touched). `consensus.go:250` answers any mesh peer's time-sync request with the raw local clock. `tak_cot.go:142` builds `stale = now + staleSec`, so every CoT event is born eight hours stale and ATAK greys or drops the track. Go's TLS verify uses the wall clock, so a freshly issued cert can read as not-yet-valid. `device_health.go:503` seeds the hard-reset budget from DB timestamps with no monotonic reading, so past resets look future-dated, the budget reads as spent and a target parks in `failed` instead of healing. And none of it shows on the panel, because every "3 s ago" is measured against the same wrong clock.

**What landed (4b94bd1).** Host side, `deploy/time/meshsat-clock-guard` + `scripts/install-kit-time.sh`: a oneshot ordered `Before=docker.service` that tries, each bounded and capped at 90 s in total, a plausibility floor stamped at install, `chronyc waitsync`, one `AT+CCLK?` read of the cellular network clock, and one GPS RMC sentence with a valid fix. It records the verdict in `/run/meshsat/clock-state` and a copy at `/srv/meshsat/data/clock-state` (the data directory is already bind-mounted, so no compose change), and it **always exits 0** — a booth kit must never be left without a bridge because the clock could not be established. The serial rungs only open ports matched by their `/dev/serial/by-id` name, so they cannot land on the XIAO or the PicoAPRS. `chrony-wait.service` is deliberately still disabled: it blocks until a source answers, and at a stand with no internet it would hold the bridge down. Bridge side, gated on that verdict and failing open in every direction (no file, unreadable file, malformed file, or a verdict from an earlier boot all read as trusted): no set-time to the radio, mesh time-sync answered with stratum 16, a peer that answers stratum 16 discarded rather than down-weighted, the verdict in `GET /api/devices/health` and a `clock ?` chip in the header.

**The cellular rung needs the modem to be told.** `AT+CTZU?` returned `0` on both kits, automatic time update off, so `AT+CCLK?` read `70/01/01` — the guard's plausibility check rejects that, which is the right answer but leaves the rung inert. `AT+CTZU=1` plus `AT&W` set on both kits on 12 Sep; NITZ arrives at the next network registration, so **whether KPN actually delivers it is only proven at the next cold boot.** Both kits are registered on KPN LTE (`+COPS: 0,0,"KPN",7`).

**Power-on checklist item: `date` on both kits before the booth opens.** If the header chip says `clock ?`, or `journalctl -u meshsat-clock-guard -b` names source `none`, the kit is running on a clock nothing could establish: its timestamps are wrong, its CoT will not draw, and it is deliberately refusing to give the radio or the mesh a reference.

**Still open:** the RTC backup cells (official Raspberry Pi RTC Battery, ML2020 with a JST plug, about EUR 6 each) are the only fix that needs no network at all, plus `dtparam=rtc_bbat_vchg=3000000` in `/boot/firmware/config.txt` — a device-tree parameter, not an EEPROM write. Not ordered yet.

**Afternoon of 12 Sep, verification on the live kits.** Both kits took the change over two pipelines (53475, 53478) and run `sha256:71c3b876`. `systemctl show docker.service -p After` now names `meshsat-clock-guard.service` on both, the guard is enabled and active with the same md5, and the container reads the verdict at `/data/clock-state` without any compose change. The gating was exercised rather than assumed: writing `trusted=no` for the current boot flips `GET /api/devices/health` to `{"trusted": false, "source": "none"}` inside the 15 s cache and raises an amber `CLOCK ?` chip in the header (Playwright at 853x480 / DSF 1.5: 57 x 21 px at x=783, inside the header row); a verdict carrying another boot's id is ignored and reads `{"trusted": true, "source": "stale"}`; the real file restores `floor`. Both kits are back on their own state file, `trusted=yes source=floor`.

**The modem needed telling.** `AT+CTZU?` answered `0` on both kits, so the A7670E had never taken network time and `AT+CCLK?` read `70/01/01` — which the guard's plausibility check rejects, correctly but uselessly. `AT+CTZU=1` and `AT&W` are now set on both; NITZ arrives at the next network registration, so whether KPN delivers it is only known after a cold boot. Both kits are registered on KPN LTE (`+COPS: 0,0,"KPN",7`). A defect found while checking this and fixed in the same session: `"20"+yy` turned the unset modem's `70` into 2070, so the parser now rejects any year outside 2024..2049 and uses `calendar.timegm` instead of `mktime` minus `time.timezone`.

**tesseract lost its RTL-SDR again at 12:43:35** (`usb 4-1: device not accepting address 2, error -62`, then four `descriptor read/64, error -110`), mid-scan and **before** either container restart of the afternoon, so neither the deploys nor the clock work caused it. The watchdog spent a level-3 reset and could not recover it; the dongle is on a Pi root port. Deliberately **not** rebooted: a warm reboot rewrites the root filesystem's last mount time with the correct clock and moves the frozen `01:10` value the cold-boot test is meant to confirm. Re-seat it by hand in the same session as the RTC cells. parallax is unaffected, five bands clear. Third root-port drop in three days (MESHSAT-855, MESHSAT-1001, MESHSAT-1002).

**RTC cells ordered 12 Sep, expected Monday 14 Sep** — two official Raspberry Pi RTC Batteries (ML2020, JST-SH, adhesive pad). Everything else is ready for them: `/sys/class/rtc/rtc0/` already exposes `charging_voltage` (0, charger off), `charging_voltage_max` 4400000, `charging_voltage_min` 1300000 and `battery_voltage`, so Ubuntu's kernel honours `dtparam=rtc_bbat_vchg=3000000`; `hwclock` is installed on both; neither `config.txt` carries an rtc line yet. **Trap for the fitting session: with no cell connected tesseract's `battery_voltage` floats** — five reads a second apart gave a steady `5128` and an earlier read `4273`, while parallax reads a clean `0`. A fitted ML2020 should read roughly 2400 to 3100 mV and climb over hours. The cell charges at 3 mA, so fit it Monday and judge it days later, not the same evening. Full step list in the MESHSAT-1056 comments.
