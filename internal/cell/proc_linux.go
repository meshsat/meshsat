//go:build linux

package cell

import (
	"os/exec"
	"syscall"
)

// bindToParent makes the kernel send the daemon SIGTERM when the Bridge
// dies, whatever killed it: a radio keyed up by a process that is gone is
// an unattended emission, and a stale core would hold the ports the next
// Bridge needs. [MESHSAT-1578]
func bindToParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
