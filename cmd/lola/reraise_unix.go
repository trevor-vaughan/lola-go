//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// reraise ends lola by sig with its default effect. It returns only when sig did not end lola, and
// main then exits with 128 plus its number instead.
func reraise(sig syscall.Signal) {
	signal.Reset(sig)
	if syscall.Kill(os.Getpid(), sig) != nil {
		return
	}
	time.Sleep(time.Second) // delivery is asynchronous, but ends lola well within this
}
