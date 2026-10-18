package tetra

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// A port that closes during a send says how far the send got (review
// 2026-10-18 13:47, TETRA-TX1): before the SDS was written it is ErrClosed
// alone, nothing went to the radio; after the whole SDS was written it is
// ErrSDSInFlight too, the radio may have sent it. [MESHSAT-1584]
func TestSendSDSTellsAPortClosedAfterTheWriteFromOneBefore(t *testing.T) {
	pdu, bits := EncodeText(0x30, "how far did it get")

	// radio runs one scripted radio on a pipe and returns the session.
	radio := func(t *testing.T, script func(br *bufio.Reader, c net.Conn)) *Session {
		t.Helper()
		client, end := net.Pipe()
		go func() {
			defer end.Close()
			script(bufio.NewReader(end), end)
		}()
		s := NewSession(client, nil, nil)
		t.Cleanup(func() { s.Close() })
		return s
	}
	send := func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := s.SendSDS(ctx, "2000", 0x30, pdu, bits)
		return err
	}

	// Closed before anything: nothing written.
	s := radio(t, func(*bufio.Reader, net.Conn) {})
	<-s.Done()
	if err := send(s); !errors.Is(err, ErrClosed) || errors.Is(err, ErrSDSInFlight) {
		t.Fatalf("closed before the send: %v; want ErrClosed alone", err)
	}

	// Closed after the service selection, before the SDS: nothing of the
	// SDS reached the radio.
	s = radio(t, func(br *bufio.Reader, c net.Conn) {
		br.ReadString('\r')
	})
	if err := send(s); !errors.Is(err, ErrClosed) || errors.Is(err, ErrSDSInFlight) {
		t.Fatalf("closed after the selection: %v; want ErrClosed alone", err)
	}

	// Closed after the whole SDS was written, before the radio answered.
	wrote := make(chan string, 1)
	s = radio(t, func(br *bufio.Reader, c net.Conn) {
		br.ReadString('\r')
		c.Write([]byte("OK\r\n"))
		wire, _ := br.ReadString('\x1a')
		wrote <- wire
	})
	err := send(s)
	if wire := <-wrote; !strings.HasPrefix(wire, "AT+CMGS=2000,") || !strings.HasSuffix(wire, "\x1a") {
		t.Fatalf("setup: the radio read %q", wire)
	}
	if !errors.Is(err, ErrSDSInFlight) || !errors.Is(err, ErrClosed) {
		t.Fatalf("closed after the SDS was written: %v; want ErrSDSInFlight (and ErrClosed)", err)
	}

	// A command other than a send still reads a closed port as ErrClosed.
	s = radio(t, func(br *bufio.Reader, c net.Conn) {
		br.ReadString('\r')
	})
	if _, err := s.Command(context.Background(), "ATI", 3*time.Second); !errors.Is(err, ErrClosed) || errors.Is(err, ErrSDSInFlight) {
		t.Fatalf("a command whose answer never came: %v", err)
	}
}

// refusingPort fails, writing nothing, every write that starts with prefix.
type refusingPort struct {
	net.Conn
	prefix string
}

func (p refusingPort) Write(b []byte) (int, error) {
	if strings.HasPrefix(string(b), p.prefix) {
		return 0, errors.New("write refused")
	}
	return p.Conn.Write(b)
}

// Every way a send can fail before the SDS was written carries
// ErrSDSNotWritten, and none after it does (review 2026-10-18 15:37,
// TETRA-ACK2). [MESHSAT-1584]
func TestSendSDSMarksEveryFailureBeforeTheSDSWasWritten(t *testing.T) {
	pdu, bits := EncodeText(0x31, "which phase")
	session := func(t *testing.T, refuse string, script func(br *bufio.Reader, c net.Conn)) *Session {
		t.Helper()
		client, end := net.Pipe()
		go func() {
			defer end.Close()
			script(bufio.NewReader(end), end)
		}()
		var port io.ReadWriteCloser = client
		if refuse != "" {
			port = refusingPort{Conn: client, prefix: refuse}
		}
		s := NewSession(port, nil, nil)
		t.Cleanup(func() { s.Close() })
		return s
	}
	answer := func(lines string) func(*bufio.Reader, net.Conn) {
		return func(br *bufio.Reader, c net.Conn) {
			br.ReadString('\r')
			c.Write([]byte(lines))
			time.Sleep(200 * time.Millisecond)
		}
	}
	afterSelection := func(lines string) func(*bufio.Reader, net.Conn) {
		return func(br *bufio.Reader, c net.Conn) {
			br.ReadString('\r')
			c.Write([]byte("OK\r\n"))
			br.ReadString('\x1a')
			c.Write([]byte(lines))
			time.Sleep(200 * time.Millisecond)
		}
	}
	wait := func(br *bufio.Reader, c net.Conn) { br.ReadString('\r'); time.Sleep(4 * time.Second) }
	for _, tc := range []struct {
		name    string
		refuse  string
		script  func(*bufio.Reader, net.Conn)
		ctxWait time.Duration
		before  bool
	}{
		{"selection +CME ERROR", "", answer("+CME ERROR: 3\r\n"), 0, true},
		{"selection ERROR", "", answer("ERROR\r\n"), 0, true},
		{"selection never answered (timeout)", "", wait, 0, true},
		{"cancelled while the selection waits", "", wait, 300 * time.Millisecond, true},
		{"selection write refused", "AT+CTSDS", wait, 0, true},
		{"AT+CMGS write refused", "AT+CMGS", func(br *bufio.Reader, c net.Conn) {
			br.ReadString('\r')
			c.Write([]byte("OK\r\n"))
			time.Sleep(200 * time.Millisecond)
		}, 0, true},
		{"AT+CMGS answered ERROR", "", afterSelection("ERROR\r\n"), 0, false},
		{"AT+CMGS answered +CME ERROR", "", afterSelection("+CME ERROR: 3\r\n"), 0, false},
		{"closed after the SDS", "", func(br *bufio.Reader, c net.Conn) {
			br.ReadString('\r')
			c.Write([]byte("OK\r\n"))
			br.ReadString('\x1a')
		}, 0, false},
		{"cancelled after the SDS", "", afterSelection(""), 300 * time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := session(t, tc.refuse, tc.script)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if tc.ctxWait > 0 {
				ctx, cancel = context.WithTimeout(context.Background(), tc.ctxWait)
				defer cancel()
			}
			_, err := s.SendSDS(ctx, "2000", 0x31, pdu, bits)
			if err == nil || errors.Is(err, ErrSDSNotWritten) != tc.before {
				t.Fatalf("%v; ErrSDSNotWritten %t, want %t", err, errors.Is(err, ErrSDSNotWritten), tc.before)
			}
		})
	}
}
