//go:build unix

package manifest_test

import (
	"syscall"

	. "github.com/onsi/gomega"
)

func mkfifo(path string) {
	Expect(syscall.Mkfifo(path, 0o600)).To(Succeed())
}
