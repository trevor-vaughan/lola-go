package consent_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConsent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "consent")
}
