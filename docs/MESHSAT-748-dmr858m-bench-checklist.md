# DMR858M bench checklist for the V1 kits (MESHSAT-748)

Read-and-do for the day the two NiceRF DMR858M modules land. It replaces the UV-K5 in the APRS chain of tesseract and parallax for TTC; the AIOC v1.2 stays as sound card and PTT, Direwolf and `aprs_0` do not change. Facts come from NiceRF's V1.2 datasheet (fieldkit repo `v2/vendor/dmr858/dmr858m-v1.2.pdf`, pin table in the geometry appendix item 10), the runbook section 14, and the 5 Sep 2026 bench work on the AIOC (MESHSAT-814). One kit at a time, tesseract first.

## 0. Before touching the module

- Both AIOCs run firmware 1.4.1 with PTT1 = CM108 GPIO3 only, stored in flash (done 5 Sep 2026). Verify: `sudo ~/aioc-util/venv/bin/python ~/aioc-util/aioc-util.py --dump | grep PTT1` on the kit prints `CM108GPIO3`.
- An antenna or a 50 ohm load is on the module's SMA before any PTT. Never key an open SMA.
- Power comes from a source that gives 5 V at 2 A: the X1202's USB-A output through a cut USB-A lead, or a bench supply. Never a Pi USB port (1.6 A total budget for all peripherals).
- The K5 and its AIOC cable stay in the pouch until step 8 passes with the case closed.
- Multimeter (the kit's ANENG) at hand: DC volts and continuity.

## 1. Module pads (24 castellated, 2.54 mm pitch, pin 1 at the SMA end of the right row)

| Pad | Name | Use on the bench |
|---|---|---|
| 1 | VCC | 5 V (3.7 to 8.5 V allowed; 5 V gives about 2 W, 8 V gives 5 W and is PCB-D's job) |
| 2, 4, 15, 17 | GND | common ground, also to the AIOC's sleeve |
| 3 | CS | leave open or tie high; 0 = sleep |
| 5 | PTT | active low, 0 = transmit; from the AIOC's PTT line |
| 6 | LINE_OUT | receive audio, about 130 mV; to the AIOC's speaker input |
| 7 to 10 | channel select 8421 | leave open (rotary knob and default apply) |
| 11, 12 | OUTP, OUTN | speaker, unused |
| 13 | MIC- | to GND |
| 14 | MIC+ | transmit audio, module supplies the mic bias; from the AIOC's mic line |
| 16 | SPKEN | 3.3 V logic, high while a signal is received; optional, see step 6 |
| 18, 19 | TXD, RXD | 57600 8N1 configuration UART, 3.3 V logic |
| 20, 23, 24 | NC | |
| 21, 22 | HST_TX, HST_RX | firmware upgrade, unused |

Current: receive under 165 mA; transmit at 5 V about 700 mA digital, up to about 1 A analogue; at 8 V 5 W analogue 1.7 A.

## 2. AIOC K1 breakout to module

The AIOC's radio side is a Kenwood K1 pair: a 2.5 mm plug (tip = MIC audio from the AIOC, sleeve = PTT, pulled to ground by the AIOC's transistor) and a 3.5 mm plug (tip = speaker audio into the AIOC, ring = the radio's data-out line, which on hardware 1.2 is the AIOC's logic input, sleeve = ground). Before soldering, confirm each contact of the K1 breakout with continuity from the plug contacts; cheap breakouts have swapped rings.

| From (AIOC K1 breakout) | To (module pad) | Note |
|---|---|---|
| 2.5 mm tip, MIC | 14 MIC+ | direct; the module biases the line as a radio would; keep the lead short |
| 2.5 mm sleeve, PTT | 5 PTT | direct; AIOC sinks to ground, module keys on low |
| 3.5 mm tip, SPK | 6 LINE_OUT | direct; line level is lower than a K5 speaker, so expect to raise gain in step 5 |
| 3.5 mm sleeve, GND | 2 GND | one explicit ground wire; the USB ground of the AIOC is the Pi's ground |
| 3.5 mm ring, data in | 16 SPKEN | optional, step 6 only; 3.3 V, never with the module on 8 V logic elsewhere |
| supply + | 1 VCC | 5 V, 2 A capable, with the ground in the same lead pair |
| supply - | 4 GND | |
| (nothing) | 13 MIC- | to GND at the module |

Checks with the multimeter before the AIOC is plugged into the kit: no continuity between VCC and GND; PTT line reads high (module pull-up) with the AIOC unplugged; supply reads 5.0 V unloaded.

## 3. Module configuration, once

1. Rotary knob to the channel that will carry 144.800 (channel 8 is the first analogue channel in the default table), DIP switch on NORMAL.
2. Serial path: the module's USB-C into the Pi if it enumerates (`lsusb`, then `ls /dev/ttyUSB*`), else a 3.3 V USB-UART dongle on pads 18 (TXD from the module) and 19 (RXD into the module), grounds common. 57600 8N1.
3. On the kit, from the repo checkout or a copy of the script:
   - `python3 scripts/dmr858m-config.py --port /dev/ttyUSBx firmware` must return a version string. If not: swap TXD/RXD, then try `--endian be`.
   - `python3 scripts/dmr858m-config.py --port /dev/ttyUSBx aprs` sets channel 8 to 144.800 MHz simplex, CTCSS off, squelch 1, low power (each command prints its status byte; `0` is OK).
   - `python3 scripts/dmr858m-config.py --port /dev/ttyUSBx freq` reads the frequency back and prints it decoded both ways; the reading that says 144800000 Hz names the byte order. If that is `be`, repeat `aprs` with `--endian be`.
   - `status` and `rssi` for a sanity read.
4. Unplug the configuration serial before the RF tests; the module keeps its settings.

## 4. First RF test, module alone

1. Antenna on the SMA. Module on 5 V. Kit's spectrum page open on `/spectrum`, APRS band.
2. Press the module's own PTT button for two seconds: a carrier at 144.800 appears on the waterfall; the supply current stays under 1.2 A; nothing resets on the kit (X1202 AC-loss count unchanged, `journalctl -u x1202-monitor --since -10min | grep -c "AC power lost"` = 0).
3. If the carrier is off-frequency, redo step 3.

## 5. Receive through the AIOC

1. Plug the AIOC into the kit (its hub port 2). The bridge's APRS gateway starts Direwolf; `docker logs --since 2m meshsat | grep -a "direwolf-preflight"` shows the ALSA test passed and the capture gain applied.
2. Let the other kit beacon (it does every 30 s by itself). Within a minute: `curl -s localhost:6050/api/aprs/status` shows `last_decode_at` moving and `receive_state: ok`.
3. Read the decode level: `docker logs --since 2m meshsat | grep -a "audio level ="`. Target 40 to 90 (Direwolf's own guidance is about 50).
   - Read the AIOC's own gains first: `sudo ~/aioc-util/venv/bin/python ~/aioc-util/aioc-util.py --audio-get-settings`. Too low (line output is quieter than a K5 speaker): raise `MESHSAT_AIOC_CAPTURE` in `/srv/meshsat/.env` toward 100 % and restart the gateway (`POST /api/gateways/aprs/stop` then `/start`); still low: `sudo ~/aioc-util/venv/bin/python ~/aioc-util/aioc-util.py --audio-rx-gain 2 --store` (firmware 1.4 gain, 1x to 16x), restart the gateway.
   - Too high (over 150, "Audio input level is too high" in the log): lower `MESHSAT_AIOC_CAPTURE` (94 % = -5.8 dB, 90 % = -9.6 dB, 85 % = -14 dB).
4. Record the final gain in the kit's `.env` and here.

## 6. Optional: SPKEN carrier-detect into the AIOC

Only after step 5 passes. Wire pad 16 to the 3.5 mm ring, then `--enable-hwcos --store` on the AIOC. Direwolf does not use it (its DCD is software), so today it changes nothing; it is there for a later bridge-side receive-health reader. Skip it on the first day.

## 7. Transmit through the AIOC, one direction at a time

1. From the OTHER kit: `curl -s -X POST localhost:6050/api/oob/send -H 'Content-Type: application/json' -d '{"peer_id":9631,"via":"aprs_0","cmd":"PING"}'`. Wait for the round trip (the reply arrives in the sender's `GET /api/oob/log?limit=2` as `kind: reply`, `result: ok`, typically within a minute).
2. On the receiving side of our transmission (the other kit), read the level it decoded us at: `docker logs --since 2m meshsat | grep -a "audio level ="`. Target 40 to 90. Too low: `--audio-tx-boost` on our AIOC (firmware 1.4), or the module's mic gain command in `dmr858m-config.py raw`. Too high (distorted, failed decodes): lower the AIOC transmit level.
3. Then the reverse direction. Never both at once: the two kits share 144.800 and a collision looks like a dead receiver.
4. Supply current during the reply burst stays under 1.2 A at 5 V; the heatsink is warm, not hot, after ten bursts.

## 8. Mount and close

1. Module on the plate with M3 standoffs in its two 3 mm holes, heatsink clear of everything, SMA pigtail to the VHF bulkhead, NA-771 outside.
2. Ferrite on both ends of the AIOC's USB lead when they are found (RF on that lead is the documented cause of latched PTT and USB drops).
3. Case closed: repeat step 7 in both directions. Both pass = the K5 goes to the pouch as fallback.

## 9. Soak before the go/no-go

24 h with the peer beaconing: `docker logs --since 10m meshsat 2>&1 | grep -ac "audio level"` must never read 0; `GET /api/aprs/status` `receive_state` stays `ok` (it reads `quiet` after a bridge restart until the first frame arrives, and the watchdog only escalates once it has heard the peer in this bridge lifetime); no `aprs_rx_deaf` events in the audit. Then the forced-restart series from MESHSAT-814 (20 bridge restarts, decodes within 2 minutes after each).

## 10. Rollback

K5 and its AIOC cable from the pouch onto the AIOC's K1 plugs, `MESHSAT_AIOC_CAPTURE` back to the K5 value (parallax 90 %, tesseract default), gateway restart. Nothing else changes.

## 11. Record

On MESHSAT-748: module firmware string, endianness that worked, final capture and transmit gains per kit, supply current on transmit, decode levels both ways, heatsink temperature after the burst series. Update memory `project_aprs_module_replacement.md`.
