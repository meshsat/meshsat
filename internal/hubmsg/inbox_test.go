package hubmsg_test

import (
	"testing"
	"time"

	"meshsat/internal/hubmsg"
	"meshsat/internal/hubmsg/hubmsgtest"
)

// memInbox is the inbox in memory: the reference the database inbox is held to.
type memInbox struct {
	entries []hubmsg.InboxEntry
	nextMsg int64
}

func (m *memInbox) ByOp(origin, opID string) (*hubmsg.InboxEntry, error) {
	for i := range m.entries {
		if m.entries[i].Origin == origin && m.entries[i].OpID == opID {
			e := m.entries[i]
			return &e, nil
		}
	}
	return nil, nil
}

func (m *memInbox) ByMid(origin, mid string) (*hubmsg.InboxEntry, error) {
	for i := range m.entries {
		if m.entries[i].Origin == origin && m.entries[i].Mid == mid {
			e := m.entries[i]
			return &e, nil
		}
	}
	return nil, nil
}

func (m *memInbox) Count() (int, error) { return len(m.entries), nil }

func (m *memInbox) PruneBefore(nowMs int64) (int, error) {
	keep := m.entries[:0]
	n := 0
	for _, e := range m.entries {
		if hubmsg.RetainedUntil(e.ExpiresAt) < nowMs {
			n++
			continue
		}
		keep = append(keep, e)
	}
	m.entries = keep
	return n, nil
}

func (m *memInbox) Insert(e *hubmsg.InboxEntry, store bool, _ []byte) error {
	if store {
		m.nextMsg++
		e.MessageID = m.nextMsg
	}
	m.entries = append(m.entries, *e)
	return nil
}

func TestFixtureInboxInMemory(t *testing.T) {
	hubmsgtest.RunInboxCases(t, hubmsgtest.LoadInboxCases(t), func(testing.TB) func(hubmsg.Delivery, time.Time, bool, int) (hubmsg.Answer, error) {
		m := &memInbox{}
		return func(d hubmsg.Delivery, now time.Time, est bool, capacity int) (hubmsg.Answer, error) {
			return hubmsg.Commit(m, d, now, est, capacity)
		}
	})
}

// Two operations of one message share the stored message; a repeat names it too.
func TestTheSecondOperationOfAMessageNamesTheStoredOne(t *testing.T) {
	m := &memInbox{}
	now := time.UnixMilli(1792330000000)
	mid := hubmsg.NewV7(now)
	mk := func(op string) hubmsg.Delivery {
		s, _ := hubmsg.NewSubmission(op, mid, "bridge:kit-b", "x", now.Add(time.Hour).UnixMilli(), now.Add(10*time.Minute).UnixMilli())
		return hubmsg.Delivery{V: 1, OpID: s.OpID, Mid: s.Mid, From: "bridge:kit-a", To: s.To, ContentType: s.ContentType,
			MidExpiresAt: s.MidExpiresAt, ExpiresAt: s.ExpiresAt, ContentSHA256: s.ContentSHA256, Body: s.Body}
	}
	first, _ := hubmsg.Commit(m, mk(hubmsg.NewV7(now)), now, true, 0)
	second, _ := hubmsg.Commit(m, mk(hubmsg.NewV7(now)), now, true, 0)
	again, _ := hubmsg.Commit(m, mk(m.entries[1].OpID), now, true, 0)
	if !first.StoredNow || second.StoredNow || first.MessageID == 0 || second.MessageID != first.MessageID || again.MessageID != first.MessageID {
		t.Fatalf("first %+v second %+v again %+v", first, second, again)
	}
}
