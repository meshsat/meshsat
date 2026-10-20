package database

// Delivery intents (HeMB-NG stage 1a, architecture r3 D2-D8, D11, D18 and
// the implementation requirements of 16 Oct 2026, G1 and G2).
//
// A submission is one send-to-contact request. It holds one intent per
// recipient. An intent holds the message, its route plan (frozen at
// submission) and its state; its attempts are ordinary delivery rows with
// intent_id set. A transmission is one physical submission of an attempt:
// generation gen of the claim the worker holds. Evidence is what came back.
//
// Only the Escalator (engine/escalator.go) changes an intent or creates or
// retires its rows; the delivery worker sends one row and finalizes one
// transmission. Every function here that changes more than one record does
// it in one transaction. With one database connection (database.go), a
// transaction holds the only connection: code inside one must use the
// transaction and never db, or it waits on itself forever. [MESHSAT-1581]

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Intent states.
const (
	IntentActive = "active"
	IntentClosed = "closed"
)

// Intent outcomes (r3 D11).
const (
	OutcomePending                 = "pending"
	OutcomeDeliveredHandset        = "delivered_handset"
	OutcomeDeliveredDevice         = "delivered_device"
	OutcomeAcceptedDestinationNode = "accepted_destination_node"
	OutcomeAckedStation            = "acked_station"
	OutcomeAcceptedUnconfirmed     = "accepted_unconfirmed"
	OutcomeMayHaveBeenSent         = "may_have_been_sent"
	OutcomeFailed                  = "failed"
	OutcomeExpired                 = "expired"
	OutcomeCancelled               = "cancelled"
	// OutcomeDeliveredApp: the recipient's MeshSat app stored it and said so through the
	// Hub (hub_msg, C6a recipient_app_stored). [MESHSAT-1584]
	OutcomeDeliveredApp = "delivered_app"
)

// Outcome classes of a transmission (r3 D18).
const (
	ClassNotSent  = "not_sent"
	ClassAccepted = "accepted"
	ClassUnknown  = "unknown"
)

// Transmission states (r3 D5).
const (
	TxAuthorized = "authorized"
	TxHandedOff  = "handed_off"
	TxDone       = "done"
)

// Statuses of an intent's attempt row. queued, retry and sending are the
// ledger's own; the others are an attempt's ends.
const (
	AttemptAccepted   = "accepted"
	AttemptNotSent    = "not_sent"
	AttemptUnknown    = "unknown"
	AttemptSuperseded = "superseded"
	AttemptCancelled  = "cancelled"
)

var (
	// ErrIntentAttempt is CancelDelivery's answer for an intent's row.
	ErrIntentAttempt = errors.New("an attempt of a delivery intent")
	// ErrIdempotencyConflict: the key was used for a different request.
	ErrIdempotencyConflict = errors.New("idempotency key already used for a different request")
	// ErrClaimLost: the row is no longer sending under this claim.
	ErrClaimLost = errors.New("the attempt is no longer claimed under this generation")
	// ErrOverCap: the reservation does not fit the intent's cap.
	ErrOverCap = errors.New("the intent's cost cap does not allow this submission")
	// ErrBadAmount: a reservation or a charge that is negative, which no
	// valid tariff produces (B9SMS-R5). [MESHSAT-1584]
	ErrBadAmount = errors.New("a negative amount is never reserved or charged")
	// ErrIntentNotFound: no intent with that id.
	ErrIntentNotFound = errors.New("delivery intent not found")
)

// RoutePlanEntry is one route of an intent's plan, frozen at submission.
type RoutePlanEntry struct {
	RouteIndex      int    `json:"route_index"`
	Kind            string `json:"kind"`
	AddressID       string `json:"address_id,omitempty"`
	InterfaceID     string `json:"interface_id,omitempty"`
	Destination     string `json:"destination,omitempty"`
	AdapterMode     string `json:"adapter_mode,omitempty"` // mesh_unicast, lxmf
	TransformSHA256 string `json:"transform_sha256,omitempty"`
	EstWireBytes    int    `json:"est_wire_bytes"`
	EstUnits        int    `json:"est_units"`
	EstMinor        int64  `json:"est_minor,omitempty"` // one submission at the tariff of admission (B9)
	UnitKind        string `json:"unit_kind,omitempty"`
	TariffID        int64  `json:"tariff_id,omitempty"`
	TariffVersion   int    `json:"tariff_version,omitempty"`
	Paid            bool   `json:"paid"`
	// CompletionRule names the outcome a matched receipt on this route
	// establishes (delivered_device, accepted_destination_node); empty
	// when the route has no receipt that proves arrival.
	CompletionRule string `json:"completion_rule,omitempty"`
	// WireTimestampMs is the LXMF timestamp of every attempt on this
	// route, allocated once at submission (AllocateWireTimestampMs): part
	// of the message hash, so the intent's retries are one message, and
	// no two intents share one, so two intentional messages with the same
	// text stay two (r3 D10). [MESHSAT-1584]
	WireTimestampMs int64 `json:"wire_ts_ms,omitempty"`
	QueueAllowS     int   `json:"queue_allow_s"`
	ReceiptAllowS   int   `json:"receipt_allow_s"`
	// EscalateUnconfirmed: escalate after an accepted, unconfirmed
	// attempt on this route once its receipt allowance has passed (r3 D6,
	// off by default). A route with a completion rule always may (T5).
	EscalateUnconfirmed bool `json:"escalate_unconfirmed,omitempty"`
	Admitted            bool `json:"admitted"`
	// Reason says why a route was not admitted (skipped_*).
	Reason string `json:"reason,omitempty"`
}

// DeliverySubmission is one send request (r3 D2).
type DeliverySubmission struct {
	ID             string   `json:"id"`
	Source         string   `json:"source"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
	RequestSHA256  string   `json:"request_sha256"`
	IntentIDs      []string `json:"intent_ids"`
	CreatedAt      string   `json:"created_at"`
	// Mode is SubmissionIntent, or SubmissionLegacy for a send made
	// outside delivery intents, whose LegacyResult (JSON) holds what it
	// queued. [MESHSAT-1584]
	Mode         string `json:"mode"`
	LegacyResult string `json:"legacy_result,omitempty"`
}

// Submission modes.
const (
	SubmissionIntent = "intent"
	SubmissionLegacy = "legacy"
)

// DeliveryIntent is one recipient's message and its state (r3 D2).
type DeliveryIntent struct {
	ID                   string           `json:"id"`
	SubmissionID         string           `json:"submission_id"`
	ContactID            string           `json:"contact_id"`
	Precedence           string           `json:"precedence"`
	Class                string           `json:"class"`
	Payload              []byte           `json:"-"`
	PayloadSHA256        string           `json:"payload_sha256"`
	RequestedStrategy    string           `json:"requested_strategy"`
	EffectiveStrategy    string           `json:"effective_strategy"`
	StrategyReason       string           `json:"strategy_reason"`
	PolicyVersion        string           `json:"policy_version"`
	RoutePlan            []RoutePlanEntry `json:"route_plan"`
	State                string           `json:"state"`
	Outcome              string           `json:"outcome"`
	PossibleDuplicate    bool             `json:"possible_duplicate"`
	Version              int              `json:"version"`
	ActiveRound          int              `json:"active_round"`
	RoundStartedMs       int64            `json:"round_started_ms"`
	CapMinor             *int64           `json:"cap_minor,omitempty"`
	Currency             string           `json:"currency"`
	ReservedMinor        int64            `json:"reserved_minor"`
	IncurredMinor        int64            `json:"incurred_minor"`
	PossibleMinor        int64            `json:"possible_minor"`
	ElapsedMs            int64            `json:"elapsed_ms"`
	HeartbeatWallAt      *string          `json:"heartbeat_wall_at,omitempty"`
	HeartbeatWallTrusted bool             `json:"heartbeat_wall_trusted"`
	ExpiryAllowS         int              `json:"expiry_allow_s"`
	ExpiresAt            *string          `json:"expires_at,omitempty"`
	CancelledAt          *string          `json:"cancelled_at,omitempty"`
	CancelReason         string           `json:"cancel_reason,omitempty"`
	LastTransition       string           `json:"last_transition"`
	CreatedAt            string           `json:"created_at"`
	UpdatedAt            string           `json:"updated_at"`
	ClosedAt             *string          `json:"closed_at,omitempty"`
}

// DeliveryTransmission is one physical submission of an attempt (r3 D3).
type DeliveryTransmission struct {
	ID              int64   `json:"id"`
	AttemptID       int64   `json:"attempt_id"`
	IntentID        string  `json:"intent_id"`
	Gen             int     `json:"gen"`
	State           string  `json:"state"`
	UnitKind        string  `json:"unit_kind,omitempty"`
	ReservedUnits   int     `json:"reserved_units"`
	AuthorizedMinor int64   `json:"authorized_minor"`
	ReservedMinor   int64   `json:"reserved_minor"`
	HandoffAt       *string `json:"handoff_at,omitempty"`
	OutcomeClass    string  `json:"outcome_class,omitempty"`
	Retryable       bool    `json:"retryable"`
	AcceptedBy      string  `json:"accepted_by,omitempty"`
	CorrKind        string  `json:"corr_kind,omitempty"`
	CorrValue       string  `json:"corr_value,omitempty"`
	CorrScope       string  `json:"corr_scope,omitempty"`
	CorrPeer        string  `json:"corr_peer,omitempty"`
	ChargedUnits    int     `json:"charged_units"`
	ChargedMinor    int64   `json:"charged_minor"`
	PossibleMinor   int64   `json:"possible_minor"`
	ChargeState     string  `json:"charge_state"`
	Detail          string  `json:"detail,omitempty"`
	CreatedAt       string  `json:"created_at"`
	FinishedAt      *string `json:"finished_at,omitempty"`
}

// DeliveryEvidence is one report about a transmission (r3 D6).
type DeliveryEvidence struct {
	ID             int64   `json:"id"`
	IntentID       string  `json:"intent_id,omitempty"`
	AttemptID      int64   `json:"attempt_id,omitempty"`
	TransmissionID int64   `json:"transmission_id,omitempty"`
	Source         string  `json:"source"`
	Event          string  `json:"event"`
	Completes      string  `json:"completes,omitempty"` // the outcome it establishes, if it is completion evidence
	CorrKind       string  `json:"corr_kind,omitempty"`
	CorrValue      string  `json:"corr_value,omitempty"`
	CorrScope      string  `json:"corr_scope,omitempty"`
	CorrPeer       string  `json:"corr_peer,omitempty"`
	Trust          string  `json:"trust"`
	Matched        bool    `json:"matched"`
	Ambiguous      bool    `json:"ambiguous"`
	Candidates     []int64 `json:"candidates,omitempty"`
	Applied        bool    `json:"applied"`
	ReceivedAt     string  `json:"received_at"`
	// DedupKey is the answer's own identity (a Hub event's id): the ledger keeps one row
	// per key, so evidence applied again after a failure or a restart is recorded once.
	DedupKey string `json:"-"`
	// Repeat is set by RecordEvidence when the key was recorded before: the row returned
	// is that one.
	Repeat bool `json:"-"`
}

// IntentAttempt is an attempt row with what the Escalator reads of it.
type IntentAttempt struct {
	MessageDelivery
	AcceptedAt        *string `json:"accepted_at,omitempty"`
	AcceptedElapsedMs *int64  `json:"accepted_elapsed_ms,omitempty"`
}

// IntentSnapshot is an intent with its rows, transmissions and matched
// evidence, read in one transaction.
type IntentSnapshot struct {
	Intent        DeliveryIntent
	Rows          []IntentAttempt
	Transmissions []DeliveryTransmission
	Evidence      []DeliveryEvidence // matched to this intent
}

// NewIntent is an intent with its round-1 attempt rows, for CreateSubmission.
type NewIntent struct {
	Intent    DeliveryIntent
	FirstRows []MessageDelivery
}

const intentColumns = `id, submission_id, contact_id, precedence, class, payload, payload_sha256,
	requested_strategy, effective_strategy, strategy_reason, policy_version, route_plan,
	state, outcome, possible_duplicate, version, active_round, round_started_ms,
	cap_minor, currency, reserved_minor, incurred_minor, possible_minor,
	elapsed_ms, heartbeat_wall_at, heartbeat_wall_trusted, expiry_allow_s, expires_at,
	cancelled_at, cancel_reason, last_transition, created_at, updated_at, closed_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanIntent(r rowScanner) (*DeliveryIntent, error) {
	var in DeliveryIntent
	var plan string
	var dup, trusted int
	if err := r.Scan(&in.ID, &in.SubmissionID, &in.ContactID, &in.Precedence, &in.Class, &in.Payload, &in.PayloadSHA256,
		&in.RequestedStrategy, &in.EffectiveStrategy, &in.StrategyReason, &in.PolicyVersion, &plan,
		&in.State, &in.Outcome, &dup, &in.Version, &in.ActiveRound, &in.RoundStartedMs,
		&in.CapMinor, &in.Currency, &in.ReservedMinor, &in.IncurredMinor, &in.PossibleMinor,
		&in.ElapsedMs, &in.HeartbeatWallAt, &trusted, &in.ExpiryAllowS, &in.ExpiresAt,
		&in.CancelledAt, &in.CancelReason, &in.LastTransition, &in.CreatedAt, &in.UpdatedAt, &in.ClosedAt); err != nil {
		return nil, err
	}
	in.PossibleDuplicate = dup != 0
	in.HeartbeatWallTrusted = trusted != 0
	if err := json.Unmarshal([]byte(plan), &in.RoutePlan); err != nil {
		return nil, fmt.Errorf("intent %s: route plan: %w", in.ID, err)
	}
	return &in, nil
}

const transmissionColumns = `id, attempt_id, intent_id, gen, state, unit_kind, reserved_units, authorized_minor,
	reserved_minor, handoff_at, outcome_class, retryable, accepted_by, corr_kind, corr_value, corr_scope, corr_peer,
	charged_units, charged_minor, possible_minor, charge_state, detail, created_at, finished_at`

func scanTransmission(r rowScanner) (*DeliveryTransmission, error) {
	var t DeliveryTransmission
	var retryable int
	if err := r.Scan(&t.ID, &t.AttemptID, &t.IntentID, &t.Gen, &t.State, &t.UnitKind, &t.ReservedUnits, &t.AuthorizedMinor,
		&t.ReservedMinor, &t.HandoffAt, &t.OutcomeClass, &retryable, &t.AcceptedBy, &t.CorrKind, &t.CorrValue, &t.CorrScope, &t.CorrPeer,
		&t.ChargedUnits, &t.ChargedMinor, &t.PossibleMinor, &t.ChargeState, &t.Detail, &t.CreatedAt, &t.FinishedAt); err != nil {
		return nil, err
	}
	t.Retryable = retryable != 0
	return &t, nil
}

const evidenceColumns = `id, COALESCE(intent_id, ''), COALESCE(attempt_id, 0), COALESCE(transmission_id, 0), source, event, completes,
	corr_kind, corr_value, corr_scope, corr_peer, trust, matched, ambiguous, candidates, applied, received_at`

func scanEvidence(r rowScanner) (*DeliveryEvidence, error) {
	var e DeliveryEvidence
	var matched, ambiguous, applied int
	var cands string
	if err := r.Scan(&e.ID, &e.IntentID, &e.AttemptID, &e.TransmissionID, &e.Source, &e.Event, &e.Completes,
		&e.CorrKind, &e.CorrValue, &e.CorrScope, &e.CorrPeer, &e.Trust, &matched, &ambiguous, &cands, &applied, &e.ReceivedAt); err != nil {
		return nil, err
	}
	e.Matched, e.Ambiguous, e.Applied = matched != 0, ambiguous != 0, applied != 0
	_ = json.Unmarshal([]byte(cands), &e.Candidates)
	return &e, nil
}

// withTx runs fn in one transaction: committed when fn returns nil,
// rolled back otherwise.
func (db *DB) withTx(fn func(tx *sqlx.Tx) error) error {
	tx, err := db.Beginx()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- submissions ----

// GetSubmissionByKey returns the submission a source made under an
// idempotency key, or nil when there is none.
func (db *DB) GetSubmissionByKey(source, key string) (*DeliverySubmission, error) {
	return getSubmission(db.DB, `source = ? AND idempotency_key = ?`, source, key)
}

// GetSubmission returns one submission by id, or nil.
func (db *DB) GetSubmission(id string) (*DeliverySubmission, error) {
	return getSubmission(db.DB, `id = ?`, id)
}

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getSubmission(q queryer, where string, args ...any) (*DeliverySubmission, error) {
	var s DeliverySubmission
	var key sql.NullString
	var ids string
	err := q.QueryRow(`SELECT id, source, idempotency_key, request_sha256, intent_ids, created_at, mode, legacy_result
		FROM delivery_submissions WHERE `+where, args...).Scan(&s.ID, &s.Source, &key, &s.RequestSHA256, &ids, &s.CreatedAt,
		&s.Mode, &s.LegacyResult)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get submission: %w", err)
	}
	s.IdempotencyKey = key.String
	_ = json.Unmarshal([]byte(ids), &s.IntentIDs)
	return &s, nil
}

// CreateLegacySubmission writes a send made outside delivery intents under
// its idempotency key: every delivery row it queues and the submission
// (mode legacy, with result(ids) as its result), in one transaction, or
// nothing. A submission the same source already made under the key comes
// back as existing (whichever mode made it) when its fingerprint is the
// same, and nothing is written; with another fingerprint,
// ErrIdempotencyConflict. The database has one connection, so the check and
// the writes cannot interleave with another request. [MESHSAT-1584]
func (db *DB) CreateLegacySubmission(sub DeliverySubmission, dels []MessageDelivery, result func(ids []int64) (string, error)) (existing *DeliverySubmission, ids []int64, err error) {
	if sub.IdempotencyKey == "" {
		return nil, nil, errors.New("legacy submission: no idempotency key")
	}
	err = db.withTx(func(tx *sqlx.Tx) error {
		prev, err := getSubmission(tx, `source = ? AND idempotency_key = ?`, sub.Source, sub.IdempotencyKey)
		if err != nil {
			return err
		}
		if prev != nil {
			if prev.RequestSHA256 != sub.RequestSHA256 {
				return ErrIdempotencyConflict
			}
			existing = prev
			return nil
		}
		for _, d := range dels {
			id, err := insertDelivery(tx, d)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		res, err := result(ids)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO delivery_submissions (id, source, idempotency_key, request_sha256, intent_ids, mode, legacy_result)
			VALUES (?, ?, ?, ?, '[]', ?, ?)`, sub.ID, sub.Source, sub.IdempotencyKey, sub.RequestSHA256, SubmissionLegacy, res); err != nil {
			return fmt.Errorf("insert submission: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if existing != nil {
		return existing, nil, nil
	}
	return nil, ids, nil
}

// CreateSubmission writes a submission, every intent and their round-1
// attempt rows in one transaction, or nothing (r3 D2, T1). A submission the
// same source already made under the same idempotency key is returned as
// existing instead, when its fingerprint is the same; with a different one
// ErrIdempotencyConflict comes back. rowIDs are the round-1 rows' ids per
// intent, in order.
func (db *DB) CreateSubmission(sub DeliverySubmission, intents []NewIntent) (existing *DeliverySubmission, rowIDs [][]int64, err error) {
	err = db.withTx(func(tx *sqlx.Tx) error {
		if sub.IdempotencyKey != "" {
			prev, err := getSubmission(tx, `source = ? AND idempotency_key = ?`, sub.Source, sub.IdempotencyKey)
			if err != nil {
				return err
			}
			if prev != nil {
				if prev.RequestSHA256 != sub.RequestSHA256 {
					return ErrIdempotencyConflict
				}
				existing = prev
				return nil
			}
		}
		ids := make([]string, 0, len(intents))
		for _, ni := range intents {
			ids = append(ids, ni.Intent.ID)
		}
		idsJSON, _ := json.Marshal(ids)
		var key any
		if sub.IdempotencyKey != "" {
			key = sub.IdempotencyKey
		}
		if _, err := tx.Exec(`INSERT INTO delivery_submissions (id, source, idempotency_key, request_sha256, intent_ids)
			VALUES (?, ?, ?, ?, ?)`, sub.ID, sub.Source, key, sub.RequestSHA256, string(idsJSON)); err != nil {
			return fmt.Errorf("insert submission: %w", err)
		}
		for _, ni := range intents {
			in := ni.Intent
			plan, _ := json.Marshal(in.RoutePlan)
			// An intent is written active and pending (T1); a team member
			// no route admits is written closed and failed at once (T2,
			// C8), with its route plan as the reasons. [MESHSAT-1584]
			state, outcome, transition := IntentActive, OutcomePending, "T1"
			var closedAt any
			if in.State == IntentClosed {
				state, outcome, transition, closedAt = IntentClosed, in.Outcome, "T2", in.ClosedAt
			}
			if _, err := tx.Exec(`INSERT INTO delivery_intents (id, submission_id, contact_id, precedence, class, payload, payload_sha256,
				requested_strategy, effective_strategy, strategy_reason, policy_version, route_plan,
				state, outcome, version, active_round, round_started_ms, cap_minor, currency, elapsed_ms,
				heartbeat_wall_at, heartbeat_wall_trusted, expiry_allow_s, expires_at, last_transition, closed_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, 0, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
				in.ID, sub.ID, in.ContactID, in.Precedence, in.Class, in.Payload, in.PayloadSHA256,
				in.RequestedStrategy, in.EffectiveStrategy, in.StrategyReason, in.PolicyVersion, string(plan),
				state, outcome, in.CapMinor, in.Currency, in.HeartbeatWallAt, boolInt(in.HeartbeatWallTrusted), in.ExpiryAllowS,
				in.ExpiresAt, transition, closedAt); err != nil {
				return fmt.Errorf("insert intent: %w", err)
			}
			var rids []int64
			for _, row := range ni.FirstRows {
				id, err := insertAttemptTx(tx, row)
				if err != nil {
					return err
				}
				rids = append(rids, id)
			}
			rowIDs = append(rowIDs, rids)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return existing, rowIDs, nil
}

// insertAttemptTx inserts one attempt row of an intent, with the shape
// QueueDirectSendTo gives a direct send (r3 D3) and the interface's next
// egress sequence number.
func insertAttemptTx(tx *sqlx.Tx, d MessageDelivery) (int64, error) {
	if d.IntentID == "" {
		return 0, errors.New("insert attempt: no intent")
	}
	if err := tx.QueryRow(`UPDATE interfaces SET egress_seq = egress_seq + 1 WHERE id = ? RETURNING egress_seq`, d.Channel).Scan(&d.SeqNum); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("insert attempt: egress seq: %w", err)
	}
	precedence := d.Precedence
	if precedence == "" {
		precedence = "Routine"
	}
	class := d.Class
	if class == "" {
		class = DeliveryClassMessage
	}
	status := d.Status
	if status == "" {
		status = "queued"
	}
	res, err := tx.Exec(`INSERT INTO message_deliveries
		(msg_ref, channel, status, priority, payload, text_preview, retries, max_retries, visited, qos_level, seq_num,
		 signature, signer_id, precedence, destination, delivery_class, intent_id, round, attempt_no, route_index, transform_sha256)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, '[]', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.MsgRef, d.Channel, status, d.Priority, d.Payload, d.TextPreview, d.MaxRetries, d.QoSLevel, d.SeqNum,
		d.Signature, d.SignerID, precedence, d.Destination, class, d.IntentID, d.Round, d.AttemptNo, d.RouteIndex, "")
	if err != nil {
		return 0, fmt.Errorf("insert attempt: %w", err)
	}
	return res.LastInsertId()
}

// ---- reads ----

// GetIntent returns one intent, or ErrIntentNotFound.
func (db *DB) GetIntent(id string) (*DeliveryIntent, error) {
	in, err := scanIntent(db.QueryRow(`SELECT `+intentColumns+` FROM delivery_intents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIntentNotFound
	}
	return in, err
}

// ListIntents returns the newest intents, for one contact when contactID is
// set.
func (db *DB) ListIntents(contactID string, limit int) ([]DeliveryIntent, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT ` + intentColumns + ` FROM delivery_intents`
	var args []any
	if contactID != "" {
		q += ` WHERE contact_id = ?`
		args = append(args, contactID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list intents: %w", err)
	}
	defer rows.Close()
	var out []DeliveryIntent
	for rows.Next() {
		in, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	return out, rows.Err()
}

// ActiveIntentIDs returns the ids of every active intent.
func (db *DB) ActiveIntentIDs() ([]string, error) {
	rows, err := db.Query(`SELECT id FROM delivery_intents WHERE state = 'active' ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("active intents: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// LoadIntentSnapshot reads an intent with its rows, transmissions and
// matched evidence.
func (db *DB) LoadIntentSnapshot(id string) (*IntentSnapshot, error) {
	var snap *IntentSnapshot
	err := db.withTx(func(tx *sqlx.Tx) error {
		var err error
		snap, err = loadSnapshotTx(tx, id)
		return err
	})
	return snap, err
}

func loadSnapshotTx(tx *sqlx.Tx, id string) (*IntentSnapshot, error) {
	in, err := scanIntent(tx.QueryRow(`SELECT `+intentColumns+` FROM delivery_intents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIntentNotFound
	}
	if err != nil {
		return nil, err
	}
	snap := &IntentSnapshot{Intent: *in}

	rows, err := tx.Query(`SELECT `+deliveryColumns+`, accepted_at, accepted_elapsed_ms
		FROM message_deliveries WHERE intent_id = ? ORDER BY attempt_no`, id)
	if err != nil {
		return nil, fmt.Errorf("intent rows: %w", err)
	}
	for rows.Next() {
		var a IntentAttempt
		if err := rows.Scan(append(a.scanTargets(), &a.AcceptedAt, &a.AcceptedElapsedMs)...); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan intent row: %w", err)
		}
		snap.Rows = append(snap.Rows, a)
	}
	rows.Close()

	trows, err := tx.Query(`SELECT `+transmissionColumns+` FROM delivery_transmissions WHERE intent_id = ? ORDER BY attempt_id, gen`, id)
	if err != nil {
		return nil, fmt.Errorf("intent transmissions: %w", err)
	}
	for trows.Next() {
		t, err := scanTransmission(trows)
		if err != nil {
			trows.Close()
			return nil, err
		}
		snap.Transmissions = append(snap.Transmissions, *t)
	}
	trows.Close()

	erows, err := tx.Query(`SELECT `+evidenceColumns+` FROM delivery_evidence WHERE intent_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("intent evidence: %w", err)
	}
	for erows.Next() {
		e, err := scanEvidence(erows)
		if err != nil {
			erows.Close()
			return nil, err
		}
		snap.Evidence = append(snap.Evidence, *e)
	}
	erows.Close()
	return snap, nil
}

// ---- transmissions (r3 D5, requirements G1) ----

// AuthorizeTransmission records transmission gen of an attempt claimed
// under claim gen (G1: the transmission authorized for a claim has
// gen = claim_gen), reserving reservedMinor against the intent's cap in the
// same transaction (r3 D8: reserved + incurred + possible + new <= cap).
// ErrClaimLost when the row is no longer sending under that claim,
// ErrOverCap when the cap does not allow it; nothing is written then.
func (db *DB) AuthorizeTransmission(attemptID int64, gen int, unitKind string, units int, reservedMinor int64) (int64, error) {
	if reservedMinor < 0 || units < 0 {
		return 0, ErrBadAmount
	}
	var txID int64
	err := db.withTx(func(tx *sqlx.Tx) error {
		var intentID string
		err := tx.QueryRow(`SELECT intent_id FROM message_deliveries
			WHERE id = ? AND status = 'sending' AND claim_gen = ? AND intent_id IS NOT NULL`, attemptID, gen).Scan(&intentID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClaimLost
		}
		if err != nil {
			return fmt.Errorf("authorize: %w", err)
		}
		if reservedMinor > 0 {
			res, err := tx.Exec(`UPDATE delivery_intents SET reserved_minor = reserved_minor + ?, updated_at = datetime('now')
				WHERE id = ? AND (cap_minor IS NULL OR reserved_minor + incurred_minor + possible_minor + ? <= cap_minor)`,
				reservedMinor, intentID, reservedMinor)
			if err != nil {
				return fmt.Errorf("authorize: reserve: %w", err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return ErrOverCap
			}
		}
		res, err := tx.Exec(`INSERT INTO delivery_transmissions (attempt_id, intent_id, gen, state, unit_kind, reserved_units, authorized_minor, reserved_minor)
			VALUES (?, ?, ?, 'authorized', ?, ?, ?, ?)`, attemptID, intentID, gen, unitKind, units, reservedMinor, reservedMinor)
		if err != nil {
			return fmt.Errorf("authorize: insert: %w", err)
		}
		txID, err = res.LastInsertId()
		return err
	})
	return txID, err
}

// MarkTransmissionHandedOff records that the adapter is about to be called:
// from here on the outcome is unknown until finalization (r3 D5 step 2).
func (db *DB) MarkTransmissionHandedOff(txID int64) error {
	return db.MarkTransmissionHandedOffWith(txID, Correlation{})
}

// Correlation is what later evidence must carry to name a transmission:
// kind and value within a scope (the interface); peer is who it went to.
type Correlation struct {
	Kind, Value, Scope, Peer string
}

// MarkTransmissionHandedOffWith is MarkTransmissionHandedOff with the
// correlation the send will carry, prepared before the call (a mesh packet
// id): a receipt that comes back before finalization, or after a stop
// between the send and finalization, still finds its transmission
// (red-team F-2). [MESHSAT-1584]
func (db *DB) MarkTransmissionHandedOffWith(txID int64, c Correlation) error {
	res, err := db.Exec(`UPDATE delivery_transmissions SET state = 'handed_off', handoff_at = datetime('now'),
		corr_kind = ?, corr_value = ?, corr_scope = ?, corr_peer = ?
		WHERE id = ? AND state = 'authorized'`, c.Kind, c.Value, c.Scope, c.Peer, txID)
	if err != nil {
		return fmt.Errorf("handoff %d: %w", txID, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("handoff %d: transmission is not authorized", txID)
	}
	return nil
}

// TransmissionResult is what the adapter reported for one transmission.
type TransmissionResult struct {
	Class        string // ClassNotSent, ClassAccepted, ClassUnknown
	Retryable    bool   // not_sent only: the adapter knows nothing left
	ConsumeRetry bool   // a retryable not_sent spends one of the row's retries
	RetryAt      time.Time
	AcceptedBy   string
	CorrKind     string
	CorrValue    string
	CorrScope    string
	CorrPeer     string
	ChargedUnits int
	ChargedMinor int64
	Confirmed    bool   // the charge is the provider's, not an estimate
	Detail       string // the error, or what accepted it
	Reason       string // the attempt's reason when it ends not sent (oversize, ...)
	// Evidence that came back within the call (an LXMF proof), matched to
	// this transmission in the same transaction.
	Evidence []DeliveryEvidence
	// AcceptedElapsedMs is the intent's elapsed time at acceptance, from
	// the Escalator's clock: the receipt allowance runs from it.
	AcceptedElapsedMs *int64
	// Reconcile classifies evidence stored before this transmission's
	// correlation was known (a receipt that overtook the finalization):
	// matched now when this transmission is its only candidate (F-2).
	Reconcile EvidenceReclassifier
	// MatchWindowS bounds the evidence Reconcile looks at, from the handoff.
	MatchWindowS int
}

// EvidenceReclassifier says what stored evidence proves of the one
// transmission it turned out to name.
type EvidenceReclassifier func(t DeliveryTransmission, e DeliveryEvidence) (event, source, trust, completes string)

// FinalizeTransmission settles one transmission in one transaction (G1):
// its outcome, correlation and charge; the end of its reservation, on the
// transmission and in the intent's aggregates (reserved released or
// consumed, incurred, possible); the attempt row's outcome class,
// acceptance time and status (accepted, retry, not_sent, unknown, or
// superseded when a retry would no longer pass the claim fence); and the
// evidence that came back in the call. Nothing of it is committed apart.
// It returns the intent id.
func (db *DB) FinalizeTransmission(txID int64, r TransmissionResult) (string, error) {
	var intentID string
	err := db.withTx(func(tx *sqlx.Tx) error {
		var err error
		intentID, err = finalizeTx(tx, txID, r)
		return err
	})
	return intentID, err
}

func finalizeTx(tx *sqlx.Tx, txID int64, r TransmissionResult) (string, error) {
	t, err := scanTransmission(tx.QueryRow(`SELECT `+transmissionColumns+` FROM delivery_transmissions WHERE id = ?`, txID))
	if err != nil {
		return "", fmt.Errorf("finalize %d: %w", txID, err)
	}
	if t.State == TxDone {
		return t.IntentID, fmt.Errorf("finalize %d: already done", txID)
	}

	// The reservation ends here: a charge is incurred whatever the
	// outcome; an unknown outcome with no charge keeps its whole
	// reservation as possible; the rest is released (r3 D8, G1).
	reserved := t.ReservedMinor
	charged := r.ChargedMinor
	if charged < 0 || r.ChargedUnits < 0 {
		// Left handed off: the restart recovery reads it as unknown.
		return "", fmt.Errorf("finalize %d: %w", txID, ErrBadAmount)
	}
	var possible int64
	chargeState := "none"
	switch {
	case charged > 0 && r.Confirmed:
		chargeState = "confirmed"
	case charged > 0:
		chargeState = "estimated"
	case r.Class == ClassUnknown && reserved > 0:
		possible = reserved
		chargeState = "possible"
	}
	// A correlation prepared at the handoff stays when the result names
	// none (an error after a send that may have gone out keeps its id).
	if r.CorrKind == "" {
		r.CorrKind, r.CorrValue, r.CorrScope, r.CorrPeer = t.CorrKind, t.CorrValue, t.CorrScope, t.CorrPeer
	}
	if _, err := tx.Exec(`UPDATE delivery_transmissions SET state = 'done', outcome_class = ?, retryable = ?, accepted_by = ?,
		corr_kind = ?, corr_value = ?, corr_scope = ?, corr_peer = ?, charged_units = ?, charged_minor = ?,
		possible_minor = ?, reserved_minor = 0, charge_state = ?, detail = ?, finished_at = datetime('now')
		WHERE id = ?`, r.Class, boolInt(r.Retryable), r.AcceptedBy, r.CorrKind, r.CorrValue, r.CorrScope, r.CorrPeer,
		r.ChargedUnits, charged, possible, chargeState, r.Detail, txID); err != nil {
		return "", fmt.Errorf("finalize %d: transmission: %w", txID, err)
	}
	t.CorrKind, t.CorrValue, t.CorrScope, t.CorrPeer = r.CorrKind, r.CorrValue, r.CorrScope, r.CorrPeer
	if _, err := tx.Exec(`UPDATE delivery_intents SET dirty = 1 WHERE id = ?`, t.IntentID); err != nil {
		return "", fmt.Errorf("finalize %d: dirty: %w", txID, err)
	}
	if r.Reconcile != nil && t.CorrKind != "" {
		if err := reconcileTx(tx, *t, r.Reconcile, r.MatchWindowS); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(`UPDATE delivery_intents SET reserved_minor = reserved_minor - ?, incurred_minor = incurred_minor + ?,
		possible_minor = possible_minor + ?, updated_at = datetime('now') WHERE id = ?`, reserved, charged, possible, t.IntentID); err != nil {
		return "", fmt.Errorf("finalize %d: intent aggregates: %w", txID, err)
	}

	// The row follows its transmission, if it is still in flight under
	// the claim this transmission belongs to.
	var retries, maxRetries int
	err = tx.QueryRow(`SELECT retries, max_retries FROM message_deliveries WHERE id = ? AND status = 'sending' AND claim_gen = ?`,
		t.AttemptID, t.Gen).Scan(&retries, &maxRetries)
	if errors.Is(err, sql.ErrNoRows) {
		return t.IntentID, nil // nothing in flight for this claim: the record above is the account
	}
	if err != nil {
		return "", fmt.Errorf("finalize %d: row: %w", txID, err)
	}
	switch r.Class {
	case ClassAccepted:
		_, err = tx.Exec(`UPDATE message_deliveries SET status = 'accepted', outcome_class = 'accepted', accepted_at = datetime('now'),
			accepted_elapsed_ms = ?, last_error = '', channel_ref = ?, reason = 'sent', updated_at = datetime('now') WHERE id = ?`,
			r.AcceptedElapsedMs, r.CorrValue, t.AttemptID)
	case ClassUnknown:
		_, err = tx.Exec(`UPDATE message_deliveries SET status = 'unknown', outcome_class = 'unknown', last_error = ?,
			reason = 'withheld_outcome_unknown', updated_at = datetime('now') WHERE id = ?`, r.Detail, t.AttemptID)
	default: // not sent
		if r.ConsumeRetry {
			retries++
		}
		if r.Retryable && (!r.ConsumeRetry || retries < maxRetries) {
			// G2: a retryable not_sent is not settled; the row waits for
			// its next claim, if the claim fence would still let it.
			status := "retry"
			reason := ""
			var live int
			_ = tx.QueryRow(`SELECT COUNT(*) FROM delivery_intents i JOIN message_deliveries d ON d.intent_id = i.id
				WHERE d.id = ? AND i.state = 'active' AND i.active_round = d.round`, t.AttemptID).Scan(&live)
			if live == 0 {
				status, reason = AttemptSuperseded, AttemptSuperseded
			}
			next := r.RetryAt
			if next.IsZero() {
				next = time.Now()
			}
			_, err = tx.Exec(`UPDATE message_deliveries SET status = ?, outcome_class = 'not_sent', retries = ?, next_retry = ?,
				last_error = ?, reason = ?, updated_at = datetime('now') WHERE id = ?`,
				status, retries, next.UTC().Format("2006-01-02 15:04:05"), r.Detail, reason, t.AttemptID)
		} else {
			reason := r.Reason
			if reason == "" {
				reason = "not_sent"
			}
			_, err = tx.Exec(`UPDATE message_deliveries SET status = 'not_sent', outcome_class = 'not_sent', retries = ?,
				last_error = ?, reason = ?, updated_at = datetime('now') WHERE id = ?`, retries, r.Detail, reason, t.AttemptID)
		}
	}
	if err != nil {
		return "", fmt.Errorf("finalize %d: row status: %w", txID, err)
	}
	for _, ev := range r.Evidence {
		ev.IntentID, ev.AttemptID, ev.TransmissionID, ev.Matched = t.IntentID, t.AttemptID, t.ID, true
		if ev.CorrKind == "" {
			ev.CorrKind, ev.CorrValue, ev.CorrScope, ev.CorrPeer = r.CorrKind, r.CorrValue, r.CorrScope, r.CorrPeer
		}
		if _, err := insertEvidenceTx(tx, ev); err != nil {
			return "", err
		}
	}
	return t.IntentID, nil
}

// EndAttemptNotSent ends an attempt the worker refused before authorizing
// any transmission (the route no longer allowed, the chain no longer fits,
// the cap): no transmission row, no call (r3 D8). A retryable refusal puts
// the row back to wait for its next claim, as finalizeTx does.
func (db *DB) EndAttemptNotSent(attemptID int64, gen int, reason, detail string, retryable bool, retryAt time.Time) error {
	return db.withTx(func(tx *sqlx.Tx) error {
		status := AttemptNotSent
		if retryable {
			status = "retry"
			var live int
			_ = tx.QueryRow(`SELECT COUNT(*) FROM delivery_intents i JOIN message_deliveries d ON d.intent_id = i.id
				WHERE d.id = ? AND i.state = 'active' AND i.active_round = d.round`, attemptID).Scan(&live)
			if live == 0 {
				status, reason = AttemptSuperseded, AttemptSuperseded
			}
		}
		if retryAt.IsZero() {
			retryAt = time.Now()
		}
		res, err := tx.Exec(`UPDATE message_deliveries SET status = ?, outcome_class = 'not_sent', reason = ?, last_error = ?,
			next_retry = ?, updated_at = datetime('now') WHERE id = ? AND status = 'sending' AND claim_gen = ?`,
			status, reason, detail, retryAt.UTC().Format("2006-01-02 15:04:05"), attemptID, gen)
		if err != nil {
			return fmt.Errorf("end attempt %d: %w", attemptID, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrClaimLost
		}
		return nil
	})
}

// RecoverStats counts what RecoverIntentDeliveries did.
type RecoverStats struct {
	NeverAuthorized int // claimed, no transmission: back to retry
	Authorized      int // reserved, adapter never called: released, back to retry
	HandedOff       int // adapter called, not finalized: unknown, charge possible
	Replayed        int // finalized transmission, row still sending: a defect, replayed
}

// RecoverIntentDeliveries settles the intent attempts a stop left
// 'sending', from durable state only, reading only the transmission the
// current claim owns (gen = claim_gen; requirements G1, replacing r3 D5's
// rows for 'sending'). Earlier generations keep their outcomes. Run at
// start, before any delivery worker.
func (db *DB) RecoverIntentDeliveries() (RecoverStats, error) {
	var st RecoverStats
	type stale struct {
		id  int64
		gen int
	}
	var rows []stale
	err := func() error {
		q, err := db.Query(`SELECT id, claim_gen FROM message_deliveries WHERE status = 'sending' AND intent_id IS NOT NULL`)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var s stale
			if err := q.Scan(&s.id, &s.gen); err != nil {
				return err
			}
			rows = append(rows, s)
		}
		return q.Err()
	}()
	if err != nil {
		return st, fmt.Errorf("recover intents: %w", err)
	}
	for _, s := range rows {
		err := db.withTx(func(tx *sqlx.Tx) error {
			t, err := scanTransmission(tx.QueryRow(`SELECT `+transmissionColumns+` FROM delivery_transmissions WHERE attempt_id = ? AND gen = ?`, s.id, s.gen))
			switch {
			case errors.Is(err, sql.ErrNoRows):
				st.NeverAuthorized++
				_, err = tx.Exec(`UPDATE message_deliveries SET status = 'retry', next_retry = datetime('now'),
					last_error = 'recovered after restart: claimed, never sent', updated_at = datetime('now')
					WHERE id = ? AND status = 'sending'`, s.id)
				return err
			case err != nil:
				return err
			}
			switch t.State {
			case TxAuthorized:
				st.Authorized++
				_, err := finalizeTx(tx, t.ID, TransmissionResult{Class: ClassNotSent, Retryable: true,
					Detail: "recovered after restart: authorized, never handed to the adapter"})
				return err
			case TxHandedOff:
				st.HandedOff++
				_, err := finalizeTx(tx, t.ID, TransmissionResult{Class: ClassUnknown,
					Detail: "recovered after restart: handed to the adapter, its outcome never recorded"})
				return err
			default:
				// Cannot happen with atomic finalization: replay the
				// recorded result into the row, never another call.
				st.Replayed++
				status := t.OutcomeClass
				switch status {
				case ClassAccepted:
					status = AttemptAccepted
				case ClassUnknown:
					status = AttemptUnknown
				default:
					status = AttemptNotSent
					if t.Retryable {
						status = "retry"
					}
				}
				_, err := tx.Exec(`UPDATE message_deliveries SET status = ?, outcome_class = ?, updated_at = datetime('now')
					WHERE id = ? AND status = 'sending'`, status, t.OutcomeClass, s.id)
				return err
			}
		})
		if err != nil {
			return st, fmt.Errorf("recover intent row %d: %w", s.id, err)
		}
	}
	return st, nil
}

// ---- evidence (r3 D6, D18) ----

func insertEvidenceTx(tx *sqlx.Tx, e DeliveryEvidence) (int64, error) {
	cands, _ := json.Marshal(e.Candidates)
	if e.Candidates == nil {
		cands = []byte("[]")
	}
	if e.Trust == "" {
		e.Trust = "unauthenticated"
	}
	var intentID, attemptID, txID any
	if e.IntentID != "" {
		intentID = e.IntentID
	}
	if e.AttemptID != 0 {
		attemptID = e.AttemptID
	}
	if e.TransmissionID != 0 {
		txID = e.TransmissionID
	}
	var dedup any
	if e.DedupKey != "" {
		dedup = e.DedupKey
	}
	res, err := tx.Exec(`INSERT INTO delivery_evidence (intent_id, attempt_id, transmission_id, source, event, completes,
		corr_kind, corr_value, corr_scope, corr_peer, trust, matched, ambiguous, candidates, dedup_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		intentID, attemptID, txID, e.Source, e.Event, e.Completes, e.CorrKind, e.CorrValue, e.CorrScope, e.CorrPeer,
		e.Trust, boolInt(e.Matched), boolInt(e.Ambiguous), string(cands), dedup)
	if err != nil {
		return 0, fmt.Errorf("insert evidence: %w", err)
	}
	return res.LastInsertId()
}

// EvidenceClassifier decides, for evidence that matched exactly one
// transmission, what it proves of that transmission: its event, source,
// trust and the outcome it completes (empty for evidence that completes
// nothing).
type EvidenceClassifier func(t DeliveryTransmission) (event, source, trust, completes string)

// candidatesTx are the transmissions a piece of evidence can name: its
// correlation kind and value within its scope, handed off no longer than
// windowS before the evidence arrived (0 = no bound). A reference used
// again after the window names only the newer transmission; two inside it
// are ambiguous.
func candidatesTx(tx *sqlx.Tx, kind, value, scope, receivedAt string, windowS int) ([]DeliveryTransmission, error) {
	q := `SELECT ` + transmissionColumns + ` FROM delivery_transmissions WHERE corr_kind = ? AND corr_value = ? AND corr_scope = ?`
	args := []any{kind, value, scope}
	if windowS > 0 {
		q += ` AND handoff_at IS NOT NULL AND handoff_at <= datetime(?) AND datetime(handoff_at, '+' || ? || ' seconds') >= datetime(?)`
		args = append(args, receivedAt, windowS, receivedAt)
	}
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("match evidence: %w", err)
	}
	defer rows.Close()
	var cands []DeliveryTransmission
	for rows.Next() {
		t, err := scanTransmission(rows)
		if err != nil {
			return nil, err
		}
		cands = append(cands, *t)
	}
	return cands, rows.Err()
}

// reconcileTx matches the unmatched evidence stored for a correlation that
// is only now known, under the same rule as RecordEvidence: matched when
// t is its only candidate, ambiguous when there are more.
func reconcileTx(tx *sqlx.Tx, t DeliveryTransmission, classify EvidenceReclassifier, windowS int) error {
	rows, err := tx.Query(`SELECT `+evidenceColumns+` FROM delivery_evidence
		WHERE matched = 0 AND ambiguous = 0 AND corr_kind = ? AND corr_value = ? AND corr_scope = ?`, t.CorrKind, t.CorrValue, t.CorrScope)
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	var evs []DeliveryEvidence
	for rows.Next() {
		e, err := scanEvidence(rows)
		if err != nil {
			rows.Close()
			return err
		}
		evs = append(evs, *e)
	}
	rows.Close()
	for _, e := range evs {
		cands, err := candidatesTx(tx, e.CorrKind, e.CorrValue, e.CorrScope, e.ReceivedAt, windowS)
		if err != nil {
			return err
		}
		switch {
		case len(cands) == 1 && cands[0].ID == t.ID:
			event, source, trust, completes := classify(t, e)
			if _, err := tx.Exec(`UPDATE delivery_evidence SET matched = 1, intent_id = ?, attempt_id = ?, transmission_id = ?,
				event = ?, source = ?, trust = ?, completes = ? WHERE id = ?`,
				t.IntentID, t.AttemptID, t.ID, event, source, trust, completes, e.ID); err != nil {
				return fmt.Errorf("reconcile %d: %w", e.ID, err)
			}
			if _, err := tx.Exec(`UPDATE delivery_intents SET dirty = 1 WHERE id = ?`, t.IntentID); err != nil {
				return err
			}
		case len(cands) > 1:
			var ids []int64
			for _, c := range cands {
				ids = append(ids, c.ID)
			}
			b, _ := json.Marshal(ids)
			if _, err := tx.Exec(`UPDATE delivery_evidence SET ambiguous = 1, candidates = ? WHERE id = ?`, string(b), e.ID); err != nil {
				return fmt.Errorf("reconcile %d: %w", e.ID, err)
			}
		}
	}
	return nil
}

// CorrelationInUse reports whether a correlation value is taken: a
// transmission in that scope used it within windowS seconds, or, with
// windowS 0, any transmission still kept. A sender that picks its own
// references (mesh packet ids) picks another.
func (db *DB) CorrelationInUse(kind, value, scope string, windowS int) (bool, error) {
	q := `SELECT COUNT(*) FROM delivery_transmissions WHERE corr_kind = ? AND corr_value = ? AND corr_scope = ?`
	args := []any{kind, value, scope}
	if windowS > 0 {
		q += ` AND (handoff_at IS NULL OR datetime(handoff_at, '+' || ? || ' seconds') >= datetime('now'))`
		args = append(args, windowS)
	}
	var n int
	err := db.QueryRow(q, args...).Scan(&n)
	return n > 0, err
}

// RecordEvidence stores evidence and matches it by the fields it carries
// itself (r3 D18): the transmissions with its correlation kind and value
// within its scope, open or closed, handed off within windowS seconds
// (0 = no bound). Exactly one: matched, and classify says what it proves.
// None: stored unmatched (a later finalization may still reconcile it).
// More than one: stored ambiguous with the candidates; it completes
// nothing. Evidence with a DedupKey recorded before is not stored again:
// the earlier row comes back with Repeat set. Returns the stored row.
func (db *DB) RecordEvidence(e DeliveryEvidence, windowS int, classify EvidenceClassifier) (DeliveryEvidence, error) {
	err := db.withTx(func(tx *sqlx.Tx) error {
		if e.DedupKey != "" {
			prev, err := scanEvidence(tx.QueryRow(`SELECT `+evidenceColumns+` FROM delivery_evidence WHERE dedup_key = ?`, e.DedupKey))
			switch {
			case err == nil:
				key := e.DedupKey
				e = *prev
				e.DedupKey, e.Repeat = key, true
				return nil
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		var now string
		if err := tx.QueryRow(`SELECT datetime('now')`).Scan(&now); err != nil {
			return err
		}
		cands, err := candidatesTx(tx, e.CorrKind, e.CorrValue, e.CorrScope, now, windowS)
		if err != nil {
			return err
		}
		switch len(cands) {
		case 0:
			e.Matched, e.Completes = false, ""
		case 1:
			t := cands[0]
			e.Matched, e.IntentID, e.AttemptID, e.TransmissionID = true, t.IntentID, t.AttemptID, t.ID
			if classify != nil {
				e.Event, e.Source, e.Trust, e.Completes = classify(t)
			}
		default:
			e.Ambiguous, e.Matched, e.Completes = true, false, ""
			for _, t := range cands {
				e.Candidates = append(e.Candidates, t.ID)
			}
		}
		id, err := insertEvidenceTx(tx, e)
		e.ID = id
		if err == nil && e.Matched {
			_, err = tx.Exec(`UPDATE delivery_intents SET dirty = 1 WHERE id = ?`, e.IntentID)
		}
		return err
	})
	return e, err
}

// ---- the Escalator's transactions (r3 section 3a) ----

// IntentChange is what the Escalator decided for one intent. Transition
// names a transition of r3 section 3a; it is applied only when the intent
// is still at ExpectVersion, and the version goes up by one. With no
// Transition only the bookkeeping (StampAccepted) is applied.
type IntentChange struct {
	Transition    string
	ExpectVersion int
	// StampAccepted records, per attempt row, the intent's elapsed time
	// at which it was seen accepted (its receipt allowance runs from it).
	StampAccepted map[int64]int64
	// Retire retires the unclaimed rows (queued, retry, held) of these
	// rounds, or of every round with RetireAll, as RetireStatus with
	// RetireReason; a row named in RowReasons takes that reason instead.
	Retire       []int
	RetireAll    bool
	RetireStatus string
	RetireReason string
	RowReasons   map[int64]string
	// NewRow is the next round's attempt; NewActiveRound and
	// RoundStartedMs go with it.
	NewRow         *MessageDelivery
	NewActiveRound int
	RoundStartedMs int64
	// SetOutcome sets the intent's outcome; Close closes the intent.
	SetOutcome        string
	PossibleDuplicate *bool
	Close             bool
	CancelReason      string
	ElapsedMs         int64
}

// ApplyIntentChange reads an intent in one transaction, lets decide choose
// a change from that snapshot, and applies the change in the same
// transaction. A change whose ExpectVersion is no longer the intent's
// version is a stale decision and applies nothing (T15). Returns the
// change applied, or nil.
func (db *DB) ApplyIntentChange(intentID string, decide func(*IntentSnapshot) *IntentChange) (*IntentChange, error) {
	var applied *IntentChange
	err := db.withTx(func(tx *sqlx.Tx) error {
		snap, err := loadSnapshotTx(tx, intentID)
		if err != nil {
			return err
		}
		// Looked at: whatever made it dirty is now in this decision.
		if _, err := tx.Exec(`UPDATE delivery_intents SET dirty = 0 WHERE id = ?`, intentID); err != nil {
			return err
		}
		ch := decide(snap)
		if ch == nil {
			return nil
		}
		for rowID, ms := range ch.StampAccepted {
			if _, err := tx.Exec(`UPDATE message_deliveries SET accepted_elapsed_ms = ?
				WHERE id = ? AND intent_id = ? AND accepted_elapsed_ms IS NULL`, ms, rowID, intentID); err != nil {
				return fmt.Errorf("stamp acceptance: %w", err)
			}
		}
		if ch.Transition == "" {
			// Bookkeeping, not a transition: no version change.
			if ch.PossibleDuplicate != nil {
				if _, err := tx.Exec(`UPDATE delivery_intents SET possible_duplicate = ? WHERE id = ? AND state = 'active'`,
					boolInt(*ch.PossibleDuplicate), intentID); err != nil {
					return fmt.Errorf("possible duplicate: %w", err)
				}
			}
			applied = ch
			return nil
		}
		res, err := tx.Exec(`UPDATE delivery_intents SET version = version + 1, last_transition = ?, updated_at = datetime('now')
			WHERE id = ? AND version = ? AND state = 'active'`, ch.Transition, intentID, ch.ExpectVersion)
		if err != nil {
			return fmt.Errorf("%s: %w", ch.Transition, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil // stale: another transition came first
		}
		if ch.RetireAll || len(ch.Retire) > 0 {
			status := ch.RetireStatus
			if status == "" {
				status = AttemptSuperseded
			}
			q := `SELECT id FROM message_deliveries WHERE intent_id = ? AND status IN ('queued', 'retry', 'held')`
			args := []any{intentID}
			if !ch.RetireAll {
				ph := strings.TrimSuffix(strings.Repeat("?,", len(ch.Retire)), ",")
				q += ` AND round IN (` + ph + `)`
				for _, r := range ch.Retire {
					args = append(args, r)
				}
			}
			ids, err := queryIDsTx(tx, q, args...)
			if err != nil {
				return err
			}
			for _, id := range ids {
				reason := ch.RetireReason
				if r, ok := ch.RowReasons[id]; ok {
					reason = r
				}
				if reason == "" {
					reason = status
				}
				if _, err := tx.Exec(`UPDATE message_deliveries SET status = ?, reason = ?, held_at = NULL, updated_at = datetime('now')
					WHERE id = ? AND status IN ('queued', 'retry', 'held')`, status, reason, id); err != nil {
					return fmt.Errorf("retire row %d: %w", id, err)
				}
			}
		}
		if ch.NewRow != nil {
			if _, err := insertAttemptTx(tx, *ch.NewRow); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE delivery_intents SET active_round = ?, round_started_ms = ? WHERE id = ?`,
				ch.NewActiveRound, ch.RoundStartedMs, intentID); err != nil {
				return fmt.Errorf("advance round: %w", err)
			}
		}
		if ch.SetOutcome != "" {
			if _, err := tx.Exec(`UPDATE delivery_intents SET outcome = ? WHERE id = ?`, ch.SetOutcome, intentID); err != nil {
				return fmt.Errorf("set outcome: %w", err)
			}
		}
		if ch.PossibleDuplicate != nil {
			if _, err := tx.Exec(`UPDATE delivery_intents SET possible_duplicate = ? WHERE id = ?`, boolInt(*ch.PossibleDuplicate), intentID); err != nil {
				return fmt.Errorf("possible duplicate: %w", err)
			}
		}
		if ch.CancelReason != "" {
			if _, err := tx.Exec(`UPDATE delivery_intents SET cancelled_at = datetime('now'), cancel_reason = ? WHERE id = ?`, ch.CancelReason, intentID); err != nil {
				return fmt.Errorf("cancel: %w", err)
			}
		}
		if ch.Close {
			if _, err := tx.Exec(`UPDATE delivery_intents SET state = 'closed', closed_at = datetime('now'), elapsed_ms = MAX(elapsed_ms, ?) WHERE id = ?`,
				ch.ElapsedMs, intentID); err != nil {
				return fmt.Errorf("close: %w", err)
			}
		}
		applied = ch
		return nil
	})
	return applied, err
}

// ReviewClosedIntent looks at a closed intent in one transaction (T14):
// decide returns the outcome the D11 rule gives now and the possible
// duplicate; a stronger outcome is recorded (version + 1), never a reopen,
// never a send; the dirty flag is cleared. stronger orders outcomes.
// Returns the outcome when it was upgraded.
func (db *DB) ReviewClosedIntent(intentID string, decide func(*IntentSnapshot) (string, bool), stronger func(a, b string) bool) (string, error) {
	var upgraded string
	err := db.withTx(func(tx *sqlx.Tx) error {
		snap, err := loadSnapshotTx(tx, intentID)
		if err != nil {
			return err
		}
		if snap.Intent.State != IntentClosed {
			return nil
		}
		if _, err := tx.Exec(`UPDATE delivery_intents SET dirty = 0 WHERE id = ?`, intentID); err != nil {
			return err
		}
		best, dup := decide(snap)
		if dup != snap.Intent.PossibleDuplicate {
			if _, err := tx.Exec(`UPDATE delivery_intents SET possible_duplicate = ? WHERE id = ?`, boolInt(dup), intentID); err != nil {
				return err
			}
		}
		if stronger(best, snap.Intent.Outcome) {
			if _, err := tx.Exec(`UPDATE delivery_intents SET outcome = ?, version = version + 1, last_transition = 'T14',
				updated_at = datetime('now') WHERE id = ?`, best, intentID); err != nil {
				return fmt.Errorf("T14: %w", err)
			}
			upgraded = best
		}
		return nil
	})
	return upgraded, err
}

// IntentsToReview are the intents the Escalator must look at: every active
// one, and every closed one something was written about since it last
// looked (dirty).
func (db *DB) IntentsToReview() ([]string, error) {
	rows, err := db.Query(`SELECT id FROM delivery_intents WHERE state = 'active' OR dirty = 1 ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("intents to review: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HasIntents reports whether any delivery intent is kept, open or closed:
// while one is, its transmissions can still be answered, and the
// Escalator runs to take the answers in.
func (db *DB) HasIntents() (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM delivery_intents LIMIT 1)`).Scan(&n)
	return n > 0, err
}

// UpgradeClosedOutcome sets a closed intent's outcome from late evidence
// (T14): recorded, never reopened, never a send. An empty outcome keeps
// the outcome and only records the possible duplicate (no version change).
func (db *DB) UpgradeClosedOutcome(intentID string, expectVersion int, outcome string, possibleDuplicate bool) (bool, error) {
	if outcome == "" {
		_, err := db.Exec(`UPDATE delivery_intents SET possible_duplicate = ? WHERE id = ? AND state = 'closed'`,
			boolInt(possibleDuplicate), intentID)
		return err == nil, err
	}
	res, err := db.Exec(`UPDATE delivery_intents SET outcome = ?, possible_duplicate = ?, version = version + 1, last_transition = 'T14',
		updated_at = datetime('now') WHERE id = ? AND state = 'closed' AND version = ?`, outcome, boolInt(possibleDuplicate), intentID, expectVersion)
	if err != nil {
		return false, fmt.Errorf("T14: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func queryIDsTx(tx *sqlx.Tx, q string, args ...any) ([]int64, error) {
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---- clocks (r3 D7) ----

// PersistIntentClocks writes each active intent's elapsed time with the
// wall clock of this heartbeat and whether it was trusted.
func (db *DB) PersistIntentClocks(elapsed map[string]int64, wallAt time.Time, trusted bool) error {
	if len(elapsed) == 0 {
		return nil
	}
	return db.withTx(func(tx *sqlx.Tx) error {
		for id, ms := range elapsed {
			if _, err := tx.Exec(`UPDATE delivery_intents SET elapsed_ms = MAX(elapsed_ms, ?), heartbeat_wall_at = ?, heartbeat_wall_trusted = ?
				WHERE id = ? AND state = 'active'`, ms, wallAt.UTC().Format("2006-01-02 15:04:05"), boolInt(trusted), id); err != nil {
				return fmt.Errorf("persist clock %s: %w", id, err)
			}
		}
		return nil
	})
}

// IntentClock is an active intent's persisted clock.
type IntentClock struct {
	ID              string
	ElapsedMs       int64
	HeartbeatWallAt *time.Time
	Trusted         bool
}

// ActiveIntentClocks reads the persisted clocks of the active intents.
func (db *DB) ActiveIntentClocks() ([]IntentClock, error) {
	rows, err := db.Query(`SELECT id, elapsed_ms, heartbeat_wall_at, heartbeat_wall_trusted FROM delivery_intents WHERE state = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("intent clocks: %w", err)
	}
	defer rows.Close()
	var out []IntentClock
	for rows.Next() {
		var c IntentClock
		var wall sql.NullString
		var trusted int
		if err := rows.Scan(&c.ID, &c.ElapsedMs, &wall, &trusted); err != nil {
			return nil, err
		}
		c.Trusted = trusted != 0
		if wall.Valid {
			if t, ok := parseSQLiteTime(wall.String); ok {
				c.HeartbeatWallAt = &t
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetIntentElapsed sets an active intent's elapsed time (the restart
// adjustment of r3 D7).
func (db *DB) SetIntentElapsed(id string, ms int64) error {
	_, err := db.Exec(`UPDATE delivery_intents SET elapsed_ms = ? WHERE id = ? AND state = 'active'`, ms, id)
	return err
}

// ParseTime reads a time the database stored (DATETIME columns come back as
// RFC 3339, TEXT ones as SQLite's own form). [MESHSAT-1584]
func ParseTime(s string) (time.Time, bool) { return parseSQLiteTime(s) }

func parseSQLiteTime(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ---- wire identity (r3 D10) ----

const wireTimestampKey = "intent_wire_ts_last_ms"

// AllocateWireTimestampMs hands out an LXMF wire timestamp in milliseconds:
// nowMs, or one past the last one handed out, whichever is later, so every
// allocation is distinct and none goes back in time. The last value is
// kept in system_config, so a restart does not hand one out again.
// [MESHSAT-1584]
func (db *DB) AllocateWireTimestampMs(nowMs int64) (int64, error) {
	var next int64
	err := db.withTx(func(tx *sqlx.Tx) error {
		var raw string
		err := tx.QueryRow(`SELECT value FROM system_config WHERE key = ?`, wireTimestampKey).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		last, _ := strconv.ParseInt(raw, 10, 64)
		next = nowMs
		if next <= last {
			next = last + 1
		}
		_, err = tx.Exec(`INSERT INTO system_config (key, value, updated_at) VALUES (?, ?, datetime('now'))
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now')`, wireTimestampKey, strconv.FormatInt(next, 10))
		return err
	})
	return next, err
}

// ---- tariffs (r3 D8) ----

// Tariff prices one unit kind on one interface.
type Tariff struct {
	ID          int64  `json:"id"`
	InterfaceID string `json:"interface_id"`
	UnitKind    string `json:"unit_kind"`
	UnitBytes   int    `json:"unit_bytes"`
	MinUnits    int    `json:"min_units"`
	PriceMinor  int64  `json:"price_minor"`
	Currency    string `json:"currency"`
	Version     int    `json:"version"`
	Free        bool   `json:"free"`
}

// What a tariff may say in this release (B9SMS-R3, R5): prices in EUR, the
// intents' currency, with no conversion; at most EUR 10.000 a unit, 10.000
// units at least per submission, 1 MiB a unit, so no cost overflows.
// [MESHSAT-1584]
const (
	TariffCurrency      = "EUR"
	MaxTariffPriceMinor = 1_000_000
	MaxTariffMinUnits   = 10_000
	MaxTariffUnitBytes  = 1 << 20
)

// Validate says whether the tariff can price a delivery intent. The
// engine asks it of every stored tariff it uses, so a row written before
// these limits, or past the API, never prices a send.
func (t Tariff) Validate() error {
	switch {
	case t.UnitKind == "":
		return errors.New("the tariff names no unit")
	case t.Currency != TariffCurrency:
		return fmt.Errorf("the tariff is in %q; this release prices in %s only, with no conversion", t.Currency, TariffCurrency)
	case t.PriceMinor < 0 || t.PriceMinor > MaxTariffPriceMinor:
		return fmt.Errorf("price_minor %d is outside 0..%d", t.PriceMinor, MaxTariffPriceMinor)
	case t.MinUnits < 0 || t.MinUnits > MaxTariffMinUnits:
		return fmt.Errorf("min_units %d is outside 1..%d", t.MinUnits, MaxTariffMinUnits)
	case t.UnitBytes < 0 || t.UnitBytes > MaxTariffUnitBytes:
		return fmt.Errorf("unit_bytes %d is outside 0..%d", t.UnitBytes, MaxTariffUnitBytes)
	case !t.Free && t.PriceMinor == 0:
		return errors.New("a paid tariff with no price")
	}
	return nil
}

// Units is what one submission of wireBytes costs in this tariff's unit:
// ceil(bytes / unit_bytes) with unit_bytes > 0, else one per submission;
// never fewer than min_units (r3 D8). [MESHSAT-1584]
func (t Tariff) Units(wireBytes int) int {
	u := 1
	if t.UnitBytes > 0 {
		u = (wireBytes + t.UnitBytes - 1) / t.UnitBytes
	}
	if u < t.MinUnits {
		u = t.MinUnits
	}
	if u < 1 {
		u = 1
	}
	return u
}

// Price is units at this tariff, in minor units of its currency. It never
// wraps: a product past int64 is math.MaxInt64, which no cap allows.
func (t Tariff) Price(units int) int64 {
	if t.Free || units <= 0 || t.PriceMinor <= 0 {
		return 0
	}
	if t.PriceMinor > math.MaxInt64/int64(units) {
		return math.MaxInt64
	}
	return int64(units) * t.PriceMinor
}

// Cost is what one submission of wireBytes costs: its units and its price,
// or why the tariff cannot price it (Validate, or a price past the limit).
func (t Tariff) Cost(wireBytes int) (units int, minor int64, err error) {
	if err := t.Validate(); err != nil {
		return 0, 0, err
	}
	units = t.Units(wireBytes)
	if minor = t.Price(units); minor == math.MaxInt64 {
		return 0, 0, fmt.Errorf("%d units at %d is past what can be priced", units, t.PriceMinor)
	}
	return units, minor, nil
}

// InsertTariff adds a new version of an interface's tariff, valid now; the
// version counts up per interface. Earlier versions stay for the record.
func (db *DB) InsertTariff(t Tariff) (Tariff, error) {
	err := db.withTx(func(tx *sqlx.Tx) error {
		var v int
		if err := tx.Get(&v, `SELECT COALESCE(MAX(version), 0) FROM delivery_tariffs WHERE interface_id = ?`, t.InterfaceID); err != nil {
			return err
		}
		t.Version = v + 1
		if t.Currency == "" {
			t.Currency = "EUR"
		}
		res, err := tx.Exec(`INSERT INTO delivery_tariffs (interface_id, unit_kind, unit_bytes, min_units, price_minor, currency, version, valid_from, free)
			VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), ?)`, t.InterfaceID, t.UnitKind, t.UnitBytes, t.MinUnits, t.PriceMinor, t.Currency, t.Version, boolInt(t.Free))
		if err != nil {
			return err
		}
		t.ID, err = res.LastInsertId()
		return err
	})
	return t, err
}

// ListTariffs returns every tariff version, newest first per interface.
func (db *DB) ListTariffs() ([]Tariff, error) {
	rows, err := db.Query(`SELECT id, interface_id, unit_kind, unit_bytes, min_units, price_minor, currency, version, free
		FROM delivery_tariffs ORDER BY interface_id, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tariff
	for rows.Next() {
		var t Tariff
		var free int
		if err := rows.Scan(&t.ID, &t.InterfaceID, &t.UnitKind, &t.UnitBytes, &t.MinUnits, &t.PriceMinor, &t.Currency, &t.Version, &free); err != nil {
			return nil, err
		}
		t.Free = free != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// CurrentTariff returns the newest tariff of an interface, or nil.
func (db *DB) CurrentTariff(ifaceID string) (*Tariff, error) {
	var t Tariff
	var free int
	err := db.QueryRow(`SELECT id, interface_id, unit_kind, unit_bytes, min_units, price_minor, currency, version, free
		FROM delivery_tariffs WHERE interface_id = ? AND valid_from <= datetime('now')
		ORDER BY valid_from DESC, version DESC LIMIT 1`, ifaceID).Scan(&t.ID, &t.InterfaceID, &t.UnitKind, &t.UnitBytes,
		&t.MinUnits, &t.PriceMinor, &t.Currency, &t.Version, &free)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tariff %s: %w", ifaceID, err)
	}
	t.Free = free != 0
	return &t, nil
}

// ---- retention (r3 D2) ----

// PruneClosedIntents removes intents closed more than days ago (at least
// one day), with their transmissions, evidence and the submissions left
// with no intent. Attempt rows stay, as every delivery row does.
func (db *DB) PruneClosedIntents(days int) (int64, error) {
	if days < 1 {
		days = 1
	}
	var n int64
	err := db.withTx(func(tx *sqlx.Tx) error {
		cutoff := "-" + strconv.Itoa(days) + " days"
		if _, err := tx.Exec(`DELETE FROM delivery_evidence WHERE intent_id IN
			(SELECT id FROM delivery_intents WHERE state = 'closed' AND closed_at < datetime('now', ?))`, cutoff); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM delivery_transmissions WHERE intent_id IN
			(SELECT id FROM delivery_intents WHERE state = 'closed' AND closed_at < datetime('now', ?))`, cutoff); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM delivery_intents WHERE state = 'closed' AND closed_at < datetime('now', ?)`, cutoff)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		if _, err := tx.Exec(`DELETE FROM delivery_evidence WHERE intent_id IS NULL AND received_at < datetime('now', ?)`, cutoff); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM delivery_submissions WHERE NOT EXISTS
			(SELECT 1 FROM delivery_intents i WHERE i.submission_id = delivery_submissions.id)
			AND created_at < datetime('now', ?)`, cutoff)
		return err
	})
	return n, err
}

// ---- A-13: proofs that come after the send stopped waiting ----

// SetTransmissionProof stores, on the attempt's handed-off transmission of
// this generation, the packet hash its proof will name and the key that
// must sign it (hex), as the packet goes out. [MESHSAT-1584]
func (db *DB) SetTransmissionProof(attemptID int64, gen int, proofHash, proofKey string) error {
	res, err := db.Exec(`UPDATE delivery_transmissions SET proof_hash = ?, proof_key = ?
		WHERE attempt_id = ? AND gen = ? AND state = 'handed_off'`, proofHash, proofKey, attemptID, gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no handed-off transmission for attempt %d generation %d", attemptID, gen)
	}
	return nil
}

// ExpectedProofHashes lists the proof hashes of kept transmissions that no
// proof has completed yet.
func (db *DB) ExpectedProofHashes() ([]string, error) {
	var out []string
	err := db.Select(&out, `SELECT t.proof_hash FROM delivery_transmissions t
		WHERE t.proof_hash != '' AND NOT EXISTS (SELECT 1 FROM delivery_evidence e
			WHERE e.transmission_id = t.id AND e.event = 'proof' AND e.matched = 1)`)
	return out, err
}

// RecordProofEvidence takes a proof no waiting send took (A-13). hash is
// the full packet hash (hex) for an explicit proof, or its first 16 bytes
// for an implicit one (prefix). valid checks the proof against a candidate
// transmission's stored hash and key. One valid candidate gets the proof
// as matched evidence (destination_node, signed, completing
// accepted_destination_node) and its intent is marked dirty, in one
// transaction; a second copy of the same proof adds nothing. found is false
// when no stored transmission is proven by it (someone else's proof).
func (db *DB) RecordProofEvidence(hash string, prefix bool, valid func(proofHash, proofKey string) bool) (e DeliveryEvidence, found bool, err error) {
	err = db.withTx(func(tx *sqlx.Tx) error {
		q := `SELECT id, attempt_id, intent_id, proof_hash, proof_key FROM delivery_transmissions WHERE proof_hash = ?`
		if prefix {
			q = `SELECT id, attempt_id, intent_id, proof_hash, proof_key FROM delivery_transmissions
				WHERE proof_hash != '' AND substr(proof_hash, 1, ?) = ?`
		}
		args := []any{hash}
		if prefix {
			args = []any{len(hash), hash}
		}
		rows, err := tx.Query(q, args...)
		if err != nil {
			return err
		}
		type cand struct {
			id, attempt                    int64
			intent, ph, pk, ck, cv, cs, cp string
		}
		var proven []cand
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.id, &c.attempt, &c.intent, &c.ph, &c.pk); err != nil {
				rows.Close()
				return err
			}
			if valid(c.ph, c.pk) {
				proven = append(proven, c)
			}
		}
		rows.Close()
		if len(proven) != 1 {
			// None: not ours. More than one: a signature over two packet
			// hashes at once cannot happen; refuse rather than guess.
			return nil
		}
		found = true
		e, err = proofEvidenceTx(tx, proven[0].id)
		return err
	})
	return e, found, err
}

// RecordLateDelivery takes a proof of a resource transfer that came after
// the send stopped waiting (A-13): the attempt's transmission of this
// generation gets it, as RecordProofEvidence does. A resource is proven on
// its link, inside this process, so no stored hash is needed.
func (db *DB) RecordLateDelivery(attemptID int64, gen int) (e DeliveryEvidence, err error) {
	err = db.withTx(func(tx *sqlx.Tx) error {
		var txID int64
		if err := tx.Get(&txID, `SELECT id FROM delivery_transmissions WHERE attempt_id = ? AND gen = ?`, attemptID, gen); err != nil {
			return err
		}
		e, err = proofEvidenceTx(tx, txID)
		return err
	})
	return e, err
}

// proofEvidenceTx records a destination's signed proof for a transmission
// as matched evidence and marks its intent dirty; once per transmission.
func proofEvidenceTx(tx *sqlx.Tx, txID int64) (DeliveryEvidence, error) {
	var have int
	if err := tx.Get(&have, `SELECT COUNT(*) FROM delivery_evidence WHERE transmission_id = ? AND event = 'proof' AND matched = 1`, txID); err != nil {
		return DeliveryEvidence{}, err
	}
	if have > 0 {
		return DeliveryEvidence{}, nil
	}
	e := DeliveryEvidence{TransmissionID: txID, Source: "destination_node", Event: "proof",
		Completes: OutcomeAcceptedDestinationNode, Trust: "signed", Matched: true}
	if err := tx.QueryRow(`SELECT attempt_id, intent_id, corr_kind, corr_value, corr_scope, corr_peer FROM delivery_transmissions WHERE id = ?`, txID).
		Scan(&e.AttemptID, &e.IntentID, &e.CorrKind, &e.CorrValue, &e.CorrScope, &e.CorrPeer); err != nil {
		return DeliveryEvidence{}, err
	}
	id, err := insertEvidenceTx(tx, e)
	if err != nil {
		return DeliveryEvidence{}, err
	}
	e.ID = id
	_, err = tx.Exec(`UPDATE delivery_intents SET dirty = 1 WHERE id = ?`, e.IntentID)
	return e, err
}
