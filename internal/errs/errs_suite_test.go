package errs_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestErrs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "errs")
}
