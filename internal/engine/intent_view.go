package engine

import (
	"fmt"
	"strings"

	"meshsat/internal/database"
)

// IntentView is a delivery intent as the status API shows it (architecture
// r3 D11): the top-level outcome with its words, notes (possible duplicate,
// may still be sent), and per route what is known, attempt by attempt.
// [MESHSAT-1581]
type IntentView struct {
	database.DeliveryIntent
	Text   string      `json:"text"`
	Label  string      `json:"label"`
	Notes  []string    `json:"notes,omitempty"`
	Routes []RouteView `json:"routes"`
}

// RouteView is one route of the plan with its attempts and the facts they
// established.
type RouteView struct {
	database.RoutePlanEntry
	Facts    []string      `json:"facts,omitempty"`
	Attempts []AttemptView `json:"attempts,omitempty"`
}

// AttemptView is one attempt row with its transmissions and evidence.
type AttemptView struct {
	DeliveryID    int64                           `json:"delivery_id"`
	Round         int                             `json:"round"`
	AttemptNo     int                             `json:"attempt_no"`
	Status        string                          `json:"status"`
	Reason        string                          `json:"reason,omitempty"`
	LastError     string                          `json:"last_error,omitempty"`
	AcceptedAt    *string                         `json:"accepted_at,omitempty"`
	Transmissions []database.DeliveryTransmission `json:"transmissions"`
	Evidence      []database.DeliveryEvidence     `json:"evidence,omitempty"`
}

// outcomeLabel is r3 D11's wording for an outcome.
func outcomeLabel(o string) string {
	switch o {
	case database.OutcomePending:
		return "sending"
	case database.OutcomeDeliveredHandset:
		return "delivered to handset"
	case database.OutcomeDeliveredDevice:
		return "delivered to device"
	case database.OutcomeAcceptedDestinationNode:
		return "accepted by the recipient's Reticulum node (signed)"
	case database.OutcomeAckedStation:
		return "acked by station (unauthenticated)"
	case database.OutcomeAcceptedUnconfirmed:
		return "accepted, not confirmed"
	case database.OutcomeMayHaveBeenSent:
		return "may have been sent"
	case database.OutcomeFailed:
		return "not sent"
	case database.OutcomeExpired:
		return "expired"
	case database.OutcomeCancelled:
		return "cancelled"
	}
	return o
}

// BuildIntentView turns a snapshot into the status the API returns.
func BuildIntentView(s *database.IntentSnapshot) IntentView {
	v := IntentView{DeliveryIntent: s.Intent, Text: string(s.Intent.Payload), Label: outcomeLabel(s.Intent.Outcome)}
	txByRow := map[int64][]database.DeliveryTransmission{}
	for _, t := range s.Transmissions {
		txByRow[t.AttemptID] = append(txByRow[t.AttemptID], t)
	}
	evByRow := map[int64][]database.DeliveryEvidence{}
	for _, e := range s.Evidence {
		evByRow[e.AttemptID] = append(evByRow[e.AttemptID], e)
	}
	inFlight := map[string]bool{}
	for _, p := range s.Intent.RoutePlan {
		rv := RouteView{RoutePlanEntry: p}
		facts := map[string]bool{}
		addFact := func(f string) {
			if !facts[f] {
				facts[f] = true
				rv.Facts = append(rv.Facts, f)
			}
		}
		if !p.Admitted {
			addFact("not used: " + p.Reason)
		}
		for _, r := range s.Rows {
			if r.RouteIndex != p.RouteIndex {
				continue
			}
			av := AttemptView{DeliveryID: r.ID, Round: r.Round, AttemptNo: r.AttemptNo, Status: r.Status,
				Reason: r.Reason, LastError: r.LastError, AcceptedAt: r.AcceptedAt,
				Transmissions: txByRow[r.ID], Evidence: evByRow[r.ID]}
			dropped := droppedTransmissions(s)
			for _, e := range av.Evidence {
				if e.Matched && e.Completes != "" {
					addFact(outcomeLabel(e.Completes))
				}
				if e.Matched && strings.HasPrefix(e.Event, "dropped:") && dropped[e.TransmissionID] {
					addFact(fmt.Sprintf("not sent: the radio on %s refused it (%s)", p.InterfaceID, strings.TrimPrefix(e.Event, "dropped:")))
				}
			}
			for _, t := range av.Transmissions {
				switch {
				case dropped[t.ID]:
					// Handed to the radio, which sent nothing (above).
				case t.State == database.TxHandedOff:
					inFlight[p.InterfaceID] = true
					addFact("may have been sent on " + p.InterfaceID)
				case t.OutcomeClass == database.ClassAccepted && t.AcceptedBy != "destination_node":
					addFact(fmt.Sprintf("accepted by %s on %s", t.AcceptedBy, p.InterfaceID))
				case t.OutcomeClass == database.ClassUnknown:
					addFact("may have been sent on " + p.InterfaceID)
				}
			}
			switch r.Status {
			case "sending":
				inFlight[p.InterfaceID] = true
			case database.AttemptCancelled:
				addFact("cancelled")
			case database.AttemptSuperseded:
				if r.Reason == reasonOffline {
					// Down, or tried and not out (a 9704 with no sky): not
					// "offline" alone. [MESHSAT-1584]
					addFact("skipped: not sent within its queue allowance")
				}
			case database.AttemptNotSent:
				addFact("not sent: " + r.Reason)
			}
			rv.Attempts = append(rv.Attempts, av)
		}
		v.Routes = append(v.Routes, rv)
	}
	if s.Intent.PossibleDuplicate {
		v.Notes = append(v.Notes, "possible duplicate")
	}
	if s.Intent.Outcome == database.OutcomeCancelled || s.Intent.Outcome == database.OutcomeExpired ||
		(s.Intent.State == database.IntentClosed && len(inFlight) > 0) {
		for iface := range inFlight {
			v.Notes = append(v.Notes, "may still be sent on "+iface)
		}
	}
	return v
}
