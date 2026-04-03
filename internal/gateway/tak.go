package gateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/transport"
)

// TAKGateway bridges MeshSat messages to/from a TAK server via CoT XML over TCP.
type TAKGateway struct {
	config TAKConfig
	db     *database.DB
	inCh   chan InboundMessage
	outCh  chan *transport.MeshMessage

	conn       net.Conn
	connected  atomic.Bool
	msgsIn     atomic.Int64
	msgsOut    atomic.Int64
	errors     atomic.Int64
	lastActive atomic.Int64
	startTime  time.Time

	// Position coalescing: track last PLI send time per node
	coalesceMu sync.Mutex
	lastPLI    map[uint32]time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewTAKGateway creates a new TAK/CoT gateway.
func NewTAKGateway(cfg TAKConfig, db *database.DB) *TAKGateway {
	return &TAKGateway{
		config:  cfg,
		db:      db,
		inCh:    make(chan InboundMessage, 32),
		outCh:   make(chan *transport.MeshMessage, 10),
		lastPLI: make(map[uint32]time.Time),
	}
}

// Start connects to the TAK server and begins read/write workers.
// If auto_enroll is enabled and no certificate exists, enrolls first.
func (g *TAKGateway) Start(ctx context.Context) error {
	ctx, g.cancel = context.WithCancel(ctx)
	g.startTime = time.Now()

	// Auto-enroll if configured and no certificate is available yet
	if err := g.ensureCertificate(); err != nil {
		return fmt.Errorf("tak: certificate setup: %w", err)
	}

	conn, err := g.dial()
	if err != nil {
		return fmt.Errorf("tak: connect to %s:%d: %w", g.config.Host, g.config.Port, err)
	}
	g.conn = conn
	g.connected.Store(true)

	g.wg.Add(2)
	go g.readWorker(ctx)
	go g.writeWorker(ctx)

	log.Info().
		Str("host", g.config.Host).
		Int("port", g.config.Port).
		Bool("ssl", g.config.SSL).
		Msg("tak gateway started")
	return nil
}

// ensureCertificate checks if TLS certs are available, enrolling if needed.
func (g *TAKGateway) ensureCertificate() error {
	if !g.config.SSL {
		return nil
	}

	// If file paths are set, trust them
	if g.config.CertFile != "" && g.config.KeyFile != "" {
		return nil
	}

	// If credential_id is set, check it exists in DB
	if g.config.CredentialID != "" {
		row, err := g.db.GetCredentialCache(g.config.CredentialID)
		if err == nil && row != nil {
			return nil
		}
		// Credential referenced but missing — fall through to enrollment
		log.Warn().Str("credential_id", g.config.CredentialID).Msg("tak: referenced credential not found")
	}

	// Check if enrolled cert already exists in credential cache
	row, _ := g.db.GetCredentialCache("tak-enrolled-cert")
	if row != nil {
		// Use enrolled cert — set credential_id so dial() picks it up
		g.config.CredentialID = "tak-enrolled-cert"
		log.Info().Str("subject", row.CertSubject).Msg("tak: using enrolled certificate from credential cache")
		return nil
	}

	// No cert available — auto-enroll if configured
	if !g.config.HasEnrollmentConfig() {
		return fmt.Errorf("no certificate configured and auto_enroll not enabled")
	}

	log.Info().Str("url", g.config.EnrollURL).Msg("tak: auto-enrolling with TAK Server")

	result, err := TAKEnroll(TAKEnrollConfig{
		ServerURL: g.config.EnrollURL,
		Username:  g.config.EnrollUsername,
		Password:  g.config.EnrollPassword,
	})
	if err != nil {
		return fmt.Errorf("auto-enroll failed: %w", err)
	}

	if err := StoreEnrolledCert(g.db, result); err != nil {
		return fmt.Errorf("store enrolled cert: %w", err)
	}

	g.config.CredentialID = "tak-enrolled-cert"
	log.Info().
		Str("subject", result.Subject).
		Time("expires", result.NotAfter).
		Msg("tak: auto-enrollment successful")
	return nil
}

// Stop shuts down the TAK gateway.
func (g *TAKGateway) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	if g.conn != nil {
		g.conn.Close()
	}
	g.wg.Wait()
	g.connected.Store(false)
	log.Info().Msg("tak gateway stopped")
	return nil
}

// Forward enqueues a MeshSat message for CoT delivery to the TAK server.
func (g *TAKGateway) Forward(ctx context.Context, msg *transport.MeshMessage) error {
	select {
	case g.outCh <- msg:
		return nil
	default:
		g.errors.Add(1)
		return fmt.Errorf("tak outbound queue full")
	}
}

// Enqueue submits a message for outbound delivery via the gateway.
func (g *TAKGateway) Enqueue(msg *transport.MeshMessage) error {
	return g.Forward(context.Background(), msg)
}

// Receive returns the inbound message channel.
func (g *TAKGateway) Receive() <-chan InboundMessage {
	return g.inCh
}

// Status returns the current gateway status.
func (g *TAKGateway) Status() GatewayStatus {
	s := GatewayStatus{
		Type:        "tak",
		Connected:   g.connected.Load(),
		MessagesIn:  g.msgsIn.Load(),
		MessagesOut: g.msgsOut.Load(),
		Errors:      g.errors.Load(),
	}
	if ts := g.lastActive.Load(); ts > 0 {
		s.LastActivity = time.Unix(ts, 0)
	}
	if s.Connected && !g.startTime.IsZero() {
		s.ConnectionUptime = time.Since(g.startTime).Truncate(time.Second).String()
	}
	return s
}

// Type returns the gateway type identifier.
func (g *TAKGateway) Type() string {
	return "tak"
}

// dial establishes a TCP or TLS connection to the TAK server.
func (g *TAKGateway) dial() (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", g.config.Host, g.config.Port)

	if !g.config.SSL {
		return net.DialTimeout("tcp", addr, 10*time.Second)
	}

	tlsCfg, err := g.buildTLSConfig()
	if err != nil {
		return nil, err
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config:    tlsCfg,
	}
	return dialer.DialContext(context.Background(), "tcp", addr)
}

// buildTLSConfig constructs TLS config from credential_cache or filesystem paths.
func (g *TAKGateway) buildTLSConfig() (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Try credential_cache first (enrolled certs or manually uploaded)
	if g.config.CredentialID != "" {
		if err := g.loadTLSFromCredentialCache(tlsCfg); err != nil {
			log.Warn().Err(err).Str("credential_id", g.config.CredentialID).Msg("tak: credential cache load failed, trying file paths")
		} else {
			return tlsCfg, nil
		}
	}

	// Fallback: filesystem paths
	if g.config.CertFile != "" && g.config.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(g.config.CertFile, g.config.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if g.config.CAFile != "" {
		caPEM, err := os.ReadFile(g.config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("invalid CA certificate")
		}
		tlsCfg.RootCAs = pool
	}

	return tlsCfg, nil
}

// loadTLSFromCredentialCache loads client cert, key, and CA from the credential_cache DB.
func (g *TAKGateway) loadTLSFromCredentialCache(tlsCfg *tls.Config) error {
	// Load client cert
	certRow, err := g.db.GetCredentialCache(g.config.CredentialID)
	if err != nil || certRow == nil {
		return fmt.Errorf("credential %q not found", g.config.CredentialID)
	}

	// Load key (convention: cert ID + replace "cert" with "key", or "-key" suffix)
	keyID := g.config.CredentialID
	if keyID == "tak-enrolled-cert" {
		keyID = "tak-enrolled-key"
	}
	keyRow, err := g.db.GetCredentialCache(keyID)
	if err != nil || keyRow == nil {
		return fmt.Errorf("credential key %q not found", keyID)
	}

	cert, err := tls.X509KeyPair(certRow.EncryptedData, keyRow.EncryptedData)
	if err != nil {
		return fmt.Errorf("parse client certificate from credential cache: %w", err)
	}
	tlsCfg.Certificates = []tls.Certificate{cert}

	// Load truststore/CA if available
	tsID := g.config.CredentialID
	if tsID == "tak-enrolled-cert" {
		tsID = "tak-enrolled-truststore"
	}
	tsRow, _ := g.db.GetCredentialCache(tsID)
	if tsRow != nil && len(tsRow.EncryptedData) > 0 {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(tsRow.EncryptedData) {
			tlsCfg.RootCAs = pool
		}
	}

	return nil
}

// readWorker reads newline-delimited CoT XML from the TAK server.
func (g *TAKGateway) readWorker(ctx context.Context) {
	defer g.wg.Done()

	scanner := bufio.NewScanner(g.conn)
	scanner.Buffer(make([]byte, 64*1024), 256*1024) // up to 256KB per CoT event

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set read deadline so we don't block forever
		if err := g.conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			log.Warn().Err(err).Msg("tak: set read deadline")
			return
		}

		if !scanner.Scan() {
			if ctx.Err() != nil {
				return
			}
			err := scanner.Err()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // read timeout, try again
				}
				log.Warn().Err(err).Msg("tak: read error, reconnecting")
			} else {
				log.Warn().Msg("tak: connection closed by server")
			}
			g.connected.Store(false)
			g.reconnect(ctx)
			if ctx.Err() != nil {
				return
			}
			scanner = bufio.NewScanner(g.conn)
			scanner.Buffer(make([]byte, 64*1024), 256*1024)
			continue
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		ev, err := ParseCotEvent(line)
		if err != nil {
			log.Debug().Err(err).Msg("tak: parse inbound CoT event")
			continue
		}

		// Skip server keepalive/ping events (type "t-x-c-t")
		if ev.Type == "t-x-c-t" || ev.Type == "t-x-c-t-r" {
			continue
		}

		// Publish to CoT event stream for dashboard
		GlobalTakEventBus.Publish(CotEventToRecord(ev, "inbound"))

		msg := CotEventToInboundMessage(ev)
		select {
		case g.inCh <- msg:
			g.msgsIn.Add(1)
			g.lastActive.Store(time.Now().Unix())
		default:
			log.Warn().Msg("tak: inbound channel full")
		}
	}
}

// writeWorker sends CoT XML events to the TAK server.
func (g *TAKGateway) writeWorker(ctx context.Context) {
	defer g.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.outCh:
			g.sendMessage(msg)
		}
	}
}

// sendMessage converts a MeshMessage to CoT XML and writes it to the TCP stream.
func (g *TAKGateway) sendMessage(msg *transport.MeshMessage) {
	uid := fmt.Sprintf("meshsat-%08x", msg.From)
	callsign := fmt.Sprintf("%s-%04x", g.config.CallsignPrefix, msg.From&0xFFFF)

	var ev CotEvent

	cotType := MeshMessageToCotType(msg)
	switch cotType {
	case CotEventTypeChat:
		ev = BuildChatEvent(uid, callsign, msg.DecodedText, g.config.CotStaleSec)
	case CotEventTypeSensor:
		ev = BuildTelemetryEvent(uid, callsign, 0, 0, g.config.CotStaleSec, msg.DecodedText)
	case CotEventTypeWaypoint:
		// Parse waypoint from Meshtastic WAYPOINT_APP protobuf
		if len(msg.RawPayload) > 0 {
			wp, err := transport.ParseWaypointPayload(msg.RawPayload)
			if err == nil && wp.Latitude != 0 {
				ev = BuildWaypointEvent(uid, callsign, wp.Latitude, wp.Longitude, wp.Name, wp.Description, g.config.CotStaleSec)
			} else {
				return // can't build waypoint without coordinates
			}
		} else {
			return
		}
	default:
		// Position — parse from RawPayload if available (PortNum 3 = POSITION_APP)
		if msg.PortNum == 3 && len(msg.RawPayload) > 0 {
			// Coalesce: skip if last PLI for this node was too recent
			if g.config.CoalesceSeconds > 0 {
				g.coalesceMu.Lock()
				last, ok := g.lastPLI[msg.From]
				if ok && time.Since(last) < time.Duration(g.config.CoalesceSeconds)*time.Second {
					g.coalesceMu.Unlock()
					return
				}
				g.lastPLI[msg.From] = time.Now()
				g.coalesceMu.Unlock()
			}

			pos, err := transport.ParsePositionPayload(msg.RawPayload)
			if err == nil && pos.LatitudeI != 0 {
				enrich := PositionEnrichment{
					Lat:        float64(pos.LatitudeI) / 1e7,
					Lon:        float64(pos.LongitudeI) / 1e7,
					Alt:        float64(pos.Altitude),
					Speed:      float64(pos.GroundSpeed),
					Course:     float64(pos.GroundTrack) / 1e5,
					PDOP:       float64(pos.PDOP) / 100.0,
					HDOP:       float64(pos.HDOP) / 100.0,
					SatsInView: int(pos.SatsInView),
					FixQuality: int(pos.FixQuality),
					FixType:    int(pos.FixType),
					Battery:    -1, // unknown unless we have telemetry
				}
				ev = BuildEnrichedPositionEvent(uid, callsign, enrich, g.config.CotStaleSec)
			} else {
				ev = BuildPositionEvent(uid, callsign, 0, 0, 0, g.config.CotStaleSec)
			}
		} else {
			ev = BuildPositionEvent(uid, callsign, 0, 0, 0, g.config.CotStaleSec)
		}

		if msg.DecodedText != "" {
			if ev.Detail == nil {
				ev.Detail = &CotDetail{}
			}
			ev.Detail.Remarks = &CotRemarks{Source: "MeshSat", Text: msg.DecodedText}
		}
	}

	var outBytes []byte
	if g.config.Protocol == "protobuf" {
		takMsg, err := CotEventToProto(ev)
		if err != nil {
			log.Warn().Err(err).Msg("tak: convert to protobuf")
			g.errors.Add(1)
			return
		}
		outBytes, err = MarshalTakProto(takMsg)
		if err != nil {
			log.Warn().Err(err).Msg("tak: marshal protobuf")
			g.errors.Add(1)
			return
		}
	} else {
		var err error
		outBytes, err = MarshalCotEvent(ev)
		if err != nil {
			log.Warn().Err(err).Msg("tak: marshal CoT XML")
			g.errors.Add(1)
			return
		}
		outBytes = append(outBytes, '\n') // XML uses newline delimiter
	}

	if !g.connected.Load() || g.conn == nil {
		g.errors.Add(1)
		return
	}

	if err := g.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Warn().Err(err).Msg("tak: set write deadline")
	}

	if _, err := g.conn.Write(outBytes); err != nil {
		log.Warn().Err(err).Msg("tak: write to server")
		g.errors.Add(1)
		g.connected.Store(false)
		return
	}

	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	log.Debug().Str("uid", ev.UID).Str("type", ev.Type).Msg("tak: sent CoT event")

	// Publish to CoT event stream for dashboard
	GlobalTakEventBus.Publish(CotEventToRecord(&ev, "outbound"))
}

// reconnect attempts to re-establish the TAK server connection with backoff.
func (g *TAKGateway) reconnect(ctx context.Context) {
	wait := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		conn, err := g.dial()
		if err != nil {
			log.Warn().Err(err).Dur("retry_in", wait).Msg("tak: reconnect failed")
			wait *= 2
			if wait > 5*time.Minute {
				wait = 5 * time.Minute
			}
			continue
		}

		if g.conn != nil {
			g.conn.Close()
		}
		g.conn = conn
		g.connected.Store(true)
		log.Info().Msg("tak: reconnected to server")
		return
	}
}
