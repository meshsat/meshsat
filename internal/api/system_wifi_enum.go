// host-ops: allowed-in-standalone
//
// WiFi adapter enumeration — walk /sys/class/net to list every wireless
// interface on the host, labelling each with role (onboard vs USB),
// driver, bus, and whether it currently owns the default route (i.e.
// is the link an SSH session is reaching us on). Drives the
// adapter-aware Network tab redesign — see MESHSAT-643.
//
// Pragma because we read `/sys/class/net/*` and shell out to `ip route`
// for mgmt detection. Field kits run bridge standalone; no HAL.
// [MESHSAT-642]

package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WiFiInterface is one row in the adapter list.
type WiFiInterface struct {
	Name   string `json:"name"`    // e.g. "wlan0", "wlx90de80f3a70b"
	MAC    string `json:"mac"`     // from /sys/class/net/<n>/address
	Driver string `json:"driver"`  // e.g. "brcmfmac", "rtl8xxxu"
	Bus    string `json:"bus"`     // "pci" / "usb" / "sdio" / "platform"
	Role   string `json:"role"`    // "onboard" | "usb" | "unknown"
	State  string `json:"state"`   // "up" | "down" | "unknown"
	IsMgmt bool   `json:"is_mgmt"` // owns the default route today
}

// @Summary List WiFi interfaces on the host
// @Description Enumerates every wireless adapter (onboard + USB dongles) with role labels, driver, bus, up/down state, and a hint on which one owns the default route so the UI can warn before mutating the mgmt link. [MESHSAT-642]
// @Tags system
// @Produce json
// @Success 200 {array} WiFiInterface
// @Router /api/system/wifi/interfaces [get]
func (s *Server) handleWiFiInterfaces(w http.ResponseWriter, r *http.Request) {
	ifaces, err := enumerateWiFiInterfaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeExecError("enumerate WiFi", err))
		return
	}
	if ifaces == nil {
		ifaces = []WiFiInterface{}
	}
	// Annotate management flag using `ip route` — best-effort. If the
	// lookup fails the field stays false, which is the safer default.
	if mgmt := defaultRouteIface(r.Context()); mgmt != "" {
		for i := range ifaces {
			if ifaces[i].Name == mgmt {
				ifaces[i].IsMgmt = true
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, ifaces)
}

// enumerateWiFiInterfaces walks /sys/class/net and picks the rows that
// look like wireless adapters (presence of a `wireless` or `phy80211`
// subdirectory, or a name beginning with `wl`).
func enumerateWiFiInterfaces() ([]WiFiInterface, error) {
	const sysNet = "/sys/class/net"
	entries, err := os.ReadDir(sysNet)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", sysNet, err)
	}
	var out []WiFiInterface
	for _, e := range entries {
		name := e.Name()
		if !looksLikeWiFi(sysNet, name) {
			continue
		}
		out = append(out, buildWiFiInterface(sysNet, name))
	}
	return out, nil
}

// looksLikeWiFi returns true when the sysfs entry is a wireless link.
// Conservative — we only require the `wireless` dir, which exists for
// every cfg80211-driven device (brcmfmac, rtl, ath, etc.).
func looksLikeWiFi(sysNet, name string) bool {
	if strings.HasPrefix(name, "wl") {
		return true
	}
	if _, err := os.Stat(filepath.Join(sysNet, name, "wireless")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(sysNet, name, "phy80211")); err == nil {
		return true
	}
	return false
}

// buildWiFiInterface populates one row. All reads are best-effort —
// missing sysfs entries just leave fields empty rather than failing
// the whole enumeration.
func buildWiFiInterface(sysNet, name string) WiFiInterface {
	iface := WiFiInterface{Name: name, Role: "unknown", State: "unknown"}
	iface.MAC = strings.TrimSpace(readFileOr(filepath.Join(sysNet, name, "address"), ""))
	iface.State = strings.ToLower(strings.TrimSpace(readFileOr(filepath.Join(sysNet, name, "operstate"), "unknown")))

	// Device symlink points at the controller — `/sys/class/net/wlan0/device`
	// resolves to something like `/sys/devices/platform/axi/1000...` on
	// the Pi 5 onboard, or `/sys/devices/platform/axi/.../usb1/...` for
	// a USB dongle. Resolving the symlink + checking the target path is
	// the most reliable cross-distro way to detect USB.
	if target, err := os.Readlink(filepath.Join(sysNet, name, "device")); err == nil {
		lower := strings.ToLower(target)
		switch {
		case strings.Contains(lower, "/usb"):
			iface.Bus = "usb"
			iface.Role = "usb"
		case strings.Contains(lower, "/mmc") || strings.Contains(lower, "/sdio"):
			iface.Bus = "sdio"
			iface.Role = "onboard"
		case strings.Contains(lower, "/pci"):
			iface.Bus = "pci"
			iface.Role = "onboard"
		case strings.Contains(lower, "/platform"):
			iface.Bus = "platform"
			// Pi 5 wlan0 is platform-attached — treat as onboard.
			iface.Role = "onboard"
		}
	}

	// Driver symlink: /sys/class/net/<n>/device/driver → .../bus/pci/drivers/brcmfmac
	if dlink, err := os.Readlink(filepath.Join(sysNet, name, "device", "driver")); err == nil {
		iface.Driver = filepath.Base(dlink)
	}
	return iface
}

// defaultRouteIface returns the iface name from `ip -4 route show default`
// (e.g. "wlan0"). Empty string on failure. We accept either `default via
// X dev Y` or `default dev Y via X` orderings.
func defaultRouteIface(ctx context.Context) string {
	return parseDefaultRouteDev(runIPRoute(ctx))
}

// runIPRoute runs `ip -4 route show default`. Bound by a 5 s cap so a
// stuck netlink never pegs the handler. Separate variable so tests can
// stub it.
var runIPRoute = func(ctx context.Context) string {
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := execWithTimeout(cctx, "ip", "-4", "route", "show", "default")
	return out
}

// parseDefaultRouteDev scans output like
//
//	default via 192.168.181.1 dev wlan0 proto dhcp src 192.168.181.210 metric 600
//
// and returns the token after `dev`. Empty on malformed input.
func parseDefaultRouteDev(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

// readFileOr is a tiny helper for the one-liner sysfs attribute reads.
func readFileOr(path, fallback string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	return string(b)
}
