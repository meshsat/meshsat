package hubmsg

import (
	"sort"
)

// Event is one piece of evidence about an operation (C6), as published on a sender's
// msg/status. Source is where a party took it from ("local" for its own broker session,
// "status_topic" for its msg/status); it is the receiving party's annotation, never published.
type Event struct {
	V         int    `json:"v"`
	Source    string `json:"source,omitempty"`
	OpID      string `json:"op_id"`
	Mid       string `json:"mid"`
	Recipient string `json:"recipient"`
	EventID   string `json:"event_id"`
	Rev       int    `json:"rev"`
	Class     string `json:"class"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Authority string `json:"authority"`
	Scope     string `json:"scope"`
	Detail    string `json:"detail"`
	At        string `json:"at"`
}

// Operation is what an event must name to join an operation's evidence (C6).
type Operation struct {
	OpID      string `json:"op_id"`
	Mid       string `json:"mid"`
	Recipient string `json:"recipient"`
}

// Projection is an operation's state derived from its evidence (C6).
type Projection struct {
	State         string   `json:"state"`
	Conflict      bool     `json:"conflict"`
	FailedReasons []string `json:"failed_reasons"`
	Evidence      []string `json:"evidence"`
	Rejected      []string `json:"rejected"`
}

// The receipt kinds (C6a): which kind may carry which class under which authority.
var allowedKinds = map[[3]string]bool{
	{"hub_broker_puback", "accepted", "local"}:         true,
	{"hub_forwarded", "forwarded", "hub"}:              true,
	{"recipient_app_stored", "completed", "recipient"}: true,
	{"recipient_refused", "failed", "recipient"}:       true,
	{"recipient_refused", "expired", "recipient"}:      true,
	{"hub_refused", "failed", "hub"}:                   true,
	{"hub_expired", "expired", "hub"}:                  true,
	{"hub_unknown", "unknown", "hub"}:                  true,
	{"recipient_busy", "unknown", "recipient"}:         true,
}

// SourceOf is where evidence of each authority may come from (C6, r4).
var SourceOf = map[string]string{"local": "local", "hub": "status_topic", "recipient": "status_topic"}

// ProjectionOrder is the strength of the classes, strongest first.
var ProjectionOrder = []string{"completed", "failed", "expired", "forwarded", "unknown", "accepted", "pending"}

// Valid reports whether e may join op's evidence: the same operation, mid and recipient, a
// kind allowed for its class and authority, from the source its authority has, a v7 id.
func Valid(op Operation, e Event) bool {
	return e.OpID == op.OpID && e.Mid == op.Mid && e.Recipient == op.Recipient &&
		allowedKinds[[3]string{e.Kind, e.Class, e.Authority}] && SourceOf[e.Authority] != "" &&
		SourceOf[e.Authority] == e.Source && ValidV7(e.EventID)
}

// Reduce derives an operation's state from its events in any order and with any duplicates
// (C6): the set union of the valid events by event id; the state is the strongest class in
// it; completed and failed together mark a conflict; rev decides nothing.
func Reduce(op Operation, events []Event) Projection {
	held := map[string]Event{}
	rejected := map[string]bool{}
	for _, e := range events {
		if Valid(op, e) {
			if _, ok := held[e.EventID]; !ok {
				held[e.EventID] = e
			}
		} else {
			rejected[e.EventID] = true
		}
	}
	classes := map[string]bool{}
	reasons := map[string]bool{}
	for _, e := range held {
		classes[e.Class] = true
		if (e.Class == "failed" || e.Class == "expired") && e.Reason != "" {
			reasons[e.Reason] = true
		}
	}
	p := Projection{State: "pending", FailedReasons: []string{}, Evidence: []string{}, Rejected: []string{}}
	for _, c := range ProjectionOrder {
		if classes[c] {
			p.State = c
			break
		}
	}
	p.Conflict = classes["completed"] && classes["failed"]
	for r := range reasons {
		p.FailedReasons = append(p.FailedReasons, r)
	}
	for id := range held {
		p.Evidence = append(p.Evidence, id)
	}
	for id := range rejected {
		if _, ok := held[id]; !ok {
			p.Rejected = append(p.Rejected, id)
		}
	}
	sort.Strings(p.FailedReasons)
	sort.Strings(p.Evidence)
	sort.Strings(p.Rejected)
	return p
}
