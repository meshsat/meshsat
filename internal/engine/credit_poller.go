package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// CreditPoller periodically fetches the Iridium credit balance from the
// Cloudloop (Ground Control) API and stores snapshots in the database.
type CreditPoller struct {
	db       *database.DB
	client   *http.Client
	baseURL  string
	apiKey   string
	secret   string
	interval time.Duration
	cancel   context.CancelFunc
}

// NewCreditPoller creates a new credit balance poller.
// interval is the polling period (default 1h).
func NewCreditPoller(db *database.DB, baseURL, apiKey, secret string, interval time.Duration) *CreditPoller {
	return &CreditPoller{
		db:       db,
		client:   &http.Client{Timeout: 30 * time.Second},
		baseURL:  baseURL,
		apiKey:   apiKey,
		secret:   secret,
		interval: interval,
	}
}

// Start begins the polling loop. It polls once immediately, then at the
// configured interval. Call Stop() to cancel.
func (cp *CreditPoller) Start(ctx context.Context) {
	ctx, cp.cancel = context.WithCancel(ctx)
	go cp.run(ctx)
}

// Stop cancels the polling loop.
func (cp *CreditPoller) Stop() {
	if cp.cancel != nil {
		cp.cancel()
	}
}

func (cp *CreditPoller) run(ctx context.Context) {
	log.Info().
		Str("base_url", cp.baseURL).
		Dur("interval", cp.interval).
		Msg("credit balance poller started")

	// Poll immediately on startup
	cp.poll(ctx)

	ticker := time.NewTicker(cp.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("credit balance poller stopped")
			return
		case <-ticker.C:
			cp.poll(ctx)
		}
	}
}

// cloudloopBalanceResponse represents the Cloudloop API balance response.
type cloudloopBalanceResponse struct {
	Balance  int    `json:"balance"`
	Currency string `json:"currency"`
}

func (cp *CreditPoller) poll(ctx context.Context) {
	balance, currency, err := cp.fetchBalance(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("credit balance poll failed")
		return
	}

	if err := cp.db.InsertCreditBalance(balance, currency, "cloudloop"); err != nil {
		log.Error().Err(err).Msg("failed to store credit balance")
		return
	}

	log.Info().Int("balance", balance).Str("currency", currency).Msg("credit balance updated")
}

func (cp *CreditPoller) fetchBalance(ctx context.Context) (int, string, error) {
	url := fmt.Sprintf("%s/v1/sbd/credits", cp.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", cp.apiKey)
	req.Header.Set("X-API-Secret", cp.secret)

	resp, err := cp.client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return 0, "", fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("API returned %d: %s", resp.StatusCode, string(body))
	}

	var result cloudloopBalanceResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, "", fmt.Errorf("decode response: %w", err)
	}

	currency := result.Currency
	if currency == "" {
		currency = "credits"
	}

	return result.Balance, currency, nil
}

// FetchNow triggers a single immediate poll (used by manual refresh endpoint).
func (cp *CreditPoller) FetchNow(ctx context.Context) error {
	balance, currency, err := cp.fetchBalance(ctx)
	if err != nil {
		return err
	}
	return cp.db.InsertCreditBalance(balance, currency, "cloudloop")
}
