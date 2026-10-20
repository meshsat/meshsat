package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"meshsat/internal/database"
	"meshsat/internal/directory"
	"meshsat/internal/types"
)

// Team send (HeMB-NG stage 2 C8): a team is a group of kind TEAM, named by
// id. Its members are resolved once, at submission, and frozen: one
// submission, one intent per member. A member no route admits gets an
// intent written closed and failed at once, with the reasons in its route
// plan; when no member has a route, nothing is written and the caller gets
// every member's reasons. Delivery intents only. [MESHSAT-1584]

// TeamResolver is what a team send needs of the directory: the contact ids
// of a TEAM group in order (directory.SQLStore.TeamMembers).
type TeamResolver interface {
	TeamMembers(ctx context.Context, groupID string) ([]string, error)
}

// conflictResolver names the contact that holds an address the Hub sent for
// a member (directory.SQLStore.AddressConflicts).
type conflictResolver interface {
	AddressConflicts(ctx context.Context, contactID string) ([]directory.AddressConflict, error)
}

var (
	// ErrTeamNeedsIntents: a team send exists on the delivery-intent path
	// only, and MESHSAT_DELIVERY_INTENTS is off.
	ErrTeamNeedsIntents = errors.New("a team send needs delivery intents (MESHSAT_DELIVERY_INTENTS)")
	// ErrEmptyTeam: the team has no members.
	ErrEmptyTeam = errors.New("the team has no members")

	errNoAddresses = errors.New("no addresses")
)

// reasonNoAddress is a member's reason when it has no address at all, or
// is no longer in the directory.
const reasonNoAddress = "skipped_no_address"

// MemberRefusal is a team member no route admits, and why.
type MemberRefusal struct {
	ContactID string
	Plan      []database.RoutePlanEntry
	Detail    string
}

// TeamNoRouteError: no member of the team has a route that admits. Nothing
// was written.
type TeamNoRouteError struct {
	Members []MemberRefusal
}

func (e *TeamNoRouteError) Error() string {
	var parts []string
	for _, m := range e.Members {
		var rs []string
		for _, p := range m.Plan {
			rs = append(rs, p.Kind+": "+p.Reason)
		}
		if m.Detail != "" {
			rs = append(rs, m.Detail)
		}
		parts = append(parts, m.ContactID+" ("+strings.Join(rs, "; ")+")")
	}
	return fmt.Sprintf("%s: no member of the team can be reached: %s", ErrNoRouteAdmits, strings.Join(parts, ", "))
}

func (e *TeamNoRouteError) Unwrap() error { return ErrNoRouteAdmits }

// SubmitTeam turns a team send into one submission with an intent per
// member, written in one transaction. The same source and idempotency key
// with the same request returns the original submission (its members as
// they were then); with a different request, database.ErrIdempotencyConflict.
func (e *Escalator) SubmitTeam(ctx context.Context, req IntentRequest) (*IntentSubmission, error) {
	if req.Source == "" {
		req.Source = "api"
	}
	if req.Precedence == "" {
		req.Precedence = types.DefaultPrecedence
	}
	req.ContactID = ""
	fp := req.fingerprint()
	if req.IdempotencyKey != "" {
		prev, err := e.db.GetSubmissionByKey(req.Source, req.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if prev != nil {
			if prev.RequestSHA256 != fp {
				return nil, database.ErrIdempotencyConflict
			}
			return e.loadSubmission(prev)
		}
	}
	if e.d == nil || e.d.recipientResolver == nil {
		return nil, errors.New("delivery intents: no recipient resolver")
	}
	teams, ok := e.d.recipientResolver.(TeamResolver)
	if !ok {
		return nil, errors.New("delivery intents: the directory cannot resolve teams")
	}
	members, err := teams.TeamMembers(ctx, req.GroupID)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, ErrEmptyTeam
	}
	subUUID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	subID := subUUID.String()
	var intents []database.NewIntent
	var refused []MemberRefusal
	for _, cid := range members {
		mreq := req
		mreq.ContactID, mreq.GroupID = cid, ""
		ni, nre, err := e.prepareIntent(ctx, mreq, subID)
		switch {
		case errors.Is(err, errNoAddresses) || errors.Is(err, directory.ErrNotFound):
			plan, detail, cerr := e.noAddressPlan(ctx, cid)
			if cerr != nil {
				return nil, cerr
			}
			ni = e.failedMemberIntent(mreq, subID, plan)
			refused = append(refused, MemberRefusal{ContactID: cid, Plan: plan, Detail: detail})
		case err != nil:
			return nil, err
		case nre != nil:
			ni = e.failedMemberIntent(mreq, subID, nre.Plan)
			ni.Intent.RequestedStrategy, ni.Intent.EffectiveStrategy = nre.requested, nre.effective
			refused = append(refused, MemberRefusal{ContactID: cid, Plan: nre.Plan, Detail: nre.Detail})
		}
		intents = append(intents, ni)
	}
	if len(refused) == len(members) {
		return nil, &TeamNoRouteError{Members: refused}
	}
	ids := make([]string, 0, len(intents))
	for _, ni := range intents {
		ids = append(ids, ni.Intent.ID)
	}
	sub := database.DeliverySubmission{ID: subID, Source: req.Source, IdempotencyKey: req.IdempotencyKey,
		RequestSHA256: fp, IntentIDs: ids}
	return e.create(sub, intents)
}

// noAddressPlan is the plan of a member with no address: the addresses the
// Hub sent for it that another contact holds, each naming the holder (r20
// F1: a generic "no address" hid the remedy), or just no address.
func (e *Escalator) noAddressPlan(ctx context.Context, contactID string) ([]database.RoutePlanEntry, string, error) {
	if cr, ok := e.d.recipientResolver.(conflictResolver); ok {
		conflicts, err := cr.AddressConflicts(ctx, contactID)
		if err != nil {
			return nil, "", err
		}
		if len(conflicts) > 0 {
			plan := make([]database.RoutePlanEntry, 0, len(conflicts))
			for i, c := range conflicts {
				plan = append(plan, database.RoutePlanEntry{RouteIndex: i, Kind: c.Kind, Destination: c.Value,
					Reason: reasonAddressHeld, HeldBy: c.HolderContactID})
			}
			return plan, "the Hub sent this contact's address but another contact holds it: remove it there " +
				"(DELETE /api/directory/contacts/{id}/addresses/{address_id}) and the Hub's next snapshot gives it here", nil
		}
	}
	return []database.RoutePlanEntry{{Reason: reasonNoAddress}}, "no address", nil
}

// failedMemberIntent is a member's intent that closes failed at submission,
// its route plan the reasons (transition T2, no attempt).
func (e *Escalator) failedMemberIntent(req IntentRequest, subID string, plan []database.RoutePlanEntry) database.NewIntent {
	id, _ := uuid.NewV7()
	sum := sha256.Sum256(req.Body)
	now := e.clock.Wall().UTC().Format("2006-01-02 15:04:05")
	return database.NewIntent{Intent: database.DeliveryIntent{
		ID:                id.String(),
		SubmissionID:      subID,
		ContactID:         req.ContactID,
		Precedence:        string(req.Precedence),
		Class:             database.DeliveryClassMessage,
		Payload:           append([]byte(nil), req.Body...),
		PayloadSHA256:     hex.EncodeToString(sum[:]),
		RequestedStrategy: string(req.Strategy),
		RoutePlan:         plan,
		Currency:          "EUR",
		ExpiryAllowS:      e.cfg.ExpiryAllowS,
		State:             database.IntentClosed,
		Outcome:           database.OutcomeFailed,
		ClosedAt:          &now,
		Version:           1,
		ActiveRound:       1,
	}}
}
