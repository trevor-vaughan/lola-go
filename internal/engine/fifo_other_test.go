//go:build !unix

package engine_test

import . "github.com/onsi/ginkgo/v2"

func mkfifo(string) {
	Skip("FIFOs need a Unix system")
}

func mksocket(string) {
	Skip("Unix sockets need a Unix system")
}
