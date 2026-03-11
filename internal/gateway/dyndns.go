package gateway

import (
	"bytes"
	"context"
	"encoding/json"
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
	if d.config.Provider == "cloudflare" {
		return d.updateCloudflare(ip)
	}

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

// updateCloudflare uses the Cloudflare API v4 to update a DNS A record.
// Requires: Token (API token), ZoneID, Domain (FQDN of A record).
// RecordID is auto-resolved on first call if not set.
func (d *DynDNSUpdater) updateCloudflare(ip string) error {
	if d.config.ZoneID == "" {
		return fmt.Errorf("cloudflare: zone_id is required")
	}
	if d.config.Token == "" {
		return fmt.Errorf("cloudflare: token (API token) is required")
	}

	client := &http.Client{Timeout: 15 * time.Second}

	// Auto-resolve record ID if not configured
	recordID := d.config.RecordID
	if recordID == "" {
		var err error
		recordID, err = d.cloudflareResolveRecord(client)
		if err != nil {
			return fmt.Errorf("cloudflare: resolve record: %w", err)
		}
		d.config.RecordID = recordID
	}

	// PUT the updated A record
	payload := map[string]interface{}{
		"type":    "A",
		"name":    d.config.Domain,
		"content": ip,
		"ttl":     120,
		"proxied": false,
	}
	body, _ := json.Marshal(payload)

	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s",
		d.config.ZoneID, recordID)
	req, err := http.NewRequest("PUT", apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.config.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MeshSat DynDNS/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	var cfResp struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &cfResp); err != nil {
		return fmt.Errorf("cloudflare: parse response: %w", err)
	}
	if !cfResp.Success {
		msg := "unknown error"
		if len(cfResp.Errors) > 0 {
			msg = cfResp.Errors[0].Message
		}
		return fmt.Errorf("cloudflare: %s", msg)
	}
	return nil
}

// cloudflareResolveRecord looks up the DNS record ID by domain name within the zone.
func (d *DynDNSUpdater) cloudflareResolveRecord(client *http.Client) (string, error) {
	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?type=A&name=%s",
		d.config.ZoneID, d.config.Domain)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+d.config.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MeshSat DynDNS/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var cfResp struct {
		Success bool `json:"success"`
		Result  []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &cfResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if !cfResp.Success || len(cfResp.Result) == 0 {
		return "", fmt.Errorf("no A record found for %s in zone %s", d.config.Domain, d.config.ZoneID)
	}
	return cfResp.Result[0].ID, nil
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
