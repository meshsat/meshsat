package routing

import (
	"context"
	"fmt"
	"sync"
	"time"

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

// ZigBeeTransportProvider returns the coordinator transport currently in
// use, or nil. The ZigBee gateway allocates a new transport on every
// start (a coordinator reset, a USB re-enumeration, an operator restart),
// so the interface resolves it per call instead of pinning the first one.
// [MESHSAT-815]
type ZigBeeTransportProvider func() *transport.DirectZigBeeTransport

// zigbeeRebindInterval is how often eventLoop checks for a new transport.
const zigbeeRebindInterval = 5 * time.Second

// ZigBeeInterface is a bidirectional Reticulum interface over ZigBee 3.0.
// Reticulum packets are sent as raw binary via ZNP AF_DATA_REQUEST.
type ZigBeeInterface struct {
	config   ZigBeeInterfaceConfig
	provider ZigBeeTransportProvider
	callback func(packet []byte)

	mu      sync.Mutex
	online  bool
	stopCh  chan struct{}
	stopped bool
}

// NewZigBeeInterface creates a new ZigBee Reticulum interface bound to zt
// (which may be nil until SetTransportProvider supplies a live lookup).
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
		config:   config,
		provider: func() *transport.DirectZigBeeTransport { return zt },
		callback: callback,
		stopCh:   make(chan struct{}),
	}
}

// SetTransportProvider makes the interface follow the gateway's current
// transport across restarts. [MESHSAT-815]
func (z *ZigBeeInterface) SetTransportProvider(fn ZigBeeTransportProvider) {
	z.mu.Lock()
	z.provider = fn
	z.mu.Unlock()
}

func (z *ZigBeeInterface) transport() *transport.DirectZigBeeTransport {
	z.mu.Lock()
	p := z.provider
	z.mu.Unlock()
	if p == nil {
		return nil
	}
	return p()
}

func (z *ZigBeeInterface) setOnline(on bool) {
	z.mu.Lock()
	z.online = on
	z.mu.Unlock()
}

// Start begins monitoring for inbound ZigBee data and marks the interface online.
func (z *ZigBeeInterface) Start(ctx context.Context) error {
	zt := z.transport()
	z.setOnline(zt != nil && zt.IsRunning())

	go z.eventLoop(ctx)

	log.Info().Str("iface", z.config.Name).Bool("online", z.IsOnline()).
		Uint16("dst_addr", z.config.DstAddr).Msg("zigbee reticulum interface started")
	return nil
}

// Send transmits a Reticulum packet as raw binary via ZigBee.
func (z *ZigBeeInterface) Send(ctx context.Context, packet []byte) error {
	zt := z.transport()
	if zt == nil || !zt.IsRunning() {
		z.setOnline(false)
		return fmt.Errorf("zigbee interface %s is offline", z.config.Name)
	}
	if len(packet) > 100 {
		return fmt.Errorf("packet %d bytes exceeds ZigBee MTU 100 for %s", len(packet), z.config.Name)
	}

	if err := zt.Send(z.config.DstAddr, z.config.DstEndpoint, z.config.ClusterID, packet); err != nil {
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

// eventLoop follows the current transport: it subscribes to the one the
// provider returns and re-subscribes whenever the gateway swaps it.
func (z *ZigBeeInterface) eventLoop(ctx context.Context) {
	current := z.transport()
	var events chan transport.ZigBeeEvent
	if current != nil {
		events = current.Subscribe()
	}
	rebind := time.NewTicker(zigbeeRebindInterval)
	defer rebind.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-z.stopCh:
			return
		case <-rebind.C:
			zt := z.transport()
			if zt != current {
				current = zt
				events = nil
				if zt != nil {
					events = zt.Subscribe()
				}
				log.Info().Str("iface", z.config.Name).Bool("has_transport", zt != nil).
					Msg("zigbee iface: rebound to the gateway's current transport")
			}
			z.setOnline(zt != nil && zt.IsRunning())
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			z.setOnline(current != nil && current.IsRunning())
			if event.Type == "data" && len(event.Data) >= 2 {
				log.Debug().Str("iface", z.config.Name).Int("size", len(event.Data)).
					Uint16("cluster", event.ClusterID).Msg("zigbee iface: received reticulum packet")
				z.callback(event.Data)
			}
		}
	}
}
