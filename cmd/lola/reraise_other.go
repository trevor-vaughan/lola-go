//go:build !unix

package main

import "syscall"

// reraise does nothing where a process cannot send itself a signal (Windows): main exits with 128
// plus its number.
func reraise(syscall.Signal) {}
