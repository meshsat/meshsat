package rns

import (
	"bytes"
	"compress/bzip2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/msgpack"
	"meshsat/internal/reticulum"
)

// RNS Resource transfer over a link (RNS/Resource.py): the sender encrypts
// the whole payload once with the link key, splits the ciphertext into
// parts of SDU bytes, advertises hash, size and a map of 4-byte part hashes,
// and serves parts on request; the receiver requests windows of parts,
// reassembles, decrypts, verifies the hash and returns a proof. Single
// segment only (payloads up to 1 MiB), never compressed on send, bzip2
// accepted on receive.

const (
	resourceMapHashLen     = 4
	resourceRandomHashLen  = 4
	resourceWindow         = 4
	resourceWindowMin      = 2
	resourceWindowMaxSlow  = 10
	resourceWindowMaxFast  = 75
	resourceWindowFlex     = 4
	resourceAdvOverhead    = 134
	resourceMaxAdvRetries  = 4
	resourceMaxRetries     = 16
	resourceMaxSize        = 1*1024*1024 - 1
	resourceHashmapNotExh  = 0x00
	resourceHashmapExh     = 0xFF
	resourceProcessingGrac = time.Second
	resourceRetryGrace     = 250 * time.Millisecond
	resourcePerRetryDelay  = 500 * time.Millisecond
	resourceSenderGrace    = 10 * time.Second
	resourceCollisionGuard = 2*resourceWindowMaxFast + resourceHashmapMaxLen
	resourceHashmapMaxLen  = (431 - resourceAdvOverhead) / resourceMapHashLen // 74 at MTU 500
)

var (
	// ErrResourceTooLarge is returned for payloads above one segment.
	ErrResourceTooLarge = errors.New("rns: resource larger than one segment (1 MiB)")
	// ErrResourceFailed is returned when a transfer did not conclude.
	ErrResourceFailed = errors.New("rns: resource transfer failed")
)

// ResourceResult is the outcome of an outgoing resource.
type ResourceResult struct {
	Delivered bool
	Err       error
}

// OutgoingResource is a resource we are sending on a link.
type OutgoingResource struct {
	Hash          [FullHashLen]byte
	link          *Link
	mu            sync.Mutex
	data          []byte // encrypted stream
	parts         [][]byte
	partSent      []bool
	mapHashes     [][resourceMapHashLen]byte
	randomHash    [resourceRandomHashLen]byte
	expectedProof [FullHashLen]byte
	dataSize      int
	sentParts     int
	advRetries    int
	advSent       time.Time
	lastActivity  time.Time
	rtt           time.Duration
	state         int // 0 advertised, 1 transferring, 2 awaiting proof, 3 complete, 4 failed
	recvMinHeight int
	done          chan ResourceResult
}

// Done resolves once the receiver proved the resource or the transfer failed.
func (r *OutgoingResource) Done() <-chan ResourceResult { return r.done }

// IncomingResource is a resource being received on a link.
type IncomingResource struct {
	Hash         [FullHashLen]byte
	link         *Link
	mu           sync.Mutex
	size         int // transfer (ciphertext) size
	dataSize     int
	totalParts   int
	parts        [][]byte
	hashmap      [][resourceMapHashLen]byte
	hashmapSet   []bool
	hashHeight   int
	randomHash   [resourceRandomHashLen]byte
	compressed   bool
	received     int
	outstanding  int
	window       int
	windowMax    int
	windowMin    int
	consecutive  int
	waitingHMU   bool
	retries      int
	lastActivity time.Time
	reqSent      time.Time
	state        int    // 0 transferring, 1 assembling, 2 complete, 3 failed
	Data         []byte // set when complete
}

// linkResources is the per-link resource state.
type linkResources struct {
	mu       sync.Mutex
	incoming map[[FullHashLen]byte]*IncomingResource
	outgoing map[[FullHashLen]byte]*OutgoingResource
}

func (l *Link) res() *linkResources {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.resources == nil {
		l.resources = &linkResources{incoming: map[[FullHashLen]byte]*IncomingResource{}, outgoing: map[[FullHashLen]byte]*OutgoingResource{}}
	}
	return l.resources
}

func mapHash(part []byte, random [resourceRandomHashLen]byte) [resourceMapHashLen]byte {
	h := sha256.Sum256(append(append([]byte(nil), part...), random[:]...))
	var out [resourceMapHashLen]byte
	copy(out[:], h[:resourceMapHashLen])
	return out
}

// SendResource advertises payload on the link and serves it. The returned
// resource resolves when the receiver proves it.
func (m *LinkManager) SendResource(l *Link, payload []byte) (*OutgoingResource, error) {
	if l.State() != LinkActive && l.State() != LinkStale {
		return nil, errors.New("rns: link not active")
	}
	if len(payload) > resourceMaxSize {
		return nil, ErrResourceTooLarge
	}
	var prefix [resourceRandomHashLen]byte
	rand.Read(prefix[:])
	plain := append(append([]byte(nil), prefix[:]...), payload...)
	enc, err := l.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	sdu := l.mtu - reticulum.HeaderMaxSize - reticulum.IFACMinSize
	r := &OutgoingResource{link: l, data: enc, dataSize: len(payload), done: make(chan ResourceResult, 1), rtt: l.RTT()}
	for {
		rand.Read(r.randomHash[:])
		r.Hash = sha256.Sum256(append(append([]byte(nil), payload...), r.randomHash[:]...))
		r.expectedProof = sha256.Sum256(append(append([]byte(nil), payload...), r.Hash[:]...))
		r.parts = r.parts[:0]
		r.mapHashes = r.mapHashes[:0]
		collision := false
		guard := map[[resourceMapHashLen]byte]bool{}
		for i := 0; i < len(enc); i += sdu {
			end := i + sdu
			if end > len(enc) {
				end = len(enc)
			}
			part := enc[i:end]
			mh := mapHash(part, r.randomHash)
			if guard[mh] {
				collision = true
				break
			}
			guard[mh] = true
			r.parts = append(r.parts, part)
			r.mapHashes = append(r.mapHashes, mh)
		}
		if !collision {
			break
		}
	}
	r.partSent = make([]bool, len(r.parts))
	rs := l.res()
	rs.mu.Lock()
	rs.outgoing[r.Hash] = r
	rs.mu.Unlock()
	m.advertise(r)
	return r, nil
}

func (r *OutgoingResource) advertisement(segment int) msgpack.Value {
	start := segment * resourceHashmapMaxLen
	end := (segment + 1) * resourceHashmapMaxLen
	if end > len(r.parts) {
		end = len(r.parts)
	}
	var hm []byte
	for i := start; i < end; i++ {
		hm = append(hm, r.mapHashes[i][:]...)
	}
	kv := func(k string, v msgpack.Value) msgpack.KV { return msgpack.KV{Key: msgpack.Str(k), Val: v} }
	return msgpack.MapOf(
		kv("t", msgpack.Int(int64(len(r.data)))),
		kv("d", msgpack.Int(int64(r.dataSize))),
		kv("n", msgpack.Int(int64(len(r.parts)))),
		kv("h", msgpack.Bin(r.Hash[:])),
		kv("r", msgpack.Bin(r.randomHash[:])),
		kv("o", msgpack.Bin(r.Hash[:])),
		kv("i", msgpack.Int(1)),
		kv("l", msgpack.Int(1)),
		kv("q", msgpack.Nil()),
		kv("f", msgpack.Int(0x01)), // encrypted, not compressed, not split, no metadata
		kv("m", msgpack.Bin(hm)),
	)
}

func (m *LinkManager) advertise(r *OutgoingResource) {
	adv := msgpack.Pack(r.advertisement(0))
	ct, err := r.link.Encrypt(adv)
	if err != nil {
		r.fail(err)
		return
	}
	r.link.mu.Lock()
	m.sendLinkPacket(r.link, reticulum.PacketData, reticulum.ContextResourceAdv, ct)
	r.link.mu.Unlock()
	r.mu.Lock()
	r.advSent = m.n.now()
	r.lastActivity = r.advSent
	r.mu.Unlock()
}

func (r *OutgoingResource) fail(err error) {
	r.mu.Lock()
	if r.state >= 3 {
		r.mu.Unlock()
		return
	}
	r.state = 4
	r.mu.Unlock()
	rs := r.link.res()
	rs.mu.Lock()
	delete(rs.outgoing, r.Hash)
	rs.mu.Unlock()
	select {
	case r.done <- ResourceResult{Err: err}:
	default:
	}
}

// handleRequest serves a RESOURCE_REQ (decrypted plaintext).
func (m *LinkManager) handleResourceRequest(l *Link, plain []byte) {
	if len(plain) < 1+FullHashLen {
		return
	}
	exhausted := plain[0] == resourceHashmapExh
	pad := 1
	var lastMap [resourceMapHashLen]byte
	if exhausted {
		if len(plain) < 1+resourceMapHashLen+FullHashLen {
			return
		}
		copy(lastMap[:], plain[1:1+resourceMapHashLen])
		pad = 1 + resourceMapHashLen
	}
	var hash [FullHashLen]byte
	copy(hash[:], plain[pad:pad+FullHashLen])
	rs := l.res()
	rs.mu.Lock()
	r := rs.outgoing[hash]
	rs.mu.Unlock()
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.rtt == 0 {
		r.rtt = m.n.now().Sub(r.advSent)
	}
	r.state = 1
	r.lastActivity = m.n.now()
	wanted := plain[pad+FullHashLen:]
	want := map[[resourceMapHashLen]byte]bool{}
	for i := 0; i+resourceMapHashLen <= len(wanted); i += resourceMapHashLen {
		var mh [resourceMapHashLen]byte
		copy(mh[:], wanted[i:i+resourceMapHashLen])
		want[mh] = true
	}
	start := r.recvMinHeight
	end := start + resourceCollisionGuard
	if end > len(r.parts) {
		end = len(r.parts)
	}
	var toSend [][]byte
	for i := start; i < end; i++ {
		if want[r.mapHashes[i]] {
			toSend = append(toSend, r.parts[i])
			if !r.partSent[i] {
				r.partSent[i] = true
				r.sentParts++
			}
		}
	}
	var hmu []byte
	if exhausted {
		idx := r.recvMinHeight
		for i := r.recvMinHeight; i < end; i++ {
			idx++
			if r.mapHashes[i] == lastMap {
				break
			}
		}
		r.recvMinHeight = max(idx-1-resourceWindowMaxFast, 0)
		if idx%resourceHashmapMaxLen != 0 {
			r.mu.Unlock()
			log.Debug().Msg("rns: resource sequencing error, cancelling")
			r.fail(errors.New("resource sequencing error"))
			return
		}
		segment := idx / resourceHashmapMaxLen
		hs := segment * resourceHashmapMaxLen
		he := (segment + 1) * resourceHashmapMaxLen
		if he > len(r.parts) {
			he = len(r.parts)
		}
		var hm []byte
		for i := hs; i < he; i++ {
			hm = append(hm, r.mapHashes[i][:]...)
		}
		if len(hm) == 0 {
			r.mu.Unlock()
			r.fail(errors.New("resource HMU error"))
			return
		}
		hmu = append(append([]byte(nil), r.Hash[:]...), msgpack.Pack(msgpack.Array(msgpack.Int(int64(segment)), msgpack.Bin(hm)))...)
	}
	if r.sentParts == len(r.parts) {
		r.state = 2
	}
	r.mu.Unlock()
	l.mu.Lock()
	for _, p := range toSend {
		m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextResource, p)
	}
	if hmu != nil {
		if ct, err := l.Encrypt(hmu); err == nil {
			m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextResourceHMU, ct)
		}
	}
	l.mu.Unlock()
}

// handleResourceProof validates a RESOURCE_PRF for an outgoing resource.
func (m *LinkManager) handleResourceProof(l *Link, data []byte) {
	if len(data) != 2*FullHashLen {
		return
	}
	var hash [FullHashLen]byte
	copy(hash[:], data[:FullHashLen])
	rs := l.res()
	rs.mu.Lock()
	r := rs.outgoing[hash]
	rs.mu.Unlock()
	if r == nil {
		return
	}
	if !bytes.Equal(data[FullHashLen:], r.expectedProof[:]) {
		return
	}
	r.mu.Lock()
	r.state = 3
	r.mu.Unlock()
	rs.mu.Lock()
	delete(rs.outgoing, r.Hash)
	rs.mu.Unlock()
	l.mu.Lock()
	l.lastProof = m.n.now()
	l.mu.Unlock()
	select {
	case r.done <- ResourceResult{Delivered: true}:
	default:
	}
}

// handleResourceAdv accepts an advertisement (decrypted) when the link
// allows resources, then requests the first window.
func (m *LinkManager) handleResourceAdv(l *Link, plain []byte) {
	v, _, err := msgpack.UnpackValue(plain)
	if err != nil || v.Kind != msgpack.KindMap {
		return
	}
	get := func(k string) msgpack.Value {
		for _, kv := range v.Map {
			if kv.Key.Kind == msgpack.KindStr && kv.Key.Str == k {
				return kv.Val
			}
		}
		return msgpack.Value{}
	}
	t, _ := get("t").AsInt()
	d, _ := get("d").AsInt()
	n, _ := get("n").AsInt()
	f, _ := get("f").AsInt()
	segs, _ := get("l").AsInt()
	h := get("h").Bin
	rb := get("r").Bin
	hm := get("m").Bin
	if len(h) != FullHashLen || len(rb) != resourceRandomHashLen || n <= 0 || t <= 0 || t > 3*resourceMaxSize {
		return
	}
	if segs > 1 || f&0x20 != 0 {
		log.Debug().Msg("rns: multi-segment or metadata resource not supported, rejecting")
		m.rejectResource(l, h)
		return
	}
	if l.AcceptResources == nil || !l.AcceptResources(int(d)) {
		m.rejectResource(l, h)
		return
	}
	sdu := l.mtu - reticulum.HeaderMaxSize - reticulum.IFACMinSize
	r := &IncomingResource{link: l, size: int(t), dataSize: int(d), totalParts: int((t + int64(sdu) - 1) / int64(sdu)),
		compressed: f&0x02 != 0, window: resourceWindow, windowMax: resourceWindowMaxSlow, windowMin: resourceWindowMin,
		consecutive: -1, lastActivity: m.n.now()}
	copy(r.Hash[:], h)
	copy(r.randomHash[:], rb)
	if int(n) != r.totalParts {
		r.totalParts = int(n)
	}
	r.parts = make([][]byte, r.totalParts)
	r.hashmap = make([][resourceMapHashLen]byte, r.totalParts)
	r.hashmapSet = make([]bool, r.totalParts)
	rs := l.res()
	rs.mu.Lock()
	if _, exists := rs.incoming[r.Hash]; exists {
		rs.mu.Unlock()
		return
	}
	rs.incoming[r.Hash] = r
	rs.mu.Unlock()
	log.Debug().Str("link", hexh(l.ID)).Int("size", r.size).Int("parts", r.totalParts).Msg("rns: accepting resource")
	r.hashmapUpdate(0, hm)
	m.requestNext(r)
}

func (m *LinkManager) rejectResource(l *Link, hash []byte) {
	if ct, err := l.Encrypt(hash); err == nil {
		l.mu.Lock()
		m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextResourceRCL, ct)
		l.mu.Unlock()
	}
}

func (r *IncomingResource) hashmapUpdate(segment int, hm []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := len(hm) / resourceMapHashLen
	for i := 0; i < count; i++ {
		idx := i + segment*resourceHashmapMaxLen
		if idx >= r.totalParts {
			break
		}
		if !r.hashmapSet[idx] {
			r.hashHeight++
		}
		copy(r.hashmap[idx][:], hm[i*resourceMapHashLen:(i+1)*resourceMapHashLen])
		r.hashmapSet[idx] = true
	}
	r.waitingHMU = false
}

// requestNext asks for the next window of parts (Resource.request_next).
func (m *LinkManager) requestNext(r *IncomingResource) {
	r.mu.Lock()
	if r.state >= 1 || r.waitingHMU {
		r.mu.Unlock()
		return
	}
	r.outstanding = 0
	exhausted := false
	var req []byte
	pn := r.consecutive + 1
	i := 0
	for ; pn < r.totalParts && i < r.window; pn++ {
		if r.parts[pn] != nil {
			continue
		}
		if !r.hashmapSet[pn] {
			exhausted = true
			break
		}
		req = append(req, r.hashmap[pn][:]...)
		r.outstanding++
		i++
	}
	data := []byte{resourceHashmapNotExh}
	if exhausted {
		data = []byte{resourceHashmapExh}
		data = append(data, r.hashmap[r.hashHeight-1][:]...)
		r.waitingHMU = true
	}
	data = append(data, r.Hash[:]...)
	data = append(data, req...)
	r.lastActivity = m.n.now()
	r.reqSent = r.lastActivity
	l := r.link
	r.mu.Unlock()
	ct, err := l.Encrypt(data)
	if err != nil {
		return
	}
	l.mu.Lock()
	m.sendLinkPacket(l, reticulum.PacketData, reticulum.ContextResourceReq, ct)
	l.mu.Unlock()
}

// receivePart stores a RESOURCE part.
func (m *LinkManager) receivePart(l *Link, part []byte) {
	rs := l.res()
	rs.mu.Lock()
	var cands []*IncomingResource
	for _, r := range rs.incoming {
		cands = append(cands, r)
	}
	rs.mu.Unlock()
	for _, r := range cands {
		r.mu.Lock()
		if r.state >= 1 {
			r.mu.Unlock()
			continue
		}
		mh := mapHash(part, r.randomHash)
		start := r.consecutive + 1
		matched := false
		for idx := start; idx < start+r.window && idx < r.totalParts; idx++ {
			if r.hashmapSet[idx] && r.hashmap[idx] == mh && r.parts[idx] == nil {
				r.parts[idx] = append([]byte(nil), part...)
				r.received++
				r.outstanding--
				for cp := r.consecutive + 1; cp < r.totalParts && r.parts[cp] != nil; cp++ {
					r.consecutive = cp
				}
				matched = true
				break
			}
		}
		if !matched {
			r.mu.Unlock()
			continue
		}
		r.lastActivity = m.n.now()
		r.retries = 0
		if r.received == r.totalParts {
			r.state = 1
			r.mu.Unlock()
			m.assemble(r)
			return
		}
		if r.outstanding <= 0 {
			if r.window < r.windowMax {
				r.window++
				if r.window-r.windowMin > resourceWindowFlex-1 {
					r.windowMin++
				}
			}
			r.mu.Unlock()
			m.requestNext(r)
			return
		}
		r.mu.Unlock()
		return
	}
}

func (m *LinkManager) assemble(r *IncomingResource) {
	stream := bytes.Join(r.parts, nil)
	plain, err := r.link.Decrypt(stream)
	if err != nil || len(plain) < resourceRandomHashLen {
		m.concludeIncoming(r, nil, errors.New("resource decrypt failed"))
		return
	}
	data := plain[resourceRandomHashLen:]
	if r.compressed {
		dec, err := io.ReadAll(io.LimitReader(bzip2.NewReader(bytes.NewReader(data)), 64*1024*1024))
		if err != nil {
			m.concludeIncoming(r, nil, errors.New("resource decompress failed"))
			return
		}
		data = dec
	}
	calc := sha256.Sum256(append(append([]byte(nil), data...), r.randomHash[:]...))
	if calc != r.Hash {
		m.concludeIncoming(r, nil, errors.New("resource hash mismatch"))
		return
	}
	proof := sha256.Sum256(append(append([]byte(nil), data...), r.Hash[:]...))
	pd := append(append([]byte(nil), r.Hash[:]...), proof[:]...)
	r.link.mu.Lock()
	m.sendLinkPacket(r.link, reticulum.PacketProof, reticulum.ContextResourcePRF, pd)
	r.link.mu.Unlock()
	m.concludeIncoming(r, data, nil)
}

func (m *LinkManager) concludeIncoming(r *IncomingResource, data []byte, err error) {
	r.mu.Lock()
	if err == nil {
		r.state = 2
		r.Data = data
	} else {
		r.state = 3
	}
	r.mu.Unlock()
	rs := r.link.res()
	rs.mu.Lock()
	delete(rs.incoming, r.Hash)
	rs.mu.Unlock()
	if err != nil {
		log.Debug().Err(err).Str("link", hexh(r.link.ID)).Msg("rns: incoming resource failed")
		return
	}
	log.Debug().Str("link", hexh(r.link.ID)).Int("bytes", len(data)).Msg("rns: resource received")
	if r.link.OnResource != nil {
		r.link.OnResource(r.link, data)
	}
	if r.link.local != nil && r.link.local.OnLinkResource != nil {
		r.link.local.OnLinkResource(r.link, data)
	}
}

// resourceWatchdog retries requests and advertisements and times transfers out.
func (m *LinkManager) resourceWatchdog(now time.Time) {
	for _, l := range m.All() {
		rs := l.res()
		rs.mu.Lock()
		var inc []*IncomingResource
		var out []*OutgoingResource
		for _, r := range rs.incoming {
			inc = append(inc, r)
		}
		for _, r := range rs.outgoing {
			out = append(out, r)
		}
		rs.mu.Unlock()
		rtt := l.RTT()
		if rtt < 100*time.Millisecond {
			rtt = 100 * time.Millisecond
		}
		for _, r := range inc {
			r.mu.Lock()
			wait := time.Duration(4)*rtt + resourceRetryGrace + time.Duration(r.retries)*resourcePerRetryDelay
			if wait < 2*time.Second {
				wait = 2 * time.Second
			}
			if r.state == 0 && now.After(r.lastActivity.Add(wait)) {
				if r.retries >= resourceMaxRetries {
					r.mu.Unlock()
					m.concludeIncoming(r, nil, errors.New("timed out waiting for parts"))
					continue
				}
				r.retries++
				if r.window > r.windowMin {
					r.window--
				}
				r.waitingHMU = false
				r.lastActivity = now
				r.mu.Unlock()
				m.requestNext(r)
				continue
			}
			r.mu.Unlock()
		}
		for _, r := range out {
			r.mu.Lock()
			switch r.state {
			case 0:
				timeout := time.Duration(6)*rtt + resourceProcessingGrac
				if now.After(r.advSent.Add(timeout)) {
					if r.advRetries >= resourceMaxAdvRetries {
						r.mu.Unlock()
						r.fail(errors.New("no part requests after advertisement"))
						continue
					}
					r.advRetries++
					r.mu.Unlock()
					m.advertise(r)
					continue
				}
			case 1, 2:
				maxWait := 6*rtt*resourceMaxRetries + resourceSenderGrace
				if now.After(r.lastActivity.Add(maxWait)) {
					r.mu.Unlock()
					r.fail(errors.New("timed out waiting for the receiver"))
					continue
				}
			}
			r.mu.Unlock()
		}
	}
}
