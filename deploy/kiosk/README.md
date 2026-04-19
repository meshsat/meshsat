# MeshSat Kiosk — Pi 5 + touchscreen provisioning

One-shot kiosk setup that boots a Pi 5 straight into the bridge UI
fullscreen. Works with:

- Any HDMI monitor / touchscreen (no extra config).
- **Raspberry Pi Touch Display 2** (SC1635 — DSI, 720×1280 native
  portrait, capacitive multitouch). One line in
  `/boot/firmware/config.txt` to enable the DSI driver; the kiosk
  launcher auto-rotates to landscape via a Wayland output transform.
  See **Raspberry Pi Touch Display 2** below.
- The original 7" Pi Touch Display (SC0175 — 800×480). No config
  changes on current Ubuntu 24.04 images; `KIOSK_ROTATE=normal` to
  keep native landscape.

## What it gives you

| Component | Package | Role |
|---|---|---|
| `cage` | Ubuntu main | Minimal Wayland compositor — one app, fullscreen, no desktop. |
| `chromium-browser` | Ubuntu main | The kiosk shell. Launched in `--kiosk --app=…` mode. |
| `swayidle` | Ubuntu main | Idle detector. Calls `/api/system/backlight` at 2 min (dim) / 5 min (off); full brightness on any input. |

> **Physical wiring note for the bundled 2-wire power lead.** The
> Touch Display 2 ships with a tiny GPIO cable that clips onto a
> 5V + GND pair. On the field kits **pin 2 is already occupied by
> the satellite modem V_IN+**, so clip the lead onto **pin 4 (5V)
> + pin 14 (GND)** instead. Pin 4 is the same 5V rail internally
> — no electrical change — just routing round the modem's
> connector.
| Autologin on tty1 | stock `getty@tty1` drop-in | Pi boots to the `kiosk` user with no password prompt. |
| Chromium managed policy | `/etc/chromium/policies/managed/meshsat-kiosk.json` | URL allowlist locked to `localhost`; devtools, password manager, autofill, history, printing all off. Operator can't break out of the app. |

Zero third-party repos. All stock Ubuntu 24.04 packages.

## Install

```bash
cd /srv/meshsat   # wherever you've cloned the repo on the Pi
sudo bash scripts/install-kiosk.sh
sudo reboot
```

After reboot the Pi auto-logs in as `kiosk` on tty1 and launches
Chromium fullscreen at `http://localhost:6050/?shell=kiosk`. The
`?shell=kiosk` query forces **Operator Mode** on first load
(persisted to localStorage, so subsequent loads keep it).

SSH keeps working. Other TTYs (Ctrl+Alt+F2…F6) still give a normal
login shell — only tty1 is the kiosk.

## Point at a different bridge

Default is `http://localhost:6050/?shell=kiosk`. Override with
`BRIDGE_URL`:

```bash
sudo BRIDGE_URL=http://192.168.1.42:6050/?shell=kiosk \
  bash scripts/install-kiosk.sh
```

Re-running the installer is safe (idempotent) — the autologin
drop-in, launcher, and policy file all get rewritten.

## Raspberry Pi Touch Display 2 (720×1280, new DSI panel)

The Touch Display 2 (SC1635, product code KW-3379) is the 2024
panel with **720×1280 native portrait** resolution — it's NOT the
original 800×480 7" display.

### 1. Enable the DSI panel at boot

Append to `/boot/firmware/config.txt` under `[all]`:

```
dtoverlay=vc4-kms-v3d
dtoverlay=vc4-kms-dsi-generic
```

On recent Ubuntu 24.04 raspi kernels (6.8+) the panel is often
auto-detected and the second line may not be strictly necessary.
If the screen stays dark, re-check:

```
dmesg | grep -iE 'dsi|drm|panel'
```

A working boot emits a `[drm] Panel attached` line for DSI-1.

### 2. Landscape rotation

The panel's EDID reports portrait. The kiosk launcher exports
`WLR_OUTPUT_TRANSFORM=90` by default so cage rotates 90° clockwise
to landscape (1280 wide × 720 tall). If you want portrait instead
(some dashboards prefer it), override before install:

```
sudo KIOSK_ROTATE=normal bash scripts/install-kiosk.sh
```

Valid values: `normal` · `90` · `180` · `270`.

### 3. Touch calibration

Capacitive touch follows the rotation automatically under Wayland
(libinput handles the transform from the compositor). No separate
calibration step on Ubuntu 24.04 — that was only an X11 problem.

### Original 7" Touch Display (SC0175, 800×480)

No dtoverlay needed on Ubuntu 24.04. Install with
`KIOSK_ROTATE=normal` so the already-landscape panel isn't rotated
unnecessarily:

```
sudo KIOSK_ROTATE=normal bash scripts/install-kiosk.sh
```

## Backlight control

`swayidle` talks to `meshsat` via REST:

```
POST /api/system/backlight  { "value": 0–255 }
```

See `internal/api/system.go` (MESHSAT-556). The endpoint walks
`/sys/class/backlight/*`, picks the first device with a readable
`max_brightness`, and maps `value` onto that device's range. No
suid or sudoers entry needed — the bridge owns the sysfs write.

## Uninstall

```bash
sudo bash scripts/uninstall-kiosk.sh
sudo reboot
```

Restores the stock getty, removes the Chromium policy, strips the
kiosk user's `.bash_profile` hook. Packages stay installed (safer
— removing Chromium breaks anything else that pulls it in).

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Pi boots to a normal login, no Chromium | Autologin drop-in missing. Check `systemctl cat getty@tty1` — should show the `--autologin kiosk` override. |
| Chromium starts but the page is blank | Bridge container isn't up. `docker ps` from tty2 / SSH. |
| Screen never dims | `swayidle` not running. Check `pgrep -fa swayidle` from SSH; the launcher starts it in the background at login. |
| Can't break out of the kiosk | That's the point. SSH in and run the uninstaller, or Ctrl+Alt+F2 for a shell. |
| URL allowlist blocks a legitimate site | Intentional — the policy locks Chromium to `localhost`. Edit `/etc/chromium/policies/managed/meshsat-kiosk.json` to widen it. |

## Related YouTrack issues

- MESHSAT-577 — Pi Touch Display 2 dtoverlay (see **DSI panel** above)
- MESHSAT-578 — dedicated kiosk user + autologin + Wayland session (this installer)
- MESHSAT-579 — systemd unit + Chromium policy lockdown (this installer)
- MESHSAT-580 — backlight dim via swayidle (this installer)
- MESHSAT-556 — the `/api/system/backlight` REST endpoint this depends on (Done)
- MESHSAT-549 — `?shell=kiosk` URL param → Operator Mode (Done)
