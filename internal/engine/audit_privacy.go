package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"meshsat/internal/database"
)

// The audit trail is the published exception to retention: SIDN fonds
// project 261042, deliverable D6 [MESHSAT-1137]. Its rows are kept for the
// hash chain, so their personal fields are kept out or redacted instead:
//   - a new row never holds a message's text, a phone number, an email
//     address, a long device number or an IP address (MinimizeAuditDetail,
//     applied in AuditEvent before the row is hashed);
//   - a row older than the retention period has what is left of those
//     redacted in place (RedactOlderThan), after a signed audit_checkpoint
//     row records that the whole chain verified intact. A redacted row keeps
//     its stored hash and its link; its hash is no longer recomputed.

var (
	auditPhone  = regexp.MustCompile(`\+[0-9][0-9 ]{7,16}[0-9]`)
	auditLong   = regexp.MustCompile(`\b[0-9]{15,20}\b`) // IMEI, ICCID, IMSI
	auditEmail  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	auditNodeID = regexp.MustCompile(`![0-9a-fA-F]{8}\b`)
)

func maskPersonal(s string, nodeIDs bool) string {
	s = auditPhone.ReplaceAllString(s, "[phone]")
	s = auditLong.ReplaceAllString(s, "[number]")
	s = auditEmail.ReplaceAllString(s, "[email]")
	if nodeIDs {
		s = auditNodeID.ReplaceAllString(s, "![node]")
	}
	return s
}

// jsonWithout drops keys from a JSON object, putting the length of a
// dropped text value in its place under <key>_chars when chars is set.
func jsonWithout(detail string, chars bool, keys ...string) (string, bool) {
	var m map[string]any
	if json.Unmarshal([]byte(detail), &m) != nil {
		return detail, false
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		delete(m, k)
		if s, isText := v.(string); isText && chars {
			m[k+"_chars"] = utf8.RuneCountInString(s)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return detail, false
	}
	return string(b), true
}

// MinimizeAuditDetail is the detail a new audit row is written with: the
// message text of deliver and dispatch rows becomes its length (the
// delivery id column says which message), the SOS text its length, the
// requester's IP is left out of API token rows, and numbers and addresses
// anywhere else are masked.
func MinimizeAuditDetail(eventType, detail string) string {
	switch eventType {
	case "deliver", "dispatch":
		return fmt.Sprintf("%d characters", utf8.RuneCountInString(detail))
	case "sos_activated":
		if out, ok := jsonWithout(detail, true, "message"); ok {
			return out
		}
	case "api_token_set", "api_token_cleared":
		if out, ok := jsonWithout(detail, false, "remote"); ok {
			return out
		}
	}
	return maskPersonal(detail, false)
}

// RedactAuditDetail is what an old row's detail becomes after the
// retention period: as MinimizeAuditDetail, with the text of deliver and
// dispatch rows removed altogether and node ids masked as well.
func RedactAuditDetail(eventType, detail string) string {
	switch eventType {
	case "deliver", "dispatch":
		return "[redacted]"
	}
	return maskPersonal(MinimizeAuditDetail(eventType, detail), true)
}

func auditHash(e database.AuditLogEntry) string {
	h := sha256.New()
	h.Write([]byte(e.PrevHash))
	h.Write([]byte(e.Timestamp))
	h.Write([]byte(e.EventType))
	h.Write([]byte(e.Detail))
	return hex.EncodeToString(h.Sum(nil))
}

// ChainReport is a verification of the whole audit chain.
type ChainReport struct {
	Rows          int    `json:"rows"`
	HashesChecked int    `json:"hashes_checked"`
	Redacted      int    `json:"redacted"`
	BrokenAt      int64  `json:"broken_at"` // the row id, -1 when intact
	Reason        string `json:"reason,omitempty"`
}

// VerifyAll checks every row: its link to the row before, and its hash
// unless it was redacted.
func (ss *SigningService) VerifyAll() (ChainReport, error) {
	rows, err := ss.db.AuditRowsAll()
	if err != nil {
		return ChainReport{BrokenAt: -1}, err
	}
	rep := ChainReport{Rows: len(rows), BrokenAt: -1}
	for i, e := range rows {
		if i > 0 && e.PrevHash != rows[i-1].Hash {
			rep.BrokenAt, rep.Reason = e.ID, "link to the row before"
			return rep, nil
		}
		if e.RedactedAt != nil {
			rep.Redacted++
			continue
		}
		if auditHash(e) != e.Hash {
			rep.BrokenAt, rep.Reason = e.ID, "hash"
			return rep, nil
		}
		rep.HashesChecked++
	}
	return rep, nil
}

// RedactOlderThan redacts the personal fields of every row written before
// cutoff. It does nothing when no row changes; it refuses when the chain
// does not verify (a redaction must never cover a break); otherwise it
// first writes an audit_checkpoint row signed with the kit's key, naming
// the verified head, then redacts.
func (ss *SigningService) RedactOlderThan(cutoff, now time.Time) (int64, error) {
	rows, err := ss.db.AuditRowsToRedact(cutoff)
	if err != nil {
		return 0, err
	}
	changes := map[int64]string{}
	for _, r := range rows {
		if nd := RedactAuditDetail(r.EventType, r.Detail); nd != r.Detail {
			changes[r.ID] = nd
		}
	}
	if len(changes) == 0 {
		return 0, nil
	}
	rep, err := ss.VerifyAll()
	if err != nil {
		return 0, err
	}
	if rep.BrokenAt != -1 {
		return 0, fmt.Errorf("audit chain broken at row %d (%s): nothing redacted", rep.BrokenAt, rep.Reason)
	}
	all, err := ss.db.AuditRowsAll()
	if err != nil || len(all) == 0 {
		return 0, fmt.Errorf("audit head: %v", err)
	}
	head := all[len(all)-1]
	sig := ss.Sign([]byte(head.Hash))
	detail, _ := json.Marshal(map[string]any{
		"verified_rows": rep.Rows,
		"head_id":       head.ID,
		"head_hash":     head.Hash,
		"redacting":     len(changes),
		"signer":        ss.SignerID(),
		"signature":     hex.EncodeToString(sig),
	})
	ss.AuditEvent("audit_checkpoint", nil, nil, nil, nil, string(detail))
	return ss.db.RedactAuditRows(changes, now)
}
