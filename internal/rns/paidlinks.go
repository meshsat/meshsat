package rns

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Paid links [MESHSAT-1547].
//
// A paid bearer (Iridium, SMS) never appears in Transmitter.Floodable, so no
// announce, path request or pathless packet is ever broadcast on it. That
// keeps a busy mesh from spending satellite messages on background traffic,
// but it also means a destination behind the satellite can never be learned:
// its announce never crosses, and a path request for it never goes out.
//
// A PaidPolicy opens one paid interface for a short list of destinations.
// Their announces, path requests and path responses cross it, each at most
// once per MinInterval per destination; everything else stays off it. Both
// ends of the link should list the same destinations: a path request that
// arrives over the link is only answered for a destination on this node's
// list. Packets along a known path were never affected and still are not.

// PaidPoliciesConfigKey is the system_config key the policies persist under.
const PaidPoliciesConfigKey = "reticulum_paid_policies"

// DefaultPaidMinInterval spaces announces and path requests for one
// destination on one paid interface when a policy names no interval.
const DefaultPaidMinInterval = 5 * time.Minute

// PaidPolicy lets the listed destinations cross one paid interface.
type PaidPolicy struct {
	Dests [][HashLen]byte
	// MinInterval between two announces (or two path requests) for the same
	// destination on this interface; 0 means DefaultPaidMinInterval.
	MinInterval time.Duration
	// Bitrate for the announce cap on this interface when the transmitter
	// reports none; 0 leaves the cap off.
	Bitrate int
}

func (p *PaidPolicy) allows(dest [HashLen]byte) bool {
	for _, d := range p.Dests {
		if d == dest {
			return true
		}
	}
	return false
}

type paidKind byte

const (
	paidAnnounce    paidKind = iota // announces and path responses
	paidPathRequest                 // path requests
)

type paidKey struct {
	iface string
	dest  [HashLen]byte
	kind  paidKind
}

// onlineChecker is implemented by transmitters that know whether an
// interface is up; without it every paid interface counts as online.
type onlineChecker interface {
	Online(ifaceID string) bool
}

// SetPaidPolicy installs (or with nil or an empty list removes) the policy
// for one paid interface. The interface must stay out of Floodable.
func (n *Node) SetPaidPolicy(ifaceID string, p *PaidPolicy) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if p == nil || len(p.Dests) == 0 {
		delete(n.paid, ifaceID)
		return
	}
	cp := *p
	cp.Dests = append([][HashLen]byte(nil), p.Dests...)
	if cp.MinInterval <= 0 {
		cp.MinInterval = DefaultPaidMinInterval
	}
	n.paid[ifaceID] = &cp
}

// PaidPolicies returns a copy of the installed policies.
func (n *Node) PaidPolicies() map[string]PaidPolicy {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]PaidPolicy, len(n.paid))
	for id, p := range n.paid {
		cp := *p
		cp.Dests = append([][HashLen]byte(nil), p.Dests...)
		out[id] = cp
	}
	return out
}

// paidTake returns the paid interfaces, other than except, that dest may
// cross now, and books the send on each. Called without n.mu held.
func (n *Node) paidTake(dest [HashLen]byte, kind paidKind, except string) []string {
	n.mu.Lock()
	if len(n.paid) == 0 {
		n.mu.Unlock()
		return nil
	}
	ids := make([]string, 0, len(n.paid))
	for id, p := range n.paid {
		if id != except && p.allows(dest) {
			ids = append(ids, id)
		}
	}
	n.mu.Unlock()
	sort.Strings(ids)
	oc, _ := n.tx.(onlineChecker)
	now := n.now()
	out := ids[:0]
	for _, id := range ids {
		if oc != nil && !oc.Online(id) {
			continue
		}
		if n.paidBook(id, dest, kind, now) {
			out = append(out, id)
		}
	}
	return out
}

// paidGate decides one send on a named interface: true for an interface
// without a policy, otherwise only for a listed destination whose interval
// has passed (and then the send is booked). Called without n.mu held.
func (n *Node) paidGate(ifaceID string, dest [HashLen]byte, kind paidKind) bool {
	n.mu.Lock()
	p := n.paid[ifaceID]
	if p == nil {
		n.mu.Unlock()
		return true
	}
	if !p.allows(dest) {
		n.stats.PaidSuppressed++
		n.mu.Unlock()
		return false
	}
	n.mu.Unlock()
	return n.paidBook(ifaceID, dest, kind, n.now())
}

func (n *Node) paidBook(ifaceID string, dest [HashLen]byte, kind paidKind, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.paid[ifaceID]
	if p == nil {
		return false
	}
	k := paidKey{iface: ifaceID, dest: dest, kind: kind}
	if last, ok := n.paidLast[k]; ok && now.Sub(last) < p.MinInterval {
		n.stats.PaidSuppressed++
		return false
	}
	n.paidLast[k] = now
	n.stats.PaidTx++
	return true
}

// announceBitrate is the bit rate the announce cap uses on an interface.
func (n *Node) announceBitrate(ifaceID string) int {
	if b := n.tx.Bitrate(ifaceID); b > 0 {
		return b
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if p := n.paid[ifaceID]; p != nil {
		return p.Bitrate
	}
	return 0
}

// PaidPolicyJSON is the persisted and API form of one policy.
type PaidPolicyJSON struct {
	Dests          []string `json:"dests"`
	MinIntervalSec int      `json:"min_interval_sec"`
	Bitrate        int      `json:"bitrate"`
}

// ToJSON renders a policy with hex destination hashes.
func (p PaidPolicy) ToJSON() PaidPolicyJSON {
	out := PaidPolicyJSON{Dests: make([]string, 0, len(p.Dests)), MinIntervalSec: int(p.MinInterval / time.Second), Bitrate: p.Bitrate}
	for _, d := range p.Dests {
		out.Dests = append(out.Dests, hex.EncodeToString(d[:]))
	}
	return out
}

// ParsePaidPolicy validates the JSON form: every destination is 32 hex
// characters, duplicates are dropped, the interval and bit rate are not
// negative.
func ParsePaidPolicy(j PaidPolicyJSON) (*PaidPolicy, error) {
	if j.MinIntervalSec < 0 || j.Bitrate < 0 {
		return nil, fmt.Errorf("min_interval_sec and bitrate must not be negative")
	}
	p := &PaidPolicy{MinInterval: time.Duration(j.MinIntervalSec) * time.Second, Bitrate: j.Bitrate}
	seen := map[[HashLen]byte]bool{}
	for _, s := range j.Dests {
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != HashLen {
			return nil, fmt.Errorf("destination %q is not %d hex characters", s, 2*HashLen)
		}
		var d [HashLen]byte
		copy(d[:], b)
		if !seen[d] {
			seen[d] = true
			p.Dests = append(p.Dests, d)
		}
	}
	return p, nil
}

// MarshalPaidPolicies renders the installed policies for system_config.
func (n *Node) MarshalPaidPolicies() (string, error) {
	m := map[string]PaidPolicyJSON{}
	for id, p := range n.PaidPolicies() {
		m[id] = p.ToJSON()
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// LoadPaidPolicies installs policies from their system_config form and
// returns how many were installed. A malformed entry is skipped.
func (n *Node) LoadPaidPolicies(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	var m map[string]PaidPolicyJSON
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return 0, err
	}
	count := 0
	for id, j := range m {
		p, err := ParsePaidPolicy(j)
		if err != nil || len(p.Dests) == 0 {
			continue
		}
		n.SetPaidPolicy(id, p)
		count++
	}
	return count, nil
}
