//go:build !linux

package cell

import "os/exec"

func bindToParent(cmd *exec.Cmd) {}
