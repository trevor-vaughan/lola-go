package yamldoc_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestYamldoc(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "yamldoc")
}
