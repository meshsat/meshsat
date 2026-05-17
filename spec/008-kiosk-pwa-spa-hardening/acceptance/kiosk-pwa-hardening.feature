Feature: Kiosk + PWA + SPA Hardening (meshsat Phase 7, EXECUTION-PLAN §6.7)

  Background:
    Given a Pi 5 with the Touch Display 2 attached via DSI1
    And the meshsat bridge container is running on the Pi

  # REQ-700 + REQ-701 — Pi Touch Display 2 provisioning
  Scenario: dtoverlay lines land + Pi presents labwc at 1280x720 landscape
    When the Ansible playbook field-kit.yml runs against a fresh Pi 5
    Then /boot/firmware/config.txt contains "dtoverlay=vc4-kms-v3d"
    And /boot/firmware/config.txt contains "dtoverlay=vc4-kms-dsi-ili9881-7inch,rotation=90"
    When the Pi reboots
    Then the display shows a labwc Wayland session at 1280x720 landscape

  # REQ-702 + REQ-703 — kiosk user + greetd autologin
  Scenario: kiosk user + greetd autologin in place
    When the playbook completes
    Then the kiosk user exists with /usr/sbin/nologin shell
    And `loginctl enable-linger kiosk` succeeded
    And /etc/greetd/config.toml contains initial_session command="labwc" user="kiosk"

  # REQ-704 + REQ-705 + REQ-706 — systemd unit + restart + healthz wait
  Scenario: meshsat-kiosk.service launches Chromium after bridge healthz
    Given the bridge is starting (healthz not yet 200)
    When the kiosk service starts
    Then the ExecStartPre polls /healthz until it returns 200
    And then Chromium launches in kiosk mode against http://localhost:6050/

  Scenario: Chromium crash restarts within 5 seconds
    Given Chromium is running
    When the Chromium process is killed
    Then within 5 seconds Chromium is running again (via systemd Restart=always RestartSec=5)

  # REQ-707 — policy lockdown
  Scenario: Chromium policy restricts URL allowlist
    Given the policy file at /etc/chromium/policies/managed/meshsat-lockdown.json is in place
    When the operator tries to navigate to https://example.com inside the kiosk
    Then the navigation is blocked
    And the URL allowlist contains exactly "http://localhost:6050/*"

  # REQ-708 + REQ-709 — backlight + sudo scope
  Scenario: backlight dims after 10 min idle
    Given the kiosk has been idle for 600 seconds
    Then /sys/class/backlight/10-0045/brightness is 32
    When the operator touches the screen
    Then within 1 second /sys/class/backlight/10-0045/brightness is 200

  # REQ-710 + REQ-711 + REQ-712 + REQ-714 — PWA
  Scenario: PWA installable on Chrome
    When the operator opens the SPA in desktop Chrome
    Then Chrome shows the "Install MeshSat" prompt
    And /manifest.json + /sw.js + icon files return 200 with correct MIME types

  Scenario: Service worker /sw/reset unregisters
    Given a broken service worker is registered
    When the operator visits /sw/reset
    Then the service worker is unregistered

  # REQ-715 + REQ-716 — OSK
  Scenario: OSK auto-shows on coarse-pointer + no hardware keyboard
    Given the SPA is loaded on a touch device with no hardware keyboard
    When the operator taps a text input
    Then the simple-keyboard OSK is visible

  Scenario: OSK respects inputmode=numeric
    Given the operator taps an input with inputmode="numeric"
    Then the OSK renders the numeric-pad layout

  # REQ-717 — touch widget reorder
  Scenario: DashboardView widgets reorder via touch
    When the operator drags a widget via touch (touchstart → touchmove → touchend)
    Then the widget reorders in the layout

  # REQ-718 + REQ-719 — overflow + modal fitness
  Scenario: Settings tabs scroll horizontally at 720 wide
    Given viewport width=720
    When the operator opens Settings
    Then the 17-tab strip is horizontally scrollable with snap-x
    And a fade-on-right indicator appears when not all tabs fit

  Scenario: Modal fits 720-wide viewport
    Given viewport width=720
    When a Dashboard modal opens
    Then the modal renders at max-w-full (not max-w-2xl)

  # REQ-722 + REQ-723 — tap-target floor
  Scenario: Primary buttons meet 48dp minimum
    When the operator measures any primary action button
    Then the button has h-12 (48px) minimum height

  Scenario: Interfaces rule-form checkboxes are 40px tap targets
    When the operator views the InterfacesView rule form
    Then every checkbox is wrapped in a 40px-tall clickable label

  # REQ-724 — responsive grid
  Scenario: Dashboard grid single-column at 400px
    Given viewport width=400
    When the operator opens Dashboard
    Then the widget grid renders as 1 column

  # REQ-725 + REQ-726 — input semantics + textarea defaults
  Scenario: Phone input declares inputmode=tel
    When the operator inspects a phone input field
    Then the input element has inputmode="tel"

  Scenario: Textareas default to rows=2 with resize-y sm:resize-none
    When the operator views any SPA textarea
    Then it defaults to 2 rows and resize-y on mobile, resize-none on sm+

  # REQ-728 — pull-to-refresh
  Scenario: Pull-to-refresh on Inbox triggers fetch
    Given the operator is on the Inbox view
    When the operator pulls down at the top of the list past 60px
    Then a fetch reloads the inbox

  # REQ-729 — EEPROM + UART boot verify
  Scenario: Onboarding refuses to proceed without PSU_MAX_CURRENT=5000
    Given a Pi 5 with default EEPROM (no PSU_MAX_CURRENT=5000)
    When the field-kit-onboard.sh script runs
    Then the script exits non-zero with an error citing Constitution Article X

  # REQ-730 — nightly restart
  Scenario: Kiosk auto-restarts nightly at 03:00
    Given the meshsat-kiosk.service has been running for 24 hours
    When local time hits 03:00
    Then a systemctl --user restart fires
    And within 30 seconds Chromium is running again
