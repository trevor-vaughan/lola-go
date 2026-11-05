package untrusted_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestUntrusted(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "untrusted")
}
