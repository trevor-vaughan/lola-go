//go:build !unix

package config_test

import . "github.com/onsi/ginkgo/v2"

func mkfifo(string) {
	Skip("FIFOs need a Unix system")
}
