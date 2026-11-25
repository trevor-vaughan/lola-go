//go:build unix

package source

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detach starts cmd in a session of its own, which has no controlling terminal: an ssh that git
// starts cannot open /dev/tty to ask for a passphrase or about a host key, and fails instead of
// prompting over (and reading the keys of) whatever holds the terminal. Cancelling cmd kills its
// whole process group, so that ssh dies with git rather than outliving it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return os.ErrProcessDone
	}
}
