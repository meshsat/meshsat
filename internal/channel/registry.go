package channel

import (
	"fmt"
	"sync"
)

// Registry holds all known transport channels.
type Registry struct {
	mu       sync.RWMutex
	channels map[string]ChannelDescriptor
	order    []string // insertion order for List()
}

// NewRegistry creates an empty channel registry.
func NewRegistry() *Registry {
	return &Registry{
		channels: make(map[string]ChannelDescriptor),
	}
}

// Register adds a channel descriptor. Returns error if ID already registered.
func (r *Registry) Register(d ChannelDescriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.channels[d.ID]; exists {
		return fmt.Errorf("channel %q already registered", d.ID)
	}
	r.channels[d.ID] = d
	r.order = append(r.order, d.ID)
	return nil
}

// Get returns the descriptor for a channel ID. Returns false if not found.
func (r *Registry) Get(id string) (ChannelDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.channels[id]
	return d, ok
}

// List returns all registered channels in registration order.
func (r *Registry) List() []ChannelDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ChannelDescriptor, 0, len(r.order))
	for _, id := range r.order {
		result = append(result, r.channels[id])
	}
	return result
}

// IDs returns all registered channel IDs in registration order.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// IsPaid returns true if the channel is a paid transport.
func (r *Registry) IsPaid(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.channels[id]
	return ok && d.IsPaid
}

// CanSend returns true if the channel can be a rule destination.
func (r *Registry) CanSend(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.channels[id]
	return ok && d.CanSend
}

// CanReceive returns true if the channel can be a rule source.
func (r *Registry) CanReceive(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.channels[id]
	return ok && d.CanReceive
}
