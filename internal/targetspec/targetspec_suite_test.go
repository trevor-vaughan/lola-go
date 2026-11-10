package targetspec_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTargetspec(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "targetspec")
}
