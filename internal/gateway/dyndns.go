package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// DynDNSUpdater keeps a hostname pointed at the device's current IP.
type DynDNSUpdater struct {
	config DynDNSConfig
	lastIP string
	lastAt time.Time

	mu     sync.RWMutex
	cancel context.CancelFunc
}

// DynDNSStatus reports the updater's current state.
type DynDNSStatus struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider"`
	Domain     string `json:"domain"`
	CurrentIP  string `json:"current_ip"`
	LastUpdate string `json:"last_update,omitempty"`
}

// NewDynDNSUpdater creates a new DynDNS updater.
func NewDynDNSUpdater(cfg DynDNSConfig) *DynDNSUpdater {
	if cfg.Interval <= 0 {
		cfg.Interval = 300
	}
	return &DynDNSUpdater{config: cfg}
}

// Start begins the periodic IP check loop.
func (d *DynDNSUpdater) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	go d.run(ctx)
}

// Stop cancels the updater.
func (d *DynDNSUpdater) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

// ForceUpdate triggers an immediate DNS update.
func (d *DynDNSUpdater) ForceUpdate() error {
	ip := getInterfaceIP("wwan0")
	if ip == "" {
		return fmt.Errorf("no IP on wwan0")
	}
	return d.update(ip)
}

// Status returns the current updater state.
func (d *DynDNSUpdater) Status() DynDNSStatus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := DynDNSStatus{
		Enabled:   d.config.Enabled,
		Provider:  d.config.Provider,
		Domain:    d.config.Domain,
		CurrentIP: d.lastIP,
	}
	if !d.lastAt.IsZero() {
		s.LastUpdate = d.lastAt.UTC().Format(time.RFC3339)
	}
	return s
}

func (d *DynDNSUpdater) run(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(d.config.Interval) * time.Second)
	defer ticker.Stop()

	// Initial check
	d.check()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.check()
		}
	}
}

func (d *DynDNSUpdater) check() {
	ip := getInterfaceIP("wwan0")
	if ip == "" {
		return
	}

	d.mu.RLock()
	lastIP := d.lastIP
	d.mu.RUnlock()

	if ip == lastIP {
		return
	}

	if err := d.update(ip); err != nil {
		log.Warn().Err(err).Str("provider", d.config.Provider).Msg("dyndns: update failed")
		return
	}

	d.mu.Lock()
	d.lastIP = ip
	d.lastAt = time.Now()
	d.mu.Unlock()

	log.Info().Str("provider", d.config.Provider).Str("domain", d.config.Domain).Str("ip", ip).Msg("dyndns: updated")
}

func (d *DynDNSUpdater) update(ip string) error {
	url := d.buildURL(ip)
	if url == "" {
		return fmt.Errorf("unsupported provider: %s", d.config.Provider)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	// DynDNS v2 providers use Basic Auth
	if d.config.Username != "" && d.config.Password != "" {
		req.SetBasicAuth(d.config.Username, d.config.Password)
	}
	req.Header.Set("User-Agent", "MeshSat DynDNS/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	result := strings.TrimSpace(string(body))

	return d.validateResponse(result)
}

func (d *DynDNSUpdater) buildURL(ip string) string {
	switch d.config.Provider {
	case "duckdns":
		return fmt.Sprintf("https://www.duckdns.org/update?domains=%s&token=%s&ip=%s",
			d.config.Domain, d.config.Token, ip)
	case "noip":
		return fmt.Sprintf("https://dynupdate.no-ip.com/nic/update?hostname=%s&myip=%s",
			d.config.Domain, ip)
	case "dynu":
		return fmt.Sprintf("https://api.dynu.com/nic/update?hostname=%s&myip=%s",
			d.config.Domain, ip)
	case "custom":
		url := d.config.CustomURL
		url = strings.ReplaceAll(url, "{ip}", ip)
		url = strings.ReplaceAll(url, "{hostname}", d.config.Domain)
		return url
	default:
		return ""
	}
}

func (d *DynDNSUpdater) validateResponse(result string) error {
	switch d.config.Provider {
	case "duckdns":
		if result == "OK" {
			return nil
		}
		return fmt.Errorf("duckdns returned: %s", result)
	case "noip", "dynu":
		if strings.HasPrefix(result, "good") || strings.HasPrefix(result, "nochg") {
			return nil
		}
		return fmt.Errorf("dyndns returned: %s", result)
	case "custom":
		// No validation for custom providers
		return nil
	default:
		return nil
	}
}

// getInterfaceIP returns the first IPv4 address on the named interface.
func getInterfaceIP(name string) string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return ""
}
