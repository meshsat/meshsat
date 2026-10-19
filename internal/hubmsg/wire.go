package hubmsg

import (
	"encoding/json"
	"strings"
)

// ContentTypeText is the one content type of stage 2.
const ContentTypeText = "text/plain;charset=utf-8"

// MaxBody is the largest body the Hub admits, in bytes.
const MaxBody = 16 * 1024

// Submission is what a sender publishes on its own msg/out (C4). Body is the text, a JSON
// string: its UTF-8 bytes are the bytes the recipient stores and the digest covers.
type Submission struct {
	V             int    `json:"v"`
	OpID          string `json:"op_id"`
	Mid           string `json:"mid"`
	To            string `json:"to"`
	ContentType   string `json:"content_type"`
	MidExpiresAt  int64  `json:"mid_expires_at"`
	ExpiresAt     int64  `json:"expires_at"`
	ContentSHA256 string `json:"content_sha256"`
	Body          string `json:"body"`
}

// NewSubmission builds a submission with its digest.
func NewSubmission(opID, mid, to, body string, midExpiresAt, expiresAt int64) (Submission, error) {
	s := Submission{V: 1, OpID: opID, Mid: mid, To: to, ContentType: ContentTypeText,
		MidExpiresAt: midExpiresAt, ExpiresAt: expiresAt, Body: body}
	d, err := ContentSHA256(mid, to, ContentTypeText, midExpiresAt, []byte(body))
	if err != nil {
		return Submission{}, err
	}
	s.ContentSHA256 = d
	return s, nil
}

// Delivery is what the Hub publishes on a recipient's msg/in: the submission's fields and the
// principal it came from, the origin of the recipient's commit key (C3).
type Delivery struct {
	V             int    `json:"v"`
	OpID          string `json:"op_id"`
	Mid           string `json:"mid"`
	From          string `json:"from"`
	To            string `json:"to"`
	ContentType   string `json:"content_type"`
	MidExpiresAt  int64  `json:"mid_expires_at"`
	ExpiresAt     int64  `json:"expires_at"`
	ContentSHA256 string `json:"content_sha256"`
	Body          string `json:"body"`
}

// Ack is the recipient's answer on its own msg/ack (C5), naming the origin it committed under.
type Ack struct {
	V      int    `json:"v"`
	OpID   string `json:"op_id"`
	Mid    string `json:"mid"`
	From   string `json:"from"`
	Result string `json:"result"` // "stored" or "refused"
	Reason string `json:"reason"`
}

// Refusal is the Hub's answer to a submission it did not admit, on the sender's msg/status.
// It is not evidence of an operation: no reducer takes it in. On the sender it ends the
// attempt (the Hub did not take the message).
type Refusal struct {
	V      int    `json:"v"`
	Type   string `json:"type"` // "refusal"
	OpID   string `json:"op_id"`
	Mid    string `json:"mid"`
	Reason string `json:"reason"`
}

// StatusMessage is one msg/status payload: an event, or a refusal when Type is "refusal".
type StatusMessage struct {
	Event
	Type string `json:"type"`
}

// ParseStatus reads one msg/status payload. ok is false for a payload that is neither.
func ParseStatus(payload []byte) (ev *Event, ref *Refusal, ok bool) {
	var m StatusMessage
	if err := json.Unmarshal(payload, &m); err != nil || m.V != 1 {
		return nil, nil, false
	}
	if m.Type == "refusal" {
		var r Refusal
		if err := json.Unmarshal(payload, &r); err != nil {
			return nil, nil, false
		}
		return nil, &r, true
	}
	if m.Type != "" {
		return nil, nil, false
	}
	e := m.Event
	e.Source = "" // the receiving party annotates where it took the event from
	return &e, nil, true
}

// BridgeOf returns the bridge id of a "bridge:<id>" principal, or "".
func BridgeOf(principal string) string {
	id, ok := strings.CutPrefix(principal, "bridge:")
	if !ok || id == "" || strings.ContainsAny(id, "/+#") {
		return ""
	}
	return id
}

// Principal is the principal of a bridge id.
func Principal(bridgeID string) string { return "bridge:" + bridgeID }
