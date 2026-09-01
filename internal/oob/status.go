package oob

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// StatusSources are the in-container facts the PING and STATUS-NET bodies
// are built from. Any of them may be nil; the field is then omitted.
type StatusSources struct {
	Uptime  func() time.Duration
	Battery func() (pct float64, ac bool, ok bool)
	Queued  func() int
	WLAN    func() (operstate string, ok bool)
}

// BearerBudget returns how many args bytes a reply may carry on a bearer.
// APRS messages are 67 characters (a 40-byte frame), the SBD plaintext
// path is 120 characters (a 73-byte frame), SMS is 160 characters with one
// character of margin, everything else takes the full 73.
func BearerBudget(ifaceID string) int {
	switch {
	case strings.HasPrefix(ifaceID, "aprs"):
		return 15
	case strings.HasPrefix(ifaceID, "iridium_imt"):
		return MaxArgs
	case strings.HasPrefix(ifaceID, "iridium"):
		return 48
	case strings.HasPrefix(ifaceID, "cellular"), strings.HasPrefix(ifaceID, "sms"):
		return MaxArgs - 1
	}
	return MaxArgs
}

// formatUptime renders a duration as 3d4h, 17h or 42m.
func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", mins)
}

// pingBody builds the PING reply: fields in importance order so a small
// bearer budget keeps the head. Example: u17h b98A q0 wU t1432Z s3.
func (s *Service) pingBody() string {
	var parts []string
	src := s.d.Status
	if src.Uptime != nil {
		parts = append(parts, "u"+formatUptime(src.Uptime()))
	}
	if src.Battery != nil {
		if pct, ac, ok := src.Battery(); ok {
			flag := "B"
			if ac {
				flag = "A"
			}
			parts = append(parts, fmt.Sprintf("b%d%s", int(pct+0.5), flag))
		} else {
			parts = append(parts, "b?")
		}
	}
	if src.Queued != nil {
		parts = append(parts, fmt.Sprintf("q%d", src.Queued()))
	}
	if src.WLAN != nil {
		if state, ok := src.WLAN(); ok {
			w := "D"
			if state == "up" {
				w = "U"
			}
			parts = append(parts, "w"+w)
		}
	}
	parts = append(parts, "t"+s.now().UTC().Format("1504")+"Z")
	parts = append(parts, fmt.Sprintf("s%X", s.bearerBitmap()))
	if s.d.Host != nil && !s.d.Host.Available() {
		parts = append(parts, "agent:none")
	}
	return strings.Join(parts, " ")
}

// bearerBitmap sets bit i when the i-th interface target in Targets is up.
func (s *Service) bearerBitmap() uint64 {
	if s.d.BearersUp == nil {
		return 0
	}
	up := s.d.BearersUp()
	var bits uint64
	i := 0
	for _, t := range Targets {
		if t.Kind != KindInterface {
			continue
		}
		if up[t.IfaceID] {
			bits |= 1 << i
		}
		i++
	}
	return bits
}

// statusNetBody builds the STATUS-NET reply from the host agent, falling
// back to the wlan operstate when the agent is absent.
func (s *Service) statusNetBody(ctx context.Context) string {
	if s.d.Host != nil && s.d.Host.Available() {
		res, err := s.d.Host.Call(ctx, "net_status", nil)
		if err == nil {
			var parts []string
			for _, k := range []string{"ip", "gw", "wpa", "bssid", "rssi", "usb"} {
				if v, ok := res[k]; ok && v != nil && fmt.Sprint(v) != "" {
					parts = append(parts, k+":"+fmt.Sprint(v))
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	state := "?"
	if s.d.Status.WLAN != nil {
		if st, ok := s.d.Status.WLAN(); ok {
			state = st
		}
	}
	return "wlan0:" + state + " agent:none"
}

// chunk splits body into at most maxChunks pieces of at most size bytes.
// The remainder beyond the last chunk is dropped; the caller's seq/total
// numbering tells the operator when that happened.
func chunk(body string, size, maxChunks int) []string {
	if size <= 0 {
		return nil
	}
	if body == "" {
		return []string{""}
	}
	var out []string
	for len(body) > 0 && len(out) < maxChunks {
		n := size
		if n > len(body) {
			n = len(body)
		}
		out = append(out, body[:n])
		body = body[n:]
	}
	return out
}
