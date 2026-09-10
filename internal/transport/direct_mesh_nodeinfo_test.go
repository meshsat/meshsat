package transport

import (
	"testing"
	"time"
)

// Since the channel split the two kit radios hear each other on keys they
// cannot decrypt. An automatic NodeInfo request across such a packet is a
// directed frame the peer cannot read, and its bridge answers with the same
// request back, so the radios ping-ponged about 30 requests a minute each
// way. The transport never asks across an undecryptable packet, and asks a
// decryptable unnamed node once per nodeInfoRequestInterval, not once per
// packet. [MESHSAT-1000]
func TestHandlePacket_NoNodeInfoRequestAcrossEncryptedPackets(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	port := &fakeWritePort{}
	tr.file = port
	tr.connected = true
	tr.myNodeNum = 1

	for i := 0; i < 5; i++ {
		tr.handlePacket(&ProtoMeshPacket{From: 2, To: 1, Encrypted: []byte{1, 2, 3, 4, 5, 6}})
	}
	if n := port.count(); n != 0 {
		t.Fatalf("%d NodeInfo requests sent across encrypted packets, want 0", n)
	}
	if _, ok := tr.nodes[2]; !ok {
		t.Fatal("encrypted packet did not create the NodeDB entry")
	}
}

func TestHandlePacket_NodeInfoRequestThrottledPerNode(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	port := &fakeWritePort{}
	tr.file = port
	tr.connected = true
	tr.myNodeNum = 1

	pkt := func() *ProtoMeshPacket {
		return &ProtoMeshPacket{From: 3, To: 0xffffffff, Decoded: &ProtoData{PortNum: PortNumTelemetry, Payload: []byte{1}}}
	}
	for i := 0; i < 5; i++ {
		tr.handlePacket(pkt())
	}
	if n := port.count(); n != 1 {
		t.Fatalf("%d NodeInfo requests for one unnamed node in a burst, want 1", n)
	}

	// Once the interval has passed the node is asked again.
	tr.nodesMu.Lock()
	tr.nodeInfoReqAt[3] = time.Now().Add(-nodeInfoRequestInterval - time.Second)
	tr.nodesMu.Unlock()
	tr.handlePacket(pkt())
	if n := port.count(); n != 2 {
		t.Fatalf("%d NodeInfo requests after the interval, want 2", n)
	}

	// A named node is never asked.
	tr.nodesMu.Lock()
	tr.nodes[3].LongName = "T-Deck"
	tr.nodeInfoReqAt[3] = time.Now().Add(-nodeInfoRequestInterval - time.Second)
	tr.nodesMu.Unlock()
	tr.handlePacket(pkt())
	if n := port.count(); n != 2 {
		t.Fatalf("%d NodeInfo requests for a named node, want 2", n)
	}
}
