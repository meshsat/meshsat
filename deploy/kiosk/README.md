# MeshSat Kiosk — Pi 5 + Raspberry Pi Touch Display 2

One-shot kiosk setup that boots a Pi 5 straight into the bridge UI
fullscreen, in landscape, with touch input aligned to the rotation.

Tested against:

- **Raspberry Pi Touch Display 2** (SC1635, 720×1280 DSI portrait —
  rotated to 1280×720 landscape by the kiosk launcher). Goodix
  capacitive multitouch.
- Any HDMI monitor / touchscreen (no config changes beyond the
  installer — skip the DSI overlay bits).

## What gets installed

All stock Ubuntu 24.04 (`noble`) packages — zero third-party repos.

| Component | Package | Role |
|---|---|---|
| Wayland compositor | `labwc` (0.7+) | One fullscreen app, no desktop; implements `wlr_output_management_v1` so rotation works. |
| Kiosk shell | `chromium-browser` | `--kiosk --app=<bridge URL>` mode. |
| Output rotation | `wlr-randr` | Called from `~/.config/labwc/autostart` to rotate DSI-2 90° CW. |
| Touch rotation | `/etc/udev/rules.d/99-touch-rotate.rules` | Sets `LIBINPUT_CALIBRATION_MATRIX` on the Goodix touchscreen so input coords track the display rotation. |
| Idle backlight dim | `swayidle` | 3 min of no input → 32/255 (never on mains, MESHSAT-827), any input → 200/255, via the sudoers-scoped `meshsat-backlight` wrapper. |
| Autologin | stock `getty@tty1.service` drop-in | Pi boots straight into the `kiosk` user on the console. |
| Chromium lockdown | `/etc/chromium/policies/managed/meshsat-kiosk.json` | URL allowlist = localhost only; devtools, password manager, history, autofill, printing all disabled. |

## Install

```bash
sudo bash scripts/install-kiosk.sh
sudo poweroff
# pull + restore power
```

> **Do a cold boot, not `sudo reboot`.** Warm reboots can leave the
> DSI bridge chip on the Touch Display 2 in a deferred-probe loop
> where the panel driver never re-binds. A full power cycle resets
> the bridge chip cleanly.

On cold boot the Pi auto-logs in as `kiosk` on tty1 → labwc starts →
`autostart` rotates DSI-2 to 90° and launches Chromium fullscreen at
`http://localhost:6050/?shell=kiosk`. The `?shell=kiosk` query forces
Operator Mode on first load.

SSH + tty2…tty6 still give a normal login shell; only tty1 is the
kiosk.

## Point at a different bridge

```bash
sudo BRIDGE_URL=http://192.168.1.42:6050/?shell=kiosk \
  bash scripts/install-kiosk.sh
```

Re-running the installer is safe — the autologin drop-in, launcher,
labwc config, udev rule, and Chromium policy all rewrite cleanly.

## DSI panel enablement

The Pi Touch Display 2 (7 inch) overlay is `vc4-kms-dsi-ili9881-7inch`
(Raspberry Pi Touch Display 2 docs and the kernel overlays README).
`vc4-kms-dsi-7inch` is the overlay of the original 800×480 Touch
Display. Both kits carry these display lines in
`/boot/firmware/config.txt` (MESHSAT-1091):

```
[all]
dtoverlay=vc4-kms-v3d
dtoverlay=vc4-kms-dsi-generic
display_auto_detect=0

[pi5]
dtoverlay=vc4-kms-dsi-7inch
dtoverlay=vc4-kms-dsi-ili9881-7inch
display_lcd_rotate=2
```

**The Touch Display 2 overlay must be the last display overlay.** The
overlay applied last wins I2C address 0x45 and DSI device 0, so every
healthy boot logs `Failed to register i2c client 7inch-touchscreen-p at
0x45 (-16)` and two `failed to add DSI device -17`: those are the
losing Touch Display 1 and generic nodes, not a fault.

Why it is pinned: with only `display_auto_detect=1`, the firmware added
the Touch Display 2 nodes on a cold power-on but not after a warm
reboot, which left only the Touch Display 1 nodes and a black panel
until a power cycle (Ubuntu 6.8.0-1051's `rpi-panel-v2-regulator` also
lacks the shutdown hook Raspberry Pi added in May 2024). Pinned, the
panel survives a warm reboot. Removing `vc4-kms-dsi-7inch` or
`vc4-kms-dsi-generic` has not been tested with the pin in place; leave
them.

**Keep every `config.txt` line under 98 characters, comments included.**
Raspberry Pi documents a 98-character limit and the firmware does not
warn: tesseract's 105-byte `gpio=24=ip,pn  # MESHSAT-669 ...` line is
rejected at every boot (`Invalid gpio command` in the firmware log).
Read the firmware log before blaming hardware when a `config.txt` line
seems to have no effect.

## Touch calibration matrix

The udev rule in `99-touch-rotate.rules` sets
`LIBINPUT_CALIBRATION_MATRIX=0 -1 1 1 0 0`. That's 90° CCW on the
libinput side, which compensates for labwc's 90° CW output rotation
so finger-to-pixel mapping is consistent.

Swap to `0 1 0 -1 0 1` if you mount the panel the other way up (the
matrix is mechanical, not logical). The four rotations:

| Rotation | Matrix |
|---|---|
| Identity (no rotate) | `1 0 0 0 1 0` |
| 90° CW | `0 1 0 -1 0 1` |
| 180° | `-1 0 1 0 -1 1` |
| 90° CCW | `0 -1 1 1 0 0` ← installed default |

After editing the rule, reload with:

```bash
sudo udevadm control --reload-rules
sudo udevadm trigger --subsystem-match=input
sudo pkill -9 -f labwc            # session respawns via getty@tty1
```

## Backlight control

`swayidle` calls the bridge's REST endpoint — no suid or sudoers
entry on the kiosk user is needed:

```
POST /api/system/backlight  { "value": 0–255 }
```

The handler walks `/sys/class/backlight/*`, picks the first device
with a readable `max_brightness`, and scales the 0–255 input onto
that device's range. On Touch Display 2 the sysfs device is
`11-0045` (Goodix controller, DSI channel 11) with
`max_brightness=31`, so `value=255` → 31 (full), `value=0` → 0 (off).

## Uninstall

```bash
sudo bash scripts/uninstall-kiosk.sh
sudo poweroff
```

Reverts the autologin, Chromium policy, udev rule, labwc config,
and the kiosk user's `.bash_profile` hook. Packages stay installed
(safer — removing Chromium breaks anything else that pulls it in).

## Troubleshooting

| Symptom | Fix |
|---|---|
| Screen stays dark after cold boot | Check `dmesg \| grep -i dsi` — should emit `rp1dsi_host_attach: Attach DSI device name=dsi-7inch channel=0 lanes=2` and `[drm] Panel attached`. If not, reseat the DSI FPC ribbon and cold-boot again. |
| Display up but upside-down or sideways | Edit `wlr-randr --output DSI-2 --transform 90` in `~/.config/labwc/autostart` (try 0 / 90 / 180 / 270). |
| Touch off by 90° | Swap the matrix in `/etc/udev/rules.d/99-touch-rotate.rules` per the table above; reload + restart tty1. |
| Touch off diagonally / mirrored | Try `flipped-90` style matrices; bracket with the 4 mirrored variants: `0 1 0 1 0 0`, `0 -1 1 -1 0 1`, `-1 0 1 0 1 0`, `1 0 0 0 -1 1`. |
| Chromium opens but shows "site can't be reached" | The bridge container wasn't ready when Chromium first loaded. `sudo pkill -9 -f chromium` — labwc respawns it; or wait ~30 s for the container healthcheck to flip green and reload. |
| Chromium crashed with apport core | labwc respawns on exit; if persistent, check `/home/kiosk/snap/chromium/common/chromium/Crash Reports`. |
| Touch stopped responding | Cold-boot. Warm reboots can wedge the Goodix controller along with the DSI bridge. |

## Related YouTrack

- MESHSAT-577 — DSI panel provisioning (this README's hardware notes)
- MESHSAT-578 — kiosk user + autologin + Wayland session (this installer)
- MESHSAT-579 — systemd unit + Chromium policy lockdown (this installer)
- MESHSAT-580 — backlight dim via swayidle (this installer)
- MESHSAT-556 — `/api/system/backlight` REST endpoint (Done)
- MESHSAT-549 — `?shell=kiosk` → Operator Mode (Done)
