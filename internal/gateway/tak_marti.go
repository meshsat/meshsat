package gateway

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

// MartiClient interacts with a TAK Server Marti REST API (port 8443).
type MartiClient struct {
	baseURL string
	client  *http.Client
}

// MartiMission represents a TAK Server mission.
type MartiMission struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Tool        string `json:"tool,omitempty"`
	CreateTime  string `json:"createTime,omitempty"`
	Groups      []struct {
		Name string `json:"name"`
	} `json:"groups,omitempty"`
}

// MartiMissionsResponse is the TAK Server response for listing missions.
type MartiMissionsResponse struct {
	Version string         `json:"version"`
	Type    string         `json:"type"`
	Data    []MartiMission `json:"data"`
}

// MartiSyncContent represents a downloadable file from TAK Server.
type MartiSyncContent struct {
	Hash        string `json:"hash"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	MIMEType    string `json:"mimeType"`
	SubmitTime  string `json:"submissionTime,omitempty"`
	SubmitterID string `json:"submitter,omitempty"`
}

// NewMartiClient creates a client for the TAK Server Marti REST API.
// The baseURL should include the scheme and port, e.g., https://tak-server:8443.
// If certFile/keyFile are provided, mutual TLS is used.
func NewMartiClient(baseURL, certFile, keyFile, caFile string) (*MartiClient, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("marti: load client cert: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("marti: read CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("marti: invalid CA certificate")
		}
		tlsCfg.RootCAs = pool
	} else {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // TAK Server self-signed certs
	}

	return &MartiClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// ListMissions returns all missions from the TAK Server.
func (c *MartiClient) ListMissions() ([]MartiMission, error) {
	resp, err := c.client.Get(c.baseURL + "/Marti/api/missions")
	if err != nil {
		return nil, fmt.Errorf("marti: list missions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("marti: list missions: %d: %s", resp.StatusCode, string(body))
	}

	var result MartiMissionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("marti: decode missions: %w", err)
	}
	return result.Data, nil
}

// SubscribeMission subscribes this client to a mission by name.
func (c *MartiClient) SubscribeMission(name, uid string) error {
	url := fmt.Sprintf("%s/Marti/api/missions/%s/subscription?uid=%s", c.baseURL, name, uid)
	req, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("marti: subscribe mission %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("marti: subscribe mission %q: %d: %s", name, resp.StatusCode, string(body))
	}

	log.Info().Str("mission", name).Str("uid", uid).Msg("marti: subscribed to mission")
	return nil
}

// DownloadContent downloads a file from the TAK Server by content hash.
func (c *MartiClient) DownloadContent(hash string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/Marti/sync/content?hash=%s", c.baseURL, hash)
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("marti: download %s: %w", hash, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("marti: download %s: %d", hash, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("marti: read content: %w", err)
	}

	filename := resp.Header.Get("Content-Disposition")
	return data, filename, nil
}

// UploadContent uploads a file to the TAK Server.
func (c *MartiClient) UploadContent(filename string, data []byte, mimeType string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("marti: create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("marti: write form data: %w", err)
	}
	writer.Close()

	url := c.baseURL + "/Marti/sync/upload"
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("marti: upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("marti: upload: %d: %s", resp.StatusCode, string(respBody))
	}

	// Response contains the content hash
	respBody, _ := io.ReadAll(resp.Body)
	return string(bytes.TrimSpace(respBody)), nil
}

// GetSASnapshot returns the current situational awareness CoT snapshot.
func (c *MartiClient) GetSASnapshot() ([]byte, error) {
	resp, err := c.client.Get(c.baseURL + "/Marti/api/cot/sa")
	if err != nil {
		return nil, fmt.Errorf("marti: SA snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("marti: SA snapshot: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
