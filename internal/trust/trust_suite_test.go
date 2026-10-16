package trust_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTrust(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "trust")
}
