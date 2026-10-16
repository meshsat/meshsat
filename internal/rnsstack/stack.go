// Package rnsstack assembles the bridge's upstream-compatible Reticulum node
// (internal/rns) on top of the interface registry, the processor and the
// database, so that cmd/meshsat/main.go and the interop harness build the
// same thing. [MESHSAT-1348]
package rnsstack

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/lxmf"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/routing"
)

// Config is what Build needs.
type Config struct {
	Identity *routing.Identity
	Registry *routing.InterfaceRegistry
	DB       *database.DB
	// AnnounceInterval for the bridge destination; 0 disables the announcer.
	AnnounceInterval time.Duration
	AcceptLinks      bool
	IFACNetname      string
	IFACNetkey       string
	PathTTL          time.Duration
	// OnAnnounce receives every accepted remote announce (legacy tables).
	OnAnnounce func(ann *reticulum.Announce, raw []byte, iface string)
	// OnPacket receives decrypted single-packet data for meshsat.bridge.
	OnPacket func(plain []byte, pkt *rns.Packet)

	// LXMF endpoint; Enabled false leaves it out.
	LXMF LXMFConfig
}

// LXMFConfig configures the lxmf.delivery destination.
type LXMFConfig struct {
	Enabled              bool
	DisplayName          string
	StampCost            int
	EnforceStamps        bool
	MaxOutboundStampCost int
	AnnounceInterval     time.Duration
}

// Stack is the built node plus the bridge destination and the LXMF router.
type Stack struct {
	Node   *rns.Node
	Bridge *rns.Destination
	LXMF   *lxmf.Router // nil when disabled
	cfg    Config

	inboundMu sync.RWMutex
	onInbound func(source, text string)

	late *lateProofs // nil until EnableLateProofs
}

// SetInboundHook hands every accepted inbound LXMF message, after it is
// filed, to fn with the sender's lxmf.delivery hash (hex) and the text,
// title first. main.go feeds it to the access rules as lxmf_0, so a rule
// can carry it on over a satellite. [MESHSAT-1554]
func (st *Stack) SetInboundHook(fn func(source, text string)) {
	st.inboundMu.Lock()
	st.onInbound = fn
	st.inboundMu.Unlock()
}

// registryTx adapts the interface registry to rns.Transmitter.
type registryTx struct{ reg *routing.InterfaceRegistry }

func (t *registryTx) Transmit(ifaceID string, raw []byte) error { return t.reg.Send(ifaceID, raw) }

func (t *registryTx) Floodable() []string {
	ifs := t.reg.Floodable()
	out := make([]string, 0, len(ifs))
	for _, i := range ifs {
		out = append(out, i.ID())
	}
	return out
}

func (t *registryTx) HWMTU(ifaceID string) int { return t.reg.GetMTU(ifaceID) }

// Online lets the node skip a paid interface that is down instead of
// booking an announce on it [MESHSAT-1547].
func (t *registryTx) Online(ifaceID string) bool {
	ri := t.reg.Get(ifaceID)
	return ri != nil && ri.IsOnline()
}

// Bitrate maps an interface id to the bit rate the announce cap works with.
// TCP, MQTT and the like are unlimited (0); the radios use the same table
// as the time-sync limiter.
func (t *registryTx) Bitrate(ifaceID string) int {
	if ri := t.reg.Get(ifaceID); ri != nil {
		if bw := ri.Bitrate(); bw > 0 {
			return bw
		}
	}
	typ := ifaceID
	if i := strings.LastIndex(ifaceID, "_"); i > 0 {
		typ = ifaceID[:i]
	}
	switch typ {
	case "iridium", "iridium_imt", "cellular", "sms":
		return 0 // never flooded anyway
	}
	if bw, ok := routing.DefaultBandwidths[typ]; ok {
		return bw
	}
	return 0
}

// Build creates and starts the node. The bridge destination announces
// "meshsat.bridge" with the same identity and dest hash as before, so the
// Hub and the peer kit keep their addresses.
func Build(ctx context.Context, cfg Config) (*Stack, error) {
	node, err := rns.New(rns.Config{
		Identity:           cfg.Identity.ReticulumIdentity(),
		ForwardHeader1From: map[string]bool{"mesh_0": true},
		PathTTL:            cfg.PathTTL,
	}, &registryTx{reg: cfg.Registry})
	if err != nil {
		return nil, err
	}
	st := &Stack{Node: node, cfg: cfg}
	st.Bridge = node.AddDestination(&rns.Destination{
		Name:        cfg.Identity.AppName(),
		AcceptLinks: cfg.AcceptLinks,
		ProveAll:    true,
		OnPacket: func(plain []byte, pkt *rns.Packet) {
			log.Info().Int("bytes", len(plain)).Str("iface", pkt.Iface).Msg("rns: packet for meshsat.bridge")
			if cfg.OnPacket != nil {
				cfg.OnPacket(plain, pkt)
			}
		},
		OnLink: func(l *rns.Link) {
			log.Info().Str("link", hex.EncodeToString(l.ID[:])).Str("iface", l.Iface).Msg("rns: peer link to meshsat.bridge active")
		},
		OnLinkPacket: func(l *rns.Link, plain []byte, pkt *rns.Packet) {
			if cfg.OnPacket != nil {
				cfg.OnPacket(plain, pkt)
			}
		},
	})
	if cfg.IFACNetname != "" || cfg.IFACNetkey != "" {
		f, err := reticulum.NewIFAC(cfg.IFACNetname, cfg.IFACNetkey, reticulum.DefaultIFACSize)
		if err != nil {
			return nil, err
		}
		node.SetIFAC("tcp_0", f)
		log.Info().Msg("rns: interface access codes enabled on tcp_0")
	}
	node.OnAnnounce = func(ann *reticulum.Announce, pkt *rns.Packet) {
		if cfg.OnAnnounce != nil {
			cfg.OnAnnounce(ann, pkt.Raw, pkt.Iface)
		}
	}
	if cfg.DB != nil {
		if raw, err := cfg.DB.GetSystemConfig(rns.PaidPoliciesConfigKey); err == nil && raw != "" {
			if n, err := node.LoadPaidPolicies(raw); err != nil {
				log.Warn().Err(err).Msg("rns: paid link policies unreadable, every paid link stays closed")
			} else if n > 0 {
				log.Info().Int("interfaces", n).Msg("rns: paid link policies loaded")
			}
		}
		st.loadPaths()
		node.OnPathChanged = func(dest [rns.HashLen]byte, e *rns.PathEntry) { go st.persistPath(e) }
	}
	// The late-proof listener goes in before the node is reachable: Build
	// returns the node to main.go, which only then hands it to the
	// packet ingress (Processor.SetRNSNode), so no proof can arrive before
	// it, be dropped and then filtered as a duplicate (red-team A13-R2).
	// [MESHSAT-1584]
	if cfg.DB != nil && cfg.LXMF.Enabled {
		if err := st.EnableLateProofs(cfg.DB, nil); err != nil {
			log.Error().Err(err).Msg("lxmf: stored proof hashes unreadable; every unmatched proof is checked against the database")
		}
	}
	if cfg.LXMF.Enabled {
		st.LXMF = lxmf.New(node, lxmf.Config{
			Identity:             cfg.Identity.ReticulumIdentity(),
			DisplayName:          cfg.LXMF.DisplayName,
			StampCost:            cfg.LXMF.StampCost,
			EnforceStamps:        cfg.LXMF.EnforceStamps,
			MaxOutboundStampCost: cfg.LXMF.MaxOutboundStampCost,
			OnMessage:            st.storeInbound,
		})
		log.Info().Str("lxmf_dest", st.LXMF.HashHex()).Str("display_name", cfg.LXMF.DisplayName).Int("stamp_cost", cfg.LXMF.StampCost).Msg("lxmf: delivery destination registered")
	}
	node.Start(ctx)
	if cfg.AnnounceInterval > 0 {
		go st.announcer(ctx)
	}
	tid := node.IdentityHash()
	log.Info().Str("transport_id", hex.EncodeToString(tid[:])).
		Str("dest_hash", cfg.Identity.DestHashHex()).Bool("accept_links", cfg.AcceptLinks).
		Msg("rns: upstream-compatible Reticulum node started")
	return st, nil
}

func (st *Stack) announcer(ctx context.Context) {
	lxmfEvery := st.cfg.LXMF.AnnounceInterval
	if lxmfEvery <= 0 {
		lxmfEvery = 30 * time.Minute
	}
	lastLXMF := time.Time{}
	announce := func() {
		if err := st.Node.Announce(st.Bridge, "", false); err != nil {
			log.Warn().Err(err).Msg("rns: announce failed")
		}
		if st.LXMF != nil && time.Since(lastLXMF) >= lxmfEvery {
			if err := st.LXMF.Announce(); err != nil {
				log.Warn().Err(err).Msg("lxmf: announce failed")
			}
			lastLXMF = time.Now()
		}
	}
	// Give the interfaces a moment to come up, then announce and repeat.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	announce()
	t := time.NewTicker(st.cfg.AnnounceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			announce()
		}
	}
}

// storeInbound files an inbound LXMF message in the messages table so the
// existing inbox shows it. From/to are "lxmf:<hash>", transport "reticulum".
func (st *Stack) storeInbound(m *lxmf.Message) {
	text := string(m.Content)
	if len(m.Title) > 0 {
		text = string(m.Title) + "\n" + text
	}
	st.fileInbound(m, text)
	st.inboundMu.RLock()
	fn := st.onInbound
	st.inboundMu.RUnlock()
	if fn != nil && text != "" {
		fn(hex.EncodeToString(m.Source[:]), text)
	}
}

func (st *Stack) fileInbound(m *lxmf.Message, text string) {
	if st.cfg.DB == nil {
		return
	}
	row := &database.Message{
		PacketID:    uint32(m.Hash[0])<<24 | uint32(m.Hash[1])<<16 | uint32(m.Hash[2])<<8 | uint32(m.Hash[3]),
		FromNode:    "lxmf:" + hex.EncodeToString(m.Source[:]),
		ToNode:      "lxmf:" + hex.EncodeToString(m.Dest[:]),
		PortNum:     1,
		PortNumName: "LXMF",
		DecodedText: text,
		RxTime:      int64(m.Timestamp),
		Direction:   "rx",
		Transport:   "reticulum",
	}
	if _, err := st.cfg.DB.InsertMessageWithStatus(row, "received"); err != nil {
		log.Warn().Err(err).Msg("lxmf: store inbound message failed")
	}
}

// Deliver implements engine.LXMFSender for the delivery ledger: the row's
// destination is the lxmf.delivery hash, the payload the JSON body from
// the API (content, title), the preview the content text.
func (st *Stack) Deliver(ctx context.Context, del database.MessageDelivery) (string, error) {
	if st.LXMF == nil {
		return "", errors.New("lxmf router not running")
	}
	destBytes, err := hex.DecodeString(del.Destination)
	if err != nil || len(destBytes) != lxmf.DestLen {
		return "", fmt.Errorf("lxmf: destination must be 32 hex characters, got %q", del.Destination)
	}
	var dest [lxmf.DestLen]byte
	copy(dest[:], destBytes)
	body := LXMFBody{Content: del.TextPreview}
	if len(del.Payload) > 0 {
		_ = json.Unmarshal(del.Payload, &body)
	}
	// A delivery intent's attempt names the message's timestamp (ts), so
	// its retries are one message with one hash [MESHSAT-1581], and sends
	// on free interfaces only, at every step [MESHSAT-1584]. Free-only
	// follows from the row being an intent's attempt (its durable
	// intent_id), not from the body's free_only alone: a row written before
	// that field existed would otherwise go out on a paid link (red-team
	// U-1). Paid intent routes come with B9 and a typed authorization.
	freeOnly := body.FreeOnly || del.IntentID != ""
	opts := lxmf.SendOptions{Timestamp: body.Timestamp, FreeOnly: freeOnly}
	if lp := st.late; lp != nil && del.IntentID != "" {
		lp.watch(&opts, del.ID, del.ClaimGen)
	}
	m, _, err := st.LXMF.SendWith(ctx, dest, []byte(body.Content), []byte(body.Title), lxmf.MapOf(), 0, opts)
	if err != nil {
		if m != nil {
			return hex.EncodeToString(m.Hash[:]), err
		}
		return "", err
	}
	if st.cfg.DB != nil {
		text := body.Content
		if body.Title != "" {
			text = body.Title + "\n" + text
		}
		row := &database.Message{
			PacketID:    uint32(m.Hash[0])<<24 | uint32(m.Hash[1])<<16 | uint32(m.Hash[2])<<8 | uint32(m.Hash[3]),
			FromNode:    "lxmf:" + st.LXMF.HashHex(),
			ToNode:      "lxmf:" + del.Destination,
			PortNum:     1,
			PortNumName: "LXMF",
			DecodedText: text,
			RxTime:      time.Now().Unix(),
			Direction:   "tx",
			Transport:   "reticulum",
		}
		if _, err := st.cfg.DB.InsertMessageWithStatus(row, "delivered"); err != nil {
			log.Warn().Err(err).Msg("lxmf: store outbound message failed")
		}
	}
	return hex.EncodeToString(m.Hash[:]), nil
}

// LXMFBody is the JSON payload of a class-lxmf ledger row. Timestamp, in
// seconds since the epoch, is set on a delivery intent's attempts; empty
// means now.
type LXMFBody struct {
	Content   string  `json:"content"`
	Title     string  `json:"title,omitempty"`
	Timestamp float64 `json:"ts,omitempty"`
	FreeOnly  bool    `json:"free_only,omitempty"`
}

// CrossesPaidLink reports whether the known path to an LXMF destination
// (32 hex characters) leaves this node on a paid link, the satellite. A
// delivery intent never sends over one without a price (architecture r3
// D8). [MESHSAT-1581]
func (st *Stack) CrossesPaidLink(destHex string) bool {
	if st.LXMF == nil {
		return false
	}
	b, err := hex.DecodeString(destHex)
	if err != nil || len(b) != lxmf.DestLen {
		return false
	}
	var dest [lxmf.DestLen]byte
	copy(dest[:], b)
	return st.LXMF.SlowPath(dest)
}

func (st *Stack) persistPath(e *rns.PathEntry) {
	if e.Announce == nil {
		return
	}
	var blobs []byte
	for _, b := range e.RandomBlobs {
		blobs = append(blobs, b...)
	}
	err := st.cfg.DB.UpsertRNSPath(database.RNSPath{
		DestHash:    hex.EncodeToString(e.DestHash[:]),
		NextHop:     hex.EncodeToString(e.NextHop[:]),
		Hops:        e.Hops,
		Iface:       e.Iface,
		ExpiresAt:   e.Expires,
		RandomBlobs: blobs,
		AnnounceRaw: e.Announce.MarshalPacket(),
	})
	if err != nil {
		log.Warn().Err(err).Msg("rns: persist path failed")
	}
}

func (st *Stack) loadPaths() {
	rows, err := st.cfg.DB.GetRNSPaths()
	if err != nil {
		log.Warn().Err(err).Msg("rns: load paths failed")
		return
	}
	n := 0
	for _, r := range rows {
		ann, err := reticulum.UnmarshalAnnouncePacket(r.AnnounceRaw)
		if err != nil || ann.Verify() != nil {
			continue
		}
		dest, err1 := hex.DecodeString(r.DestHash)
		nh, err2 := hex.DecodeString(r.NextHop)
		if err1 != nil || err2 != nil || len(dest) != rns.HashLen || len(nh) != rns.HashLen {
			continue
		}
		e := &rns.PathEntry{Hops: r.Hops, Expires: r.ExpiresAt, Iface: r.Iface, Announce: ann, Timestamp: time.Now()}
		copy(e.DestHash[:], dest)
		copy(e.NextHop[:], nh)
		for i := 0; i+10 <= len(r.RandomBlobs); i += 10 {
			e.RandomBlobs = append(e.RandomBlobs, append([]byte(nil), r.RandomBlobs[i:i+10]...))
		}
		st.Node.Paths().Put(e)
		_ = st.Node.Remember(e.DestHash, ann.PublicKey, ann.AppData)
		n++
	}
	if n > 0 {
		log.Info().Int("paths", n).Msg("rns: paths loaded from database")
	}
}

// DeliverTimeout gives a delivery whose path crosses a paid link (the
// satellite) ten minutes instead of the ledger's three [MESHSAT-1548].
func (st *Stack) DeliverTimeout(dest string) time.Duration {
	if st.LXMF == nil {
		return 0
	}
	b, err := hex.DecodeString(dest)
	if err != nil || len(b) != lxmf.DestLen {
		return 0
	}
	var d [lxmf.DestLen]byte
	copy(d[:], b)
	if st.LXMF.SlowPath(d) {
		return 10 * time.Minute
	}
	return 0
}

// lateProofs makes a destination's proof count when it comes after an
// intent's LXMF send stopped waiting for it, or after a restart (A-13):
// the packet hash and the signing key are stored with the transmission as
// the packet goes out, and a proof no waiting send took is checked against
// them and recorded as evidence. [MESHSAT-1584]
type lateProofs struct {
	db     *database.DB
	notify func(intentID string)
	mu     sync.Mutex
	expect map[[rns.HashLen]byte]struct{} // first 16 bytes of the hashes waited for
	// unfiltered: the stored hashes could not be loaded, so every unmatched
	// proof is checked against the database instead of the set.
	unfiltered bool
}

// Test seams: loading the stored hashes, and storing one before a send.
var (
	loadExpectedProofs = func(db *database.DB) ([]string, error) { return db.ExpectedProofHashes() }
	storeProof         = func(db *database.DB, attemptID int64, gen int, hash, key string) error {
		return db.SetTransmissionProof(attemptID, gen, hash, key)
	}
)

// EnableLateProofs turns that on: the hashes stored before a restart are
// loaded, and the node hands every unmatched proof here. notify (may be
// nil) hears which intent got evidence; its heartbeat finds it anyway.
// When loading fails the listener is installed all the same, checking
// every unmatched proof against the database, and the error is returned.
func (st *Stack) EnableLateProofs(db *database.DB, notify func(intentID string)) error {
	hashes, err := loadExpectedProofs(db)
	lp := &lateProofs{db: db, notify: notify, expect: map[[rns.HashLen]byte]struct{}{}, unfiltered: err != nil}
	for _, h := range hashes {
		if b, err := hex.DecodeString(h); err == nil && len(b) >= rns.HashLen {
			var k [rns.HashLen]byte
			copy(k[:], b)
			lp.expect[k] = struct{}{}
		}
	}
	st.late = lp
	prev := st.Node.OnUnmatchedProof
	st.Node.OnUnmatchedProof = func(pkt *rns.Packet) {
		lp.take(pkt)
		if prev != nil {
			prev(pkt)
		}
	}
	return err
}

// SetLateProofNotify names who hears that an intent got late evidence (the
// Escalator, created after the stack).
func (st *Stack) SetLateProofNotify(notify func(intentID string)) {
	if lp := st.late; lp != nil {
		lp.mu.Lock()
		lp.notify = notify
		lp.mu.Unlock()
	}
}

// watch sets a send's hooks for one intent attempt (its row and claim
// generation).
func (lp *lateProofs) watch(opts *lxmf.SendOptions, attemptID int64, gen int) {
	// Stored before the packet is written; a failure to store stops the
	// write, so no transmission goes out without what its proof needs
	// (red-team A13-R1).
	opts.Prepare = func(h [32]byte, key ed25519.PublicKey) error {
		if err := storeProof(lp.db, attemptID, gen, hex.EncodeToString(h[:]), hex.EncodeToString(key)); err != nil {
			log.Warn().Err(err).Int64("attempt", attemptID).Msg("lxmf: proof hash not stored; the packet is not sent")
			return err
		}
		var k [rns.HashLen]byte
		copy(k[:], h[:])
		lp.mu.Lock()
		lp.expect[k] = struct{}{}
		lp.mu.Unlock()
		return nil
	}
	opts.OnLateDelivery = func() {
		e, err := lp.db.RecordLateDelivery(attemptID, gen)
		if err != nil {
			log.Warn().Err(err).Int64("attempt", attemptID).Msg("lxmf: late resource proof not recorded")
			return
		}
		lp.recorded(e)
	}
}

// take checks a proof no waiting send took against the stored hashes.
func (lp *lateProofs) take(pkt *rns.Packet) {
	data := pkt.Hdr.Data
	var key [rns.HashLen]byte
	var lookup string
	prefix := false
	switch len(data) {
	case reticulum.ExplicitProofLen:
		copy(key[:], data[:rns.HashLen])
		lookup = hex.EncodeToString(data[:rns.FullHashLen])
	case reticulum.ImplicitProofLen:
		// Addressed to the truncated packet hash.
		key = pkt.Hdr.DestHash
		lookup, prefix = hex.EncodeToString(key[:]), true
	default:
		return
	}
	lp.mu.Lock()
	_, waited := lp.expect[key]
	unfiltered := lp.unfiltered
	lp.mu.Unlock()
	if !waited && !unfiltered {
		return // someone else's proof passing through, or one already taken
	}
	valid := func(proofHash, proofKey string) bool {
		hb, err1 := hex.DecodeString(proofHash)
		kb, err2 := hex.DecodeString(proofKey)
		if err1 != nil || err2 != nil || len(hb) != rns.FullHashLen || len(kb) != ed25519.PublicKeySize {
			return false
		}
		var h [rns.FullHashLen]byte
		copy(h[:], hb)
		return reticulum.ValidateProof(data, h, ed25519.PublicKey(kb)) == nil
	}
	e, found, err := lp.db.RecordProofEvidence(lookup, prefix, valid)
	if err != nil {
		log.Warn().Err(err).Msg("lxmf: late proof not recorded")
		return
	}
	if !found {
		return
	}
	lp.mu.Lock()
	delete(lp.expect, key)
	lp.mu.Unlock()
	lp.recorded(e)
}

func (lp *lateProofs) recorded(e database.DeliveryEvidence) {
	if e.ID == 0 {
		return // this transmission had its proof already
	}
	log.Info().Str("intent", e.IntentID).Int64("transmission", e.TransmissionID).
		Msg("lxmf: a proof came after the send stopped waiting; recorded as evidence")
	lp.mu.Lock()
	notify := lp.notify
	lp.mu.Unlock()
	if notify != nil {
		notify(e.IntentID)
	}
}
