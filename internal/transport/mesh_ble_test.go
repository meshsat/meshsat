package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeGatt is a node's GATT service in memory: FromRadio hands out queued
// payloads then empties, FromNum rings when something is queued.
type fakeGatt struct {
	mu      sync.Mutex
	queue   [][]byte
	writes  [][]byte
	notif   chan struct{}
	lost    chan struct{}
	readErr error
}

func newFakeGatt() *fakeGatt {
	return &fakeGatt{notif: make(chan struct{}, 1), lost: make(chan struct{})}
}

func (f *fakeGatt) ReadFromRadio(ctx context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	if len(f.queue) == 0 {
		return nil, nil
	}
	data := f.queue[0]
	f.queue = f.queue[1:]
	return data, nil
}

func (f *fakeGatt) WriteToRadio(ctx context.Context, payload []byte) error {
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), payload...))
	f.mu.Unlock()
	return nil
}

func (f *fakeGatt) Notified() <-chan struct{} { return f.notif }
func (f *fakeGatt) Lost() <-chan struct{}     { return f.lost }

func (f *fakeGatt) arrive(payloads ...[]byte) {
	f.mu.Lock()
	f.queue = append(f.queue, payloads...)
	f.mu.Unlock()
	select {
	case f.notif <- struct{}{}:
	default:
	}
}

func (f *fakeGatt) written() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.writes...)
}

func readFrames(t *testing.T, s *bleMeshStream, want int) [][]byte {
	t.Helper()
	reader := &meshFrameReader{port: s}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var frames [][]byte
	for len(frames) < want {
		frame, err := reader.readFrame(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", len(frames), err)
		}
		frames = append(frames, frame)
	}
	return frames
}

// A ToRadio goes out as the bare protobuf, and FromRadio payloads come back
// framed as the serial link frames them, so the frame reader is unchanged.
// [MESHSAT-1390]
func TestBLEMeshStream_FramesReadsAndUnframesWrites(t *testing.T) {
	noMeshPacing(t)
	gatt := newFakeGatt()
	s := newBLEMeshStream(gatt)
	defer s.Close()

	// The wake burst means nothing on a GATT link.
	if err := wakeDevice(s); err != nil {
		t.Fatal(err)
	}
	if n := len(gatt.written()); n != 0 {
		t.Fatalf("the wake burst reached ToRadio (%d writes)", n)
	}
	if err := sendFrame(s, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	if w := gatt.written(); len(w) != 1 || string(w[0]) != "\x01\x02\x03" {
		t.Fatalf("ToRadio got %x", w)
	}

	gatt.arrive([]byte("hello"), []byte("world"))
	frames := readFrames(t, s, 2)
	if string(frames[0]) != "hello" || string(frames[1]) != "world" {
		t.Fatalf("frames %q", frames)
	}
}

func TestBLEMeshStream_IdleReadsPauseAndTheDoorbellWakes(t *testing.T) {
	noMeshPacing(t)
	gatt := newFakeGatt()
	s := newBLEMeshStream(gatt)
	defer s.Close()
	if err := sendFrame(s, []byte{0x08, 0x01}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	start := time.Now()
	n, err := s.Read(buf)
	if n != 0 || err != nil {
		t.Fatalf("idle read: n=%d err=%v", n, err)
	}
	if since := time.Since(start); since < meshReadTimeout/2 || since > 2*time.Second {
		t.Fatalf("idle read paused %s, want about %s", since, meshReadTimeout)
	}
	gatt.arrive([]byte("late"))
	if frames := readFrames(t, s, 1); string(frames[0]) != "late" {
		t.Fatalf("frame %q", frames[0])
	}
}

func TestBLEMeshStream_LostLinkIsAnError(t *testing.T) {
	noMeshPacing(t)
	gatt := newFakeGatt()
	s := newBLEMeshStream(gatt)
	defer s.Close()
	if err := sendFrame(s, []byte{0x08, 0x01}); err != nil {
		t.Fatal(err)
	}
	close(gatt.lost)
	buf := make([]byte, 64)
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := s.Read(buf)
		if errors.Is(err, errBLELinkLost) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("read after the link was lost: %v, want errBLELinkLost", err)
		}
	}
	if _, err := s.Write([]byte{meshStart1, meshStart2, 0, 1, 0x08}); !errors.Is(err, errBLELinkLost) {
		t.Fatalf("write after the link was lost: %v", err)
	}
}

func TestBLEMeshStream_ReadErrorEndsTheStream(t *testing.T) {
	noMeshPacing(t)
	gatt := newFakeGatt()
	s := newBLEMeshStream(gatt)
	defer s.Close()
	gatt.mu.Lock()
	gatt.readErr = errors.New("org.bluez.Error.Failed: Not connected")
	gatt.mu.Unlock()
	if err := sendFrame(s, []byte{0x08, 0x01}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := s.Read(buf)
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the read error never reached the reader")
		}
	}
}

func TestBLEMeshStream_RejectsUnframedWrites(t *testing.T) {
	noMeshPacing(t)
	s := newBLEMeshStream(newFakeGatt())
	defer s.Close()
	if _, err := s.Write([]byte{0x08, 0x01}); err == nil {
		t.Fatal("an unframed write was accepted")
	}
	if _, err := s.Write([]byte{meshStart1, meshStart2, 0, 5, 0x08}); err == nil {
		t.Fatal("a frame with the wrong length was accepted")
	}
	s.Close()
	s.Close() // twice is fine: the transport closes a session more than once
}

func TestIsMeshBLE(t *testing.T) {
	for port, want := range map[string]bool{"ble": true, "BLE": true, "ble:E0:72:A1:B3:C2:ED": true, "tcp://127.0.0.1:4403": false, "/dev/ttyACM0": false, "supervisor": false, "auto": false, "": false} {
		if got := IsMeshBLE(port); got != want {
			t.Errorf("IsMeshBLE(%q) = %v, want %v", port, got, want)
		}
	}
	if meshTransportName("ble") != "ble" || meshTransportName("ble:E0:72:A1:B3:C2:ED") != "ble" {
		t.Fatal("meshTransportName")
	}
	if got := meshBLEAddress("ble:e0:72:a1:b3:c2:ed"); got != "E0:72:A1:B3:C2:ED" {
		t.Fatalf("meshBLEAddress = %q", got)
	}
	if got := meshBLEAddress("ble"); got != "" {
		t.Fatalf("bare ble names %q", got)
	}
	if got := meshBLEAddress("ble:junk"); got != "" {
		t.Fatalf("junk names %q", got)
	}
}

// The chosen node is remembered by the Bridge across its restarts, and a
// port setting that names an address wins over the memory.
func TestBLELink_RemembersTheChosenNode(t *testing.T) {
	dir := t.TempDir()
	tr := NewDirectMeshTransport("ble")
	if tr.ble == nil {
		t.Fatal("a ble port made no link")
	}
	tr.SetBLEStateDir(dir)
	tr.ble.address, tr.ble.name, tr.ble.loaded = "E0:72:A1:B3:C2:ED", "MSPA_c2ec", true
	tr.ble.save()
	if _, err := os.Stat(filepath.Join(dir, bleNodeFile)); err != nil {
		t.Fatal(err)
	}

	again := NewDirectMeshTransport("ble")
	again.SetBLEStateDir(dir)
	if got := again.ble.Address(); got != "E0:72:A1:B3:C2:ED" {
		t.Fatalf("remembered %q", got)
	}
	st := again.BLEStatus()
	if st.Mode != "idle" || st.Address != "E0:72:A1:B3:C2:ED" || st.Name != "MSPA_c2ec" {
		t.Fatalf("status %+v", st)
	}

	named := NewDirectMeshTransport("ble:11:22:33:44:55:66")
	named.SetBLEStateDir(dir)
	if got := named.ble.Address(); got != "11:22:33:44:55:66" {
		t.Fatalf("the port's address lost to the memory: %q", got)
	}

	serial := NewDirectMeshTransport("/dev/ttyACM0")
	if serial.ble != nil {
		t.Fatal("a serial port made a ble link")
	}
	if st := serial.BLEStatus(); st.Mode != "off" {
		t.Fatalf("serial status %+v", st)
	}
	if err := serial.BLEPair("123456"); !errors.Is(err, ErrMeshNotBLE) {
		t.Fatalf("pair on serial: %v", err)
	}
}

// Without a node chosen, the transport says so instead of dialling anything.
func TestBLELink_StreamWithoutANode(t *testing.T) {
	tr := NewDirectMeshTransport("ble")
	tr.SetBLEStateDir(t.TempDir())
	if _, err := tr.bleStream(); err == nil {
		t.Fatal("a stream with no node chosen")
	}
	if err := tr.BLEConnect(context.Background(), "not-an-address"); err == nil {
		t.Fatal("a bad address was accepted")
	}
}
