package opencode_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestOpenCode(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "opencode")
}
