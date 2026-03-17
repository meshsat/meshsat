package vpn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// Peer represents a WireGuard peer managed by wireguard-ui.
type Peer struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Email             string    `json:"email"`
	PublicKey         string    `json:"public_key"`
	PresharedKey      string    `json:"preshared_key,omitempty"`
	AllocatedIPs      []string  `json:"allocated_ips"`
	AllowedIPs        []string  `json:"allowed_ips"`
	Endpoint          string    `json:"endpoint"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastHandshakeTime string    `json:"latest_handshake_at,omitempty"`
	TransferRx        int64     `json:"transfer_rx,omitempty"`
	TransferTx        int64     `json:"transfer_tx,omitempty"`
}

// PeerStatus holds runtime tunnel status for a peer.
type PeerStatus struct {
	PublicKey     string `json:"public_key"`
	Endpoint      string `json:"endpoint"`
	LastHandshake string `json:"last_handshake"`
	TransferRx    int64  `json:"transfer_rx"`
	TransferTx    int64  `json:"transfer_tx"`
	Connected     bool   `json:"connected"`
}

// CreatePeerRequest is the payload for creating a new WireGuard peer.
type CreatePeerRequest struct {
	Name string `json:"name"`
}

// Client communicates with the wireguard-ui REST API.
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

// NewClient creates a VPN management client.
func NewClient(baseURL, username, password string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  baseURL,
		username: username,
		password: password,
		http: &http.Client{
			Timeout: 15 * time.Second,
			Jar:     jar,
		},
	}
}

// login authenticates with wireguard-ui and stores the session cookie.
func (c *Client) login() error {
	payload := map[string]string{
		"username": c.username,
		"password": c.password,
	}
	body, _ := json.Marshal(payload)
	resp, err := c.http.Post(c.baseURL+"/api/session", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("vpn login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vpn login: status %d", resp.StatusCode)
	}
	return nil
}

// doRequest performs an authenticated HTTP request, retrying login once on 401.
func (c *Client) doRequest(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	// Retry login once on 401
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := c.login(); err != nil {
			return nil, err
		}
		// Rebuild request (body may have been consumed)
		if body != nil {
			data, _ := json.Marshal(body)
			bodyReader = bytes.NewReader(data)
		}
		req, _ = http.NewRequest(method, c.baseURL+path, bodyReader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err = c.http.Do(req)
		if err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// ListPeers returns all configured WireGuard peers.
func (c *Client) ListPeers() ([]Peer, error) {
	resp, err := c.doRequest(http.MethodGet, "/api/wireguard/client", nil)
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list peers: status %d", resp.StatusCode)
	}
	var peers []Peer
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		return nil, fmt.Errorf("list peers decode: %w", err)
	}
	return peers, nil
}

// CreatePeer provisions a new WireGuard peer and returns its details.
func (c *Client) CreatePeer(name string) (*Peer, error) {
	payload := CreatePeerRequest{Name: name}
	resp, err := c.doRequest(http.MethodPost, "/api/wireguard/client", payload)
	if err != nil {
		return nil, fmt.Errorf("create peer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create peer: status %d: %s", resp.StatusCode, string(body))
	}
	var peer Peer
	if err := json.NewDecoder(resp.Body).Decode(&peer); err != nil {
		return nil, fmt.Errorf("create peer decode: %w", err)
	}
	return &peer, nil
}

// GetPeer returns a single peer by ID.
func (c *Client) GetPeer(peerID string) (*Peer, error) {
	resp, err := c.doRequest(http.MethodGet, "/api/wireguard/client/"+peerID, nil)
	if err != nil {
		return nil, fmt.Errorf("get peer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get peer: status %d", resp.StatusCode)
	}
	var peer Peer
	if err := json.NewDecoder(resp.Body).Decode(&peer); err != nil {
		return nil, fmt.Errorf("get peer decode: %w", err)
	}
	return &peer, nil
}

// DeletePeer removes a WireGuard peer by ID.
func (c *Client) DeletePeer(peerID string) error {
	resp, err := c.doRequest(http.MethodDelete, "/api/wireguard/client/"+peerID, nil)
	if err != nil {
		return fmt.Errorf("delete peer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete peer: status %d", resp.StatusCode)
	}
	return nil
}

// EnablePeer enables a disabled WireGuard peer.
func (c *Client) EnablePeer(peerID string) error {
	resp, err := c.doRequest(http.MethodPost, "/api/wireguard/client/"+peerID+"/enable", nil)
	if err != nil {
		return fmt.Errorf("enable peer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("enable peer: status %d", resp.StatusCode)
	}
	return nil
}

// DisablePeer disables a WireGuard peer.
func (c *Client) DisablePeer(peerID string) error {
	resp, err := c.doRequest(http.MethodPost, "/api/wireguard/client/"+peerID+"/disable", nil)
	if err != nil {
		return fmt.Errorf("disable peer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("disable peer: status %d", resp.StatusCode)
	}
	return nil
}

// GetPeerConfig returns the WireGuard client configuration file for a peer.
func (c *Client) GetPeerConfig(peerID string) (string, error) {
	resp, err := c.doRequest(http.MethodGet, "/api/wireguard/client/"+peerID+"/configuration", nil)
	if err != nil {
		return "", fmt.Errorf("get peer config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get peer config: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read peer config: %w", err)
	}
	return string(data), nil
}

// Healthy returns true if the wireguard-ui API is reachable.
func (c *Client) Healthy() bool {
	resp, err := c.http.Get(c.baseURL + "/api/wireguard/server")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
}
