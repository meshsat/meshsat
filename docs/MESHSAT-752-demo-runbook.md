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


## 17. TTC readiness board (7 Sep 2026, end of the APRS session; about 70 %)

| Area | State | Closes when |
|---|---|---|
| Kit-to-kit relay, APRS first, SMS fallback, both directions | proven through the rules on the UV-K5 + AIOC chain (section 16 numbers) | re-measured on the PicoAPRS chain, then a 24 h soak with beacons only |
| Booth screen (TTC mode) | 16/16 checks on both panels at the real viewport | a full-day run on both panels |
| Radios | Meshtastic 2.6.10 on both kits, mesh split (msat-ttc-01 / 02), Bluetooth off | Bluetooth read-back at the next serial window |
| Power | 5 V inlet unchanged (both kits died on 4 Sep) | 12 V refit (parts 9 Sep) and 24 h zero-AC-loss on both kits |
| APRS hardware | tuned K5 chain being replaced by the PicoAPRS V4 this week | section 14 device setup, then section 16 pre-flight on the new chain |
| Handhelds | T-Echo (mesh A), T-Deck Plus (mesh B), T-Deck Pro ordered, power packs 8 Sep | Pro joined to an island, charging plan for two days |
| Bench items | SanDisk card (MESHSAT-819), X1202 switch plug (MESHSAT-805) arrived; screen protectors FDHYFGDY 2-pack for the Touch Display 2 ordered 8 Sep (Amazon 407-8701954-9922741, EUR 9.99, Sat 12 Sep; one sheet per kit, no spare) | installed on both kits; protector checked against the 155.5 x 88 mm window before peeling, fitted with the top plate off |
| Plate stack | middle plate sags under the X1202, cells and Pi 5; two extra M3 rods at mid-span of the long edges (MESHSAT-863) | fitted on both kits, plate pulled flat, fieldkit BUILD.md + CAD updated |
| Software follow-ups | MESHSAT-861 (resolver honours a disabled interface, receive_state after a restart), MESHSAT-859 (time-sync config) | landed and verified |
| Booth paths on the panel | three lanes drawn, tap to choose (MESHSAT-962, dad90ac + 0597731); Hub routes created on the NL Hub | 5/5 texts each way on each path on both kits, section 18 numbers |
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
| APRS radio | mesh text goes out on aprs_0 through the `peer_link` group (SMS to the peer when its receiver is deaf), the 7 Sep relay | `ttc:aprs` = the old rule 1, renamed |
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
creates the two SMS contacts, adopts rule 1 as `ttc:aprs`, creates the two SMS rules disabled, enables
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
