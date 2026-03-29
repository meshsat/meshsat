package routing

import (
	"context"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"meshsat/internal/transport"
)

// ZigBeeInterfaceConfig configures a ZigBee Reticulum interface.
type ZigBeeInterfaceConfig struct {
	// Name is the interface identifier (e.g. "zigbee_0").
	Name string
	// DstAddr is the ZigBee destination address (default 0xFFFF = broadcast).
	DstAddr uint16
	// DstEndpoint is the ZigBee destination endpoint (default 1).
	DstEndpoint byte
	// ClusterID is the ZigBee cluster ID (default 0x0006 = On/Off).
	ClusterID uint16
}

// ZigBeeInterface is a bidirectional Reticulum interface over ZigBee 3.0.
// Reticulum packets are sent as raw binary via ZNP AF_DATA_REQUEST.
type ZigBeeInterface struct {
	config    ZigBeeInterfaceConfig
	transport *transport.DirectZigBeeTransport
	callback  func(packet []byte)

	mu      sync.Mutex
	online  bool
	stopCh  chan struct{}
	stopped bool
}

// NewZigBeeInterface creates a new ZigBee Reticulum interface.
func NewZigBeeInterface(config ZigBeeInterfaceConfig, zt *transport.DirectZigBeeTransport, callback func(packet []byte)) *ZigBeeInterface {
	if config.DstAddr == 0 {
		config.DstAddr = 0xFFFF // broadcast
	}
	if config.DstEndpoint == 0 {
		config.DstEndpoint = 1
	}
	if config.ClusterID == 0 {
		config.ClusterID = 0x0006 // On/Off cluster
	}
	return &ZigBeeInterface{
		config:    config,
		transport: zt,
		callback:  callback,
		stopCh:    make(chan struct{}),
	}
}

// Start begins monitoring for inbound ZigBee data and marks the interface online.
func (z *ZigBeeInterface) Start(ctx context.Context) error {
	z.mu.Lock()
	z.online = z.transport.IsRunning()
	z.mu.Unlock()

	go z.eventLoop(ctx)

	log.Info().Str("iface", z.config.Name).Bool("online", z.IsOnline()).
		Uint16("dst_addr", z.config.DstAddr).Msg("zigbee reticulum interface started")
	return nil
}

// Send transmits a Reticulum packet as raw binary via ZigBee.
func (z *ZigBeeInterface) Send(ctx context.Context, packet []byte) error {
	z.mu.Lock()
	online := z.online
	z.mu.Unlock()

	if !online {
		return fmt.Errorf("zigbee interface %s is offline", z.config.Name)
	}
	if len(packet) > 100 {
		return fmt.Errorf("packet %d bytes exceeds ZigBee MTU 100 for %s", len(packet), z.config.Name)
	}

	if err := z.transport.Send(z.config.DstAddr, z.config.DstEndpoint, z.config.ClusterID, packet); err != nil {
		return fmt.Errorf("zigbee send: %w", err)
	}

	log.Debug().Str("iface", z.config.Name).Int("size", len(packet)).
		Msg("zigbee iface: packet sent")
	return nil
}

// Stop shuts down the ZigBee interface.
func (z *ZigBeeInterface) Stop() {
	z.mu.Lock()
	defer z.mu.Unlock()

	if z.stopped {
		return
	}
	z.stopped = true
	z.online = false
	close(z.stopCh)
	log.Info().Str("iface", z.config.Name).Msg("zigbee reticulum interface stopped")
}

// IsOnline returns whether the ZigBee coordinator is running.
func (z *ZigBeeInterface) IsOnline() bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.online
}

func (z *ZigBeeInterface) eventLoop(ctx context.Context) {
	events := z.transport.Subscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case <-z.stopCh:
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			// Track online state
			running := z.transport.IsRunning()
			z.mu.Lock()
			z.online = running
			z.mu.Unlock()

			if event.Type == "data" && len(event.Data) >= 2 {
				log.Debug().Str("iface", z.config.Name).Int("size", len(event.Data)).
					Uint16("cluster", event.ClusterID).Msg("zigbee iface: received reticulum packet")
				z.callback(event.Data)
			}
		}
	}
}
