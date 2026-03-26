package routing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// AstrocastInterfaceConfig configures an Astrocast Reticulum interface.
type AstrocastInterfaceConfig struct {
	Name string // e.g. "astrocast_0"
}

// AstrocastInterface is a bidirectional Reticulum interface over the Astronode S module.
// Uplink sends Reticulum packets (max 160 bytes), downlink receives via cloud commands.
type AstrocastInterface struct {
	config   AstrocastInterfaceConfig
	astro    transport.AstrocastTransport
	callback func(packet []byte)

	mu      sync.Mutex
	online  bool
	stopCh  chan struct{}
	stopped bool
}

// NewAstrocastInterface creates a new Astrocast Reticulum interface.
func NewAstrocastInterface(config AstrocastInterfaceConfig, astro transport.AstrocastTransport, callback func(packet []byte)) *AstrocastInterface {
	return &AstrocastInterface{
		config:   config,
		astro:    astro,
		callback: callback,
		stopCh:   make(chan struct{}),
	}
}

// Start begins monitoring for downlink commands and marks the interface online.
func (a *AstrocastInterface) Start(ctx context.Context) error {
	status, err := a.astro.GetStatus(ctx)
	if err != nil {
		log.Warn().Err(err).Str("iface", a.config.Name).Msg("astrocast iface: could not get status")
	}

	a.mu.Lock()
	a.online = err == nil && status.Connected
	a.mu.Unlock()

	go a.eventLoop(ctx)

	log.Info().Str("iface", a.config.Name).Bool("online", a.IsOnline()).
		Msg("astrocast reticulum interface started")
	return nil
}

// Send transmits a Reticulum packet as an Astrocast uplink message.
func (a *AstrocastInterface) Send(ctx context.Context, packet []byte) error {
	a.mu.Lock()
	online := a.online
	a.mu.Unlock()

	if !online {
		return fmt.Errorf("astrocast interface %s is offline", a.config.Name)
	}
	if len(packet) > 160 {
		return fmt.Errorf("packet %d bytes exceeds Astrocast MTU 160", len(packet))
	}

	result, err := a.astro.Send(ctx, packet)
	if err != nil {
		return fmt.Errorf("astrocast send: %w", err)
	}
	if !result.Queued {
		return fmt.Errorf("astrocast send failed: message not queued (id=%d)", result.MessageID)
	}

	log.Debug().Str("iface", a.config.Name).Int("size", len(packet)).
		Msg("astrocast iface: packet sent")
	return nil
}

// Stop shuts down the interface.
func (a *AstrocastInterface) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return
	}
	a.stopped = true
	a.online = false
	close(a.stopCh)
	log.Info().Str("iface", a.config.Name).Msg("astrocast reticulum interface stopped")
}

// IsOnline returns whether the Astronode module is connected.
func (a *AstrocastInterface) IsOnline() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.online
}

// eventLoop subscribes to Astrocast events for downlink command reception.
func (a *AstrocastInterface) eventLoop(ctx context.Context) {
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		default:
		}

		start := time.Now()
		err := a.listenOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		a.mu.Lock()
		stopped := a.stopped
		a.mu.Unlock()
		if stopped {
			return
		}

		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}
		if err != nil {
			log.Debug().Err(err).Str("iface", a.config.Name).Dur("backoff", backoff).
				Msg("astrocast iface: event loop reconnecting")
		}

		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (a *AstrocastInterface) listenOnce(ctx context.Context) error {
	events, err := a.astro.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	if status, err := a.astro.GetStatus(ctx); err == nil {
		a.mu.Lock()
		a.online = status.Connected
		a.mu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.stopCh:
			return nil
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("event channel closed")
			}
			switch event.Type {
			case "command_available":
				// Downlink command — read and deliver as Reticulum packet
				a.handleDownlink(ctx)
			case "connected":
				a.mu.Lock()
				a.online = true
				a.mu.Unlock()
			case "disconnected":
				a.mu.Lock()
				a.online = false
				a.mu.Unlock()
				return fmt.Errorf("module disconnected")
			}
		}
	}
}

func (a *AstrocastInterface) handleDownlink(ctx context.Context) {
	cmd, err := a.astro.ReadCommand(ctx)
	if err != nil {
		log.Debug().Err(err).Str("iface", a.config.Name).Msg("astrocast iface: read command failed")
		return
	}
	if cmd == nil || len(cmd.Data) == 0 {
		return
	}

	// Clear the command from the module after reading
	if err := a.astro.ClearCommand(ctx); err != nil {
		log.Warn().Err(err).Str("iface", a.config.Name).Msg("astrocast iface: clear command failed")
	}

	if len(cmd.Data) < 2 {
		log.Debug().Str("iface", a.config.Name).Int("size", len(cmd.Data)).
			Msg("astrocast iface: downlink too short for Reticulum packet")
		return
	}

	log.Debug().Str("iface", a.config.Name).Int("size", len(cmd.Data)).
		Msg("astrocast iface: received Reticulum packet via downlink")
	a.callback(cmd.Data)
}
