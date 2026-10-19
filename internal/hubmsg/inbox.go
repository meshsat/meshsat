package hubmsg

import (
	"time"
)

// The receiver inbox (C3): what a delivery on this bridge's msg/in commits, and what the
// bridge answers on its msg/ack. The commit key is (origin, op_id), with an index
// (origin, mid); the message is stored once per (origin, mid).

// The inbox's bounds (C3, r3).
const (
	// SkewMs is the clock skew a commit allows past an operation's deadline.
	SkewMs = 120_000
	// RetentionAfterExpiryMs is how long after deadline plus skew an entry is kept, so a late
	// repeat of a committed operation is acknowledged, not refused as expired.
	RetentionAfterExpiryMs = 24 * 60 * 60 * 1000
	// Capacity is the target number of live entries. It is not an eviction rule: at capacity
	// with nothing past its retention, a new operation is refused receiver_busy.
	Capacity = 50_000
)

// InboxEntry is one committed operation.
type InboxEntry struct {
	Origin        string
	OpID          string
	Mid           string
	Recipient     string
	ContentType   string
	MidExpiresAt  int64
	ExpiresAt     int64
	ContentSHA256 string
	// MessageID is the stored message's row; operations of one (origin, mid) share it.
	MessageID int64
	// CommittedAt is the receiver's clock at the commit, Unix ms.
	CommittedAt int64
	// WithinSkew: committed after the deadline but within SkewMs. Unjudged: committed while
	// the receiver's clock was not established, so the deadline was not judged.
	WithinSkew bool
	Unjudged   bool
}

// InboxTx is the inbox inside one transaction: Commit's reads and its write are atomic.
type InboxTx interface {
	// ByOp is the entry under (origin, op_id), nil when there is none.
	ByOp(origin, opID string) (*InboxEntry, error)
	// ByMid is an entry of (origin, mid), nil when there is none.
	ByMid(origin, mid string) (*InboxEntry, error)
	// Count is the number of entries.
	Count() (int, error)
	// PruneBefore deletes the entries whose deadline plus skew plus retention is before
	// nowMs, and returns how many.
	PruneBefore(nowMs int64) (int, error)
	// Insert writes e; with store set it also stores the message (body) and a pending
	// hand-off for it, and sets e.MessageID.
	Insert(e *InboxEntry, store bool, body []byte) error
}

// Answer is what the receiver commits and answers for one delivery.
type Answer struct {
	Result     string `json:"result"` // "stored" or "refused"
	Reason     string `json:"reason"`
	StoredNow  bool   `json:"stored_now"`
	WithinSkew bool   `json:"accepted_within_skew,omitempty"`
	Unjudged   bool   `json:"expiry_unjudged,omitempty"`
	// MessageID is the stored message's row on a stored answer.
	MessageID int64 `json:"-"`
}

func refusedAnswer(reason string) Answer { return Answer{Result: "refused", Reason: reason} }

// Commit decides one delivery for recipient in the contract's order (C3): valid ids (C1e),
// the digest recomputed from the delivered fields (C1d), the deadline within the message's,
// a known (origin, op_id), a known (origin, mid), the deadline at commit, the capacity.
// clockEstablished is the clock guard's word: without it the deadline is not judged, because
// a wrong clock must not drop messages. capacity <= 0 is Capacity.
func Commit(tx InboxTx, d Delivery, now time.Time, clockEstablished bool, capacity int) (Answer, error) {
	if capacity <= 0 {
		capacity = Capacity
	}
	if !ValidV7(d.OpID) || !ValidV7(d.Mid) {
		return refusedAnswer("bad_id"), nil
	}
	digest, err := ContentSHA256(d.Mid, d.To, d.ContentType, d.MidExpiresAt, []byte(d.Body))
	if err != nil || digest != d.ContentSHA256 {
		return refusedAnswer("digest_mismatch"), nil
	}
	if d.ExpiresAt > d.MidExpiresAt {
		return refusedAnswer("bad_expiry"), nil
	}
	if e, err := tx.ByOp(d.From, d.OpID); err != nil {
		return Answer{}, err
	} else if e != nil {
		if e.Mid == d.Mid && e.ContentSHA256 == d.ContentSHA256 {
			// Committed before: acknowledged again, after its deadline too (it was in time).
			return Answer{Result: "stored", MessageID: e.MessageID}, nil
		}
		return refusedAnswer("op_conflict"), nil
	}
	known, err := tx.ByMid(d.From, d.Mid)
	if err != nil {
		return Answer{}, err
	}
	if known != nil && known.ContentSHA256 != d.ContentSHA256 {
		// Never an acknowledgement of the stored message for another payload.
		return refusedAnswer("mid_conflict"), nil
	}
	nowMs := now.UnixMilli()
	e := &InboxEntry{Origin: d.From, OpID: d.OpID, Mid: d.Mid, Recipient: d.To, ContentType: d.ContentType,
		MidExpiresAt: d.MidExpiresAt, ExpiresAt: d.ExpiresAt, ContentSHA256: d.ContentSHA256, CommittedAt: nowMs}
	switch {
	case !clockEstablished:
		e.Unjudged = true
	case nowMs > d.ExpiresAt+SkewMs:
		return refusedAnswer("expired_at_recipient"), nil
	case nowMs > d.ExpiresAt:
		e.WithinSkew = true
	}
	n, err := tx.Count()
	if err != nil {
		return Answer{}, err
	}
	if n >= capacity {
		// An entry past its retention goes first; a live entry is never evicted.
		if _, err := tx.PruneBefore(nowMs); err != nil {
			return Answer{}, err
		}
		if n, err = tx.Count(); err != nil {
			return Answer{}, err
		}
		if n >= capacity {
			return refusedAnswer("receiver_busy"), nil
		}
	}
	store := known == nil
	if !store {
		e.MessageID = known.MessageID
	}
	if err := tx.Insert(e, store, []byte(d.Body)); err != nil {
		return Answer{}, err
	}
	return Answer{Result: "stored", StoredNow: store, WithinSkew: e.WithinSkew, Unjudged: e.Unjudged, MessageID: e.MessageID}, nil
}

// RetainedUntil is when an entry with this deadline may go.
func RetainedUntil(expiresAt int64) int64 { return expiresAt + SkewMs + RetentionAfterExpiryMs }
