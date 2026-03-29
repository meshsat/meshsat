package routing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/reticulum"
	"meshsat/internal/transport"
)

// smsRNSPrefix identifies SMS messages carrying Reticulum packets.
const smsRNSPrefix = "RNS:"

// SMSInterfaceConfig configures an SMS Reticulum interface.
type SMSInterfaceConfig struct {
	// Name is the interface identifier (e.g. "sms_0").
	Name string
	// PeerNumber is the phone number of the Reticulum peer for point-to-point SMS.
	// Empty string disables sending (receive-only).
	PeerNumber string
}

// SMSInterface is a bidirectional Reticulum interface over cellular SMS.
// Reticulum packets are base64-encoded for SMS text transport.
// Inbound SMS from the peer number are decoded and delivered as Reticulum packets.
type SMSInterface struct {
	config   SMSInterfaceConfig
	cell     transport.CellTransport
	callback func(packet []byte)

	mu      sync.Mutex
	online  bool
	stopCh  chan struct{}
	stopped bool
}

// NewSMSInterface creates a new SMS Reticulum interface.
func NewSMSInterface(config SMSInterfaceConfig, cell transport.CellTransport, callback func(packet []byte)) *SMSInterface {
	return &SMSInterface{
		config:   config,
		cell:     cell,
		callback: callback,
		stopCh:   make(chan struct{}),
	}
}

// Start begins monitoring for inbound SMS and marks the interface online.
func (s *SMSInterface) Start(ctx context.Context) error {
	status, err := s.cell.GetStatus(ctx)
	if err != nil {
		log.Warn().Err(err).Str("iface", s.config.Name).Msg("sms iface: could not get modem status")
	}

	s.mu.Lock()
	s.online = err == nil && status.Connected
	s.mu.Unlock()

	go s.eventLoop(ctx)

	log.Info().Str("iface", s.config.Name).Str("peer", s.config.PeerNumber).
		Bool("online", s.IsOnline()).Msg("sms reticulum interface started")
	return nil
}

// Send transmits a Reticulum packet as a base64-encoded SMS.
func (s *SMSInterface) Send(ctx context.Context, packet []byte) error {
	s.mu.Lock()
	online := s.online
	s.mu.Unlock()

	if !online {
		return fmt.Errorf("sms interface %s is offline", s.config.Name)
	}
	if s.config.PeerNumber == "" {
		return fmt.Errorf("sms interface %s: no peer number configured", s.config.Name)
	}
	// SMS text limit ~160 chars; base64 of 120 bytes = 160 chars.
	// Concatenated SMS supports longer, but cap at reasonable Reticulum MTU.
	if len(packet) > 140 {
		return fmt.Errorf("packet %d bytes exceeds SMS MTU 140 for %s", len(packet), s.config.Name)
	}

	// Prefix with "RNS:" so the receiver can distinguish Reticulum packets from normal SMS.
	encoded := smsRNSPrefix + base64.StdEncoding.EncodeToString(packet)
	if err := s.cell.SendSMS(ctx, s.config.PeerNumber, encoded); err != nil {
		return fmt.Errorf("sms send: %w", err)
	}

	log.Debug().Str("iface", s.config.Name).Int("size", len(packet)).
		Int("sms_chars", len(encoded)).Msg("sms iface: packet sent")
	return nil
}

// Stop shuts down the SMS interface.
func (s *SMSInterface) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return
	}
	s.stopped = true
	s.online = false
	close(s.stopCh)
	log.Info().Str("iface", s.config.Name).Msg("sms reticulum interface stopped")
}

// IsOnline returns whether the cellular modem is connected.
func (s *SMSInterface) IsOnline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online
}

func (s *SMSInterface) eventLoop(ctx context.Context) {
	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		start := time.Now()
		err := s.listenOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return
		}

		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}
		if err != nil {
			log.Debug().Err(err).Str("iface", s.config.Name).Dur("backoff", backoff).
				Msg("sms iface: event loop reconnecting")
		}

		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (s *SMSInterface) listenOnce(ctx context.Context) error {
	events, err := s.cell.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	if status, err := s.cell.GetStatus(ctx); err == nil {
		s.mu.Lock()
		s.online = status.Connected
		s.mu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("event channel closed")
			}
			switch event.Type {
			case "sms_received":
				s.handleSMS(event)
			case "connected":
				s.mu.Lock()
				s.online = true
				s.mu.Unlock()
			case "disconnected":
				s.mu.Lock()
				s.online = false
				s.mu.Unlock()
				return fmt.Errorf("modem disconnected")
			}
		}
	}
}

func (s *SMSInterface) handleSMS(event transport.CellEvent) {
	if len(event.Data) == 0 {
		return
	}

	var sms struct {
		Sender string `json:"sender"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(event.Data, &sms); err != nil {
		return
	}

	// Only accept SMS with the "RNS:" prefix — everything else is a normal SMS.
	if !strings.HasPrefix(sms.Text, smsRNSPrefix) {
		return
	}

	// Filter by peer number if configured (point-to-point link).
	if s.config.PeerNumber != "" && sms.Sender != "" && sms.Sender != s.config.PeerNumber {
		log.Debug().Str("iface", s.config.Name).Str("sender", sms.Sender).
			Str("peer", s.config.PeerNumber).
			Msg("sms iface: ignoring RNS packet from non-peer sender")
		return
	}

	packet, err := base64.StdEncoding.DecodeString(sms.Text[len(smsRNSPrefix):])
	if err != nil {
		log.Debug().Err(err).Str("iface", s.config.Name).
			Msg("sms iface: base64 decode failed, ignoring")
		return
	}

	// Validate: Reticulum packets have at least a 2-byte header
	if len(packet) < 2 {
		log.Debug().Str("iface", s.config.Name).Int("size", len(packet)).
			Msg("sms iface: received data too short for Reticulum packet, ignoring")
		return
	}

	log.Debug().Str("iface", s.config.Name).Str("sender", sms.Sender).
		Int("size", len(packet)).Msg("sms iface: received reticulum packet")
	s.callback(packet)
}

// RegisterSMSInterface creates the SMSInterface and its ReticulumInterface wrapper.
func RegisterSMSInterface(config SMSInterfaceConfig, cell transport.CellTransport, callback func(packet []byte)) (*SMSInterface, *ReticulumInterface) {
	smsIface := NewSMSInterface(config, cell, callback)
	ri := NewReticulumInterface(
		config.Name,
		reticulum.IfaceCellular,
		140, // single SMS binary capacity
		smsIface.Send,
	)
	return smsIface, ri
}
