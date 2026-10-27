//go:build unix

package engine_test

import (
	"net"
	"syscall"

	. "github.com/onsi/gomega"
)

func mkfifo(path string) {
	Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
}

// mksocket leaves a Unix socket file at path.
func mksocket(path string) {
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	Expect(err).NotTo(HaveOccurred())
	l.SetUnlinkOnClose(false)
	Expect(l.Close()).To(Succeed())
}
