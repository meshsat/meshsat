package transport

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	serial "go.bug.st/serial"
	"golang.org/x/sys/unix"
)

func TestMarkCloexecByPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "port")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if on, _ := fdCloexec(fd); on {
		t.Fatal("precondition: a bare unix.Open must not carry FD_CLOEXEC")
	}
	fd2, err := unix.Open(path, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd2)

	n, err := markCloexecByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("marked %d descriptors, want 2", n)
	}
	for _, f := range []int{fd, fd2} {
		if on, err := fdCloexec(f); err != nil || !on {
			t.Fatalf("fd %d: cloexec=%v err=%v", f, on, err)
		}
	}
	if n, err := markCloexecByPath(path); err != nil || n != 2 {
		t.Fatalf("second pass must be idempotent: n=%d err=%v", n, err)
	}
	if n, err := markCloexecByPath(filepath.Join(t.TempDir(), "absent")); err != nil || n != 0 {
		t.Fatalf("unrelated path: n=%d err=%v", n, err)
	}
}

// openPTY hands out a pseudo-terminal so the serial open paths can be
// exercised without hardware. Skips where the sandbox has no /dev/ptmx.
func openPTY(t *testing.T) (master int, slave string) {
	t.Helper()
	m, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	if err := unix.IoctlSetPointerInt(m, unix.TIOCSPTLCK, 0); err != nil {
		unix.Close(m)
		t.Skipf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(m, unix.TIOCGPTN)
	if err != nil {
		unix.Close(m)
		t.Skipf("ptsname: %v", err)
	}
	return m, "/dev/pts/" + strconv.Itoa(n)
}

// cloexecStateByPath counts this process's descriptors on path with and
// without FD_CLOEXEC.
func cloexecStateByPath(t *testing.T, path string) (set, unset int) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if link, err := os.Readlink("/proc/self/fd/" + e.Name()); err != nil || link != path {
			continue
		}
		on, err := fdCloexec(fd)
		if err != nil {
			continue
		}
		if on {
			set++
		} else {
			unset++
		}
	}
	return set, unset
}

func TestOpenRawSerialCloexec(t *testing.T) {
	m, slave := openPTY(t)
	defer unix.Close(m)
	p, err := openRawSerial(slave, 9600)
	if err != nil {
		t.Fatalf("openRawSerial on %s: %v", slave, err)
	}
	defer p.Close()
	if on, err := fdCloexec(p.fd); err != nil || !on {
		t.Fatalf("raw serial descriptor: cloexec=%v err=%v", on, err)
	}
}

func TestCloexecSerialBugst(t *testing.T) {
	m, slave := openPTY(t)
	defer unix.Close(m)
	port, err := serial.Open(slave, &serial.Mode{BaudRate: 9600})
	if err != nil {
		t.Fatalf("serial.Open on %s: %v", slave, err)
	}
	defer port.Close()
	if set, unset := cloexecStateByPath(t, slave); set+unset == 0 {
		t.Fatalf("no descriptor on %s after serial.Open", slave)
	}
	cloexecSerial(slave)
	if set, unset := cloexecStateByPath(t, slave); unset != 0 || set == 0 {
		t.Fatalf("after cloexecSerial: set=%d unset=%d", set, unset)
	}
}
