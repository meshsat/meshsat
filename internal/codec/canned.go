package codec

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

const (
	// HeaderCanned is the magic prefix for a canned message frame.
	HeaderCanned byte = 0xCA
)

// DefaultCodebook is the built-in military brevity codebook.
var DefaultCodebook = map[uint8]string{
	1:  "Copy.",
	2:  "Roger.",
	3:  "Negative.",
	4:  "Affirmative.",
	5:  "Stand by.",
	6:  "All clear.",
	7:  "Moving out.",
	8:  "Returning to base.",
	9:  "Position confirmed.",
	10: "Mission complete.",
	11: "Need resupply.",
	12: "Requesting backup.",
	13: "Medical emergency.",
	14: "Evacuate immediately.",
	15: "Hold position.",
	16: "Proceed to waypoint.",
	17: "Enemy contact.",
	18: "All personnel accounted for.",
	19: "Weather deteriorating.",
	20: "Low battery warning.",
	21: "Signal lost.",
	22: "Relay message.",
	23: "Check in.",
	24: "Going silent.",
	25: "SOS — need immediate help.",
	26: "Camp established.",
	27: "Trail blocked — rerouting.",
	28: "Water source found.",
	29: "Shelter located.",
	30: "Search area clear — no findings.",
}

// Codebook holds a forward and reverse lookup table for canned messages.
type Codebook struct {
	mu      sync.RWMutex
	forward map[uint8]string
	reverse map[string]uint8
}

// NewCodebook creates a Codebook from the given forward map.
func NewCodebook(m map[uint8]string) *Codebook {
	cb := &Codebook{
		forward: make(map[uint8]string, len(m)),
		reverse: make(map[string]uint8, len(m)),
	}
	for id, text := range m {
		cb.forward[id] = text
		cb.reverse[text] = id
	}
	return cb
}

// defaultCB is the singleton codebook used by the package-level functions.
var defaultCB = NewCodebook(DefaultCodebook)

// LoadCodebookFromFile reads a JSON file mapping string IDs to message text
// and returns a Codebook. The JSON format is {"1": "Copy.", "2": "Roger.", ...}.
func LoadCodebookFromFile(path string) (*Codebook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[uint8]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return NewCodebook(raw), nil
}

// EncodeCanned returns the 2-byte wire frame for the given message ID.
func EncodeCanned(id uint8) []byte {
	return []byte{HeaderCanned, id}
}

// DecodeCanned decodes a canned message frame using the default codebook.
func DecodeCanned(data []byte) (string, error) {
	return defaultCB.Decode(data)
}

// IsCanned reports whether the data starts with the canned message header.
func IsCanned(data []byte) bool {
	return len(data) >= 1 && data[0] == HeaderCanned
}

// LookupByText returns the message ID for the given text in the default codebook.
func LookupByText(text string) (uint8, bool) {
	return defaultCB.LookupByText(text)
}

// Decode decodes a canned message frame using this codebook.
func (cb *Codebook) Decode(data []byte) (string, error) {
	if len(data) < 2 {
		return "", errors.New("codec: canned data too short")
	}
	if data[0] != HeaderCanned {
		return "", errors.New("codec: invalid canned header")
	}
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	msg, ok := cb.forward[data[1]]
	if !ok {
		return "", errors.New("codec: unknown canned message ID")
	}
	return msg, nil
}

// LookupByText returns the message ID for the given text.
func (cb *Codebook) LookupByText(text string) (uint8, bool) {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	id, ok := cb.reverse[text]
	return id, ok
}
