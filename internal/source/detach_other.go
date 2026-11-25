//go:build !unix

package source

import "os/exec"

// detach does nothing off Unix, which has no session to leave; an ssh there may still prompt on
// the console.
func detach(*exec.Cmd) {}
