package smpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// State is the client's link state.
type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateBound        State = "bound"
)

// ErrNotBound is returned by Submit while the client has no bound session.
var ErrNotBound = errors.New("smpp: not bound to the cell")

// ErrBindRefused wraps a bind response the server refused.
var ErrBindRefused = errors.New("smpp: bind refused")

// ErrTimeout is returned when a response did not arrive in time.
var ErrTimeout = errors.New("smpp: timed out waiting for the response")

// Config configures a Client. Zero durations take the defaults.
type Config struct {
	Addr       string // host:port
	SystemID   string
	Password   string
	SystemType string

	DialTimeout    time.Duration // 10 s
	BindTimeout    time.Duration // 10 s
	EnquireEvery   time.Duration // 30 s: the server never sends one itself
	EnquireTimeout time.Duration // 10 s without the resp = dead session
	SubmitTimeout  time.Duration // 60 s: forward mode waits for the phone's ack
	ReconnectMin   time.Duration // 2 s
	ReconnectMax   time.Duration // 60 s
}

func (c *Config) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	set(&c.DialTimeout, 10*time.Second)
	set(&c.BindTimeout, 10*time.Second)
	set(&c.EnquireEvery, 30*time.Second)
	set(&c.EnquireTimeout, 10*time.Second)
	set(&c.SubmitTimeout, 60*time.Second)
	set(&c.ReconnectMin, 2*time.Second)
	set(&c.ReconnectMax, 60*time.Second)
}

// Callbacks are what the client reports to its owner. Every callback may
// be nil. OnDeliver runs on the reader goroutine and must return at once:
// the MSC fails the phone's text after 5 s without the deliver_sm_resp.
type Callbacks struct {
	// OnDeliver answers a deliver_sm with the status to send back.
	OnDeliver func(sm SM) uint32
	// OnAlert receives an alert_notification (it has no response).
	OnAlert func(a Alert)
	// OnState is called on every state change; err says why a session ended.
	OnState func(state State, err error)
}

// Status is a snapshot of the client for the API.
type Status struct {
	State         State      `json:"state"`
	Addr          string     `json:"addr"`
	SystemID      string     `json:"system_id"`
	BoundSince    *time.Time `json:"bound_since,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastErrorAt   *time.Time `json:"last_error_at,omitempty"`
	Reconnects    int64      `json:"reconnects"`
	BindRefusals  int64      `json:"bind_refusals"`
	EnquireRTTMs  int64      `json:"enquire_rtt_ms"`
	LastRxAt      *time.Time `json:"last_rx_at,omitempty"`
	LastDeliverAt *time.Time `json:"last_deliver_at,omitempty"`
	Submits       int64      `json:"submits"`
	Delivers      int64      `json:"delivers"`
	Alerts        int64      `json:"alerts"`
}

// Client keeps one transceiver session to an SMPP server, reconnecting
// with a capped backoff, and answers what the server sends.
type Client struct {
	cfg Config
	cb  Callbacks

	mu         sync.Mutex
	sess       *session
	state      State
	boundSince time.Time
	lastErr    string
	lastErrAt  time.Time
	enquireRTT time.Duration
	lastRx     time.Time
	lastDeliv  time.Time

	reconnects   atomic.Int64
	bindRefusals atomic.Int64
	submits      atomic.Int64
	delivers     atomic.Int64
	alerts       atomic.Int64

	done chan struct{}
}

// New makes a client; Run starts it.
func New(cfg Config, cb Callbacks) *Client {
	cfg.defaults()
	return &Client{cfg: cfg, cb: cb, state: StateDisconnected, done: make(chan struct{})}
}

// Status returns a snapshot.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{
		State:        c.state,
		Addr:         c.cfg.Addr,
		SystemID:     c.cfg.SystemID,
		LastError:    c.lastErr,
		Reconnects:   c.reconnects.Load(),
		BindRefusals: c.bindRefusals.Load(),
		EnquireRTTMs: c.enquireRTT.Milliseconds(),
		Submits:      c.submits.Load(),
		Delivers:     c.delivers.Load(),
		Alerts:       c.alerts.Load(),
	}
	if !c.boundSince.IsZero() && c.state == StateBound {
		t := c.boundSince
		s.BoundSince = &t
	}
	if !c.lastErrAt.IsZero() {
		t := c.lastErrAt
		s.LastErrorAt = &t
	}
	if !c.lastRx.IsZero() {
		t := c.lastRx
		s.LastRxAt = &t
	}
	if !c.lastDeliv.IsZero() {
		t := c.lastDeliv
		s.LastDeliverAt = &t
	}
	return s
}

// Bound reports whether a session is bound right now.
func (c *Client) Bound() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == StateBound && c.sess != nil
}

func (c *Client) setState(s State, err error) {
	c.mu.Lock()
	c.state = s
	if s == StateBound {
		c.boundSince = time.Now()
	}
	if err != nil {
		c.lastErr = err.Error()
		c.lastErrAt = time.Now()
	}
	c.mu.Unlock()
	if c.cb.OnState != nil {
		c.cb.OnState(s, err)
	}
}

// Run keeps a session bound until ctx ends, then unbinds and returns.
func (c *Client) Run(ctx context.Context) {
	defer close(c.done)
	wait := c.cfg.ReconnectMin
	for {
		if ctx.Err() != nil {
			return
		}
		c.setState(StateConnecting, nil)
		sess, err := c.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				c.setState(StateDisconnected, nil)
				return
			}
			refused := errors.Is(err, ErrBindRefused)
			if refused {
				c.bindRefusals.Add(1)
				wait = c.cfg.ReconnectMax // a wrong password does not fix itself in two seconds
			}
			c.setState(StateDisconnected, err)
			log.Warn().Err(err).Str("addr", c.cfg.Addr).Dur("retry_in", wait).Msg("smpp: session not established")
			if !sleepCtx(ctx, wait) {
				return
			}
			if wait < c.cfg.ReconnectMax {
				wait *= 2
				if wait > c.cfg.ReconnectMax {
					wait = c.cfg.ReconnectMax
				}
			}
			continue
		}
		wait = c.cfg.ReconnectMin
		c.mu.Lock()
		c.sess = sess
		c.mu.Unlock()
		c.setState(StateBound, nil)
		log.Info().Str("addr", c.cfg.Addr).Str("system_id", c.cfg.SystemID).Msg("smpp: bound as transceiver")

		endErr := c.serve(ctx, sess)

		c.mu.Lock()
		c.sess = nil
		c.mu.Unlock()
		if ctx.Err() != nil {
			sess.unbindAndClose(2 * time.Second)
			c.setState(StateDisconnected, nil)
			return
		}
		sess.close()
		c.reconnects.Add(1)
		c.setState(StateDisconnected, endErr)
		log.Warn().Err(endErr).Str("addr", c.cfg.Addr).Dur("retry_in", wait).Msg("smpp: session ended, reconnecting")
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// Wait blocks until Run returned.
func (c *Client) Wait() { <-c.done }

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// connect dials and binds. A refused bind wraps ErrBindRefused.
func (c *Client) connect(ctx context.Context) (*session, error) {
	d := net.Dialer{Timeout: c.cfg.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.cfg.Addr, err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}
	s := newSession(conn)
	body, err := EncodeBind(Bind{SystemID: c.cfg.SystemID, Password: c.cfg.Password, SystemType: c.cfg.SystemType, Version: Version})
	if err != nil {
		conn.Close()
		return nil, err
	}
	seq := s.nextSeq()
	ch := s.expect(seq)
	if err := s.write(Encode(CmdBindTransceiver, 0, seq, body)); err != nil {
		s.forget(seq)
		conn.Close()
		return nil, fmt.Errorf("bind: %w", err)
	}
	// Read the response directly: the reader goroutine starts after the bind.
	conn.SetReadDeadline(time.Now().Add(c.cfg.BindTimeout))
	pdu, err := readPDU(conn)
	conn.SetReadDeadline(time.Time{})
	s.forget(seq)
	close(ch)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("bind: no response: %w", err)
	}
	if pdu.ID != CmdBindTransceiverRe {
		conn.Close()
		return nil, fmt.Errorf("bind: unexpected answer 0x%08x", pdu.ID)
	}
	if pdu.Status != StatusOK {
		conn.Close()
		return nil, fmt.Errorf("%w: %s (0x%02x)", ErrBindRefused, StatusText(pdu.Status), pdu.Status)
	}
	return s, nil
}

// serve runs the reader and the enquire_link clock until the session ends.
func (c *Client) serve(ctx context.Context, s *session) error {
	readErr := make(chan error, 1)
	go func() { readErr <- c.readLoop(s) }()

	ticker := time.NewTicker(c.cfg.EnquireEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case <-ticker.C:
			start := time.Now()
			if _, err := c.request(ctx, s, CmdEnquireLink, nil, c.cfg.EnquireTimeout); err != nil {
				s.close()
				<-readErr
				return fmt.Errorf("enquire_link: %w", err)
			}
			c.mu.Lock()
			c.enquireRTT = time.Since(start)
			c.mu.Unlock()
		}
	}
}

// request sends a PDU and waits for its response on this session.
func (c *Client) request(ctx context.Context, s *session, id uint32, body []byte, timeout time.Duration) (PDU, error) {
	seq := s.nextSeq()
	ch := s.expect(seq)
	if err := s.write(Encode(id, 0, seq, body)); err != nil {
		s.forget(seq)
		return PDU{}, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case pdu, ok := <-ch:
		if !ok {
			return PDU{}, errors.New("session closed")
		}
		return pdu, nil
	case <-t.C:
		s.forget(seq)
		return PDU{}, ErrTimeout
	case <-ctx.Done():
		s.forget(seq)
		return PDU{}, ctx.Err()
	}
}

// Submit sends one submit_sm on the bound session and waits for its
// response. The status is the server's command_status (0 = accepted; in
// forward mode, delivered to the phone). err is set for a transport
// failure, a timeout or no session, never for a non-zero status.
func (c *Client) Submit(ctx context.Context, sm SM) (resp SMResp, status uint32, err error) {
	c.mu.Lock()
	s := c.sess
	c.mu.Unlock()
	if s == nil {
		return SMResp{}, 0, ErrNotBound
	}
	body, err := EncodeSM(sm)
	if err != nil {
		return SMResp{}, 0, err
	}
	c.submits.Add(1)
	pdu, err := c.request(ctx, s, CmdSubmitSM, body, c.cfg.SubmitTimeout)
	if err != nil {
		return SMResp{}, 0, err
	}
	if pdu.ID == CmdGenericNack {
		return SMResp{}, pdu.Status, nil
	}
	resp, _ = DecodeSMResp(pdu.Body)
	return resp, pdu.Status, nil
}

func (c *Client) readLoop(s *session) error {
	for {
		pdu, err := readPDU(s.conn)
		if err != nil {
			if s.isClosed() {
				return errors.New("session closed")
			}
			if errors.Is(err, io.EOF) {
				return errors.New("the cell closed the connection")
			}
			return err
		}
		c.mu.Lock()
		c.lastRx = time.Now()
		c.mu.Unlock()
		if pdu.IsResponse() {
			s.deliverResp(pdu)
			continue
		}
		switch pdu.ID {
		case CmdDeliverSM:
			c.handleDeliver(s, pdu)
		case CmdEnquireLink:
			s.write(Encode(CmdEnquireLinkResp, StatusOK, pdu.Sequence, nil))
		case CmdUnbind:
			s.write(Encode(CmdUnbindResp, StatusOK, pdu.Sequence, nil))
			s.close()
			return errors.New("the cell unbound the session")
		case CmdAlertNotification:
			c.alerts.Add(1)
			if a, err := DecodeAlert(pdu.Body); err == nil {
				if c.cb.OnAlert != nil {
					c.cb.OnAlert(a)
				}
			} else {
				log.Warn().Err(err).Msg("smpp: alert_notification did not decode")
			}
		default:
			log.Warn().Uint32("command", pdu.ID).Msg("smpp: unexpected request from the cell")
			s.write(Encode(CmdGenericNack, StatusInvalidCmdID, pdu.Sequence, nil))
		}
	}
}

func (c *Client) handleDeliver(s *session, pdu PDU) {
	c.delivers.Add(1)
	c.mu.Lock()
	c.lastDeliv = time.Now()
	c.mu.Unlock()
	sm, err := DecodeSM(pdu.Body)
	if err != nil {
		log.Warn().Err(err).Msg("smpp: deliver_sm did not decode")
		s.write(Encode(CmdDeliverSMResp, StatusInvalidMsgLen, pdu.Sequence, EncodeSMResp(SMResp{})))
		return
	}
	status := StatusOK
	if c.cb.OnDeliver != nil {
		status = c.safeDeliver(sm)
	}
	// Answer first: the MSC fails the phone's text 5 s after sending it.
	s.write(Encode(CmdDeliverSMResp, status, pdu.Sequence, EncodeSMResp(SMResp{})))
}

func (c *Client) safeDeliver(sm SM) (status uint32) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("smpp: deliver handler panicked")
			status = StatusSysErr
		}
	}()
	return c.cb.OnDeliver(sm)
}

// --- session ---

type session struct {
	conn    net.Conn
	writeMu sync.Mutex
	seq     atomic.Uint32
	mu      sync.Mutex
	pending map[uint32]chan PDU
	closed  atomic.Bool
}

func newSession(conn net.Conn) *session {
	return &session{conn: conn, pending: map[uint32]chan PDU{}}
}

func (s *session) nextSeq() uint32 {
	for {
		n := s.seq.Add(1)
		if n != 0 && n <= 0x7FFFFFFF {
			return n
		}
		s.seq.Store(0)
	}
}

func (s *session) expect(seq uint32) chan PDU {
	ch := make(chan PDU, 1)
	s.mu.Lock()
	s.pending[seq] = ch
	s.mu.Unlock()
	return ch
}

func (s *session) forget(seq uint32) {
	s.mu.Lock()
	delete(s.pending, seq)
	s.mu.Unlock()
}

func (s *session) deliverResp(pdu PDU) {
	s.mu.Lock()
	ch, ok := s.pending[pdu.Sequence]
	if ok {
		delete(s.pending, pdu.Sequence)
	}
	s.mu.Unlock()
	if !ok {
		log.Warn().Uint32("seq", pdu.Sequence).Uint32("command", pdu.ID).Uint32("status", pdu.Status).
			Msg("smpp: late or unknown response (a submit_sm_resp after its timeout?)")
		return
	}
	ch <- pdu
}

func (s *session) write(frame []byte) error {
	if s.closed.Load() {
		return errors.New("session closed")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := s.conn.Write(frame)
	return err
}

func (s *session) isClosed() bool { return s.closed.Load() }

func (s *session) close() {
	if s.closed.Swap(true) {
		return
	}
	s.conn.Close()
	s.mu.Lock()
	for seq, ch := range s.pending {
		close(ch)
		delete(s.pending, seq)
	}
	s.mu.Unlock()
}

// unbindAndClose sends unbind, waits briefly for the resp, then closes,
// so the MSC drops the route cleanly instead of finding a dead socket.
func (s *session) unbindAndClose(wait time.Duration) {
	if s.closed.Load() {
		return
	}
	seq := s.nextSeq()
	ch := s.expect(seq)
	if err := s.write(Encode(CmdUnbind, 0, seq, nil)); err == nil {
		t := time.NewTimer(wait)
		select {
		case <-ch:
		case <-t.C:
		}
		t.Stop()
	}
	s.close()
}

// readPDU reads one framed PDU.
func readPDU(r io.Reader) (PDU, error) {
	var hdr [16]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return PDU{}, err
	}
	h, _ := DecodeHeader(hdr[:])
	if h.Length < 16 || h.Length > 1<<16 {
		return PDU{}, fmt.Errorf("smpp: bad command_length %d", h.Length)
	}
	body := make([]byte, h.Length-16)
	if _, err := io.ReadFull(r, body); err != nil {
		return PDU{}, err
	}
	return PDU{Header: h, Body: body}, nil
}
