// Package smpptest is a scriptable SMPP 3.4 server for tests: it behaves
// like osmo-msc's SMSC where the Bridge's gateway can tell (closed bind
// policy, one route that follows the newest bind, deliver_sm with a
// response wait, transaction-mode submit_sm answers, alert_notification,
// server-side unbind or a dropped socket). [MESHSAT-1557]
package smpptest

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"meshsat/internal/smpp"
)

// SubmitFunc decides what a submit_sm gets back: the status, a delay before
// the response (transaction mode waits for the phone) and a message id.
type SubmitFunc func(sm smpp.SM) (status uint32, delay time.Duration, messageID string)

// Server is one listening fake SMSC.
type Server struct {
	ln   net.Listener
	Addr string

	// SystemID and Password are the one accepted credential pair; an empty
	// SystemID accepts every bind.
	SystemID, Password string
	// OnSubmit is consulted for every submit_sm; nil accepts with status 0.
	OnSubmit SubmitFunc
	// AnswerEnquire false makes the server swallow enquire_link (a dead
	// MSC that keeps the socket open).
	AnswerEnquire atomic.Bool

	mu       sync.Mutex
	sessions []*Session
	current  *Session // the route: the newest bound session
	submits  []smpp.SM
	binds    int
	done     chan struct{}
	closed   atomic.Bool
}

// Session is one accepted connection.
type Session struct {
	srv     *Server
	conn    net.Conn
	writeMu sync.Mutex
	seq     atomic.Uint32
	bound   atomic.Bool
	closed  atomic.Bool
	sysID   string
	mu      sync.Mutex
	pending map[uint32]chan smpp.PDU
}

// New starts a server on 127.0.0.1 with an ephemeral port.
func New() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, Addr: ln.Addr().String(), done: make(chan struct{})}
	s.AnswerEnquire.Store(true)
	go s.accept()
	return s, nil
}

func (s *Server) accept() {
	defer close(s.done)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		sess := &Session{srv: s, conn: conn, pending: map[uint32]chan smpp.PDU{}}
		s.mu.Lock()
		s.sessions = append(s.sessions, sess)
		s.mu.Unlock()
		go sess.serve()
	}
}

// Close stops listening and drops every session.
func (s *Server) Close() {
	if s.closed.Swap(true) {
		return
	}
	s.ln.Close()
	s.mu.Lock()
	sessions := append([]*Session(nil), s.sessions...)
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.close()
	}
	<-s.done
}

// Binds counts successful binds so far.
func (s *Server) Binds() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binds
}

// Submits returns every submit_sm received.
func (s *Server) Submits() []smpp.SM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]smpp.SM(nil), s.submits...)
}

// Current returns the session the route points at (the newest bind).
func (s *Server) Current() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// WaitBound waits until a bound session exists.
func (s *Server) WaitBound(timeout time.Duration) *Session {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c := s.Current(); c != nil && c.bound.Load() && !c.closed.Load() {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// Deliver sends deliver_sm on the current session and waits for the
// deliver_sm_resp, as the MSC does for 5 s.
func (s *Server) Deliver(sm smpp.SM, wait time.Duration) (status uint32, err error) {
	c := s.Current()
	if c == nil || !c.bound.Load() {
		return 0, errors.New("no bound session (route empty)")
	}
	body, err := smpp.EncodeSM(sm)
	if err != nil {
		return 0, err
	}
	pdu, err := c.request(smpp.CmdDeliverSM, body, wait)
	if err != nil {
		return 0, err
	}
	return pdu.Status, nil
}

// Alert sends alert_notification for msisdn with the availability status
// (0 available, 2 unavailable) on the current session.
func (s *Server) Alert(msisdn string, avail byte) error {
	c := s.Current()
	if c == nil || !c.bound.Load() {
		return errors.New("no bound session")
	}
	body := smpp.EncodeAlert(smpp.Alert{
		SourceTON: smpp.TONNetworkSpecific, SourceNPI: smpp.NPIISDN, SourceAddr: msisdn,
		TLVs: []smpp.TLV{{Tag: smpp.TagMSAvailabilityStatus, Value: []byte{avail}}},
	})
	return c.write(smpp.Encode(smpp.CmdAlertNotification, 0, c.nextSeq(), body))
}

// Unbind sends unbind on the current session and closes it after the resp
// (what an osmo-msc restart looks like).
func (s *Server) Unbind() error {
	c := s.Current()
	if c == nil {
		return errors.New("no session")
	}
	_, err := c.request(smpp.CmdUnbind, nil, 2*time.Second)
	c.close()
	return err
}

// Drop closes the current session's socket without a word.
func (s *Server) Drop() {
	if c := s.Current(); c != nil {
		c.close()
	}
}

func (s *Server) record(sm smpp.SM) {
	s.mu.Lock()
	s.submits = append(s.submits, sm)
	s.mu.Unlock()
}

// --- session ---

func (c *Session) nextSeq() uint32 { return c.seq.Add(1) }

func (c *Session) write(frame []byte) error {
	if c.closed.Load() {
		return errors.New("closed")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.conn.Write(frame)
	return err
}

func (c *Session) close() {
	if c.closed.Swap(true) {
		return
	}
	c.conn.Close()
	c.srv.mu.Lock()
	if c.srv.current == c {
		c.srv.current = nil // osmo-msc: acl->esme = NULL on close
	}
	c.srv.mu.Unlock()
	c.mu.Lock()
	for seq, ch := range c.pending {
		close(ch)
		delete(c.pending, seq)
	}
	c.mu.Unlock()
}

func (c *Session) request(id uint32, body []byte, wait time.Duration) (smpp.PDU, error) {
	seq := c.nextSeq()
	ch := make(chan smpp.PDU, 1)
	c.mu.Lock()
	c.pending[seq] = ch
	c.mu.Unlock()
	if err := c.write(smpp.Encode(id, 0, seq, body)); err != nil {
		return smpp.PDU{}, err
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case pdu, ok := <-ch:
		if !ok {
			return smpp.PDU{}, errors.New("session closed")
		}
		return pdu, nil
	case <-t.C:
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
		return smpp.PDU{}, fmt.Errorf("no response within %s (the MSC would fail the phone with cause 38)", wait)
	}
}

func (c *Session) serve() {
	defer c.close()
	for {
		pdu, err := readPDU(c.conn)
		if err != nil {
			return
		}
		if pdu.IsResponse() {
			c.mu.Lock()
			ch, ok := c.pending[pdu.Sequence]
			if ok {
				delete(c.pending, pdu.Sequence)
			}
			c.mu.Unlock()
			if ok {
				ch <- pdu
			}
			continue
		}
		switch pdu.ID {
		case smpp.CmdBindTransceiver, smpp.CmdBindReceiver, smpp.CmdBindTransmitter:
			c.handleBind(pdu)
		case smpp.CmdUnbind:
			c.bound.Store(false)
			c.srv.mu.Lock()
			if c.srv.current == c {
				c.srv.current = nil
			}
			c.srv.mu.Unlock()
			c.write(smpp.Encode(smpp.CmdUnbindResp, 0, pdu.Sequence, nil))
		case smpp.CmdEnquireLink:
			if c.srv.AnswerEnquire.Load() {
				c.write(smpp.Encode(smpp.CmdEnquireLinkResp, 0, pdu.Sequence, nil))
			}
		case smpp.CmdSubmitSM:
			c.handleSubmit(pdu)
		default:
			c.write(smpp.Encode(smpp.CmdGenericNack, smpp.StatusInvalidCmdID, pdu.Sequence, nil))
		}
	}
}

func (c *Session) handleBind(pdu smpp.PDU) {
	respID := pdu.ID | 0x80000000
	b, err := smpp.DecodeBind(pdu.Body)
	if err != nil {
		c.write(smpp.Encode(smpp.CmdGenericNack, smpp.StatusInvalidMsgLen, pdu.Sequence, nil))
		return
	}
	status := smpp.StatusOK
	switch {
	case b.Version != smpp.Version:
		status = smpp.StatusSysErr
	case c.bound.Load():
		status = smpp.StatusAlreadyBound
	case c.srv.SystemID != "" && b.SystemID != c.srv.SystemID:
		status = smpp.StatusInvalidSystemID
	case c.srv.SystemID != "" && b.Password != c.srv.Password:
		status = smpp.StatusInvalidPassword
	}
	if status == smpp.StatusOK {
		c.sysID = b.SystemID
		c.bound.Store(true)
		c.srv.mu.Lock()
		c.srv.current = c // osmo-msc: acl->esme = esme, the newest bind takes the route
		c.srv.binds++
		c.srv.mu.Unlock()
	}
	c.write(smpp.Encode(respID, status, pdu.Sequence, smpp.EncodeBindResp(smpp.BindResp{SystemID: "fake-msc"})))
}

func (c *Session) handleSubmit(pdu smpp.PDU) {
	if !c.bound.Load() {
		c.write(smpp.Encode(smpp.CmdSubmitSMResp, smpp.StatusInvalidBindStat, pdu.Sequence, nil))
		return
	}
	sm, err := smpp.DecodeSM(pdu.Body)
	if err != nil {
		c.write(smpp.Encode(smpp.CmdGenericNack, smpp.StatusInvalidMsgLen, pdu.Sequence, nil))
		return
	}
	c.srv.record(sm)
	status, delay, msgID := smpp.StatusOK, time.Duration(0), ""
	if c.srv.OnSubmit != nil {
		status, delay, msgID = c.srv.OnSubmit(sm)
	}
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		c.write(smpp.Encode(smpp.CmdSubmitSMResp, status, pdu.Sequence, smpp.EncodeSMResp(smpp.SMResp{MessageID: msgID})))
	}()
}

func readPDU(r io.Reader) (smpp.PDU, error) {
	var hdr [16]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return smpp.PDU{}, err
	}
	h, _ := smpp.DecodeHeader(hdr[:])
	if h.Length < 16 || h.Length > 1<<16 {
		return smpp.PDU{}, fmt.Errorf("bad length %d", h.Length)
	}
	body := make([]byte, h.Length-16)
	if _, err := io.ReadFull(r, body); err != nil {
		return smpp.PDU{}, err
	}
	return smpp.PDU{Header: h, Body: body}, nil
}
