package relayclient

import (
	"encoding/base64"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// wsConn presents a client-side relay WebSocket (bare payload frames) as a
// net.Conn, so tls.Client and http.Transport run over it unchanged. This is
// the Go form of what the phone does; the live test is its first user.
type wsConn struct {
	ws     *websocket.Conn
	local  relayAddr
	remote relayAddr
	rbuf   []byte
	wmu    sync.Mutex
	rdl    time.Time
}

func newWSConn(ws *websocket.Conn, clientID, bridgeID string) *wsConn {
	return &wsConn{ws: ws, local: relayAddr{clientID}, remote: relayAddr{bridgeID}}
}

func (c *wsConn) Read(p []byte) (int, error) {
	for len(c.rbuf) == 0 {
		_ = c.ws.SetReadDeadline(c.rdl)
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		c.rbuf = data
	}
	n := copy(p, c.rbuf)
	c.rbuf = c.rbuf[n:]
	return n, nil
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	written := 0
	for len(p) > 0 {
		n := len(p)
		if n > chunkSize {
			n = chunkSize
		}
		if err := c.ws.WriteMessage(websocket.BinaryMessage, p[:n]); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (c *wsConn) Close() error {
	c.wmu.Lock()
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(2*time.Second))
	c.wmu.Unlock()
	return c.ws.Close()
}

func (c *wsConn) LocalAddr() net.Addr  { return c.local }
func (c *wsConn) RemoteAddr() net.Addr { return c.remote }

func (c *wsConn) SetDeadline(t time.Time) error {
	c.rdl = t
	return c.SetWriteDeadline(t)
}

func (c *wsConn) SetReadDeadline(t time.Time) error { c.rdl = t; return nil }

func (c *wsConn) SetWriteDeadline(t time.Time) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.SetWriteDeadline(t)
}

var _ net.Conn = (*wsConn)(nil)
