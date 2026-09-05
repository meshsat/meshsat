package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

// Every descriptor the bridge holds on a serial port, a USB device node or a
// GPIO chip must carry FD_CLOEXEC. Go's os/exec closes nothing by itself: a
// child (rtl_power_fftw, direwolf, jspr_helper.py, btmgmt) inherits every
// descriptor that lacks the flag and keeps the device open long after the
// bridge has closed and reopened it, which defeats the reopen and reset paths
// and pins deleted device nodes. Descriptors opened here through unix.Open
// carry unix.O_CLOEXEC at open time; ports opened through go.bug.st/serial,
// which opens without the flag, are marked right after the open with
// cloexecSerial. [MESHSAT-807]

// markCloexecByPath sets FD_CLOEXEC on every descriptor of this process that
// refers to path and returns how many descriptors refer to it.
func markCloexecByPath(path string) (int, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, fmt.Errorf("list descriptors: %w", err)
	}
	n := 0
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		link, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err != nil || (link != resolved && link != path) {
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			continue
		}
		if flags&unix.FD_CLOEXEC == 0 {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
				return n, fmt.Errorf("fd %d: %w", fd, err)
			}
		}
		n++
	}
	return n, nil
}

// cloexecSerial marks a port that was just opened through go.bug.st/serial
// close-on-exec. Best effort: a failure is logged, never fatal.
func cloexecSerial(path string) {
	if _, err := markCloexecByPath(path); err != nil {
		log.Debug().Err(err).Str("port", path).Msg("serial: could not mark close-on-exec")
	}
}

// fdCloexec reports whether fd carries FD_CLOEXEC.
func fdCloexec(fd int) (bool, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return false, err
	}
	return flags&unix.FD_CLOEXEC != 0, nil
}
