package tetra

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned once the PEI port has closed or failed.
var ErrClosed = errors.New("tetra: PEI closed")

// ErrSDSInFlight is wrapped beside ErrClosed by SendSDS when the port
// closed after the whole SDS was written to it and before the radio
// answered: the radio may have sent it. A port that closed before the
// write returns ErrClosed alone. [MESHSAT-1584]
var ErrSDSInFlight = errors.New("tetra: the port closed after the SDS was written, before the radio answered")

// errClosedAfterWrite is exchangeHeld's ErrClosed once its write went out.
var errClosedAfterWrite = fmt.Errorf("%w after the command was written", ErrClosed)

// ErrTimeout is returned when the radio did not finish a command in time.
var ErrTimeout = errors.New("tetra: no answer from the radio")

// CMEError is a +CME ERROR the radio answered. Code 3 is "operation not
// allowed" (a group query in Direct Mode, MT-and-TE routing), 4 "not
// supported".
type CMEError struct{ Code int }

func (e *CMEError) Error() string { return fmt.Sprintf("tetra: +CME ERROR: %d", e.Code) }

// ErrRadioNotSent is wrapped by a send the radio took (+CMGS and OK) and
// then reported as not sent: a late +CMGS status other than 4, or a
// +CME ERROR while its report was awaited. Seen on 17 Oct 2026 when both
// kits sent at the same instant: the losing radio answered OK, then
// +CME ERROR: 42 and +CMGS: 0,5,<ref>, and nothing arrived; every SDS
// that arrived was reported 4. Whether anything went on air is not known.
var ErrRadioNotSent = errors.New("tetra: the radio reports the SDS not sent")

// sentStatus is the late +CMGS status of an SDS that went out.
const sentStatus = 4

// NotSentError says what the radio reported for a send it did not make.
type NotSentError struct {
	Status int // the late +CMGS status, -1 when no report came
	CME    int // a +CME ERROR that came while the report was awaited, 0 none
}

func (e *NotSentError) Error() string {
	msg := ErrRadioNotSent.Error()
	if e.Status >= 0 {
		msg += fmt.Sprintf(" (status %d", e.Status)
	} else {
		msg += " (no report"
	}
	if e.CME != 0 {
		msg += fmt.Sprintf(", +CME ERROR: %d", e.CME)
	}
	return msg + ")"
}

func (e *NotSentError) Unwrap() error { return ErrRadioNotSent }

// SendReport is the radio's late account of an SDS it sent:
// +CMGS: <SDS instance>,<SDS status>,<message reference>. The MTP3550
// sends it a moment after the OK, and not every time (17 Oct 2026: two of
// three sends).
type SendReport struct {
	Instance int
	Status   int
	Ref      int
}

// Session is one open PEI: a reader that splits the radio's lines, one AT
// command in flight at a time, incoming SDS and send reports delivered to
// callbacks. The callbacks run on the reader goroutine and must not block.
type Session struct {
	port io.ReadWriteCloser

	cmdMu   sync.Mutex  // one command in flight
	waiting atomic.Bool // a command is reading resp
	resp    chan string

	reports chan SendReport
	// awaitingReport routes a +CME ERROR to lateErrs while SendSDS waits
	// for a report, instead of to OnLine.
	awaitingReport atomic.Bool
	lateErrs       chan int

	OnSDS        func(Received)
	OnLine       func(string) // an unsolicited line nothing else claimed
	OnDecodeFail func(header, data string, err error)

	done    chan struct{}
	errMu   sync.Mutex
	readErr error
	closed  atomic.Bool
}

// NewSession starts reading the port. Set the callbacks before calling it.
func NewSession(port io.ReadWriteCloser, onSDS func(Received), onLine func(string)) *Session {
	s := &Session{
		port:     port,
		resp:     make(chan string, 64),
		reports:  make(chan SendReport, 8),
		lateErrs: make(chan int, 8),
		OnSDS:    onSDS,
		OnLine:   onLine,
		done:     make(chan struct{}),
	}
	go s.readLoop()
	return s
}

// Done is closed when the port fails or is closed.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the session ended (nil while it runs).
func (s *Session) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.readErr
}

// Close closes the port and ends the reader.
func (s *Session) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	err := s.port.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	return err
}

var reportRe = regexp.MustCompile(`^\+CMGS:\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)`)

func (s *Session) readLoop() {
	defer close(s.done)
	buf := make([]byte, 1024)
	var acc []byte
	var pending *Received
	var pendingHeader string
	for {
		n, err := s.port.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			for {
				i := indexNL(acc)
				if i < 0 {
					break
				}
				line := strings.TrimSpace(string(acc[:i]))
				acc = acc[i+1:]
				line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
				if line == "" {
					continue
				}
				if pending != nil {
					r := *pending
					pending = nil
					if err := r.DecodePDU(line); err != nil {
						if s.OnDecodeFail != nil {
							s.OnDecodeFail(pendingHeader, line, err)
						}
						continue
					}
					if s.OnSDS != nil {
						s.OnSDS(r)
					}
					continue
				}
				if strings.HasPrefix(line, "+CTSDSR:") {
					r, err := ParseCTSDSR(line)
					if err != nil {
						if s.OnDecodeFail != nil {
							s.OnDecodeFail(line, "", err)
						}
						continue
					}
					pending, pendingHeader = &r, line
					continue
				}
				if m := reportRe.FindStringSubmatch(line); m != nil {
					inst, _ := strconv.Atoi(m[1])
					st, _ := strconv.Atoi(m[2])
					ref, _ := strconv.Atoi(m[3])
					select {
					case s.reports <- SendReport{Instance: inst, Status: st, Ref: ref}:
					default:
					}
					continue
				}
				if s.awaitingReport.Load() && !s.waiting.Load() &&
					(strings.HasPrefix(line, "+CME ERROR:") || strings.HasPrefix(line, "+CMS ERROR:")) {
					code, _ := strconv.Atoi(strings.TrimSpace(line[strings.Index(line, ":")+1:]))
					select {
					case s.lateErrs <- code:
					default:
					}
					continue
				}
				if s.waiting.Load() {
					select {
					case s.resp <- line:
					default:
					}
					// A command's final line ends it here, on the reader:
					// what the radio says next (a send's late +CME ERROR
					// right after its OK) is late, not this command's.
					if isFinal(line) {
						s.waiting.Store(false)
					}
					continue
				}
				if s.OnLine != nil {
					s.OnLine(line)
				}
			}
			if len(acc) > 4096 { // a radio talking without line ends
				acc = acc[:0]
			}
		}
		if err != nil {
			s.errMu.Lock()
			if s.closed.Load() {
				s.readErr = ErrClosed
			} else {
				s.readErr = fmt.Errorf("tetra: PEI read: %w", err)
			}
			s.errMu.Unlock()
			return
		}
	}
}

// isFinal says whether a line ends an AT command's answer.
func isFinal(line string) bool {
	return line == "OK" || line == "ERROR" ||
		strings.HasPrefix(line, "+CME ERROR:") || strings.HasPrefix(line, "+CMS ERROR:")
}

func indexNL(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

// Command sends one AT command and returns the lines before OK. A
// +CME ERROR is a *CMEError.
func (s *Session) Command(ctx context.Context, cmd string, timeout time.Duration) ([]string, error) {
	return s.exchange(ctx, cmd, []byte(cmd+"\r"), timeout)
}

func (s *Session) exchange(ctx context.Context, cmd string, raw []byte, timeout time.Duration) ([]string, error) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	return s.exchangeHeld(ctx, cmd, raw, timeout)
}

// exchangeHeld is exchange for a caller that holds cmdMu.
func (s *Session) exchangeHeld(ctx context.Context, cmd string, raw []byte, timeout time.Duration) ([]string, error) {
	select {
	case <-s.done:
		return nil, ErrClosed
	default:
	}
	for drained := false; !drained; {
		select {
		case <-s.resp:
		default:
			drained = true
		}
	}
	s.waiting.Store(true)
	defer s.waiting.Store(false)
	if _, err := s.port.Write(raw); err != nil {
		return nil, fmt.Errorf("tetra: write %s: %w", cmd, err)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var lines []string
	for {
		select {
		case <-ctx.Done():
			return lines, ctx.Err()
		case <-s.done:
			return lines, errClosedAfterWrite
		case <-deadline.C:
			return lines, fmt.Errorf("%w (%s)", ErrTimeout, cmd)
		case line := <-s.resp:
			switch {
			case line == "OK":
				return lines, nil
			case line == "ERROR":
				return lines, fmt.Errorf("tetra: %s: ERROR", cmd)
			case strings.HasPrefix(line, "+CME ERROR:"), strings.HasPrefix(line, "+CMS ERROR:"):
				code, _ := strconv.Atoi(strings.TrimSpace(line[strings.Index(line, ":")+1:]))
				return lines, &CMEError{Code: code}
			case line == cmd: // echo, if ATE0 has not landed yet
			default:
				lines = append(lines, line)
			}
		}
	}
}

// Identity is what ATI reports.
type Identity struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	TEI          string `json:"tei"`
	ITSI         string `json:"itsi"`
	GTSI         string `json:"gtsi"`
	Firmware     string `json:"firmware"`
}

// Identify reads ATI.
func (s *Session) Identify(ctx context.Context) (Identity, error) {
	lines, err := s.Command(ctx, "ATI", 3*time.Second)
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	for _, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Manufacturer":
			id.Manufacturer = v
		case "Model Type":
			id.Model = v
		case "TEI":
			id.TEI = v
		case "ITSI":
			id.ITSI = v
		case "GTSI":
			id.GTSI = v
		case "Radio Ver":
			id.Firmware = v
		}
	}
	return id, nil
}

// Mode reads AT+CTOM?: 0 trunked (TMO, a network), 1 direct (DMO).
func (s *Session) Mode(ctx context.Context) (int, error) {
	lines, err := s.Command(ctx, "AT+CTOM?", 3*time.Second)
	if err != nil {
		return -1, err
	}
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "+CTOM:"); ok {
			return strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return -1, fmt.Errorf("tetra: AT+CTOM? answered %q", lines)
}

// SetMode switches the radio between TMO (0) and DMO (1).
func (s *Session) SetMode(ctx context.Context, mode int) error {
	_, err := s.Command(ctx, fmt.Sprintf("AT+CTOM=%d", mode), 10*time.Second)
	return err
}

var lastRangeRe = regexp.MustCompile(`\((\d+)-(\d+)\)\s*$`)

// MaxSDSBits reads the largest PDU AT+CMGS takes (the MTP3550: 1184 bits,
// 148 bytes).
func (s *Session) MaxSDSBits(ctx context.Context) (int, error) {
	lines, err := s.Command(ctx, "AT+CMGS=?", 3*time.Second)
	if err != nil {
		return 0, err
	}
	for _, l := range lines {
		if m := lastRangeRe.FindStringSubmatch(l); m != nil {
			return strconv.Atoi(m[2])
		}
	}
	return 0, fmt.Errorf("tetra: AT+CMGS=? answered %q", lines)
}

// Battery reads AT+CBC?, the charge in percent.
func (s *Session) Battery(ctx context.Context) (int, error) {
	lines, err := s.Command(ctx, "AT+CBC?", 3*time.Second)
	if err != nil {
		return -1, err
	}
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "+CBC:"); ok {
			f := strings.Split(v, ",")
			return strconv.Atoi(strings.TrimSpace(f[len(f)-1]))
		}
	}
	return -1, fmt.Errorf("tetra: AT+CBC? answered %q", lines)
}

// TextPIDs are the text services the gateway takes over from the radio's
// screen while it runs.
var TextPIDs = []byte{PIDText, PIDImmediateText}

// routedPIDs are what RouteTexts registers: the texts and the MeshSat link
// control (acks, hello), which the radio's screen has no use for.
var routedPIDs = append(append([]byte{}, TextPIDs...), LinkPID)

// RouteTexts sends incoming texts to the PEI (toPEI) or back to the
// radio's own screen. The MTP3550 refuses "both" (service profile 2) with
// +CME ERROR: 3, so a text goes to one or the other.
func (s *Session) RouteTexts(ctx context.Context, toPEI bool) error {
	profile := 0
	if toPEI {
		profile = 1
	}
	for _, pid := range routedPIDs {
		if _, err := s.Command(ctx, fmt.Sprintf("AT+CTSP=%d,3,%d", profile, pid), 3*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// reportWait is how long SendSDS keeps the line after the OK for the
// radio's +CMGS report, so a late line never lands in the next command.
var reportWait = 4 * time.Second

// SendSDS sends one SDS type 4 PDU to dest (an SSI or TSI). It returns
// once the radio took it (+CMGS: <instance> and OK) and its report came or
// reportWait passed. The report decides: status 4 is sent; another status,
// or a +CME ERROR while the report is awaited, is a *NotSentError
// (ErrRadioNotSent); no report at all counts as sent (one live send
// arrived without one) and returns a nil report. The line is held from
// the service selection to the report, so nothing the radio says late
// lands in another command.
func (s *Session) SendSDS(ctx context.Context, dest string, ref byte, pdu []byte, bits int) (*SendReport, error) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	// SDS type 4, SSI addressing, no area, normal priority, no end to end
	// encryption: the radio's own default (AT+CTSDS? 12,0,0,0,0).
	const sel = "AT+CTSDS=12,0,0,0,0"
	if _, err := s.exchangeHeld(ctx, sel, []byte(sel+"\r"), 3*time.Second); err != nil {
		return nil, err
	}
	for drained := false; !drained; {
		select {
		case <-s.reports:
		case <-s.lateErrs:
		default:
			drained = true
		}
	}
	s.awaitingReport.Store(true)
	defer s.awaitingReport.Store(false)
	cmd := fmt.Sprintf("AT+CMGS=%s,%d", dest, bits)
	raw := []byte(cmd + "\r\n" + strings.ToUpper(hex.EncodeToString(pdu)) + "\x1a")
	lines, err := s.exchangeHeld(ctx, cmd, raw, 20*time.Second)
	if errors.Is(err, errClosedAfterWrite) {
		return nil, fmt.Errorf("%w (%w)", ErrSDSInFlight, ErrClosed)
	}
	if err != nil {
		return nil, err
	}
	accepted := false
	for _, l := range lines {
		if strings.HasPrefix(l, "+CMGS:") {
			accepted = true
		}
	}
	if !accepted {
		return nil, fmt.Errorf("tetra: AT+CMGS answered OK without +CMGS: %q", lines)
	}
	t := time.NewTimer(reportWait)
	defer t.Stop()
	cme := 0
	for {
		select {
		case code := <-s.lateErrs:
			cme = code // the report usually follows; keep waiting for it
		case r := <-s.reports:
			// A PDU without the SDS-TL header (PID below 0x80, the MeshSat
			// link control) has no message reference for the report to
			// carry, so its report is the next one; an SDS-TL text's report
			// carries the text's reference (live: ref 181 for ref 0xB5).
			if r.Ref != int(ref) && !(len(pdu) > 0 && pdu[0] < 0x80) {
				continue
			}
			// The reader queues a +CME ERROR before the report that follows
			// it, but both can be waiting here at once and select picks
			// either: take the error too, so its code is never lost.
			select {
			case code := <-s.lateErrs:
				cme = code
			default:
			}
			if r.Status != sentStatus || cme != 0 {
				return &r, &NotSentError{Status: r.Status, CME: cme}
			}
			return &r, nil
		case <-t.C:
			if cme != 0 {
				return nil, &NotSentError{Status: -1, CME: cme}
			}
			return nil, nil
		case <-ctx.Done():
			return nil, nil
		case <-s.done:
			return nil, nil
		}
	}
}
