package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// openPTYSlave opens a pseudo-terminal pair and returns the slave's path and
// a descriptor on it; the test is skipped where /dev/ptmx is unavailable.
func openPTYSlave(t *testing.T) (string, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("ptsname: %v", err)
	}
	path := fmt.Sprintf("/dev/pts/%d", n)
	slave, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open %s: %v", path, err)
	}
	t.Cleanup(func() { slave.Close() })
	return path, slave
}

func hupcl(t *testing.T, f *os.File) bool {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS2)
	if err != nil {
		t.Fatal(err)
	}
	return tio.Cflag&unix.HUPCL != 0
}

// A close with HUPCL set drops DTR, and the radio's TinyUSB stack can stall
// when the next open raises it again. [MESHSAT-850]
func TestKeepLinesOnClose_ClearsHUPCL(t *testing.T) {
	path, slave := openPTYSlave(t)
	tio, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS2)
	if err != nil {
		t.Fatal(err)
	}
	tio.Cflag |= unix.HUPCL
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS2, tio); err != nil {
		t.Fatal(err)
	}
	if !hupcl(t, slave) {
		t.Fatal("positive control: HUPCL not set before the call")
	}

	if err := keepLinesOnClose(path); err != nil {
		t.Fatal(err)
	}
	if hupcl(t, slave) {
		t.Fatal("HUPCL still set: closing the port would drop DTR")
	}
	if err := keepLinesOnClose(path); err != nil {
		t.Fatalf("second call on a cleared port: %v", err)
	}
}

func TestKeepLinesOnClose_NoDescriptor(t *testing.T) {
	if err := keepLinesOnClose(filepath.Join(t.TempDir(), "ttyACM9")); err == nil {
		t.Fatal("no error for a port this process does not hold")
	}
}
