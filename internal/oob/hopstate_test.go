package oob

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"meshsat/internal/hopwire"
)

// Media hopping part two over STATUS-NET. [MESHSAT-1650]

var ourLinks = []hopwire.Link{
	{Iface: "aprs_0", State: hopwire.Admitted}, {Iface: "cellular_0", State: hopwire.Admitted},
	{Iface: "iridium_imt_0", State: hopwire.Recovering}, {Iface: "mesh_0", State: hopwire.Excluded},
	{Iface: "tetra_0", State: hopwire.Suspect}, {Iface: "zigbee_0", State: hopwire.Down},
}

type peerNotes struct {
	mu    sync.Mutex
	notes []string
	links [][]hopwire.Link
}

func (p *peerNotes) note(peerID uint16, alias, bearer, via string, r hopwire.Report) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notes = append(p.notes, alias+" "+bearer+" "+via)
	p.links = append(p.links, r.Links)
}

func (p *peerNotes) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.notes)
}

func withHop(h *harness, on bool) *peerNotes {
	notes := &peerNotes{}
	h.svc.d.HopState = func(budget int) string {
		if !on {
			return ""
		}
		return hopwire.Encode(ourLinks, budget)
	}
	h.svc.d.HopPeer = notes.note
	return notes
}

// replyBody puts our STATUS-NET reply back together as the peer reads it.
func replyBody(t *testing.T, h *harness, sends []sentItem) string {
	t.Helper()
	body := ""
	for _, s := range sends {
		_, ra := h.openReply(t, s.text)
		body += string(ra.Body)
	}
	return body
}

// status_net_without_args_as_before: a STATUS-NET without an argument is
// answered exactly as before part two while media hopping is off, and with
// no hop dependency at all; nothing is noted.
func TestStatusNetWithoutArgsAsBefore(t *testing.T) {
	for _, set := range []bool{false, true} {
		h := newHarness(t, RoleReadonly, false)
		var notes *peerNotes
		if set {
			notes = withHop(h, false)
		}
		res := h.svc.Execute(context.Background(), Origin{PeerID: h.peer.PeerID, Alias: "parallax", Role: RoleReadonly, Bearer: "cellular_0"}, CmdStatusNet, nil)
		if res.Code != RCOK || res.Body != "wlan0:up agent:none" {
			t.Fatalf("hop deps %v: %s %q", set, res.Code, res.Body)
		}
		h.svc.HandleInbound(context.Background(), "cellular_0", "+31653207829", h.peerFrame(t, 1, CmdStatusNet, nil, true, false))
		waitFor(t, func() bool { return len(h.sends()) == 1 })
		if b := replyBody(t, h, h.sends()); b != "wlan0:up agent:none" {
			t.Fatalf("hop deps %v: reply %q", set, b)
		}
		if notes != nil && notes.count() != 0 {
			t.Fatalf("a plain STATUS-NET noted a peer state")
		}
	}
}

// status_net_request_carries_hop_state: a request's argument is the peer's
// hop state, noted as such; the reply carries ours after the network status,
// whole or marked cut, within the four chunks the bearer allows.
func TestStatusNetRequestCarriesHopState(t *testing.T) {
	for _, tc := range []struct{ bearer, addr string }{{"aprs_0", "PD0XYZ-7"}, {"cellular_0", "+31653207829"}} {
		h := newHarness(t, RoleReadonly, false)
		notes := withHop(h, true)
		h.svc.HandleInbound(context.Background(), tc.bearer, tc.addr, h.peerFrame(t, 1, CmdStatusNet, []byte("H1:m0Xa0A"), false, false))
		waitFor(t, func() bool { return notes.count() == 1 && len(h.sends()) > 0 })
		time.Sleep(50 * time.Millisecond)
		if notes.notes[0] != "parallax "+tc.bearer+" request" || len(notes.links[0]) != 2 || notes.links[0][0].Iface != "mesh_0" || notes.links[0][0].State != hopwire.Excluded {
			t.Fatalf("%s: noted %v %v", tc.bearer, notes.notes, notes.links)
		}
		sends := h.sends()
		if len(sends) > MaxReplyChunks {
			t.Fatalf("%s: %d chunks", tc.bearer, len(sends))
		}
		for _, s := range sends {
			if _, ra := h.openReply(t, s.text); len(ra.Body) > BearerBudget(tc.bearer)-ReplyHeaderLen {
				t.Fatalf("%s: chunk body %d bytes", tc.bearer, len(ra.Body))
			}
		}
		body := replyBody(t, h, sends)
		if !strings.HasPrefix(body, "wlan0:up agent:none H1:") {
			t.Fatalf("%s: body %q", tc.bearer, body)
		}
		form, _ := hopwire.Find(body)
		r, err := hopwire.Parse(form)
		if err != nil || len(r.Links) == 0 {
			t.Fatalf("%s: form %q: %v", tc.bearer, form, err)
		}
		if full := hopwire.Encode(ourLinks, 1000); tc.bearer == "cellular_0" && (form != full || r.Cut) {
			t.Fatalf("SMS has room for the whole form: %q, want %q", form, full)
		}
		if tc.bearer == "aprs_0" && !r.Cut {
			t.Fatalf("APRS cannot carry six links in 40 bytes with the status, yet %q is not cut", form)
		}
	}
}

// status_net_bad_argument_refused: an argument that is not a hop form is
// refused, and nothing is noted.
func TestStatusNetBadArgumentRefused(t *testing.T) {
	h := newHarness(t, RoleReadonly, false)
	notes := withHop(h, true)
	for _, arg := range []string{"x", "H2:m0A", "H1:m0Q"} {
		res := h.svc.Execute(context.Background(), Origin{PeerID: h.peer.PeerID, Role: RoleReadonly, Bearer: "aprs_0"}, CmdStatusNet, []byte(arg))
		if res.Code != RCBadArgs {
			t.Errorf("%q: %s", arg, res.Code)
		}
	}
	if notes.count() != 0 {
		t.Fatalf("noted %v", notes.notes)
	}
	big := make([]byte, MaxArgs-ExpiryLen+1)
	if res := h.svc.Execute(context.Background(), Origin{PeerID: h.peer.PeerID, Role: RoleReadonly, Bearer: "mesh_0"}, CmdStatusNet, big); res.Code != RCBadArgs {
		t.Errorf("oversize argument: %s", res.Code)
	}
}

// peerReply seals a reply as the remote peer.
func (h *harness) peerReply(t *testing.T, counter uint32, cmd byte, ra ReplyArgs) string {
	t.Helper()
	wire, err := Seal(Frame{Reply: true, PeerID: h.peer.PeerID, Counter: counter, Cmd: cmd, Args: EncodeReplyArgs(ra)}, h.key, Role(h.peer.LocalRole).Other())
	if err != nil {
		t.Fatal(err)
	}
	return Encode(wire)
}

// status_net_reply_reassembled: a peer's reply in chunks, in any order, is
// noted once, when the last chunk is in; a reply missing a chunk, a refused
// reply and another command's reply are not.
func TestStatusNetReplyReassembled(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	notes := withHop(h, true)
	ctx := context.Background()
	body := "wlan0:up agent:none H1:a0Am0Xt0S"
	parts := []string{body[:10], body[10:20], body[20:]}
	in := func(counter uint32, cmd byte, rc ResultCode, req uint16, seq, total byte, b string) {
		h.svc.HandleInbound(ctx, "aprs_0", "PD0XYZ-7", h.peerReply(t, counter, cmd, ReplyArgs{RC: rc, ReqCounterLo: req, Seq: seq, Total: total, Body: []byte(b)}))
	}
	in(1, CmdStatusNet, RCOK, 40, 2, 3, parts[1])
	in(2, CmdStatusNet, RCOK, 40, 1, 3, parts[0])
	if notes.count() != 0 {
		t.Fatal("noted before the last chunk")
	}
	in(3, CmdStatusNet, RCOK, 40, 3, 3, parts[2])
	if notes.count() != 1 || notes.notes[0] != "parallax aprs_0 reply" || len(notes.links[0]) != 3 {
		t.Fatalf("noted %v %v", notes.notes, notes.links)
	}
	in(4, CmdStatusNet, RCOK, 41, 1, 2, parts[0]) // its second chunk never comes
	in(5, CmdStatusNet, RCRefused, 42, 1, 1, "H1:m0X")
	in(6, CmdPing, RCOK, 43, 1, 1, "u3h H1:m0X")
	in(7, CmdStatusNet, RCOK, 44, 1, 1, "wlan0:up agent:none H1:m0Q")
	if notes.count() != 1 {
		t.Fatalf("noted %v", notes.notes)
	}
}

// share_hop_state_free_bearers_and_rate: our state goes to a peer on a free
// bearer that is up, never a paid one, at most once per HopShareEvery, and
// is audited; with media hopping off or no free bearer up nothing goes.
func TestShareHopStateFreeBearersAndRate(t *testing.T) {
	h := newHarness(t, RoleControl, false)
	withHop(h, true)
	now := time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)
	h.svc.d.Now = func() time.Time { return now }
	h.svc.d.FreeBearer = func(id string) bool { return id == "aprs_0" || id == "mesh_0" }
	ctx := context.Background()

	res := h.svc.ShareHopState(ctx)
	if len(res) != 1 || !res[0].Sent || res[0].Bearer != "aprs_0" {
		t.Fatalf("share %+v", res)
	}
	sends := h.sends()
	if len(sends) != 1 || sends[0].iface != "aprs_0" || sends[0].addr != "PD0XYZ-7" {
		t.Fatalf("sends %+v", sends)
	}
	wire, _ := ExtractFrame(sends[0].text)
	f, err := Open(wire, h.key, Role(h.peer.LocalRole))
	if err != nil || f.Reply || f.Cmd != CmdStatusNet || len(f.Args) > BearerBudget("aprs_0")-ExpiryLen {
		t.Fatalf("request %+v %v", f, err)
	}
	if _, err := hopwire.Parse(string(f.Args)); err != nil {
		t.Fatalf("argument %q: %v", f.Args, err)
	}
	h.mu.Lock()
	audited := contains(h.audits, "oob_hop_share")
	h.mu.Unlock()
	if !audited {
		t.Fatal("not audited")
	}

	now = now.Add(HopShareEvery - time.Second)
	if res := h.svc.ShareHopState(ctx); len(res) != 1 || res[0].Sent || res[0].Skipped != "recent" || len(h.sends()) != 1 {
		t.Fatalf("within the rate: %+v, %d sends", res, len(h.sends()))
	}
	now = now.Add(time.Second)
	if res := h.svc.ShareHopState(ctx); len(res) != 1 || !res[0].Sent || len(h.sends()) != 2 {
		t.Fatalf("after the rate: %+v", res)
	}

	// Only a paid bearer up: nothing goes.
	now = now.Add(HopShareEvery)
	h.up["aprs_0"] = false
	if res := h.svc.ShareHopState(ctx); len(res) != 1 || res[0].Skipped != "no_free_bearer" || len(h.sends()) != 2 {
		t.Fatalf("paid only: %+v, %d sends", res, len(h.sends()))
	}
	h.up["aprs_0"] = true

	// Media hopping off: nothing at all.
	withHop(h, false)
	if res := h.svc.ShareHopState(ctx); res != nil || len(h.sends()) != 2 {
		t.Fatalf("off: %+v, %d sends", res, len(h.sends()))
	}
	// No free-bearer check wired: nothing either.
	withHop(h, true)
	h.svc.d.FreeBearer = nil
	if res := h.svc.ShareHopState(ctx); res != nil || len(h.sends()) != 2 {
		t.Fatalf("no free-bearer check: %+v", res)
	}
}
