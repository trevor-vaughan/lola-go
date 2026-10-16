//go:build !unix

package migrate_test

import . "github.com/onsi/ginkgo/v2"

func mkfifo(string) {
	Skip("FIFOs need a Unix system")
}
