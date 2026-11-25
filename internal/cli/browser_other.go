//go:build !unix

package cli

import "os/exec"

// detach does nothing here: on Windows the launcher is rundll32, which opens the default browser
// as a graphical program and has no terminal fallback, so no process-creation flag is set.
func detach(*exec.Cmd) {}
