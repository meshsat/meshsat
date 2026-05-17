# ADR-0013 — Pi 5 field-kit hardware contract: EEPROM, UART, USB VID:PID auto-detect, two-kit fleet

* Status: Accepted — codified after the fact 2026-05-17. Each constituent rule is in place on the active fleet (`tesseract01` + `parallax01`). This ADR consolidates them as a single citable contract so future field-kit provisioning automation can reference one source.
* Date: Rules accumulated 2026-03 (initial Pi 5 onboarding) through 2026-04-18 (MESHSAT-523 EEPROM fix). ADR recorded 2026-05-17.
* Deciders: `ufwtqkgz@meshsat.net`
* Source: `constitution.md` Article X + `.claude/rules/ecosystem-fleet.md` L42–L66 + `.claude/rules/transport-protocols.md` (per-modem rules)

## Context

The Bridge runs on Raspberry Pi 5 (8 GB) inside Pelican-cased field kits. Pi 5 has specific quirks that, if not handled, brick a fresh deployment:

1. **PSU_MAX_CURRENT cap**: Default EEPROM caps total USB-A bus current at 600 mA. A field kit pulls ~1.5 A across LTE modem + GPS + Meshtastic + ZigBee + RockBLOCK — the modem brown-outs on every RF burst (`AT check failed after 30s` loop). Bricked the parallax01 onboarding 2026-04-18 (MESHSAT-523).
2. **UART boot console garbage**: With `console=serial0,115200` in `cmdline.txt`, the kernel reads garbage on `/dev/ttyAMA0` at boot — the RockBLOCK 9704's TXD is undefined at power-on, garbage matches "panic" patterns, kernel halts. `gpio=` does NOT work on Pi 5 (RP1 is PCIe).
3. **USB device identity is dynamic**: ESP32-S3 mesh radios, A7670E cellular modems, RockBLOCK 9603/9704 satellites, CC2652P ZigBee dongles all share USB-serial chipsets (CH340, CP2102, FTDI). Pinning ports by `/dev/ttyUSBN` is fragile across reboots.
4. **GPIO sysfs is read-only in containers**: The bridge mounts `/sys:/sys:ro`. Sysfs GPIO writes silently fail. libgpiod chardev (`/dev/gpiochip*`) works.
5. **Two field kits, 99% identical**: `tesseract01` (SBD 9603) and `parallax01` (IMT 9704) differ only in satellite modem family. Hardware drift between kits = operator confusion.

## Decision

**A single hardware contract** all field-kit Pi 5s MUST satisfy. Provisioning automation MUST enforce all five clauses; field-kit-onboarding is incomplete until they're verified.

### Clause 1 — EEPROM `PSU_MAX_CURRENT=5000` (MESHSAT-523)
The bootloader EEPROM MUST contain:
```
BOOT_UART=1
POWER_OFF_ON_HALT=1
BOOT_ORDER=0xf41
PSU_MAX_CURRENT=5000
PCIE_PROBE=1
```
Apply with `sudo rpi-eeprom-config --apply <config>` + reboot. Confirm with `vcgencmd get_config usb_max_current_enable` returning `1`. Without this, A7670E + RockBLOCK 9704 + WiFi + ZigBee draws > 600 mA and the bus power-collapses on RF burst.

### Clause 2 — UART boot fix
`/boot/firmware/cmdline.txt` MUST NOT contain `console=serial0,115200`. `/boot/firmware/config.txt` MUST contain `enable_uart=0`. Without this, the RockBLOCK 9704's undefined TXD at boot produces garbage on the kernel console and panics the boot.

### Clause 3 — USB VID:PID auto-detection cascade
`MESHSAT_*_PORT=auto` (default) MUST trigger `DeviceSupervisor`'s VID:PID identification cascade + protocol probing (Meshtastic protobuf for ESP32-S3, AT commands for cellular + Iridium SBD, JSPR for 9704 IMT, ZNP for ZigBee). Manual pinning (`MESHSAT_*_PORT=/dev/ttyUSBN`) is supported but discouraged — pinning across reboots is fragile when 5+ USB-serial devices share a hub.

**Known VID:PID collision**: SONOFF ZigBee dongle shares VID:PID with some Meshtastic devices. If ZigBee is plugged in, operator MUST pin `MESHSAT_ZIGBEE_PORT=/dev/ttyUSBN` to disambiguate.

### Clause 4 — GPIO via libgpiod chardev
GPIO access MUST go through `github.com/warthog618/go-gpiocdev` (chardev `/dev/gpiochip*`), NOT sysfs. The container mounts `/sys:/sys:ro` so sysfs GPIO is read-only and silently fails. Pi 5 gpiochip bases start at 512 (RP1 is PCIe-attached).

### Clause 5 — Two-kit fleet identity
The active fleet is exactly two kits:

| Kit | Codename | Satellite modem | UART baud |
|---|---|---|---|
| Kit A | `tesseract01` | RockBLOCK 9603 (SBD) | 19200 |
| Kit B | `parallax01` | RockBLOCK 9704 (IMT) | 230400 |

Both: Pi 5 8 GB + active cooler + Geekworm X1202 UPS (50 Wh) + LilyGO T-Call A7670E + USB GPS u-blox + Quansheng UV-K5(8) + Nagoya NA-771 + AIOC v1.2 + ESP32-S3 LoRa + RTL-SDR v4 + ZigBee CC2652P + Tuya ZigBee Temp/Humidity Sensor IP65 + Sabrent HB-UM43 hub + IP67 case + TAOGLAS Iridium antenna + KPN SIM + DCF77 receiver + WeAct 3.7" e-paper + ANENG multimeter combo + Ubuntu Server 24.04. Combined with 2 Android phones + Hub = ~99% MeshSat demo coverage.

**Previously-active kits `mule01` + `rocket01` are DECOMMISSIONED as of 2026-04-04.** Don't expand the active fleet without explicit operator decision.

## Consequences

**Positive**
- A fresh field-kit provisioning automation can reference one ADR and one set of clauses — no scattered "remember to also do X" gotchas.
- The two-kit fleet is small enough to reason about completely; expansion is a deliberate operator act, not drift.
- Phase 7 kiosk provisioning (`ansible/playbooks/field-kit.yml`) lands on top of this contract — display + labwc + Chromium ride on a known-good base.

**Negative**
- Five separate clauses to verify on every kit deployment. Mitigation: an end-to-end smoketest playbook (under construction in Phase 7) gates all five.
- The EEPROM clause requires root + reboot — can't fix retrospectively without operator action on the physical kit. Mitigation: provisioning script bakes the EEPROM at first boot, before any container is started.
- Pi Touch Display 2 (Phase 7) requires `dtoverlay=vc4-kms-v3d` + `dtoverlay=vc4-kms-dsi-ili9881-7inch,rotation=90` in `config.txt` — that's a Phase 7 addition to this contract.

**Forward direction**
- The kit BOM is in scope of two operator-facing docs: `docs/hardware/MeshSat-Field-Kit-BOM.docx` (procurement) and `docs/hardware/MeshSat-Field-Kit-GPIO-Pinout.docx` (wiring). Keep this ADR for the operational contract; let those docs serve the procurement + assembly steps.
- A third field kit (e.g. an HF radio variant) would be a new clause + new BOM doc, but the EEPROM/UART/GPIO clauses transfer unchanged.

## Alternatives considered

- **Use Pi 4 instead of Pi 5**: rejected — no `PSU_MAX_CURRENT` cap problem, but Pi 4 lacks the CPU headroom for HeMB RLNC + Reticulum + 9 interfaces + SPA serving concurrently.
- **External powered USB hub eliminating EEPROM clause**: rejected — adds another component to the IP67 case and another failure mode; the EEPROM clause is one-time per kit.
- **Sysfs GPIO with privileged container + `/sys:/sys:rw`**: rejected — opens too much of the host to the container; chardev is the correct abstraction.
- **BananaPi BPI-M4 Zero (cheaper)**: tried, deprecated 2026-Q1 — Allwinner H618 USB is unreliable under sustained load; not recommended for field deployment.

## Compliance

- `ansible/playbooks/field-kit.yml` MUST enforce clauses 1+2+4 at provisioning time (clause 3 is auto-on by default, clause 5 is an operator-side decision).
- The "Pi 5 field kit smoketest" in Phase 7 closure MUST verify all five clauses end-to-end on a fresh image.
- Expanding the active fleet beyond `tesseract01` + `parallax01` requires an explicit operator decision + a new entry in `PROJECT.json#deployment_targets` + a memory entry recording the kit's hardware delta.
- Don't conflate this ADR with `docs/hardware/MeshSat-Field-Kit-BOM.docx` (procurement) or `docs/hardware/MeshSat-Field-Kit-GPIO-Pinout.docx` (wiring) — those serve different audiences and are operator-facing rather than agent-enforceable.
