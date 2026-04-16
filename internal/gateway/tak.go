package gateway

import (
	"bufio"
	"bytes"
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
	pb "meshsat/internal/gateway/takproto"
	"meshsat/internal/transport"
)

// TAKGateway bridges MeshSat messages to/from a TAK server via CoT XML over TCP.
type TAKGateway struct {
	config TAKConfig
	db     *database.DB
	inCh   chan InboundMessage
	outCh  chan *transport.MeshMessage

	conn            net.Conn
	connected       atomic.Bool
	negotiatedProto atomic.Int32 // 0=XML, 1=protobuf (set by version negotiation)
	msgsIn          atomic.Int64
	msgsOut         atomic.Int64
	errors          atomic.Int64
	lastActive      atomic.Int64
	startTime       time.Time

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

// SendCotEvent serializes and writes a fully-built CoT event directly to
// the TAK server connection. Bypasses the MeshMessage→CoT translation in
// sendMessage(), so callers like the zigbee bridge can publish synthesized
// sensor markers with their own callsign + position. [MESHSAT-509]
//
// Returns nil if the gateway isn't connected — losing a sensor reading
// during a TAK reconnect is a soft failure, not worth surfacing as an
// error to the caller (zigbee gateway has no good fallback either).
func (g *TAKGateway) SendCotEvent(ev CotEvent) error {
	if !g.connected.Load() || g.conn == nil {
		return nil
	}
	var outBytes []byte
	if g.useProtobuf() {
		takMsg, err := CotEventToProto(ev)
		if err != nil {
			g.errors.Add(1)
			return fmt.Errorf("tak: convert CoT to protobuf: %w", err)
		}
		outBytes, err = MarshalTakProto(takMsg)
		if err != nil {
			g.errors.Add(1)
			return fmt.Errorf("tak: marshal protobuf: %w", err)
		}
	} else {
		b, err := MarshalCotEvent(ev)
		if err != nil {
			g.errors.Add(1)
			return fmt.Errorf("tak: marshal CoT XML: %w", err)
		}
		outBytes = append(b, '\n')
	}
	if err := g.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Warn().Err(err).Msg("tak: set write deadline")
	}
	if _, err := g.conn.Write(outBytes); err != nil {
		g.errors.Add(1)
		g.connected.Store(false)
		return fmt.Errorf("tak: write to server: %w", err)
	}
	g.msgsOut.Add(1)
	g.lastActive.Store(time.Now().Unix())
	GlobalTakEventBus.Publish(CotEventToRecord(&ev, "outbound"))
	return nil
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

// sendVersionNegotiation sends a TAK Protocol version negotiation message on connect.
// Always starts with XML (the only guaranteed common format), then upgrades to protobuf
// if the server responds with maxProtoVersion >= 1.
func (g *TAKGateway) sendVersionNegotiation() {
	g.negotiatedProto.Store(0) // reset to XML until server confirms

	now := time.Now().UTC()
	verXML := fmt.Sprintf(
		`<event version="2.0" uid="meshsat-takp" type="t-x-takp-v" how="m-g" time="%s" start="%s" stale="%s">`+
			`<point lat="0" lon="0" hae="0" ce="999999" le="999999"/>`+
			`<detail><TakControl minProtoVersion="0" maxProtoVersion="1"/></detail></event>`,
		now.Format(cotTimeFormat), now.Format(cotTimeFormat),
		now.Add(30*time.Second).Format(cotTimeFormat))

	if err := g.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err == nil {
		g.conn.Write(append([]byte(verXML), '\n')) //nolint:errcheck
	}
	log.Info().Msg("tak: sent XML version negotiation (minProto=0, maxProto=1)")
}

// sendProtobufVersionNegotiation sends the version negotiation as a protobuf TakMessage.
// Called after the server indicates protobuf support, to confirm the upgrade.
func (g *TAKGateway) sendProtobufVersionNegotiation() {
	msg := &pb.TakMessage{
		TakControl: &pb.TakControl{
			MinProtoVersion: 0,
			MaxProtoVersion: 1,
			ContactUid:      "meshsat-takp",
		},
	}
	frame, err := MarshalTakProto(msg)
	if err != nil {
		log.Warn().Err(err).Msg("tak: marshal protobuf version negotiation")
		return
	}
	if err := g.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err == nil {
		g.conn.Write(frame) //nolint:errcheck
	}
	log.Info().Msg("tak: sent protobuf version negotiation confirmation")
}

// handleVersionNegotiation processes a version negotiation response and upgrades
// the connection to protobuf if the server supports it.
func (g *TAKGateway) handleVersionNegotiation(maxProto int) {
	if maxProto >= 1 {
		g.negotiatedProto.Store(1)
		g.sendProtobufVersionNegotiation()
		log.Info().Int("version", maxProto).Msg("tak: negotiated protobuf mode")
	} else {
		g.negotiatedProto.Store(0)
		log.Info().Msg("tak: server supports XML only")
	}
}

// useProtobuf returns true if outbound messages should use protobuf framing.
func (g *TAKGateway) useProtobuf() bool {
	// Explicit config override takes precedence
	if g.config.Protocol == "protobuf" {
		return true
	}
	if g.config.Protocol == "xml" {
		return false
	}
	// Default: use negotiated protocol
	return g.negotiatedProto.Load() >= 1
}

// readWorker reads CoT events from the TAK server in mixed mode (XML + protobuf).
// Detects protocol by first byte: 0xBF = TAK Protocol v1 (protobuf), otherwise XML.
func (g *TAKGateway) readWorker(ctx context.Context) {
	defer g.wg.Done()

	// Send version negotiation on initial connect
	g.sendVersionNegotiation()

	reader := bufio.NewReaderSize(g.conn, 256*1024)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := g.conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			log.Warn().Err(err).Msg("tak: set read deadline")
			return
		}

		// Peek first byte to detect protocol
		firstByte, err := reader.Peek(1)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Warn().Err(err).Msg("tak: read error, reconnecting")
			g.connected.Store(false)
			g.reconnect(ctx)
			if ctx.Err() != nil {
				return
			}
			reader = bufio.NewReaderSize(g.conn, 256*1024)
			g.sendVersionNegotiation()
			continue
		}

		var ev *CotEvent

		if firstByte[0] == 0xBF {
			// TAK Protocol v1 (protobuf framed)
			msg, err := ReadTakProtoMessage(reader)
			if err != nil {
				log.Debug().Err(err).Msg("tak: parse protobuf message")
				continue
			}

			// Handle protobuf TakControl (version negotiation) separately
			if tc := msg.GetTakControl(); tc != nil && msg.GetCotEvent() == nil {
				log.Info().
					Uint32("min", tc.GetMinProtoVersion()).
					Uint32("max", tc.GetMaxProtoVersion()).
					Str("contact", tc.GetContactUid()).
					Msg("tak: protobuf version negotiation received")
				g.handleVersionNegotiation(int(tc.GetMaxProtoVersion()))
				continue
			}

			ev, err = ProtoToCotEvent(msg)
			if err != nil {
				log.Debug().Err(err).Msg("tak: convert protobuf to CoT")
				continue
			}
		} else {
			// XML CoT (newline-delimited)
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				log.Warn().Err(err).Msg("tak: XML read error, reconnecting")
				g.connected.Store(false)
				g.reconnect(ctx)
				if ctx.Err() != nil {
					return
				}
				reader = bufio.NewReaderSize(g.conn, 256*1024)
				g.sendVersionNegotiation()
				continue
			}
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			ev, err = ParseCotEvent(line)
			if err != nil {
				log.Debug().Err(err).Msg("tak: parse XML CoT event")
				continue
			}
		}

		// Handle XML version negotiation responses — extract negotiated protocol
		if ev.Type == "t-x-takp-v" || ev.Type == "t-x-takp-r" {
			maxProto := 0
			if ev.Detail != nil && ev.Detail.TakControl != nil {
				maxProto = ev.Detail.TakControl.MaxProtoVersion
			}
			log.Info().Str("type", ev.Type).Int("maxProto", maxProto).Msg("tak: XML version negotiation response")
			g.handleVersionNegotiation(maxProto)
			continue
		}

		// Skip keepalives
		if ev.Type == "t-x-c-t" || ev.Type == "t-x-c-t-r" {
			continue
		}

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
	if g.useProtobuf() {
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
