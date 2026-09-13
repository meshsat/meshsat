package transport

import (
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

func noMeshPacing(t *testing.T) {
	t.Helper()
	gap, dwell := meshWriteGap, meshDisconnectDwell
	meshWriteGap, meshDisconnectDwell = 0, 0
	t.Cleanup(func() { meshWriteGap, meshDisconnectDwell = gap, dwell })
}

// Close tells the radio the client is leaving before it closes the port, as
// the official client does. [MESHSAT-850]
func TestClose_SendsDisconnectFirst(t *testing.T) {
	noMeshPacing(t)
	fp := &fakeWritePort{}
	tr := NewDirectMeshTransport("/dev/null")
	tr.file = fp
	tr.connected = true

	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if fp.count() != 1 {
		t.Fatalf("%d writes, want the disconnect frame", fp.count())
	}
	w := fp.writes[0]
	if len(w) < 4 || w[0] != meshStart1 || w[1] != meshStart2 || int(w[2])<<8|int(w[3]) != len(w)-4 {
		t.Fatalf("not a Meshtastic frame: % x", w)
	}
	var msg pb.ToRadio
	if err := proto.Unmarshal(w[4:], &msg); err != nil {
		t.Fatal(err)
	}
	if !msg.GetDisconnect() {
		t.Fatalf("frame is not ToRadio.disconnect: %v", &msg)
	}
	if tr.file != nil || tr.connected {
		t.Fatal("port left open")
	}
}

func TestClose_WithoutASessionWritesNothing(t *testing.T) {
	noMeshPacing(t)
	fp := &fakeWritePort{}
	tr := NewDirectMeshTransport("/dev/null")
	tr.file = fp // opened, no session yet

	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if fp.count() != 0 {
		t.Fatalf("%d writes to a port with no session", fp.count())
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestSendFrame_PacesWritesLikeTheOfficialClient(t *testing.T) {
	gap := meshWriteGap
	meshWriteGap = 40 * time.Millisecond
	t.Cleanup(func() { meshWriteGap = gap })

	fp := &fakeWritePort{}
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := sendFrame(fp, []byte{0x08, 0x01}); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 120*time.Millisecond {
		t.Fatalf("4 frames in %s, want at least three 40 ms gaps", d)
	}
	if fp.count() != 4 {
		t.Fatalf("%d writes, want 4", fp.count())
	}
}
