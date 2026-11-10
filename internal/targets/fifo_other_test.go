//go:build !unix

package targets_test

import . "github.com/onsi/ginkgo/v2"

func mkfifo(string) {
	Skip("FIFOs need a Unix system")
}
