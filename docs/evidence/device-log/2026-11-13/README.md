# Device reliability log, 13 November 2026

SIDN fonds project 261042, deliverable D4: every time a radio or modem on the two MeshSat field kits locked up, which device it was, when, what cleared it and whether the case had to be opened, published with the kits' powered hours. A wedge is a device that stops answering until something resets it.

**Correction, 13 November 2026:** The first export of 13 November 2026 counted 65 PicoAPRS receive silences as cleared by hand with a press of the radio's PTT button, the case opened. The kits logged when each silence began, not how it ended, and the PTT button was pressed 2 or 3 times in that period. Those silences now show "end not logged"; the totals for "cleared by hand" and "case opened" went down by the same number.

## Result

|  | tesseract | parallax |
| --- | ---: | ---: |
| Wedges recorded automatically | 294 | 286 |
| ... the watchdog's own software step | 174 | 180 |
| ... came back by itself | 47 | 36 |
| ... cleared by hand | 2 | 4 |
| ... came back with no software step that acted (a hand, a restart or the device itself) | 4 | 6 |
| ... never recovered; the device was taken out of the kit | 2 | 2 |
| ... end not logged | 64 | 57 |
| ... still open when the log was read | 1 | 1 |
| Case opened | 2 | 5 |
| Case not opened | 221 | 215 |
| Case opened or not: not recorded | 68 | 63 |
| Reboots the mesh radio reported (logged since 13 November 2026) | 0 | 0 |
| Powered hours since 13 September 2026 | 818 | 819 |
| Automatic log starts | 2026-09-05T21:38:20Z | 2026-09-05T21:38:20Z |

Read from the kits on 2026-11-13T08:05:36Z. Every wedge, with its steps and the audit rows that prove it, is in `wedges.csv` and `wedges.json`; the rows themselves are in `events.jsonl`.

## By device, both kits

| Device | Wedges | Software step | By itself | By hand | No step acted | Restart | Taken out | End not logged | Still open | Longest (hours) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| APRS radio | 219 | 106 | 11 | 0 | 0 | 0 | 0 | 100 | 2 | 50.5 |
| ZigBee coordinator | 148 | 66 | 70 | 0 | 10 | 0 | 0 | 2 | 0 | 3.9 |
| RTL-SDR receiver | 120 | 101 | 1 | 5 | 0 | 0 | 2 | 11 | 0 | 47.9 |
| mesh radio | 69 | 67 | 1 | 0 | 0 | 0 | 0 | 1 | 0 | 2.8 |
| cellular modem | 16 | 14 | 0 | 1 | 0 | 0 | 0 | 1 | 0 | 0.9 |
| Iridium 9704 (IMT) | 5 | 0 | 0 | 0 | 0 | 0 | 2 | 3 | 0 | 28.1 |
| GPS receiver | 2 | 0 | 0 | 0 | 0 | 0 | 0 | 2 | 0 | 0.0 |
| Iridium 9602/9603 (SBD) | 1 | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 | 0.0 |

18 of the APRS receive silences began on both kits within two minutes of each other. The APRS watchdog reads a silence as frames that stopped while the channel was busy; when both kits report one together, the cause is more likely on the air than in either radio, so those rows say more about the channel than about the hardware.

## How the kits record it

- **The device-health watchdog** in the Bridge probes every USB device at protocol level every 30 seconds. Three misses in a row (one, for a radio that went silent the moment its port opened) start a ladder of recovery steps: reconnect, the device's own reset, a cut of its USB hub port. One step runs per grace window, hard resets are budgeted per hour, and a ladder that runs out waits an hour before it starts again.
- **The APRS receive watchdog** does the same for the APRS radio: a receiver that stops decoding while the channel is busy gets its gateway restarted, its port reopened and, last, the bridge restarted. A PicoAPRS that is switched off cannot be reset by software; it is reported as silent.
- **Every row goes into the kit's audit log**, where each row carries the SHA-256 of the row before it, so a changed or removed row breaks the chain.
- **What software cannot see is added by hand on the kit**, in the Bridge web app (Interfaces, Devices tab): a fix done by hand and whether the case had to be opened, and a device taken out of the kit or put back. These go into the same signed log.

## How the log is read

`scripts/device-log/` in this repository reads each kit's device rows and folds them into one record per wedge. The rules, tested in `test_fold.py`:

- A wedge starts when the watchdog calls a device unhealthy and ends when it answers again. The hourly restarts of a ladder that ran out, and a silent radio reported again after each restart, belong to the same wedge.
- **What cleared it.** A software step is credited only when the device answered within that step's grace window, about three minutes at most. A step whose own error says the thing it acts on was missing (no switchable hub port, no such device) is never credited.
- **By itself.** A device that answered again within 5 minutes with no step that acted, or an APRS receiver that decoded a frame again with no reset in between, is put down as having come back by itself, case not opened. This is an inference and is marked as one.
- **Case opened.** A software step never needs the case opened. A press of the PicoAPRS's PTT button does, since the radio sits inside the sealed case; it counts only where a note from the kit or an answer in `history.toml` records it. Anything else is "not recorded".
- **Taken out.** A device taken out of the kit is not wedged: its rows after the removal are left out, and a wedge open at the removal is closed by it.
- **Older rows.** Before 13 November 2026 the watchdog did not log a device that left the USB bus while wedged, the end of a PicoAPRS silence, or its own restarts. For those rows the reading is an inference, marked in the `inferred` column.

## Devices taken out or swapped

| Date | Kit | Device | Change | Note | Source |
| --- | --- | --- | --- | --- | --- |
| 2026-09-18 | both | mesh radio | fitted | XIAO ESP32-S3 replaced by a LILYGO T-Echo | MESHSAT-1112 |
| 2026-09-20 | both | ZigBee coordinator | removed | taken out when the kits were rebuilt | kit notes, 20 Sep 2026 |
| 2026-09-20 | both | GPS receiver | removed | taken out when the kits were rebuilt | kit notes, 20 Sep 2026 |
| 2026-10-17 | tesseract | Iridium 9704 (IMT) | removed | Iridium 9704 moved to the compact kit | MESHSAT-1620 |
| 2026-10-17 | both | RTL-SDR receiver | removed | no RTL-SDR on either kit since | MESHSAT-1590 |
| 2026-10-23 | parallax | Iridium 9704 (IMT) | removed | Iridium 9704 replaced by a RockBLOCK Mk2 (9602) | MESHSAT-1383 |
| 2026-10-23 | both | Iridium 9602/9603 (SBD) | fitted | RockBLOCK Mk2 (9602, SBD) fitted | MESHSAT-1383 |

## Before automatic logging, and devices the watchdog does not watch

From the project's own notes, not recorded by the kits. "Inferred" means the notes do not say whether the case was opened and the answer follows from where the device sits.

| Date | Kit | Device | What happened | What cleared it | Case opened | Kind | Source |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-04-18 | parallax | cellular modem (A7670E) | power dips on every transmission, modem stopped answering | Pi bootloader power limit raised, then a reboot | not recorded | wedge | MESHSAT-523 |
| 2026-04-21 | both | USB WiFi adapter (MT7921U) | dropped off USB under load | replaced with an MT7612U | yes (inferred: a swap) | bench | kit notes |
| 2026-04-21 | both | touch display | black after a kernel update | kernel rolled back and pinned | no | setup | kit notes |
| 2026-04-22 | parallax | RTL-SDR receiver | USB busy after scans were killed | not cleared that day; the kits were switched off | not recorded | bench | MESHSAT-656 |
| 2026-04-24 | both | touch display | black | UPS board power button, 30 s off | not recorded | wedge | kit notes |
| 2026-04-24 | both | mesh radio (XIAO ESP32-S3) | serial port silent | power cycle of the kit | not recorded | wedge | MESHSAT-685 |
| 2026-09-02 | parallax | cellular modem (A7670E) | command channel blocked by a network query we sent | not recorded; answering again by 3 Sep evening | not recorded | self-inflicted | kit notes |
| 2026-09-02 | both | APRS radio (UV-K5) | receiver deaf, four times between 2 and 5 Sep (39 and 28 minutes on 5 Sep) | came back by itself | no | wedge | MESHSAT-814 |
| 2026-09-03 | parallax | mesh radio (XIAO ESP32-S3) | wedged through a warm reboot and three restarts of the bridge | power cycle of the kit by hand, 4 Sep | yes (inferred: the kit had no outside power button) | wedge | MESHSAT-781 |
| 2026-09-04 | both | UPS board (X1202) and audio interface (AIOC) | 202 power-loss events, a reboot on parallax, the AIOC off USB on tesseract, both packs running empty | cells taken out, load reduced, charger wired to the board | yes | wiring | MESHSAT-793 |
| 2026-09-05 | parallax | mesh radio (XIAO ESP32-S3) | wedged | the watchdog's hub-port cut, 3.5 minutes, before its rows went to the audit log | no | wedge | MESHSAT-817 |
| 2026-09-06 | both | touch display | black while the kiosk ran | UPS board power cycle | yes (inferred: the kit had no outside power button) | wedge (not watched) | MESHSAT-824 |
| 2026-09-11 | both | APRS radio (PicoAPRS) | switched off after 12 hours without USB power (empty cell) | PTT held for 3 s | yes (inferred: the radio sits inside the case) | wedge (not watched then) | MESHSAT-1028 |
| 2026-09-13 | both | touch display | dead after a warm reboot | cold power cycle, then the display overlay pinned | yes (inferred: the kit had no outside power button) | setup | MESHSAT-1091 |
| 2026-09-15 | parallax | touch display | display controller hung after a warm reboot | power off and on with the button | yes (inferred: the kit had no outside power button) | wedge (not watched) | kit notes |
| 2026-09-21 | both | touch display | panels dim with everything set to full | the panel's 5 V lead moved to its own header pin | yes | wiring | kit notes |

## Powered hours

- **tesseract:** 818 hours over 24 boots.
- **parallax:** 819 hours over 24 boots.

Each boot counts from the kit's own uptime counter at its last journal entry, so a wrong wall clock at boot (the kits had no clock cell until they were fitted this autumn) changes nothing. The system journal on the kits goes back to 13 September 2026; time powered before that was not recorded. From 13 November 2026 the Bridge also keeps its own count, every five minutes, for boots the journal no longer holds.

## Chain check

- **tesseract:** 9,372 rows, ids 1 to 9372 with 0 missing, 0 broken links, 0 hashes that do not recompute. 4 rows (ids 57, 58, 59, 60) hold bytes that are not text, from a relayed binary frame; the API shows them with replacement characters, so their hashes can only be recomputed on the kit. The kit's own check of the whole chain (9,372 rows, hashes and links) passes.
- **parallax:** 9,878 rows, ids 1 to 9878 with 0 missing, 0 broken links, 0 hashes that do not recompute. The kit's own check of the whole chain (9,878 rows, hashes and links) passes.

The published `events.jsonl` holds the device rows only, with their hashes; the other rows of the audit log record messages and stay on the kits.

## Limits

- The touch display is not watched; its hangs are in the notes table only.
- A PicoAPRS that ran its cell empty still enumerates on USB and is caught only as a silent TNC once its link is open.
- Before 18 September 2026 the StarTech hubs' port "cut" was taken for a power cut; it is a data reconnect, so a step of that name did not remove power.
- The kits run as development and demonstration kits: wedges during bench work are in the log like any other.
- Durations run from the first to the last logged moment and include time the kit was switched off.

## Files

- `wedges.csv`, `wedges.json`: one row per wedge, automatic and from the notes.
- `events.jsonl`: every device row of both kits' audit logs, redacted (numbers, addresses and node ids masked).
- `power.json`: powered time per boot.
- `../history.toml`: what the kits could not record, from the project's notes.

This export was generated by `scripts/device-log/export.py` and is not edited by hand.
