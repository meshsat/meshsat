package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/certpin"
	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// WebhookGateway bridges Meshtastic mesh messages to/from HTTP webhooks.
type WebhookGateway struct {
	config WebhookConfig
	db     *database.DB
	inCh   chan InboundMessage
	outCh  chan *transport.MeshMessage

	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	lastActive atomic.Int64
	startTime  time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewWebhookGateway creates a new webhook gateway.
func NewWebhookGateway(cfg WebhookConfig, db *database.DB) *WebhookGateway {
	return &WebhookGateway{
		config: cfg,
		db:     db,
		inCh:   make(chan InboundMessage, 32),
		outCh:  make(chan *transport.MeshMessage, 10),
	}
}

// Start begins the webhook gateway workers.
func (g *WebhookGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()
	g.connected.Store(true)

	if g.config.OutboundURL != "" {
		g.wg.Add(1)
		go g.sendWorker(ctx)
	}

	log.Info().
		Bool("outbound", g.config.OutboundURL != "").
		Bool("inbound", g.config.InboundEnabled).
		Msg("webhook gateway started")
	return nil
}

// Stop shuts down the gateway.
func (g *WebhookGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	g.connected.Store(false)
	log.Info().Msg("webhook gateway stopped")
	return nil
}

// Forward enqueues a message for webhook delivery.
func (g *WebhookGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	select {
	case g.outCh <- msg:
		return nil
	default:
		g.errors.Add(1)
		return fmt.Errorf("webhook outbound queue full")
	}
}

// Receive returns the inbound message channel.
func (g *WebhookGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *WebhookGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "webhook",
		Connected:   g.connected.Load(),
		MessagesIn:  g.msgsIn.Load(),
		MessagesOut: g.msgsOut.Load(),
		Errors:      g.errors.Load(),
	}
	if ts := g.lastActive.Load(); ts > 0 {
		s.LastActivity = time.Unix(ts, 0)
	}
	if s.Connected && !g.startTime.IsZero() {
		s.ConnectionUptime = time.Since(g.startTime).Truncate(time.Second).String()
	}
	return s
}

// Type returns the gateway type identifier.
func (g *WebhookGateway) Type() string {
	return "webhook"
}

// Config returns the gateway configuration (for secret validation).
func (g *WebhookGateway) Config() WebhookConfig {
	return g.config
}

// ForwardWebhookInbound pushes an externally-received webhook message into the gateway.
func (g *WebhookGateway) ForwardWebhookInbound(msg InboundMessage) {
	select {
	case g.inCh <- msg:
		g.msgsIn.Add(1)
		g.lastActive.Store(time.Now().Unix())
	default:
		log.Warn().Msg("webhook: inbound channel full")
	}
}

// sendWorker dequeues messages and POSTs them to the configured URL.
func (g *WebhookGateway) sendWorker(ctx context.Context) {
	defer g.wg.Done()

	pin := certpin.FromEnv("MESHSAT_HUB_CERT_PIN", "MESHSAT_HUB_CERT_PIN_BACKUP")
	client := certpin.PinnedClient(pin, time.Duration(g.config.TimeoutSec)*time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			g.postWithRetry(ctx, client, msg)
		}
	}
}

func (g *WebhookGateway) postWithRetry(ctx context.Context, client *http.Client, msg *transport.MeshMessage) {
	payload := map[string]interface{}{
		"from":      fmt.Sprintf("!%08x", msg.From),
		"to":        fmt.Sprintf("!%08x", msg.To),
		"channel":   msg.Channel,
		"portnum":   msg.PortNum,
		"text":      msg.DecodedText,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"source":    "meshsat",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Warn().Err(err).Msg("webhook: marshal failed")
		return
	}

	wait := 5 * time.Second
	for attempt := 0; attempt <= g.config.RetryCount; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait *= 2
			if wait > 5*time.Minute {
				wait = 5 * time.Minute
			}
		}

		req, err := http.NewRequestWithContext(ctx, g.config.OutboundMethod, g.config.OutboundURL, bytes.NewReader(body))
		if err != nil {
			log.Warn().Err(err).Msg("webhook: request creation failed")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range g.config.OutboundHeaders {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			log.Warn().Err(err).Int("attempt", attempt+1).Str("url", g.config.OutboundURL).Msg("webhook: POST failed")
			if g.db != nil {
				_ = g.db.InsertWebhookLog("outbound", g.config.OutboundURL, g.config.OutboundMethod, 0, string(body), "", err.Error())
			}
			g.errors.Add(1)
			continue
		}
		resp.Body.Close()

		errMsg := ""
		if resp.StatusCode >= 400 {
			errMsg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if g.db != nil {
			_ = g.db.InsertWebhookLog("outbound", g.config.OutboundURL, g.config.OutboundMethod, resp.StatusCode, string(body), "", errMsg)
		}

		if resp.StatusCode < 400 {
			g.msgsOut.Add(1)
			g.lastActive.Store(time.Now().Unix())
			log.Info().Str("url", g.config.OutboundURL).Int("status", resp.StatusCode).Msg("webhook: POST success")
			return
		}

		log.Warn().Int("status", resp.StatusCode).Int("attempt", attempt+1).Str("url", g.config.OutboundURL).Msg("webhook: POST returned error")
		g.errors.Add(1)
	}

	log.Error().Str("url", g.config.OutboundURL).Int("retries", g.config.RetryCount).Msg("webhook: all retries exhausted")
}
