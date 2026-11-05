package native_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNative(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "native")
}
