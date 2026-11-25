//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a new session, which has no controlling terminal: a text browser that
// xdg-open falls back to cannot open /dev/tty.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
