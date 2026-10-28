package post

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// A slip is what the kit's receipt printer puts on paper for a post: the
// pickup code large, the recipient masked, the time and the portal URL;
// for a reply, the code and the first 100 characters of it. The paper is
// the person's copy of the code when a screenshot is not an option. The
// layout and the ESC/POS bytes are the printer service's
// (deploy/printer/meshsat-receipt-printer, the only ESC/POS code the kits
// have): the Bridge only says what goes on the slip, as a "post_slip"
// event on /api/events, and only with MESHSAT_POST_PRINT on.

// SlipEventType is the event the printer service follows.
const SlipEventType = "post_slip"

// Slip kinds.
const (
	SlipPost  = "post"  // an approved post left the queue
	SlipReply = "reply" // a reply came back for a code
)

// SlipTextChars is how much of a reply goes on paper.
const SlipTextChars = 100

// Slip is the content of one slip.
type Slip struct {
	Kind      string `json:"kind"`
	Code      string `json:"code"`
	To        string `json:"to"`     // the recipient, masked
	At        string `json:"at"`     // RFC 3339 UTC
	URL       string `json:"url"`    // the portal
	Pickup    string `json:"pickup"` // the pickup page with the code filled in
	Text      string `json:"text,omitempty"`
	Emergency bool   `json:"emergency,omitempty"`
	Operator  string `json:"operator,omitempty"`
}

// Printer takes a slip. The kit's is EventPrinter; a test's records them.
type Printer interface {
	PrintSlip(Slip)
}

// EventPrinter publishes a slip on the Bridge's event stream, where the
// receipt printer service picks it up.
type EventPrinter struct {
	Emit func(transport.MeshEvent)
}

// PrintSlip emits the slip as a post_slip event.
func (p EventPrinter) PrintSlip(s Slip) {
	if p.Emit == nil {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	msg := "Community Post slip: code " + s.Code
	if s.Kind == SlipReply {
		msg = "Community Post reply slip: code " + s.Code
	}
	p.Emit(transport.MeshEvent{Type: SlipEventType, Message: msg, Data: data, Time: s.At})
}

// MaskRecipient hides most of a recipient: +31612345678 becomes
// +3161****78, maria@example.org becomes m***@example.org. The person who
// wrote knows whom they wrote to; the slip is read by anyone who finds it.
func MaskRecipient(v string) string {
	if i := strings.Index(v, "@"); i > 0 {
		return v[:1] + "***" + v[i:]
	}
	switch n := len(v); {
	case n >= 9:
		return v[:5] + "****" + v[n-2:]
	case n >= 6:
		return v[:3] + "****" + v[n-2:]
	}
	return "****"
}

// excerpt is the first n characters of text, with "..." when it was cut.
func excerpt(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return strings.TrimSpace(string(r[:n])) + "..."
}

// PrinterReport is what the receipt printer service tells the Bridge about
// itself: that it follows the event stream, which printer it uses, where
// the slip sequence is, and how its last slip went.
type PrinterReport struct {
	Service    string `json:"service,omitempty"`
	Printer    string `json:"printer,omitempty"`
	Count      int    `json:"count"`
	Queued     int    `json:"queued"`
	LastSlipAt string `json:"last_slip_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// PrinterStaleAfter is how long a report counts as the printer being there.
const PrinterStaleAfter = 3 * time.Minute

// PrinterState is the printer as the kit knows it.
type PrinterState struct {
	Print      bool           `json:"print"`                 // MESHSAT_POST_PRINT
	Heard      bool           `json:"heard"`                 // a report came within PrinterStaleAfter
	AgeSeconds int            `json:"age_seconds,omitempty"` // of the last report
	ReportedAt string         `json:"reported_at,omitempty"`
	Report     *PrinterReport `json:"report,omitempty"`
}

type printerMemory struct {
	mu     sync.Mutex
	report *PrinterReport
	at     time.Time
}

// ReportPrinter takes the printer service's report.
func (s *Service) ReportPrinter(r PrinterReport) {
	s.printer.mu.Lock()
	s.printer.report, s.printer.at = &r, s.now()
	s.printer.mu.Unlock()
}

// PrinterState is the printer as the kit knows it now.
func (s *Service) PrinterState() PrinterState {
	st := PrinterState{Print: s.cfg.Print}
	s.printer.mu.Lock()
	defer s.printer.mu.Unlock()
	if s.printer.report == nil {
		return st
	}
	age := s.now().Sub(s.printer.at)
	r := *s.printer.report
	st.Report, st.ReportedAt, st.AgeSeconds = &r, s.printer.at.Format(time.RFC3339), int(age.Seconds())
	st.Heard = age <= PrinterStaleAfter
	return st
}

// printSlip hands a slip to the printer, when there is one.
func (s *Service) printSlip(m *database.PostMessage, reply *database.PostReply) {
	if s.deps.Printer == nil || m == nil {
		return
	}
	slip := Slip{Kind: SlipPost, Code: m.Code, To: MaskRecipient(m.Recipient), At: s.now().Format(time.RFC3339),
		URL: s.cfg.PortalURL, Pickup: s.pickupURL(m.Code), Emergency: m.Class == database.PostClassEmergency,
		Operator: s.Settings().Operator}
	if reply != nil {
		slip.Kind, slip.Text = SlipReply, excerpt(reply.Text, SlipTextChars)
	}
	s.deps.Printer.PrintSlip(slip)
}

// pickupURL is the portal's pickup page with the code filled in.
func (s *Service) pickupURL(code string) string {
	return strings.TrimSuffix(s.cfg.PortalURL, "/") + "/pickup?code=" + code
}
