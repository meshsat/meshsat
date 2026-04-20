package api

// Shared shell-exec + input-validator helpers for the system-management
// handlers (bluetooth, WiFi). Ported from cubeos/hal/internal/handlers —
// field kits run meshsat standalone without HAL so the bridge grows its
// own host-management surface. The HAL side of this code is battle-tested;
// ports preserve the validation + timeout patterns verbatim so we inherit
// its security posture.
//
// Host tooling required inside the container for these handlers to work:
//   bluetoothctl + rfkill (bluez, util-linux), iw (iw), wpa_cli
//   (wpasupplicant), nsenter (util-linux), iproute2. Plus compose-level
//   `pid: host` + `cap_add: [SYS_ADMIN, NET_ADMIN]` + a bind-mount of
//   /var/run/dbus so BlueZ is reachable from inside the container.
//
// [MESHSAT-623, MESHSAT-624]

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ── regex (compiled once) ─────────────────────────────────────────────

var (
	reInterfaceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,14}$`)
	reSSID          = regexp.MustCompile(`^[^\x00]{1,32}$`)
)

// ── validators ────────────────────────────────────────────────────────

func validateInterfaceName(name string) error {
	if name == "" {
		return fmt.Errorf("interface name is required")
	}
	if !reInterfaceName.MatchString(name) {
		return fmt.Errorf("invalid interface name")
	}
	return nil
}

func validateMACAddress(mac string) error {
	if mac == "" {
		return fmt.Errorf("MAC address is required")
	}
	if _, err := net.ParseMAC(mac); err != nil {
		return fmt.Errorf("invalid MAC address")
	}
	return nil
}

func validateSSID(ssid string) error {
	if ssid == "" {
		return fmt.Errorf("SSID is required")
	}
	if len(ssid) > 32 {
		return fmt.Errorf("SSID too long (max 32)")
	}
	if strings.ContainsRune(ssid, 0) {
		return fmt.Errorf("SSID contains null byte")
	}
	if !reSSID.MatchString(ssid) {
		return fmt.Errorf("invalid SSID")
	}
	return nil
}

func validateWiFiPassword(pw string) error {
	if len(pw) < 8 || len(pw) > 63 {
		return fmt.Errorf("WiFi password must be 8-63 characters")
	}
	if strings.ContainsRune(pw, 0) {
		return fmt.Errorf("password contains null byte")
	}
	return nil
}

// ── body-size limiter ────────────────────────────────────────────────

// limitBody wraps the request body in an http.MaxBytesReader so a caller
// can't OOM the bridge with a multi-GB JSON body.
func limitBody(r *http.Request, maxBytes int64) *http.Request {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
	return r
}

// ── exec with timeout ────────────────────────────────────────────────

const defaultExecTimeout = 30 * time.Second

// execWithTimeout runs a shell command bounded by ctx; if ctx has no
// deadline a 30 s one is applied. Returns combined stdout+stderr + err.
func execWithTimeout(ctx context.Context, name string, args ...string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultExecTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("command timed out")
	}
	return string(out), err
}

// sanitizeExecError returns a safe error message without leaking system
// internals to the client. Log the raw err server-side; return this to
// the caller.
func sanitizeExecError(operation string, err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		return fmt.Sprintf("%s: command timed out", operation)
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return fmt.Sprintf("%s failed (exit code %d)", operation, exitErr.ExitCode())
	}
	return fmt.Sprintf("%s failed", operation)
}

// writeSuccess is a small convenience for "200 OK with a status string"
// — matches meshsat's writeJSON pattern, equivalent to HAL's
// successResponse helper.
func writeSuccess(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusOK, map[string]string{"status": message})
}
